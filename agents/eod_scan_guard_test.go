package agents

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestRunEODMarketScan_RefusesOverlap verifies the in-flight guard: if
// RunEODMarketScan is already running, a second concurrent call must bail
// out immediately rather than racing on shared state (scanner.DailyCache).
func TestRunEODMarketScan_RefusesOverlap(t *testing.T) {
	var entered, completed int32

	// Deps whose LoadUniverse blocks long enough for a second call to overlap.
	slowDeps := EODScanDeps{
		LoadUniverse: func() (map[uint32]string, map[uint32]string) {
			atomic.AddInt32(&entered, 1)
			time.Sleep(300 * time.Millisecond)
			return nil, nil // empty universe -> first run aborts quickly after the guard section
		},
	}

	scanner := &ScannerAgent{DailyCache: &DailyCache{Loaded: true}}

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			RunEODMarketScan(slowDeps, scanner)
			atomic.AddInt32(&completed, 1)
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt32(&entered); got != 1 {
		t.Errorf("expected LoadUniverse to be entered exactly once (overlap should be refused), got %d", got)
	}
	if got := atomic.LoadInt32(&completed); got != 2 {
		t.Errorf("expected both goroutines to return, got %d", got)
	}
	if atomic.LoadInt32(&eodScanInFlight) != 0 {
		t.Error("expected eodScanInFlight to be reset to 0 after completion")
	}
}
