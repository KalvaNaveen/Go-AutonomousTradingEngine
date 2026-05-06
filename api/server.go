package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bnf_go_engine/agents"
	"bnf_go_engine/config"
	"bnf_go_engine/core"
	"bnf_go_engine/storage"
	"bnf_go_engine/simulator"

	"github.com/gorilla/websocket"
)

// Server provides the REST API for the dashboard frontend.
// Replaces the Python FastAPI backend.
type Server struct {
	risk       *agents.RiskAgent
	journal    *core.Journal
	exec       *agents.ExecutionAgent
	scanner    *agents.ScannerAgent
	tickStore  *storage.TickStore
	dailyCache *storage.DailyCache
	macro      *agents.MacroAgent
	startTime  time.Time
}

func NewServer(
	risk *agents.RiskAgent,
	journal *core.Journal,
	exec *agents.ExecutionAgent,
	scanner *agents.ScannerAgent,
	tickStore *storage.TickStore,
	dailyCache *storage.DailyCache,
	macro *agents.MacroAgent,
) *Server {
	return &Server{risk, journal, exec, scanner, tickStore, dailyCache, macro, time.Now()}
}

func (s *Server) Start(addr string) {
	mux := http.NewServeMux()

	// CORS middleware wrapper
	handler := corsMiddleware(mux)

	// Health
	mux.HandleFunc("/api/health", s.handleHealth)

	// Engine status
	mux.HandleFunc("/api/status", s.handleStatus)

	// Active positions
	mux.HandleFunc("/api/positions", s.handlePositions)

	// Trade history
	mux.HandleFunc("/api/trades", s.handleTrades)
	mux.HandleFunc("/api/trades/dates", s.handleTradeDates)

	// Daily summary
	mux.HandleFunc("/api/summary", s.handleSummary)

	// P&L summary (daily/weekly/monthly breakdown)
	mux.HandleFunc("/api/pnl-summary", s.handlePnlSummary)

	// Agent logs
	mux.HandleFunc("/api/logs", s.handleLogs)

	// Risk stats
	mux.HandleFunc("/api/risk", s.handleRisk)

	// Performance metrics
	mux.HandleFunc("/api/performance", s.handlePerformance)
	
	// Trade analysis (REMOVED)

	// Live WebSocket
	mux.HandleFunc("/api/ws/live", s.handleWSLive)

	// Historical Simulator WebSocket
	mux.HandleFunc("/api/ws/simulator", s.handleWSSimulator)

	// Serve pre-built dashboard from dist/ directory (no npm needed)
	distDir := filepath.Join(config.BaseDir, "dashboard", "dist")
	if _, err := os.Stat(distDir); err == nil {
		fs := http.FileServer(http.Dir(distDir))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			// Allow scripts/styles to execute (CSP)
			w.Header().Set("Content-Security-Policy", "default-src * 'unsafe-inline' 'unsafe-eval' data: blob:; script-src * 'unsafe-inline' 'unsafe-eval'; style-src * 'unsafe-inline';")
			// If the file exists, serve it; otherwise serve index.html (SPA fallback)
			path := filepath.Join(distDir, r.URL.Path)
			if _, err := os.Stat(path); os.IsNotExist(err) || r.URL.Path == "/" {
				http.ServeFile(w, r, filepath.Join(distDir, "index.html"))
				return
			}
			fs.ServeHTTP(w, r)
		})
		log.Printf("[API] Serving dashboard from %s", distDir)
	} else {
		log.Println("[API] No dashboard dist found — UI will not be served")
	}

	log.Printf("[API] Starting on %s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Printf("[API] Server error: %v", err)
	}
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
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
		"engine":       "Quantix Engine v2.0",
		"uptime":       fmt.Sprintf("%dh %dm %ds", int(time.Since(s.startTime).Hours()), int(time.Since(s.startTime).Minutes())%60, int(time.Since(s.startTime).Seconds())%60),
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

	stats := s.risk.GetDailyStats()

	openPositions := make([]map[string]interface{}, 0)
	s.exec.Mu.RLock()
	for oid, trade := range s.exec.ActiveTrades {
		pos := map[string]interface{}{
			"oid":         oid,
			"symbol":      trade.Symbol,
			"strategy":    trade.Strategy,
			"entry_price": trade.EntryPrice,
			"stop_price":  trade.StopPrice,
			"target":      trade.TargetPrice,
			"qty":         trade.Qty,
			"is_short":    trade.IsShort,
			"regime":      trade.Regime,
			"entry_time":  trade.EntryTime.Format(time.RFC3339),
		}

		// Live P&L
		if s.tickStore != nil && trade.Token > 0 {
			ltp := s.tickStore.GetLTPIfFresh(trade.Token)
			if ltp > 0 {
				var pnl float64
				if trade.IsShort {
					pnl = (trade.EntryPrice - ltp) * float64(trade.RemainingQty)
				} else {
					pnl = (ltp - trade.EntryPrice) * float64(trade.RemainingQty)
				}
				pos["ltp"] = ltp
				pos["unrealised_pnl"] = fmt.Sprintf("%.2f", pnl)
			}
		}
		openPositions = append(openPositions, pos)
	}
	s.exec.Mu.RUnlock()

	indexData := map[string]interface{}{
		"nifty50":       nil,
		"nifty50_pct":   nil,
		"banknifty":     nil,
		"banknifty_pct": nil,
		"vix":           nil,
		"vix_pct":       nil,
	}
	if s.tickStore != nil {
		if ltp := s.tickStore.GetLTP(config.Nifty50Token); ltp > 0 {
			indexData["nifty50"] = ltp
			indexData["nifty50_pct"] = s.tickStore.GetChangePct(config.Nifty50Token)
		}
		if ltp := s.tickStore.GetLTP(config.SectorTokens["NIFTY BANK"]); ltp > 0 {
			indexData["banknifty"] = ltp
			indexData["banknifty_pct"] = s.tickStore.GetChangePct(config.SectorTokens["NIFTY BANK"])
		}
		if ltp := s.tickStore.GetLTP(config.IndiaVIXToken); ltp > 0 {
			indexData["vix"] = ltp
			indexData["vix_pct"] = s.tickStore.GetChangePct(config.IndiaVIXToken)
		}
	}

	return map[string]interface{}{
		"regime":         regime,
		"engine_stopped": s.risk.EngineStopped,
		"stop_reason":    s.risk.StopReason,
		"stats":          stats,
		"open_positions": openPositions,
		"capital":        s.risk.TotalCapital,
		"daily_pnl":      s.risk.DailyPnl,
		"paper_mode":     config.PaperMode,
		"universe_count": len(s.scanner.Universe),
		"index_data":     indexData,
		"news_feed":      s.macro.GetNewsFeed(),
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.buildStatusData())
}

// handleAnalysis removed

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
			"entry_time":    trade.EntryTime.Format(time.RFC3339),
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

func (s *Server) handleTradeDates(w http.ResponseWriter, r *http.Request) {
	dates := s.journal.GetAvailableDates()
	writeJSON(w, map[string]interface{}{"dates": dates})
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	dateStr := r.URL.Query().Get("date")
	if dateStr == "" {
		dateStr = config.TodayIST().Format("2006-01-02")
	}
	summary := s.journal.GetDailySummaryForDate(dateStr)
	writeJSON(w, map[string]interface{}{"summary": summary, "date": dateStr})
}

func (s *Server) handlePnlSummary(w http.ResponseWriter, r *http.Request) {
	now := config.NowIST()
	today := now.Format("2006-01-02")

	// Weekly: Monday of current week → today
	weekday := now.Weekday()
	if weekday == 0 { weekday = 7 } // Sunday = 7
	monday := now.AddDate(0, 0, -int(weekday-1))
	weekStart := monday.Format("2006-01-02")
	weeklyBreakdown := s.journal.GetPnlBreakdown(weekStart, today)

	// Monthly: 1st of current month → today
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Format("2006-01-02")
	monthlyBreakdown := s.journal.GetPnlBreakdown(monthStart, today)

	// Yearly: 1st of January of current year → today
	yearStart := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location()).Format("2006-01-02")
	yearlyBreakdown := s.journal.GetPnlBreakdown(yearStart, today)

	// Aggregate weekly totals
	wPnl, wTrades, wWins := 0.0, 0, 0
	for _, d := range weeklyBreakdown {
		wPnl += d["pnl"].(float64)
		wTrades += d["trades"].(int)
		wWins += d["wins"].(int)
	}

	// Aggregate monthly totals
	mPnl, mTrades, mWins := 0.0, 0, 0
	for _, d := range monthlyBreakdown {
		mPnl += d["pnl"].(float64)
		mTrades += d["trades"].(int)
		mWins += d["wins"].(int)
	}

	// Aggregate yearly totals
	yPnl, yTrades, yWins := 0.0, 0, 0
	for _, d := range yearlyBreakdown {
		yPnl += d["pnl"].(float64)
		yTrades += d["trades"].(int)
		yWins += d["wins"].(int)
	}

	writeJSON(w, map[string]interface{}{
		"weekly": map[string]interface{}{
			"pnl": wPnl, "trades": wTrades, "wins": wWins, "losses": wTrades - wWins,
			"from": weekStart, "to": today,
			"breakdown": weeklyBreakdown,
		},
		"monthly": map[string]interface{}{
			"pnl": mPnl, "trades": mTrades, "wins": mWins, "losses": mTrades - mWins,
			"from": monthStart, "to": today,
			"breakdown": monthlyBreakdown,
		},
		"yearly": map[string]interface{}{
			"pnl": yPnl, "trades": yTrades, "wins": yWins, "losses": yTrades - yWins,
			"from": yearStart, "to": today,
			"breakdown": yearlyBreakdown,
		},
	})
}

func (s *Server) buildLogsData(dateStr string) map[string]interface{} {
	if dateStr == "" {
		dateStr = config.TodayIST().Format("2006-01-02")
	}
	logs := s.journal.GetLogsForDate(dateStr)
	return map[string]interface{}{"logs": logs, "date": dateStr}
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.buildLogsData(r.URL.Query().Get("date")))
}

func (s *Server) handleRisk(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{
		"total_capital":      s.risk.TotalCapital,
		"active_capital":     s.risk.ActiveCapital,
		"risk_reserve":       s.risk.RiskReserve,
		"daily_pnl":          s.risk.DailyPnl,
		"weekly_pnl":         s.risk.WeeklyPnl,
		"consecutive_losses": s.risk.ConsecutiveLosses,
		"engine_stopped":     s.risk.EngineStopped,
		"stop_reason":        s.risk.StopReason,
		"open_positions":     len(s.risk.OpenPositions),
		"max_positions":      config.MaxOpenPositions,
		"max_risk_per_trade": config.MaxRiskPerTradePct,
		"daily_loss_limit":   config.DailyLossLimitPct,
	})
}

func (s *Server) handlePerformance(w http.ResponseWriter, r *http.Request) {
	from := r.URL.Query().Get("from")
	to := r.URL.Query().Get("to")
	if from == "" {
		from = config.TodayIST().AddDate(0, -1, 0).Format("2006-01-02")
	}
	if to == "" {
		to = config.TodayIST().Format("2006-01-02")
	}
	summary := s.journal.GetPeriodSummary(from, to)
	writeJSON(w, map[string]interface{}{
		"from":         from,
		"to":           to,
		"total_trades": summary.Total,
		"wins":         summary.Wins,
		"losses":       summary.Losses,
		"win_rate":     summary.WinRate,
		"gross_pnl":    summary.GrossPnl,
		"est_charges":  summary.EstCharges,
		"best_regime":  summary.BestRegime,
		"worst_regime": summary.WorstRegime,
	})
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for the dashboard
	},
}

func (s *Server) handleWSLive(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[API] WS Upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	// Stream updates every 500ms
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	// Send an initial payload immediately
	payload := map[string]interface{}{
		"type":   "live_update",
		"health": s.buildHealthData(),
		"status": s.buildStatusData(),
		"logs":   s.buildLogsData(""),
	}
	if err := conn.WriteJSON(payload); err != nil {
		return
	}

	for range ticker.C {
		payload := map[string]interface{}{
			"type":   "live_update",
			"health": s.buildHealthData(),
			"status": s.buildStatusData(),
			"logs":   s.buildLogsData(""),
		}

		if err := conn.WriteJSON(payload); err != nil {
			// Client disconnected
			break
		}
	}
}

func (s *Server) handleWSSimulator(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[API] Simulator WS Upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	daysStr := r.URL.Query().Get("days")
	days := 30
	if parsed, err := strconv.Atoi(daysStr); err == nil && parsed > 0 {
		days = parsed
	}

	strategiesStr := r.URL.Query().Get("strategies")
	var parsedStrategies []string
	if strategiesStr != "" {
		parsedStrategies = strings.Split(strategiesStr, ",")
	}

	writeLog := func(format string, v ...interface{}) {
		msg := fmt.Sprintf("[SIM] "+format, v...)
		conn.WriteMessage(1, []byte(msg)) // 1 = TextMessage
	}

	writeLog("Initializing Quantum Simulation Core (Days: %d)...", days)

	endDate := config.TodayIST().Format("2006-01-02")
	startDate := config.TodayIST().AddDate(0, 0, -days).Format("2006-01-02")

	bt := simulator.NewBacktester(simulator.BacktestConfig{
		StartDate:      startDate,
		EndDate:        endDate,
		InitialCapital: config.TotalCapital,
		MaxPositions:   5,
		Strategies:     parsedStrategies,
		TokenToCompany: s.scanner.TokenToCompany,
	})
	
	bt.LogOutput = writeLog

	writeLog("Connecting to historical data matrix...")
	
	res, err := bt.Run()
	if err != nil {
		writeLog("[ERROR] Simulator aborted: %v", err)
		return
	}

	writeLog("════ Simulation Summary ════")
	writeLog("Total trades executed: %d", res.TotalTrades)
	writeLog("Win Rate: %.1f%%  |  Profit Factor: %.2f", res.WinRate, res.ProfitFactor)
	writeLog("Absolute PNL: ₹%.2f", res.TotalPnL)
	writeLog("Ending Equity: ₹%.2f", res.FinalCapital)
	
	writeLog("Connection closed. Session ended.")
}
