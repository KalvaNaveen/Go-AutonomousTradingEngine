package engine

import (
	"context"
	"fmt"
	"log"

	"bnf_go_engine/broker"
	"bnf_go_engine/config"
	"bnf_go_engine/model"
)

// Trail computes the ratcheted watermark and stop for a position given a fresh
// quote. Pure function — unit-tested independently of I/O.
//
//   - watermark = max(peak, last) — and, on days AFTER the entry day, also the
//     session high (we held through the whole session, so the high is ours; on
//     the entry day the pre-15:20 high predates ownership and must be ignored).
//   - stop = watermark × (1 − pct/100), and it only ever moves UP.
func Trail(peak, sl, last, dayHigh float64, entryDay bool, pct float64) (newPeak, newSL float64) {
	newPeak = peak
	if !entryDay && dayHigh > newPeak {
		newPeak = dayHigh
	}
	if last > newPeak {
		newPeak = last
	}
	newSL = sl
	if s := newPeak * (1 - pct/100); s > newSL {
		newSL = s
	}
	return newPeak, newSL
}

// MonitorOnce polls every open position once: updates the last price, ratchets
// the trailing stop (writing an sl_events audit row on each move), and exits a
// position immediately when the price has fallen to/below its stop.
//
// It is stateless — positions are re-loaded from the store each tick — so it
// survives restarts. The caller gates it to market hours on trading days.
func (e *Engine) MonitorOnce(ctx context.Context) {
	if e.Quotes == nil || e.Store == nil {
		return
	}
	open, err := e.Store.OpenPositions()
	if err != nil {
		log.Printf("[Monitor] load positions: %v", err)
		return
	}
	if len(open) == 0 {
		return
	}
	today := config.NowIST().Format("2006-01-02")

	for _, p := range open {
		if ctx.Err() != nil {
			return
		}
		ohlc, err := e.Quotes.Daily(ctx, p.Symbol)
		if err != nil || ohlc.Last <= 0 {
			continue // quote hiccup — try again next tick
		}

		oldSL := p.SLPrice
		p.PeakPrice, p.SLPrice = Trail(p.PeakPrice, p.SLPrice, ohlc.Last, ohlc.High,
			p.TradeDate == today, config.BTSTStopLossPct)
		p.LastPrice = ohlc.Last

		if err := e.Store.UpdateTrail(&p, config.NowIST(), oldSL); err != nil {
			log.Printf("[Monitor] trail persist %s: %v", p.Symbol, err)
		}
		if p.SLPrice > oldSL {
			log.Printf("[Monitor] %s SL trailed %.2f → %.2f (peak %.2f, last %.2f)",
				p.Symbol, oldSL, p.SLPrice, p.PeakPrice, ohlc.Last)
		}

		// ── Breach → exit now at the observed price ─────────────────────
		if ohlc.Last <= p.SLPrice {
			if ps, ok := e.Broker.(broker.PriceSetter); ok {
				ps.SetPrice(p.Symbol, ohlc.Last)
			}
			fill, err := e.Broker.SquareOff(p.Symbol, p.Qty)
			if err != nil {
				log.Printf("[Monitor] %s SL-breach sell failed: %v", p.Symbol, err)
				continue
			}
			p.ExitPrice = fill
			p.ExitReason = model.ExitStopLoss
			p.ExitTime = config.NowIST()
			p.PnL = (fill - p.EntryPrice) * float64(p.Qty)
			p.Charges = model.CNCCharges(p.Invested(), fill*float64(p.Qty))
			p.Status = model.StatusClosed
			if err := e.Store.ClosePosition(&p); err != nil {
				log.Printf("[Monitor] persist close %s: %v", p.Symbol, err)
				continue
			}
			e.notify(fmt.Sprintf("⛔ *Trailing SL hit* — %s [%s]\n`%s` exit %.2f (SL %.2f, peak %.2f) → P&L %+.0f",
				today, e.modeTag(), p.Symbol, fill, p.SLPrice, p.PeakPrice, p.PnL))
		}
	}
}
