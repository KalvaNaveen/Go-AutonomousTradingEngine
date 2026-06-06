package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"bnf_go_engine/agents"
	"bnf_go_engine/config"
	"bnf_go_engine/core"
	"bnf_go_engine/research"
	"bnf_go_engine/storage"

	"github.com/joho/godotenv"
)

func main() {
	core.InitGlobalLogger()
	// ══════════════════════════════════════════════════════════════
	//  PHASE 0: Environment Setup
	// ══════════════════════════════════════════════════════════════
	envPaths := []string{"./.env"}
	if len(os.Args) > 1 {
		envPaths = append([]string{os.Args[1] + "/.env"}, envPaths...)
	}
	bnfRoot := os.Getenv("ENGINE_ROOT")
	if bnfRoot != "" {
		envPaths = append([]string{bnfRoot + "/.env"}, envPaths...)
	}
	for _, p := range envPaths {
		if err := godotenv.Load(p); err == nil {
			log.Printf("[Engine] Loaded .env from %s", p)
			break
		}
	}

	config.Reload()
	// Apply any saved dashboard overrides (data/config_override.json).
	// This wires "Apply Config" into the live EMA agent from the very first scan.
	config.LoadOverride(config.BaseDir + string(os.PathSeparator) + "data" + string(os.PathSeparator) + "config_override.json")
	config.PrintBanner()

	// ══════════════════════════════════════════════════════════════
	//  Auto-Login (Token Refresh)
	// ══════════════════════════════════════════════════════════════
	if config.ZerodhaUserID != "" && config.ZerodhaTOTPSecret != "" {
		needsLogin := false
		if config.KiteAccessToken == "" {
			needsLogin = true
		} else {
			client := &http.Client{Timeout: 10 * time.Second}
			req, _ := http.NewRequest("GET", "https://api.kite.trade/user/profile", nil)
			req.Header.Set("X-Kite-Version", "3")
			req.Header.Set("Authorization", fmt.Sprintf("token %s:%s", config.KiteAPIKey, config.KiteAccessToken))
			resp, err := client.Do(req)
			if err != nil || resp.StatusCode != 200 {
				needsLogin = true
				if resp != nil {
					resp.Body.Close()
				}
			} else {
				resp.Body.Close()
				log.Println("[Engine] Access token is VALID ✅")
			}
		}
		if needsLogin {
			login := core.NewAutoLogin()
			if login.Run() {
				agents.SendTelegram("✅ *AUTO LOGIN SUCCESS*")
			}
		}
	}

	// ══════════════════════════════════════════════════════════════
	//  Initialize Core Systems
	// ══════════════════════════════════════════════════════════════
	scanner := agents.NewScannerAgent()
	signalAgent := agents.NewSignalAlertAgent()
	signalAgent.Scanner = scanner

	waitForNetwork()

	// ══════════════════════════════════════════════════════════════
	//  Load Universe & Preload Daily Cache
	// ══════════════════════════════════════════════════════════════
	dataAgent := agents.NewDataAgent()
	if err := dataAgent.LoadUniverse(); err != nil {
		log.Printf("[Engine] WARNING: Universe load failed: %v", err)
	}
	if len(dataAgent.Universe) == 0 {
		dataAgent.Universe = map[uint32]string{
			738561:  "RELIANCE",
			2953217: "TCS",
			341249:  "HDFCBANK",
		}
	}

	// Section II: Add index + ETF tokens for macro timing & gold ratio
	dataAgent.Universe[config.NiftySpotToken] = "NIFTY 50"         // ROC regime + Gold ratio
	dataAgent.Universe[config.SmallcapToken] = "NIFTY SMLCAP 100"  // Smallcap ROC
	dataAgent.Universe[research.GOLDBEESToken] = "GOLDBEES"         // Gold ratio

	scanner.Universe = dataAgent.Universe
	scanner.TokenToCompany = dataAgent.TokenToCompany

	// FIX-12: Startup token count guard
	// Ensure benchmark tokens are NOT in pattern scan queue (scanner.Universe)
	benchmarkTokens := []uint32{config.IndiaVIXToken, config.BankNiftySpotToken}
	for name, tok := range config.SectorTokens {
		benchmarkTokens = append(benchmarkTokens, tok)
		_ = name
	}
	for _, bToken := range benchmarkTokens {
		if _, inUniverse := scanner.Universe[bToken]; inUniverse {
			// Benchmark tokens should only be subscribed via WebSocket for live data,
			// not scanned for patterns (they have no OHLCV history in cache)
			log.Printf("[Engine] WARNING: Benchmark token %d found in scan universe — removing", bToken)
			delete(scanner.Universe, bToken)
		}
	}
	log.Printf("[Engine] Token count: %d equity (scan) + %d benchmark (monitor only)",
		len(scanner.Universe), len(benchmarkTokens))

	// ══════════════════════════════════════════════════════════════
	//  TickStore + WebSocket (for live SL monitoring & Auth Check)
	// ══════════════════════════════════════════════════════════════
	tickStore := storage.NewTickStore()

	scanner.GetLTP = func(token uint32) float64 { return tickStore.GetLTPIfFresh(token) }
	scanner.GetVWAP = func(token uint32) float64 { return tickStore.GetVWAP(token) }
	scanner.GetVolume = func(token uint32) int64 { return tickStore.GetVolume(token) }
	scanner.GetDepth = func(token uint32) map[string]float64 { return tickStore.GetDepth(token) }
	scanner.GetCandles5m = func(token uint32) []agents.Candle { return tickStore.GetCandles5Min(token) }
	scanner.GetORB = func(token uint32) (float64, float64) { return tickStore.GetORB(token) }
	scanner.GetDayOpen = func(token uint32) float64 { return tickStore.GetDayOpen(token) }
	scanner.ComputeRVol = func(token uint32) float64 { return 1.0 }
	scanner.GetADRatio = scanner.ComputeADRatio

	// (OI filter removed — F&O open interest is not in the book; the engine trades
	//  cash equity on price/volume action only.)

	signalAgent.GetLTP = scanner.GetLTP

	// Load NSE holidays synchronously NOW so IsNonTradingPeriod() has data
	// before the WebSocket connect decision below. The daily refresh goroutine
	// is started later (after full init) to keep startup ordering clean.
	refreshNSEHolidays()

	allTokens := dataAgent.GetAllTokens()
	ws := storage.NewKiteWebSocket(tickStore, allTokens)
	// Wire holiday awareness into the WebSocket so its reconnect loop knows
	// not to retry on holidays, weekends, or outside market hours.
	ws.IsNonTradingPeriod = func() bool {
		now := config.NowIST()
		if now.Weekday() == time.Saturday || now.Weekday() == time.Sunday {
			return true
		}
		if isNSEHoliday(now) {
			return true
		}
		hhmm := now.Hour()*100 + now.Minute()
		return hhmm < 900 || hhmm >= 1545
	}

	if config.KiteAPIKey != "" && !ws.IsNonTradingPeriod() {
		err := ws.Connect()
		if err != nil {
			log.Printf("[Engine] WebSocket connect failed with current token: %v. Triggering AutoLogin...", err)
			login := core.NewAutoLogin()
			if login.Run() {
				ws.UpdateToken(config.KiteAccessToken)
				if err2 := ws.Connect(); err2 != nil {
					log.Printf("[Engine] WebSocket connect failed even after AutoLogin: %v", err2)
				} else {
					log.Printf("[Engine] WebSocket connected after AutoLogin. Subscribed %d tokens", len(allTokens))
				}
			} else {
				log.Println("[Engine] AutoLogin failed. WebSocket is offline.")
			}
		} else {
			log.Printf("[Engine] WebSocket connected. Subscribed %d tokens", len(allTokens))
		}
	} else if config.KiteAPIKey != "" {
		log.Println("[Engine] Non-trading day/time — skipping WebSocket connect at startup")
	}

	// Now that token is potentially refreshed, preload Cache.
	// Include sector index tokens (NIFTY BANK, IT, AUTO…) so that Bird's Eye
	// sector strength has data. They are NOT added to scanner.Universe (no pattern
	// scan on indices) — just loaded into the cache for breadth calculations.
	dailyCache := storage.NewDailyCache()
	log.Println("[Engine] Preloading daily cache (500d historical for ROC)...")
	preloadUniverse := make(map[uint32]string, len(dataAgent.Universe)+len(config.SectorTokens))
	for t, s := range dataAgent.Universe {
		preloadUniverse[t] = s
	}
	for name, tok := range config.SectorTokens {
		preloadUniverse[tok] = name
	}
	dailyCache.Preload(preloadUniverse)
	scanner.DailyCache = dailyCache.ToScannerCache()

	// getKiteLTP fetches fresh LTP from Kite Quote API — used by both the bot and EOD scan.
	// Falls back to tick store when WS is live (market hours), Kite Quote API otherwise.
	var kiteQuoteCache map[uint32]float64
	var kiteQuoteMu sync.Mutex
	getKiteLTP := func(token uint32) float64 {
		// During market hours prefer the live WebSocket tick (zero-latency)
		if ltp := tickStore.GetLTPIfFresh(token); ltp > 0 {
			return ltp
		}
		// Otherwise use the most recently fetched Kite Quote batch
		kiteQuoteMu.Lock()
		ltp := kiteQuoteCache[token]
		kiteQuoteMu.Unlock()
		return ltp
	}
	// getLTPSource returns "live" when a fresh WebSocket tick exists, "quote" otherwise.
	getLTPSource := func(token uint32) string {
		if ltp := tickStore.GetLTPIfFresh(token); ltp > 0 {
			return "live"
		}
		return "quote"
	}
	refreshKiteQuotes := func() {
		tokens := dataAgent.GetAllTokens()
		quotes := dataAgent.FetchKiteQuotes(tokens)
		kiteQuoteMu.Lock()
		kiteQuoteCache = quotes
		kiteQuoteMu.Unlock()
	}

	// Wire RefreshCache: reload historical bars + fetch fresh LTP from Kite Quote API
	scanner.RefreshCache = func() {
		log.Println("[Bot] Refreshing cache + Kite quotes before scan...")
		dailyCache.Preload(preloadUniverse)
		scanner.DailyCache = dailyCache.ToScannerCache()
		refreshKiteQuotes()
		log.Println("[Bot] Cache + quotes refreshed ✅")
	}
	scanner.GetLTP = getKiteLTP

	// Kronos AI ranker — optional, gracefully skipped when service is offline
	kronosURL := os.Getenv("KRONOS_SERVICE_URL")
	if kronosURL == "" {
		kronosURL = "http://localhost:8765"
	}
	kronosClient := agents.NewKronosClient(kronosURL)
	if kronosClient.IsAlive() {
		log.Println("[Engine] Kronos service online ✅ — AI ranking enabled")
		agents.SendTelegram("🤖 *Kronos AI ranker online* — signals will include predicted 5-day upside")
	} else {
		log.Println("[Engine] Kronos service offline — AI ranking disabled (rule-based order used)")
	}
	scanner.Kronos = kronosClient

	// ══════════════════════════════════════════════════════════════
	//  Research Automation (Sections II-IV of Blueprint)
	// ══════════════════════════════════════════════════════════════

	// (Fundamental/Screener.in filter removed — book is purely technical/price-action.)

	// Section II.2: Nifty/Gold ratio (using GOLDBEES from Kite, not TradingView)
	go func() {
		niftyCloses, nOk := scanner.DailyCache.Closes[config.NiftySpotToken]
		goldCloses, gOk := scanner.DailyCache.Closes[research.GOLDBEESToken]
		log.Printf("[GoldRatio] Nifty token=%d has %d closes (ok=%v), GOLDBEES token=%d has %d closes (ok=%v)",
			config.NiftySpotToken, len(niftyCloses), nOk,
			research.GOLDBEESToken, len(goldCloses), gOk)
		if nOk && gOk && len(niftyCloses) > 0 && len(goldCloses) > 0 {
			result := research.ComputeNiftyGoldRatio(niftyCloses, goldCloses)
			if result != nil {
				log.Printf("[GoldRatio] ✅ Ratio=%.2f Percentile=%.1f%% Signal=%s",
					result.CurrentRatio, result.Percentile, result.Signal)
				agents.SendTelegram(fmt.Sprintf(
					"📊 *NIFTY/GOLD RATIO*\nRatio: `%.2f` | Range: `%.2f - %.2f`\nPercentile: `%.1f%%` | Signal: `%s`",
					result.CurrentRatio, result.Low252, result.High252,
					result.Percentile, result.Signal))
			}
		} else {
			log.Printf("[GoldRatio] ⚠️ Missing data — Nifty closes=%d, GOLDBEES closes=%d", len(niftyCloses), len(goldCloses))
		}
	}()

	// Section IV: Chittorgarh IPO scraper
	// FIX-08: Wire IPO list into scanner so IPO Base pattern only applies to actual IPOs
	go func() {
		log.Println("[Research] Scraping Chittorgarh for recent IPOs...")
		allIPOs, err := research.FetchRecentIPOs()
		if err != nil {
			log.Printf("[Research] IPO scrape failed: %v", err)
			return
		}
		recent := research.FilterRecentIPOs(allIPOs, 90)

		// Build IPO symbol set for scanner pattern gating
		ipoSet := make(map[string]bool)
		for _, ipo := range recent {
			if ipo.Symbol != "" {
				ipoSet[ipo.Symbol] = true
			}
		}
		scanner.IPOSymbols = ipoSet
		log.Printf("[Research] IPO symbols loaded for pattern gating: %d stocks", len(ipoSet))

		if len(recent) > 0 {
			msg := "🆕 *RECENT IPOs (90 days)*\n"
			for _, ipo := range recent {
				msg += fmt.Sprintf("• `%s` (%s) — %s\n", ipo.CompanyName, ipo.Symbol, ipo.ListingDate)
			}
			agents.SendTelegram(msg)
		}
	}()

	// Section V.2: Event calendar — check if today is a major event day
	isMajor, eventName := research.IsMajorEventDay()
	scanner.IsMajorEventDay = isMajor
	scanner.MajorEventName = eventName
	if isMajor {
		log.Printf("[Research] ⚠️ MAJOR EVENT DAY: %s — Bull Flag signals suppressed", eventName)
		agents.SendTelegram(fmt.Sprintf("⚠️ *MAJOR EVENT*: `%s`\nBull Flag entries suppressed today.", eventName))
	}


	// ══════════════════════════════════════════════════════════════
	//  Scheduled Daily Token Refresh (8:30 AM)
	// ══════════════════════════════════════════════════════════════
	go func() {
		for {
			now := config.NowIST()
			if now.Hour() == 8 && now.Minute() == 30 {
				login := core.NewAutoLogin()
				if login.Run() && ws != nil {
					ws.UpdateToken(config.KiteAccessToken)
					ws.Close()
				}
				time.Sleep(61 * time.Second)
			}
			time.Sleep(20 * time.Second)
		}
	}()

	// ══════════════════════════════════════════════════════════════
	// Refresh NSE holiday list once daily at 06:00 IST.
	go func() {
		for {
			now := config.NowIST()
			next := time.Date(now.Year(), now.Month(), now.Day(), 6, 0, 0, 0, now.Location())
			if !next.After(now) {
				next = next.AddDate(0, 0, 1)
			}
			time.Sleep(time.Until(next))
			refreshNSEHolidays()
		}
	}()

	agents.SendTelegram(fmt.Sprintf(
		"🚀 *ZENITH SIGNAL ENGINE — Online*\n"+
			"Universe: `%d stocks`\n"+
			"Strategy: `EMA Pullback Bounce`\n"+
			"SL: `%.1f%%–%.1f%%` structural\n"+
			"📡 Type *scan* to scan market anytime\n"+
			"⏰ EOD alerts auto-sent at 16:00 IST",
		len(scanner.Universe),
		config.SLFloorPct, config.SLCeilingPct))

	// Start Telegram bot — listens for manual "scan" commands from your chat
	agents.StartTelegramBot(scanner, signalAgent)

	log.Println("[Engine] ✅ Initialized. Telegram bot active. EOD alerts at 16:00 IST.")

	// ══════════════════════════════════════════════════════════════
	//  MAIN LOOP — Signal-only, EOD alerts at 15:31
	// ══════════════════════════════════════════════════════════════

	for {
		today := config.NowIST()

		if today.Weekday() == time.Saturday || today.Weekday() == time.Sunday {
			sleepUntilMorning()
			continue
		}
		if isNSEHoliday(today) {
			log.Println("[Engine] NSE Holiday — sleeping")
			sleepUntilMorning()
			continue
		}

		log.Printf("[Engine] ═══ TRADING DAY: %s ═══", today.Format("2006-01-02"))
		scanner.NewSession()
		tickStore.ResetDaily()
		alertsDone := false

		ticker := time.NewTicker(30 * time.Second)
	dayLoop:
		for range ticker.C {
			now := config.NowIST()
			t := now.Hour()*100 + now.Minute()

			// ── EOD run at 16:00 (market close + 30 min, candle fully finalized) ──
			if t >= 1600 && !alertsDone {
				log.Println("[Engine] ═══ EOD RUN (16:00) ═══")

				dailyCache.Preload(dataAgent.Universe)
				freshCache := dailyCache.ToScannerCache()
				scanner.DailyCache = freshCache
				refreshKiteQuotes()

				// SELL — stocks that crossed below EMA10 (open positions only)
				signalAgent.RunEODSellAlerts(scanner.Universe)

				// Full EOD market scan (BUY breadth, Trigger Candles, MOMO leaders, CSV)
				go agents.RunEODMarketScan(agents.EODScanDeps{
					LoadUniverse:    dataAgent.LoadEODScanUniverse,
					PreloadCache:    dailyCache.Preload,
					GetScannerCache: func() *agents.DailyCache { return dailyCache.ToScannerCache() },
					GetLiveLTP:      getKiteLTP,
					GetLTPSource:    getLTPSource,
					GetLiveVolume:   func(token uint32) int64 { return tickStore.GetVolume(token) },
					Kronos:          kronosClient,
					SignalAgent:     signalAgent,
				}, scanner)

				alertsDone = true
				log.Println("[Engine] ═══ EOD RUN COMPLETE ═══")
			}

			if t >= 1605 {
				ticker.Stop()
				break dayLoop
			}
		}

		agents.SendTelegram("🌙 *ENGINE SLEEPING* — Next alerts tomorrow at 16:00.")
		sleepUntilMorning()
		now := config.NowIST()
		log.Printf("[Engine] ═══ WAKING UP — %s ═══", now.Format("2006-01-02 15:04"))

		// ── Daily Kite token refresh ──────────────────────────────────────
		// Kite access tokens expire every day at midnight IST.
		// Re-validate and auto-login before the trading day starts.
		tokenOK := false
		if config.KiteAccessToken != "" {
			client := &http.Client{Timeout: 10 * time.Second}
			req, _ := http.NewRequest("GET", "https://api.kite.trade/user/profile", nil)
			req.Header.Set("X-Kite-Version", "3")
			req.Header.Set("Authorization", fmt.Sprintf("token %s:%s", config.KiteAPIKey, config.KiteAccessToken))
			if resp, err := client.Do(req); err == nil {
				tokenOK = resp.StatusCode == 200
				resp.Body.Close()
			}
		}
		if !tokenOK {
			log.Println("[Engine] Kite token expired — running AutoLogin...")
			agents.SendTelegram("🔑 *Kite token expired* — auto-refreshing...")
			if config.ZerodhaUserID != "" && config.ZerodhaTOTPSecret != "" {
				login := core.NewAutoLogin()
				if login.Run() {
					log.Println("[Engine] AutoLogin SUCCESS — token refreshed")
					agents.SendTelegram("✅ *Kite token refreshed* — engine ready for trading day")
					if ws != nil {
						ws.UpdateToken(config.KiteAccessToken)
					}
				} else {
					log.Println("[Engine] AutoLogin FAILED — manual token entry required")
					agents.SendTelegram("🚨 *Kite AutoLogin FAILED*\nPlease update `KITE_ACCESS_TOKEN` in `.env` and restart the engine.")
				}
			} else {
				agents.SendTelegram("🚨 *Kite token expired* — TOTP not configured. Please update `KITE_ACCESS_TOKEN` in `.env` and restart.")
			}
		} else {
			log.Println("[Engine] Kite token valid ✅")
		}

		// Heartbeat — confirms engine is alive every morning
		kronosStatus := "offline"
		if kronosClient != nil && kronosClient.IsAlive() {
			kronosStatus = "online"
		}
		tokenStatus := "✅ valid"
		if !tokenOK {
			tokenStatus = "⚠️ refreshed"
		}
		agents.SendTelegram(fmt.Sprintf(
			"☀️ *ZENITH ENGINE — Good Morning*\n"+
				"Date: `%s`\n"+
				"Universe: `%d stocks`\n"+
				"Kite token: `%s`\n"+
				"Kronos AI: `%s`\n"+
				"EOD scan scheduled at *16:00 IST*",
			now.Format("Mon, 02 Jan 2006"),
			len(scanner.Universe),
			tokenStatus,
			kronosStatus,
		))
	}
}

func sleepUntilMorning() {
	now := config.NowIST()
	tomorrow := now.AddDate(0, 0, 1)
	for tomorrow.Weekday() == time.Saturday || tomorrow.Weekday() == time.Sunday {
		tomorrow = tomorrow.AddDate(0, 0, 1)
	}
	wake := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 8, 25, 0, 0, config.IST)
	sleepDur := wake.Sub(now)
	if sleepDur > 0 {
		log.Printf("[Engine] Sleeping %.1f hours until %s", sleepDur.Hours(), wake.Format("2006-01-02 15:04"))
		time.Sleep(sleepDur)
	}
}

func waitForNetwork() {
	client := &http.Client{Timeout: 3 * time.Second}
	for i := 0; i < 5; i++ {
		resp, err := client.Get("https://api.kite.trade")
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(3 * time.Second)
	}
}

// NSE holidays — YYYY-MM-DD keys, loaded from NSE API and refreshed daily at 06:00.
var (
	nseHolidaysMu  sync.RWMutex
	nseHolidaysMap = map[string]string{} // YYYY-MM-DD → description
)

// Static fallback covering 2026 + 2027 (used when NSE API is unreachable).
var nseHolidaysFallback = map[string]bool{
	// 2026
	"2026-01-26": true, "2026-02-17": true, "2026-03-10": true,
	"2026-03-30": true, "2026-04-02": true, "2026-04-03": true,
	"2026-04-14": true, "2026-05-01": true, "2026-07-06": true,
	"2026-08-15": true, "2026-08-18": true, "2026-09-04": true,
	"2026-10-02": true, "2026-10-20": true, "2026-10-21": true,
	"2026-11-09": true, "2026-11-10": true, "2026-11-24": true,
	"2026-12-25": true,
	// 2027 — approximate; API fetch will override these
	"2027-01-26": true, "2027-03-29": true, "2027-04-02": true,
	"2027-04-14": true, "2027-05-01": true, "2027-08-15": true,
	"2027-10-02": true, "2027-10-19": true, "2027-11-12": true,
	"2027-12-25": true,
}

func refreshNSEHolidays() {
	fetched := research.FetchNSEHolidays() // now returns YYYY-MM-DD keys
	if len(fetched) == 0 {
		log.Printf("[Engine] NSE holiday fetch returned empty — keeping existing/fallback list")
		return
	}
	nseHolidaysMu.Lock()
	// Merge into map (keeps prior-year data across year-end)
	for k, v := range fetched {
		nseHolidaysMap[k] = v
	}
	nseHolidaysMu.Unlock()
	log.Printf("[Engine] NSE holidays refreshed: %d entries", len(fetched))
}

func isNSEHoliday(t time.Time) bool {
	dateStr := t.Format("2006-01-02")
	nseHolidaysMu.RLock()
	_, fromAPI := nseHolidaysMap[dateStr]
	nseHolidaysMu.RUnlock()
	if fromAPI {
		return true
	}
	return nseHolidaysFallback[dateStr]
}

