package agents

import (
	"time"

	"bnf_go_engine/config"
)

// EMACrossStrategy detects a fresh EMA10/EMA20 crossover with CMF volume confirmation.
type EMACrossStrategy struct{}

func (e *EMACrossStrategy) Name() string { return "EMA_CROSS" }

func (e *EMACrossStrategy) Detect(token uint32, symbol string, ltp float64, regime string, ctx StrategyContext) *Signal {
	// Book Ch.5: suppress all new entries on major event days.
	if ctx.IsMajorEventDay {
		return nil
	}
	closes, cOk := ctx.Cache.Closes[token]
	highs, hOk := ctx.Cache.Highs[token]
	lows, lOk := ctx.Cache.Lows[token]
	volumes, vOk := ctx.Cache.Volumes[token]
	if !cOk || !hOk || !lOk || !vOk {
		return nil
	}

	if !DetectEMACrossFromSlice(closes, highs, lows, volumes, config.CMFBuyThreshold) {
		return nil
	}

	// Volume spike vs 20-day avg (time-paced for intraday — live scanner only)
	avgVol := ctx.Cache.AvgVol[token]
	if avgVol > 0 && ctx.GetVolume != nil {
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
				return nil
			}
		}
	}

	prevLow := lows[len(lows)-2]
	entryPrice := ltp * 1.003
	stopPrice := config.ComputeStructuralSL(entryPrice, prevLow)
	if stopPrice >= entryPrice {
		return nil
	}

	// Book Ch.8: Risk-based sizing — Qty = Risk_Amount / (Entry − SL)
	qty := computeRiskBasedQty(config.TotalCapital, ctx.CapitalMultiplier, entryPrice, stopPrice)
	if qty < 1 {
		return nil
	}

	return &Signal{
		Strategy:    "EMA_CROSS_BUY",
		Symbol:      symbol, Token: token, Regime: regime,
		EntryPrice:  entryPrice, StopPrice: stopPrice,
		Qty:         qty, Product: "CNC",
		IsLargeBase: false, // EMA crossover = mini/flag base → exit with 20 EMA
	}
}

// DetectEMACrossFromSlice is the pure-data form used by the backtest engine.
// Checks for a fresh EMA10/EMA20 crossover within last 3 bars with CMF buy confirmation.
// No live feeds — operates entirely on the provided OHLCV slices.
func DetectEMACrossFromSlice(closes, highs, lows, volumes []float64, cmfThreshold float64) bool {
	if len(closes) < config.EMA20Period+5 {
		return false
	}
	ema10s := computeEMASeries(closes, config.EMA10Period)
	ema20s := computeEMASeries(closes, config.EMA20Period)
	ne10, ne20 := len(ema10s), len(ema20s)
	if ne10 < 2 || ne20 < 2 {
		return false
	}
	// Index from end so EMA10[ne10-1-k] and EMA20[ne20-1-k] align to the same calendar bar.
	crossed := false
	for k := 0; k < 3; k++ {
		i10, i10n := ne10-2-k, ne10-1-k
		i20, i20n := ne20-2-k, ne20-1-k
		if i10 < 0 || i20 < 0 {
			break
		}
		if ema10s[i10] <= ema20s[i20] && ema10s[i10n] > ema20s[i20n] {
			crossed = true
			break
		}
	}
	if !crossed {
		return false
	}
	return computeCMF(closes, highs, lows, volumes, config.CMFPeriod) >= cmfThreshold
}
