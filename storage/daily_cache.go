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
	Closes       []float64
	Highs        []float64
	Lows         []float64
	Volumes      []float64
	EMA25        float64
	RSI14        float64
	BBUpper      float64
	BBLower      float64
	AvgDailyVol  float64
	TurnoverCr   float64
	PivotSupport float64
	ATR14        float64
	SMA50        float64
	SMA150       float64
	SMA200       float64
	SMA200Up     bool
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

// Preload fetches 260 days of historical data for all universe tokens.
// Must be called before market open (8:45 AM).
func (dc *DailyCache) Preload(universe map[uint32]string) bool {
	log.Printf("[DailyCache] Preloading %d tokens (260d)...", len(universe))
	loaded := 0
	failed := 0

	for token, symbol := range universe {
		dailyData, err := dc.fetchDaily(token, 260)
		if err != nil || len(dailyData) < 25 {
			failed++
			if err != nil {
				log.Printf("[DailyCache] %s failed: %v", symbol, err)
			}
			continue
		}

		closes := make([]float64, len(dailyData))
		highs := make([]float64, len(dailyData))
		lows := make([]float64, len(dailyData))
		volumes := make([]float64, len(dailyData))
		for i, d := range dailyData {
			closes[i] = d.Close
			highs[i] = d.High
			lows[i] = d.Low
			volumes[i] = float64(d.Volume)
		}

		ema25 := data.ComputeEMA(closes, 25)
		ema25Val := 0.0
		if len(ema25) > 0 {
			ema25Val = ema25[len(ema25)-1]
		}

		rsiSlice := data.ComputeRSI(closes, 14)
		rsi14 := 50.0
		if len(rsiSlice) > 0 {
			rsi14 = rsiSlice[len(rsiSlice)-1]
		}

		bbHi, _, bbLo := data.ComputeBollinger(closes, 20, 2.0)

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

		sma50, sma150, sma200 := 0.0, 0.0, 0.0
		if len(closes) >= 50 {
			sma50 = sma(closes, 50)
		}
		if len(closes) >= 150 {
			sma150 = sma(closes, 150)
		}
		if len(closes) >= 200 {
			sma200 = sma(closes, 200)
		}

		sma200Up := false
		if len(closes) >= 220 {
			sma200Prev := sma(closes[:len(closes)-20], 200)
			sma200Up = sma200 > sma200Prev
		}

		high52 := maxSlice(closes)
		low52 := minSlice(closes)

		dc.mu.Lock()
		dc.store[token] = &DailyCacheEntry{
			Symbol:       symbol,
			Closes:       closes,
			Highs:        highs,
			Lows:         lows,
			Volumes:      volumes,
			EMA25:        ema25Val,
			RSI14:        rsi14,
			BBUpper:      bbHi,
			BBLower:      bbLo,
			AvgDailyVol:  avgVol,
			TurnoverCr:   math.Round(avgTurn*100) / 100,
			PivotSupport: pivot,
			ATR14:        math.Round(atr14*100) / 100,
			SMA50:        math.Round(sma50*100) / 100,
			SMA150:       math.Round(sma150*100) / 100,
			SMA200:       math.Round(sma200*100) / 100,
			SMA200Up:     sma200Up,
			High52W:      math.Round(high52*100) / 100,
			Low52W:       math.Round(low52*100) / 100,
		}
		dc.mu.Unlock()

		loaded++
		// Rate limit: Kite historical_data limit ~3/sec
		time.Sleep(350 * time.Millisecond)
	}

	// Compute RS scores cross-sectionally
	dc.computeRSScores()

	dc.mu.Lock()
	dc.loaded = loaded >= max(1, int(float64(len(universe))*0.8))
	dc.mu.Unlock()

	log.Printf("[DailyCache] Loaded %d/%d tokens. Failed: %d. Ready: %v",
		loaded, len(universe), failed, dc.loaded)
	return dc.loaded
}

// ToScannerCache converts to the agents.DailyCache format expected by scanner
func (dc *DailyCache) ToScannerCache() *agents.DailyCache {
	dc.mu.RLock()
	defer dc.mu.RUnlock()

	sc := &agents.DailyCache{
		SMA200:       make(map[uint32]float64),
		ATR:          make(map[uint32]float64),
		EMA25:        make(map[uint32]float64),
		BBLower:      make(map[uint32]float64),
		Closes:       make(map[uint32][]float64),
		Highs:        make(map[uint32][]float64),
		Lows:         make(map[uint32][]float64),
		AvgVol:       make(map[uint32]float64),
		TurnoverCr:   make(map[uint32]float64),
		PivotSupport: make(map[uint32]float64),
		Loaded:       dc.loaded,
	}

	for token, entry := range dc.store {
		sc.SMA200[token] = entry.SMA200
		sc.ATR[token] = entry.ATR14
		sc.EMA25[token] = entry.EMA25
		sc.BBLower[token] = entry.BBLower
		sc.Closes[token] = entry.Closes
		sc.Highs[token] = entry.Highs
		sc.Lows[token] = entry.Lows
		sc.AvgVol[token] = entry.AvgDailyVol
		sc.TurnoverCr[token] = entry.TurnoverCr
		sc.PivotSupport[token] = entry.PivotSupport
	}

	return sc
}

func (dc *DailyCache) IsLoaded() bool {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	return dc.loaded
}

func (dc *DailyCache) GetAvgDailyVol(token uint32) float64 {
	dc.mu.RLock()
	defer dc.mu.RUnlock()
	if e, ok := dc.store[token]; ok {
		return e.AvgDailyVol
	}
	return 1.0
}

// ── Kite REST Historical Data ────────────────────────────────

type dailyBar struct {
	Close  float64
	High   float64
	Low    float64
	Volume int64
}

func (dc *DailyCache) fetchDaily(token uint32, days int) ([]dailyBar, error) {
	now := config.NowIST()
	from := now.AddDate(0, 0, -days)

	url := fmt.Sprintf(
		"https://api.kite.trade/instruments/historical/%d/day?from=%s&to=%s",
		token,
		from.Format("2006-01-02"),
		now.Format("2006-01-02"),
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
			bars = append(bars, dailyBar{
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

// ── RS Score computation ─────────────────────────────────────
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
		if len(closes) < 260 {
			continue
		}
		cNow := closes[len(closes)-1]
		p12, p3, p1 := 0.0, 0.0, 0.0
		if len(closes) >= 252 && closes[len(closes)-252] > 0 {
			p12 = (cNow - closes[len(closes)-252]) / closes[len(closes)-252] * 100
		}
		if len(closes) >= 63 && closes[len(closes)-63] > 0 {
			p3 = (cNow - closes[len(closes)-63]) / closes[len(closes)-63] * 100
		}
		if len(closes) >= 21 && closes[len(closes)-21] > 0 {
			p1 = (cNow - closes[len(closes)-21]) / closes[len(closes)-21] * 100
		}
		perfs = append(perfs, tokenPerf{token, p12*0.4 + p3*0.3 + p1*0.3})
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
