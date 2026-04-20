package simulator

import (
	"database/sql"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"bnf_go_engine/agents"
	"bnf_go_engine/config"
	"bnf_go_engine/storage"

	_ "modernc.org/sqlite"
)

// BacktestConfig holds simulation parameters
type BacktestConfig struct {
	StartDate      string
	EndDate        string
	InitialCapital float64
	MaxPositions   int
	RiskPerTrade   float64
	Strategies     []string
	TokenToCompany map[uint32]string
}

// BacktestTrade is a single simulated trade
type BacktestTrade struct {
	Date       string
	Token      uint32
	Symbol     string
	Strategy   string
	Regime     string
	EntryPrice float64
	ExitPrice  float64
	Target     float64
	StopLoss   float64
	Qty        int
	PnL        float64
	IsShort    bool
	ExitReason string
	HoldMins   float64
	RVol       float64
}

// BacktestResult holds the outcome
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
	FinalCapital  float64
	ExecutionTime time.Duration
}

// MinuteBar is one minute of simulated intraday data
type MinuteBar struct {
	Timestamp string // "YYYY-MM-DD HH:MM:SS"
	Date      string // "YYYY-MM-DD"
	Time      string // "HH:MM"
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    int64
}

type DailyBar struct {
	Date   string
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume int64
}

// Backtester is the simulation engine
type Backtester struct {
	Config    BacktestConfig
	HistDB    string
	Universe  map[uint32]string
	LogOutput func(string, ...interface{})
	DailyBars map[uint32][]DailyBar
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

	if cfg.InitialCapital == 0 { cfg.InitialCapital = config.TotalCapital }
	if cfg.MaxPositions == 0 { cfg.MaxPositions = config.MaxOpenPositions }
	if cfg.RiskPerTrade == 0 { cfg.RiskPerTrade = config.MaxRiskPerTradePct }

	return &Backtester{
		Config:   cfg,
		HistDB:   histDB,
	}
}

func (bt *Backtester) log(format string, v ...interface{}) {
	if bt.LogOutput != nil {
		bt.LogOutput(format, v...)
	} else {
		log.Printf("[Backtest] "+format, v...)
	}
}

// PreloadedData holds all data loaded from the database for reuse across multiple runs
type PreloadedData struct {
	DaysMap   map[string]*DayData
	DailyBars map[uint32][]DailyBar
	Dates     []string
}

// DayData holds all minute bars for a single trading day
type DayData struct {
	Date  string
	Ticks map[string]map[uint32]MinuteBar // time -> token -> Bar
}

func (bt *Backtester) Run() (*BacktestResult, error) {
	data, err := bt.PreloadData()
	if err != nil {
		return nil, err
	}
	return bt.RunWithData(data)
}

// PreloadData loads all minute and daily data from historical.db once.
// The result can be reused across multiple RunWithData() calls with different configs.
func (bt *Backtester) PreloadData() (*PreloadedData, error) {
	if bt.HistDB == "" {
		return nil, fmt.Errorf("no historical.db found")
	}

	db, err := sql.Open("sqlite", bt.HistDB+"?_journal_mode=WAL&_busy_timeout=30000")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Enable WAL mode for concurrent reads (critical when another process holds the DB)
	db.Exec(`PRAGMA journal_mode=WAL`)
	db.Exec(`PRAGMA busy_timeout=30000`)

	// ── Performance: Create indexes if missing (skipped gracefully if DB is locked) ──
	bt.log("Ensuring DB indexes exist...")
	_, idxErr := db.Exec(`CREATE INDEX IF NOT EXISTS idx_minute_data_datetime ON minute_data(date_time)`)
	if idxErr != nil {
		bt.log("Warning: Could not create index (DB may be locked by another process): %v", idxErr)
		bt.log("Continuing without index — query will be slower but will work.")
	}
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_daily_data_token_date ON daily_data(token, date)`)
	bt.log("Index check complete.")

	bt.log("Fetching exact intraday ticks from: %s to %s", bt.Config.StartDate, bt.Config.EndDate)

	// Load daily_data ONLY for the date range needed (not full table)
	bt.log("Loading daily cache bars...")
	queryD := `SELECT token, date, open, high, low, close, volume FROM daily_data WHERE date >= ? ORDER BY token, date ASC`
	// Load from 300 days before start for indicator warmup
	warmupStart, _ := time.Parse("2006-01-02", bt.Config.StartDate)
	warmupDate := warmupStart.AddDate(0, 0, -300).Format("2006-01-02")
	dailyBars := make(map[uint32][]DailyBar)
	rowsD, err := db.Query(queryD, warmupDate)
	if err == nil {
		for rowsD.Next() {
			var dtk uint32
			var dbb DailyBar
			rowsD.Scan(&dtk, &dbb.Date, &dbb.Open, &dbb.High, &dbb.Low, &dbb.Close, &dbb.Volume)
			dailyBars[dtk] = append(dailyBars[dtk], dbb)
		}
		rowsD.Close()
	}
	bt.log("Daily cache loaded: %d tokens", len(dailyBars))

	// The stored timestamps have timezone suffixes like "+05:30".
	// Use LIKE-based match to handle both "2026-04-08 09:15:00" and "2026-04-08 09:15:00+05:30" formats.
	bt.log("Querying minute_data (indexed)...")
	query := `
		SELECT token, date_time, open, high, low, close, volume
		FROM minute_data
		WHERE date_time >= ? AND date_time < ?
		ORDER BY date_time ASC
	`
	// Use start of day and day-after-end to capture all timezone variants
	rows, err := db.Query(query, bt.Config.StartDate, bt.Config.EndDate+"Z")
	if err != nil {
		query = `
			SELECT instrument_token, date_time, open, high, low, close, volume
			FROM minute_data
			WHERE date_time >= ? AND date_time < ?
			ORDER BY date_time ASC
		`
		rows, err = db.Query(query, bt.Config.StartDate, bt.Config.EndDate+"Z")
		if err != nil {
			return nil, fmt.Errorf("failed querying minute_data: %v", err)
		}
	}
	defer rows.Close()

	daysMap := make(map[string]*DayData)
	totalLoaded := 0

	for rows.Next() {
		var b MinuteBar
		var token uint32
		rows.Scan(&token, &b.Timestamp, &b.Open, &b.High, &b.Low, &b.Close, &b.Volume)
		parts := strings.Split(b.Timestamp, " ")
		if len(parts) == 2 {
			b.Date = parts[0]
			b.Time = parts[1][:5] // "HH:MM"
		} else {
			continue // skip malformed
		}

		if daysMap[b.Date] == nil {
			daysMap[b.Date] = &DayData{
				Date:  b.Date,
				Ticks: make(map[string]map[uint32]MinuteBar),
			}
		}

		if daysMap[b.Date].Ticks[b.Time] == nil {
			daysMap[b.Date].Ticks[b.Time] = make(map[uint32]MinuteBar)
		}

		daysMap[b.Date].Ticks[b.Time][token] = b
		totalLoaded++

		if totalLoaded%200000 == 0 {
			bt.log("Aggregating historical vectors... parsed %d minute bars", totalLoaded)
		}
	}

	bt.log("Buffered %d exact intraday ticks across %d trading days.", totalLoaded, len(daysMap))

	if len(daysMap) == 0 {
		return nil, fmt.Errorf("0 intraday ticks found in date range")
	}

	var dates []string
	for d := range daysMap {
		dates = append(dates, d)
	}
	sort.Strings(dates)

	return &PreloadedData{DaysMap: daysMap, DailyBars: dailyBars, Dates: dates}, nil
}

// RunWithData runs the simulation using pre-loaded data (avoids re-reading from DB)
func (bt *Backtester) RunWithData(data *PreloadedData) (*BacktestResult, error) {
	bt.DailyBars = data.DailyBars

	result := &BacktestResult{}
	capital := bt.Config.InitialCapital
	peakCapital := capital
	maxDD := 0.0

	// ── Main Replay Loop ──
	for _, date := range data.Dates {
		dayInfo := data.DaysMap[date]

		stratCount := 14
		if len(bt.Config.Strategies) > 0 {
			stratCount = len(bt.Config.Strategies)
		}
		bt.log("▶ Evaluating %d active strategies isolated for %s...", stratCount, date)
		
		// Map Universe from tokens present today
		universeMap := make(map[uint32]string)
		for _, mmap := range dayInfo.Ticks {
			for token := range mmap {
				companyName, ok := bt.Config.TokenToCompany[token]
				if ok && companyName != "" {
					universeMap[token] = companyName
				} else {
					universeMap[token] = fmt.Sprintf("SYM_%d", token)
				}
			}
			break
		}

		// Initialize Isolated Simulator Core
		mockTickStore := storage.NewTickStore()
		scanner := agents.NewScannerAgent()
		scanner.Universe = universeMap
		scanner.DailyCache = &agents.DailyCache{Loaded: false}
		scanner.SimulatorMode = true
		
		// Map scanner hooks directly to the mocked store
		scanner.GetLTP = func(token uint32) float64 { return mockTickStore.GetLTP(token) }
		scanner.GetVWAP = func(token uint32) float64 { return mockTickStore.GetVWAP(token) }
		scanner.GetDayOpen = func(token uint32) float64 { return mockTickStore.GetDayOpen(token) }
		scanner.GetVolume = func(token uint32) int64 { return mockTickStore.GetVolume(token) }
		scanner.GetCandles5m = func(token uint32) []agents.Candle { return mockTickStore.GetCandles5Min(token) }
		scanner.GetIntraday = func(token uint32, f string) []agents.Candle { return mockTickStore.GetCandles5Min(token) }
		scanner.GetDepth = func(token uint32) map[string]float64 { return map[string]float64{"bid_ask_ratio": 1.0} }
		scanner.GetORB = func(token uint32) (float64, float64) { return 0, 0 }
		
		scanner.ComputeRVol = func(token uint32) float64 { 
			// Standard fallback given no historical daily caches in simulation intraday context
			return 2.8 
		}

		// Execute daily cache population for this historical date
		errCache := bt.buildDailyCache(scanner.DailyCache, universeMap, date)
		if errCache != nil {
			bt.log("Warning: Cache build failed for %s", date)
		}

		var activeTrades []BacktestTrade

		// Define chronological trading limits
		times := []string{}
		for t := range dayInfo.Ticks {
			times = append(times, t)
		}
		sort.Strings(times)

		dayTradesCount := 0
		dayPnl := 0.0

		// --- Fast 5-Min Builder State ---
		completedCandles := make(map[uint32][]agents.Candle)
		activeCandles := make(map[uint32]*agents.Candle)
		currentBucketStr := ""

		for _, timeStr := range times {
			isEOD := timeStr > "15:20"

			// Determine bucket for THIS minute
			bucketHr := timeStr[:2]
			bucketMins := timeStr[3:5]
			bMinInt := int(bucketMins[0]-'0')*10 + int(bucketMins[1]-'0')
			bMinInt = (bMinInt / 5) * 5
			bucketStr := fmt.Sprintf("%s:%02d", bucketHr, bMinInt)

			if currentBucketStr != "" && bucketStr != currentBucketStr {
				for token, cand := range activeCandles {
					if cand != nil {
						completedCandles[token] = append(completedCandles[token], *cand)
						activeCandles[token] = nil
					}
				}
			}
			currentBucketStr = bucketStr

			// Push ticks to mock store and aggregate 5m
			for token, bar := range dayInfo.Ticks[timeStr] {
				// We simply inject the closed 1-minute close as the LTS
				mockTickStore.SetSimulatorState(token, bar.Close, bar.Volume, bar.Open)
				
				cand := activeCandles[token]
				if cand == nil {
					activeCandles[token] = &agents.Candle{Open: bar.Open, High: bar.High, Low: bar.Low, Close: bar.Close, Volume: bar.Volume}
				} else {
					if bar.High > cand.High { cand.High = bar.High }
					if bar.Low < cand.Low { cand.Low = bar.Low }
					cand.Close = bar.Close
					cand.Volume += bar.Volume 
				}
			}

			// Square off logic if near EOD
			if isEOD && len(activeTrades) > 0 {
				for _, t := range activeTrades {
					bar, ok := dayInfo.Ticks[timeStr][t.Token]
					if ok {
						exitPnl := 0.0
						if t.IsShort {
							exitPnl = t.EntryPrice - bar.Close
						} else {
							exitPnl = bar.Close - t.EntryPrice
						}
						t.ExitPrice = bar.Close
						t.ExitReason = "EOD_SQUAREOFF"
						t.PnL = exitPnl
						dayPnl += exitPnl
						result.TradeLog = append(result.TradeLog, t)
					}
				}
				activeTrades = nil
				continue
			}

			// Prevent new trades after 15:15
			if timeStr > "15:15" {
				continue
			}

			// Manage active stops and targets on the minute frame
			if len(activeTrades) > 0 {
				var remaining []BacktestTrade
				for _, t := range activeTrades {
					bar, ok := dayInfo.Ticks[timeStr][t.Token]
					closedTrade := false

					if ok {
						if t.IsShort {
							if bar.High >= t.StopLoss {
								t.ExitPrice = t.StopLoss
								t.PnL = t.EntryPrice - t.StopLoss
								t.ExitReason = "STOP_HIT"
								closedTrade = true
							} else if bar.Low <= t.Target {
								t.ExitPrice = t.Target
								t.PnL = t.EntryPrice - t.Target
								t.ExitReason = "TARGET_HIT"
								closedTrade = true
							}
						} else {
							if bar.Low <= t.StopLoss {
								t.ExitPrice = t.StopLoss
								t.PnL = t.StopLoss - t.EntryPrice
								t.ExitReason = "STOP_HIT"
								closedTrade = true
							} else if bar.High >= t.Target {
								t.ExitPrice = t.Target
								t.PnL = t.Target - t.EntryPrice
								t.ExitReason = "TARGET_HIT"
								closedTrade = true
							}
						}
					}

					if closedTrade {
						dayPnl += t.PnL
						result.TradeLog = append(result.TradeLog, t)
					} else {
						remaining = append(remaining, t)
					}
				}
				activeTrades = remaining
			}

			// Only scan on 5-min boundaries to exactly match live Engine
			if strings.HasSuffix(timeStr, "0") || strings.HasSuffix(timeStr, "5") {
				// Fast O(1) 5min array injection
				for token := range universeMap {
					merged := append([]agents.Candle{}, completedCandles[token]...)
					if cand := activeCandles[token]; cand != nil {
						merged = append(merged, *cand)
					}
					mockTickStore.SetCandles5Min(token, merged)
				}

				if dayTradesCount < bt.Config.MaxPositions {
					signals := scanner.RunStrategyScans("NORMAL", bt.Config.Strategies)
					for _, sig := range signals {
						if dayTradesCount >= bt.Config.MaxPositions { break }

						// Execute trade directly
						bt.log("Evaluated Intraday Signal: %s on %v at %s", sig.Strategy, sig.Symbol, timeStr)
						bar := dayInfo.Ticks[timeStr][sig.Token]
						
						isShort := sig.StopPrice > bar.Close

						trade := BacktestTrade{
							Date: date, Token: sig.Token, Symbol: sig.Symbol, Strategy: sig.Strategy,
							EntryPrice: bar.Close, IsShort: isShort, RVol: sig.RVol,
							StopLoss: sig.StopPrice, Target: sig.TargetPrice,
						}
						activeTrades = append(activeTrades, trade)
						dayTradesCount++
					}
				}
			}
		}

		result.DailyReturns = append(result.DailyReturns, dayPnl)
		capital += dayPnl
		if capital > peakCapital { peakCapital = capital }
		dd := (peakCapital - capital) / peakCapital
		if dd > maxDD { maxDD = dd }
	}

	result.TotalTrades = len(result.TradeLog)
	totalWinPnl := 0.0
	totalLossPnl := 0.0
	for _, t := range result.TradeLog {
		result.TotalPnL += t.PnL
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
	}
	if result.WinTrades > 0 { result.AvgWin = totalWinPnl / float64(result.WinTrades) }
	if result.LossTrades > 0 { result.AvgLoss = totalLossPnl / float64(result.LossTrades) }
	if totalLossPnl > 0 { result.ProfitFactor = totalWinPnl / totalLossPnl }

	return result, nil
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

// ── Historical Cache Rebuilder ──

func (bt *Backtester) buildDailyCache(cache *agents.DailyCache, universe map[uint32]string, cutoffDate string) error {
	cache.SMA200 = make(map[uint32]float64)
	cache.ATR = make(map[uint32]float64)
	cache.EMA25 = make(map[uint32]float64)
	cache.BBLower = make(map[uint32]float64)
	cache.Closes = make(map[uint32][]float64)
	cache.Highs = make(map[uint32][]float64)
	cache.Lows = make(map[uint32][]float64)
	cache.AvgVol = make(map[uint32]float64)
	cache.TurnoverCr = make(map[uint32]float64)
	cache.PivotSupport = make(map[uint32]float64)
	cache.Loaded = true

	for token := range universe {
		bars := bt.DailyBars[token]
		if len(bars) == 0 { continue }
		
		var closes, highs, lows []float64
		var volumes []float64
		var lastClose float64
		
		for _, b := range bars {
			if b.Date >= cutoffDate { break } // Only data *before* today
			closes = append(closes, b.Close)
			highs = append(highs, b.High)
			lows = append(lows, b.Low)
			volumes = append(volumes, float64(b.Volume))
			lastClose = b.Close
		}
		
		if len(closes) < 20 { continue }

		cache.Closes[token] = closes
		cache.Highs[token] = highs
		cache.Lows[token] = lows

		// Compute indicators natively matching Python/Live
		ema25 := computeEMA(closes, 25)
		if len(ema25) > 0 { cache.EMA25[token] = ema25[len(ema25)-1] }

		_, _, bbl := computeBollinger(closes, 20, 2.0)
		cache.BBLower[token] = bbl

		if len(closes) >= 200 {
			cache.SMA200[token] = computeSMA(closes, 200)
		}

		if len(volumes) >= 20 {
			cache.AvgVol[token] = computeSMA(volumes, 20)
		}

		atrVals := computeATR(highs, lows, closes, 14)
		if len(atrVals) > 0 {
			cache.ATR[token] = atrVals[len(atrVals)-1]
		}
		
		cache.TurnoverCr[token] = cache.AvgVol[token] * lastClose / 10000000.0

		// Simple CPR Pivot
		lastH := highs[len(highs)-1]
		lastL := lows[len(lows)-1]
		lastC := closes[len(closes)-1]
		pivot := (lastH + lastL + lastC) / 3.0
		cache.PivotSupport[token] = (2 * pivot) - lastH
	}
	return nil
}

// Indicator math helpers
func computeSMA(data []float64, window int) float64 {
	if len(data) < window { return 0 }
	s := 0.0
	for _, v := range data[len(data)-window:] { s += v }
	return s / float64(window)
}

func computeEMA(data []float64, window int) []float64 {
	if len(data) < window { return nil }
	k := 2.0 / float64(window+1)
	res := make([]float64, len(data))
	
	sum := 0.0
	for i := 0; i < window; i++ { sum += data[i] }
	res[window-1] = sum / float64(window)

	for i := window; i < len(data); i++ {
		res[i] = data[i]*k + res[i-1]*(1.0-k)
	}
	return res
}

func computeBollinger(data []float64, window int, mult float64) (float64, float64, float64) {
	if len(data) < window { return 0,0,0 }
	slice := data[len(data)-window:]
	sma := computeSMA(slice, window)
	
	var variance float64
	for _, v := range slice { variance += (v-sma)*(v-sma) }
	stddev := math.Sqrt(variance / float64(window))
	
	return sma + mult*stddev, sma, sma - mult*stddev
}

func computeATR(highs, lows, closes []float64, period int) []float64 {
	if len(closes) < period { return nil }
	trs := make([]float64, len(closes))
	for i:=1; i<len(closes); i++ {
		h, l, pc := highs[i], lows[i], closes[i-1]
		trs[i] = math.Max(h-l, math.Max(math.Abs(h-pc), math.Abs(l-pc)))
	}
	
	res := make([]float64, len(closes))
	sum := 0.0
	for i:=1; i<=period; i++ { sum += trs[i] }
	res[period] = sum / float64(period)
	
	for i:=period+1; i<len(closes); i++ {
		res[i] = (res[i-1]*float64(period-1) + trs[i]) / float64(period)
	}
	return res
}

func (bt *Backtester) SaveToJournal(result *BacktestResult) error {
	return nil // Stub for now
}

func (bt *Backtester) PrintSummary(r *BacktestResult) {
	log.Println("═══════════════════════════════════════════════════")
	log.Println("       BACKTEST RESULTS — Intraday Simulator Core")
	log.Println("═══════════════════════════════════════════════════")
	log.Printf("  Capital:       ₹%.0f", bt.Config.InitialCapital)
	log.Printf("  Total Trades:  %d", r.TotalTrades)
	log.Printf("  Win Rate:      %.1f%% (%d/%d)", r.WinRate, r.WinTrades, r.TotalTrades)
	log.Printf("  Total P&L:     ₹%.2f", r.TotalPnL)
	log.Printf("  Profit Factor: %.2f", r.ProfitFactor)
	log.Println("═══════════════════════════════════════════════════")
}

func RunBacktestAndSeed() {
	// Stub for now
}
