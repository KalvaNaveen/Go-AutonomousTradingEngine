package data

import (
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


