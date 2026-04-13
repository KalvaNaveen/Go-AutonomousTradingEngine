package data

import (
	"math"
)

// ComputeEMA matches Python: k = 2 / (period + 1), seed = SMA of first period.
func ComputeEMA(prices []float64, period int) []float64 {
	if len(prices) < period {
		// Python returns the original prices, but we should return identical length or just what we have.
		// For Go, avoiding slice modification of arguments is safer.
		res := make([]float64, len(prices))
		copy(res, prices)
		return res
	}
	
	k := 2.0 / float64(period+1)
	
	// Seed is simple moving average of first 'period' elements
	var sum float64
	for i := 0; i < period; i++ {
		sum += prices[i]
	}
	seed := sum / float64(period)
	
	ema := make([]float64, 0, len(prices)-period+1)
	ema = append(ema, seed)
	
	for i := period; i < len(prices); i++ {
		nextEma := (prices[i] * k) + (ema[len(ema)-1] * (1.0 - k))
		ema = append(ema, nextEma)
	}
	return ema
}

// ComputeRSI matches Python DataAgent.compute_rsi calculation using Wilder's smoothing
func ComputeRSI(prices []float64, period int) []float64 {
	if len(prices) < period+1 {
		return []float64{50.0}
	}

	deltas := make([]float64, len(prices)-1)
	for i := 1; i < len(prices); i++ {
		deltas[i-1] = prices[i] - prices[i-1]
	}

	gains := make([]float64, len(deltas))
	losses := make([]float64, len(deltas))
	for i, d := range deltas {
		if d > 0 {
			gains[i] = d
		} else if d < 0 {
			losses[i] = -d
		}
	}

	var ag, al float64
	for i := 0; i < period; i++ {
		ag += gains[i]
		al += losses[i]
	}
	ag /= float64(period)
	al /= float64(period)

	rsiSize := len(deltas) - period
	if rsiSize <= 0 {
		return []float64{50.0}
	}

	rsi := make([]float64, 0, rsiSize)
	
	for i := period; i < len(deltas); i++ {
		ag = (ag*float64(period-1) + gains[i]) / float64(period)
		al = (al*float64(period-1) + losses[i]) / float64(period)
		
		var rs float64
		if al != 0 {
			rs = ag / al
		} else {
			rs = 100
		}
		
		rsiVal := 100.0 - (100.0 / (1.0 + rs))
		rsi = append(rsi, rsiVal)
	}
	
	if len(rsi) == 0 {
		return []float64{50.0}
	}
	return rsi
}

// ComputeBollinger matches DataAgent.compute_bollinger
func ComputeBollinger(prices []float64, period int, sd float64) (upper float64, middle float64, lower float64) {
	if len(prices) == 0 {
		return 0, 0, 0
	}
	if len(prices) < period {
		last := prices[len(prices)-1]
		return last, last, last
	}

	window := prices[len(prices)-period:]
	var sum float64
	for _, p := range window {
		sum += p
	}
	m := sum / float64(period)

	var varianceSum float64
	for _, p := range window {
		// Python np.std defaults to Sample variance? No, np.std defaults to ddof=0 which is Population Variance.
		diff := p - m
		varianceSum += diff * diff
	}
	s := math.Sqrt(varianceSum / float64(period))

	return m + (sd * s), m, m - (sd * s)
}

// ComputeADX matches TA-Lib ADX using full Wilder's smoothing for TR, +DM, -DM, and ADX.
func ComputeADX(highs []float64, lows []float64, closes []float64, period int) float64 {
	if len(highs) < period+2 {
		return 0.0
	}

	trList := make([]float64, 0, len(highs)-1)
	plusDM := make([]float64, 0, len(highs)-1)
	minusDM := make([]float64, 0, len(highs)-1)

	for i := 1; i < len(highs); i++ {
		h, l, pc := highs[i], lows[i], closes[i-1]

		tr1 := h - l
		tr2 := math.Abs(h - pc)
		tr3 := math.Abs(l - pc)
		tr := math.Max(tr1, math.Max(tr2, tr3))
		trList = append(trList, tr)

		up := highs[i] - highs[i-1]
		down := lows[i-1] - lows[i]

		var pdm, mdm float64
		if up > down && up > 0 {
			pdm = up
		}
		if down > up && down > 0 {
			mdm = down
		}
		plusDM = append(plusDM, pdm)
		minusDM = append(minusDM, mdm)
	}

	if len(trList) < period {
		return 0.0
	}

	// Step 1: Seed with SMA of first 'period' values
	var atr, smoothPDM, smoothMDM float64
	for i := 0; i < period; i++ {
		atr += trList[i]
		smoothPDM += plusDM[i]
		smoothMDM += minusDM[i]
	}

	// Step 2: Build DX series using Wilder's smoothing for TR, +DM, -DM
	dxList := make([]float64, 0, len(trList)-period)

	for i := period; i < len(trList); i++ {
		// Wilder's smoothing: prev - (prev/period) + current
		atr = atr - (atr / float64(period)) + trList[i]
		smoothPDM = smoothPDM - (smoothPDM / float64(period)) + plusDM[i]
		smoothMDM = smoothMDM - (smoothMDM / float64(period)) + minusDM[i]

		var pdi, mdi float64
		if atr > 0 {
			pdi = 100 * smoothPDM / atr
			mdi = 100 * smoothMDM / atr
		}

		var dx float64
		if (pdi + mdi) > 0 {
			dx = 100 * math.Abs(pdi-mdi) / (pdi + mdi)
		}
		dxList = append(dxList, dx)
	}

	if len(dxList) < period {
		if len(dxList) == 0 {
			return 0.0
		}
		var sum float64
		for _, v := range dxList {
			sum += v
		}
		return math.Round(sum/float64(len(dxList))*100) / 100
	}

	// Step 3: ADX = Wilder's recursive smoothing of DX (not simple average)
	// Seed ADX with SMA of first 'period' DX values
	var adxSeed float64
	for i := 0; i < period; i++ {
		adxSeed += dxList[i]
	}
	adx := adxSeed / float64(period)

	// Apply Wilder's smoothing: ADX = (prevADX * (period-1) + currentDX) / period
	for i := period; i < len(dxList); i++ {
		adx = (adx*float64(period-1) + dxList[i]) / float64(period)
	}

	return math.Round(adx*100) / 100
}

// ComputeMACD translates DataAgent.compute_macd
func ComputeMACD(prices []float64, fast int, slow int, signal int) (macd float64, sigLine float64, hist float64) {
	if len(prices) < slow+signal {
		return 0.0, 0.0, 0.0
	}

	emaFast := ComputeEMA(prices, fast)
	emaSlow := ComputeEMA(prices, slow)

	macdLine := make([]float64, 0, len(emaSlow))
	// Align indices. emaFast has length len(prices)-fast+1
	// emaSlow has length len(prices)-slow+1
	for i := 0; i < len(emaSlow); i++ {
		fIdx := len(emaFast) - len(emaSlow) + i
		macdLine = append(macdLine, emaFast[fIdx]-emaSlow[i])
	}

	// signal is EMA of MACD over `signal` period
	// Extract the last (signal*3) if available, normally python slice is `macd_line[-signal * 3:]`
	startIdx := len(macdLine) - (signal * 3)
	if startIdx < 0 {
		startIdx = 0
	}
	macdSlice := macdLine[startIdx:]
	
	sLine := ComputeEMA(macdSlice, signal)
	if len(sLine) == 0 {
		return 0.0, 0.0, 0.0
	}
	
	macdVal := macdLine[len(macdLine)-1]
	signalVal := sLine[len(sLine)-1]
	
	return math.Round(macdVal*10000)/10000, 
	       math.Round(signalVal*10000)/10000, 
		   math.Round((macdVal-signalVal)*10000)/10000
}
