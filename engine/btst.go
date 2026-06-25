// Package engine orchestrates the BTST daily cycle: the 3:20 PM entry (this file)
// and the next-day 3:20 PM exit (P3). It ties together the Chartink scanner, the
// broker (paper or live), the SQLite store, and Telegram reporting.
package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"bnf_go_engine/broker"
	"bnf_go_engine/config"
	"bnf_go_engine/model"
	"bnf_go_engine/scanner"
	"bnf_go_engine/store"
)

// Engine holds the wired dependencies for the BTST cycle.
type Engine struct {
	Scraper *scanner.Scraper
	Broker  broker.Broker
	Store   *store.Store
	Notify  func(string) // Telegram sender (agents.SendTelegram)
	Quotes  quoteSource  // daily-OHLC source for exits (Yahoo in paper mode)

	// Gates wired in P4 (nil = disabled). MacroGate returning ok=false skips the
	// whole day; NewsFilter drops individual symbols, returning a reason per drop.
	MacroGate  func(ctx context.Context) (ok bool, reason string)
	NewsFilter func(ctx context.Context, names map[string]string) (dropped map[string]string)

	// ApproveBuy (optional) gates BUY placement on a human decision: it receives
	// the proposed basket and returns true to PROCEED, false to HOLD. nil =
	// unconditional placement. SELL is never gated.
	ApproveBuy func(ctx context.Context, proposal string) bool
}

// forceKeyT marks a context as a forced manual run.
type forceKeyT struct{}

// WithForceEntry marks ctx so RunEntry bypasses the once-per-day guard and
// re-runs cleanly (purging today's positions + scan first). Used by the manual
// /api/run trigger for repeated testing.
func WithForceEntry(ctx context.Context) context.Context {
	return context.WithValue(ctx, forceKeyT{}, true)
}

func forced(ctx context.Context) bool {
	v, _ := ctx.Value(forceKeyT{}).(bool)
	return v
}

// RunEntry executes the 3:20 PM entry: scan → gates → equal-weight sizing →
// BUY + SL → persist → Telegram report. It is idempotent per trade date, unless
// the context is marked WithForceEntry (manual re-run).
func (e *Engine) RunEntry(ctx context.Context) error {
	now := config.NowIST()
	date := now.Format("2006-01-02")

	if forced(ctx) {
		// Clean slate so a manual re-test doesn't duplicate today's rows.
		if err := e.Store.PurgeDate(date); err != nil {
			return fmt.Errorf("force purge: %w", err)
		}
	} else if done, err := e.Store.HasEntryFor(date); err != nil {
		return fmt.Errorf("entry idempotency check: %w", err)
	} else if done {
		e.notify(fmt.Sprintf("ℹ️ BTST entry for %s already done — skipping duplicate run.", date))
		return nil
	}

	// ── Tier-1 macro gate ──────────────────────────────────────────────
	if e.MacroGate != nil {
		if ok, reason := e.MacroGate(ctx); !ok {
			e.notify(fmt.Sprintf("⚠️ *BTST Skipped* — %s [%s]\nReason: %s\nNo trades placed.",
				date, e.modeTag(), reason))
			return nil
		}
	}

	// ── Scan ChartInk ──────────────────────────────────────────────────
	stocks, err := e.Scraper.Fetch(ctx, config.BTSTMaxStocks)
	if err != nil {
		e.notify(fmt.Sprintf("🚨 *BTST scan failed* — %s\n%v", date, err))
		return err
	}
	if len(stocks) == 0 {
		e.notify(fmt.Sprintf("⚠️ *BTST* — %s [%s]\nScreener returned 0 stocks. No trades.",
			date, e.modeTag()))
		return nil
	}

	// ── Tier-2 per-stock news filter ───────────────────────────────────
	dropped := map[string]string{}
	if e.NewsFilter != nil {
		names := make(map[string]string, len(stocks))
		for _, s := range stocks {
			names[s.Symbol] = s.Name
		}
		dropped = e.NewsFilter(ctx, names)
	}
	kept := stocks[:0:0]
	for _, s := range stocks {
		if _, bad := dropped[s.Symbol]; !bad {
			kept = append(kept, s)
		}
	}
	if len(kept) == 0 {
		e.recordScan(date, stocks, nil, dropped, false)
		e.notify(fmt.Sprintf("⚠️ *BTST* — %s [%s]\nAll %d stocks dropped by news filter. No trades.",
			date, e.modeTag(), len(stocks)))
		return nil
	}

	// ── Equal-weight sizing: ₹CapitalPerDay ÷ N ────────────────────────
	n := len(kept)
	perStock := config.BTSTCapitalPerDay / float64(n)

	// Build the proposed basket first (qty + estimated entry/SL), so it can be
	// shown for approval before any order is placed.
	var plan []planItem
	var planDeployed float64
	for _, s := range kept {
		qty := int(perStock / s.Close)
		if qty < 1 {
			dropped[s.Symbol] = "price > per-stock budget"
			continue
		}
		plan = append(plan, planItem{stock: s, qty: qty, estSL: s.Close * (1 - config.BTSTStopLossPct/100)})
		planDeployed += s.Close * float64(qty)
	}
	if len(plan) == 0 {
		e.recordScan(date, stocks, nil, dropped, false)
		e.notify(fmt.Sprintf("⚠️ *BTST* — %s [%s]\nNo affordable stocks. No trades.", date, e.modeTag()))
		return nil
	}

	// ── Manual BUY approval (Telegram) ─────────────────────────────────
	if e.ApproveBuy != nil {
		proposal := e.proposalReport(date, plan, planDeployed)
		if !e.ApproveBuy(ctx, proposal) {
			e.recordScan(date, stocks, nil, dropped, true)
			e.notify(fmt.Sprintf("🛑 *BTST HELD* — %s [%s]\nNot approved. No BUY orders placed.",
				date, e.modeTag()))
			return nil
		}
	}

	// ── Place the approved basket ──────────────────────────────────────
	var placed []model.Position
	var deployed float64
	for _, it := range plan {
		s, qty := it.stock, it.qty
		// Inject reference price for the paper broker (live ignores this).
		if ps, ok := e.Broker.(broker.PriceSetter); ok {
			ps.SetPrice(s.Symbol, s.Close)
		}
		orderID, fill, err := e.Broker.PlaceMarketBuy(s.Symbol, qty)
		if err != nil {
			dropped[s.Symbol] = "buy failed: " + err.Error()
			continue
		}
		slPrice := fill * (1 - config.BTSTStopLossPct/100)
		if _, err := e.Broker.PlaceSLM(s.Symbol, qty, slPrice); err != nil {
			// Non-fatal: paper tracks SL in software; log via drop note.
			dropped[s.Symbol+" (SL)"] = "SL register failed: " + err.Error()
		}
		p := model.Position{
			Symbol: s.Symbol, Qty: qty, EntryPrice: fill, EntryTime: now,
			SLPrice: slPrice, TradeDate: date, Paper: e.Broker.IsPaper(),
			BuyOrderID: orderID, Status: model.StatusOpen,
		}
		if err := e.Store.SaveOpen(&p); err != nil {
			return fmt.Errorf("persist %s: %w", s.Symbol, err)
		}
		placed = append(placed, p)
		deployed += p.Invested()
	}

	tradedSet := make(map[string]bool, len(placed))
	for _, p := range placed {
		tradedSet[p.Symbol] = true
	}
	e.recordScan(date, stocks, tradedSet, dropped, false)

	e.notify(e.entryReport(date, len(stocks), placed, deployed, dropped))
	return nil
}

// recordScan persists the day's scanned stocks and their outcome (traded /
// dropped+reason / held) so the dashboard can show the full scan, not just trades.
func (e *Engine) recordScan(date string, stocks []scanner.Stock, traded map[string]bool, dropped map[string]string, held bool) {
	if e.Store == nil {
		return
	}
	rows := make([]store.ScanRow, 0, len(stocks))
	for _, s := range stocks {
		r := store.ScanRow{Date: date, Symbol: s.Symbol, Close: s.Close, Outcome: "dropped"}
		switch {
		case traded[s.Symbol]:
			r.Outcome = "traded"
		case dropped[s.Symbol] != "":
			r.Reason = dropped[s.Symbol]
		case held:
			r.Outcome, r.Reason = "held", "not approved"
		default:
			r.Outcome = "dropped"
		}
		rows = append(rows, r)
	}
	if err := e.Store.SaveScan(date, rows); err != nil {
		// Non-fatal: scan history is observability, not trade-critical.
		e.notify(fmt.Sprintf("⚠️ scan record failed for %s: %v", date, err))
	}
}

// planItem is one proposed BUY (set before placement, used for the approval message).
type planItem struct {
	stock scanner.Stock
	qty   int
	estSL float64
}

// proposalReport formats the basket sent to Telegram for human approval.
func (e *Engine) proposalReport(date string, plan []planItem, deployed float64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🟡 *BTST — Approval needed* — %s [%s]\n", date, e.modeTag())
	fmt.Fprintf(&b, "Proposed: %d stocks · ₹%s of ₹%s\n",
		len(plan), commaINR(deployed), commaINR(config.BTSTCapitalPerDay))
	b.WriteString("———\n")
	for _, it := range plan {
		fmt.Fprintf(&b, "`%-12s` qty:%d  ~entry:%.2f  ~SL:%.2f\n",
			it.stock.Symbol, it.qty, it.stock.Close, it.estSL)
	}
	return b.String()
}

func (e *Engine) entryReport(date string, scanned int, placed []model.Position,
	deployed float64, dropped map[string]string) string {

	var b strings.Builder
	fmt.Fprintf(&b, "🟢 *BTST Entry* — %s [%s]\n", date, e.modeTag())
	fmt.Fprintf(&b, "Traded: %d / %d (max %d)\n", len(placed), scanned, config.BTSTMaxStocks)
	fmt.Fprintf(&b, "Capital deployed: ₹%s of ₹%s\n",
		commaINR(deployed), commaINR(config.BTSTCapitalPerDay))
	b.WriteString("———\n")
	for _, p := range placed {
		fmt.Fprintf(&b, "`%-12s` qty:%d  entry:%.2f  SL:%.2f\n",
			p.Symbol, p.Qty, p.EntryPrice, p.SLPrice)
	}
	if len(dropped) > 0 {
		b.WriteString("———\nRemoved:\n")
		keys := make([]string, 0, len(dropped))
		for k := range dropped {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "• %s — %s\n", k, dropped[k])
		}
	}
	return b.String()
}

func (e *Engine) modeTag() string {
	if e.Broker.IsPaper() {
		return "PAPER"
	}
	return "LIVE"
}

func (e *Engine) notify(msg string) {
	if e.Notify != nil {
		e.Notify(msg)
	}
}

// commaINR formats a rupee amount with Indian-style grouping (e.g. 4,25,000).
func commaINR(v float64) string {
	n := int64(v + 0.5)
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	head := s[:len(s)-3]
	tail := s[len(s)-3:]
	var parts []string
	for len(head) > 2 {
		parts = append([]string{head[len(head)-2:]}, parts...)
		head = head[:len(head)-2]
	}
	if head != "" {
		parts = append([]string{head}, parts...)
	}
	return strings.Join(parts, ",") + "," + tail
}
