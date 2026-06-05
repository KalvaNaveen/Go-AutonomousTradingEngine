package storage

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"sort"
	"sync"
	"time"

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


// Preload loads 5 years of OHLCV for every universe token directly from Kite API.
// DB cache is bypassed so that every scan always reflects the latest Kite data.
func (dc *DailyCache) Preload(universe map[uint32]string) bool {
	const fiveYearsCalendar = 1825

	log.Printf("[DailyCache] Preloading %d tokens (live Kite API, no DB cache)...", len(universe))

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
				bars, err := dc.fetchDaily(job.token, fiveYearsCalendar)
				if err != nil || len(bars) < 25 {
					mu.Lock()
					failed++
					mu.Unlock()
					if err != nil {
						log.Printf("[DailyCache] %s failed: %v", job.symbol, err)
					}
					time.Sleep(350 * time.Millisecond)
					continue
				}

				dc.buildEntryFromBars(job.token, job.symbol, bars)

				mu.Lock()
				loaded++
				if loaded%50 == 0 {
					log.Printf("[DailyCache] Progress: %d/%d loaded", loaded, total)
				}
				mu.Unlock()

				time.Sleep(350 * time.Millisecond)
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

