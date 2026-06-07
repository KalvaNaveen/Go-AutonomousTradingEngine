package agents

import (
	"testing"
)

// TestKronosRankSignals_LivePipeline drives the REAL Go integration path:
// EODScanResult + DailyCache → RankSignals() → live Kronos service → re-ranked results.
// Requires the Kronos service running on localhost:8765 (skips if unreachable).
func TestKronosRankSignals_LivePipeline(t *testing.T) {
	client := NewKronosClient("http://localhost:8765")
	if !client.IsAlive() {
		t.Skip("Kronos service not reachable on localhost:8765 — skipping live pipeline test")
	}

	const n = 95
	closes := make([]float64, n)
	highs := make([]float64, n)
	lows := make([]float64, n)
	vols := make([]float64, n)
	dates := make([]string, n)
	price := 1500.0
	for i := 0; i < n; i++ {
		price *= 1.0 + (float64(i%7)-3)*0.004
		closes[i] = price
		highs[i] = price * 1.01
		lows[i] = price * 0.99
		vols[i] = 200000
		dates[i] = "2026-0" + string(rune('1'+(i/28))) + "-15"
	}

	cache := &DailyCache{
		Closes:       map[uint32][]float64{101: closes, 102: closes},
		Highs:        map[uint32][]float64{101: highs, 102: highs},
		Lows:         map[uint32][]float64{101: lows, 102: lows},
		Volumes:      map[uint32][]float64{101: vols, 102: vols},
		TradingDates: dates,
	}

	results := []EODScanResult{
		{Symbol: "RELIANCE", Token: 101, LTP: closes[n-1]},
		{Symbol: "TCS", Token: 102, LTP: closes[n-1]},
	}

	ranked := client.RankSignals(results, cache)

	if len(ranked) != 2 {
		t.Fatalf("expected 2 ranked results, got %d", len(ranked))
	}
	for _, r := range ranked {
		t.Logf("Ranked: %s  KronosUpside=%.2f%%  LTP=%.2f", r.Symbol, r.KronosUpside, r.LTP)
	}
	// At least one of the trained symbols should have received a non-zero upside score
	// from the live model (proves real round-trip, not a fallback no-op).
	gotScore := false
	for _, r := range ranked {
		if r.KronosUpside != 0 {
			gotScore = true
		}
	}
	if !gotScore {
		t.Error("expected at least one symbol to receive a Kronos upside score from the live service — got all zeros (service may have returned fallback/error)")
	}
}
