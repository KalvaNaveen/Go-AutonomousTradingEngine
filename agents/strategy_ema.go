package agents

import (
	"log"
	"math"

	"bnf_go_engine/config"
)

// EMAStrategy is the engine's SOLE entry setup: the pure-EMA pullback/bounce
// described in "Swing Trading Simplified" by Ankur Patel, Ch.3 (Momentum: The
// Market's Fuel, p.44-49).
//
// Book rules implemented:
//   • p.47 — "When the 10 and 20 EMAs are rising and the 10 is above the 20, it
//     is evident that a stock is in a strong uptrend."  → trend confirmation
//   • p.45 — "stocks tend to find support at the 10 EMA for the short term, and
//     the 20 or 50 EMA for the mid-term."               → pullback to a key EMA
//   • p.47 — "During the pullback period, prices typically decreased with low
//     volume."                                          → light-volume pullback
//   • p.45 — "This provides an opportunity for new buyers to enter the market."
//     → entry on the bounce off the EMA, SL just below the pullback support.
type EMAStrategy struct{}

func (e *EMAStrategy) Name() string { return "EMA_PULLBACK" }

func (e *EMAStrategy) Detect(token uint32, symbol string, ltp float64, regime string, ctx StrategyContext) *Signal {
	// Book Ch.5: suppress new entries on major event days.
	if ctx.IsMajorEventDay {
		return nil
	}
	closes := ctx.Cache.Closes[token]
	highs := ctx.Cache.Highs[token]
	lows := ctx.Cache.Lows[token]
	volumes := ctx.Cache.Volumes[token]
	if closes == nil || lows == nil {
		return nil
	}

	pullbackLow, formed := DetectEMAPullbackFromSlice(closes, highs, lows, volumes)
	if !formed {
		return nil
	}

	entryPrice := ltp
	if entryPrice <= 0 {
		entryPrice = closes[len(closes)-1]
	}
	// Book Ch.6/8: SL just below the pullback support (the EMA the stock bounced
	// off), clamped to the structural floor/ceiling.
	stopPrice := math.Max(pullbackLow*0.99, entryPrice*(1-config.SLCeilingPct/100))
	if stopPrice >= entryPrice {
		return nil
	}
	qty := computeRiskBasedQty(config.TotalCapital, ctx.CapitalMultiplier, entryPrice, stopPrice)
	if qty <= 0 {
		log.Printf("[EMA] %s: qty=0 — skipping", symbol)
		return nil
	}

	log.Printf("[EMA] PULLBACK BOUNCE: %s LTP=%.2f SL=%.2f Qty=%d (risk-based)", symbol, ltp, stopPrice, qty)
	return &Signal{
		Strategy:   "EMA_PULLBACK",
		Symbol:     symbol, Token: token, Regime: regime,
		EntryPrice: entryPrice, StopPrice: stopPrice,
		Qty: qty, Product: "CNC",
	}
}

// DetectEMAPullbackFromSlice implements the book's EMA pullback entry (Ch.3
// p.44-49) on pre-sliced OHLCV arrays. Pure-data form — used by both the live
// scanner and the backtest engine. Returns (pullbackLow, formed); the bounce
// itself is the entry signal (no separate breakout level).
func DetectEMAPullbackFromSlice(closes, highs, lows, volumes []float64) (pullbackLow float64, formed bool) {
	need := config.EMA50Period + 20
	if len(closes) < need || len(lows) < need {
		return
	}
	n := len(closes)
	lastClose := closes[n-1]
	prevClose := closes[n-2]

	ema10s := computeEMASeries(closes, config.EMA10Period)
	ema20s := computeEMASeries(closes, config.EMA20Period)
	ema50s := computeEMASeries(closes, config.EMA50Period)
	if len(ema10s) < 6 || len(ema20s) < 6 || len(ema50s) < 1 {
		return
	}
	ema10 := ema10s[len(ema10s)-1]
	ema20 := ema20s[len(ema20s)-1]
	ema50 := ema50s[len(ema50s)-1]
	ema10Prev := ema10s[len(ema10s)-6] // ~5 bars ago
	ema20Prev := ema20s[len(ema20s)-6]

	// ── Ch.3 p.47: confirmed uptrend — 10 EMA > 20 EMA > 50 EMA, 10 & 20 rising ──
	if !(ema10 > ema20 && ema20 > ema50) {
		return
	}
	if !(ema10 > ema10Prev && ema20 > ema20Prev) {
		return // EMAs must be sloping higher
	}
	if lastClose < ema50 {
		return // price must remain above the long-term trend
	}

	// ── Ch.3 p.45-47: pullback to a key EMA (10/20/50) — the average "catches up"
	// and acts as support. Over the recent window the low must reach an EMA band. ──
	const pullbackWindow = 7
	const touchTol = 0.02 // within 2% of an EMA = "found support"
	touched := false
	pullbackLow = lows[n-1]
	for i := n - pullbackWindow; i < n; i++ {
		if i < 0 {
			continue
		}
		lo := lows[i]
		if lo < pullbackLow {
			pullbackLow = lo
		}
		for _, ema := range []float64{ema10, ema20, ema50} {
			if ema <= 0 {
				continue
			}
			// low dipped into the EMA support band, or dipped below then closed back near it
			if (lo <= ema*(1+touchTol) && lo >= ema*(1-touchTol)) ||
				(lo <= ema && closes[i] >= ema*(1-touchTol)) {
				touched = true
			}
		}
	}
	if !touched {
		return
	}

	// Prior extension: before the pullback the stock was meaningfully ABOVE the
	// 10 EMA — a genuine momentum-phase pullback, not a stock hugging the average.
	idx := n - pullbackWindow - 3
	if idx < 0 {
		return
	}
	if closes[idx] < ema10*1.01 {
		return
	}

	// ── Ch.3 p.45/47: the pullback happens on LIGHT (declining) volume ──
	if volumes != nil && len(volumes) >= 25 {
		var pullVol, baseVol float64
		pc := 0
		for i := n - pullbackWindow; i < n; i++ {
			if i >= 0 {
				pullVol += volumes[i]
				pc++
			}
		}
		bc := 0
		for i := n - 25; i < n-pullbackWindow; i++ {
			if i >= 0 {
				baseVol += volumes[i]
				bc++
			}
		}
		if pc > 0 && bc > 0 {
			pullVol /= float64(pc)
			baseVol /= float64(bc)
			if baseVol > 0 && pullVol >= baseVol {
				return // pullback not on lighter volume — not a clean shallow pullback
			}
		}
	}

	// ── Bounce confirmation: price reclaims the fast EMA on an up (green) bar ──
	// (book: the stock "finds support... and continues its upward trajectory")
	if lastClose <= ema10 {
		return
	}
	if lastClose <= prevClose {
		return
	}

	formed = true
	return
}
