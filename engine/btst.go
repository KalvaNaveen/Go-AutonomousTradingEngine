// Package engine orchestrates the BTST daily cycle: the 3:20 PM entry (this file)
// and the next-day 3:20 PM exit (P3). It ties together the Chartink scanner, the
// broker (paper or live), the SQLite store, and the report sink.
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

// StockSource abstracts the scan (single scraper, multi-screener union, or a
// test stub). max is the per-screener cap.
type StockSource interface {
	Fetch(ctx context.Context, max int) ([]scanner.Stock, error)
}

// Engine holds the wired dependencies for the BTST cycle.
type Engine struct {
	Scanner StockSource
	Broker  broker.Broker
	Store   *store.Store
	Notify  func(string) // report sink (logs + dashboard)
	Quotes  quoteSource  // daily-OHLC source for exits/monitor (Yahoo in paper mode)

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

// RunCycle executes the full 3:20 PM daily cycle with carry-over netting:
//
//  1. Fetch the fresh buy list (distinct union of all screeners) FIRST.
//  2. Holdings still on the list are CARRIED — not sold, not re-bought.
//  3. Holdings that fell off the list are squared off (sell before buy).
//  4. New buys = list minus anything already held; ₹CapitalPerDay ÷ N over
//     the NEW buys only (carried positions consume no fresh capital).
//
// Idempotent per trade date, unless the context is marked WithForceEntry.
// If the scan fails entirely, all eligible holdings are squared off (classic
// BTST discipline — never hold blind) and the error is returned.
func (e *Engine) RunCycle(ctx context.Context) error {
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
		e.notify(fmt.Sprintf("ℹ️ BTST cycle for %s already done — skipping duplicate run.", date))
		return nil
	}

	// ── Tier-1 macro gate (dormant unless enabled) ─────────────────────
	if e.MacroGate != nil {
		if ok, reason := e.MacroGate(ctx); !ok {
			e.notify(fmt.Sprintf("⚠️ *BTST Skipped* — %s [%s]\nReason: %s\nNo new buys.",
				date, e.modeTag(), reason))
			// Still square off eligible holdings — the exit is never gated.
			return e.exitEligible(ctx, nil, false)
		}
	}

	// ── 1. Scan (fresh buy list decides both sells and buys) ───────────
	stocks, err := e.Scanner.Fetch(ctx, config.BTSTMaxStocks)
	if err != nil {
		e.notify(fmt.Sprintf("🚨 *BTST scan failed* — %s\n%v\nSquaring off all eligible holdings (cannot determine carries).", date, err))
		if exitErr := e.exitEligible(ctx, nil, false); exitErr != nil {
			e.notify(fmt.Sprintf("🚨 fallback exit also failed: %v", exitErr))
		}
		return err
	}

	// ── Tier-2 per-stock news filter (dormant unless enabled) ──────────
	dropped := map[string]string{}
	if e.NewsFilter != nil && len(stocks) > 0 {
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

	// ── 2+3. Carry netting, then sell what fell off the list ───────────
	open, err := e.Store.OpenPositions()
	if err != nil {
		return fmt.Errorf("load holdings: %w", err)
	}
	keptSet := make(map[string]bool, len(kept))
	for _, s := range kept {
		keptSet[s.Symbol] = true
	}
	heldSet := make(map[string]bool, len(open))
	carried := map[string]bool{}
	for _, p := range open {
		heldSet[p.Symbol] = true
		if keptSet[p.Symbol] && p.TradeDate < date {
			carried[p.Symbol] = true
			if err := e.Store.MarkCarried(p.ID); err != nil {
				e.notify(fmt.Sprintf("⚠️ carry mark failed for %s: %v", p.Symbol, err))
			}
		}
	}
	if err := e.exitEligible(ctx, carried, false); err != nil {
		e.notify(fmt.Sprintf("🚨 exit leg failed: %v", err))
	}

	// ── 4. New buys = list minus anything already held ─────────────────
	var buys []scanner.Stock
	for _, s := range kept {
		if !heldSet[s.Symbol] {
			buys = append(buys, s)
		}
	}
	if len(buys) == 0 {
		e.recordScan(date, stocks, nil, carried, dropped, false)
		e.notify(fmt.Sprintf("ℹ️ *BTST* — %s [%s]\nScan %d, carried %d, no NEW stocks to buy.",
			date, e.modeTag(), len(stocks), len(carried)))
		return nil
	}

	// Equal-weight sizing over NEW buys only.
	perStock := config.BTSTCapitalPerDay / float64(len(buys))
	var plan []planItem
	var planDeployed float64
	for _, s := range buys {
		qty := int(perStock / s.Close)
		if qty < 1 {
			dropped[s.Symbol] = "price > per-stock budget"
			continue
		}
		plan = append(plan, planItem{stock: s, qty: qty, estSL: s.Close * (1 - config.BTSTStopLossPct/100)})
		planDeployed += s.Close * float64(qty)
	}
	if len(plan) == 0 {
		e.recordScan(date, stocks, nil, carried, dropped, false)
		e.notify(fmt.Sprintf("⚠️ *BTST* — %s [%s]\nNo affordable new stocks. No buys.", date, e.modeTag()))
		return nil
	}

	// ── Manual BUY approval (optional hook; nil = trade unconditionally) ──
	if e.ApproveBuy != nil {
		proposal := e.proposalReport(date, plan, planDeployed)
		if !e.ApproveBuy(ctx, proposal) {
			e.recordScan(date, stocks, nil, carried, dropped, true)
			e.notify(fmt.Sprintf("🛑 *BTST HELD* — %s [%s]\nNot approved. No BUY orders placed.",
				date, e.modeTag()))
			return nil
		}
	}

	// ── Place the basket (initial trailing stop = entry × (1 − pct)) ───
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
			// Non-fatal: the software monitor owns the trailing stop anyway.
			dropped[s.Symbol+" (SL)"] = "SL register failed: " + err.Error()
		}
		p := model.Position{
			Symbol: s.Symbol, Qty: qty, EntryPrice: fill, EntryTime: now,
			SLPrice: slPrice, PeakPrice: fill, LastPrice: fill,
			TradeDate: date, Paper: e.Broker.IsPaper(),
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
	e.recordScan(date, stocks, tradedSet, carried, dropped, false)

	e.notify(e.entryReport(date, len(stocks), len(carried), placed, deployed, dropped))
	return nil
}

// recordScan persists the day's scanned stocks and their outcome (traded /
// carried / dropped+reason / held) plus source screener, so the dashboard can
// show the full scan — the system's audit trail.
func (e *Engine) recordScan(date string, stocks []scanner.Stock, traded, carried map[string]bool, dropped map[string]string, held bool) {
	if e.Store == nil {
		return
	}
	rows := make([]store.ScanRow, 0, len(stocks))
	for _, s := range stocks {
		r := store.ScanRow{Date: date, Symbol: s.Symbol, Close: s.Close, PerChange: s.PerChange,
			Source: s.Source, Outcome: "dropped"}
		switch {
		case traded[s.Symbol]:
			r.Outcome = "traded"
		case carried[s.Symbol]:
			r.Outcome, r.Reason = "carried", "already held — sell skipped"
		case dropped[s.Symbol] != "":
			r.Reason = dropped[s.Symbol]
		case held:
			r.Outcome, r.Reason = "held", "not approved"
		default:
			r.Outcome = "dropped"
		}
		rows = append(rows, r)
	}
	if err := e.Store.SaveScan(date, config.NowIST(), rows); err != nil {
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

// proposalReport formats the proposed basket for the approval hook.
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

func (e *Engine) entryReport(date string, scanned, carried int, placed []model.Position,
	deployed float64, dropped map[string]string) string {

	var b strings.Builder
	fmt.Fprintf(&b, "🟢 *BTST Entry* — %s [%s]\n", date, e.modeTag())
	fmt.Fprintf(&b, "Scanned: %d · Carried: %d · New buys: %d\n", scanned, carried, len(placed))
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
