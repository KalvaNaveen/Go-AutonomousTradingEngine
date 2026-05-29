package agents

import (
	"log"
	"time"

	"bnf_go_engine/config"
)

// FlatBaseStrategy detects stocks that have consolidated in a very tight,
// sideways range for 5+ weeks while volume dried up — above SMA200.
// Book Ch.4 (p.120): "One of the most reliable continuation patterns.
// The stock has digested its prior gains and is ready for the next leg up."
//
// Unlike IPO Base (which is limited to recent IPOs), Flat Base applies to
// any established stock that forms a tight range above its long-term trend.
// Entry: breakout above the flat top (resistance).
// This is a mini base → trail exit with 20 EMA.
type FlatBaseStrategy struct{}

func (f *FlatBaseStrategy) Name() string { return "FLAT_BASE" }

func (f *FlatBaseStrategy) Detect(token uint32, symbol string, ltp float64, regime string, ctx StrategyContext) *Signal {
	// Book Ch.5: suppress all new entries on major event days.
	if ctx.IsMajorEventDay {
		return nil
	}
	// Skip IPO stocks — handled by IPOBaseStrategy
	if ctx.IPOSymbols != nil && ctx.IPOSymbols[symbol] {
		return nil
	}

	closes, cOk := ctx.Cache.Closes[token]
	highs, hOk := ctx.Cache.Highs[token]
	volumes := ctx.Cache.Volumes[token]
	if !cOk || !hOk || len(closes) < config.FlatBaseMinBars+config.RegimeSMAPeriod+5 {
		return nil
	}

	flatTop, flatLow, formed := DetectFlatBaseFromSlice(closes, highs, volumes)
	if !formed {
		return nil
	}

	// Entry: LTP must break above the flat top resistance
	if ltp <= flatTop {
		return nil
	}

	// Book Ch.4 p.120 + Ch.11 p.272: Breakout must be on expanding volume.
	// Time-paced: compare projected daily volume (current ÷ fraction of day) to 20-day avg.
	// This rejects quiet, low-conviction drifts above the flat top.
	if ctx.GetVolume != nil {
		avgVol := ctx.Cache.AvgVol[token]
		if avgVol > 0 {
			currentVol := float64(ctx.GetVolume(token))
			if currentVol > 0 {
				now := config.NowIST()
				open := time.Date(now.Year(), now.Month(), now.Day(), 9, 15, 0, 0, now.Location())
				fraction := now.Sub(open).Minutes() / 375.0
				if fraction > 1.0 {
					fraction = 1.0
				}
				if fraction < 0.05 {
					fraction = 0.05
				}
				if currentVol/fraction < avgVol*config.VolumeSpikeMultiplier {
					return nil // Flat base breakout on weak volume — likely false
				}
			}
		}
	}

	entryPrice := ltp * 1.002
	stopPrice := config.ComputeStructuralSL(entryPrice, flatLow)
	if stopPrice >= entryPrice {
		return nil
	}

	qty := computeRiskBasedQty(config.TotalCapital, ctx.CapitalMultiplier, entryPrice, stopPrice)
	if qty < 1 {
		log.Printf("[FLAT] %s: qty=0 — skipping", symbol)
		return nil
	}

	log.Printf("[FLAT] BREAKOUT: %s LTP=%.2f FlatTop=%.2f FlatLow=%.2f SL=%.2f Qty=%d",
		symbol, ltp, flatTop, flatLow, stopPrice, qty)

	return &Signal{
		Strategy:    "FLAT_BASE",
		Symbol:      symbol, Token: token, Regime: regime,
		EntryPrice:  entryPrice, StopPrice: stopPrice,
		Qty:         qty, Product: "CNC",
		IsLargeBase: false, // Tight base → trail with 20 EMA
	}
}

// DetectFlatBaseFromSlice is the pure-data form used by the backtest engine.
// Returns (flatTop, flatLow, formed).
//
// Flat Base logic (Book Ch.4 p.120):
//  1. The stock must be above its 200-day SMA (long-term uptrend confirmed).
//  2. In the most recent FlatBaseMinBars–FlatBaseMaxBars bars, the total
//     price range (high − low) must be < FlatBaseMaxRangePct% of the high.
//     This is the "flat" — a very tight consolidation.
//  3. Volume must have contracted during the base (no distribution).
//  4. The EMA20 should be rising (stock is coiling, not topping).
//  5. Entry: LTP breaks above the flat top (resistance of the base).
func DetectFlatBaseFromSlice(closes, highs, volumes []float64) (flatTop, flatLow float64, formed bool) {
	n := len(closes)
	if n < config.FlatBaseMinBars+config.RegimeSMAPeriod+5 {
		return
	}

	// ── Step 1: Stock must be above its 200-day SMA ──────────────────────────
	if n >= config.RegimeSMAPeriod {
		sma200 := computeSMA(closes, config.RegimeSMAPeriod)
		if sma200 > 0 && closes[n-1] < sma200 {
			return // Below SMA200 — long-term downtrend, not a base
		}
	}

	// ── Step 2: Find a valid flat base in the recent bars ───────────────────
	// Search windows: try lengths from FlatBaseMinBars to FlatBaseMaxBars.
	// Use the most recent qualifying window.
	for baseLen := config.FlatBaseMinBars; baseLen <= config.FlatBaseMaxBars; baseLen++ {
		if n < baseLen+5 {
			break
		}
		startIdx := n - baseLen

		// Compute high and low over the base window
		baseH := highs[startIdx]
		baseL := closes[startIdx]
		for i := startIdx; i < n; i++ {
			if highs[i] > baseH {
				baseH = highs[i]
			}
			if closes[i] < baseL {
				baseL = closes[i]
			}
		}

		if baseH <= 0 {
			continue
		}

		// Range check: must be < FlatBaseMaxRangePct%
		rangePct := (baseH - baseL) / baseH * 100
		if rangePct > config.FlatBaseMaxRangePct {
			continue // Too wide — not a flat base
		}

		// ── Step 3: Volume contraction during base ───────────────────────────
		if len(volumes) >= n {
			// Compare base volume to the 50 bars before the base
			preBaseStart := startIdx - 50
			if preBaseStart < 0 {
				preBaseStart = 0
			}
			var preBaseVol float64
			preBaseLen := startIdx - preBaseStart
			if preBaseLen <= 0 {
				goto volumeOK // Can't compute pre-base vol — skip check
			}
			for i := preBaseStart; i < startIdx; i++ {
				preBaseVol += volumes[i]
			}
			preBaseVol /= float64(preBaseLen)

			var baseVol float64
			for i := startIdx; i < n; i++ {
				baseVol += volumes[i]
			}
			baseVol /= float64(baseLen)

			if preBaseVol > 0 && baseVol >= preBaseVol*0.85 {
				continue // Volume not contracting enough
			}
		}

	volumeOK:
		// ── Step 4: EMA20 must be rising (coiling, not topping) ─────────────
		if len(closes) >= config.EMA20Period+5 {
			ema20s := computeEMASeries(closes, config.EMA20Period)
			if len(ema20s) >= 5 {
				recentEMA := ema20s[len(ema20s)-1]
				olderEMA := ema20s[len(ema20s)-5]
				if recentEMA <= olderEMA {
					continue // EMA20 is flat or declining — stock may be topping
				}
			}
		}

		// Valid flat base found — use the most recent one
		flatTop = baseH
		flatLow = baseL
		formed = true
		return
	}
	return
}
