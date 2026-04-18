package main

import (
	"fmt"
	"log"
	"math"
	"path/filepath"
	"time"

	"bnf_go_engine/config"
	"bnf_go_engine/simulator"

	"github.com/joho/godotenv"
)

const (
	LookbackDays       = 180
	TargetMonthlyPct   = 2.0
	MonthsInLookback   = 6.0 // 180 / 30
)

// paramSet describes one dimension of the grid search
type paramSet struct {
	Name   string
	Values []float64
	Apply  func(float64) // sets the config var
	Reset  func()        // restores default
}

// intParamSet for integer config vars 
type intParamSet struct {
	Name   string
	Values []int
	Apply  func(int)
	Reset  func()
}

// strategyOptResult stores the best result for a single strategy
type strategyOptResult struct {
	Strategy      string
	TargetMet     bool
	MonthlyReturn float64
	TotalPnL      float64
	WinRate       float64
	TotalTrades   int
	ProfitFactor  float64
	ParamNames    []string
	ParamValues   []string
	Elapsed       time.Duration
}

func main() {
	// config.BaseDir is already set correctly by config.init() (uses CWD for go run)
	_ = godotenv.Load(filepath.Join(config.BaseDir, ".env"))
	config.Reload()

	fmt.Println("══════════════════════════════════════════════════════════")
	fmt.Println("  BNF STRATEGY OPTIMIZER — Grid Search Over 180 Days")
	fmt.Printf("  Capital: ₹%.0f | Target: ≥%.1f%% monthly return\n", config.TotalCapital, TargetMonthlyPct)
	fmt.Printf("  BaseDir: %s\n", config.BaseDir)
	fmt.Printf("  Date Range: %s to %s\n",
		config.TodayIST().AddDate(0, 0, -LookbackDays).Format("2006-01-02"),
		config.TodayIST().Format("2006-01-02"))
	fmt.Println("══════════════════════════════════════════════════════════")

	strategies := []string{
		"S1_MA_CROSS", "S2_BB_MEAN_REV", "S3_ORB",
		"S6_TREND_SHORT", "S6_VWAP_BAND", "S7_MEAN_REV_LONG",
		"S8_VOL_PIVOT", "S9_MTF_MOMENTUM",
		"S10_GAP_FILL", "S11_VWAP_REVERT", "S12_EOD_REVERT", "S13_SECTOR_ROT",
		"S14_RSI_SCALP", "S15_RSI_SWING",
	}

	var allResults []strategyOptResult

	for _, stratName := range strategies {
		fmt.Printf("\n\n▶ OPTIMIZING: %s\n", stratName)
		start := time.Now()
		result := optimizeStrategy(stratName)
		result.Elapsed = time.Since(start)
		allResults = append(allResults, result)
		printResult(result)
	}

	// Final summary
	fmt.Println("\n\n══════════════════════════════════════════════════════════")
	fmt.Println("  OPTIMIZATION COMPLETE — SUMMARY")
	fmt.Println("══════════════════════════════════════════════════════════")
	met := 0
	for _, r := range allResults {
		status := "❌ NOT MET"
		if r.TargetMet {
			status = "✅ TARGET MET"
			met++
		}
		fmt.Printf("  %-20s  %s  Monthly: %+.2f%%  PnL: ₹%.0f  Trades: %d\n",
			r.Strategy, status, r.MonthlyReturn, r.TotalPnL, r.TotalTrades)
	}
	fmt.Printf("\n  %d / %d strategies met the %.1f%% monthly target.\n", met, len(allResults), TargetMonthlyPct)
	fmt.Println("══════════════════════════════════════════════════════════")
}

func optimizeStrategy(stratName string) strategyOptResult {
	grid := buildGrid(stratName)

	if len(grid.combos) == 0 {
		// No tunable params (S10, S11, S12, S13) — just run once with defaults
		fmt.Printf("  [%s] No tunable config params — running single baseline backtest\n", stratName)
		res, err := runBacktest(stratName)
		if err != nil {
			log.Printf("  [%s] Backtest error: %v\n", stratName, err)
			return strategyOptResult{Strategy: stratName}
		}
		monthly := computeMonthly(res)
		return strategyOptResult{
			Strategy: stratName, TargetMet: monthly >= TargetMonthlyPct,
			MonthlyReturn: monthly, TotalPnL: res.TotalPnL, WinRate: res.WinRate,
			TotalTrades: res.TotalTrades, ProfitFactor: res.ProfitFactor,
			ParamNames: []string{"(defaults)"}, ParamValues: []string{"—"},
		}
	}

	fmt.Printf("  [%s] Grid size: %d combinations across %d params\n",
		stratName, len(grid.combos), len(grid.paramNames))

	var bestMonthly float64 = -999
	var bestResult *simulator.BacktestResult
	var bestComboIdx int = -1

	for i, combo := range grid.combos {
		// Apply this parameter combination
		combo.apply()

		res, err := runBacktest(stratName)
		if err != nil {
			if i == 0 {
				fmt.Printf("    ⚠ First combo error: %v\n", err)
			}
			combo.restore()
			continue
		}
		monthly := computeMonthly(res)

		if i%10 == 0 || monthly > bestMonthly {
			fmt.Printf("    combo %d/%d → Trades=%d PnL=₹%.0f Monthly=%.2f%%\n",
				i+1, len(grid.combos), res.TotalTrades, res.TotalPnL, monthly)
		}

		if monthly > bestMonthly {
			bestMonthly = monthly
			bestResult = res
			bestComboIdx = i
		}

		// Restore defaults before next combo
		combo.restore()
	}

	if bestResult == nil {
		return strategyOptResult{Strategy: stratName}
	}

	// Collect winning param names/values
	var paramNames, paramValues []string
	for _, p := range grid.combos[bestComboIdx].params {
		paramNames = append(paramNames, p.name)
		paramValues = append(paramValues, p.valueStr)
	}

	return strategyOptResult{
		Strategy: stratName, TargetMet: bestMonthly >= TargetMonthlyPct,
		MonthlyReturn: bestMonthly, TotalPnL: bestResult.TotalPnL,
		WinRate: bestResult.WinRate, TotalTrades: bestResult.TotalTrades,
		ProfitFactor: bestResult.ProfitFactor,
		ParamNames: paramNames, ParamValues: paramValues,
	}
}

func runBacktest(stratName string) (*simulator.BacktestResult, error) {
	endDate := config.TodayIST().Format("2006-01-02")
	startDate := config.TodayIST().AddDate(0, 0, -LookbackDays).Format("2006-01-02")

	bt := simulator.NewBacktester(simulator.BacktestConfig{
		StartDate:      startDate,
		EndDate:        endDate,
		InitialCapital: config.TotalCapital,
		MaxPositions:   config.MaxOpenPositions,
		RiskPerTrade:   config.MaxRiskPerTradePct,
		Strategies:     []string{stratName},
	})

	// Suppress verbose logs during optimization
	bt.LogOutput = func(format string, v ...interface{}) {}

	return bt.Run()
}

func computeMonthly(res *simulator.BacktestResult) float64 {
	if config.TotalCapital <= 0 {
		return 0
	}
	return (res.TotalPnL / config.TotalCapital) / MonthsInLookback * 100
}

func printResult(r strategyOptResult) {
	status := "NOT MET"
	if r.TargetMet {
		status = "TARGET MET ✅"
	}
	fmt.Println("═══════════════════════════════════════════════════")
	fmt.Printf("Strategy      : %s\n", r.Strategy)
	fmt.Printf("Status        : %s\n", status)
	fmt.Printf("Monthly Return: %.2f%%\n", r.MonthlyReturn)
	fmt.Printf("Total PnL     : ₹%.0f\n", r.TotalPnL)
	fmt.Printf("Win Rate      : %.1f%%\n", r.WinRate)
	fmt.Printf("Total Trades  : %d\n", r.TotalTrades)
	fmt.Printf("Profit Factor : %.2f\n", r.ProfitFactor)
	fmt.Printf("Elapsed       : %s\n", r.Elapsed.Round(time.Second))
	fmt.Println("Winning Params:")
	for i, name := range r.ParamNames {
		val := "—"
		if i < len(r.ParamValues) {
			val = r.ParamValues[i]
		}
		fmt.Printf("  %s = %s\n", name, val)
	}
	fmt.Println("═══════════════════════════════════════════════════")
}

// ════════════════════════════════════════════════════════════════
//  GRID CONSTRUCTION — one per strategy
// ════════════════════════════════════════════════════════════════

type paramSnapshot struct {
	name     string
	valueStr string
}

type comboEntry struct {
	params  []paramSnapshot
	apply   func()
	restore func()
}

type grid struct {
	paramNames []string
	combos     []comboEntry
}

// floatRange generates values from lo to hi at step increments
func floatRange(lo, hi, step float64) []float64 {
	var vals []float64
	for v := lo; v <= hi+step*0.01; v += step {
		vals = append(vals, math.Round(v*1000)/1000)
	}
	return vals
}

// intRange generates values from lo to hi at step increments
func intRange(lo, hi, step int) []int {
	var vals []int
	for v := lo; v <= hi; v += step {
		vals = append(vals, v)
	}
	return vals
}

func buildGrid(stratName string) grid {
	switch stratName {
	case "S1_MA_CROSS":
		return buildS1Grid()
	case "S2_BB_MEAN_REV":
		return buildS2Grid()
	case "S3_ORB":
		return buildS3Grid()
	case "S6_TREND_SHORT":
		return buildS6TrendGrid()
	case "S6_VWAP_BAND":
		return buildS6VWAPGrid()
	case "S7_MEAN_REV_LONG":
		return buildS7Grid()
	case "S8_VOL_PIVOT":
		return buildS8Grid()
	case "S9_MTF_MOMENTUM":
		return buildS9Grid()
	case "S14_RSI_SCALP":
		return buildS14Grid()
	case "S15_RSI_SWING":
		return buildS15Grid()
	default:
		// S10, S11, S12, S13 — no tunable config params
		return grid{}
	}
}

// ═══ S1: MA CROSS ═══
func buildS1Grid() grid {
	// Tunable: ATR_SL_MULT (1.0-2.5), RR (1.5-4.5), ADX_MIN (12-30)
	atrVals := floatRange(0.8, 2.5, 0.5)
	rrVals := floatRange(1.5, 4.5, 1.0)
	adxVals := intRange(12, 30, 6)

	defaults := struct{ atr, rr float64; adx int }{config.S1_ATR_SL_MULT, config.S1_RR, config.S1_ADX_MIN}

	var combos []comboEntry
	for _, atr := range atrVals {
		for _, rr := range rrVals {
			for _, adx := range adxVals {
				a, r, d := atr, rr, adx
				combos = append(combos, comboEntry{
					params: []paramSnapshot{
						{"S1_ATR_SL_MULT", fmt.Sprintf("%.1f", a)},
						{"S1_RR", fmt.Sprintf("%.1f", r)},
						{"S1_ADX_MIN", fmt.Sprintf("%d", d)},
					},
					apply: func() {
						config.S1_ATR_SL_MULT = a
						config.S1_RR = r
						config.S1_ADX_MIN = d
					},
					restore: func() {
						config.S1_ATR_SL_MULT = defaults.atr
						config.S1_RR = defaults.rr
						config.S1_ADX_MIN = defaults.adx
					},
				})
			}
		}
	}
	return grid{paramNames: []string{"S1_ATR_SL_MULT", "S1_RR", "S1_ADX_MIN"}, combos: combos}
}

// ═══ S2: BB MEAN REV ═══
func buildS2Grid() grid {
	sdVals := floatRange(1.0, 3.0, 0.5)
	rsiOSVals := intRange(20, 40, 5)
	rrVals := floatRange(0.8, 2.0, 0.4)

	defaults := struct{ sd float64; rsiOS int; rr float64 }{config.S2_BB_SD, config.S2_RSI_OVERSOLD, config.S2_RR}

	var combos []comboEntry
	for _, sd := range sdVals {
		for _, rsiOS := range rsiOSVals {
			for _, rr := range rrVals {
				s, r, rv := sd, rsiOS, rr
				combos = append(combos, comboEntry{
					params: []paramSnapshot{
						{"S2_BB_SD", fmt.Sprintf("%.1f", s)},
						{"S2_RSI_OVERSOLD", fmt.Sprintf("%d", r)},
						{"S2_RR", fmt.Sprintf("%.1f", rv)},
					},
					apply: func() {
						config.S2_BB_SD = s
						config.S2_RSI_OVERSOLD = r
						config.S2_RR = rv
					},
					restore: func() {
						config.S2_BB_SD = defaults.sd
						config.S2_RSI_OVERSOLD = defaults.rsiOS
						config.S2_RR = defaults.rr
					},
				})
			}
		}
	}
	return grid{paramNames: []string{"S2_BB_SD", "S2_RSI_OVERSOLD", "S2_RR"}, combos: combos}
}

// ═══ S3: ORB ═══
func buildS3Grid() grid {
	targetVals := floatRange(0.5, 2.0, 0.25)

	defaults := config.S3_TARGET_MULT

	var combos []comboEntry
	for _, t := range targetVals {
		tv := t
		combos = append(combos, comboEntry{
			params: []paramSnapshot{{"S3_TARGET_MULT", fmt.Sprintf("%.2f", tv)}},
			apply:  func() { config.S3_TARGET_MULT = tv },
			restore: func() { config.S3_TARGET_MULT = defaults },
		})
	}
	return grid{paramNames: []string{"S3_TARGET_MULT"}, combos: combos}
}

// ═══ S6 TREND SHORT ═══
func buildS6TrendGrid() grid {
	rsiLowVals := intRange(50, 70, 5)
	rvolVals := floatRange(0.8, 2.0, 0.3)
	turnoverVals := floatRange(15.0, 50.0, 10.0)

	defaults := struct{ rsiLow int; rvol, turnover float64 }{config.S6_RSI_ENTRY_LOW, config.S6_RVOL_MIN, config.S6_MIN_TURNOVER_CR}

	var combos []comboEntry
	for _, rsiL := range rsiLowVals {
		for _, rv := range rvolVals {
			for _, tn := range turnoverVals {
				r, v, t := rsiL, rv, tn
				combos = append(combos, comboEntry{
					params: []paramSnapshot{
						{"S6_RSI_ENTRY_LOW", fmt.Sprintf("%d", r)},
						{"S6_RVOL_MIN", fmt.Sprintf("%.1f", v)},
						{"S6_MIN_TURNOVER_CR", fmt.Sprintf("%.0f", t)},
					},
					apply: func() {
						config.S6_RSI_ENTRY_LOW = r
						config.S6_RVOL_MIN = v
						config.S6_MIN_TURNOVER_CR = t
					},
					restore: func() {
						config.S6_RSI_ENTRY_LOW = defaults.rsiLow
						config.S6_RVOL_MIN = defaults.rvol
						config.S6_MIN_TURNOVER_CR = defaults.turnover
					},
				})
			}
		}
	}
	return grid{paramNames: []string{"S6_RSI_ENTRY_LOW", "S6_RVOL_MIN", "S6_MIN_TURNOVER_CR"}, combos: combos}
}

// ═══ S6 VWAP BAND ═══
func buildS6VWAPGrid() grid {
	sdVals := floatRange(1.0, 3.0, 0.5)
	rrVals := floatRange(0.8, 2.5, 0.5)

	defaults := struct{ sd, rr float64 }{config.S6_VWAP_SD, config.S6_VWAP_RR}

	var combos []comboEntry
	for _, sd := range sdVals {
		for _, rr := range rrVals {
			s, r := sd, rr
			combos = append(combos, comboEntry{
				params: []paramSnapshot{
					{"S6_VWAP_SD", fmt.Sprintf("%.1f", s)},
					{"S6_VWAP_RR", fmt.Sprintf("%.1f", r)},
				},
				apply:   func() { config.S6_VWAP_SD = s; config.S6_VWAP_RR = r },
				restore: func() { config.S6_VWAP_SD = defaults.sd; config.S6_VWAP_RR = defaults.rr },
			})
		}
	}
	return grid{paramNames: []string{"S6_VWAP_SD", "S6_VWAP_RR"}, combos: combos}
}

// ═══ S7: MEAN REV LONG ═══
func buildS7Grid() grid {
	rsiOSVals := intRange(18, 38, 5)
	rvolVals := floatRange(0.8, 2.5, 0.5)
	vwapDevVals := floatRange(0.010, 0.035, 0.005)

	defaults := struct{ rsiOS int; rvol, vwap float64 }{config.S7_RSI_OVERSOLD, config.S7_RVOL_MIN, config.S7_VWAP_DEVIATION_PCT}

	var combos []comboEntry
	for _, rsi := range rsiOSVals {
		for _, rv := range rvolVals {
			for _, vw := range vwapDevVals {
				r, v, w := rsi, rv, vw
				combos = append(combos, comboEntry{
					params: []paramSnapshot{
						{"S7_RSI_OVERSOLD", fmt.Sprintf("%d", r)},
						{"S7_RVOL_MIN", fmt.Sprintf("%.1f", v)},
						{"S7_VWAP_DEVIATION_PCT", fmt.Sprintf("%.3f", w)},
					},
					apply: func() {
						config.S7_RSI_OVERSOLD = r
						config.S7_RVOL_MIN = v
						config.S7_VWAP_DEVIATION_PCT = w
					},
					restore: func() {
						config.S7_RSI_OVERSOLD = defaults.rsiOS
						config.S7_RVOL_MIN = defaults.rvol
						config.S7_VWAP_DEVIATION_PCT = defaults.vwap
					},
				})
			}
		}
	}
	return grid{paramNames: []string{"S7_RSI_OVERSOLD", "S7_RVOL_MIN", "S7_VWAP_DEVIATION_PCT"}, combos: combos}
}

// ═══ S8: VOL PIVOT ═══
func buildS8Grid() grid {
	spikeVals := floatRange(1.5, 4.0, 0.5)

	defaults := config.S8_VOL_SPIKE_MULT

	var combos []comboEntry
	for _, sp := range spikeVals {
		s := sp
		combos = append(combos, comboEntry{
			params: []paramSnapshot{{"S8_VOL_SPIKE_MULT", fmt.Sprintf("%.1f", s)}},
			apply:   func() { config.S8_VOL_SPIKE_MULT = s },
			restore: func() { config.S8_VOL_SPIKE_MULT = defaults },
		})
	}
	return grid{paramNames: []string{"S8_VOL_SPIKE_MULT"}, combos: combos}
}

// ═══ S9: MTF MOMENTUM ═══
func buildS9Grid() grid {
	rsiThreshVals := intRange(30, 60, 5)
	atrVals := floatRange(1.0, 3.0, 0.5)
	rrVals := floatRange(1.5, 4.5, 1.0)

	defaults := struct{ rsiT int; atr, rr float64 }{config.S9_RSI_THRESHOLD, config.S9_ATR_SL_MULT, config.S9_RR}

	var combos []comboEntry
	for _, rsi := range rsiThreshVals {
		for _, atr := range atrVals {
			for _, rr := range rrVals {
				r, a, rv := rsi, atr, rr
				combos = append(combos, comboEntry{
					params: []paramSnapshot{
						{"S9_RSI_THRESHOLD", fmt.Sprintf("%d", r)},
						{"S9_ATR_SL_MULT", fmt.Sprintf("%.1f", a)},
						{"S9_RR", fmt.Sprintf("%.1f", rv)},
					},
					apply: func() {
						config.S9_RSI_THRESHOLD = r
						config.S9_ATR_SL_MULT = a
						config.S9_RR = rv
					},
					restore: func() {
						config.S9_RSI_THRESHOLD = defaults.rsiT
						config.S9_ATR_SL_MULT = defaults.atr
						config.S9_RR = defaults.rr
					},
				})
			}
		}
	}
	return grid{paramNames: []string{"S9_RSI_THRESHOLD", "S9_ATR_SL_MULT", "S9_RR"}, combos: combos}
}

// ═══ S14: RSI SCALP ═══
func buildS14Grid() grid {
	rsiOSVals := intRange(5, 20, 5)
	rsiOBVals := intRange(80, 95, 5)
	stopVals := floatRange(0.003, 0.008, 0.001)

	defaults := struct{ rsiOS, rsiOB int; stop float64 }{config.S14_RSI_OVERSOLD, config.S14_RSI_OVERBOUGHT, config.S14_STOP_PCT}

	var combos []comboEntry
	for _, rsiOS := range rsiOSVals {
		for _, rsiOB := range rsiOBVals {
			for _, stop := range stopVals {
				o, b, s := rsiOS, rsiOB, stop
				combos = append(combos, comboEntry{
					params: []paramSnapshot{
						{"S14_RSI_OVERSOLD", fmt.Sprintf("%d", o)},
						{"S14_RSI_OVERBOUGHT", fmt.Sprintf("%d", b)},
						{"S14_STOP_PCT", fmt.Sprintf("%.3f", s)},
					},
					apply: func() {
						config.S14_RSI_OVERSOLD = o
						config.S14_RSI_OVERBOUGHT = b
						config.S14_STOP_PCT = s
					},
					restore: func() {
						config.S14_RSI_OVERSOLD = defaults.rsiOS
						config.S14_RSI_OVERBOUGHT = defaults.rsiOB
						config.S14_STOP_PCT = defaults.stop
					},
				})
			}
		}
	}
	return grid{paramNames: []string{"S14_RSI_OVERSOLD", "S14_RSI_OVERBOUGHT", "S14_STOP_PCT"}, combos: combos}
}

// ═══ S15: RSI SWING ═══
func buildS15Grid() grid {
	rsiOSVals := intRange(25, 45, 5)
	atrVals := floatRange(0.5, 2.0, 0.5)
	rrVals := floatRange(1.0, 3.0, 0.5)

	defaults := struct{ rsiOS int; atr, rr float64 }{config.S15_RSI_OVERSOLD, config.S15_ATR_SL_MULT, config.S15_RR}

	var combos []comboEntry
	for _, rsi := range rsiOSVals {
		for _, atr := range atrVals {
			for _, rr := range rrVals {
				r, a, rv := rsi, atr, rr
				combos = append(combos, comboEntry{
					params: []paramSnapshot{
						{"S15_RSI_OVERSOLD", fmt.Sprintf("%d", r)},
						{"S15_ATR_SL_MULT", fmt.Sprintf("%.1f", a)},
						{"S15_RR", fmt.Sprintf("%.1f", rv)},
					},
					apply: func() {
						config.S15_RSI_OVERSOLD = r
						config.S15_ATR_SL_MULT = a
						config.S15_RR = rv
					},
					restore: func() {
						config.S15_RSI_OVERSOLD = defaults.rsiOS
						config.S15_ATR_SL_MULT = defaults.atr
						config.S15_RR = defaults.rr
					},
				})
			}
		}
	}
	return grid{paramNames: []string{"S15_RSI_OVERSOLD", "S15_ATR_SL_MULT", "S15_RR"}, combos: combos}
}
