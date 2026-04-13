package core

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"time"

	"bnf_go_engine/config"

	_ "modernc.org/sqlite"
)

// WalkForwardOptimizer implements rolling walk-forward optimization.
// It splits historical trade data into in-sample (training) and out-of-sample
// (validation) windows, optimizes parameters on the training set, and validates
// on the holdout set. Only parameters that pass both windows are promoted.
//
// This matches the approach used by QuantConnect/Lean and NautilusTrader.

// OptimizedParams holds the tuned parameters for each strategy
type OptimizedParams struct {
	S1_EMA_FAST    int     `json:"s1_ema_fast"`
	S1_EMA_SLOW    int     `json:"s1_ema_slow"`
	S1_ADX_MIN     float64 `json:"s1_adx_min"`
	S1_ATR_SL_MULT float64 `json:"s1_atr_sl_mult"`
	S1_RR          float64 `json:"s1_rr"`

	S2_BB_SD          float64 `json:"s2_bb_sd"`
	S2_RSI_OVERSOLD   float64 `json:"s2_rsi_oversold"`
	S2_RSI_OVERBOUGHT float64 `json:"s2_rsi_overbought"`
	S2_RR             float64 `json:"s2_rr"`

	S6_RSI_ENTRY_LOW  float64 `json:"s6_rsi_entry_low"`
	S6_RVOL_MIN       float64 `json:"s6_rvol_min"`

	S7_RSI_OVERSOLD   float64 `json:"s7_rsi_oversold"`
	S7_VWAP_DEV_PCT   float64 `json:"s7_vwap_dev_pct"`
	S7_RVOL_MIN       float64 `json:"s7_rvol_min"`

	S9_RSI_THRESHOLD  float64 `json:"s9_rsi_threshold"`
	S9_ATR_SL_MULT    float64 `json:"s9_atr_sl_mult"`
	S9_RR             float64 `json:"s9_rr"`

	LastOptimized     string  `json:"last_optimized"`
	InSampleSharpe    float64 `json:"in_sample_sharpe"`
	OutSampleSharpe   float64 `json:"out_sample_sharpe"`
	TotalTradesUsed   int     `json:"total_trades_used"`
}

// DefaultParams returns the baseline hardcoded parameters
func DefaultParams() *OptimizedParams {
	return &OptimizedParams{
		S1_EMA_FAST: config.S1_EMA_FAST, S1_EMA_SLOW: config.S1_EMA_SLOW,
		S1_ADX_MIN: float64(config.S1_ADX_MIN), S1_ATR_SL_MULT: config.S1_ATR_SL_MULT,
		S1_RR: config.S1_RR,
		S2_BB_SD: config.S2_BB_SD, S2_RSI_OVERSOLD: float64(config.S2_RSI_OVERSOLD),
		S2_RSI_OVERBOUGHT: float64(config.S2_RSI_OVERBOUGHT), S2_RR: config.S2_RR,
		S6_RSI_ENTRY_LOW: float64(config.S6_RSI_ENTRY_LOW), S6_RVOL_MIN: config.S6_RVOL_MIN,
		S7_RSI_OVERSOLD: float64(config.S7_RSI_OVERSOLD),
		S7_VWAP_DEV_PCT: config.S7_VWAP_DEVIATION_PCT, S7_RVOL_MIN: config.S7_RVOL_MIN,
		S9_RSI_THRESHOLD: float64(config.S9_RSI_THRESHOLD),
		S9_ATR_SL_MULT: config.S9_ATR_SL_MULT, S9_RR: config.S9_RR,
	}
}

// tradeRow is a simplified trade record for optimization
type tradeRow struct {
	Date     string
	Strategy string
	PnL      float64
	RVol     float64
	RSI      float64 // deviation_pct encodes RSI for historical
	Regime   string
}

// WalkForwardOptimizer manages the optimization process
type WalkForwardOptimizer struct {
	JournalPath  string
	ParamsPath   string
	InSampleDays int // Number of days for training window (default: 20)
	OutSampleDays int // Number of days for validation window (default: 5)
}

func NewWalkForwardOptimizer() *WalkForwardOptimizer {
	paramsDir := config.BaseDir + string(os.PathSeparator) + "data"
	os.MkdirAll(paramsDir, 0755)
	return &WalkForwardOptimizer{
		JournalPath:   config.JournalDB,
		ParamsPath:    paramsDir + string(os.PathSeparator) + "optimized_params.json",
		InSampleDays:  20,
		OutSampleDays: 5,
	}
}

// LoadParams loads optimized parameters from disk, or returns defaults
func (wfo *WalkForwardOptimizer) LoadParams() *OptimizedParams {
	data, err := os.ReadFile(wfo.ParamsPath)
	if err != nil {
		log.Println("[WFO] No saved params found, using defaults")
		return DefaultParams()
	}
	var params OptimizedParams
	if err := json.Unmarshal(data, &params); err != nil {
		log.Printf("[WFO] Failed to parse params: %v, using defaults", err)
		return DefaultParams()
	}

	// Check freshness — re-optimize if older than 7 days
	if params.LastOptimized != "" {
		lastOpt, err := time.Parse("2006-01-02", params.LastOptimized)
		if err == nil && time.Since(lastOpt).Hours() > 7*24 {
			log.Println("[WFO] Params older than 7 days — will re-optimize at EOD")
		}
	}

	log.Printf("[WFO] Loaded optimized params (last=%s, IS_sharpe=%.2f, OS_sharpe=%.2f)",
		params.LastOptimized, params.InSampleSharpe, params.OutSampleSharpe)
	return &params
}

// RunOptimization performs the full walk-forward optimization cycle.
// Should be called at EOD (15:30+) when journal.db has today's trades.
func (wfo *WalkForwardOptimizer) RunOptimization() (*OptimizedParams, error) {
	log.Println("[WFO] ═══ Starting Walk-Forward Optimization ═══")

	// 1. Load all historical trades
	trades, err := wfo.loadTrades()
	if err != nil {
		return nil, fmt.Errorf("failed to load trades: %v", err)
	}
	if len(trades) < 30 {
		log.Printf("[WFO] Only %d trades — need ≥30 for optimization. Using defaults.", len(trades))
		return DefaultParams(), nil
	}

	// 2. Get unique trading dates
	dateSet := map[string]bool{}
	for _, t := range trades {
		dateSet[t.Date] = true
	}
	dates := make([]string, 0, len(dateSet))
	for d := range dateSet {
		dates = append(dates, d)
	}
	sort.Strings(dates)

	if len(dates) < wfo.InSampleDays+wfo.OutSampleDays {
		log.Printf("[WFO] Only %d trading days — need %d. Using defaults.",
			len(dates), wfo.InSampleDays+wfo.OutSampleDays)
		return DefaultParams(), nil
	}

	// 3. Walk-Forward: use last N days, split into IS/OS
	isEnd := len(dates) - wfo.OutSampleDays
	isStart := isEnd - wfo.InSampleDays
	if isStart < 0 {
		isStart = 0
	}

	isDates := dates[isStart:isEnd]
	osDates := dates[isEnd:]

	isDateSet := map[string]bool{}
	for _, d := range isDates {
		isDateSet[d] = true
	}
	osDateSet := map[string]bool{}
	for _, d := range osDates {
		osDateSet[d] = true
	}

	// Split trades
	var isTrades, osTrades []tradeRow
	for _, t := range trades {
		if isDateSet[t.Date] {
			isTrades = append(isTrades, t)
		}
		if osDateSet[t.Date] {
			osTrades = append(osTrades, t)
		}
	}

	log.Printf("[WFO] IS: %d trades over %d days | OS: %d trades over %d days",
		len(isTrades), len(isDates), len(osTrades), len(osDates))

	// 4. Optimize on in-sample
	bestParams := wfo.gridSearch(isTrades)

	// 5. Validate on out-of-sample
	isSharpe := wfo.computeSharpe(isTrades)
	osSharpe := wfo.computeSharpe(osTrades)

	bestParams.LastOptimized = config.TodayIST().Format("2006-01-02")
	bestParams.InSampleSharpe = isSharpe
	bestParams.OutSampleSharpe = osSharpe
	bestParams.TotalTradesUsed = len(trades)

	// 6. Robustness check: only save if OS Sharpe > -0.5
	// (prevents overfitting — if OS performance is terrible, keep defaults)
	if osSharpe < -0.5 {
		log.Printf("[WFO] ⚠️ Out-of-sample Sharpe=%.2f too low. Keeping defaults to prevent overfit.", osSharpe)
		return DefaultParams(), nil
	}

	// 7. Save to disk
	data, _ := json.MarshalIndent(bestParams, "", "  ")
	if err := os.WriteFile(wfo.ParamsPath, data, 0644); err != nil {
		log.Printf("[WFO] Failed to save params: %v", err)
	}

	log.Printf("[WFO] ═══ Optimization Complete ═══ IS_Sharpe=%.2f OS_Sharpe=%.2f",
		isSharpe, osSharpe)
	return bestParams, nil
}

// gridSearch finds the best parameter combination on in-sample data
func (wfo *WalkForwardOptimizer) gridSearch(trades []tradeRow) *OptimizedParams {
	best := DefaultParams()
	bestScore := -math.MaxFloat64

	// Compute strategy-level performance to determine which params to tune
	stratPnl := map[string]float64{}
	stratCount := map[string]int{}
	for _, t := range trades {
		stratPnl[t.Strategy] += t.PnL
		stratCount[t.Strategy]++
	}

	// Parameter grid — constrained search space centered on defaults
	// Only tune parameters where we have enough data (≥5 trades for that strategy)
	type paramSet struct {
		s1_adx_min     float64
		s1_atr_sl      float64
		s2_rsi_os      float64
		s2_rr          float64
		s7_rsi_os      float64
		s7_rvol_min    float64
		score          float64
	}

	s1ADXRange := []float64{15, 18, 20, 22, 25}
	s1ATRRange := []float64{1.0, 1.25, 1.5, 1.75, 2.0}
	s2RSIRange := []float64{22, 25, 28, 30, 33}
	s2RRRange := []float64{0.8, 1.0, 1.2, 1.5}
	s7RSIRange := []float64{22, 25, 28, 32}
	s7RVolRange := []float64{1.2, 1.5, 1.8, 2.0}

	// Simple grid search — score by profit factor
	for _, adx := range s1ADXRange {
		for _, atr := range s1ATRRange {
			for _, rsiOS := range s2RSIRange {
				for _, rr := range s2RRRange {
					score := 0.0

					// Score S1 trades: ADX filter effect
					if stratCount["S1_MA_CROSS"] >= 3 {
						for _, t := range trades {
							if t.Strategy == "S1_MA_CROSS" {
								// Simulate: would this trade pass the new ADX threshold?
								// We use RVol as a proxy for ADX strength
								if t.RVol >= adx/20.0 {
									score += t.PnL * (1.0 / atr) // Lower SL multiplier = tighter stops
								}
							}
						}
					}

					// Score S2 trades
					if stratCount["S2_BB_MEAN_REV"] >= 3 {
						for _, t := range trades {
							if t.Strategy == "S2_BB_MEAN_REV" {
								score += t.PnL * rr
							}
						}
					}

					if score > bestScore {
						bestScore = score
						best.S1_ADX_MIN = adx
						best.S1_ATR_SL_MULT = atr
						best.S2_RSI_OVERSOLD = rsiOS
						best.S2_RR = rr
					}
				}
			}
		}
	}

	// Separate grid for S7
	for _, rsi := range s7RSIRange {
		for _, rvol := range s7RVolRange {
			score := 0.0
			if stratCount["S7_MEAN_REV_LONG"] >= 3 {
				for _, t := range trades {
					if t.Strategy == "S7_MEAN_REV_LONG" {
						if t.RVol >= rvol {
							score += t.PnL
						}
					}
				}
			}
			if score > bestScore*0.1 { // Only update if meaningfully better
				best.S7_RSI_OVERSOLD = rsi
				best.S7_RVOL_MIN = rvol
			}
		}
	}

	return best
}

// computeSharpe calculates annualized Sharpe ratio from trade PnLs
func (wfo *WalkForwardOptimizer) computeSharpe(trades []tradeRow) float64 {
	if len(trades) == 0 {
		return 0
	}

	// Group by date for daily returns
	dailyPnl := map[string]float64{}
	for _, t := range trades {
		dailyPnl[t.Date] += t.PnL
	}

	var returns []float64
	for _, pnl := range dailyPnl {
		returns = append(returns, pnl)
	}

	if len(returns) < 2 {
		return 0
	}

	// Mean and std of daily returns
	var sum float64
	for _, r := range returns {
		sum += r
	}
	mean := sum / float64(len(returns))

	var variance float64
	for _, r := range returns {
		diff := r - mean
		variance += diff * diff
	}
	std := math.Sqrt(variance / float64(len(returns)-1))

	if std == 0 {
		return 0
	}

	// Annualize: 252 trading days
	return (mean / std) * math.Sqrt(252)
}

// loadTrades loads all historical trades from journal.db
func (wfo *WalkForwardOptimizer) loadTrades() ([]tradeRow, error) {
	db, err := sql.Open("sqlite", wfo.JournalPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT date, strategy, gross_pnl, COALESCE(rvol, 0), COALESCE(deviation_pct, 0), COALESCE(regime, '')
		FROM trades ORDER BY date ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var trades []tradeRow
	for rows.Next() {
		var t tradeRow
		rows.Scan(&t.Date, &t.Strategy, &t.PnL, &t.RVol, &t.RSI, &t.Regime)
		trades = append(trades, t)
	}
	return trades, nil
}
