package agents

import (
	"log"
	"time"

	"bnf_go_engine/config"
)

// CupHandleStrategy detects Cup & Handle breakouts.
// Book Ch.4 (p.86-102): The cup is a rounded bottom formation after a prior
// high. The handle is a brief, low-volume pullback from the cup rim. Breakout
// above the rim on expanding volume is the entry trigger.
//
// This is a large-base pattern (weeks to months) → trail exit with 50 EMA.
type CupHandleStrategy struct{}

func (c *CupHandleStrategy) Name() string { return "CUP_HANDLE" }

func (c *CupHandleStrategy) Detect(token uint32, symbol string, ltp float64, regime string, ctx StrategyContext) *Signal {
	// Book Ch.5: suppress all new entries on major event days.
	if ctx.IsMajorEventDay {
		return nil
	}
	closes, cOk := ctx.Cache.Closes[token]
	highs, hOk := ctx.Cache.Highs[token]
	lows, lOk := ctx.Cache.Lows[token]
	volumes, vOk := ctx.Cache.Volumes[token]
	if !cOk || !hOk || !lOk || !vOk || len(closes) < config.CupMinBars+config.HandleMaxBars+10 {
		return nil
	}

	rimHigh, handleLow, formed := DetectCupHandleFromSlice(closes, highs, lows, volumes)
	if !formed {
		return nil
	}

	// Entry: LTP must break above the cup rim (resistance level)
	if ltp <= rimHigh {
		return nil
	}

	// Book Ch.5 p.128: Breakout must be on above-average volume (2-3× the 20-day avg).
	// Time-paced: compare projected daily volume (current ÷ fraction of day) to 20-day avg.
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
				// Cup breakout needs at least VolumeSpikeMultiplier× avg
				if currentVol/fraction < avgVol*config.VolumeSpikeMultiplier {
					return nil // Rim breakout on weak volume — likely a false breakout
				}
			}
		}
	}

	entryPrice := ltp * 1.002
	stopPrice := config.ComputeStructuralSL(entryPrice, handleLow)
	if stopPrice >= entryPrice {
		return nil
	}

	qty := computeRiskBasedQty(config.TotalCapital, ctx.CapitalMultiplier, entryPrice, stopPrice)
	if qty < 1 {
		log.Printf("[CUP] %s: qty=0 — skipping", symbol)
		return nil
	}

	log.Printf("[CUP] BREAKOUT: %s LTP=%.2f Rim=%.2f HandleLow=%.2f SL=%.2f Qty=%d",
		symbol, ltp, rimHigh, handleLow, stopPrice, qty)

	return &Signal{
		Strategy:    "CUP_HANDLE",
		Symbol:      symbol, Token: token, Regime: regime,
		EntryPrice:  entryPrice, StopPrice: stopPrice,
		Qty:         qty, Product: "CNC",
		IsLargeBase: true, // Large base → trail exit with 50 EMA
	}
}

// DetectCupHandleFromSlice is the pure-data form used by the backtest engine.
// Returns (rimHigh, handleLow, formed).
//
// Cup & Handle logic (Book Ch.4 p.86-102):
//  1. Prior high (the left side of the cup) exists in the CupSearchWindow.
//  2. Cup: rounded bottom, depth 10-33% from the prior high, over CupMinBars–CupMaxBars bars.
//  3. Cup must recover to within CupRimRecoveryPct% of the prior high.
//  4. Handle: short pullback CupHandleMinBars–HandleMaxBars bars, depth < 1/3 of cup depth.
//  5. Volume contracts during the handle.
//  6. Entry: close above the cup rim (prior high).
func DetectCupHandleFromSlice(closes, highs, lows, volumes []float64) (rimHigh, handleLow float64, formed bool) {
	n := len(closes)
	minNeeded := config.CupMinBars + config.HandleMaxBars + 10
	if n < minNeeded {
		return
	}

	// Search window — look in recent history only (last CupSearchWindow bars)
	searchEnd := n - config.HandleMinBars - 5
	searchStart := n - config.CupSearchWindow
	if searchStart < 0 {
		searchStart = 0
	}

	// ── Step 1: Find the left rim (prior high before cup) ────────────────────
	// The rim is the peak we expect the stock to break above.
	var leftRim float64
	leftRimIdx := -1
	for i := searchStart; i < searchEnd-config.CupMinBars; i++ {
		if highs[i] > leftRim {
			leftRim = highs[i]
			leftRimIdx = i
		}
	}
	if leftRimIdx < 0 || leftRim <= 0 {
		return
	}

	// ── Step 2: Find the cup base (lowest point after left rim) ─────────────
	cupEnd := leftRimIdx + config.CupMinBars
	if cupEnd >= n-config.HandleMinBars {
		return
	}

	cupBase := highs[leftRimIdx+1]
	cupBaseIdx := leftRimIdx + 1
	for i := leftRimIdx + 1; i <= cupEnd; i++ {
		if lows[i] < cupBase {
			cupBase = lows[i]
			cupBaseIdx = i
		}
	}

	// Cup depth validation: 10–33% of left rim
	if leftRim <= 0 {
		return
	}
	cupDepthPct := (leftRim - cupBase) / leftRim * 100
	if cupDepthPct < config.CupMinDepthPct || cupDepthPct > config.CupMaxDepthPct {
		return // Depth out of valid range
	}

	// Cup must be rounded (gradual decline then recovery) — verify the right side
	// recovers to near the left rim before the handle forms.
	// Look for the recovery from the cup base toward the right rim.
	rightRimSearchEnd := n - config.HandleMinBars
	var rightRim float64
	rightRimIdx := -1
	for i := cupBaseIdx + 1; i < rightRimSearchEnd; i++ {
		if highs[i] > rightRim {
			rightRim = highs[i]
			rightRimIdx = i
		}
	}
	if rightRimIdx < 0 {
		return
	}

	// Right rim must be within CupRimRecoveryPct% of left rim
	recoveryGap := (leftRim - rightRim) / leftRim * 100
	if recoveryGap > config.CupRimRecoveryPct {
		return // Right side didn't recover enough — not a valid cup
	}

	// Use the higher of the two rims as the breakout resistance level
	rimResistance := leftRim
	if rightRim > rimResistance {
		rimResistance = rightRim
	}

	// ── Step 3: Handle — small pullback after the right rim ──────────────────
	handleStart := rightRimIdx + 1
	handleEnd := n - 1

	handleBars := handleEnd - handleStart + 1
	if handleBars < config.HandleMinBars || handleBars > config.HandleMaxBars {
		return // Handle too short or too long
	}

	// Handle low should not retrace more than 1/3 of the cup depth
	var hLow float64 = highs[handleStart]
	for i := handleStart; i <= handleEnd; i++ {
		if lows[i] < hLow {
			hLow = lows[i]
		}
	}

	maxHandleRetrace := cupDepthPct / 3
	handleRetracePct := (rimResistance - hLow) / rimResistance * 100
	if handleRetracePct > maxHandleRetrace || handleRetracePct < 0 {
		return // Handle retraced too much
	}

	// ── Step 4: Volume contraction in handle ─────────────────────────────────
	if len(volumes) >= n && handleBars >= 3 {
		// Average volume during cup right-side recovery
		cupRightLen := rightRimIdx - cupBaseIdx + 1
		if cupRightLen < 1 {
			cupRightLen = 1
		}
		var cupRightVol float64
		for i := cupBaseIdx; i <= rightRimIdx; i++ {
			cupRightVol += volumes[i]
		}
		cupRightVol /= float64(cupRightLen)

		// Average volume during handle
		var handleVol float64
		for i := handleStart; i <= handleEnd; i++ {
			handleVol += volumes[i]
		}
		handleVol /= float64(handleBars)

		// Handle volume should be lower than cup right-side volume
		if cupRightVol > 0 && handleVol >= cupRightVol*0.95 {
			return // Volume not drying up in handle — possible distribution
		}
	}

	rimHigh = rimResistance
	handleLow = hLow
	formed = true
	return
}
