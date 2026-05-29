package agents

// findSharpestRise finds the sub-window with the highest percentage gain meeting constraints.
// Only searches the most recent 90 candles to avoid matching stale historical poles.
// minGain: minimum percentage gain for a valid pole
// minLen/maxLen: min/max candle count for the pole
func findSharpestRise(closes []float64, minGain float64, minLen, maxLen int) (int, int) {
	bestGain := 0.0
	bestStart := -1
	bestEnd := -1

	searchStart := len(closes) - 90
	if searchStart < 0 {
		searchStart = 0
	}

	for start := searchStart; start < len(closes)-minLen; start++ {
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
