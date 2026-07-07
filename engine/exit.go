package engine

import (
	"context"
	"fmt"
	"strings"

	"bnf_go_engine/broker"
	"bnf_go_engine/config"
	"bnf_go_engine/model"
	"bnf_go_engine/quotes"
)

// Quotes is the daily-OHLC source used to evaluate exits (Yahoo in paper mode).
// Wired separately so live mode can swap in a Kite-based source if desired.
type quoteSource interface {
	Daily(ctx context.Context, nseSymbol string) (quotes.OHLC, error)
}

// WithQuotes wires the daily-OHLC source and returns the engine for chaining.
func (e *Engine) WithQuotes(q quoteSource) *Engine { e.Quotes = q; return e }

// RunExit squares off open BTST positions at the next-day 3:20 PM.
//
// For each open position whose trade date is before today, it fetches the day's
// OHLC: if the low breached the (trailing) stop, the exit is booked at the SL
// price (reason stoploss — the safety net when the intraday monitor missed it);
// otherwise it squares off at the current price (reason squareoff).
//
// includeToday=true ignores the "must be a prior trade date" guard — used only
// for testing a same-session round-trip.
func (e *Engine) RunExit(ctx context.Context, includeToday bool) error {
	return e.exitEligible(ctx, nil, includeToday)
}

// exitEligible is RunExit with carry-over netting: positions whose symbol is in
// `carried` are re-listed by today's scan and are NOT sold (the hold continues).
func (e *Engine) exitEligible(ctx context.Context, carried map[string]bool, includeToday bool) error {
	if e.Quotes == nil {
		return fmt.Errorf("exit: no quote source wired")
	}
	today := config.NowIST().Format("2006-01-02")

	open, err := e.Store.OpenPositions()
	if err != nil {
		return fmt.Errorf("load open positions: %w", err)
	}

	var closed []model.Position
	var skipped []string
	for _, p := range open {
		if !includeToday && p.TradeDate >= today {
			continue // bought today — BTST holds until next trading day
		}
		if carried[p.Symbol] {
			continue // re-listed by today's scan — sell skipped, hold continues
		}

		ohlc, err := e.Quotes.Daily(ctx, p.Symbol)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (quote: %v)", p.Symbol, err))
			continue
		}

		var exitPrice float64
		var reason string
		if ohlc.Low > 0 && ohlc.Low <= p.SLPrice {
			// Stop-loss breached intraday — assume filled at the SL trigger.
			exitPrice = p.SLPrice
			reason = model.ExitStopLoss
		} else {
			// Square off at the current (3:20 PM) price.
			exitPrice = ohlc.Last
			reason = model.ExitSquareOff
		}
		if exitPrice <= 0 {
			skipped = append(skipped, p.Symbol+" (no exit price)")
			continue
		}

		// Drive the broker sell (paper fills at the injected price; live sells at market).
		if ps, ok := e.Broker.(broker.PriceSetter); ok {
			ps.SetPrice(p.Symbol, exitPrice)
		}
		fill, err := e.Broker.SquareOff(p.Symbol, p.Qty)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s (sell: %v)", p.Symbol, err))
			continue
		}

		p.ExitPrice = fill
		p.ExitReason = reason
		p.ExitTime = config.NowIST()
		p.PnL = (fill - p.EntryPrice) * float64(p.Qty)
		p.Status = model.StatusClosed
		if err := e.Store.ClosePosition(&p); err != nil {
			return fmt.Errorf("persist close %s: %w", p.Symbol, err)
		}
		closed = append(closed, p)
	}

	if len(closed) == 0 && len(skipped) == 0 {
		return nil // nothing eligible — silent (no spurious report)
	}
	e.notify(e.exitReport(today, closed, skipped))
	return nil
}

func (e *Engine) exitReport(date string, closed []model.Position, skipped []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🔴 *BTST Exit* — %s [%s]\n———\n", date, e.modeTag())

	var net, invested float64
	wins := 0
	for _, p := range closed {
		tag := "🟢"
		if p.PnL < 0 {
			tag = "🔴"
		} else {
			wins++
		}
		slMark := ""
		if p.ExitReason == model.ExitStopLoss {
			slMark = " ⛔SL"
		}
		fmt.Fprintf(&b, "%s `%-12s` %.2f→%.2f  %+.0f (%+.1f%%)%s\n",
			tag, p.Symbol, p.EntryPrice, p.ExitPrice, p.PnL, p.PnLPct(), slMark)
		net += p.PnL
		invested += p.Invested()
	}
	b.WriteString("———\n")
	pct := 0.0
	if invested > 0 {
		pct = net / invested * 100
	}
	winRate := 0.0
	if len(closed) > 0 {
		winRate = float64(wins) / float64(len(closed)) * 100
	}
	fmt.Fprintf(&b, "Net P&L: ₹%s (%+.2f%%)\n", commaINR(net), pct)
	fmt.Fprintf(&b, "Wins: %d/%d (%.0f%%)\n", wins, len(closed), winRate)
	if len(skipped) > 0 {
		b.WriteString("———\nSkipped:\n")
		for _, s := range skipped {
			fmt.Fprintf(&b, "• %s\n", s)
		}
	}
	return b.String()
}
