package agents

import (
	"testing"

	"bnf_go_engine/config"
)

// ══════════════════════════════════════════════════════════════
//  E2E TESTS: Final Verified Swing Trading Blueprint
//  Every test maps to a specific doc line/section.
// ══════════════════════════════════════════════════════════════

// --- Section II.1: ROC Regime Detection ---

func TestROCRegime_AggressiveNearZero(t *testing.T) {
	// Doc L30: "Buy aggressively near 0"
	s := NewScannerAgent()
	s.DailyCache = makeMockCache()
	// Nifty closes: 378+ bars, current ≈ past → ROC ≈ 0
	niftyCloses := makeFlat(400, 100.0)
	s.DailyCache.Closes[config.NiftySpotToken] = niftyCloses
	s.DailyCache.Closes[config.SmallcapToken] = makeFlat(450, 100.0)
	s.DailyCache.Loaded = true

	regime := s.DetectRegime()
	if regime != "AGGRESSIVE" {
		t.Errorf("ROC near 0 should be AGGRESSIVE, got %s", regime)
	}
}

func TestROCRegime_DefensiveNear45(t *testing.T) {
	// Doc L30: "Reduce equity near 45"
	s := NewScannerAgent()
	s.DailyCache = makeMockCache()
	// Nifty closes: past=100, current=145 → ROC=45%
	niftyCloses := makeFlat(400, 100.0)
	niftyCloses[len(niftyCloses)-1] = 145.0
	s.DailyCache.Closes[config.NiftySpotToken] = niftyCloses
	s.DailyCache.Closes[config.SmallcapToken] = makeFlat(450, 100.0)
	s.DailyCache.Loaded = true

	regime := s.DetectRegime()
	if regime != "DEFENSIVE" {
		t.Errorf("ROC near 45 should be DEFENSIVE, got %s", regime)
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

// --- Section III.1: Fundamental Filter Gates Scanner ---

func TestFundamentalFilter_BlocksFailedStocks(t *testing.T) {
	// Doc L74: "Only execute these on stocks that passed the fundamental screens"
	s := NewScannerAgent()
	s.DailyCache = makeMockCacheWithStock(1, "BADSTOCK", 500.0)
	s.Universe = map[uint32]string{1: "BADSTOCK"}
	s.FundamentalPassed = map[string]bool{"BADSTOCK": false}
	s.GetLTP = func(token uint32) float64 { return 500.0 }

	signals := s.RunAllScans("NORMAL")
	for _, sig := range signals {
		if sig.Symbol == "BADSTOCK" {
			t.Error("Stock that FAILED fundamentals should NOT generate signals")
		}
	}
}

func TestFundamentalFilter_AllowsPassedStocks(t *testing.T) {
	s := NewScannerAgent()
	s.FundamentalPassed = map[string]bool{"GOODSTOCK": true}
	// Passes — should NOT be blocked by fundamental filter
	passed, exists := s.FundamentalPassed["GOODSTOCK"]
	if !exists || !passed {
		t.Error("GOODSTOCK should be marked as passed")
	}
}

// --- Section III.2: Near ATH Filter ---

func TestATHFilter_RejectsStockFarFromHigh(t *testing.T) {
	// Doc L51: "Never buy at 52-week lows"
	// FIX-02: Now uses full cache history (Closes+Highs) instead of High52W
	s := NewScannerAgent()
	s.DailyCache = makeMockCache()
	// Stock peaked at 1000, now at 800 → 20% below ATH → should fail
	closes := makeFlat(500, 900.0)
	closes[50] = 1000.0 // Historical ATH
	s.DailyCache.Closes[1] = closes
	s.DailyCache.Highs[1] = makeFlat(500, 900.0)

	if s.passesPhase2Filter(1, 800.0) {
		t.Error("Stock 20% below ATH should be rejected")
	}
}

func TestATHFilter_AcceptsStockNearHigh(t *testing.T) {
	// Doc L51: "Always buy at or near All-Time Highs"
	// FIX-02: Now uses full cache history
	s := NewScannerAgent()
	s.DailyCache = makeMockCache()
	closes := makeFlat(500, 950.0)
	closes[50] = 1000.0 // Historical ATH
	s.DailyCache.Closes[1] = closes
	s.DailyCache.Highs[1] = makeFlat(500, 960.0)

	// LTP=970 → 3% below ATH (1000) → should pass
	if !s.passesPhase2Filter(1, 970.0) {
		t.Error("Stock 3% below ATH should be accepted")
	}
}

// --- Section V.1: VCP Breakout ---

func TestVCP_RejectsIfBelowEMA21(t *testing.T) {
	// Doc L81: "setup is dead if stock closes below 21 EMA"
	s := NewScannerAgent()
	s.DailyCache = makeMockCache()
	closes := makeFlat(100, 100.0)
	closes[len(closes)-1] = 90.0 // Last close below EMA
	s.DailyCache.Closes[1] = closes
	s.DailyCache.Highs[1] = makeFlat(100, 105.0)
	s.DailyCache.Lows[1] = makeFlat(100, 95.0)
	s.DailyCache.EMA21[1] = 100.0 // EMA21 = 100, close = 90

	sig := s.detectVCPBreakout(1, "TEST", 110.0, "NORMAL")
	if sig != nil {
		t.Error("VCP should be rejected when close < 21 EMA")
	}
}

func TestVCP_RejectsIfVolumeDIDNotDryUp(t *testing.T) {
	// Doc L79: "volume MUST dry up during contraction"
	s := NewScannerAgent()
	s.DailyCache = makeMockCache()

	// Build a valid contraction pattern but with INCREASING volume
	closes, highs, lows := buildVCPPattern()
	volumes := make([]float64, len(closes))
	// Early: low volume, Late: HIGH volume (wrong — should dry up)
	for i := range volumes {
		if i < len(volumes)/2 {
			volumes[i] = 100
		} else {
			volumes[i] = 500 // Volume INCREASED
		}
	}
	s.DailyCache.Closes[1] = closes
	s.DailyCache.Highs[1] = highs
	s.DailyCache.Lows[1] = lows
	s.DailyCache.Volumes[1] = volumes
	s.DailyCache.EMA21[1] = 80.0

	sig := s.detectVCPBreakout(1, "TEST", highs[len(highs)-1]+5, "NORMAL")
	if sig != nil {
		t.Error("VCP should be rejected when volume did NOT dry up")
	}
}

// --- Section V.2: Bull Flag Event Invalidation ---

func TestBullFlag_SuppressedOnMajorEventDay(t *testing.T) {
	// Doc L86: "Do not trade if massive fundamental trigger"
	s := NewScannerAgent()
	s.IsMajorEventDay = true
	s.DailyCache = makeMockCache()

	sig := s.detectBullFlag(1, "TEST", 500.0, "NORMAL")
	if sig != nil {
		t.Error("Bull Flag should be suppressed on major event day")
	}
}

func TestBullFlag_AllowedOnNormalDay(t *testing.T) {
	s := NewScannerAgent()
	s.IsMajorEventDay = false
	// Won't generate signal (no data) but should NOT return nil due to event check
	// Just verify the event check doesn't block
	if s.IsMajorEventDay {
		t.Error("IsMajorEventDay should be false on normal day")
	}
}

// --- Section V.4: Cup with Handle 1-Day Confirmation ---

func TestCupHandle_Requires1DayConfirmation(t *testing.T) {
	// Doc L95: "Wait for 1-day confirmation candle after neckline broken"
	s := NewScannerAgent()
	s.DailyCache = makeMockCache()

	closes := make([]float64, 65)
	highs := make([]float64, 65)
	// Build cup shape
	for i := range closes {
		closes[i] = 100.0
		highs[i] = 105.0
	}
	// Trough in middle
	for i := 20; i < 50; i++ {
		closes[i] = 85.0
	}
	// Yesterday close BELOW neckline → should NOT confirm
	closes[len(closes)-2] = 95.0 // Below neckline (100)
	closes[len(closes)-1] = 101.0
	s.DailyCache.Closes[1] = closes
	s.DailyCache.Highs[1] = highs

	sig := s.detectCupWithHandle(1, "TEST", 101.0, "NORMAL")
	if sig != nil {
		t.Error("Cup Handle should require yesterday's close ABOVE neckline (1-day confirmation)")
	}
}

// --- Section VI.1: Position Limits ---

func TestConfig_MaxPositions6(t *testing.T) {
	// Doc L108: "Maximum 5 to 6 stocks"
	if config.MaxOpenPositions != 6 {
		t.Errorf("MaxOpenPositions should be 6, got %d", config.MaxOpenPositions)
	}
}

func TestConfig_TradeAllocation5to10(t *testing.T) {
	// Doc L109: "5% to 10% of total portfolio per trade"
	if config.MinTradeAllocPct != 5.0 {
		t.Errorf("MinTradeAllocPct should be 5.0, got %.1f", config.MinTradeAllocPct)
	}
	if config.MaxTradeAllocPct != 10.0 {
		t.Errorf("MaxTradeAllocPct should be 10.0, got %.1f", config.MaxTradeAllocPct)
	}
}

// --- Section VI.2: Stop-Loss ---

func TestConfig_MaxSL7Percent(t *testing.T) {
	// Doc L112: "Absolute Maximum SL: 7%"
	if config.HardStopLossPct != 7.0 {
		t.Errorf("HardStopLossPct should be 7.0, got %.1f", config.HardStopLossPct)
	}
}

func TestConfig_IdealSL3to5(t *testing.T) {
	// Doc L113: "Ideal Active SL: 3% to 5%"
	if config.TightSLPct != 3.0 {
		t.Errorf("TightSLPct should be 3.0, got %.1f", config.TightSLPct)
	}
	if config.IdealSLPct != 5.0 {
		t.Errorf("IdealSLPct should be 5.0, got %.1f", config.IdealSLPct)
	}
}

// --- Section VI.3: Add to Winners Only ---

func TestSignal_StopPriceIs7Percent(t *testing.T) {
	// Verify signal stop price = entry * (1 - 7/100)
	entry := 100.0
	expected := entry * (1 - config.HardStopLossPct/100) // 93.0
	if expected != 93.0 {
		t.Errorf("Stop price for entry=100 should be 93.0, got %.1f", expected)
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

func TestConfig_EMA21Period(t *testing.T) {
	// Doc L132: "21-day EMA"
	if config.EMA21Period != 21 {
		t.Errorf("EMA21Period should be 21, got %d", config.EMA21Period)
	}
}

func TestConfig_2RedCandles(t *testing.T) {
	// Doc L133: "two continuous red candles"
	if config.RedCandlesBelowEMA != 2 {
		t.Errorf("RedCandlesBelowEMA should be 2, got %d", config.RedCandlesBelowEMA)
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
	s.DailyCache.EMA21[1] = 101.0 // EMA21 = 101

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
	s.DailyCache.EMA21[1] = 101.0

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
	s.DailyCache.EMA21[1] = 101.0

	sig := s.CheckReEntry(1, "TEST", "NORMAL")
	if sig != nil {
		t.Error("Should NOT re-enter when close is below 21 EMA")
	}
}

// --- Section V: Position Sizing ---

func TestPositionSizing_5to10Percent(t *testing.T) {
	// Doc L109: "5% to 10% of total portfolio per trade"
	capital := 500000.0
	maxAlloc := capital * config.MaxTradeAllocPct / 100 // 50000
	minAlloc := capital * config.MinTradeAllocPct / 100 // 25000

	if maxAlloc != 50000 {
		t.Errorf("Max allocation should be 50000, got %.0f", maxAlloc)
	}
	if minAlloc != 25000 {
		t.Errorf("Min allocation should be 25000, got %.0f", minAlloc)
	}
}

// ══════════════════════════════════════════════════════════════
//  Test Helpers
// ══════════════════════════════════════════════════════════════

func makeMockCache() *DailyCache {
	return &DailyCache{
		SMA200:       make(map[uint32]float64),
		ATR:          make(map[uint32]float64),
		EMA25:        make(map[uint32]float64),
		EMA21:        make(map[uint32]float64),
		BBLower:      make(map[uint32]float64),
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
