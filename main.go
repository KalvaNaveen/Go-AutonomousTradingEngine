package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"time"

	"bnf_go_engine/agents"
	"bnf_go_engine/api"
	"bnf_go_engine/broker"
	"bnf_go_engine/config"
	"bnf_go_engine/core"
	"bnf_go_engine/storage"

	"github.com/joho/godotenv"
)

func main() {
	// Load .env — try multiple locations (Go project, Python project, CLI arg)
	envPaths := []string{
		"./.env",
	}
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

	// Re-read env vars after loading
	config.Reload()

	config.PrintBanner()
	startNs := time.Now().UnixNano()

	// ══════════════════════════════════════════════════════════════
	//  PHASE 0: Auto-Login (Token Refresh at 8:30 AM)
	// ══════════════════════════════════════════════════════════════
	if config.ZerodhaUserID != "" && config.ZerodhaTOTPSecret != "" {
		needsLogin := false
		
		// Always login if no token
		if config.KiteAccessToken == "" {
			needsLogin = true
			log.Println("[Engine] No access token found — auto-login required")
		} else {
			// Validate existing token with a quick API call
			log.Println("[Engine] Validating existing access token...")
			client := &http.Client{Timeout: 10 * time.Second}
			req, _ := http.NewRequest("GET", "https://api.kite.trade/user/profile", nil)
			req.Header.Set("X-Kite-Version", "3")
			req.Header.Set("Authorization", fmt.Sprintf("token %s:%s", config.KiteAPIKey, config.KiteAccessToken))
			resp, err := client.Do(req)
			if err != nil || resp.StatusCode != 200 {
				needsLogin = true
				log.Println("[Engine] Access token is INVALID/EXPIRED — auto-login required")
				if resp != nil {
					resp.Body.Close()
				}
			} else {
				resp.Body.Close()
				log.Println("[Engine] Access token is VALID ✅")
			}
		}
		
		if needsLogin {
			log.Println("[Engine] Running auto-login for fresh access token...")
			login := core.NewAutoLogin()
			if login.Run() {
				agents.SendTelegram("✅ *AUTO LOGIN SUCCESS* — Token refreshed")
			} else {
				agents.SendTelegram("🚨 *AUTO LOGIN FAILED* — Check credentials")
			}
		}
	}

	// ══════════════════════════════════════════════════════════════
	//  PHASE 1: Initialize Core Systems
	// ══════════════════════════════════════════════════════════════
	stateManager := core.NewStateManager()
	journal := core.NewJournal()
	risk := agents.NewRiskAgent(config.TotalCapital)
	scanner := agents.NewScannerAgent()
	exec := agents.NewExecutionAgent(risk, journal, stateManager)
	exec.Scanner = scanner
	fillMonitor := core.NewFillMonitor(stateManager)

	// ── ML Signal Filter (with Backtester cold-start fix) ──
	mlFilter := core.NewMLFilter()
	mlFilter.TrainFromJournal()

	// If ML has insufficient training data, run backtester to seed journal.db
	if !mlFilter.Trained {
		log.Println("[Engine] ML filter needs more data — running backtester to seed...")
		core.RunBacktestAndSeed()
		// Retrain with new backtest data
		mlFilter.TrainFromJournal()
	}

	log.Printf("[Engine] ML Filter: trained=%v samples=%d accuracy=%.1f%%",
		mlFilter.Trained, mlFilter.TrainCount, mlFilter.Accuracy)

	// ── Walk-Forward Optimizer ──
	wfo := core.NewWalkForwardOptimizer()
	optParams := wfo.LoadParams()
	log.Printf("[Engine] WFO params loaded (last=%s)", optParams.LastOptimized)
	// WFO params influence strategy thresholds — applied at session start
	_ = optParams // TODO: wire optParams into scanner config overrides
	_ = wfo       // Used at EOD for re-optimization

	// ── Portfolio VaR Engine ──
	varEngine := core.NewVaREngine(config.TotalCapital)

	// ── Wait for Network on service startup ──
	waitForNetwork()

	// ══════════════════════════════════════════════════════════════
	//  PHASE 2: Wire Broker (Paper or Live)
	// ══════════════════════════════════════════════════════════════
	// paperBroker is declared here so we can wire GetLTP after TickStore init
	var paperBroker *broker.RealisticPaperBroker
	if config.PaperMode {
		paperBroker = broker.NewRealisticPaperBroker(config.TotalCapital)
		defer paperBroker.Stop()
		exec.PlaceOrder = paperBroker.PlaceOrder
		exec.CancelOrder = paperBroker.CancelOrder
		log.Println("[Engine] Realistic Paper Broker enabled (slippage + margin tracking)")
	} else {
		kb := broker.NewKiteBroker()
		exec.PlaceOrder = kb.PlaceOrder
		exec.CancelOrder = kb.CancelOrder
		log.Println("[Engine] LIVE broker connected (Kite)")
		if balance, err := kb.GetMargins(); err == nil && balance > 0 {
			risk.TotalCapital = balance
			risk.ActiveCapital = balance * config.ActiveCapitalPct
			risk.RiskReserve = balance * config.RiskReservePct
			log.Printf("[Engine] Live capital: ₹%.2f", balance)
		}
	}

	// ══════════════════════════════════════════════════════════════
	//  PHASE 3: Load Universe & Preload Daily Cache
	// ══════════════════════════════════════════════════════════════
	dataAgent := agents.NewDataAgent()
	if err := dataAgent.LoadUniverse(); err != nil {
		log.Printf("[Engine] WARNING: Universe load failed: %v", err)
	}
	if len(dataAgent.Universe) == 0 {
		log.Println("[Engine] CRITICAL: Empty universe. Using fallback symbols.")
		// Hardcode a few for safety
		dataAgent.Universe = map[uint32]string{
			738561:  "RELIANCE",
			2953217: "TCS",
			341249:  "HDFCBANK",
			408065:  "INFY",
			2552833: "SBIN",
		}
	}

	// Wire universe into scanner
	scanner.Universe = dataAgent.Universe

	// Preload daily cache (260d historical data)
	dailyCache := storage.NewDailyCache()
	log.Println("[Engine] Preloading daily cache (this takes ~2 minutes)...")
	cacheOK := dailyCache.Preload(dataAgent.Universe)
	if !cacheOK {
		log.Println("[Engine] WARNING: Daily cache incomplete. Continuing with available data.")
	}
	scanner.DailyCache = dailyCache.ToScannerCache()

	// ══════════════════════════════════════════════════════════════
	//  PHASE 4: Initialize TickStore + WebSocket
	// ══════════════════════════════════════════════════════════════
	tickStore := storage.NewTickStore()

	// Wire TickStore into scanner
	scanner.GetLTP = func(token uint32) float64 { return tickStore.GetLTPIfFresh(token) }
	scanner.GetVWAP = func(token uint32) float64 { return tickStore.GetVWAP(token) }
	scanner.GetVolume = func(token uint32) int64 { return tickStore.GetVolume(token) }
	scanner.GetDepth = func(token uint32) map[string]float64 { return tickStore.GetDepth(token) }
	scanner.GetCandles5m = func(token uint32) []agents.Candle { return tickStore.GetCandles5Min(token) }
	scanner.GetORB = func(token uint32) (float64, float64) { return tickStore.GetORB(token) }
	scanner.GetDayOpen = func(token uint32) float64 { return tickStore.GetDayOpen(token) }
	scanner.ComputeRVol = func(token uint32) float64 {
		vol := tickStore.GetVolume(token)
		avgVol := dailyCache.GetAvgDailyVol(token)
		if avgVol <= 0 || vol <= 0 {
			return 0
		}
		// Time-normalize: compare current volume against what we'd EXPECT at this time of day.
		// Market hours: 9:15 AM to 3:30 PM = 375 minutes.
		// At 10:00 AM (45 mins in), only ~12% of daily volume has traded, so comparing
		// against the FULL day average would give RVol=0.12 — always below the 1.2 filter.
		// Fix: scale the denominator by elapsed fraction.
		now := config.NowIST()
		marketOpenMins := 9*60 + 15 // 9:15 AM
		currentMins := now.Hour()*60 + now.Minute()
		elapsedMins := float64(currentMins - marketOpenMins)
		if elapsedMins < 1 {
			elapsedMins = 1
		}
		totalMarketMins := 375.0 // 9:15 to 15:30
		if elapsedMins > totalMarketMins {
			elapsedMins = totalMarketMins
		}
		fractionOfDay := elapsedMins / totalMarketMins
		expectedVolNow := avgVol * fractionOfDay
		if expectedVolNow <= 0 {
			return 0
		}
		return float64(vol) / expectedVolNow
	}
	scanner.GetIndiaVIX = func() float64 {
		return tickStore.GetLTPIfFresh(config.IndiaVIXToken)
	}
	scanner.GetADRatio = func() float64 {
		var tokens []uint32
		for t := range dataAgent.Universe {
			tokens = append(tokens, t)
		}
		adv, dec := tickStore.GetAdvanceCount(tokens)
		total := adv + dec
		if total == 0 {
			return 0.5
		}
		return float64(adv) / float64(total)
	}

	// Wire LTP into execution agent
	exec.GetLTP = scanner.GetLTP

	// Connect WebSocket
	allTokens := dataAgent.GetAllTokens()
	ws := storage.NewKiteWebSocket(tickStore, allTokens)
	if config.KiteAPIKey != "" && config.KiteAccessToken != "" {
		if err := ws.Connect(); err != nil {
			log.Printf("[Engine] WebSocket connect failed: %v (will retry)", err)
		} else {
			log.Printf("[Engine] WebSocket connected. Subscribed %d tokens", len(allTokens))
		}
	} else {
		log.Println("[Engine] No Kite credentials — WebSocket skipped (paper mode)")
	}

	// ══════════════════════════════════════════════════════════════
	//  PHASE 5: Crash Recovery
	// ══════════════════════════════════════════════════════════════
	exec.RestoreFromState()

	// Wire Paper Broker LTP (needs TickStore + Universe, so done after both are ready)
	if paperBroker != nil {
		// Build reverse map: symbol → token
		symbolToToken := make(map[string]uint32)
		for token, sym := range dataAgent.Universe {
			symbolToToken[sym] = token
		}
		paperBroker.GetLTP = func(symbol string) float64 {
			if tok, ok := symbolToToken[symbol]; ok {
				return tickStore.GetLTPIfFresh(tok)
			}
			return 0
		}
		log.Println("[Engine] Paper Broker wired with real LTP feed")
	}

	// ══════════════════════════════════════════════════════════════
	//  PHASE 6: MacroAgent (News Intelligence)
	// ══════════════════════════════════════════════════════════════
	macroAgent := agents.NewMacroAgent(dataAgent.Universe)
	macroAgent.GetLTPIfFresh = func(token uint32) float64 { return tickStore.GetLTPIfFresh(token) }
	macroAgent.GetDayOpen = func(token uint32) float64 { return tickStore.GetDayOpen(token) }
	macroAgent.ComputeRVol = scanner.ComputeRVol
	macroAgent.Start()
	defer macroAgent.Stop()
	log.Println("[Engine] MacroAgent news scanner started")

	// ══════════════════════════════════════════════════════════════
	//  PHASE 7: Start API Server (Dashboard Backend)
	// ══════════════════════════════════════════════════════════════
	apiServer := api.NewServer(risk, journal, exec, scanner, tickStore, dailyCache, macroAgent, mlFilter)
	go apiServer.Start(":8081")
	log.Println("[Engine] API server started on :8081")

	// Wire FillMonitor (for live mode)
	if !config.PaperMode {
		fillMonitor.PlaceOrder = exec.PlaceOrder
		fillMonitor.CancelOrder = exec.CancelOrder
		fillMonitor.AlertFn = func(msg string) { agents.SendTelegram(msg) }
		log.Println("[Engine] FillMonitor wired for live order tracking")
	}
	_ = fillMonitor // used in live mode

	// ══════════════════════════════════════════════════════════════
	//  PHASE 8: Launch UI
	// ══════════════════════════════════════════════════════════════
	go launchDashboardUI()

	// ══════════════════════════════════════════════════════════════
	//  STARTUP COMPLETE
	// ══════════════════════════════════════════════════════════════
	initTime := (time.Now().UnixNano() - startNs) / 1000
	journal.LogAgentActivity("Engine", "STARTUP",
		fmt.Sprintf("Go Engine v2.0 | Capital=%.0f | Mode=%v | Universe=%d | Strategies=12 | ML=%v | Init=%dμs",
			risk.TotalCapital, config.PaperMode, len(dataAgent.Universe), mlFilter.Trained, initTime),
		config.NowIST().Format("15:04:05"))

	agents.SendTelegram(fmt.Sprintf(
		"🚀 *BNF GO ENGINE v2.0*\nMode: `%s`\nCapital: `₹%.0f`\nUniverse: `%d stocks`\nStrategies: `12 (S1-S13)`\nML Filter: `%v (acc=%.0f%%)`\nMin Net Profit: `₹50/trade`\nInit: `%dμs`",
		map[bool]string{true: "PAPER", false: "LIVE"}[config.PaperMode],
		risk.TotalCapital, len(dataAgent.Universe),
		mlFilter.Trained, mlFilter.Accuracy, initTime))

	log.Printf("[Engine] ✅ Fully initialized in %dμs. Entering main loop...", initTime)

	// ══════════════════════════════════════════════════════════════
	//  MAIN TRADING LOOP — 24/7 Multi-Day Operation
	// ══════════════════════════════════════════════════════════════
	// Runs continuously: trades during market hours, sleeps overnight,
	// skips weekends, auto-restarts each morning.

	for { // Outer loop: one iteration per trading day
		today := config.NowIST()
		dayOfWeek := today.Weekday()

		// ── Weekend Skip ──
		if dayOfWeek == time.Saturday || dayOfWeek == time.Sunday {
			dayName := dayOfWeek.String()
			log.Printf("[Engine] %s detected — sleeping until Monday", dayName)
			agents.SendTelegram(fmt.Sprintf("📅 *WEEKEND — ENGINE OFF*\n`%s` — Market closed.\nWill auto-restart Monday 08:25 AM.", dayName))
			sleepUntilMorning()
			continue
		}

		// ── After-hours check ──
		t0 := today.Hour()*100 + today.Minute()
		if t0 >= 1605 {
			log.Printf("[Engine] After market hours (%02d:%02d). Sleeping until tomorrow.", today.Hour(), today.Minute())
			sleepUntilMorning()
			continue
		}

		log.Printf("[Engine] ═══ NEW TRADING DAY: %s (%s) ═══",
			today.Format("2006-01-02"), dayOfWeek.String())

		// Reset daily state
		risk = agents.NewRiskAgent(config.TotalCapital)
		exec.Risk = risk
		scanner.NewSession()
		tickStore.ResetDaily()
		holidayChecked := false
		killSwitchAlerted := false

		// ── Intraday Tick Loop (200ms for faster reaction) ──
		ticker := time.NewTicker(200 * time.Millisecond)
		scanCount := 0
		lastRegimeCheck := time.Time{}
		currentRegime := "UNKNOWN"

	dayLoop:
		for range ticker.C {
			now := config.NowIST()
			t := now.Hour()*100 + now.Minute()

			// ── Kill Switch File Check ──
			killFile := config.BaseDir + string(os.PathSeparator) + "data" + string(os.PathSeparator) + "kill_switch.txt"
			if _, err := os.Stat(killFile); err == nil && !killSwitchAlerted {
				risk.EngineStopped = true
				risk.StopReason = "MANUAL_KILL_SWITCH_FILE"
				exec.FlattenAll("KILL_SWITCH")
				agents.SendTelegram("🛑 *KILL SWITCH ACTIVATED* — All positions flattened")
				killSwitchAlerted = true
			}

			// Pre-market wait
			if t < 915 {
				continue
			}

			// ── Holiday Detection via Kite (once at 9:16) ──
			if !holidayChecked && t >= 916 {
				holidayChecked = true
				if !tickStore.IsReady() && !config.PaperMode {
					log.Println("[Engine] No ticks received by 9:16 — possible holiday")
					agents.SendTelegram("📅 *HOLIDAY DETECTED*\nNo live ticks by 9:16 AM. Engine parking.")
					risk.EngineStopped = true
					risk.StopReason = "Market Holiday (No ticks)"
				}
			}

			// ── Shutdown time: EOD Squareoff + Break ──
			sqH, sqM := config.ParseTime(config.IntradaySquareoff)
			if t >= sqH*100+sqM {
				exec.FlattenAll("EOD_SQUAREOFF")
				exec.DailySummaryAlert(currentRegime)
				stats := risk.GetDailyStats()
				log.Printf("[Engine] ═══ DAY COMPLETE ═══ Trades=%v Wins=%v P&L=₹%.2f Scans=%d",
					stats["total"], stats["wins"], stats["gross_pnl"], scanCount)
				ticker.Stop()
				break dayLoop
			}

			// ── Position monitoring runs at 200ms (5x/sec for fast SL) ──
			monStart := time.Now().UnixNano()
			exec.MonitorPositions(currentRegime)
			monElapsed := time.Now().UnixNano() - monStart

			// ── Strategy scanning now runs at EVERY 200ms tick ──
			// Go is fast enough: 250 stocks × 12 strategies = ~10ms
			// Catches breakouts 0.5-0.8s earlier than 1s scanning

			if risk.EngineStopped {
				continue // Only monitor positions, don't scan
			}

			tickStart := time.Now().UnixNano()

			// Regime detection (every 15 minutes)
			if time.Since(lastRegimeCheck) > 15*time.Minute || currentRegime == "UNKNOWN" {
				currentRegime = scanner.DetectRegime()
				lastRegimeCheck = time.Now()
			}

			// Scan for new signals
			scanStart := time.Now().UnixNano()
			signals := scanner.RunAllScans(currentRegime)

			// Drain MacroAgent news signals
			macroSignals := macroAgent.DrainSignals()
			signals = append(signals, macroSignals...)

			scanElapsed := time.Now().UnixNano() - scanStart
			scanCount++

			// Execute approved signals (with macro veto + ML filter + VaR + sector check)
			execStart := time.Now().UnixNano()
			executed := 0
			mlBlocked := 0
			varBlocked := 0
			for _, sig := range signals {
				if config.DisabledStrategies[sig.Strategy] {
					continue
				}
				if macroAgent.CheckVeto(sig.Symbol, !sig.IsShort, currentRegime) {
					continue
				}
				// Sector correlation guard: max 3 positions per sector
				if exec.SectorCount(sig.Symbol) >= 3 {
					continue
				}

				// ML Signal Filter: reject low-probability signals
				fv := core.FeatureVector{
					RSI:         core.Clamp01(sig.RSI / 100.0),
					ADX:         core.Clamp01(sig.ADX / 100.0),
					RVol:        core.Clamp01(sig.RVol / 5.0),
					BBPosition:  0.5,
					VIX:         core.Clamp01(scanner.GetCurrentVIX() / 40.0),
					ADRatio:     scanner.GetCurrentADRatio(),
					HourOfDay:   core.Clamp01(float64(now.Hour()-9) / 6.0),
					RegimeScore: core.EncodeRegime(currentRegime),
				}
				mlOK, mlProb := mlFilter.ShouldTradeSignal(fv)
				if !mlOK {
					mlBlocked++
					log.Printf("[ML] Blocked %s %s (prob=%.2f)", sig.Strategy, sig.Symbol, mlProb)
					continue
				}

				// Portfolio VaR check: block if adding this trade breaches VaR limit
				var currentVaRPositions []core.VaRPosition
				for _, t := range exec.ActiveTrades {
					currentVaRPositions = append(currentVaRPositions, core.VaRPosition{
						Token: t.Token, Symbol: t.Symbol,
						EntryPrice: t.EntryPrice, Qty: t.Qty, IsShort: t.IsShort,
					})
				}
				newVaRPos := core.VaRPosition{
					Token: sig.Token, Symbol: sig.Symbol,
					EntryPrice: sig.EntryPrice, Qty: 1, IsShort: sig.IsShort,
				}
				varOK, varReason := varEngine.CheckNewTrade(currentVaRPositions, newVaRPos)
				if !varOK {
					varBlocked++
					log.Printf("[VaR] Blocked %s %s: %s", sig.Strategy, sig.Symbol, varReason)
					continue
				}

				if exec.Execute(sig, currentRegime) {
					executed++
				}
			}
			execElapsed := time.Now().UnixNano() - execStart
			tickElapsed := time.Now().UnixNano() - tickStart

			// Performance logging
			if len(signals) > 0 || len(exec.ActiveTrades) > 0 {
				log.Printf("[PERF] Tick:%dns Scan:%dns(%d) Mon:%dns Exec:%dns(%d) Open:%d ML_block:%d VaR_block:%d",
					tickElapsed, scanElapsed, len(signals),
					monElapsed, execElapsed, executed,
					len(exec.ActiveTrades), mlBlocked, varBlocked)
			}

			if risk.EngineStopped && !killSwitchAlerted {
				log.Printf("[Engine] RISK STOP: %s", risk.StopReason)
				agents.SendTelegram(fmt.Sprintf("🛑 *ENGINE STOPPED*: `%s`", risk.StopReason))
				killSwitchAlerted = true
			}
		}

		// ── Post-Market Cleanup ──
		log.Println("[Engine] Day complete. Sleeping until next trading morning...")
		sleepUntilMorning()
		log.Printf("[Engine] ═══ WAKING UP — %s ═══", config.NowIST().Format("2006-01-02 15:04"))
	}
}

// sleepUntilMorning sleeps until 08:25 AM IST on the next trading day
func sleepUntilMorning() {
	now := config.NowIST()
	tomorrow := now.AddDate(0, 0, 1)
	// Skip weekends
	for tomorrow.Weekday() == time.Saturday || tomorrow.Weekday() == time.Sunday {
		tomorrow = tomorrow.AddDate(0, 0, 1)
	}
	wake := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 8, 25, 0, 0, config.IST)
	sleepDur := wake.Sub(now)
	if sleepDur <= 0 {
		return
	}
	log.Printf("[Engine] Sleeping %.1f hours until %s", sleepDur.Hours(), wake.Format("2006-01-02 15:04"))
	time.Sleep(sleepDur)
}

func launchDashboardUI() {
	time.Sleep(1 * time.Second) // Let API server bind

	dashboardDir := config.BaseDir + string(os.PathSeparator) + "dashboard"
	if _, err := os.Stat(dashboardDir); os.IsNotExist(err) {
		log.Printf("[UI] Dashboard directory not found at: %s", dashboardDir)
		return
	}

	log.Println("[UI] Launching Vite Dashboard Server...")
	
	// Create the command
	cmd := exec.Command("npm", "run", "dev")
	cmd.Dir = dashboardDir
	cmd.Stdout = nil // ignore vite spam
	cmd.Stderr = nil
	
	if err := cmd.Start(); err != nil {
		log.Printf("[UI] Failed to launch Vite server: %v", err)
		return
	}
	
	// Give Vite a moment to start
	time.Sleep(3 * time.Second)
	
	// Open the browser
	log.Println("[UI] Opening browser at http://127.0.0.1:7999")
	exec.Command("cmd", "/c", "start", "http://127.0.0.1:7999").Start()
	
	// Wait for process to exit
	cmd.Wait()
}

// waitForNetwork pings a reliable endpoint to ensure DNS and network are up 
// before attempting an API init. This solves NSSM bootup DNS issues.
func waitForNetwork() {
	client := &http.Client{Timeout: 3 * time.Second}
	maxRetries := 10
	for i := 0; i < maxRetries; i++ {
		// Ping NSE archives or Kite API to test DNS and connectivity
		resp, err := client.Get("https://api.kite.trade")
		if err == nil {
			resp.Body.Close()
			return // Network is up!
		}
		
		log.Printf("[Engine] Network/DNS not ready (attempt %d/%d): %v", i+1, maxRetries, err)
		time.Sleep(3 * time.Second)
	}
	log.Println("[Engine] WARNING: Proceeding despite network check failure.")
}
