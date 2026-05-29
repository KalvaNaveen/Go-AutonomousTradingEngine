package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"bnf_go_engine/agents"
	"bnf_go_engine/backtest"
	"bnf_go_engine/config"
	"bnf_go_engine/core"
	"bnf_go_engine/storage"

	"github.com/gorilla/websocket"
)

type Server struct {
	journal    *core.Journal
	exec       *agents.ExecutionAgent
	scanner    *agents.ScannerAgent
	tickStore  *storage.TickStore
	dailyCache *storage.DailyCache
	startTime  time.Time
}

func NewServer(
	journal *core.Journal,
	exec *agents.ExecutionAgent,
	scanner *agents.ScannerAgent,
	tickStore *storage.TickStore,
	dailyCache *storage.DailyCache,
) *Server {
	return &Server{journal, exec, scanner, tickStore, dailyCache, time.Now()}
}

func (s *Server) Start(addr string) {
	mux := http.NewServeMux()
	handler := corsMiddleware(authMiddleware(mux))

	// /api/health stays unauthenticated so the dashboard can render a login wall
	// before holding a token. All other endpoints require auth when configured.
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/positions", s.handlePositions)
	mux.HandleFunc("/api/trades", s.handleTrades)
	mux.HandleFunc("/api/trades/dates", s.handleTradesDates)
	mux.HandleFunc("/api/summary", s.handleSummary)
	mux.HandleFunc("/api/pnl-summary", s.handlePnlSummary)
	mux.HandleFunc("/api/backtest/run", s.handleBacktestRun)
	mux.HandleFunc("/api/backtest/history", s.handleBacktestHistory)
	mux.HandleFunc("/api/config/apply", s.handleConfigApply)
	mux.HandleFunc("/api/ws/live", s.handleWSLive)

	distDir := filepath.Join(config.BaseDir, "dashboard", "dist")
	if _, err := os.Stat(distDir); err == nil {
		fs := http.FileServer(http.Dir(distDir))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Security-Policy", "default-src * 'unsafe-inline' 'unsafe-eval' data: blob:; script-src * 'unsafe-inline' 'unsafe-eval'; style-src * 'unsafe-inline';")
			path := filepath.Join(distDir, r.URL.Path)
			if _, err := os.Stat(path); os.IsNotExist(err) || r.URL.Path == "/" {
				http.ServeFile(w, r, filepath.Join(distDir, "index.html"))
				return
			}
			fs.ServeHTTP(w, r)
		})
	}

	log.Printf("[API] Starting on %s", addr)
	http.ListenAndServe(addr, handler)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := config.DashboardAllowedOrigin
		if origin == "" {
			origin = "http://127.0.0.1:8085"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authMiddleware enforces a shared-secret token on protected /api/* routes.
// Reads the token from `Authorization: Bearer …` or `?token=…`. /api/health
// and the static dashboard files are exempt so the UI can bootstrap.
func authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if config.DashboardAPIToken == "" {
			next.ServeHTTP(w, r)
			return
		}
		// Public paths
		if !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api/health" {
			next.ServeHTTP(w, r)
			return
		}
		got := ""
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			got = strings.TrimPrefix(h, "Bearer ")
		} else if t := r.URL.Query().Get("token"); t != "" {
			got = t
		}
		want := config.DashboardAPIToken
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func (s *Server) buildHealthData() map[string]interface{} {
	wsConnected := false
	ticksFresh := false
	if s.tickStore != nil {
		ticksFresh = s.tickStore.IsFresh()
		wsConnected = s.tickStore.IsReady()
	}

	return map[string]interface{}{
		"status":       "running",
		"engine":       "Zenith Engine v3.0",
		"paper_mode":   config.PaperMode,
		"ws_connected": wsConnected,
		"ticks_fresh":  ticksFresh,
		"cache_loaded": s.dailyCache.IsLoaded(),
		"timestamp":    config.NowIST().Format(time.RFC3339),
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.buildHealthData())
}

func (s *Server) buildStatusData() map[string]interface{} {
	regime := "UNKNOWN"
	if s.scanner != nil {
		regime = s.scanner.LastRegime
		if regime == "" {
			regime = s.scanner.DetectRegime()
		}
	}

	openPositions := make([]map[string]interface{}, 0)
	s.exec.Mu.RLock()
	for oid, trade := range s.exec.ActiveTrades {
		remainQty := trade.RemainingQty
		if remainQty == 0 {
			remainQty = trade.Qty
		}
		pos := map[string]interface{}{
			"oid":           oid,
			"symbol":        trade.Symbol,
			"strategy":      trade.Strategy,
			"entry_price":   trade.EntryPrice,
			"stop_price":    trade.StopPrice,
			"target":        trade.TargetPrice,
			"qty":           remainQty,
			"is_short":      trade.IsShort,
			"regime":        trade.Regime,
			"entry_time":    trade.EntryTime.Format(time.RFC3339),
			"entry_date":    trade.EntryDate.Format("2006-01-02"),
			"product":       trade.Product,
		}

		if s.tickStore != nil && trade.Token > 0 {
			ltp := s.tickStore.GetLTPIfFresh(trade.Token)
			if ltp > 0 {
				var pnl float64
				if trade.IsShort {
					pnl = (trade.EntryPrice - ltp) * float64(remainQty)
				} else {
					pnl = (ltp - trade.EntryPrice) * float64(remainQty)
				}
				pnlPct := 0.0
				if trade.EntryPrice > 0 {
					if trade.IsShort {
						pnlPct = (trade.EntryPrice - ltp) / trade.EntryPrice * 100
					} else {
						pnlPct = (ltp - trade.EntryPrice) / trade.EntryPrice * 100
					}
				}
				pos["ltp"] = ltp
				pos["unrealised_pnl"] = fmt.Sprintf("%.2f", pnl)
				pos["pnl_pct"] = fmt.Sprintf("%.2f", pnlPct)
			}
		}
		openPositions = append(openPositions, pos)
	}
	s.exec.Mu.RUnlock()

	// Sort openPositions by symbol so UI doesn't shuffle
	sort.SliceStable(openPositions, func(i, j int) bool {
		s1, _ := openPositions[i]["symbol"].(string)
		s2, _ := openPositions[j]["symbol"].(string)
		return s1 < s2
	})

	universeCount := 0
	if s.scanner != nil && s.scanner.Universe != nil {
		universeCount = len(s.scanner.Universe)
	}

	indexData := map[string]interface{}{}
	if s.tickStore != nil {
		indexData["NIFTY_50"] = map[string]float64{
			"ltp":    s.tickStore.GetLTPIfFresh(config.NiftySpotToken),
			"change": s.tickStore.GetChangePct(config.NiftySpotToken),
		}
		indexData["BANK_NIFTY"] = map[string]float64{
			"ltp":    s.tickStore.GetLTPIfFresh(config.BankNiftySpotToken),
			"change": s.tickStore.GetChangePct(config.BankNiftySpotToken),
		}
		indexData["INDIA_VIX"] = map[string]float64{
			"ltp":    s.tickStore.GetLTPIfFresh(config.IndiaVIXToken),
			"change": s.tickStore.GetChangePct(config.IndiaVIXToken),
		}
	}

	// Calculate Live Stats
	stats := map[string]interface{}{
		"total": 0, "wins": 0, "losses": 0,
		"win_rate": 0.0, "gross_pnl": 0.0,
	}
	if s.journal != nil {
		today := config.TodayIST().Format("2006-01-02")
		ps := s.journal.GetPeriodSummary(today, today)
		if ps != nil {
			stats["total"] = ps.Total
			stats["wins"] = ps.Wins
			stats["losses"] = ps.Losses
			stats["win_rate"] = ps.WinRate
			
			realized := ps.GrossPnl
			unrealized := 0.0
			for _, pos := range openPositions {
				if upnlStr, ok := pos["unrealised_pnl"].(string); ok {
					var val float64
					fmt.Sscanf(upnlStr, "%f", &val)
					unrealized += val
				}
			}
			stats["gross_pnl"] = realized + unrealized
		}
	}

	return map[string]interface{}{
		"regime":         regime,
		"open_positions": openPositions,
		"capital":        config.TotalCapital,
		"paper_mode":     config.PaperMode,
		"universe_count": universeCount,
		"index_data":     indexData,
		"stats":          stats,
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.buildStatusData())
}

func (s *Server) handlePositions(w http.ResponseWriter, r *http.Request) {
	positions := make([]map[string]interface{}, 0)
	s.exec.Mu.RLock()
	for oid, trade := range s.exec.ActiveTrades {
		pos := map[string]interface{}{
			"oid":           oid,
			"symbol":        trade.Symbol,
			"strategy":      trade.Strategy,
			"entry_price":   trade.EntryPrice,
			"stop_price":    trade.StopPrice,
			"target_price":  trade.TargetPrice,
			"qty":           trade.Qty,
			"remaining_qty": trade.RemainingQty,
			"is_short":      trade.IsShort,
			"product":       trade.Product,
			"regime":        trade.Regime,
		}
		positions = append(positions, pos)
	}
	s.exec.Mu.RUnlock()
	writeJSON(w, map[string]interface{}{"positions": positions})
}

func (s *Server) handleTrades(w http.ResponseWriter, r *http.Request) {
	dateStr := r.URL.Query().Get("date")
	if dateStr == "" {
		dateStr = config.TodayIST().Format("2006-01-02")
	}
	trades := s.journal.GetAllTradesForDate(dateStr)
	writeJSON(w, map[string]interface{}{"trades": trades, "date": dateStr})
}

func (s *Server) handleTradesDates(w http.ResponseWriter, r *http.Request) {
	dates := s.journal.GetAvailableDates()
	writeJSON(w, map[string]interface{}{"dates": dates})
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	dateStr := r.URL.Query().Get("date")
	if dateStr == "" {
		dateStr = config.TodayIST().Format("2006-01-02")
	}
	summary := s.journal.GetDailySummaryForDate(dateStr)
	writeJSON(w, map[string]interface{}{"summary": summary})
}

func (s *Server) handlePnlSummary(w http.ResponseWriter, r *http.Request) {
	today := config.TodayIST()

	// Weekly summary: Monday to Friday of the current week
	offset := int(time.Monday - today.Weekday())
	if offset > 0 {
		offset = -6
	}
	weekStart := today.AddDate(0, 0, offset)
	weekEnd := weekStart.AddDate(0, 0, 4)

	weeklySummary := s.journal.GetPeriodSummary(weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02"))
	weeklyBreakdown := s.journal.GetPnlBreakdown(weekStart.Format("2006-01-02"), weekEnd.Format("2006-01-02"))

	// Monthly summary: 1st to last day of current month
	monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location())
	monthEnd := monthStart.AddDate(0, 1, -1)

	monthlySummary := s.journal.GetPeriodSummary(monthStart.Format("2006-01-02"), monthEnd.Format("2006-01-02"))
	monthlyBreakdown := s.journal.GetPnlBreakdown(monthStart.Format("2006-01-02"), monthEnd.Format("2006-01-02"))

	// Calculate unrealized P&L from open positions
	unrealized := 0.0
	s.exec.Mu.RLock()
	for _, trade := range s.exec.ActiveTrades {
		if s.tickStore != nil && trade.Token > 0 {
			ltp := s.tickStore.GetLTPIfFresh(trade.Token)
			if ltp > 0 {
				if trade.IsShort {
					unrealized += (trade.EntryPrice - ltp) * float64(trade.RemainingQty)
				} else {
					unrealized += (ltp - trade.EntryPrice) * float64(trade.RemainingQty)
				}
			}
		}
	}
	s.exec.Mu.RUnlock()

	// unrealized_pnl is the same value for both periods — it's the mark-to-market on all
	// currently open positions. Exposing it separately lets the UI label it clearly so the
	// user can distinguish realized (closed trades) from floating (open positions).
	// "pnl" = realized + unrealized, kept for any consumers that read the total directly.
	writeJSON(w, map[string]interface{}{
		"weekly": map[string]interface{}{
			"realized_pnl":   weeklySummary.GrossPnl,
			"unrealized_pnl": unrealized,
			"pnl":            weeklySummary.GrossPnl + unrealized,
			"trades":         weeklySummary.Total,
			"wins":           weeklySummary.Wins,
			"losses":         weeklySummary.Losses,
			"breakdown":      weeklyBreakdown,
		},
		"monthly": map[string]interface{}{
			"realized_pnl":   monthlySummary.GrossPnl,
			"unrealized_pnl": unrealized,
			"pnl":            monthlySummary.GrossPnl + unrealized,
			"trades":         monthlySummary.Total,
			"wins":           monthlySummary.Wins,
			"losses":         monthlySummary.Losses,
			"breakdown":      monthlyBreakdown,
		},
	})
}

// ── Backtest handlers ────────────────────────────────────────────

func (s *Server) handleBacktestRun(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			msg := fmt.Sprintf("%v", rec)
			log.Printf("[Backtest] PANIC: %s", msg)
			http.Error(w, fmt.Sprintf(`{"error":"backtest panic: %s"}`, msg), 500)
		}
	}()

	if r.Method != "POST" {
		http.Error(w, `{"error":"POST required"}`, 405)
		return
	}
	var cfg backtest.Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, 400)
		return
	}
	if cfg.Capital <= 0 {
		cfg.Capital = config.TotalCapital
	}
	if cfg.SLFloorPct == 0 {
		cfg.SLFloorPct = config.SLFloorPct
	}
	if cfg.SLCeilingPct == 0 {
		cfg.SLCeilingPct = config.SLCeilingPct
	}
	if cfg.MaxTradeAllocPct == 0 {
		cfg.MaxTradeAllocPct = config.MaxTradeAllocPct
	}

	// Build agents.DailyCache snapshot from storage.DailyCache
	agentsCache := s.dailyCache.ExportAgentsCache()
	if agentsCache == nil || !agentsCache.Loaded {
		http.Error(w, `{"error":"daily cache not loaded — start engine first"}`, 503)
		return
	}

	result := backtest.Run(agentsCache, s.scanner.Universe, cfg)
	if err := backtest.SaveResult(result); err != nil {
		log.Printf("[Backtest] Save error: %v", err)
	}
	writeJSON(w, result)
}

func (s *Server) handleBacktestHistory(w http.ResponseWriter, r *http.Request) {
	results, err := backtest.LoadHistory(20)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%v"}`, err), 500)
		return
	}
	writeJSON(w, map[string]interface{}{"results": results})
}

func (s *Server) handleConfigApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, `{"error":"POST required"}`, 405)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"read body"}`, 400)
		return
	}
	path := filepath.Join("data", "config_override.json")
	if err := os.WriteFile(path, body, 0644); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%v"}`, err), 500)
		return
	}
	// Live-reload: push override values into the running config vars immediately.
	// The EMA agent picks up the new SL%, CMF threshold, and alloc on the next scan cycle.
	config.LoadOverride(path)
	writeJSON(w, map[string]string{"status": "ok", "path": path})
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // direct/local connection with no Origin header
		}
		allowed := config.DashboardAllowedOrigin
		if allowed == "" {
			allowed = "http://127.0.0.1:8085"
		}
		return origin == allowed
	},
}

func (s *Server) handleWSLive(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil { return }
	defer conn.Close()

	ticker := time.NewTicker(1000 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			payload := map[string]interface{}{
				"type":   "live_update",
				"health": s.buildHealthData(),
				"status": s.buildStatusData(),
				"logs": map[string]interface{}{
					"logs": core.GlobalMemLog.GetLogs(),
				},
			}
			if err := conn.WriteJSON(payload); err != nil {
				return
			}
		}
	}
}
