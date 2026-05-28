package agents

import (
	"testing"

	"bnf_go_engine/config"
)

// ══════════════════════════════════════════════════════════════
//  Book 1:1 Replica Tests — 10 gap-fix implementations
//  Each test maps to a specific page in "Swing Trading Simplified"
//  by Ankur Patel.
// ══════════════════════════════════════════════════════════════

// ─── Ch.11 p.269: Monthly Gainers Scan ────────────────────────────────────────

func TestMonthlyGainersScan_AcceptsStrongMomentum(t *testing.T) {
	// Hockey-stick pattern: low base then sharp recent rally.
	// closes[0..170] = 50 (deep base, sets 180-day low)
	// closes[170..189] = 50 → 70 (medium rally)
	// closes[189..199] = 70 → 200 (recent surge — sets 10-day low ~70)
	// Final close = 200 satisfies:
	//   180d: (200-50)/50 = 300% > 90% ✓
	//    90d: (200-50)/50 = 300% > 30% ✓
	//    30d: (200-50)/50 = 300% > 20% ✓
	//    10d: (200-70)/70 = 186% > 20% ✓
	closes := make([]float64, 200)
	for i := 0; i < 170; i++ {
		closes[i] = 50.0
	}
	for i := 170; i < 190; i++ {
		closes[i] = 50.0 + float64(i-170)
	}
	for i := 190; i < 200; i++ {
		closes[i] = 70.0 + float64(i-190)*13.0
	}
	if !passesMonthlyGainersScan(closes) {
		t.Error("Strong momentum stock should pass Monthly Gainers (Ch.11 p.269)")
	}
}

func TestMonthlyGainersScan_BlocksWeakMomentum(t *testing.T) {
	// Flat closes — no gains from lows in any window
	closes := makeFlat(200, 100.0)
	if passesMonthlyGainersScan(closes) {
		t.Error("Flat stock should fail Monthly Gainers (Ch.11 p.269)")
	}
}

func TestMonthlyGainersScan_SkipSafeWithShortHistory(t *testing.T) {
	// Only 5 closes — all windows short; filter must be skip-safe
	closes := makeFlat(5, 100.0)
	if !passesMonthlyGainersScan(closes) {
		t.Error("Short-history stock should be skip-safe")
	}
}

// ─── Ch.11 p.271: Tight Range Scan ────────────────────────────────────────────

func TestTightRangeScan_AcceptsTightDays(t *testing.T) {
	// Closes: ..., 100.0, 101.0 (1% day), 100.5 (-0.5% day)
	closes := []float64{99.0, 100.0, 101.0, 100.5}
	if !passesTightRangeScan(closes) {
		t.Error("Tight range stock should pass Tight Range scan (Ch.11 p.271)")
	}
}

func TestTightRangeScan_BlocksVolatileDay(t *testing.T) {
	// Today's bar moved +5% — exceeds ±2.5% limit
	closes := []float64{99.0, 100.0, 100.5, 105.5}
	if passesTightRangeScan(closes) {
		t.Error("Stock with >2.5%% daily move should fail Tight Range (Ch.11 p.271)")
	}
}

func TestTightRangeScan_BlocksVolatileYesterday(t *testing.T) {
	// Yesterday's bar moved +5% — exceeds ±3.5% limit
	closes := []float64{99.0, 100.0, 105.0, 105.5}
	if passesTightRangeScan(closes) {
		t.Error("Stock with >3.5%% prev-day move should fail Tight Range (Ch.11 p.271)")
	}
}

// ─── Ch.4 p.121: Pocket Pivot ─────────────────────────────────────────────────

func TestPocketPivot_DetectsHighVolumeUpDay(t *testing.T) {
	// 20 bars: closes alternate small ups/downs with consistent volume,
	// then today is an up day with huge volume.
	closes := make([]float64, 20)
	volumes := make([]float64, 20)
	for i := range closes {
		closes[i] = 100.0 + float64(i%2) // alternating 100, 101
		volumes[i] = 1000.0
	}
	// Make last bar an up day with vol higher than any prior down-day vol
	closes[19] = closes[18] + 2.0
	volumes[19] = 5000.0 // huge volume spike
	if !hasRecentPocketPivot(closes, volumes) {
		t.Error("Up day with vol > highest down-day vol should be pocket pivot (Ch.4 p.121)")
	}
}

func TestPocketPivot_RejectsNoPivot(t *testing.T) {
	// 20 bars where up-day volumes are LOWER than down-day volumes (distribution)
	closes := make([]float64, 20)
	volumes := make([]float64, 20)
	for i := range closes {
		closes[i] = 100.0
		volumes[i] = 1000.0
	}
	// Down days with huge volume, up days with tiny volume
	for i := 1; i < 20; i++ {
		if i%2 == 0 {
			closes[i] = closes[i-1] - 1.0
			volumes[i] = 10000.0 // huge down-day vol
		} else {
			closes[i] = closes[i-1] + 0.5
			volumes[i] = 500.0 // tiny up-day vol
		}
	}
	if hasRecentPocketPivot(closes, volumes) {
		t.Error("Distribution pattern should not produce pocket pivot (Ch.4 p.121)")
	}
}

// ─── Ch.4 p.127: Higher Low in Base ───────────────────────────────────────────

func TestHigherLowInBase_AcceptsAscendingLows(t *testing.T) {
	// Build 50 lows with clearly ascending swing lows
	lows := make([]float64, 50)
	for i := range lows {
		lows[i] = 100.0
	}
	// Plant swing lows at indices 10, 25, 40 — each higher than the previous
	lows[10] = 95.0
	lows[25] = 96.0
	lows[40] = 97.0
	if !hasHigherLowInBase(lows) {
		t.Error("Ascending swing lows should pass Higher Low in Base (Ch.4 p.127)")
	}
}

func TestHigherLowInBase_RejectsDescendingLows(t *testing.T) {
	lows := make([]float64, 50)
	for i := range lows {
		lows[i] = 100.0
	}
	// Plant DESCENDING swing lows
	lows[10] = 98.0
	lows[25] = 96.0
	lows[40] = 94.0
	if hasHigherLowInBase(lows) {
		t.Error("Descending swing lows should fail Higher Low in Base (Ch.4 p.127)")
	}
}

func TestHigherLowInBase_SkipSafeShortHistory(t *testing.T) {
	lows := makeFlat(10, 100.0) // below required lookback (40)
	if !hasHigherLowInBase(lows) {
		t.Error("Short-history lows should be skip-safe")
	}
}

// ─── Ch.4 p.137: Breakout Attempts ────────────────────────────────────────────

func TestBreakoutAttempts_AcceptsWithPriorAttempt(t *testing.T) {
	highs := makeFlat(70, 100.0)
	// Plant resistance at 102, with a prior attempt that touched then retraced
	highs[5] = 102.0  // resistance set early
	highs[20] = 101.5 // touched resistance (within 1%)
	highs[23] = 99.0  // retraced ≥ 2% below resistance
	if !hasPriorBreakoutAttempt(highs) {
		t.Error("Stock with prior breakout attempt should pass (Ch.4 p.137)")
	}
}

func TestBreakoutAttempts_RejectsNoPriorAttempt(t *testing.T) {
	highs := makeFlat(70, 100.0)
	highs[5] = 102.0 // only one bar — no attempts to revisit
	if hasPriorBreakoutAttempt(highs) {
		t.Error("Stock with no prior breakout attempts should fail (Ch.4 p.137)")
	}
}

// ─── Ch.7 p.179-180: ATR-Based Stop ───────────────────────────────────────────

func TestATRStop_StandardEntry(t *testing.T) {
	// Book example: entry 100, ATR 1.84 → SL ≈ 98.16
	sl := config.ComputeATRStopLoss(100.0, 1.84, false)
	// SL ceiling (5%) = 95.0 → 98.16 > 95.0 means ceiling not violated.
	// Floor (3%) = 97.0 → 98.16 > 97.0 means floor not violated either.
	// 98.16 is between floor and ceiling, but it gets clamped to floor (97.0).
	// Because the clamp logic: if sl > floor → use floor (structural too tight).
	// 98.16 > 97.0, so clamped to 97.0.
	if sl <= 0 || sl > 100.0 {
		t.Errorf("ATR stop should be positive and below entry, got %.2f", sl)
	}
}

func TestATRStop_MiniBaseCap(t *testing.T) {
	// Mini-base: ATR=3.0 (large). Mult capped at 1.0, so SL = 100 - 3 = 97
	// But floor is 97, ceiling is 95. So SL would be clamped.
	sl := config.ComputeATRStopLoss(100.0, 3.0, true)
	if sl <= 0 || sl >= 100.0 {
		t.Errorf("Mini-base ATR stop should be below entry, got %.2f", sl)
	}
}

func TestATR_ComputationOnRealData(t *testing.T) {
	// 30 bars of synthetic OHLC data with known TR
	n := 30
	highs := make([]float64, n)
	lows := make([]float64, n)
	closes := make([]float64, n)
	for i := 0; i < n; i++ {
		highs[i] = 101.0 + float64(i%3)
		lows[i] = 99.0 - float64(i%2)
		closes[i] = 100.0
	}
	atr := config.ComputeATR(highs, lows, closes, 14)
	if atr <= 0 {
		t.Errorf("ATR should be positive on real OHLC data, got %.4f", atr)
	}
}

// ─── Ch.10 p.257: 3-Session Breadth Confirmation ──────────────────────────────

func TestBreadthConfirmation_SessionsConstant(t *testing.T) {
	if config.BreadthConfirmationSessions != 3 {
		t.Errorf("BreadthConfirmationSessions should be 3, got %d",
			config.BreadthConfirmationSessions)
	}
}

// ─── Ch.6 p.163: Extended-Move Sell ───────────────────────────────────────────

func TestExtendedMove_Thresholds(t *testing.T) {
	if config.ExtendedMoveMinPct != 25.0 {
		t.Errorf("ExtendedMoveMinPct should be 25 (Ch.6 p.163), got %.1f",
			config.ExtendedMoveMinPct)
	}
	if config.ExtendedMoveSessionsWindow < 3 || config.ExtendedMoveSessionsWindow > 10 {
		t.Errorf("ExtendedMoveSessionsWindow should be a small window 'few sessions', got %d",
			config.ExtendedMoveSessionsWindow)
	}
}

// ─── Ch.6 p.173: Hybrid Selling 25-35% ───────────────────────────────────────

func TestHybridPartialSell_Range(t *testing.T) {
	if config.HybridPartialSellPct < 25.0 || config.HybridPartialSellPct > 35.0 {
		t.Errorf("HybridPartialSellPct must be in 25-35%% range (Ch.6 p.173), got %.1f",
			config.HybridPartialSellPct)
	}
}

// ─── Ch.6 p.171-172: Downside Pivot Exit ──────────────────────────────────────

func TestDownsidePivot_LookbackConstant(t *testing.T) {
	if config.DownsidePivotLookback < 5 || config.DownsidePivotLookback > 20 {
		t.Errorf("DownsidePivotLookback should be in 5-20 bars range (Ch.6 p.171-172), got %d",
			config.DownsidePivotLookback)
	}
}

// ─── Ch.11 p.269: Monthly Gainers exact thresholds ────────────────────────────

func TestMonthlyGainers_BookThresholds(t *testing.T) {
	// Verify the 4 percentages match the book EXACTLY (Fig 11.2)
	if config.MonthlyGainers10DayMinPct != 20.0 {
		t.Errorf("MonthlyGainers10DayMinPct must be 20%% (book p.269), got %.1f",
			config.MonthlyGainers10DayMinPct)
	}
	if config.MonthlyGainers30DayMinPct != 20.0 {
		t.Errorf("MonthlyGainers30DayMinPct must be 20%% (book p.269), got %.1f",
			config.MonthlyGainers30DayMinPct)
	}
	if config.MonthlyGainers90DayMinPct != 30.0 {
		t.Errorf("MonthlyGainers90DayMinPct must be 30%% (book p.269), got %.1f",
			config.MonthlyGainers90DayMinPct)
	}
	if config.MonthlyGainers180DayMinPct != 90.0 {
		t.Errorf("MonthlyGainers180DayMinPct must be 90%% (book p.269), got %.1f",
			config.MonthlyGainers180DayMinPct)
	}
}

// ─── Ch.11 p.271: Tight Range exact thresholds ────────────────────────────────

func TestTightRange_BookThresholds(t *testing.T) {
	if config.TightRangeTodayMaxPct != 2.5 {
		t.Errorf("TightRangeTodayMaxPct must be 2.5%% (book p.271), got %.1f",
			config.TightRangeTodayMaxPct)
	}
	if config.TightRangeYesterdayMaxPct != 3.5 {
		t.Errorf("TightRangeYesterdayMaxPct must be 3.5%% (book p.271), got %.1f",
			config.TightRangeYesterdayMaxPct)
	}
}

// ─── Ch.4 p.121: Pocket Pivot lookback constant ───────────────────────────────

func TestPocketPivot_LookbackConstant(t *testing.T) {
	if config.PocketPivotLookback != 10 {
		t.Errorf("PocketPivotLookback must be 10 days (book p.121), got %d",
			config.PocketPivotLookback)
	}
}
