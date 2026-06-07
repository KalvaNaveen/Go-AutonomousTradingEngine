package agents

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRankSignals_CapsBatchSize verifies that RankSignals never sends more
// than kronosMaxBatchSize signals to the service in one request — the fix
// for the "manual scan never shows Kronos identity" issue, which was caused
// by huge batches (100-190 stocks) timing out the 60s HTTP client on slow
// CPU inference (~12s/stock), silently falling back to no AI ranking.
func TestRankSignals_CapsBatchSize(t *testing.T) {
	var gotBatchSize int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req kronosPredictRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotBatchSize = len(req.Signals)

		ranked := make([]KronosRankedSignal, len(req.Signals))
		for i, s := range req.Signals {
			ranked[i] = KronosRankedSignal{Symbol: s.Symbol, UpsidePct: float64(len(req.Signals) - i)}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(kronosPredictResponse{Ranked: ranked, Model: "test-model"})
	}))
	defer srv.Close()

	client := NewKronosClient(srv.URL)
	// Force every symbol to be "trained" regardless of instruments.csv presence.
	client.trainedSyms = nil

	const totalSignals = 50
	closes := make([]float64, 100)
	for i := range closes {
		closes[i] = 100 + float64(i)
	}
	cache := &DailyCache{
		Closes: map[uint32][]float64{},
		Highs:  map[uint32][]float64{},
		Lows:   map[uint32][]float64{},
		Volumes: map[uint32][]float64{},
		TradingDates: make([]string, 100),
	}
	var results []EODScanResult
	for i := 0; i < totalSignals; i++ {
		tok := uint32(1000 + i)
		cache.Closes[tok] = closes
		cache.Highs[tok] = closes
		cache.Lows[tok] = closes
		cache.Volumes[tok] = closes
		results = append(results, EODScanResult{Symbol: "SYM" + string(rune('A'+i%26)) + string(rune('0'+i/26)), Token: tok, Signal: "BUY"})
	}

	ranked := client.RankSignals(results, cache)

	if gotBatchSize > kronosMaxBatchSize {
		t.Errorf("expected batch sent to Kronos to be capped at %d, but service received %d", kronosMaxBatchSize, gotBatchSize)
	}
	if gotBatchSize == 0 {
		t.Fatal("expected the service to receive a non-empty batch")
	}
	if len(ranked) != totalSignals {
		t.Errorf("expected all %d signals to be returned (ranked + overflow appended), got %d", totalSignals, len(ranked))
	}
	t.Logf("Sent %d of %d signals to Kronos (capped at %d); returned %d total", gotBatchSize, totalSignals, kronosMaxBatchSize, len(ranked))
}
