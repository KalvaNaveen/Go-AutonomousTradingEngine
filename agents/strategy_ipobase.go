package agents

import (
	"log"
	"math"
	"time"

	"bnf_go_engine/config"
)

// IPOBaseStrategy detects IPO Base breakouts for recent IPO stocks.
type IPOBaseStrategy struct{}

func (ip *IPOBaseStrategy) Name() string { return "IPO_BASE" }

func (ip *IPOBaseStrategy) Detect(token uint32, symbol string, ltp float64, regime string, ctx StrategyContext) *Signal {
	// Book Ch.5: suppress all new entries on major event days.
	if ctx.IsMajorEventDay {
		return nil
	}
	// Doc V.3: "After listing" — only apply to recent IPOs
	if ctx.IPOSymbols == nil || !ctx.IPOSymbols[symbol] {
		return nil
	}

	closes := ctx.Cache.Closes[token]
	highs := ctx.Cache.Highs[token]
	volumes := ctx.Cache.Volumes[token]
	if closes == nil || highs == nil {
		return nil
	}

	resistance, baseLow, formed := DetectIPOBaseFromSlice(closes, highs, volumes)
	if !formed || ltp <= resistance {
		return nil
	}

	// Book Ch.4 p.119 + Ch.11 p.272: Breakout must be on expanding volume.
	// IPO Base breakout above the base high must be confirmed by ≥1.5× avg volume.
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
					return nil // IPO base breakout on weak volume — false move
				}
			}
		}
	}

	entryPrice := ltp
	stopPrice := math.Max(baseLow*0.98, entryPrice*(1-config.SLCeilingPct/100))

	// Book Ch.8: Risk-based sizing — Qty = Risk_Amount / (Entry − SL)
	qty := computeRiskBasedQty(config.TotalCapital, ctx.CapitalMultiplier, entryPrice, stopPrice)
	if qty <= 0 {
		log.Printf("[IPO_BASE] %s: qty=0 — skipping", symbol)
		return nil
	}

	log.Printf("[IPO_BASE] BREAKOUT: %s LTP=%.2f Resistance=%.2f SL=%.2f Qty=%d (risk-based)",
		symbol, ltp, resistance, stopPrice, qty)
	return &Signal{
		Strategy:    "IPO_BASE",
		Symbol:      symbol, Token: token, Regime: regime,
		EntryPrice:  entryPrice, StopPrice: stopPrice,
		Qty:         qty, Product: "CNC",
		IsLargeBase: false, // IPO base = short formation → trail with 20 EMA
	}
}

// DetectIPOBaseFromSlice checks IPO base structure on pre-sliced arrays.
// Returns (resistance, baseLow, formed). Caller checks IPO eligibility and ltp > resistance.
// No live feeds — operates entirely on the provided OHLCV slices.
func DetectIPOBaseFromSlice(closes, highs, volumes []float64) (resistance, baseLow float64, formed bool) {
	lookback := 40
	if len(closes) < lookback {
		lookback = len(closes)
	}
	if lookback < 20 {
		return
	}

	window := closes[len(closes)-lookback:]
	windowHighs := highs[len(highs)-lookback:]

	baseHigh := window[0]
	bLow := window[0]
	for _, c := range window {
		if c > baseHigh {
			baseHigh = c
		}
		if c < bLow {
			bLow = c
		}
	}

	// Sideways = range tight (< 15% of base high)
	if baseHigh > 0 && (baseHigh-bLow)/baseHigh*100 > 15 {
		return
	}

	// Resistance = max high in base
	r := windowHighs[0]
	for _, h := range windowHighs {
		if h > r {
			r = h
		}
	}

	// Volume must have contracted during the base
	if volumes != nil && len(volumes) >= lookback {
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
			return
		}
	}

	resistance = r
	baseLow = bLow
	formed = true
	return
}
