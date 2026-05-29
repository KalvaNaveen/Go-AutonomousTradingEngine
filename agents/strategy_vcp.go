package agents

import (
	"log"
	"math"
	"time"

	"bnf_go_engine/config"
)

// VCPStrategy detects Volatility Contraction Pattern breakouts.
type VCPStrategy struct{}

func (v *VCPStrategy) Name() string { return "VCP_BREAKOUT" }

func (v *VCPStrategy) Detect(token uint32, symbol string, ltp float64, regime string, ctx StrategyContext) *Signal {
	// Book Ch.5: suppress all new entries on major event days (budget, RBI policy, result day).
	if ctx.IsMajorEventDay {
		return nil
	}
	closes := ctx.Cache.Closes[token]
	highs := ctx.Cache.Highs[token]
	lows := ctx.Cache.Lows[token]
	volumes := ctx.Cache.Volumes[token]
	if closes == nil || highs == nil || lows == nil {
		return nil
	}

	resistance, lastPullbackLow, formed := DetectVCPFromSlice(closes, highs, lows, volumes)
	if !formed || ltp < resistance {
		return nil
	}

	// Live breakout volume check (time-paced — live scanner only)
	if volumes != nil && len(volumes) > 20 && ctx.GetVolume != nil {
		var avgVol float64
		for i := len(volumes) - 21; i < len(volumes)-1; i++ {
			avgVol += volumes[i]
		}
		avgVol /= 20
		currentVol := float64(ctx.GetVolume(token))
		if currentVol > 0 && avgVol > 0 {
			now := config.NowIST()
			mktOpen := time.Date(now.Year(), now.Month(), now.Day(), 9, 15, 0, 0, now.Location())
			fraction := now.Sub(mktOpen).Minutes() / 375.0
			if fraction > 1.0 {
				fraction = 1.0
			}
			if fraction < 0.05 {
				fraction = 0.05
			}
			if currentVol/fraction < avgVol {
				return nil
			}
		}
	}

	entryPrice := ltp
	stopPrice := math.Max(lastPullbackLow*0.98, entryPrice*(1-config.SLCeilingPct/100))
	// Book Ch.8: Risk-based sizing — Qty = Risk_Amount / (Entry − SL)
	qty := computeRiskBasedQty(config.TotalCapital, ctx.CapitalMultiplier, entryPrice, stopPrice)
	if qty <= 0 {
		log.Printf("[VCP] %s: qty=0 — skipping", symbol)
		return nil
	}

	log.Printf("[VCP] BREAKOUT: %s LTP=%.2f Resistance=%.2f SL=%.2f Qty=%d (risk-based)", symbol, ltp, resistance, stopPrice, qty)
	return &Signal{
		Strategy:   "VCP_BREAKOUT",
		Symbol:     symbol, Token: token, Regime: regime,
		EntryPrice: entryPrice, StopPrice: stopPrice,
		Qty:        qty, Product: "CNC",
		IsLargeBase: true, // VCP uses 120-day lookback = large base → trail with 50 EMA
	}
}

// DetectVCPFromSlice checks VCP structure on pre-sliced OHLCV arrays.
// Returns (resistance, lastPullbackLow, formed). Caller must check ltp >= resistance.
// No live feeds — operates entirely on the provided OHLCV slices.
func DetectVCPFromSlice(closes, highs, lows, volumes []float64) (resistance, lastPullbackLow float64, formed bool) {
	lookback := config.VCPLookbackDays
	if len(highs) < lookback || len(lows) < lookback || len(closes) < lookback {
		return
	}

	lastClose := closes[len(closes)-1]

	// Invalidate if below EMA20
	ema20s := computeEMASeries(closes, config.EMA20Period)
	if len(ema20s) > 0 && lastClose < ema20s[len(ema20s)-1] {
		return
	}
	// Invalidate if below EMA63
	if len(closes) > config.EMA63Period {
		if ema63 := ComputeEMA63(closes); ema63 > 0 && lastClose < ema63 {
			return
		}
	}

	hWindow := highs[len(highs)-lookback:]
	lWindow := lows[len(lows)-lookback:]

	resistance = hWindow[0]
	for _, h := range hWindow {
		if h > resistance {
			resistance = h
		}
	}

	var pullbackDepths []float64
	lastPullbackLow = resistance
	localHigh := resistance
	inPullback := false
	currentLow := resistance

	for i := 1; i < len(hWindow); i++ {
		if !inPullback {
			if hWindow[i] > localHigh {
				localHigh = hWindow[i]
			}
			if lWindow[i] < localHigh*0.98 {
				inPullback = true
				currentLow = lWindow[i]
			}
		} else {
			if lWindow[i] < currentLow {
				currentLow = lWindow[i]
			}
			if hWindow[i] > currentLow*1.02 {
				depth := (localHigh - currentLow) / localHigh * 100
				if depth > 1.0 {
					pullbackDepths = append(pullbackDepths, depth)
					lastPullbackLow = currentLow
				}
				inPullback = false
				localHigh = hWindow[i]
			}
		}
	}
	if inPullback {
		depth := (localHigh - currentLow) / localHigh * 100
		if depth > 1.0 {
			pullbackDepths = append(pullbackDepths, depth)
			lastPullbackLow = currentLow
		}
	}

	if len(pullbackDepths) < config.VCPMinPullbacks {
		return
	}
	for i := 1; i < len(pullbackDepths); i++ {
		if pullbackDepths[i] >= pullbackDepths[i-1]*config.VCPContractionRatio {
			return
		}
	}

	// Volume contraction: late volume must be < 80% of early volume
	if volumes != nil && len(volumes) >= lookback {
		vWindow := volumes[len(volumes)-lookback:]
		earlyCount := 30
		if earlyCount > len(vWindow)/2 {
			earlyCount = len(vWindow) / 2
		}
		lateStart := len(vWindow) - 20
		if lateStart < earlyCount {
			lateStart = earlyCount
		}
		var earlyVol, lateVol float64
		for i := 0; i < earlyCount; i++ {
			earlyVol += vWindow[i]
		}
		earlyVol /= float64(earlyCount)
		for i := lateStart; i < len(vWindow); i++ {
			lateVol += vWindow[i]
		}
		lateVol /= float64(len(vWindow) - lateStart)
		if lateVol >= earlyVol*0.70 {
			return
		}
	}

	formed = true
	return
}
