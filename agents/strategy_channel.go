package agents

import (
	"log"

	"bnf_go_engine/config"
)

// TrendChannelStrategy detects stocks trading in an upward trend channel
// and signals entries when price pulls back to the lower channel line.
// Book Ch.4 (p.103-118): "Buy on the third touch of the lower trend line
// in a rising channel with higher highs and higher lows."
//
// This is a mini-base / continuation pattern → trail exit with 20 EMA.
type TrendChannelStrategy struct{}

func (t *TrendChannelStrategy) Name() string { return "TREND_CHANNEL" }

func (t *TrendChannelStrategy) Detect(token uint32, symbol string, ltp float64, regime string, ctx StrategyContext) *Signal {
	// Book Ch.5: suppress all new entries on major event days.
	if ctx.IsMajorEventDay {
		return nil
	}
	closes, cOk := ctx.Cache.Closes[token]
	highs, hOk := ctx.Cache.Highs[token]
	lows, lOk := ctx.Cache.Lows[token]
	if !cOk || !hOk || !lOk || len(closes) < config.ChannelLookback+5 {
		return nil
	}

	channelLow, channelHigh, formed := DetectTrendChannelFromSlice(closes, highs, lows)
	if !formed {
		return nil
	}

	// Entry: LTP should be near the lower channel line (within ChannelEntryBandPct%)
	// but also above the EMA20 (ensure the stock is in an uptrend)
	nearChannelLow := ltp <= channelLow*(1+config.ChannelEntryBandPct/100)
	if !nearChannelLow {
		return nil
	}

	// Must be above EMA20 (uptrend confirmation)
	if len(closes) >= config.EMA20Period {
		ema20s := computeEMASeries(closes, config.EMA20Period)
		if len(ema20s) > 0 {
			ema20 := ema20s[len(ema20s)-1]
			if ltp < ema20*0.99 { // 1% tolerance
				return nil
			}
		}
	}

	entryPrice := ltp * 1.001
	// SL = below channel low with some buffer
	stopPrice := channelLow * (1 - config.SLFloorPct/100)
	stopPrice = config.ComputeStructuralSL(entryPrice, channelLow)

	if stopPrice >= entryPrice {
		return nil
	}

	qty := computeRiskBasedQty(config.TotalCapital, ctx.CapitalMultiplier, entryPrice, stopPrice)
	if qty < 1 {
		log.Printf("[CHANNEL] %s: qty=0 — skipping", symbol)
		return nil
	}

	log.Printf("[CHANNEL] PULLBACK: %s LTP=%.2f ChannelLow=%.2f ChannelHigh=%.2f SL=%.2f Qty=%d",
		symbol, ltp, channelLow, channelHigh, stopPrice, qty)

	return &Signal{
		Strategy:    "TREND_CHANNEL",
		Symbol:      symbol, Token: token, Regime: regime,
		EntryPrice:  entryPrice, StopPrice: stopPrice,
		Qty:         qty, Product: "CNC",
		IsLargeBase: false, // Channel continuation → 20 EMA exit
	}
}

// DetectTrendChannelFromSlice is the pure-data form used by the backtest engine.
// Returns (channelLow, channelHigh, formed).
//
// Trend Channel logic (Book Ch.4 p.103-118):
//  1. Identify at least ChannelMinPivots swing highs and lows over ChannelLookback bars.
//  2. Validate "higher highs and higher lows" structure (uptrend).
//  3. Fit trend lines through the pivots.
//  4. The lower trend line is the support; the upper trend line is the resistance.
//  5. Entry: price pulling back to the lower trend line.
func DetectTrendChannelFromSlice(closes, highs, lows []float64) (channelLow, channelHigh float64, formed bool) {
	n := len(closes)
	if n < config.ChannelLookback+5 {
		return
	}

	window := config.ChannelLookback
	startIdx := n - window
	if startIdx < 0 {
		startIdx = 0
	}

	hWindow := highs[startIdx:]
	lWindow := lows[startIdx:]
	cWindow := closes[startIdx:]
	wLen := len(hWindow)

	// ── Find swing highs and lows ─────────────────────────────────────────────
	// A swing high: higher than both neighbors.
	// A swing low: lower than both neighbors.
	type pivot struct {
		idx   int
		price float64
	}
	var swingHighs, swingLows []pivot

	for i := 1; i < wLen-1; i++ {
		if hWindow[i] > hWindow[i-1] && hWindow[i] > hWindow[i+1] {
			swingHighs = append(swingHighs, pivot{i, hWindow[i]})
		}
		if lWindow[i] < lWindow[i-1] && lWindow[i] < lWindow[i+1] {
			swingLows = append(swingLows, pivot{i, lWindow[i]})
		}
	}

	if len(swingHighs) < config.ChannelMinPivots || len(swingLows) < config.ChannelMinPivots {
		return // Not enough pivots for a channel
	}

	// ── Validate higher highs and higher lows ────────────────────────────────
	higherHighs := true
	for i := 1; i < len(swingHighs); i++ {
		if swingHighs[i].price <= swingHighs[i-1].price {
			higherHighs = false
			break
		}
	}
	higherLows := true
	for i := 1; i < len(swingLows); i++ {
		if swingLows[i].price <= swingLows[i-1].price {
			higherLows = false
			break
		}
	}

	if !higherHighs || !higherLows {
		return // Not a valid uptrend channel
	}

	// ── Fit simple trend lines ───────────────────────────────────────────────
	// Use linear regression through the most recent ChannelMinPivots swing points
	// to project the current channel boundaries.
	numH := len(swingHighs)
	numL := len(swingLows)

	// Lower channel: regression through last ChannelMinPivots swing lows
	lowPivots := swingLows[numL-config.ChannelMinPivots:]
	var sumX, sumY, sumXY, sumX2 float64
	m := float64(config.ChannelMinPivots)
	for _, p := range lowPivots {
		x := float64(p.idx)
		y := p.price
		sumX += x
		sumY += y
		sumXY += x * y
		sumX2 += x * x
	}
	denom := m*sumX2 - sumX*sumX
	var lSlope, lIntercept float64
	if denom != 0 {
		lSlope = (m*sumXY - sumX*sumY) / denom
		lIntercept = (sumY - lSlope*sumX) / m
	} else {
		lIntercept = sumY / m
	}

	// Project lower channel line to current bar
	currentX := float64(wLen - 1)
	projectedLow := lIntercept + lSlope*currentX
	if projectedLow <= 0 {
		return
	}

	// Upper channel: regression through last ChannelMinPivots swing highs
	highPivots := swingHighs[numH-config.ChannelMinPivots:]
	sumX, sumY, sumXY, sumX2 = 0, 0, 0, 0
	for _, p := range highPivots {
		x := float64(p.idx)
		y := p.price
		sumX += x
		sumY += y
		sumXY += x * y
		sumX2 += x * x
	}
	denom = m*sumX2 - sumX*sumX
	var hSlope, hIntercept float64
	if denom != 0 {
		hSlope = (m*sumXY - sumX*sumY) / denom
		hIntercept = (sumY - hSlope*sumX) / m
	} else {
		hIntercept = sumY / m
	}
	projectedHigh := hIntercept + hSlope*currentX
	if projectedHigh <= projectedLow {
		return
	}

	// Channel width validation: should be 5–25% of the lower channel price
	// (too narrow = flat, too wide = volatile/not a real channel)
	channelWidthPct := (projectedHigh - projectedLow) / projectedLow * 100
	if channelWidthPct < config.ChannelMinWidthPct || channelWidthPct > config.ChannelMaxWidthPct {
		return
	}

	// Ensure the current close is within the channel (not above the upper line)
	lastClose := cWindow[wLen-1]
	if lastClose > projectedHigh*1.02 || lastClose < projectedLow*0.97 {
		return // Price has broken out of or below the channel
	}

	channelLow = projectedLow
	channelHigh = projectedHigh
	formed = true
	return
}
