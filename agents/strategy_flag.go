package agents

import (
	"log"
	"time"

	"bnf_go_engine/config"
)

// BullFlagStrategy detects Bull Flag and Mini Base breakouts.
// Book Ch.4 (p.68-85): After a sharp impulsive move (the pole), the stock
// enters a tight consolidation (the flag). When it breaks out above the flag
// high on expanding volume, it's a high-probability continuation trade.
//
// Mini Base is the intraday/short-duration variant of the bull flag:
// same concept, 3-8 bar tight range after a momentum run.
type BullFlagStrategy struct{}

func (b *BullFlagStrategy) Name() string { return "BULL_FLAG" }

func (b *BullFlagStrategy) Detect(token uint32, symbol string, ltp float64, regime string, ctx StrategyContext) *Signal {
	// Suppress on major event days (earnings, FOMC, index rebalance)
	if ctx.IsMajorEventDay {
		return nil
	}

	closes, cOk := ctx.Cache.Closes[token]
	highs, hOk := ctx.Cache.Highs[token]
	lows, lOk := ctx.Cache.Lows[token]
	volumes, vOk := ctx.Cache.Volumes[token]
	if !cOk || !hOk || !lOk || !vOk || len(closes) < config.FlagMinBars+config.FlagPoleMinBars+5 {
		return nil
	}

	flagHigh, flagLow, formed := DetectBullFlagFromSlice(closes, highs, lows, volumes)
	if !formed {
		return nil
	}

	// Entry: LTP must be breaking above the flag high (not just near it)
	if ltp <= flagHigh {
		return nil
	}

	// Live volume check (time-paced for intraday scanner)
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
				// Book Ch.11 p.272: breakout volume should exceed avg
				if currentVol/fraction < avgVol*config.VolumeSpikeMultiplier {
					return nil
				}
			}
		}
	}

	entryPrice := ltp * 1.002 // small buffer above flag high for limit order
	stopPrice := config.ComputeStructuralSL(entryPrice, flagLow)
	if stopPrice >= entryPrice {
		return nil
	}

	qty := computeRiskBasedQty(config.TotalCapital, ctx.CapitalMultiplier, entryPrice, stopPrice)
	if qty < 1 {
		log.Printf("[FLAG] %s: qty=0 — skipping", symbol)
		return nil
	}

	log.Printf("[FLAG] BREAKOUT: %s LTP=%.2f FlagHigh=%.2f FlagLow=%.2f SL=%.2f Qty=%d",
		symbol, ltp, flagHigh, flagLow, stopPrice, qty)

	return &Signal{
		Strategy:    "BULL_FLAG",
		Symbol:      symbol, Token: token, Regime: regime,
		EntryPrice:  entryPrice, StopPrice: stopPrice,
		Qty:         qty, Product: "CNC",
		IsLargeBase: false, // Mini base → exit with 20 EMA
	}
}

// DetectBullFlagFromSlice is the pure-data form used by the backtest engine.
// Returns (flagHigh, flagLow, formed).
//
// Bull Flag logic (Book Ch.4 p.68-85):
//  1. Pole: a sharp, sustained advance of ≥FlagPoleMinGain% over FlagPoleMinBars–FlagPoleMaxBars bars.
//  2. Flag: a tight consolidation of FlagMinBars–FlagMaxBars bars after the pole, range < FlagMaxRangePct%.
//  3. Volume must contract during the flag (confirms orderly pullback, not distribution).
//  4. Entry: close/LTP above flag high.
func DetectBullFlagFromSlice(closes, highs, lows, volumes []float64) (flagHigh, flagLow float64, formed bool) {
	n := len(closes)
	if n < config.FlagPoleMinBars+config.FlagMaxBars+5 {
		return
	}

	// ── Step 1: Find the pole ────────────────────────────────────────────────
	// Search the last FlagSearchWindow bars for the sharpest qualifying rise.
	searchFrom := n - config.FlagSearchWindow
	if searchFrom < 0 {
		searchFrom = 0
	}

	bestPoleStart, bestPoleEnd := -1, -1
	bestPoleGain := 0.0

	for start := searchFrom; start < n-config.FlagPoleMinBars-config.FlagMinBars; start++ {
		if closes[start] <= 0 {
			continue
		}
		for length := config.FlagPoleMinBars; length <= config.FlagPoleMaxBars; length++ {
			end := start + length
			if end >= n-config.FlagMinBars {
				break
			}
			gain := (closes[end] - closes[start]) / closes[start] * 100
			if gain >= config.FlagPoleMinGainPct && gain > bestPoleGain {
				bestPoleGain = gain
				bestPoleStart = start
				bestPoleEnd = end
			}
		}
	}

	if bestPoleStart < 0 || bestPoleEnd < 0 {
		return // No qualifying pole
	}

	// ── Step 2: Identify the flag consolidation after the pole ───────────────
	// The flag must start right after the pole ends.
	flagStart := bestPoleEnd + 1
	flagEnd := n - 1 // always the most recent bar

	flagBars := flagEnd - flagStart + 1
	if flagBars < config.FlagMinBars || flagBars > config.FlagMaxBars {
		return // Flag too short or too long
	}

	// Flag high/low over the consolidation window
	flagH := highs[flagStart]
	flagL := lows[flagStart]
	for i := flagStart + 1; i <= flagEnd; i++ {
		if highs[i] > flagH {
			flagH = highs[i]
		}
		if lows[i] < flagL {
			flagL = lows[i]
		}
	}

	// Flag range must be tight (< FlagMaxRangePct% of flag high)
	if flagH <= 0 {
		return
	}
	flagRangePct := (flagH - flagL) / flagH * 100
	if flagRangePct > config.FlagMaxRangePct {
		return // Too wide — not a tight flag
	}

	// Flag cannot retrace more than FlagMaxRetracePct% of the pole
	poleBase := closes[bestPoleStart]
	polePeak := closes[bestPoleEnd]
	if polePeak <= poleBase {
		return
	}
	poleHeight := polePeak - poleBase
	flagRetrace := (polePeak - flagL) / poleHeight * 100
	if flagRetrace > config.FlagMaxRetracePct {
		return // Too deep a pullback — not a flag, more like a full correction
	}

	// ── Step 3: Volume contraction during flag ───────────────────────────────
	if len(volumes) >= n {
		// Average volume during the pole
		var poleVol float64
		poleLen := bestPoleEnd - bestPoleStart + 1
		for i := bestPoleStart; i <= bestPoleEnd; i++ {
			poleVol += volumes[i]
		}
		poleVol /= float64(poleLen)

		// Average volume during the flag
		var flagVol float64
		for i := flagStart; i <= flagEnd; i++ {
			flagVol += volumes[i]
		}
		flagVol /= float64(flagBars)

		// Flag volume must be lower than pole volume (orderly consolidation)
		if poleVol > 0 && flagVol >= poleVol*0.90 {
			return // Volume not contracting — possible distribution
		}
	}

	flagHigh = flagH
	flagLow = flagL
	formed = true
	return
}
