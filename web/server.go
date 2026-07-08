// Package web serves the BTST dashboard: a single self-contained HTML page plus
// a JSON API, both reading from the SQLite store. It shows today's trades, open
// positions, closed trades with P&L, and headline stats (deployed, realised P&L,
// win rate) so the 30-day paper run can be watched at a glance.
package web

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"

	"bnf_go_engine/config"
	"bnf_go_engine/model"
	"bnf_go_engine/store"
)

// r2 rounds to 2 decimals — applied to every float leaving the API so no
// consumer (UI, JSON viewer, script) ever sees 877.4507500000001.
func r2(v float64) float64 { return math.Round(v*100) / 100 }

// Server wraps the store and serves the dashboard.
type Server struct {
	store   *store.Store
	paper   bool
	trigger func(force bool) string // manual scan+trade; nil = disabled
	token   string                  // required query token for /api/run
}

// New builds a dashboard server. paper controls the mode badge.
func New(st *store.Store, paper bool) *Server {
	return &Server{store: st, paper: paper}
}

// SetTrigger enables the token-protected manual /api/run endpoint. fn runs the
// scan+trade (async) and returns a short status string.
func (s *Server) SetTrigger(token string, fn func(force bool) string) {
	s.token = token
	s.trigger = fn
}

// Handler returns the HTTP mux for the dashboard.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/summary", s.handleSummary)
	mux.HandleFunc("/api/dates", s.handleDates)
	mux.HandleFunc("/api/history", s.handleHistory)
	mux.HandleFunc("/api/delete", s.handleDelete)
	if s.trigger != nil {
		mux.HandleFunc("/api/run", s.handleRun)
	}
	return mux
}

// handleDates returns the list of dates that have scan/trade data (newest first).
func (s *Server) handleDates(w http.ResponseWriter, r *http.Request) {
	dates, err := s.store.Dates()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(dates)
}

// handleHistory returns one date's scan list + the positions entered that day.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if date == "" {
		http.Error(w, "date required", http.StatusBadRequest)
		return
	}
	scan, _ := s.store.ScanByDate(date)
	scanTime, _ := s.store.ScanTime(date)
	pos, _ := s.store.PositionsByDate(date)

	out := struct {
		Date      string          `json:"date"`
		ScanTime  string          `json:"scan_time"`
		Scan      []store.ScanRow `json:"scan"`
		Positions []positionView  `json:"positions"`
		NetPnL    float64         `json:"net_pnl"`
	}{Date: date, ScanTime: scanTime, Scan: scan}
	for _, p := range pos {
		out.Positions = append(out.Positions, view(p))
		out.NetPnL += p.NetPnL()
	}
	out.NetPnL = r2(out.NetPnL)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleDelete removes trade records: ?id=<position id> deletes one position
// (and its SL events), ?date=YYYY-MM-DD wipes every position + scan row for
// that day. Destructive, so when a trigger token is configured it is required
// here too; without one (local dev dashboard) it is open.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "POST or DELETE required", http.StatusMethodNotAllowed)
		return
	}
	if s.token != "" && r.URL.Query().Get("token") != s.token {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	switch {
	case q.Get("id") != "":
		id, err := strconv.ParseInt(q.Get("id"), 10, 64)
		if err != nil {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		n, err := s.store.DeletePosition(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if n == 0 {
			http.Error(w, "no such record", http.StatusNotFound)
			return
		}
		fmt.Fprintf(w, "deleted record #%d\n", id)
	case q.Get("date") != "":
		date := q.Get("date")
		if err := s.store.PurgeDate(date); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "deleted all records for %s\n", date)
	default:
		http.Error(w, "id or date required", http.StatusBadRequest)
	}
}

// handleRun fires a manual scan+trade. Requires ?token=<BTST_TRIGGER_TOKEN>.
// ?force=1 re-runs cleanly (purges today's rows first).
func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if s.token == "" || r.URL.Query().Get("token") != s.token {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	force := r.URL.Query().Get("force") == "1"
	msg := s.trigger(force)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, msg)
}

// ListenAndServe starts the dashboard on addr (e.g. ":8085").
func (s *Server) ListenAndServe(addr string) error {
	return http.ListenAndServe(addr, s.Handler())
}

// ── JSON view models ────────────────────────────────────────────────────────

type positionView struct {
	ID         int64   `json:"id"`
	Symbol     string  `json:"symbol"`
	Qty        int     `json:"qty"`
	EntryPrice float64 `json:"entry_price"`
	SLPrice    float64 `json:"sl_price"`
	PeakPrice  float64 `json:"peak_price,omitempty"`
	LastPrice  float64 `json:"last_price,omitempty"`
	CarryCount int     `json:"carry_count,omitempty"`
	UnrealPnL  float64 `json:"unreal_pnl,omitempty"`
	UnrealPct  float64 `json:"unreal_pct,omitempty"`
	ExitPrice  float64 `json:"exit_price,omitempty"`
	ExitReason string  `json:"exit_reason,omitempty"`
	Invested   float64 `json:"invested"`
	PnL        float64 `json:"pnl"`
	PnLPct     float64 `json:"pnl_pct"`
	Charges    float64 `json:"charges,omitempty"`
	NetPnL     float64 `json:"net_pnl,omitempty"`
	NetPct     float64 `json:"net_pct,omitempty"`
	EntryAt    string  `json:"entry_at,omitempty"` // "02 Jan 15:04:05" IST
	ExitAt     string  `json:"exit_at,omitempty"`
	TradeDate  string  `json:"trade_date"`
}

type summary struct {
	Mode          string         `json:"mode"`
	Today         string         `json:"today"`
	CapitalDay    float64        `json:"capital_per_day"`
	OpenCount     int            `json:"open_count"`
	OpenInvested  float64        `json:"open_invested"`
	UnrealizedPnL float64        `json:"unrealized_pnl"`
	CarriedCount  int            `json:"carried_count"`
	ClosedCount   int            `json:"closed_count"`
	RealizedPnL   float64        `json:"realized_pnl"`
	TotalCharges  float64        `json:"total_charges"`
	NetRealized   float64        `json:"net_realized"`
	ReturnPct     float64        `json:"return_pct"`
	Wins          int            `json:"wins"`
	WinRate       float64        `json:"win_rate"`
	Open          []positionView `json:"open"`
	Closed        []positionView `json:"closed"`

	ScanDate     string           `json:"scan_date"`
	ScanTime     string           `json:"scan_time"`
	ScannedCount int              `json:"scanned_count"`
	TradedCount  int              `json:"traded_count"`
	Scan         []store.ScanRow  `json:"scan"`
	SLEvents     []store.SLEvent  `json:"sl_events"`
	Daily        []store.DailyPnL `json:"daily"` // equity-curve series (net per day)
}

func view(p model.Position) positionView {
	v := positionView{
		ID: p.ID, Symbol: p.Symbol, Qty: p.Qty, EntryPrice: r2(p.EntryPrice), SLPrice: r2(p.SLPrice),
		Invested: r2(p.Invested()), TradeDate: p.TradeDate, CarryCount: p.CarryCount,
	}
	const dt = "02 Jan 15:04:05"
	if !p.EntryTime.IsZero() {
		v.EntryAt = p.EntryTime.In(config.IST).Format(dt)
	}
	// P&L only exists once closed — leave open positions at zero rather than
	// reporting a spurious -100% from a zero exit price.
	if p.Status == model.StatusClosed {
		v.ExitPrice = r2(p.ExitPrice)
		v.ExitReason = p.ExitReason
		v.PnL = r2(p.PnL)
		v.PnLPct = r2(p.PnLPct())
		v.Charges = r2(p.Charges)
		v.NetPnL = r2(p.NetPnL())
		v.NetPct = r2(p.NetPct())
		if !p.ExitTime.IsZero() {
			v.ExitAt = p.ExitTime.In(config.IST).Format(dt)
		}
	} else {
		v.PeakPrice = r2(p.PeakPrice)
		v.LastPrice = r2(p.LastPrice)
		v.UnrealPnL = r2(p.UnrealPnL())
		if p.EntryPrice > 0 && p.LastPrice > 0 {
			v.UnrealPct = r2((p.LastPrice - p.EntryPrice) / p.EntryPrice * 100)
		}
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
		out.UnrealizedPnL += p.UnrealPnL()
		if p.CarryCount > 0 {
			out.CarriedCount++
		}
	}
	out.OpenCount = len(open)

	var closedInvested float64
	for _, p := range closed {
		out.Closed = append(out.Closed, view(p))
		out.RealizedPnL += p.PnL
		out.TotalCharges += p.Charges
		closedInvested += p.Invested()
		if p.PnL > 0 {
			out.Wins++
		}
	}
	out.NetRealized = out.RealizedPnL - out.TotalCharges
	out.ClosedCount = len(closed)
	if closedInvested > 0 {
		out.ReturnPct = out.RealizedPnL / closedInvested * 100
	}
	if out.ClosedCount > 0 {
		out.WinRate = float64(out.Wins) / float64(out.ClosedCount) * 100
	}

	// Round every remaining float leaving the API.
	out.OpenInvested = r2(out.OpenInvested)
	out.UnrealizedPnL = r2(out.UnrealizedPnL)
	out.RealizedPnL = r2(out.RealizedPnL)
	out.TotalCharges = r2(out.TotalCharges)
	out.NetRealized = r2(out.NetRealized)
	out.ReturnPct = r2(out.ReturnPct)
	out.WinRate = r2(out.WinRate)

	out.SLEvents, _ = s.store.SLEvents(20)
	for i := range out.SLEvents {
		e := &out.SLEvents[i]
		e.Price, e.OldSL, e.NewSL = r2(e.Price), r2(e.OldSL), r2(e.NewSL)
	}
	out.Daily, _ = s.store.DailyNetPnL()
	for i := range out.Daily {
		out.Daily[i].Net = r2(out.Daily[i].Net)
	}

	// Most recent daily scan (scanned vs traded vs dropped/held).
	if d, err := s.store.LatestScanDate(); err == nil && d != "" {
		if scan, err := s.store.ScanByDate(d); err == nil {
			out.ScanDate = d
			out.ScanTime, _ = s.store.ScanTime(d)
			out.Scan = scan
			out.ScannedCount = len(scan)
			for _, r := range scan {
				if r.Outcome == "traded" {
					out.TradedCount++
				}
			}
		}
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
