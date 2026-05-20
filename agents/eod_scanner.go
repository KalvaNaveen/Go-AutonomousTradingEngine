package agents

import (
	"encoding/csv"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"bnf_go_engine/config"
	"bnf_go_engine/data"
)

// ══════════════════════════════════════════════════════════════
//  EOD Market Scanner — Daily Nifty 750 Technical Scan
// ══════════════════════════════════════════════════════════════
//  Runs at 15:45 IST on trading days.
//  Scans the Nifty Total Market (~750 stocks) using existing
//  technical indicators, classifies as BUY/SELL, generates a
//  CSV report, and sends it via Telegram.

// EODScanResult holds the analysis output for a single stock.
type EODScanResult struct {
	Symbol    string
	Company   string
	Signal    string  // "BUY" or "SELL"
	LTP       float64
	MarketCap float64 // from Screener.in cache (Cr), 0 if unavailable
	EMA21     float64
	EMA63     float64
	SMA200    float64
	Volume    float64 // today's volume (from tick store or daily cache)
	AvgVolume float64 // 20-day average
	RSScore   int     // Relative Strength percentile (1-99)
	Pattern   string  // detected pattern name, or ""
	ATR       float64
	High52W   float64
	Low52W    float64
}

// EODScanDeps bundles the dependencies the EOD scanner needs.
// This avoids circular imports between agents ↔ storage.
type EODScanDeps struct {
	// LoadUniverse returns the Nifty 750 token→symbol and token→company maps.
	LoadUniverse func() (map[uint32]string, map[uint32]string)
	// PreloadCache refreshes the daily cache for the given universe.
	PreloadCache func(universe map[uint32]string) bool
	// GetScannerCache returns a fresh *DailyCache snapshot for analysis.
	GetScannerCache func() *DailyCache
	// GetLiveLTP returns the latest tick-store LTP for a token (0 if stale).
	GetLiveLTP func(token uint32) float64
	// GetLiveVolume returns the live cumulative day volume for a token.
	GetLiveVolume func(token uint32) int64
}

// RunEODMarketScan is the top-level function called from main.go at 15:45.
// It loads the Nifty 750 universe, fetches/uses daily cache, runs analysis,
// generates a CSV, and sends the report via Telegram.
func RunEODMarketScan(deps EODScanDeps, scanner *ScannerAgent) {
	startTime := time.Now()
	log.Println("[EODScan] ═══ STARTING EOD MARKET SCAN ═══")
	SendTelegram("🔍 *EOD MARKET SCAN STARTED*\nScanning Nifty Total Market (~750 stocks)...")

	// Step 1: Load the broader universe
	eodUniverse, eodCompanies := deps.LoadUniverse()
	if len(eodUniverse) == 0 {
		log.Println("[EODScan] No universe loaded — aborting scan")
		SendTelegram("❌ *EOD SCAN FAILED*: Could not load stock universe")
		return
	}
	log.Printf("[EODScan] Universe: %d stocks", len(eodUniverse))

	// Step 2: Preload daily cache for the full universe.
	// Tokens already in the cache (from the 250 trading universe) will be refreshed.
	// New tokens (~500 extra) will be fetched fresh.
	log.Println("[EODScan] Preloading daily cache for extended universe...")
	deps.PreloadCache(eodUniverse)
	scanCache := deps.GetScannerCache()

	// Step 3: Run technical analysis on each stock
	var results []EODScanResult
	scanned := 0

	for token, symbol := range eodUniverse {
		scanned++
		company := symbol
		if c, ok := eodCompanies[token]; ok && c != "" {
			company = c
		}

		result := analyzeStock(token, symbol, company, scanCache, deps, scanner)
		if result != nil {
			results = append(results, *result)
		}

		if scanned%100 == 0 {
			log.Printf("[EODScan] Analyzed %d/%d stocks...", scanned, len(eodUniverse))
		}
	}

	// Step 4: Sort results — BUY first (by RS desc), then SELL (by RS asc)
	sort.Slice(results, func(i, j int) bool {
		if results[i].Signal != results[j].Signal {
			return results[i].Signal == "BUY" // BUY comes first
		}
		if results[i].Signal == "BUY" {
			return results[i].RSScore > results[j].RSScore // Higher RS first for BUY
		}
		return results[i].RSScore < results[j].RSScore // Lower RS first for SELL
	})

	buyCount := 0
	sellCount := 0
	for _, r := range results {
		if r.Signal == "BUY" {
			buyCount++
		} else {
			sellCount++
		}
	}

	elapsed := time.Since(startTime)
	log.Printf("[EODScan] Analysis complete: %d scanned, %d BUY, %d SELL (%.1f sec)",
		scanned, buyCount, sellCount, elapsed.Seconds())

	// Step 5: Generate CSV
	csvPath, err := generateEODCSV(results)
	if err != nil {
		log.Printf("[EODScan] CSV generation failed: %v", err)
		SendTelegram(fmt.Sprintf("❌ *EOD SCAN CSV FAILED*: %v", err))
		return
	}

	// Step 6: Build and send Telegram summary
	summary := buildEODSummary(results, scanned, buyCount, sellCount, elapsed)
	SendTelegram(summary)

	// Step 7: Send CSV via Telegram
	caption := fmt.Sprintf("📊 EOD Scan — %s | %d BUY | %d SELL",
		config.NowIST().Format("02 Jan 2006"), buyCount, sellCount)
	SendTelegramDocument(csvPath, caption)

	// Step 8: Cleanup old CSV files
	cleanupOldCSVs()

	log.Printf("[EODScan] ═══ EOD MARKET SCAN COMPLETE (%d results, %.1f sec) ═══",
		len(results), elapsed.Seconds())
}

// ══════════════════════════════════════════════════════════════
//  Stock Analysis — Classify as BUY, SELL, or skip (NEUTRAL)
// ══════════════════════════════════════════════════════════════

func analyzeStock(
	token uint32,
	symbol, company string,
	cache *DailyCache,
	deps EODScanDeps,
	scanner *ScannerAgent,
) *EODScanResult {
	if cache == nil || !cache.Loaded {
		return nil
	}

	closes, cOk := cache.Closes[token]
	if !cOk || len(closes) < 25 {
		return nil
	}

	// ── Compute indicators ──
	lastClose := closes[len(closes)-1]
	if lastClose <= 0 {
		return nil
	}

	// LTP: prefer live tick data, fall back to last daily close
	ltp := lastClose
	if deps.GetLiveLTP != nil {
		liveLTP := deps.GetLiveLTP(token)
		if liveLTP > 0 {
			ltp = liveLTP
		}
	}

	// EMA 21 — computed from closes (cache no longer stores EMA21)
	ema21Val := 0.0
	ema21Slice := data.ComputeEMA(closes, 21)
	if len(ema21Slice) > 0 {
		ema21Val = ema21Slice[len(ema21Slice)-1]
	}

	// EMA 63
	ema63Val := 0.0
	if len(closes) > 63 {
		ema63Slice := data.ComputeEMA(closes, 63)
		if len(ema63Slice) > 0 {
			ema63Val = ema63Slice[len(ema63Slice)-1]
		}
	}

	// SMA 200 — computed from closes (cache no longer stores SMA200; EOD lookback=150 so rarely available)
	sma200Val := 0.0
	if len(closes) >= 200 {
		sum := 0.0
		for _, c := range closes[len(closes)-200:] {
			sum += c
		}
		sma200Val = sum / 200
	}

	// Volume
	volume := 0.0
	avgVolume := 1.0
	volumes, vOk := cache.Volumes[token]
	if vOk && len(volumes) > 0 {
		volume = volumes[len(volumes)-1]
		if len(volumes) >= 20 {
			s := 0.0
			for _, v := range volumes[len(volumes)-20:] {
				s += v
			}
			avgVolume = s / 20.0
		}
	}
	// Override with live volume if available
	if deps.GetLiveVolume != nil {
		liveVol := deps.GetLiveVolume(token)
		if liveVol > 0 {
			volume = float64(liveVol)
		}
	}

	// ATR
	atr := 0.0
	if a, ok := cache.ATR[token]; ok {
		atr = a
	}

	// RS Score
	rsScore := 50
	if rs, ok := cache.RSScore[token]; ok {
		rsScore = rs
	}

	// 52-week High/Low
	high52w := 0.0
	low52w := 0.0
	if h, ok := cache.High52W[token]; ok {
		high52w = h
	}
	if len(closes) >= 252 {
		low52w = closes[len(closes)-252]
		for _, c := range closes[len(closes)-252:] {
			if c < low52w {
				low52w = c
			}
		}
	} else {
		low52w = closes[0]
		for _, c := range closes {
			if c < low52w {
				low52w = c
			}
		}
	}

	// Market Cap from Screener.in cache (if available)
	marketCap := loadMarketCapFromCache(symbol)

	// ── Pattern Detection ──
	pattern := ""
	if scanner != nil && cache.Loaded {
		pattern = detectPatternForEOD(token, symbol, ltp, cache, scanner)
	}

	// ── Classification ──
	signal := classifyStock(ltp, ema21Val, ema63Val, sma200Val, volume, avgVolume,
		rsScore, high52w, low52w, closes, pattern)

	if signal == "" {
		return nil // NEUTRAL — not interesting enough
	}

	return &EODScanResult{
		Symbol:    symbol,
		Company:   company,
		Signal:    signal,
		LTP:       math.Round(ltp*100) / 100,
		MarketCap: marketCap,
		EMA21:     math.Round(ema21Val*100) / 100,
		EMA63:     math.Round(ema63Val*100) / 100,
		SMA200:    math.Round(sma200Val*100) / 100,
		Volume:    volume,
		AvgVolume: math.Round(avgVolume),
		RSScore:   rsScore,
		Pattern:   pattern,
		ATR:       atr,
		High52W:   high52w,
		Low52W:    math.Round(low52w*100) / 100,
	}
}

// classifyStock determines BUY, SELL, or "" (neutral) based on technical indicators.
func classifyStock(
	ltp, ema21, ema63, sma200, volume, avgVolume float64,
	rsScore int,
	high52w, low52w float64,
	closes []float64,
	pattern string,
) string {
	buyScore := 0
	sellScore := 0

	// ── BUY criteria ──
	// 1. Price above 21 EMA (trend following)
	if ema21 > 0 && ltp > ema21 {
		buyScore++
	}
	// 2. Price above 200 SMA (long-term uptrend)
	if sma200 > 0 && ltp > sma200 {
		buyScore++
	}
	// 3. Strong RS Score (top 30% of market)
	if rsScore >= 70 {
		buyScore++
	}
	// 4. Near all-time/52-week high (within 5%)
	if high52w > 0 {
		distFromHigh := ((high52w - ltp) / high52w) * 100
		if distFromHigh <= 5.0 {
			buyScore++
		}
	}
	// 5. Volume above average (institutional interest)
	if avgVolume > 0 && volume >= avgVolume*1.0 {
		buyScore++
	}
	// 6. Technical pattern detected
	if pattern != "" {
		buyScore += 2 // Strong signal boost
	}
	// 7. EMA 21 above EMA 63 (golden cross equivalent)
	if ema21 > 0 && ema63 > 0 && ema21 > ema63 {
		buyScore++
	}

	// ── SELL criteria ──
	// 1. Price below 21 EMA
	if ema21 > 0 && ltp < ema21 {
		sellScore++
	}
	// 2. Price below 200 SMA (long-term downtrend)
	if sma200 > 0 && ltp < sma200 {
		sellScore++
	}
	// 3. Weak RS Score (bottom 30%)
	if rsScore <= 30 {
		sellScore++
	}
	// 4. Near 52-week low (within 10%)
	if low52w > 0 && ltp > 0 {
		distFromLow := ((ltp - low52w) / low52w) * 100
		if distFromLow <= 10.0 {
			sellScore++
		}
	}
	// 5. Volume declining (lack of interest)
	if avgVolume > 0 && volume < avgVolume*0.7 {
		sellScore++
	}
	// 6. Two recent red candles below EMA
	if len(closes) >= 3 && ema21 > 0 {
		c1 := closes[len(closes)-1]
		c2 := closes[len(closes)-2]
		c3 := closes[len(closes)-3]
		if c1 < c2 && c2 < c3 && c1 < ema21 && c2 < ema21 {
			sellScore += 2
		}
	}
	// 7. EMA 21 below EMA 63 (death cross)
	if ema21 > 0 && ema63 > 0 && ema21 < ema63 {
		sellScore++
	}

	// ── Decision ──
	// Require a minimum score to classify (avoid noise)
	if buyScore >= 4 && buyScore > sellScore {
		return "BUY"
	}
	if sellScore >= 4 && sellScore > buyScore {
		return "SELL"
	}
	return "" // NEUTRAL — not a strong signal
}

// detectPatternForEOD checks for known patterns without generating trade signals.
// Returns the pattern name or "".
func detectPatternForEOD(token uint32, symbol string, ltp float64, cache *DailyCache, scanner *ScannerAgent) string {
	// Temporarily create a minimal scanner context for pattern detection
	tempScanner := &ScannerAgent{
		DailyCache:        cache,
		CapitalMultiplier: 1.0,
		IPOSymbols:        scanner.IPOSymbols,
		IsMajorEventDay:   false, // Don't suppress for EOD scan
		GetVolume:         scanner.GetVolume,
		GetLTP:            scanner.GetLTP,
	}

	if sig := tempScanner.detectVCPBreakout(token, symbol, ltp, "NORMAL"); sig != nil {
		return "VCP_BREAKOUT"
	}
	if sig := tempScanner.detectBullFlag(token, symbol, ltp, "NORMAL"); sig != nil {
		return "BULL_FLAG"
	}
	if sig := tempScanner.detectCupWithHandle(token, symbol, ltp, "NORMAL"); sig != nil {
		return "CUP_HANDLE"
	}
	if sig := tempScanner.detectTrendChannel(token, symbol, ltp, "NORMAL"); sig != nil {
		return "TREND_CHANNEL"
	}
	if sig := tempScanner.detectIPOBaseBreakout(token, symbol, ltp, "NORMAL"); sig != nil {
		return "IPO_BASE"
	}
	return ""
}

// loadMarketCapFromCache reads market cap from the screener.in JSON cache on disk.
func loadMarketCapFromCache(symbol string) float64 {
	cachePath := filepath.Join(".", "data", "screener_cache", strings.ToUpper(symbol)+".json")
	fileData, err := os.ReadFile(cachePath)
	if err != nil {
		return 0
	}
	// Quick parse — just look for "market_cap":NUMBER
	s := string(fileData)
	key := `"market_cap":`
	idx := strings.Index(s, key)
	if idx < 0 {
		return 0
	}
	numStart := idx + len(key)
	numEnd := numStart
	for numEnd < len(s) && (s[numEnd] == '.' || (s[numEnd] >= '0' && s[numEnd] <= '9')) {
		numEnd++
	}
	if numEnd == numStart {
		return 0
	}
	var mcap float64
	fmt.Sscanf(s[numStart:numEnd], "%f", &mcap)
	return mcap
}

// ══════════════════════════════════════════════════════════════
//  CSV Generation
// ══════════════════════════════════════════════════════════════

func generateEODCSV(results []EODScanResult) (string, error) {
	dateStr := config.NowIST().Format("2006-01-02")
	csvDir := filepath.Join(config.BaseDir, "data")
	os.MkdirAll(csvDir, 0755)
	csvPath := filepath.Join(csvDir, fmt.Sprintf("eod_scan_%s.csv", dateStr))

	file, err := os.Create(csvPath)
	if err != nil {
		return "", fmt.Errorf("create CSV: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Header
	header := []string{
		"Signal", "Symbol", "Company", "LTP", "MarketCap(Cr)",
		"EMA21", "EMA63", "SMA200", "Volume", "AvgVolume",
		"RS Score", "Pattern", "ATR", "52W High", "52W Low",
	}
	writer.Write(header)

	// Data rows
	for _, r := range results {
		row := []string{
			r.Signal,
			r.Symbol,
			r.Company,
			fmt.Sprintf("%.2f", r.LTP),
			fmt.Sprintf("%.0f", r.MarketCap),
			fmt.Sprintf("%.2f", r.EMA21),
			fmt.Sprintf("%.2f", r.EMA63),
			fmt.Sprintf("%.2f", r.SMA200),
			fmt.Sprintf("%.0f", r.Volume),
			fmt.Sprintf("%.0f", r.AvgVolume),
			fmt.Sprintf("%d", r.RSScore),
			r.Pattern,
			fmt.Sprintf("%.2f", r.ATR),
			fmt.Sprintf("%.2f", r.High52W),
			fmt.Sprintf("%.2f", r.Low52W),
		}
		writer.Write(row)
	}

	log.Printf("[EODScan] CSV written: %s (%d rows)", csvPath, len(results))
	return csvPath, nil
}

// ══════════════════════════════════════════════════════════════
//  Telegram Summary Builder
// ══════════════════════════════════════════════════════════════

func buildEODSummary(results []EODScanResult, scanned, buyCount, sellCount int, elapsed time.Duration) string {
	dateStr := config.NowIST().Format("02 Jan 2026")
	sep := "━━━━━━━━━━━━━━━━━━━━━━━━"

	msg := fmt.Sprintf(
		"📊 *EOD MARKET SCAN — %s*\n%s\n🔎 Scanned: `%d` stocks\n🟢 BUY: `%d` | 🔴 SELL: `%d`\n⏱ Time: `%.0f sec`\n",
		dateStr, sep, scanned, buyCount, sellCount, elapsed.Seconds())

	// Top 10 BUY picks
	buyPicks := 0
	for _, r := range results {
		if r.Signal == "BUY" && buyPicks < 10 {
			if buyPicks == 0 {
				msg += fmt.Sprintf("\n🟢 *TOP BUY PICKS*\n")
			}
			buyPicks++
			patternTag := ""
			if r.Pattern != "" {
				patternTag = fmt.Sprintf(" | `%s`", r.Pattern)
			}
			msg += fmt.Sprintf("`%d.` `%s` — ₹`%.0f` | RS:`%d`%s\n",
				buyPicks, r.Symbol, r.LTP, r.RSScore, patternTag)
		}
	}

	// Top 10 SELL picks
	sellPicks := 0
	for _, r := range results {
		if r.Signal == "SELL" && sellPicks < 10 {
			if sellPicks == 0 {
				msg += fmt.Sprintf("\n🔴 *TOP SELL PICKS*\n")
			}
			sellPicks++
			msg += fmt.Sprintf("`%d.` `%s` — ₹`%.0f` | RS:`%d`\n",
				sellPicks, r.Symbol, r.LTP, r.RSScore)
		}
	}

	msg += fmt.Sprintf("\n📎 _Full CSV report attached below._")
	return msg
}

// ══════════════════════════════════════════════════════════════
//  CSV Cleanup — Remove files older than EODScanCleanupDays
// ══════════════════════════════════════════════════════════════

func cleanupOldCSVs() {
	csvDir := filepath.Join(config.BaseDir, "data")
	entries, err := os.ReadDir(csvDir)
	if err != nil {
		return
	}

	cutoff := time.Now().AddDate(0, 0, -config.EODScanCleanupDays)
	removed := 0

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "eod_scan_") || !strings.HasSuffix(entry.Name(), ".csv") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			fullPath := filepath.Join(csvDir, entry.Name())
			if os.Remove(fullPath) == nil {
				removed++
			}
		}
	}

	if removed > 0 {
		log.Printf("[EODScan] Cleaned up %d old CSV files (>%d days)", removed, config.EODScanCleanupDays)
	}
}
