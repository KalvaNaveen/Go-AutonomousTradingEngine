package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"bnf_go_engine/agents"
	"bnf_go_engine/config"
	"bnf_go_engine/core"
	"bnf_go_engine/storage"
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

	// Agent logs
	mux.HandleFunc("/api/logs", s.handleLogs)

	// Risk stats
	mux.HandleFunc("/api/risk", s.handleRisk)

	// Performance metrics
	mux.HandleFunc("/api/performance", s.handlePerformance)
	
	// Trade analysis
	mux.HandleFunc("/api/analysis/", s.handleAnalysis)

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

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	wsConnected := false
	ticksFresh := false
	if s.tickStore != nil {
		ticksFresh = s.tickStore.IsFresh()
		wsConnected = s.tickStore.IsReady()
	}

	writeJSON(w, map[string]interface{}{
		"status":       "running",
		"engine":       "BNF Go Engine v1.0",
		"uptime":       fmt.Sprintf("%dh %dm %ds", int(time.Since(s.startTime).Hours()), int(time.Since(s.startTime).Minutes())%60, int(time.Since(s.startTime).Seconds())%60),
		"paper_mode":   config.PaperMode,
		"ws_connected": wsConnected,
		"ticks_fresh":  ticksFresh,
		"cache_loaded": s.dailyCache.IsLoaded(),
		"timestamp":    config.NowIST().Format(time.RFC3339),
	})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	regime := "UNKNOWN"
	if s.scanner != nil {
		regime = s.scanner.DetectRegime()
	}

	stats := s.risk.GetDailyStats()

	openPositions := make([]map[string]interface{}, 0)
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
			"entry_time":  trade.EntryTime.Format("15:04:05"),
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

	indexData := map[string]interface{}{
		"nifty50": nil,
		"vix":     nil,
	}
	if s.tickStore != nil {
		if ltp := s.tickStore.GetLTP(config.Nifty50Token); ltp > 0 {
			indexData["nifty50"] = ltp
		}
		if ltp := s.tickStore.GetLTP(config.SectorTokens["NIFTY BANK"]); ltp > 0 {
			indexData["banknifty"] = ltp
		}
		if ltp := s.tickStore.GetLTP(config.IndiaVIXToken); ltp > 0 {
			indexData["vix"] = ltp
		}
	}

	writeJSON(w, map[string]interface{}{
		"regime":         regime,
		"engine_stopped": s.risk.EngineStopped,
		"stop_reason":    s.risk.StopReason,
		"stats":          stats,
		"open_positions": openPositions,
		"capital":        s.risk.TotalCapital,
		"daily_pnl":      s.risk.DailyPnl,
		"paper_mode":     config.PaperMode,
		"universe_count": 250, // Hardcoded for Nifty 250 universe
		"index_data":     indexData,
		"news_feed":      s.macro.GetNewsFeed(),
	})
}

func (s *Server) handleAnalysis(w http.ResponseWriter, r *http.Request) {
	dateStr := strings.TrimPrefix(r.URL.Path, "/api/analysis/")
	if dateStr == "" {
		dateStr = config.TodayIST().Format("2006-01-02")
	}

	trades := s.journal.GetAllTradesForDate(dateStr)
	summary := s.journal.GetDailySummaryForDate(dateStr)

	// Build analysis payload combining trades (with grades) and summary
	analysisTrades := make([]map[string]interface{}, 0)
	for _, t := range trades {
		pnl, _ := t["gross_pnl"].(float64)
		entry, _ := t["entry_price"].(float64)
		qtyNum, _ := t["qty"].(float64)
		if qtyNum == 0 {
			qtyNum = 15 // Default fallback
		}

		isWin := pnl > 0
		isLoss := pnl <= 0
		grade := "C"
		if pnl > 0 {
			if (pnl / (entry * qtyNum)) > 0.015 {
				grade = "A"
			} else {
				grade = "B"
			}
		} else {
			if (pnl / (entry * qtyNum)) < -0.015 {
				grade = "F"
			} else {
				grade = "D"
			}
		}

		pos := []string{}
		if isWin {
			pos = append(pos, "Profitable trade")
			pos = append(pos, "Risk rewarded correctly")
		}
		neg := []string{}
		if isLoss {
			neg = append(neg, "Trade went against strategy bias")
			if reason, ok := t["exit_reason"].(string); ok && reason == "STOPLOSS" {
				neg = append(neg, "Hit hard stop level")
			}
		}

		at := map[string]interface{}{
			"symbol":      t["symbol"],
			"strategy":    t["strategy"],
			"is_win":      isWin,
			"is_loss":     isLoss,
			"entry_price": t["entry_price"],
			"exit_price":  t["full_exit_price"],
			"qty":         t["qty"],
			"pnl":         pnl,
			"exit_reason": t["exit_reason"],
			"grade":       grade,
			"positives":   pos,
			"negatives":   neg,
			"fixes":       []string{"Follow system strictly", "Monitor regime changes"},
		}
		analysisTrades = append(analysisTrades, at)
	}

	gradeCounts := map[string]int{"A": 0, "B": 0, "C": 0, "D": 0, "F": 0}
	for _, t := range analysisTrades {
		if g, ok := t["grade"].(string); ok {
			gradeCounts[g]++
		}
	}

	analysisSummary := map[string]interface{}{
		"total":    len(trades),
		"win_rate": summary["win_rate"],
		"regime":   summary["dominant_regime"],
		"grades":   gradeCounts,
	}

	writeJSON(w, map[string]interface{}{
		"trades":  analysisTrades,
		"summary": analysisSummary,
	})
}

func (s *Server) handlePositions(w http.ResponseWriter, r *http.Request) {
	positions := make([]map[string]interface{}, 0)
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
			"entry_time":    trade.EntryTime.Format("15:04:05"),
		}
		positions = append(positions, pos)
	}
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

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	dateStr := r.URL.Query().Get("date")
	if dateStr == "" {
		dateStr = config.TodayIST().Format("2006-01-02")
	}
	logs := s.journal.GetLogsForDate(dateStr)
	writeJSON(w, map[string]interface{}{"logs": logs, "date": dateStr})
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
