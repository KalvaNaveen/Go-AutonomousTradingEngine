package research

import (
	"log"
	"math"
)

// ══════════════════════════════════════════════════════════════
//  Section II.2: Nifty/Gold Ratio — Equity vs Gold Rotation
// ══════════════════════════════════════════════════════════════
//  FIX-09: This is a PORTFOLIO-LEVEL CAPITAL ALLOCATION advisory.
//  It does NOT block individual swing trade signals.
//  Blueprint: "Track NIFTY/GOLDBEES ratio chart. Buy Equities when
//  chart hits bottom of channel. Shift to Gold at top."
//  The Blueprint never says "block swing signals when gold is outperforming."
//  It says shift your overall capital allocation between equity and gold.

// GOLDBEES instrument token on NSE
const GOLDBEESToken uint32 = 3693569 // GOLDBEES NSE token (verified from Kite instruments)

type GoldRatioResult struct {
	CurrentRatio float64
	High252      float64
	Low252       float64
	Percentile   float64 // 0-100: where current ratio sits in 252-day range
	Signal       string  // "BUY_EQUITY", "BUY_GOLD", "NEUTRAL"
}

// ComputeNiftyGoldRatio computes the Nifty/GOLDBEES ratio and determines
// whether we should be in equities or gold based on channel position.
func ComputeNiftyGoldRatio(niftyCloses, goldCloses []float64) *GoldRatioResult {
	if len(niftyCloses) < 252 || len(goldCloses) < 252 {
		log.Printf("[GoldRatio] Not enough data (nifty=%d, gold=%d), need 252 days",
			len(niftyCloses), len(goldCloses))
		return nil
	}

	// Use last 252 days (1 year)
	n := 252
	nifty := niftyCloses[len(niftyCloses)-n:]
	gold := goldCloses[len(goldCloses)-n:]

	// Compute ratio series
	ratios := make([]float64, n)
	for i := 0; i < n; i++ {
		if gold[i] > 0 {
			ratios[i] = nifty[i] / gold[i]
		}
	}

	// Find channel (min/max of ratio over 252 days)
	high := ratios[0]
	low := ratios[0]
	for _, r := range ratios {
		if r > high {
			high = r
		}
		if r < low && r > 0 {
			low = r
		}
	}

	current := ratios[n-1]
	if high == low || current == 0 {
		return nil
	}

	// Percentile: where current ratio sits in the range
	percentile := ((current - low) / (high - low)) * 100

	result := &GoldRatioResult{
		CurrentRatio: math.Round(current*100) / 100,
		High252:      math.Round(high*100) / 100,
		Low252:       math.Round(low*100) / 100,
		Percentile:   math.Round(percentile*10) / 10,
	}

	// Doc: "Buy Equities at bottom of channel. Shift to Gold at top."
	// Bottom 20% = buy equities aggressively
	// Top 20% = shift to gold
	if percentile <= 20 {
		result.Signal = "BUY_EQUITY"
		log.Printf("[GoldRatio] BOTTOM of channel (%.1f%%) — BUY EQUITIES (Ratio=%.2f)",
			percentile, current)
	} else if percentile >= 80 {
		result.Signal = "BUY_GOLD"
		log.Printf("[GoldRatio] TOP of channel (%.1f%%) — SHIFT TO GOLD (Ratio=%.2f)",
			percentile, current)
	} else {
		result.Signal = "NEUTRAL"
		log.Printf("[GoldRatio] Mid-channel (%.1f%%) — NEUTRAL (Ratio=%.2f)",
			percentile, current)
	}

	return result
}
