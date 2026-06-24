// Package web serves the BTST dashboard: a single self-contained HTML page plus
// a JSON API, both reading from the SQLite store. It shows today's trades, open
// positions, closed trades with P&L, and headline stats (deployed, realised P&L,
// win rate) so the 30-day paper run can be watched at a glance.
package web

import (
	"encoding/json"
	"fmt"
	"net/http"

	"bnf_go_engine/config"
	"bnf_go_engine/model"
	"bnf_go_engine/store"
)

// Server wraps the store and serves the dashboard.
type Server struct {
	store *store.Store
	paper bool
}

// New builds a dashboard server. paper controls the mode badge.
func New(st *store.Store, paper bool) *Server {
	return &Server{store: st, paper: paper}
}

// Handler returns the HTTP mux for the dashboard.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/summary", s.handleSummary)
	return mux
}

// ListenAndServe starts the dashboard on addr (e.g. ":8085").
func (s *Server) ListenAndServe(addr string) error {
	return http.ListenAndServe(addr, s.Handler())
}

// ── JSON view models ────────────────────────────────────────────────────────

type positionView struct {
	Symbol     string  `json:"symbol"`
	Qty        int     `json:"qty"`
	EntryPrice float64 `json:"entry_price"`
	SLPrice    float64 `json:"sl_price"`
	ExitPrice  float64 `json:"exit_price,omitempty"`
	ExitReason string  `json:"exit_reason,omitempty"`
	Invested   float64 `json:"invested"`
	PnL        float64 `json:"pnl"`
	PnLPct     float64 `json:"pnl_pct"`
	TradeDate  string  `json:"trade_date"`
}

type summary struct {
	Mode         string         `json:"mode"`
	Today        string         `json:"today"`
	CapitalDay   float64        `json:"capital_per_day"`
	OpenCount    int            `json:"open_count"`
	OpenInvested float64        `json:"open_invested"`
	ClosedCount  int            `json:"closed_count"`
	RealizedPnL  float64        `json:"realized_pnl"`
	ReturnPct    float64        `json:"return_pct"`
	Wins         int            `json:"wins"`
	WinRate      float64        `json:"win_rate"`
	Open         []positionView `json:"open"`
	Closed       []positionView `json:"closed"`
}

func view(p model.Position) positionView {
	v := positionView{
		Symbol: p.Symbol, Qty: p.Qty, EntryPrice: p.EntryPrice, SLPrice: p.SLPrice,
		Invested: p.Invested(), TradeDate: p.TradeDate,
	}
	// P&L only exists once closed — leave open positions at zero rather than
	// reporting a spurious -100% from a zero exit price.
	if p.Status == model.StatusClosed {
		v.ExitPrice = p.ExitPrice
		v.ExitReason = p.ExitReason
		v.PnL = p.PnL
		v.PnLPct = p.PnLPct()
	}
	return v
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	open, err := s.store.OpenPositions()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	closed, err := s.store.ClosedPositions(500)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	out := summary{
		Mode:       map[bool]string{true: "PAPER", false: "LIVE"}[s.paper],
		Today:      config.NowIST().Format("2006-01-02"),
		CapitalDay: config.BTSTCapitalPerDay,
	}
	for _, p := range open {
		out.Open = append(out.Open, view(p))
		out.OpenInvested += p.Invested()
	}
	out.OpenCount = len(open)

	var closedInvested float64
	for _, p := range closed {
		out.Closed = append(out.Closed, view(p))
		out.RealizedPnL += p.PnL
		closedInvested += p.Invested()
		if p.PnL > 0 {
			out.Wins++
		}
	}
	out.ClosedCount = len(closed)
	if closedInvested > 0 {
		out.ReturnPct = out.RealizedPnL / closedInvested * 100
	}
	if out.ClosedCount > 0 {
		out.WinRate = float64(out.Wins) / float64(out.ClosedCount) * 100
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, dashboardHTML)
}
