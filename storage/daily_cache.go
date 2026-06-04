package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"bnf_go_engine/agents"
	"bnf_go_engine/config"
	"bnf_go_engine/data"
)

// DailyCache — precomputed daily indicators loaded at 8:45 AM.
// Exact port of Python storage/daily_cache.py
type DailyCache struct {
	mu     sync.RWMutex
	store  map[uint32]*DailyCacheEntry
	loaded bool

	// Kite REST credentials
	apiKey      string
	accessToken string
}

type DailyCacheEntry struct {
	Symbol       string
	Dates        []string
	Opens        []float64 // Daily open prices — backtest enters at next bar open
	Closes       []float64
	Highs        []float64
	Lows         []float64
	Volumes      []float64
	EMA10        float64 // Fast EMA — crossover entry signal
	EMA20        float64 // Trend EMA — entry confirmation + exit trigger
	AvgDailyVol  float64
	TurnoverCr   float64
	PivotSupport float64
	ATR14        float64
	High52W      float64
	Low52W       float64
	RSScore      int
}

func NewDailyCache() *DailyCache {
	return &DailyCache{
		store:       make(map[uint32]*DailyCacheEntry),
		apiKey:      config.KiteAPIKey,
		accessToken: config.KiteAccessToken,
	}
}

// isRegimeToken returns true for the two index tokens that need the full
// RegimeLookbackDays history for ROC calculation.
func isRegimeToken(token uint32) bool {
	return token == config.NiftySpotToken || token == config.SmallcapToken
}

// Preload loads 5 years of OHLCV for every universe token.
// Strategy: DB-first. On startup it reads historical.db (fast, no API calls).
// Only the gap between the latest DB row and today is fetched from Kite API,
// then written back to DB. First-ever run seeds the full 5-year history.
func (dc *DailyCache) Preload(universe map[uint32]string) bool {
	const fiveYearsCalendar = 1825

	histDB, dbErr := openHistoricalDB()
	if dbErr != nil {
		log.Printf("[DailyCache] ⚠️  historical.db unavailable (%v) — falling back to live API only", dbErr)
	} else {
		defer histDB.Close()
	}

	log.Printf("[DailyCache] Preloading %d tokens (DB-first, 5yr history)...", len(universe))

	type tokenJob struct {
		token  uint32
		symbol string
	}
	jobs := make(chan tokenJob, len(universe))
	for token, symbol := range universe {
		jobs <- tokenJob{token, symbol}
	}
	close(jobs)

	var mu sync.Mutex
	loaded, failed := 0, 0
	total := len(universe)

	var wg sync.WaitGroup
	for w := 0; w < 3; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				var bars []dailyBar
				hitAPI := false

				if histDB != nil {
					latest := latestDateInDB(histDB, job.token)
					today := config.NowIST().Format("2006-01-02")
					yesterday := config.NowIST().AddDate(0, 0, -1).Format("2006-01-02")

					if latest == "" {
						// First-ever load: seed full 5-year history from Kite API
						fromDate := config.NowIST().AddDate(0, 0, -fiveYearsCalendar).Format("2006-01-02")
						newBars, err := dc.fetchDailyRange(job.token, fromDate, today)
						if err == nil && len(newBars) > 0 {
							appendBarsToDB(histDB, job.token, newBars)
						}
						hitAPI = true
					} else if latest < yesterday {
						// Incremental update: fetch only the missing gap
						newBars, err := dc.fetchDailyRange(job.token, latest, today)
						if err == nil && len(newBars) > 0 {
							appendBarsToDB(histDB, job.token, newBars)
						}
						hitAPI = true
					}
					// else: DB is current (today or yesterday) — no API call needed

					// Read full history from DB (up to 5 years of trading bars)
					dbBars, err := readBarsFromDB(histDB, job.token, fiveYearsCalendar*2)
					if err == nil && len(dbBars) >= 25 {
						bars = dbBars
					}
				}

				// Fallback: DB unavailable or returned < 25 bars → call Kite API directly
				if len(bars) < 25 {
					apiBars, err := dc.fetchDaily(job.token, fiveYearsCalendar)
					if err != nil || len(apiBars) < 25 {
						mu.Lock()
						failed++
						mu.Unlock()
						if err != nil {
							log.Printf("[DailyCache] %s failed: %v", job.symbol, err)
						}
						time.Sleep(350 * time.Millisecond)
						continue
					}
					bars = apiBars
					if histDB != nil {
						appendBarsToDB(histDB, job.token, bars)
					}
					hitAPI = true
				}

				dc.buildEntryFromBars(job.token, job.symbol, bars)

				mu.Lock()
				loaded++
				if loaded%50 == 0 {
					log.Printf("[DailyCache] Progress: %d/%d loaded", loaded, total)
				}
				mu.Unlock()

				// Rate limit only when we actually called the Kite API
				if hitAPI {
					time.Sleep(350 * time.Millisecond)
				}
			}
		}()
	}

	wg.Wait()
	dc.computeRSScores()

	dc.mu.Lock()
	dc.loaded = loaded >= max(1, int(float64(len(universe))*0.8))
	dc.mu.Unlock()

	log.Printf("[DailyCache] Loaded %d/%d tokens. Failed: %d. Ready: %v",
		loaded, total, failed, dc.loaded)
	return dc.loaded
}

// buildEntryFromBars converts a []dailyBar slice into a DailyCacheEntry and stores it.
func (dc *DailyCache) buildEntryFromBars(token uint32, symbol string, bars []dailyBar) {
	opens := make([]float64, len(bars))
	closes := make([]float64, len(bars))
	highs := make([]float64, len(bars))
	lows := make([]float64, len(bars))
	volumes := make([]float64, len(bars))
	dates := make([]string, len(bars))
	for i, d := range bars {
		opens[i] = d.Open
		closes[i] = d.Close
		highs[i] = d.High
		lows[i] = d.Low
		volumes[i] = float64(d.Volume)
		dates[i] = d.Date
	}

	ema10Slice := data.ComputeEMA(closes, config.EMA10Period)
	ema10Val := 0.0
	if len(ema10Slice) > 0 {
		ema10Val = ema10Slice[len(ema10Slice)-1]
	}
	ema20Slice := data.ComputeEMA(closes, config.EMA20Period)
	ema20Val := 0.0
	if len(ema20Slice) > 0 {
		ema20Val = ema20Slice[len(ema20Slice)-1]
	}

	avgVol := 1.0
	if len(volumes) >= 20 {
		s := 0.0
		for _, v := range volumes[len(volumes)-20:] {
			s += v
		}
		avgVol = s / 20.0
	}
	avgTurn := 0.0
	if len(volumes) >= 20 && len(closes) >= 20 {
		s := 0.0
		off := len(volumes) - 20
		for i := 0; i < 20; i++ {
			s += volumes[off+i] * closes[off+i] / 1e7
		}
		avgTurn = s / 20.0
	}

	atr14 := computeATR(highs, lows, closes, 14)
	pivot := computePivotSupport(closes, lows)
	high52 := maxSlice(closes)
	low52 := minSlice(closes)

	dc.mu.Lock()
	dc.store[token] = &DailyCacheEntry{
		Symbol:       symbol,
		Dates:        dates,
		Opens:        opens,
		Closes:       closes,
		Highs:        highs,
		Lows:         lows,
		Volumes:      volumes,
		EMA10:        ema10Val,
		EMA20:        ema20Val,
		AvgDailyVol:  avgVol,
		TurnoverCr:   math.Round(avgTurn*100) / 100,
		PivotSupport: pivot,
		ATR14:        math.Round(atr14*100) / 100,
		High52W:      math.Round(high52*100) / 100,
		Low52W:       math.Round(low52*100) / 100,
	}
	dc.mu.Unlock()
}

// ToScannerCache converts to the agents.DailyCache format expected by scanner.
func (dc *DailyCache) ToScannerCache() *agents.DailyCache {
	dc.mu.RLock()
	defer dc.mu.RUnlock()

	sc := &agents.DailyCache{
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
		RSScore:      make(map[uint32]int),
		Loaded:       dc.loaded,
	}

	for token, entry := range dc.store {
		sc.ATR[token] = entry.ATR14
		sc.EMA10[token] = entry.EMA10
		sc.EMA20[token] = entry.EMA20
		sc.Opens[token] = entry.Opens
		sc.Closes[token] = entry.Closes
		sc.Highs[token] = entry.Highs
		sc.Lows[token] = entry.Lows
		sc.Volumes[token] = entry.Volumes
		sc.AvgVol[token] = entry.AvgDailyVol
		sc.TurnoverCr[token] = entry.TurnoverCr
		sc.PivotSupport[token] = entry.PivotSupport
		sc.High52W[token] = entry.High52W
		sc.RSScore[token] = entry.RSScore
		if len(entry.Dates) > len(sc.TradingDates) {
			sc.TradingDates = entry.Dates
		}
	}

	return sc
}

// ExportAgentsCache is an alias for ToScannerCache — used by the backtest API handler.
func (dc *DailyCache) ExportAgentsCache() *agents.DailyCache {
	return dc.ToScannerCache()
}

func (dc *DailyCache) IsLoaded() bool {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	return dc.loaded
}


// ── Kite REST Historical Data ────────────────────────────────

type dailyBar struct {
	Date   string
	Open   float64
	Close  float64
	High   float64
	Low    float64
	Volume int64
}

// ══════════════════════════════════════════════════════════════
//  Historical DB helpers — persistent 5-year price store
// ══════════════════════════════════════════════════════════════

func historicalDBPath() string {
	return config.BaseDir + string(os.PathSeparator) + "data" + string(os.PathSeparator) + "historical.db"
}

func openHistoricalDB() (*sql.DB, error) {
	db, err := sql.Open("sqlite", historicalDBPath()+"?_journal_mode=WAL&_busy_timeout=30000")
	if err != nil {
		return nil, err
	}
	db.Exec(`CREATE TABLE IF NOT EXISTS daily_data (
		token   INTEGER NOT NULL,
		date    TEXT    NOT NULL,
		open    REAL    NOT NULL,
		high    REAL    NOT NULL,
		low     REAL    NOT NULL,
		close   REAL    NOT NULL,
		volume  INTEGER NOT NULL,
		PRIMARY KEY (token, date)
	)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_daily_token_date ON daily_data (token, date)`)
	return db, nil
}

// latestDateInDB returns the most recent date string stored for a token, or "".
func latestDateInDB(db *sql.DB, token uint32) string {
	var d string
	db.QueryRow(`SELECT MAX(date) FROM daily_data WHERE token=?`, token).Scan(&d)
	return d
}

// readBarsFromDB reads up to limit bars for a token, oldest-first.
func readBarsFromDB(db *sql.DB, token uint32, limit int) ([]dailyBar, error) {
	rows, err := db.Query(`
		SELECT date, open, high, low, close, volume
		FROM daily_data
		WHERE token=?
		ORDER BY date ASC
		LIMIT ?`, token, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bars []dailyBar
	for rows.Next() {
		var b dailyBar
		rows.Scan(&b.Date, &b.Open, &b.High, &b.Low, &b.Close, &b.Volume)
		bars = append(bars, b)
	}
	return bars, nil
}

// appendBarsToDB upserts a batch of bars into historical.db in a single transaction.
func appendBarsToDB(db *sql.DB, token uint32, bars []dailyBar) {
	tx, err := db.Begin()
	if err != nil {
		return
	}
	stmt, err := tx.Prepare(`INSERT OR REPLACE INTO daily_data
		(token, date, open, high, low, close, volume) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return
	}
	defer stmt.Close()
	for _, b := range bars {
		stmt.Exec(token, b.Date, b.Open, b.High, b.Low, b.Close, b.Volume)
	}
	tx.Commit()
}

func (dc *DailyCache) fetchDaily(token uint32, days int) ([]dailyBar, error) {
	now := config.NowIST()
	from := now.AddDate(0, 0, -days)
	return dc.fetchDailyRange(token, from.Format("2006-01-02"), now.Format("2006-01-02"))
}

// fetchDailyRange fetches daily OHLCV bars between fromDate and toDate (inclusive).
func (dc *DailyCache) fetchDailyRange(token uint32, fromDate, toDate string) ([]dailyBar, error) {
	url := fmt.Sprintf(
		"https://api.kite.trade/instruments/historical/%d/day?from=%s&to=%s",
		token, fromDate, toDate,
	)

	for attempt := 0; attempt < 3; attempt++ {
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("X-Kite-Version", "3")
		req.Header.Set("Authorization", fmt.Sprintf("token %s:%s", dc.apiKey, dc.accessToken))

		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			if attempt < 2 {
				time.Sleep(time.Duration(attempt+1) * time.Second)
				continue
			}
			return nil, err
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)

		var result struct {
			Status string `json:"status"`
			Data   struct {
				Candles [][]interface{} `json:"candles"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, err
		}

		var bars []dailyBar
		for _, c := range result.Data.Candles {
			if len(c) < 6 {
				continue
			}
			dateStr := ""
			if s, ok := c[0].(string); ok && len(s) >= 10 {
				dateStr = s[:10]
			}
			bars = append(bars, dailyBar{
				Date:   dateStr,
				Open:   toFloat(c[1]),
				Close:  toFloat(c[4]),
				High:   toFloat(c[2]),
				Low:    toFloat(c[3]),
				Volume: int64(toFloat(c[5])),
			})
		}
		return bars, nil
	}
	return nil, fmt.Errorf("fetch failed after 3 attempts")
}

func toFloat(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case int:
		return float64(val)
	case json.Number:
		f, _ := val.Float64()
		return f
	}
	return 0
}

// computeRSScores ranks universe stocks by relative strength using available bars.
// With 5-year (1825-day) history we compute multi-timeframe RS scores.
// Regime tokens (420-day history) also get a 6-month component.
func (dc *DailyCache) computeRSScores() {
	dc.mu.Lock()
	defer dc.mu.Unlock()

	type tokenPerf struct {
		token     uint32
		composite float64
	}

	var perfs []tokenPerf
	for token, entry := range dc.store {
		closes := entry.Closes
		if len(closes) < 21 {
			continue
		}
		cNow := closes[len(closes)-1]

		// Use whatever lookbacks are available; weight shorter periods more heavily
		// when insufficient history is available.
		p6, p3, p1, pw := 0.0, 0.0, 0.0, 0.0
		if len(closes) >= 126 && closes[len(closes)-126] > 0 {
			p6 = (cNow - closes[len(closes)-126]) / closes[len(closes)-126] * 100
		}
		if len(closes) >= 63 && closes[len(closes)-63] > 0 {
			p3 = (cNow - closes[len(closes)-63]) / closes[len(closes)-63] * 100
		}
		if len(closes) >= 21 && closes[len(closes)-21] > 0 {
			p1 = (cNow - closes[len(closes)-21]) / closes[len(closes)-21] * 100
		}
		if len(closes) >= 5 && closes[len(closes)-5] > 0 {
			pw = (cNow - closes[len(closes)-5]) / closes[len(closes)-5] * 100
		}
		composite := p6*0.3 + p3*0.35 + p1*0.25 + pw*0.1
		perfs = append(perfs, tokenPerf{token, composite})
	}

	sort.Slice(perfs, func(i, j int) bool { return perfs[i].composite < perfs[j].composite })
	n := len(perfs)
	for rank, tp := range perfs {
		rs := int(float64(rank+1) / float64(n) * 100)
		if rs < 1 {
			rs = 1
		}
		if rs > 99 {
			rs = 99
		}
		if entry, ok := dc.store[tp.token]; ok {
			entry.RSScore = rs
		}
	}
}

// ── Math helpers ─────────────────────────────────────────────
func sma(prices []float64, period int) float64 {
	if len(prices) < period {
		return 0
	}
	s := 0.0
	for _, p := range prices[len(prices)-period:] {
		s += p
	}
	return s / float64(period)
}

func computeATR(highs, lows, closes []float64, period int) float64 {
	if len(highs) < period+1 {
		if len(highs) > 0 {
			s := 0.0
			for i := range highs {
				s += highs[i] - lows[i]
			}
			return s / float64(len(highs))
		}
		return 0
	}
	var trs []float64
	for i := 1; i < len(highs); i++ {
		tr := math.Max(highs[i]-lows[i], math.Max(math.Abs(highs[i]-closes[i-1]), math.Abs(lows[i]-closes[i-1])))
		trs = append(trs, tr)
	}
	if len(trs) < period {
		s := 0.0
		for _, v := range trs {
			s += v
		}
		return s / float64(len(trs))
	}
	s := 0.0
	for _, v := range trs[len(trs)-period:] {
		s += v
	}
	return s / float64(period)
}

func computePivotSupport(closes, lows []float64) float64 {
	if len(lows) < 10 {
		return 0
	}
	current := closes[len(closes)-1]
	var pivots []float64
	for i := 1; i < len(lows)-1; i++ {
		if lows[i] < lows[i-1] && lows[i] < lows[i+1] {
			pivots = append(pivots, lows[i])
		}
	}
	best := current * 0.93
	for _, p := range pivots {
		if p < current && p > best {
			best = p
		}
	}
	return best
}

func maxSlice(s []float64) float64 {
	if len(s) == 0 {
		return 0
	}
	m := s[0]
	for _, v := range s[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

func minSlice(s []float64) float64 {
	if len(s) == 0 {
		return 0
	}
	m := s[0]
	for _, v := range s[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// SyncEODToHistoricalDB fetches today's finalized OHLCV bar for every token,
// writes it to historical.db, and refreshes the in-memory cache entry so the
// next Preload (and backtest) sees current data without a full restart.
func (dc *DailyCache) SyncEODToHistoricalDB(universe map[uint32]string) {
	db, err := openHistoricalDB()
	if err != nil {
		log.Printf("[DailyCache] ❌ Failed to open historical.db for EOD sync: %v", err)
		return
	}
	defer db.Close()

	log.Printf("[DailyCache] 💾 EOD DB Sync — %d tokens...", len(universe))

	today := config.TodayIST().Format("2006-01-02")
	count := 0

	for token, symbol := range universe {
		bars, err := dc.fetchDailyRange(token, today, today)
		if err != nil || len(bars) == 0 {
			time.Sleep(340 * time.Millisecond)
			continue
		}

		// Only accept today's bar — never write a stale bar under today's date
		var todayBar *dailyBar
		for _, b := range bars {
			if b.Date == today {
				clone := b
				todayBar = &clone
				break
			}
		}
		if todayBar == nil {
			time.Sleep(340 * time.Millisecond)
			continue
		}

		appendBarsToDB(db, token, []dailyBar{*todayBar})
		count++

		// Refresh in-memory cache entry with the updated full history
		allBars, readErr := readBarsFromDB(db, token, 1825*2)
		if readErr == nil && len(allBars) >= 25 {
			dc.buildEntryFromBars(token, symbol, allBars)
		}

		time.Sleep(340 * time.Millisecond)
	}

	log.Printf("[DailyCache] ✅ EOD Sync complete — %d bars written to historical.db", count)
}
