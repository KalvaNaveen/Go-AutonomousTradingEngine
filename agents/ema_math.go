package agents

// computeSMA calculates the simple moving average of the last `period` values.
func computeSMA(closes []float64, period int) float64 {
	if len(closes) < period {
		return 0
	}
	sum := 0.0
	for _, c := range closes[len(closes)-period:] {
		sum += c
	}
	return sum / float64(period)
}

// computeMACDHistogram computes the MACD histogram series using the given
// fast/slow EMA periods and a signal EMA period.
//
//	MACD line  = EMA(fast) − EMA(slow)
//	Signal     = EMA(signal) of MACD line
//	Histogram  = MACD line − Signal
//
// Returns the histogram series aligned to the end of `closes`, or nil when
// there is not enough data. The caller only needs the last 2 values to check
// histogram direction on the bounce bar.
func computeMACDHistogram(closes []float64, fastPeriod, slowPeriod, signalPeriod int) []float64 {
	fastEMAs := computeEMASeries(closes, fastPeriod)
	slowEMAs := computeEMASeries(closes, slowPeriod)
	if len(fastEMAs) == 0 || len(slowEMAs) == 0 {
		return nil
	}

	// Align: the slow series is shorter by (slowPeriod - fastPeriod) bars.
	// Trim the start of fastEMAs so both series cover the same bars.
	offset := len(fastEMAs) - len(slowEMAs)
	if offset < 0 {
		return nil // unexpected length mismatch
	}
	alignedFast := fastEMAs[offset:]

	n := len(slowEMAs)
	if len(alignedFast) < n {
		n = len(alignedFast)
	}

	// Build MACD line
	macdLine := make([]float64, n)
	for i := 0; i < n; i++ {
		macdLine[i] = alignedFast[i] - slowEMAs[i]
	}

	// Signal line = EMA of MACD line
	signalLine := computeEMASeries(macdLine, signalPeriod)
	if len(signalLine) < 2 {
		return nil
	}

	// Histogram = MACD line − Signal line (aligned to end)
	macdOffset := n - len(signalLine)
	if macdOffset < 0 {
		return nil
	}
	hist := make([]float64, len(signalLine))
	for i := range signalLine {
		hist[i] = macdLine[macdOffset+i] - signalLine[i]
	}
	return hist
}

// computeEMASeries returns the full EMA series for the given period.
// Seed = SMA of first `period` closes, then exponential smoothing forward.
// Returns only the non-zero tail (length = len(closes) - period + 1).
func computeEMASeries(closes []float64, period int) []float64 {
	if len(closes) < period {
		return nil
	}
	result := make([]float64, len(closes))
	k := 2.0 / float64(period+1)
	seed := 0.0
	for i := 0; i < period; i++ {
		seed += closes[i]
	}
	result[period-1] = seed / float64(period)
	for i := period; i < len(closes); i++ {
		result[i] = closes[i]*k + result[i-1]*(1-k)
	}
	return result[period-1:]
}
