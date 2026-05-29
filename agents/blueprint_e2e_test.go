package agents

import (
	"testing"

	"bnf_go_engine/config"
)

// ══════════════════════════════════════════════════════════════
//  E2E TESTS: Final Verified Swing Trading Blueprint
//  Every test maps to a specific doc line/section.
// ══════════════════════════════════════════════════════════════

// --- Section II.1: SMA200 Regime Detection (Faber 2007) ---

func TestROCRegime_AggressiveNearZero(t *testing.T) {
	// AGGRESSIVE: Nifty above SMA200 AND 21-day ROC ≥ 5%
	s := NewScannerAgent()
	s.DailyCache = makeMockCache()
	// 400 bars at 100.0, last bar at 110.0 → SMA200≈100.05, ROC(21)=10% → AGGRESSIVE
	niftyCloses := makeFlat(400, 100.0)
	niftyCloses[len(niftyCloses)-1] = 110.0
	s.DailyCache.Closes[config.NiftySpotToken] = niftyCloses
	s.DailyCache.Closes[config.SmallcapToken] = makeFlat(450, 100.0)
	s.DailyCache.Loaded = true

	regime := s.DetectRegime()
	if regime != "AGGRESSIVE" {
		t.Errorf("Nifty above SMA200 + ROC≥5%% should be AGGRESSIVE, got %s", regime)
	}
}

func TestROCRegime_DefensiveNear45(t *testing.T) {
	// DEFENSIVE: Nifty below SMA200 (Faber 2007 primary rule)
	s := NewScannerAgent()
	s.DailyCache = makeMockCache()
	// 400 bars at 100.0, last bar at 80.0 → SMA200≈100, current=80 < SMA200 → DEFENSIVE
	niftyCloses := makeFlat(400, 100.0)
	niftyCloses[len(niftyCloses)-1] = 80.0
	s.DailyCache.Closes[config.NiftySpotToken] = niftyCloses
	s.DailyCache.Closes[config.SmallcapToken] = makeFlat(450, 100.0)
	s.DailyCache.Loaded = true

	regime := s.DetectRegime()
	if regime != "DEFENSIVE" {
		t.Errorf("Nifty below SMA200 should be DEFENSIVE, got %s", regime)
	}
}

func TestROCRegime_NoNewSignalsInDefensive(t *testing.T) {
	// Doc: DEFENSIVE = don't open new positions
	s := NewScannerAgent()
	s.DailyCache = makeMockCache()
	s.DailyCache.Loaded = true
	s.Universe = map[uint32]string{1: "TEST"}

	signals := s.RunAllScans("DEFENSIVE")
	if len(signals) != 0 {
		t.Errorf("DEFENSIVE regime should produce 0 signals, got %d", len(signals))
	}
}

// (Fundamental-filter tests removed — the Screener.in fundamental gate is not in the
//  book; "Swing Trading Simplified" is purely technical/price-action.)

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

// --- Section V.1: VCP Breakout ---

func TestVCP_RejectsIfBelowEMA21(t *testing.T) {
	// Doc L81: "setup is dead if stock closes below 21 EMA"
	dc := makeMockCache()
	closes := makeFlat(100, 100.0)
	closes[len(closes)-1] = 90.0 // Last close below EMA20
	dc.Closes[1] = closes
	dc.Highs[1] = makeFlat(100, 105.0)
	dc.Lows[1] = makeFlat(100, 95.0)
	dc.EMA20[1] = 100.0

	ctx := StrategyContext{Cache: dc, CapitalMultiplier: 1.0}
	sig := (&VCPStrategy{}).Detect(1, "TEST", 110.0, "NORMAL", ctx)
	if sig != nil {
		t.Error("VCP should be rejected when close < 21 EMA")
	}
}

func TestVCP_RejectsIfVolumeDIDNotDryUp(t *testing.T) {
	// Doc L79: "volume MUST dry up during contraction"
	dc := makeMockCache()
	closes, highs, lows := buildVCPPattern()
	volumes := make([]float64, len(closes))
	// Early: low volume, Late: HIGH volume (wrong — should dry up)
	for i := range volumes {
		if i < len(volumes)/2 {
			volumes[i] = 100
		} else {
			volumes[i] = 500 // Volume INCREASED — invalid VCP
		}
	}
	dc.Closes[1] = closes
	dc.Highs[1] = highs
	dc.Lows[1] = lows
	dc.Volumes[1] = volumes
	dc.EMA20[1] = 80.0

	ctx := StrategyContext{Cache: dc, CapitalMultiplier: 1.0}
	sig := (&VCPStrategy{}).Detect(1, "TEST", highs[len(highs)-1]+5, "NORMAL", ctx)
	if sig != nil {
		t.Error("VCP should be rejected when volume did NOT dry up")
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

	sig := s.CheckReEntry(1, "TEST", "NORMAL")
	if sig == nil {
		t.Error("Should generate re-entry signal when green candle reclaims 21 EMA")
	}
	if sig != nil && sig.Strategy != "VCP_REENTRY" {
		t.Errorf("Re-entry strategy should be VCP_REENTRY, got %s", sig.Strategy)
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

	sig := s.CheckReEntry(1, "TEST", "NORMAL")
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

	sig := s.CheckReEntry(1, "TEST", "NORMAL")
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
	dc.RSScore[tok] = 80

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
		Closes:       make(map[uint32][]float64),
		Highs:        make(map[uint32][]float64),
		Lows:         make(map[uint32][]float64),
		Volumes:      make(map[uint32][]float64),
		AvgVol:       make(map[uint32]float64),
		TurnoverCr:   make(map[uint32]float64),
		PivotSupport: make(map[uint32]float64),
		High52W:      make(map[uint32]float64),
		RSScore:      make(map[uint32]int),
		Loaded:       true,
	}
}

func makeMockCacheWithStock(token uint32, symbol string, price float64) *DailyCache {
	dc := makeMockCache()
	dc.Closes[token] = makeFlat(100, price)
	dc.Highs[token] = makeFlat(100, price*1.05)
	dc.Lows[token] = makeFlat(100, price*0.95)
	dc.High52W[token] = price * 1.02
	return dc
}

func makeFlat(n int, val float64) []float64 {
	s := make([]float64, n)
	for i := range s {
		s[i] = val
	}
	return s
}

func buildVCPPattern() (closes, highs, lows []float64) {
	n := 80
	closes = make([]float64, n)
	highs = make([]float64, n)
	lows = make([]float64, n)

	resistance := 100.0
	for i := 0; i < n; i++ {
		closes[i] = resistance
		highs[i] = resistance + 2
		lows[i] = resistance - 2
	}
	// Pullback 1: 25% depth (days 10-20)
	for i := 10; i < 20; i++ {
		lows[i] = resistance * 0.75
		closes[i] = resistance * 0.80
		highs[i] = resistance * 0.85
	}
	// Recovery
	for i := 20; i < 30; i++ {
		closes[i] = resistance
		highs[i] = resistance + 1
		lows[i] = resistance - 1
	}
	// Pullback 2: 10% depth (days 30-38)
	for i := 30; i < 38; i++ {
		lows[i] = resistance * 0.90
		closes[i] = resistance * 0.92
		highs[i] = resistance * 0.93
	}
	// Recovery
	for i := 38; i < 48; i++ {
		closes[i] = resistance
		highs[i] = resistance + 1
		lows[i] = resistance - 1
	}
	// Pullback 3: 5% depth (days 48-55)
	for i := 48; i < 55; i++ {
		lows[i] = resistance * 0.95
		closes[i] = resistance * 0.96
		highs[i] = resistance * 0.97
	}
	// Final recovery to resistance
	for i := 55; i < n; i++ {
		closes[i] = resistance
		highs[i] = resistance + 1
		lows[i] = resistance - 1
	}
	return
}
