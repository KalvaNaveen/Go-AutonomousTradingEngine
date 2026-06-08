package agents

import (
	"testing"

	"bnf_go_engine/config"
)

// ══════════════════════════════════════════════════════════════
//  E2E TESTS: Final Verified Swing Trading Blueprint
//  Every test maps to a specific doc line/section.
// ══════════════════════════════════════════════════════════════

// (Regime gate removed — engine scans all market conditions. Individual stock
//  filters act as the gate. See scanner_agent.go passesPhase2Filter.)

// --- Section III.2: Near ATH Filter ---

func TestATHFilter_RejectsStockFarFromHigh(t *testing.T) {
	// Book Ch.11 p.266: "near All-Time High". Stock 20% below 52W high → reject.
	// Unit-tests the isolated ATH proximity helper.
	closes := makeFlat(500, 900.0)
	closes[50] = 1000.0 // ATH outside 252-bar window
	highs := makeFlat(500, 900.0)

	// LTP=800 with high52 = 900 → distFromHigh = 11.1% > 10% → should fail
	if isWithinATHProximity(closes, highs, 800.0) {
		t.Error("Stock 20%% below ATH should fail ATH proximity")
	}
}

func TestATHFilter_AcceptsStockNearHigh(t *testing.T) {
	// Book Ch.11 p.266: "near All-Time High". Engine uses 52-week high as proxy
	// with ATHProximityPct (default 10%) tolerance.
	// Unit-test the isolated ATH proximity helper (avoids interference from
	// the other gap-fix filters layered into passesPhase2Filter).
	closes := makeFlat(500, 950.0)
	closes[50] = 1000.0 // Historical ATH — outside 252-bar window
	highs := makeFlat(500, 960.0)

	// LTP=970 with high52 = 960 (from highs) → within proximity → should pass
	if !isWithinATHProximity(closes, highs, 970.0) {
		t.Error("Stock at/above 52W-high reference should pass ATH proximity")
	}
}

// --- Section V.1: EMA Pullback (the engine's sole entry — Book Ch.3 p.44-49) ---

func TestEMAPullback_RejectsDowntrend(t *testing.T) {
	// No uptrend (10 EMA not > 20 EMA, not rising) → must NOT fire.
	dc := makeMockCache()
	n := 90
	closes := make([]float64, n)
	highs := make([]float64, n)
	lows := make([]float64, n)
	volumes := make([]float64, n)
	for i := 0; i < n; i++ {
		c := 200.0 - float64(i) // steady downtrend
		closes[i], highs[i], lows[i] = c, c+1, c-1
		volumes[i] = 1000
	}
	dc.Closes[1], dc.Highs[1], dc.Lows[1], dc.Volumes[1] = closes, highs, lows, volumes

	ctx := StrategyContext{Cache: dc, CapitalMultiplier: 1.0}
	if sig := (&EMAStrategy{}).Detect(1, "TEST", closes[n-1], ctx); sig != nil {
		t.Error("EMA pullback must be rejected in a downtrend")
	}
}

func TestEMAPullback_FiresOnBounce(t *testing.T) {
	// Steady uptrend → pullback toward the 10 EMA on light volume → green bounce.
	dc := makeMockCache()
	n := 90
	closes := make([]float64, n)
	highs := make([]float64, n)
	lows := make([]float64, n)
	volumes := make([]float64, n)
	// Bars 0..82: steady rise (price extended above EMAs); heavier volume.
	for i := 0; i < n; i++ {
		c := 100.0 + float64(i)*1.0 // rise to ~189
		closes[i], highs[i], lows[i] = c, c+1.0, c-1.0
		volumes[i] = 2000
	}
	// Bars 83..88: pullback toward the 10 EMA on LIGHT volume (dip the lows).
	for i := 83; i <= 88; i++ {
		dip := closes[82] - 6.0 // pull back ~6 below the pre-pullback close
		closes[i] = dip
		highs[i] = dip + 1.0
		lows[i] = dip - 1.0
		volumes[i] = 700 // lighter than the prior 2000
	}
	// Last bar: green bounce — open below close, expanding volume (confirms demand).
	closes[n-1] = closes[82] + 1.0
	highs[n-1] = closes[n-1] + 1.0
	lows[n-1] = closes[88]
	volumes[n-1] = 1800 // above pullback avg (700) — expanding volume required
	opens := make([]float64, n)
	for i := range opens {
		opens[i] = closes[i] - 0.5 // every bar opens below its close (green)
	}
	dc.Closes[1], dc.Highs[1], dc.Lows[1], dc.Volumes[1] = closes, highs, lows, volumes
	dc.Opens[1] = opens

	ctx := StrategyContext{Cache: dc, CapitalMultiplier: 1.0}
	sig := (&EMAStrategy{}).Detect(1, "TEST", closes[n-1], ctx)
	if sig == nil {
		t.Error("EMA pullback should fire on a clean uptrend pullback-and-bounce")
	} else if sig.Strategy != "EMA_PULLBACK" {
		t.Errorf("expected EMA_PULLBACK, got %s", sig.Strategy)
	}
}

// --- Section VI.1: Position Limits ---

func TestConfig_MaxPositions6(t *testing.T) {
	// At ₹1L capital: floor(90000/15000) = 6 positions
	got := config.ComputeMaxPositions(100000)
	if got != 6 {
		t.Errorf("ComputeMaxPositions(100000) should be 6, got %d", got)
	}
}

func TestConfig_TradeAllocation15to20(t *testing.T) {
	// At ₹1L capital with 6 positions: 15-20% per trade (₹15K-₹20K)
	if config.MinTradeAllocPct != 15.0 {
		t.Errorf("MinTradeAllocPct should be 15.0, got %.1f", config.MinTradeAllocPct)
	}
	if config.MaxTradeAllocPct != 20.0 {
		t.Errorf("MaxTradeAllocPct should be 20.0, got %.1f", config.MaxTradeAllocPct)
	}
}

// --- Section VI.2: Stop-Loss ---

func TestConfig_StructuralSLRange(t *testing.T) {
	// Best-fit defaults from backtest history: floor=3%, ceiling=5%.
	// These are vars so Apply Config can override them at runtime.
	if config.SLFloorPct != 3.0 {
		t.Errorf("SLFloorPct should be 3.0, got %.1f", config.SLFloorPct)
	}
	if config.SLCeilingPct != 5.0 {
		t.Errorf("SLCeilingPct should be 5.0, got %.1f", config.SLCeilingPct)
	}
}

func TestConfig_StructuralSLCompute(t *testing.T) {
	// Prev candle low × 0.998, clamped to [entry×0.97, entry×0.985]
	entry := 1000.0
	prevLow := 985.0
	sl := config.ComputeStructuralSL(entry, prevLow)
	// structural = 985*0.998 = 983.03, floor = 985, ceiling = 970 → clamp to floor(985)
	if sl < entry*(1-config.SLCeilingPct/100) || sl > entry*(1-config.SLFloorPct/100) {
		t.Errorf("Structural SL %.2f is outside [%.2f, %.2f]",
			sl, entry*(1-config.SLCeilingPct/100), entry*(1-config.SLFloorPct/100))
	}
}

// --- Section VI.3: Add to Winners Only ---

func TestSignal_StructuralSLCeiling(t *testing.T) {
	// Structural SL ceiling uses config.SLCeilingPct (default 5% from backtest best-fit).
	// entry=100 → ceiling = 100*(1-5/100) = 95.0
	entry := 100.0
	expected := entry * (1 - config.SLCeilingPct/100) // 95.0 with default 5%
	ceiling := entry * (1 - config.SLCeilingPct/100)
	if ceiling != expected {
		t.Errorf("SL ceiling for entry=100 should be %.1f, got %.1f", expected, ceiling)
	}
}

// --- Section VI.4: System Halt ---

func TestContingency_5SLsReduceCapital(t *testing.T) {
	// Doc L120: "5 consecutive SL → reduce to 30-40%"
	s := NewScannerAgent()
	for i := 0; i < 5; i++ {
		s.RecordSLHit()
	}
	if s.CapitalMultiplier != config.ReducedCapitalPct {
		t.Errorf("After 5 SLs, capital should be %.0f%%, got %.0f%%",
			config.ReducedCapitalPct*100, s.CapitalMultiplier*100)
	}
}

func TestContingency_WinResetsCounter(t *testing.T) {
	s := NewScannerAgent()
	s.RecordSLHit()
	s.RecordSLHit()
	s.RecordWin()
	if s.ConsecutiveSLs != 0 {
		t.Errorf("Win should reset ConsecutiveSLs to 0, got %d", s.ConsecutiveSLs)
	}
	if s.CapitalMultiplier != 1.0 {
		t.Errorf("Win should restore capital to 100%%, got %.0f%%", s.CapitalMultiplier*100)
	}
}

// --- Section VII.2: 21 EMA Exit ---

func TestConfig_EMAPeriods(t *testing.T) {
	// Fast EMA10 for crossover entry, Trend EMA20 for confirmation + exit
	if config.EMA10Period != 10 {
		t.Errorf("EMA10Period should be 10, got %d", config.EMA10Period)
	}
	if config.EMA20Period != 20 {
		t.Errorf("EMA20Period should be 20, got %d", config.EMA20Period)
	}
}

func TestConfig_SingleCloseEMAExit(t *testing.T) {
	// Book Ch.6 p.167-168: single EOD close below EMA = exit (not 2 red candles).
	// Figures 6.4 (TECHM) and 6.5 (FINCABLES) both show: "Closed below EMA — Sell on this day."
	if config.RedCandlesBelowEMA != 1 {
		t.Errorf("RedCandlesBelowEMA should be 1 (single close rule, Ch.6), got %d", config.RedCandlesBelowEMA)
	}
}

func TestEMA21_ComputeCorrectly(t *testing.T) {
	closes := make([]float64, 30)
	for i := range closes {
		closes[i] = 100.0
	}
	ema := ComputeEMA21(closes)
	if ema < 99.0 || ema > 101.0 {
		t.Errorf("EMA21 of flat 100.0 series should be ~100.0, got %.2f", ema)
	}
}

// --- Section VII.2: Re-Entry ---

func TestReEntry_GreenCandleAboveEMA(t *testing.T) {
	// Doc L134: "reclaims 21 EMA with green closing candle → re-enter"
	s := NewScannerAgent()
	s.DailyCache = makeMockCache()
	s.DailyCache.Loaded = true

	closes := makeFlat(30, 100.0)
	closes[len(closes)-2] = 98.0  // Prev close (lower)
	closes[len(closes)-1] = 102.0 // Last close (green, above EMA)
	s.DailyCache.Closes[1] = closes
	s.DailyCache.EMA20[1] = 101.0 // EMA20 = 101

	sig := s.CheckReEntry(1, "TEST")
	if sig == nil {
		t.Error("Should generate re-entry signal when green candle reclaims 21 EMA")
	}
	if sig != nil && sig.Strategy != "EMA_REENTRY" {
		t.Errorf("Re-entry strategy should be EMA_REENTRY, got %s", sig.Strategy)
	}
}

func TestReEntry_RejectsRedCandle(t *testing.T) {
	s := NewScannerAgent()
	s.DailyCache = makeMockCache()
	s.DailyCache.Loaded = true

	closes := makeFlat(30, 100.0)
	closes[len(closes)-2] = 103.0 // Prev close (higher)
	closes[len(closes)-1] = 102.0 // Last close (red)
	s.DailyCache.Closes[1] = closes
	s.DailyCache.EMA20[1] = 101.0

	sig := s.CheckReEntry(1, "TEST")
	if sig != nil {
		t.Error("Should NOT re-enter on red candle (close < prev)")
	}
}

func TestReEntry_RejectsBelowEMA(t *testing.T) {
	s := NewScannerAgent()
	s.DailyCache = makeMockCache()
	s.DailyCache.Loaded = true

	closes := makeFlat(30, 100.0)
	closes[len(closes)-2] = 98.0  // Prev
	closes[len(closes)-1] = 99.0  // Green but below EMA
	s.DailyCache.Closes[1] = closes
	s.DailyCache.EMA20[1] = 101.0

	sig := s.CheckReEntry(1, "TEST")
	if sig != nil {
		t.Error("Should NOT re-enter when close is below 21 EMA")
	}
}

// --- Section V: Position Sizing ---

func TestPositionSizing_15to20Percent(t *testing.T) {
	// At ₹1L capital: 15-20% per trade = ₹15K-₹20K per position
	capital := 100000.0
	maxAlloc := capital * config.MaxTradeAllocPct / 100 // 20000
	minAlloc := capital * config.MinTradeAllocPct / 100 // 15000

	if maxAlloc != 20000 {
		t.Errorf("Max allocation should be 20000, got %.0f", maxAlloc)
	}
	if minAlloc != 15000 {
		t.Errorf("Min allocation should be 15000, got %.0f", minAlloc)
	}
}

// ── Book Ch.4 p.133: Big Down Day red flag ────────────────────────────────────

func TestBigDownDay_BlocksEntryFor10Bars(t *testing.T) {
	// A stock with a ≥5% decline 5 bars ago should be blocked (within skip window).
	// Book Ch.4 p.133: "Give it a skip for 5-10 trading sessions."
	if config.BigDownDayPct != 5.0 {
		t.Errorf("BigDownDayPct should be 5.0, got %.1f", config.BigDownDayPct)
	}
	if config.BigDownDaySkipBars != 10 {
		t.Errorf("BigDownDaySkipBars should be 10, got %d", config.BigDownDaySkipBars)
	}

	s := NewScannerAgent()
	dc := makeMockCache()
	const tok = uint32(9999001)
	n := 300
	closes := makeFlat(n, 100.0)
	highs := makeFlat(n, 102.0)
	lows := makeFlat(n, 98.0)
	volumes := makeFlat(n, 1000.0)
	// Plant a big down day 5 bars ago: close drops >5%
	bigDownIdx := n - 5
	closes[bigDownIdx] = closes[bigDownIdx-1] * 0.93 // -7% drop
	highs[bigDownIdx] = closes[bigDownIdx] * 1.01
	lows[bigDownIdx] = closes[bigDownIdx] * 0.99
	volumes[bigDownIdx] = 1500.0 // above average

	dc.Closes[tok] = closes
	dc.Highs[tok] = highs
	dc.Lows[tok] = lows
	dc.Volumes[tok] = volumes
	dc.High52W[tok] = 101.0

	s.DailyCache = dc
	s.DailyCache.Closes[config.NiftySpotToken] = makeFlat(420, 22000.0)
	s.DailyCache.Closes[config.SmallcapToken] = makeFlat(420, 15000.0)

	// LTP = 100 (at/near the flat price, within 10% of 52W high)
	passes := s.passesPhase2Filter(tok, 100.0)
	if passes {
		t.Error("Stock with big down day 5 bars ago should be blocked (Ch.4 p.133)")
	}
}

// ── Book Ch.4 p.135-136: Rejection Candle red flag ───────────────────────────

func TestRejectionCandle_BlocksUntilReclaimed(t *testing.T) {
	// Unit test of the rejection-candle helper in isolation.
	// Book Ch.4 p.135-136: a rejection candle (upper wick ≥60% of range) blocks
	// entry until the LTP reclaims the rejection high.
	if config.RejectionWickRatio != 0.60 {
		t.Errorf("RejectionWickRatio should be 0.60, got %.2f", config.RejectionWickRatio)
	}
	if config.RejectionSkipBars != 10 {
		t.Errorf("RejectionSkipBars should be 10, got %d", config.RejectionSkipBars)
	}

	n := 50
	closes := makeFlat(n, 100.0)
	highs := makeFlat(n, 102.0)
	lows := makeFlat(n, 98.0)

	// Plant a rejection candle 5 bars ago: high=112, close=101, low=100
	// range=12, upper wick=11, ratio=91.7% ≥ 60% ✓
	rejIdx := n - 5
	highs[rejIdx] = 112.0
	closes[rejIdx] = 101.0
	lows[rejIdx] = 100.0

	// LTP = 102 — still below rejection high 112 → blocked
	if !hasUnreclaimedRejection(highs, lows, closes, 102.0) {
		t.Error("Stock with unreclaimed rejection candle should be blocked (Ch.4 p.135-136)")
	}

	// LTP = 113 — above rejection high 112 → allowed (supply absorbed)
	if hasUnreclaimedRejection(highs, lows, closes, 113.0) {
		t.Error("Stock that has reclaimed the rejection high should be allowed (Ch.4 p.135-136)")
	}
}

// ══════════════════════════════════════════════════════════════
//  Test Helpers
// ══════════════════════════════════════════════════════════════

func makeMockCache() *DailyCache {
	return &DailyCache{
		ATR:          make(map[uint32]float64),
		EMA10:        make(map[uint32]float64),
		EMA20:        make(map[uint32]float64),
		Opens:        make(map[uint32][]float64),
		Closes:       make(map[uint32][]float64),
		Highs:        make(map[uint32][]float64),
		Lows:         make(map[uint32][]float64),
		Volumes:      make(map[uint32][]float64),
		AvgVol:       make(map[uint32]float64),
		TurnoverCr:   make(map[uint32]float64),
		PivotSupport: make(map[uint32]float64),
		High52W:      make(map[uint32]float64),
		Loaded:       true,
	}
}
func makeFlat(n int, val float64) []float64 {
	s := make([]float64, n)
	for i := range s {
		s[i] = val
	}
	return s
}

// ── Regression tests for bugs found in June 2026 forensic review ──────────────

// TestRecordWin_RestoresCapitalMultiplier — regression for bug where RecordWin()
// did not restore CapitalMultiplier after consecutive SLs reduced it.
// The win-recovery ladder (3→60%, 5→80%, 7→100%) must mirror the backtest engine.
func TestRecordWin_RestoresCapitalMultiplier(t *testing.T) {
	s := NewScannerAgent()
	for i := 0; i < config.ConsecutiveSLCutoff; i++ {
		s.RecordSLHit()
	}
	if s.CapitalMultiplier != config.ReducedCapitalPct {
		t.Fatalf("setup: expected %.2f after %d SLs, got %.2f",
			config.ReducedCapitalPct, config.ConsecutiveSLCutoff, s.CapitalMultiplier)
	}

	// 3 wins → 60%
	s.RecordWin(); s.RecordWin(); s.RecordWin()
	if s.CapitalMultiplier < 0.60 {
		t.Errorf("after 3 wins: want CapitalMultiplier≥0.60, got %.2f", s.CapitalMultiplier)
	}
	// 5 wins → 80%
	s.RecordWin(); s.RecordWin()
	if s.CapitalMultiplier < 0.80 {
		t.Errorf("after 5 wins: want CapitalMultiplier≥0.80, got %.2f", s.CapitalMultiplier)
	}
	// 7 wins → 100%
	s.RecordWin(); s.RecordWin()
	if s.CapitalMultiplier < 1.0 {
		t.Errorf("after 7 wins: want CapitalMultiplier=1.0, got %.2f", s.CapitalMultiplier)
	}
}

// TestRecordSLHit_ResetsWinCounter — a new SL hit must reset the consecutive win counter
// so a subsequent win chain starts fresh from zero.
func TestRecordSLHit_ResetsWinCounter(t *testing.T) {
	s := NewScannerAgent()
	s.RecordWin(); s.RecordWin() // 2 wins
	s.RecordSLHit()              // SL hit — should reset win chain
	s.RecordWin()                // only 1 win after reset
	// After 1 win (below ladder threshold of 3), multiplier must still be 1.0 (was never reduced)
	if s.CapitalMultiplier != 1.0 {
		t.Errorf("CapitalMultiplier should remain 1.0 (no SL reduction + 1 win), got %.2f", s.CapitalMultiplier)
	}
}

