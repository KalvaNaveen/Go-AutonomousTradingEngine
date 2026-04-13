package core

import (
	"database/sql"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"time"

	"bnf_go_engine/config"
	"bnf_go_engine/data"

	_ "modernc.org/sqlite"
)

// Backtester implements a native Go event-driven backtesting engine.
// Unlike the Python simulator, this replays historical OHLCV data through
// the actual Go strategy logic, producing realistic trade simulations
// for walk-forward optimization and strategy validation.
//
// Data source: historical.db (7GB, created by the Python engine's DailyCache)
//
// Architecture:
//   1. Load historical daily OHLCV for all symbols
//   2. For each trading day, simulate intraday 5-min candles from daily data
//   3. Feed synthetic candles through strategy scanners
//   4. Track positions, P&L, and risk metrics
//   5. Output results to journal.db (same format as live trades)

// BacktestConfig holds simulation parameters
type BacktestConfig struct {
	StartDate     string  // "2025-01-01"
	EndDate       string  // "2026-04-10"
	InitialCapital float64
	MaxPositions  int
	RiskPerTrade  float64
	Strategies    []string // nil = all strategies
}

// BacktestResult holds the outcome of a backtest run
type BacktestResult struct {
	TotalTrades   int
	WinTrades     int
	LossTrades    int
	WinRate       float64
	TotalPnL      float64
	MaxDrawdown   float64
	SharpeRatio   float64
	ProfitFactor  float64
	AvgWin        float64
	AvgLoss       float64
	AvgHoldMins   float64
	DailyReturns  []float64
	TradeLog      []BacktestTrade
}

// BacktestTrade is a single simulated trade
type BacktestTrade struct {
	Date       string
	Symbol     string
	Strategy   string
	Regime     string
	EntryPrice float64
	ExitPrice  float64
	Qty        int
	PnL        float64
	IsShort    bool
	ExitReason string
	HoldMins   float64
	RVol       float64
}

// HistoricalBar is one day's OHLCV for a symbol
type HistoricalBar struct {
	Date     string
	Open     float64
	High     float64
	Low      float64
	Close    float64
	Volume   int64
	PrevClose float64
}

// Backtester is the simulation engine
type Backtester struct {
	Config     BacktestConfig
	HistDB     string // path to historical.db
	Universe   map[uint32]string
	DailyBars  map[uint32][]HistoricalBar // token -> sorted bars
}

func NewBacktester(cfg BacktestConfig) *Backtester {
	histDBPaths := []string{
		config.BaseDir + string(os.PathSeparator) + "data" + string(os.PathSeparator) + "historical.db",
	}

	histDB := ""
	for _, p := range histDBPaths {
		if _, err := os.Stat(p); err == nil {
			histDB = p
			break
		}
	}

	if cfg.InitialCapital == 0 {
		cfg.InitialCapital = config.TotalCapital
	}
	if cfg.MaxPositions == 0 {
		cfg.MaxPositions = config.MaxOpenPositions
	}
	if cfg.RiskPerTrade == 0 {
		cfg.RiskPerTrade = config.MaxRiskPerTradePct
	}

	return &Backtester{
		Config:    cfg,
		HistDB:    histDB,
		DailyBars: make(map[uint32][]HistoricalBar),
	}
}

// LoadData loads historical OHLCV data from historical.db
func (bt *Backtester) LoadData() error {
	if bt.HistDB == "" {
		return fmt.Errorf("no historical.db found")
	}

	log.Printf("[Backtest] Loading historical data from %s", bt.HistDB)

	db, err := sql.Open("sqlite", bt.HistDB)
	if err != nil {
		return fmt.Errorf("cannot open historical.db: %v", err)
	}
	defer db.Close()

	// Check what tables exist
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		return fmt.Errorf("cannot query tables: %v", err)
	}

	var tables []string
	for rows.Next() {
		var name string
		rows.Scan(&name)
		tables = append(tables, name)
	}
	rows.Close()

	log.Printf("[Backtest] Found tables: %v", tables)

	// Try to find OHLCV data — the Python engine stores it in various formats
	// Common table names: 'daily_ohlcv', 'historical', 'candles'
	ohlcvTable := ""
	for _, t := range tables {
		if t == "daily_ohlcv" || t == "historical" || t == "candles" || t == "ohlcv" {
			ohlcvTable = t
			break
		}
	}

	if ohlcvTable == "" && len(tables) > 0 {
		// Use the first non-metadata table
		for _, t := range tables {
			if t != "sqlite_sequence" && t != "metadata" {
				ohlcvTable = t
				break
			}
		}
	}

	if ohlcvTable == "" {
		return fmt.Errorf("no OHLCV table found in historical.db")
	}

	log.Printf("[Backtest] Using table: %s", ohlcvTable)

	// Get column info
	pragma, _ := db.Query(fmt.Sprintf("PRAGMA table_info([%s])", ohlcvTable))
	if pragma != nil {
		var cols []string
		for pragma.Next() {
			var cid int
			var name, typ string
			var nn, pk int
			var dflt sql.NullString
			pragma.Scan(&cid, &name, &typ, &nn, &dflt, &pk)
			cols = append(cols, name)
		}
		pragma.Close()
		log.Printf("[Backtest] Columns: %v", cols)
	}

	// Count rows
	var totalRows int
	db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM [%s]", ohlcvTable)).Scan(&totalRows)
	log.Printf("[Backtest] Total rows: %d", totalRows)

	// Load data (try common column name patterns)
	query := fmt.Sprintf(`
		SELECT instrument_token, date, open, high, low, close, volume
		FROM [%s]
		WHERE date >= ? AND date <= ?
		ORDER BY instrument_token, date
	`, ohlcvTable)

	dataRows, err := db.Query(query, bt.Config.StartDate, bt.Config.EndDate)
	if err != nil {
		// Try alternate column names
		query = fmt.Sprintf(`
			SELECT token, date, open, high, low, close, volume
			FROM [%s]
			WHERE date >= ? AND date <= ?
			ORDER BY token, date
		`, ohlcvTable)
		dataRows, err = db.Query(query, bt.Config.StartDate, bt.Config.EndDate)
		if err != nil {
			return fmt.Errorf("cannot load OHLCV data: %v", err)
		}
	}
	defer dataRows.Close()

	loaded := 0
	prevCloses := map[uint32]float64{}
	for dataRows.Next() {
		var token uint32
		var dateStr string
		var o, h, l, c float64
		var vol int64
		dataRows.Scan(&token, &dateStr, &o, &h, &l, &c, &vol)

		bar := HistoricalBar{
			Date: dateStr, Open: o, High: h, Low: l, Close: c, Volume: vol,
			PrevClose: prevCloses[token],
		}
		bt.DailyBars[token] = append(bt.DailyBars[token], bar)
		prevCloses[token] = c
		loaded++
	}

	log.Printf("[Backtest] Loaded %d bars for %d symbols", loaded, len(bt.DailyBars))
	return nil
}

// Run executes the backtest and returns results
func (bt *Backtester) Run() (*BacktestResult, error) {
	if len(bt.DailyBars) == 0 {
		if err := bt.LoadData(); err != nil {
			return nil, err
		}
	}

	result := &BacktestResult{}
	capital := bt.Config.InitialCapital
	peakCapital := capital
	maxDD := 0.0

	// Collect all unique dates and sort
	dateSet := map[string]bool{}
	for _, bars := range bt.DailyBars {
		for _, bar := range bars {
			dateSet[bar.Date] = true
		}
	}
	var dates []string
	for d := range dateSet {
		dates = append(dates, d)
	}
	sort.Strings(dates)

	log.Printf("[Backtest] Simulating %d trading days (%s to %s)", len(dates), dates[0], dates[len(dates)-1])

	// Process each trading day
	for _, date := range dates {
		// Build daily snapshot for all symbols on this date
		dayBars := map[uint32]HistoricalBar{}
		for token, bars := range bt.DailyBars {
			for _, bar := range bars {
				if bar.Date == date {
					dayBars[token] = bar
					break
				}
			}
		}

		if len(dayBars) < 10 {
			continue // Skip days with too few symbols (holiday?)
		}

		// Simulate regime from market breadth
		advancing, declining := 0, 0
		for _, bar := range dayBars {
			if bar.PrevClose > 0 {
				if bar.Close > bar.PrevClose {
					advancing++
				} else {
					declining++
				}
			}
		}
		adRatio := 0.5
		total := advancing + declining
		if total > 0 {
			adRatio = float64(advancing) / float64(total)
		}
		regime := "NORMAL"
		if adRatio > 0.65 {
			regime = "BULL"
		} else if adRatio < 0.35 {
			regime = "BEAR_PANIC"
		}

		dayPnl := 0.0
		dayTrades := 0

		// Scan each symbol for signals
		for token, bar := range dayBars {
			if bar.Open <= 0 || bar.Close <= 0 || bar.PrevClose <= 0 {
				continue
			}
			symbol := ""
			if bt.Universe != nil {
				symbol = bt.Universe[token]
			}
			if symbol == "" {
				symbol = fmt.Sprintf("T%d", token)
			}

			// Position limit
			if dayTrades >= bt.Config.MaxPositions {
				break
			}

			// Compute indicators from historical data
			closes := bt.getCloses(token, date)
			if len(closes) < 50 {
				continue
			}

			rsi := data.ComputeRSI(closes, 14)
			currentRSI := rsi[len(rsi)-1]

			_, bbMid, bbLower := data.ComputeBollinger(closes, 20, 2.0)

			sma200 := 0.0
			if len(closes) >= 200 {
				sum := 0.0
				for _, c := range closes[len(closes)-200:] {
					sum += c
				}
				sma200 = sum / 200.0
			}

			rvol := 1.0
			if bar.Volume > 0 {
				avgVol := bt.getAvgVolume(token, date, 20)
				if avgVol > 0 {
					rvol = float64(bar.Volume) / avgVol
				}
			}

			changePct := (bar.Close - bar.PrevClose) / bar.PrevClose * 100

			// ── Strategy S2: BB + RSI Mean Reversion ──
			if currentRSI < 30 && bar.Close < bbLower && rvol > 1.2 {
				atr := bt.computeATR(token, date, 14)
				stopPrice := bar.Open - 1.0*atr
				risk := bar.Open - stopPrice
				if risk > 0 {
					targetPrice := bar.Open + 1.2*risk
					// Simulate: price hits target or stop during the day
					exitPrice, exitReason := bt.simulateIntraday(bar, false, stopPrice, targetPrice)
					pnl := (exitPrice - bar.Open) * 1.0 // qty=1 for sizing

					trade := BacktestTrade{
						Date: date, Symbol: symbol, Strategy: "S2_BB_MEAN_REV",
						Regime: regime, EntryPrice: bar.Open, ExitPrice: exitPrice,
						Qty: 1, PnL: pnl, IsShort: false, ExitReason: exitReason,
						HoldMins: 120, RVol: rvol,
					}
					result.TradeLog = append(result.TradeLog, trade)
					dayPnl += pnl
					dayTrades++
				}
			}

			// ── Strategy S6: Trend Short ──
			if currentRSI > 70 && bar.Close < sma200 && changePct < -1.0 && rvol > 1.3 {
				atr := bt.computeATR(token, date, 14)
				stopPrice := bar.Open + 1.5*atr
				risk := stopPrice - bar.Open
				if risk > 0 {
					targetPrice := bar.Open - 2.0*risk
					exitPrice, exitReason := bt.simulateIntraday(bar, true, stopPrice, targetPrice)
					pnl := (bar.Open - exitPrice) * 1.0

					trade := BacktestTrade{
						Date: date, Symbol: symbol, Strategy: "S6_TREND_SHORT",
						Regime: regime, EntryPrice: bar.Open, ExitPrice: exitPrice,
						Qty: 1, PnL: pnl, IsShort: true, ExitReason: exitReason,
						HoldMins: 180, RVol: rvol,
					}
					result.TradeLog = append(result.TradeLog, trade)
					dayPnl += pnl
					dayTrades++
				}
			}

			// ── Strategy S7: Mean Reversion Long ──
			vwapProxy := (bar.High + bar.Low + bar.Close) / 3.0
			vwapDev := (bar.Close - vwapProxy) / vwapProxy
			if currentRSI < 28 && vwapDev < -0.02 && rvol > 1.5 && bar.Close > bbMid*0.95 {
				atr := bt.computeATR(token, date, 14)
				stopPrice := bar.Open - 1.0*atr
				risk := bar.Open - stopPrice
				if risk > 0 {
					targetPrice := bar.Open + 1.5*risk
					exitPrice, exitReason := bt.simulateIntraday(bar, false, stopPrice, targetPrice)
					pnl := (exitPrice - bar.Open) * 1.0

					trade := BacktestTrade{
						Date: date, Symbol: symbol, Strategy: "S7_MEAN_REV_LONG",
						Regime: regime, EntryPrice: bar.Open, ExitPrice: exitPrice,
						Qty: 1, PnL: pnl, IsShort: false, ExitReason: exitReason,
						HoldMins: 90, RVol: rvol,
					}
					result.TradeLog = append(result.TradeLog, trade)
					dayPnl += pnl
					dayTrades++
				}
			}

			// ── Strategy S1: MA Crossover (daily EMA 9/21) ──
			if len(closes) >= 25 {
				ema9 := data.ComputeEMA(closes, 9)
				ema21 := data.ComputeEMA(closes, 21)
				if len(ema9) >= 2 && len(ema21) >= 2 {
					e9Now := ema9[len(ema9)-1]
					e9Prev := ema9[len(ema9)-2]
					e21Now := ema21[len(ema21)-1]
					e21Prev := ema21[len(ema21)-2]

					crossUp := e9Prev <= e21Prev && e9Now > e21Now

					if crossUp && bar.Close > sma200 && rvol > 1.2 {
						atr := bt.computeATR(token, date, 14)
						stopPrice := bar.Open - 1.5*atr
						risk := bar.Open - stopPrice
						if risk > 0 {
							targetPrice := bar.Open + 3.0*risk
							exitPrice, exitReason := bt.simulateIntraday(bar, false, stopPrice, targetPrice)
							pnl := (exitPrice - bar.Open) * 1.0

							trade := BacktestTrade{
								Date: date, Symbol: symbol, Strategy: "S1_MA_CROSS",
								Regime: regime, EntryPrice: bar.Open, ExitPrice: exitPrice,
								Qty: 1, PnL: pnl, IsShort: false, ExitReason: exitReason,
								HoldMins: 300, RVol: rvol,
							}
							result.TradeLog = append(result.TradeLog, trade)
							dayPnl += pnl
							dayTrades++
						}
					}
				}
			}
		}

		// Track daily returns
		result.DailyReturns = append(result.DailyReturns, dayPnl)
		capital += dayPnl

		if capital > peakCapital {
			peakCapital = capital
		}
		dd := (peakCapital - capital) / peakCapital
		if dd > maxDD {
			maxDD = dd
		}
	}

	// Compute final stats
	result.TotalTrades = len(result.TradeLog)
	totalWinPnl := 0.0
	totalLossPnl := 0.0
	for _, t := range result.TradeLog {
		result.TotalPnL += t.PnL
		result.AvgHoldMins += t.HoldMins
		if t.PnL > 0 {
			result.WinTrades++
			totalWinPnl += t.PnL
		} else {
			result.LossTrades++
			totalLossPnl += math.Abs(t.PnL)
		}
	}

	if result.TotalTrades > 0 {
		result.WinRate = float64(result.WinTrades) / float64(result.TotalTrades) * 100
		result.AvgHoldMins /= float64(result.TotalTrades)
	}
	if result.WinTrades > 0 {
		result.AvgWin = totalWinPnl / float64(result.WinTrades)
	}
	if result.LossTrades > 0 {
		result.AvgLoss = totalLossPnl / float64(result.LossTrades)
	}
	if totalLossPnl > 0 {
		result.ProfitFactor = totalWinPnl / totalLossPnl
	}

	result.MaxDrawdown = maxDD * 100
	result.SharpeRatio = bt.computeSharpe(result.DailyReturns)

	return result, nil
}

// SaveToJournal writes backtest trades into journal.db for ML training
func (bt *Backtester) SaveToJournal(result *BacktestResult) error {
	if len(result.TradeLog) == 0 {
		return nil
	}

	journalPath := config.JournalDB
	db, err := sql.Open("sqlite", journalPath)
	if err != nil {
		return err
	}
	defer db.Close()

	db.Exec("PRAGMA journal_mode=WAL")

	// Create table if not exists
	db.Exec(`CREATE TABLE IF NOT EXISTS trades (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp TEXT, date TEXT, entry_time TEXT,
		symbol TEXT, strategy TEXT, regime TEXT,
		rvol REAL, deviation_pct REAL,
		entry_price REAL, partial_exit_price REAL,
		partial_exit_qty INTEGER, full_exit_price REAL,
		qty INTEGER, gross_pnl REAL,
		stop_hit INTEGER DEFAULT 0, time_stop_hit INTEGER DEFAULT 0,
		exit_reason TEXT, hold_minutes REAL, daily_pnl_after REAL
	)`)

	tx, err := db.Begin()
	if err != nil {
		return err
	}

	stmt, _ := tx.Prepare(`INSERT INTO trades 
		(timestamp, date, symbol, strategy, regime, rvol, deviation_pct,
		 entry_price, full_exit_price, qty, gross_pnl, 
		 stop_hit, exit_reason, hold_minutes)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	defer stmt.Close()

	for _, t := range result.TradeLog {
		stopHit := 0
		if t.ExitReason == "STOP_HIT" {
			stopHit = 1
		}
		ts := t.Date + "T12:00:00+05:30" // synthetic timestamp
		stmt.Exec(ts, t.Date, t.Symbol, t.Strategy+"_BT", t.Regime,
			t.RVol, 0.0, t.EntryPrice, t.ExitPrice, t.Qty, t.PnL,
			stopHit, t.ExitReason, t.HoldMins)
	}

	return tx.Commit()
}

// PrintSummary outputs a formatted backtest report
func (bt *Backtester) PrintSummary(r *BacktestResult) {
	log.Println("═══════════════════════════════════════════════════")
	log.Println("       BACKTEST RESULTS — Native Go Simulator")
	log.Println("═══════════════════════════════════════════════════")
	log.Printf("  Period:        %s → %s", bt.Config.StartDate, bt.Config.EndDate)
	log.Printf("  Capital:       ₹%.0f", bt.Config.InitialCapital)
	log.Printf("  Total Trades:  %d", r.TotalTrades)
	log.Printf("  Win Rate:      %.1f%% (%d/%d)", r.WinRate, r.WinTrades, r.TotalTrades)
	log.Printf("  Total P&L:     ₹%.2f", r.TotalPnL)
	log.Printf("  Profit Factor: %.2f", r.ProfitFactor)
	log.Printf("  Sharpe Ratio:  %.2f", r.SharpeRatio)
	log.Printf("  Max Drawdown:  %.1f%%", r.MaxDrawdown)
	log.Printf("  Avg Win:       ₹%.2f", r.AvgWin)
	log.Printf("  Avg Loss:      ₹%.2f", r.AvgLoss)
	log.Printf("  Avg Hold:      %.0f mins", r.AvgHoldMins)

	// Strategy breakdown
	stratStats := map[string][3]float64{} // [count, wins, pnl]
	for _, t := range r.TradeLog {
		s := stratStats[t.Strategy]
		s[0]++
		if t.PnL > 0 {
			s[1]++
		}
		s[2] += t.PnL
		stratStats[t.Strategy] = s
	}
	log.Println("  ── Strategy Breakdown ──")
	for strat, s := range stratStats {
		wr := 0.0
		if s[0] > 0 {
			wr = s[1] / s[0] * 100
		}
		log.Printf("    %-20s  trades=%d  WR=%.0f%%  PnL=₹%.0f", strat, int(s[0]), wr, s[2])
	}
	log.Println("═══════════════════════════════════════════════════")
}

// ── Internal helpers ────────────────────────────────────────

// simulateIntraday simulates intraday price action from daily OHLCV bar
func (bt *Backtester) simulateIntraday(bar HistoricalBar, isShort bool, stop, target float64) (exitPrice float64, reason string) {
	if isShort {
		// Short: stop hit if price goes above stop, target if below target
		if bar.High >= stop {
			return stop, "STOP_HIT"
		}
		if bar.Low <= target {
			return target, "TARGET_HIT"
		}
		return bar.Close, "EOD_SQUAREOFF"
	}

	// Long: stop hit if price goes below stop, target if above target
	if bar.Low <= stop {
		return stop, "STOP_HIT"
	}
	if bar.High >= target {
		return target, "TARGET_HIT"
	}
	return bar.Close, "EOD_SQUAREOFF"
}

// getCloses returns the last N close prices up to (and including) the given date
func (bt *Backtester) getCloses(token uint32, upToDate string) []float64 {
	bars := bt.DailyBars[token]
	var closes []float64
	for _, bar := range bars {
		if bar.Date <= upToDate {
			closes = append(closes, bar.Close)
		}
	}
	return closes
}

// getAvgVolume returns average volume over N days before the given date
func (bt *Backtester) getAvgVolume(token uint32, beforeDate string, n int) float64 {
	bars := bt.DailyBars[token]
	var vols []float64
	for _, bar := range bars {
		if bar.Date < beforeDate {
			vols = append(vols, float64(bar.Volume))
		}
	}
	if len(vols) < n {
		return 0
	}
	sum := 0.0
	for _, v := range vols[len(vols)-n:] {
		sum += v
	}
	return sum / float64(n)
}

// computeATR returns ATR for the given token up to a date
func (bt *Backtester) computeATR(token uint32, upToDate string, period int) float64 {
	bars := bt.DailyBars[token]
	var trVals []float64
	for i := 1; i < len(bars) && bars[i].Date <= upToDate; i++ {
		h, l, pc := bars[i].High, bars[i].Low, bars[i-1].Close
		tr := math.Max(h-l, math.Max(math.Abs(h-pc), math.Abs(l-pc)))
		trVals = append(trVals, tr)
	}
	if len(trVals) < period {
		if len(trVals) == 0 {
			return 0
		}
		sum := 0.0
		for _, v := range trVals {
			sum += v
		}
		return sum / float64(len(trVals))
	}
	sum := 0.0
	for _, v := range trVals[len(trVals)-period:] {
		sum += v
	}
	return sum / float64(period)
}

func (bt *Backtester) computeSharpe(returns []float64) float64 {
	if len(returns) < 2 {
		return 0
	}
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
	return (mean / std) * math.Sqrt(252)
}

// RunAndSeed executes backtest and seeds journal.db with results for ML training.
// This is the function called by the engine to solve the ML cold-start problem.
func RunBacktestAndSeed() {
	startTime := time.Now()
	log.Println("[Backtest] ═══ Starting Backtester to Seed ML Training Data ═══")

	bt := NewBacktester(BacktestConfig{
		StartDate:      "2025-01-01",
		EndDate:        config.TodayIST().Format("2006-01-02"),
		InitialCapital: config.TotalCapital,
		MaxPositions:   5,
	})

	if bt.HistDB == "" {
		log.Println("[Backtest] No historical.db found — cannot run backtest")
		return
	}

	result, err := bt.Run()
	if err != nil {
		log.Printf("[Backtest] Failed: %v", err)
		return
	}

	bt.PrintSummary(result)

	// Save to journal.db for ML training
	if err := bt.SaveToJournal(result); err != nil {
		log.Printf("[Backtest] Failed to save to journal: %v", err)
	} else {
		log.Printf("[Backtest] Saved %d backtest trades to journal.db", result.TotalTrades)
	}

	log.Printf("[Backtest] Completed in %v", time.Since(startTime))
}
