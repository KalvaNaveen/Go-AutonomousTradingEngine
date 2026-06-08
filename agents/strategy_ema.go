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

func (e *EMAStrategy) Detect(token uint32, symbol string, ltp float64, ctx StrategyContext) *Signal {
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

	opens := ctx.Cache.Opens[token]
	pullbackLow, formed := DetectEMAPullbackFromSlice(opens, closes, highs, lows, volumes)
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
		Symbol:     symbol, Token: token,
		EntryPrice: entryPrice, StopPrice: stopPrice,
		Qty: qty, Product: "CNC",
	}
}

// DetectEMAPullbackFromSlice implements the book's EMA pullback entry (Ch.3
// p.44-49) on pre-sliced OHLCV arrays. Pure-data form — used by both the live
// scanner and the backtest engine. Returns (pullbackLow, formed); the bounce
// itself is the entry signal (no separate breakout level).
// DetectEMAPullbackFromSlice — strict Book Ch.3 p.44-49 implementation.
//
// Three conditions, exactly as written:
//  1. Uptrend: 10 EMA > 20 EMA, both rising          (p.47)
//  2. Pullback: price touched 10 or 20 EMA on light volume (p.45-47)
//  3. Bounce:  green candle that closes above 10 EMA  (p.47)
func DetectEMAPullbackFromSlice(opens, closes, highs, lows, volumes []float64) (pullbackLow float64, formed bool) {
	need := config.EMA20Period + 10
	if len(closes) < need || len(lows) < need {
		return
	}
	n := len(closes)
	lastClose := closes[n-1]
	prevClose := closes[n-2]

	ema10s := computeEMASeries(closes, config.EMA10Period)
	ema20s := computeEMASeries(closes, config.EMA20Period)
	if len(ema10s) < 6 || len(ema20s) < 6 {
		return
	}
	ema10 := ema10s[len(ema10s)-1]
	ema20 := ema20s[len(ema20s)-1]
	ema10Prev := ema10s[len(ema10s)-6]
	ema20Prev := ema20s[len(ema20s)-6]

	// ── Rule 1: 10 EMA > 20 EMA, both rising (Ch.3 p.47) ────────────────────
	if ema10 <= ema20 {
		return
	}
	if ema10 <= ema10Prev || ema20 <= ema20Prev {
		return
	}

	// ── Rule 2: Pullback — price touched 10 or 20 EMA on light volume ────────
	// Book p.45: "stocks tend to find support at the 10 EMA for the short term,
	// and the 20 EMA for the mid-term."
	// Book p.47: "During the pullback period, prices typically decreased with low volume."
	const pullbackWindow = 10 // look back 10 bars for an EMA touch
	const touchTol = 0.02     // within 2% counts as "found support"
	touched := false
	pullbackLow = lows[n-1]
	for i := n - pullbackWindow; i < n-1; i++ { // exclude today's bounce bar
		if i < 0 {
			continue
		}
		lo := lows[i]
		if lo < pullbackLow {
			pullbackLow = lo
		}
		for _, ema := range []float64{ema10, ema20} {
			if ema <= 0 {
				continue
			}
			// Low dipped into the EMA support band
			if lo <= ema*(1+touchTol) && lo >= ema*(1-touchTol) {
				touched = true
			}
			// Or dipped below EMA but closed back near it (wick touch)
			if lo < ema && closes[i] >= ema*(1-touchTol) {
				touched = true
			}
		}
	}
	if !touched {
		return
	}

	// Light volume during pullback (book p.47)
	if volumes != nil && len(volumes) >= n && n >= pullbackWindow+10 {
		var pullVol, baseVol float64
		pc, bc := 0, 0
		for i := n - pullbackWindow; i < n-1; i++ {
			if i >= 0 {
				pullVol += volumes[i]
				pc++
			}
		}
		for i := n - pullbackWindow - 10; i < n-pullbackWindow; i++ {
			if i >= 0 {
				baseVol += volumes[i]
				bc++
			}
		}
		if pc > 0 && bc > 0 && baseVol/float64(bc) > 0 {
			if pullVol/float64(pc) >= baseVol/float64(bc) {
				return // pullback not on lighter volume
			}
		}
	}

	// ── Rule 3: Bounce — green candle closing above 10 EMA (Ch.3 p.47) ──────
	// "This provides an opportunity for new buyers to enter the market."
	if lastClose <= ema10 {
		return // must close back above the fast EMA
	}
	if lastClose <= prevClose {
		return // must be an up day vs prior close
	}
	// Green candle: close > open (same bar)
	if opens != nil && len(opens) >= n && opens[n-1] > 0 {
		if lastClose <= opens[n-1] {
			return
		}
	}

	formed = true
	return
}
