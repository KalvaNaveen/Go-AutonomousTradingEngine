package agents

import (
	"log"
	"math"

	"bnf_go_engine/config"
)

// ══════════════════════════════════════════════════════════════
//  Section V.2: Bull Flag Pattern
// ══════════════════════════════════════════════════════════════
//  Doc: "A sharp, vertical upward prevailing trend (The Pole)
//  followed by a tiny consolidation resting phase tilting slightly
//  downward (The Flag). Buy the breakout of the flag's upper boundary."

func (s *ScannerAgent) detectBullFlag(token uint32, symbol string, ltp float64, regime string) *Signal {
	// Doc V.2 Invalidation: "Do not trade this pattern if there is a massive
	// global/domestic fundamental trigger occurring"
	if s.IsMajorEventDay {
		return nil
	}

	closes, cOk := s.DailyCache.Closes[token]
	highs, hOk := s.DailyCache.Highs[token]
	lows, lOk := s.DailyCache.Lows[token]
	volumes, vOk := s.DailyCache.Volumes[token]
	if !cOk || !hOk || !lOk || len(closes) < 25 {
		return nil
	}

	// FIX-03: Structural detection — find sharpest rise (pole) without fixed day windows
	// Blueprint: "sharp, vertical upward prevailing trend" — no day counts specified
	poleStart, poleEnd := findSharpestRise(closes, 10.0, 5, 18)
	if poleStart < 0 || poleEnd < 0 {
		return nil
	}

	poleHigh := closes[poleEnd]
	poleRange := closes[poleEnd] - closes[poleStart]

	// Flag: everything after the pole
	if poleEnd >= len(closes)-5 {
		return nil // Not enough candles for flag
	}
	flagSlice := closes[poleEnd+1:]
	flagHighs := highs[poleEnd+1:]
	flagLows := lows[poleEnd+1:]

	// Find flag range
	flagHigh := flagHighs[0]
	flagLow := flagLows[0]
	for i := range flagHighs {
		if flagHighs[i] > flagHigh {
			flagHigh = flagHighs[i]
		}
		if flagLows[i] < flagLow {
			flagLow = flagLows[i]
		}
	}

	// Flag range must be tight (< 50% of pole range) — Blueprint: "tiny consolidation"
	flagRange := flagHigh - flagLow
	if flagRange > poleRange*0.5 {
		return nil
	}

	// Flag must not exceed pole high — it's a rest, not a new leg up
	if flagHigh > poleHigh*1.02 {
		return nil
	}

	// Flag must drift down slightly (last close < flag high)
	if flagSlice[len(flagSlice)-1] > flagHigh*0.98 {
		return nil // Not consolidating
	}

	// Breakout: LTP breaks above flag high
	if ltp <= flagHigh {
		return nil
	}

	// Volume confirmation: breakout volume > average
	if vOk && len(volumes) > 20 {
		var avgVol float64
		for i := len(volumes) - 21; i < len(volumes)-1; i++ {
			avgVol += volumes[i]
		}
		avgVol /= 20
		currentVol := 0.0
		if s.GetVolume != nil {
			currentVol = float64(s.GetVolume(token))
		}
		if currentVol > 0 && currentVol < avgVol {
			return nil
		}
	}

	entryPrice := ltp
	stopPrice := entryPrice * (1 - config.HardStopLossPct/100)
	effectiveCapital := config.TotalCapital * s.CapitalMultiplier
	positionSize := effectiveCapital * config.MaxTradeAllocPct / 100
	qty := int(math.Floor(positionSize / entryPrice))
	if qty <= 0 {
		qty = 1
	}

	poleGain := (closes[poleEnd] - closes[poleStart]) / closes[poleStart] * 100
	log.Printf("[BULL_FLAG] BREAKOUT: %s LTP=%.2f FlagHigh=%.2f PoleGain=%.1f%%",
		symbol, ltp, flagHigh, poleGain)

	return &Signal{
		Strategy: "BULL_FLAG", Symbol: symbol, Token: token, Regime: regime,
		EntryPrice: entryPrice, StopPrice: stopPrice, Qty: qty,
		Product: "CNC", IsShort: false,
	}
}

// findSharpestRise finds the sub-window with the highest gain meeting constraints.
// minGain: minimum percentage gain for a valid pole
// minLen/maxLen: min/max candle length for the pole
func findSharpestRise(closes []float64, minGain float64, minLen, maxLen int) (int, int) {
	bestGain := 0.0
	bestStart := -1
	bestEnd := -1
	for start := 0; start < len(closes)-minLen; start++ {
		for length := minLen; length <= maxLen; length++ {
			end := start + length
			if end >= len(closes) {
				break
			}
			if closes[start] <= 0 {
				continue
			}
			gain := (closes[end] - closes[start]) / closes[start] * 100
			if gain >= minGain && gain > bestGain {
				bestGain = gain
				bestStart = start
				bestEnd = end
			}
		}
	}
	return bestStart, bestEnd
}

// ══════════════════════════════════════════════════════════════
//  Section V.3: IPO Base Breakout
// ══════════════════════════════════════════════════════════════
//  Doc: "Wait for the stock to enter a Consolidation Zone where it
//  moves sideways with no clear trend. Volume must contract heavily.
//  Buy the exact moment it breaks out of the sideways base range."

func (s *ScannerAgent) detectIPOBaseBreakout(token uint32, symbol string, ltp float64, regime string) *Signal {
	// FIX-08: Blueprint-mandated — "After listing" — only apply to recent IPOs
	if s.IPOSymbols == nil || !s.IPOSymbols[symbol] {
		return nil // Not a recent IPO — skip this pattern
	}

	closes, cOk := s.DailyCache.Closes[token]
	highs, hOk := s.DailyCache.Highs[token]
	volumes, vOk := s.DailyCache.Volumes[token]
	if !cOk || !hOk {
		return nil
	}

	// Use available history, min 20 candles
	lookback := 40
	if len(closes) < lookback {
		lookback = len(closes)
	}
	if lookback < 20 {
		return nil
	}

	// IPO base: look at last N days for sideways action
	window := closes[len(closes)-lookback:]
	windowHighs := highs[len(highs)-lookback:]

	// Find range: max and min of the base
	baseHigh := window[0]
	baseLow := window[0]
	for _, c := range window {
		if c > baseHigh {
			baseHigh = c
		}
		if c < baseLow {
			baseLow = c
		}
	}

	// Sideways = range is tight (< 15% of base high)
	baseRange := ((baseHigh - baseLow) / baseHigh) * 100
	if baseRange > 15 {
		return nil // Not sideways
	}

	// Resistance line = max high in the base
	resistance := windowHighs[0]
	for _, h := range windowHighs {
		if h > resistance {
			resistance = h
		}
	}

	// Doc: "Volume must contract heavily during this base"
	if vOk && len(volumes) >= lookback {
		vWindow := volumes[len(volumes)-lookback:]
		half := lookback / 2
		var earlyVol, lateVol float64
		for i := 0; i < half; i++ {
			earlyVol += vWindow[i]
		}
		for i := half; i < lookback; i++ {
			lateVol += vWindow[i]
		}
		if lateVol >= earlyVol {
			return nil // Volume did NOT contract
		}
	}

	// Breakout: LTP above resistance
	if ltp <= resistance {
		return nil
	}

	entryPrice := ltp
	stopPrice := entryPrice * (1 - config.HardStopLossPct/100)
	effectiveCapital := config.TotalCapital * s.CapitalMultiplier
	positionSize := effectiveCapital * config.MaxTradeAllocPct / 100
	qty := int(math.Floor(positionSize / entryPrice))
	if qty <= 0 {
		qty = 1
	}

	log.Printf("[IPO_BASE] BREAKOUT: %s LTP=%.2f Resistance=%.2f BaseRange=%.1f%%",
		symbol, ltp, resistance, baseRange)

	return &Signal{
		Strategy: "IPO_BASE", Symbol: symbol, Token: token, Regime: regime,
		EntryPrice: entryPrice, StopPrice: stopPrice, Qty: qty,
		Product: "CNC", IsShort: false,
	}
}

// ══════════════════════════════════════════════════════════════
//  Section V.4: Cup with Handle
// ══════════════════════════════════════════════════════════════
//  Doc: "Rounded bottom (cup) followed by a smaller, tighter
//  consolidation (handle). Do NOT enter immediately. Wait for a
//  1-day confirmation candle after the neckline is broken."

func (s *ScannerAgent) detectCupWithHandle(token uint32, symbol string, ltp float64, regime string) *Signal {
	closes, cOk := s.DailyCache.Closes[token]
	highs, hOk := s.DailyCache.Highs[token]
	if !cOk || !hOk || len(closes) < 60 {
		return nil
	}

	// Cup: look back 60 days. Left lip = high near start, trough in middle, right lip = high near end
	leftLip := highs[len(highs)-60]
	rightLip := highs[len(highs)-16]

	// Find trough (lowest close in middle portion, days 15-45)
	trough := closes[len(closes)-45]
	for i := len(closes) - 45; i < len(closes)-15; i++ {
		if closes[i] < trough {
			trough = closes[i]
		}
	}

	// Cup depth must be significant (>5% from lips)
	// FIX-04: Neckline = max of both lips
	// Blueprint: "horizontal neckline" — using the higher lip is conservative
	neckline := math.Max(leftLip, rightLip)
	cupDepth := ((neckline - trough) / neckline) * 100
	if cupDepth < 5 || cupDepth > 35 {
		return nil // Too shallow or too deep
	}

	// Both lips must be roughly at the same level (within 5%)
	lipDiff := math.Abs(leftLip-rightLip) / math.Max(leftLip, rightLip) * 100
	if lipDiff > 5 {
		return nil
	}

	// Handle: last 15 days, tighter consolidation
	handleCloses := closes[len(closes)-15:]
	handleHigh := handleCloses[0]
	handleLow := handleCloses[0]
	for _, c := range handleCloses {
		if c > handleHigh {
			handleHigh = c
		}
		if c < handleLow {
			handleLow = c
		}
	}
	handleRange := ((handleHigh - handleLow) / handleHigh) * 100
	if handleRange > cupDepth*0.5 {
		return nil // Handle not tighter than cup
	}

	// Doc: "Wait for 1-day confirmation candle after neckline break"
	// Yesterday must have closed above neckline, and today continues above
	prevClose := closes[len(closes)-2]
	if prevClose <= neckline {
		return nil // Neckline not broken yesterday
	}
	if ltp <= neckline {
		return nil
	}

	entryPrice := ltp
	stopPrice := entryPrice * (1 - config.HardStopLossPct/100)
	effectiveCapital := config.TotalCapital * s.CapitalMultiplier
	positionSize := effectiveCapital * config.MaxTradeAllocPct / 100
	qty := int(math.Floor(positionSize / entryPrice))
	if qty <= 0 {
		qty = 1
	}

	log.Printf("[CUP_HANDLE] CONFIRMED: %s LTP=%.2f Neckline=%.2f CupDepth=%.1f%%",
		symbol, ltp, neckline, cupDepth)

	// Doc V.4: "Sell at the next major structural resistance line"
	// Use 52-week high as structural resistance target
	targetPrice := 0.0
	if h52, ok := s.DailyCache.High52W[token]; ok && h52 > entryPrice {
		targetPrice = h52
	}

	return &Signal{
		Strategy: "CUP_HANDLE", Symbol: symbol, Token: token, Regime: regime,
		EntryPrice: entryPrice, StopPrice: stopPrice, TargetPrice: targetPrice, Qty: qty,
		Product: "CNC", IsShort: false,
	}
}

// ══════════════════════════════════════════════════════════════
//  Section V.5: Trend Line Channel
// ══════════════════════════════════════════════════════════════
//  Doc: "Connect at least 2 consecutive support points. Buy near
//  the support line."

func (s *ScannerAgent) detectTrendChannel(token uint32, symbol string, ltp float64, regime string) *Signal {
	lows, lOk := s.DailyCache.Lows[token]
	highs, hOk := s.DailyCache.Highs[token]
	if !lOk || !hOk || len(lows) < 60 {
		return nil
	}

	window := 60
	lWindow := lows[len(lows)-window:]
	hWindow := highs[len(highs)-window:]

	// Find swing lows (local minima) for support line
	var supportPoints []struct{ idx int; val float64 }
	for i := 2; i < len(lWindow)-2; i++ {
		if lWindow[i] < lWindow[i-1] && lWindow[i] < lWindow[i-2] &&
			lWindow[i] < lWindow[i+1] && lWindow[i] < lWindow[i+2] {
			supportPoints = append(supportPoints, struct{ idx int; val float64 }{i, lWindow[i]})
		}
	}

	// Need at least 2 support touches
	if len(supportPoints) < 2 {
		return nil
	}

	// Find swing highs for resistance line
	var resistPoints []struct{ idx int; val float64 }
	for i := 2; i < len(hWindow)-2; i++ {
		if hWindow[i] > hWindow[i-1] && hWindow[i] > hWindow[i-2] &&
			hWindow[i] > hWindow[i+1] && hWindow[i] > hWindow[i+2] {
			resistPoints = append(resistPoints, struct{ idx int; val float64 }{i, hWindow[i]})
		}
	}

	if len(resistPoints) < 2 {
		return nil
	}

	// Compute support trendline slope using first and last support points
	sp1 := supportPoints[0]
	sp2 := supportPoints[len(supportPoints)-1]
	if sp2.idx == sp1.idx {
		return nil
	}
	supportSlope := (sp2.val - sp1.val) / float64(sp2.idx-sp1.idx)

	// FIX-07: Support must be meaningfully uptrending
	// Blueprint implies uptrend — a near-flat line is not a trend channel.
	// ⚠️ EXTENSION: 5% min appreciation over 60 days not in Blueprint.
	if len(lWindow) > 0 && lWindow[0] > 0 {
		minDailySlope := (lWindow[0] * 0.05) / float64(window)
		if supportSlope < minDailySlope {
			return nil
		}
	}

	// Project current support level
	currentSupport := sp2.val + supportSlope*float64(len(lWindow)-1-sp2.idx)

	// Doc: "Buy near the support line" — LTP within 2% of support
	distFromSupport := ((ltp - currentSupport) / currentSupport) * 100
	if distFromSupport < 0 || distFromSupport > 2.0 {
		return nil // Not near support
	}

	// FIX-07: Volume filter at support — low volume = healthy pullback
	// High volume at support = potential breakdown. Skip.
	// ⚠️ EXTENSION: Volume check not in Blueprint for this pattern.
	volumes, vOk := s.DailyCache.Volumes[token]
	if vOk && len(volumes) > 20 {
		var avgVol20 float64
		for i := len(volumes) - 21; i < len(volumes)-1; i++ {
			avgVol20 += volumes[i]
		}
		avgVol20 /= 20
		if len(volumes) > 0 && volumes[len(volumes)-1] > avgVol20*1.10 {
			return nil // Elevated selling volume near support
		}
	}

	entryPrice := ltp
	stopPrice := currentSupport * 0.98 // SL just below support
	if ((entryPrice - stopPrice) / entryPrice * 100) > config.HardStopLossPct {
		stopPrice = entryPrice * (1 - config.HardStopLossPct/100)
	}

	effectiveCapital := config.TotalCapital * s.CapitalMultiplier
	positionSize := effectiveCapital * config.MaxTradeAllocPct / 100
	qty := int(math.Floor(positionSize / entryPrice))
	if qty <= 0 {
		qty = 1
	}

	log.Printf("[TREND_CHANNEL] SUPPORT BUY: %s LTP=%.2f Support=%.2f Slope=%.2f",
		symbol, ltp, currentSupport, supportSlope)

	// Doc V.5: "Sell near the resistance line"
	rp1 := resistPoints[0]
	rp2 := resistPoints[len(resistPoints)-1]
	resistSlope := (rp2.val - rp1.val) / float64(rp2.idx-rp1.idx)
	currentResist := rp2.val + resistSlope*float64(len(hWindow)-1-rp2.idx)
	targetPrice := currentResist

	return &Signal{
		Strategy: "TREND_CHANNEL", Symbol: symbol, Token: token, Regime: regime,
		EntryPrice: entryPrice, StopPrice: stopPrice, TargetPrice: targetPrice, Qty: qty,
		Product: "CNC", IsShort: false,
	}
}
