package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"bnf_go_engine/agents"
	"bnf_go_engine/api"
	"bnf_go_engine/broker"
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
	stateManager := core.NewStateManager()
	journal := core.NewJournal()
	scanner := agents.NewScannerAgent()
	execAgent := agents.NewExecutionAgent(journal, stateManager)
	execAgent.Scanner = scanner
	fillMonitor := core.NewFillMonitor(stateManager)

	waitForNetwork()

	// ══════════════════════════════════════════════════════════════
	//  Broker (Paper or Live)
	// ══════════════════════════════════════════════════════════════
	var paperBroker *broker.RealisticPaperBroker
	if config.PaperMode {
		paperBroker = broker.NewRealisticPaperBroker(config.TotalCapital)
		defer paperBroker.Stop()
		execAgent.PlaceOrder = paperBroker.PlaceOrder
		execAgent.CancelOrder = paperBroker.CancelOrder
		log.Println("[Engine] Paper Broker enabled (CNC swing mode)")
	} else {
		kb := broker.NewKiteBroker()
		gttClient := broker.NewGTTClient()
		execAgent.PlaceOrder = kb.PlaceOrder
		execAgent.CancelOrder = kb.CancelOrder
		execAgent.CancelGTT = gttClient.CancelGTT
		// Wire FillMonitor REST hooks so it can poll order status & modify SL qty.
		fillMonitor.GetOrderStatus = func(orderID string) (*core.OrderStatus, error) {
			status, filled, pending, avg, err := kb.GetOrderStatus(orderID)
			if err != nil {
				return nil, err
			}
			return &core.OrderStatus{Status: status, FilledQty: filled, PendingQty: pending, AveragePrice: avg}, nil
		}
		fillMonitor.ModifyOrder = kb.ModifyOrder
		// Wire GTT SL placement — persists overnight, survives bot restarts
		fillMonitor.PlaceGTT = gttClient.PlaceSLGTT
		fillMonitor.CancelGTT = gttClient.CancelGTT
		log.Println("[Engine] LIVE broker connected (Kite + GTT SL)")
	}

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

	execAgent.GetLTP = scanner.GetLTP

	allTokens := dataAgent.GetAllTokens()
	ws := storage.NewKiteWebSocket(tickStore, allTokens)
	if config.KiteAPIKey != "" {
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
	}

	// Now that token is potentially refreshed, preload Cache
	dailyCache := storage.NewDailyCache()
	log.Println("[Engine] Preloading daily cache (500d historical for ROC)...")
	dailyCache.Preload(dataAgent.Universe)
	scanner.DailyCache = dailyCache.ToScannerCache()

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
	//  Crash Recovery — Restore Swing Positions
	// ══════════════════════════════════════════════════════════════
	execAgent.RestoreFromState()

	symbolToToken := make(map[string]uint32)
	for token, sym := range dataAgent.Universe {
		symbolToToken[sym] = token
	}

	// Backfill missing tokens to restored trades AND persist the fix to DB.
	// Without the Save() call, every restart would re-load token=0 from SQLite,
	// causing MonitorPositions to skip the position (LTP returns 0 → "continue").
	execAgent.Mu.Lock()
	for _, trade := range execAgent.ActiveTrades {
		if trade.Token == 0 {
			if matchedTok, exists := symbolToToken[trade.Symbol]; exists {
				trade.Token = matchedTok
				stateManager.Save(trade.EntryOID, trade) // Persist fix to DB
				log.Printf("[Engine] Backfilled token for %s: %d (was 0)", trade.Symbol, matchedTok)
			} else {
				log.Printf("[Engine] ⚠️ Cannot backfill token for %s — symbol not in universe", trade.Symbol)
			}
		}
	}
	execAgent.Mu.Unlock()

	if paperBroker != nil {
		paperBroker.GetLTP = func(symbol string) float64 {
			if tok, ok := symbolToToken[symbol]; ok {
				return tickStore.GetLTPIfFresh(tok)
			}
			return 0
		}
		paperBroker.GetDepth = func(symbol string) (float64, float64) { return 0, 0 }
	}

	// ══════════════════════════════════════════════════════════════
	//  API Server (Dashboard Backend)
	// ══════════════════════════════════════════════════════════════
	const dashboardPort = "8085"
	killOldProcess(dashboardPort)
	apiServer := api.NewServer(journal, execAgent, scanner, tickStore, dailyCache)
	go apiServer.Start(":" + dashboardPort)
	log.Printf("[Engine] Dashboard: http://127.0.0.1:%s", dashboardPort)

	if !config.PaperMode {
		fillMonitor.PlaceOrder = execAgent.PlaceOrder
		fillMonitor.CancelOrder = execAgent.CancelOrder
		fillMonitor.AlertFn = func(msg string) { agents.SendTelegram(msg) }
		// Hand FillMonitor to ExecutionAgent so each LIMIT entry is polled to fill.
		execAgent.FillMonitor = fillMonitor
	}

	// Refresh NSE holiday list at startup and once daily at 06:00 IST so the
	// hardcoded fallback isn't relied on for routine holidays.
	go refreshNSEHolidays()
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

	go launchDashboardUI()

	// ══════════════════════════════════════════════════════════════
	//  STARTUP COMPLETE
	// ══════════════════════════════════════════════════════════════
	execAgent.Mu.RLock()
	openCount := len(execAgent.ActiveTrades)
	execAgent.Mu.RUnlock()

	agents.SendTelegram(fmt.Sprintf(
		"🚀 *QUANTIX ENGINE v3.0 — SWING*\nMode: `%s`\nCapital: `₹%.0f`\nUniverse: `%d stocks`\nOpen Positions: `%d/%d`\nStrategy: `EMA10/20 Crossover + VCP`\nSL Range: `%.1f%%–%.1f%%`",
		map[bool]string{true: "PAPER", false: "LIVE"}[config.PaperMode],
		config.TotalCapital, len(dataAgent.Universe),
		openCount, config.ComputeMaxPositions(config.TotalCapital),
		config.SLFloorPct, config.SLCeilingPct))

	log.Println("[Engine] ✅ Fully initialized. Entering swing trading loop...")

	// ══════════════════════════════════════════════════════════════
	//  MAIN SWING TRADING LOOP — 24/7 Multi-Day Operation
	// ══════════════════════════════════════════════════════════════
	//  Swing trading: NO EOD squareoff. Positions held overnight.
	//  During market hours: monitor hard SL + scan for new VCP breakouts.
	//  At EOD (15:31): run EMA20 + sell pressure exit check on daily closes.

	for { // Outer loop: one iteration per trading day
		today := config.NowIST()
		dayOfWeek := today.Weekday()

		// Weekend skip
		if dayOfWeek == time.Saturday || dayOfWeek == time.Sunday {
			log.Printf("[Engine] %s — sleeping until Monday", dayOfWeek.String())
			sleepUntilMorning()
			continue
		}

		// FIX-13: NSE Holiday check
		if isNSEHoliday(today) {
			log.Printf("[Engine] NSE Holiday — sleeping until next trading day")
			sleepUntilMorning()
			continue
		}

		// After-hours check
		t0 := today.Hour()*100 + today.Minute()
		if t0 >= 1605 {
			sleepUntilMorning()
			continue
		}

		log.Printf("[Engine] ═══ TRADING DAY: %s (%s) ═══",
			today.Format("2006-01-02"), dayOfWeek.String())

		scanner.NewSession()
		tickStore.ResetDaily()
		eodCheckDone := false
		eodScanDone := false

		// Swing tick loop — 1 second interval (not 200ms, swing doesn't need HFT speed)
		ticker := time.NewTicker(1 * time.Second)
		currentRegime := "UNKNOWN"
		lastRegimeCheck := time.Time{}
		scanCount := 0

	dayLoop:
		for range ticker.C {
			now := config.NowIST()
			t := now.Hour()*100 + now.Minute()

			// Kill switch file check
			killFile := config.BaseDir + string(os.PathSeparator) + "data" + string(os.PathSeparator) + "kill_switch.txt"
			if _, err := os.Stat(killFile); err == nil {
				execAgent.FlattenAll("KILL_SWITCH")
				agents.SendTelegram("🛑 *KILL SWITCH* — All swing positions flattened")
			}

			// Pre-market wait
			if t < 915 {
				continue
			}

			// ── Phase 4: Hard SL monitoring (every second during market hours) ──
			execAgent.MonitorPositions(currentRegime)

			// ── Phase 1: Regime detection (every 30 minutes) ──
			if time.Since(lastRegimeCheck) > 30*time.Minute || currentRegime == "UNKNOWN" {
				newRegime := scanner.DetectRegime()
				if newRegime != "" {
					currentRegime = newRegime
				}
				lastRegimeCheck = time.Now()
			}

			// ── Phase 2+3: VCP Breakout scanning (every 30 seconds) ──
			if scanCount%30 == 0 {
				signals := scanner.RunAllScans(currentRegime)
				for _, sig := range signals {
					execAgent.Execute(sig, currentRegime)
				}
			}
			scanCount++

			// ── Phase 5: EOD Check — EMA20 trailing exit + sell pressure rule (once at 15:31) ──
			// Running at 15:31 ensures Kite's day candle is finalized (market closes 15:30).
			// At 15:20 the candle is still live and the close price is provisional.
			if t >= 1531 && !eodCheckDone {
				log.Println("[Engine] ═══ EOD EMA20 CHECK (15:31) ═══")

				// Refresh daily cache for latest closes
				dailyCache.Preload(dataAgent.Universe)
				freshCache := dailyCache.ToScannerCache()
				scanner.DailyCache = freshCache

				// Run EMA20 + sell pressure exit check on all positions
				execAgent.RunDailyEMACheck(freshCache)

				// Check for re-entries on stocks that were exited by EMA rule
				execAgent.CheckReEntries(scanner, currentRegime)

				// Phase 3: Check for top-up opportunities on existing positions
				execAgent.CheckTopUps(scanner, currentRegime)
				// Daily summary
				execAgent.DailySummaryAlert(currentRegime)

				eodCheckDone = true
				log.Println("[Engine] ═══ EOD EMA CHECK COMPLETE (15:31) ═══")
			}

			// ── EOD Market Scan (once at 15:45) ──
			if t >= 1545 && !eodScanDone {
				eodScanDone = true
				go agents.RunEODMarketScan(agents.EODScanDeps{
					LoadUniverse:    dataAgent.LoadEODScanUniverse,
					PreloadCache:    dailyCache.Preload,
					GetScannerCache: func() *agents.DailyCache { return dailyCache.ToScannerCache() },
					GetLiveLTP:      func(token uint32) float64 { return tickStore.GetLTPIfFresh(token) },
					GetLiveVolume:   func(token uint32) int64 { return tickStore.GetVolume(token) },
				}, scanner)
			}

			// After market close — end the day loop (but DON'T squareoff!)
			if t >= 1605 {
				log.Println("[Engine] Market closed. Swing positions HELD overnight.")
				ticker.Stop()
				break dayLoop
			}
		}

		// Post-market: sync EOD data
		log.Println("[Engine] Running post-market EOD data sync...")
		dailyCache.SyncEODToHistoricalDB(dataAgent.Universe)

		agents.SendTelegram("🌙 *ENGINE SLEEPING* — Swing positions held overnight.")
		sleepUntilMorning()
		log.Printf("[Engine] ═══ WAKING UP — %s ═══", config.NowIST().Format("2006-01-02 15:04"))
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

func launchDashboardUI() {
	const url = "http://127.0.0.1:8085"
	time.Sleep(1 * time.Second)
	exec.Command("cmd", "/c", "start", url).Start()
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

func killOldProcess(port string) {
	out, err := exec.Command("cmd", "/c", fmt.Sprintf("netstat -ano | findstr :%s | findstr LISTENING", port)).Output()
	if err != nil || len(out) == 0 {
		return
	}
	lines := splitLines(fmt.Sprintf("%s", out))
	for _, line := range lines {
		fields := splitFields(line)
		if len(fields) >= 5 {
			pid := fields[len(fields)-1]
			if pid != "0" {
				exec.Command("taskkill", "/F", "/PID", pid).Run()
				time.Sleep(500 * time.Millisecond)
			}
		}
	}
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			if len(line) > 0 {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func splitFields(s string) []string {
	var fields []string
	inField := false
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ' ' || s[i] == '\t' {
			if inField {
				fields = append(fields, s[start:i])
				inField = false
			}
		} else if !inField {
			start = i
			inField = true
		}
	}
	return fields
}

// NSE holidays are loaded once at startup from research.FetchNSEHolidays() and
// refreshed daily at 06:00 IST. Falls back to the hardcoded 2026 list below
// only if the live fetch fails (e.g., no network at boot).
//
// Source: https://www.nseindia.com/resources/exchange-communication-holidays
var (
	nseHolidaysMu sync.RWMutex
	// MM-DD keys (year-agnostic), matching research.FetchNSEHolidays's format.
	nseHolidaysMMDD = map[string]string{}
)

var nseHolidays2026Fallback = map[string]bool{
	"2026-01-26": true, "2026-02-17": true, "2026-03-10": true,
	"2026-03-30": true, "2026-04-02": true, "2026-04-03": true,
	"2026-04-14": true, "2026-05-01": true, "2026-06-06": true,
	"2026-07-06": true, "2026-08-15": true, "2026-08-18": true,
	"2026-09-04": true, "2026-10-02": true, "2026-10-20": true,
	"2026-10-21": true, "2026-11-09": true, "2026-11-10": true,
	"2026-11-24": true, "2026-12-25": true,
}

func refreshNSEHolidays() {
	fetched := research.FetchNSEHolidays()
	if len(fetched) == 0 {
		log.Printf("[Engine] NSE holiday fetch returned empty — keeping existing/fallback list")
		return
	}
	nseHolidaysMu.Lock()
	nseHolidaysMMDD = fetched
	nseHolidaysMu.Unlock()
}

func isNSEHoliday(t time.Time) bool {
	mmdd := fmt.Sprintf("%02d-%02d", t.Month(), t.Day())
	nseHolidaysMu.RLock()
	_, fromAPI := nseHolidaysMMDD[mmdd]
	nseHolidaysMu.RUnlock()
	if fromAPI {
		return true
	}
	// Fallback to hardcoded list if live fetch hasn't populated yet.
	dateStr := t.Format("2006-01-02")
	return nseHolidays2026Fallback[dateStr]
}
