package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bnf_go_engine/agents"
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
		"engine":       "Quantix Engine v3.0",
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

	return map[string]interface{}{
		"regime":         regime,
		"open_positions": openPositions,
		"capital":        config.TotalCapital,
		"paper_mode":     config.PaperMode,
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

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (s *Server) handleWSLive(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil { return }
	defer conn.Close()

	ticker := time.NewTicker(1000 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		payload := map[string]interface{}{
			"type":   "live_update",
			"health": s.buildHealthData(),
			"status": s.buildStatusData(),
		}
		if err := conn.WriteJSON(payload); err != nil { break }
	}
}
