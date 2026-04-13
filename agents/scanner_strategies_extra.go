package agents

import (
	"math"
	"sort"

	"bnf_go_engine/config"
	"bnf_go_engine/data"
)

// ══════════════════════════════════════════════════════════════
//  S6_TREND_SHORT: Intraday Short on Relative Weakness
// ══════════════════════════════════════════════════════════════
func (s *ScannerAgent) ScanS6TrendShort(regime string) []*Signal {
	if !s.IsInTradeWindow() || !s.checkDailyTradeLimit() || regime == "EXTREME_PANIC" || !s.cacheReady() {
		return nil
	}

	niftyLTP := s.GetLTP(config.Nifty50Token)
	niftyOpen := 0.0
	if s.GetDayOpen != nil {
		niftyOpen = s.GetDayOpen(config.Nifty50Token)
	}
	if niftyLTP <= 0 || niftyOpen <= 0 {
		return nil
	}
	niftyChg := (niftyLTP - niftyOpen) / niftyOpen

	today := config.TodayIST()
	var signals []*Signal

	for token, symbol := range s.Universe {
		// Cooldown check
		lastS6, exists := s.s6Cooldown[symbol]
		if exists && today.Sub(lastS6).Hours() < float64(config.S6_COOLDOWN_DAYS*24) {
			continue
		}

		turnover := s.DailyCache.TurnoverCr[token]
		if turnover < config.S6_MIN_TURNOVER_CR {
			continue
		}

		current := s.GetLTP(token)
		dayOpen := 0.0
		if s.GetDayOpen != nil {
			dayOpen = s.GetDayOpen(token)
		}
		if current <= 0 || dayOpen <= 0 {
			continue
		}

		// Must be below day open
		if current >= dayOpen {
			continue
		}

		// Must be below VWAP
		vwap := 0.0
		if s.GetVWAP != nil {
			vwap = s.GetVWAP(token)
		}
		if vwap > 0 && current >= vwap {
			continue
		}

		// Relative weakness check
		stockChg := (current - dayOpen) / dayOpen
		relativeWeakness := niftyChg - stockChg
		if relativeWeakness < config.S6_RELATIVE_WEAKNESS {
			continue
		}

		rvol := s.computeRVol(token)
		if rvol < config.S6_RVOL_MIN {
			continue
		}

		// RSI(4) check
		closes := s.DailyCache.Closes[token]
		if len(closes) < 10 {
			continue
		}
		liveCloses := make([]float64, len(closes))
		copy(liveCloses, closes)
		liveCloses = append(liveCloses, current)
		rsiSlice := data.ComputeRSI(liveCloses, config.S6_RSI_PERIOD)
		rsi4 := rsiSlice[len(rsiSlice)-1]
		if rsi4 < float64(config.S6_RSI_ENTRY_LOW) || rsi4 > float64(config.S6_RSI_ENTRY_HIGH) {
			continue
		}

		// 5-min low breakout check
		candles := s.getCandles5m(token)
		if len(candles) >= 3 {
			prevLow := candles[len(candles)-2].Low
			if prevLow > 0 && current > prevLow {
				continue
			}
		}

		atr := s.DailyCache.ATR[token]
		if atr <= 0 {
			atr = current * 0.02
		}

		// Order book filter (block shorts against buy walls)
		if !s.checkOrderBook(token, true) {
			continue
		}

		stopPrice := math.Round((current+atr*0.4)*100) / 100
		risk := stopPrice - current
		if risk <= 0 {
			continue
		}
		target1 := math.Round((current-risk*1.5)*100) / 100
		target2 := math.Round((current-risk*2.5)*100) / 100

		signals = append(signals, &Signal{
			Strategy:      "S6_TREND_SHORT",
			Symbol:        symbol,
			Token:         token,
			Regime:        regime,
			EntryPrice:    current,
			StopPrice:     stopPrice,
			PartialTarget: target1,
			TargetPrice:   target2,
			RSI:           math.Round(rsi4*100) / 100,
			ATR:           math.Round(atr*100) / 100,
			RVol:          math.Round(rvol*100) / 100,
			VWAP:          math.Round(vwap*100) / 100,
			Product:       "MIS",
			IsShort:       true,
			SortKey:       relativeWeakness,
		})
	}

	sort.Slice(signals, func(i, j int) bool { return signals[i].SortKey > signals[j].SortKey })
	if len(signals) > 3 {
		signals = signals[:3]
	}
	return signals
}

// ══════════════════════════════════════════════════════════════
//  S7: MEAN REVERSION LONG
// ══════════════════════════════════════════════════════════════
func (s *ScannerAgent) ScanS7(regime string) []*Signal {
	if !s.IsInTradeWindow() || !s.checkDailyTradeLimit() || regime == "EXTREME_PANIC" || !s.cacheReady() {
		return nil
	}

	var signals []*Signal
	for token, symbol := range s.Universe {
		// Turnover filter
		turnover := s.DailyCache.TurnoverCr[token]
		if turnover < config.S7_MIN_TURNOVER_CR {
			continue
		}

		current := s.GetLTP(token)
		if current <= 0 {
			continue
		}

		// Intraday ATR check — skip if stock is trending today
		candles := s.getCandles5m(token)
		if len(candles) >= 6 {
			var sumRange float64
			for _, c := range candles[len(candles)-6:] {
				sumRange += c.High - c.Low
			}
			avgIntraRange := sumRange / 6.0
			dailyATR := s.DailyCache.ATR[token]
			if dailyATR > 0 && avgIntraRange > dailyATR*0.50 {
				continue
			}
		}

		// Price > SMA200
		sma200 := s.DailyCache.SMA200[token]
		if current < sma200 || sma200 <= 0 {
			continue
		}

		// VWAP deviation check
		vwap := 0.0
		if s.GetVWAP != nil {
			vwap = s.GetVWAP(token)
		}
		if vwap <= 0 {
			continue
		}
		vwapDev := (current - vwap) / vwap
		if vwapDev > -config.S7_VWAP_DEVIATION_PCT {
			continue
		}

		// Price < BB Lower
		bbLower := s.DailyCache.BBLower[token]
		if current >= bbLower || bbLower <= 0 {
			continue
		}

		// RSI(14) oversold
		closes := s.DailyCache.Closes[token]
		if len(closes) < 10 {
			continue
		}
		liveCloses := make([]float64, len(closes))
		copy(liveCloses, closes)
		liveCloses = append(liveCloses, current)
		rsiSlice := data.ComputeRSI(liveCloses, config.S7_RSI_PERIOD)
		rsi := rsiSlice[len(rsiSlice)-1]
		if rsi >= float64(config.S7_RSI_OVERSOLD) {
			continue
		}

		rvol := s.computeRVol(token)
		if rvol < config.S7_RVOL_MIN {
			continue
		}

		atr := s.DailyCache.ATR[token]
		if atr <= 0 {
			atr = current * 0.02
		}

		stopPrice := math.Round((current-atr*1.5)*100) / 100
		risk := current - stopPrice
		if risk <= 0 {
			continue
		}
		targetPrice := math.Round(vwap*100) / 100
		if (targetPrice - current) < risk*1.5 {
			targetPrice = math.Round((current+risk*1.5)*100) / 100
		}
		if stopPrice >= current*0.998 {
			continue
		}

		signals = append(signals, &Signal{
			Strategy:      "S7_MEAN_REV_LONG",
			Symbol:        symbol,
			Token:         token,
			Regime:        regime,
			EntryPrice:    current,
			StopPrice:     stopPrice,
			PartialTarget: math.Round((current+1.0*risk)*100) / 100,
			TargetPrice:   targetPrice,
			RSI:           math.Round(rsi*100) / 100,
			ATR:           math.Round(atr*100) / 100,
			RVol:          math.Round(rvol*100) / 100,
			VWAP:          math.Round(vwap*100) / 100,
			Product:       "MIS",
			IsShort:       false,
			SortKey:       rsi, // lower RSI = better signal
		})
	}

	sort.Slice(signals, func(i, j int) bool { return signals[i].SortKey < signals[j].SortKey })
	if len(signals) > 3 {
		signals = signals[:3]
	}
	return signals
}
