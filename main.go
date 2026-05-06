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
	"bnf_go_engine/simulator"
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

	// ML Filter REMOVED — was untrained, insufficient data, randomly blocking signals
	// Signals now go straight through: DisabledStrategies → VaR → Execute

	// ── Walk-Forward Optimizer ──
	wfo := core.NewWalkForwardOptimizer()
	optParams := wfo.LoadParams()
	log.Printf("[Engine] WFO params loaded (last=%s)", optParams.LastOptimized)

	// Apply optimized parameters to live config — WFO self-protects with
	// minimum data requirements and out-of-sample Sharpe validation
	config.S1_ADX_MIN = int(optParams.S1_ADX_MIN)
	config.S1_ATR_SL_MULT = optParams.S1_ATR_SL_MULT
	config.S1_RR = optParams.S1_RR
	config.S2_BB_SD = optParams.S2_BB_SD
	config.S2_RSI_OVERSOLD = int(optParams.S2_RSI_OVERSOLD)
	config.S2_RSI_OVERBOUGHT = int(optParams.S2_RSI_OVERBOUGHT)
	config.S2_RR = optParams.S2_RR
	config.S7_RSI_OVERSOLD = int(optParams.S7_RSI_OVERSOLD)
	config.S7_VWAP_DEVIATION_PCT = optParams.S7_VWAP_DEV_PCT
	config.S9_RSI_THRESHOLD = int(optParams.S9_RSI_THRESHOLD)
	config.S9_ATR_SL_MULT = optParams.S9_ATR_SL_MULT
	config.S9_RR = optParams.S9_RR
	log.Println("[Engine] WFO params applied to strategy configs")

	// ── Portfolio VaR Engine — Goldman/MS standard risk management ──
	varEngine := core.NewVaREngine(config.TotalCapital)
	varEngine.Enabled = true
	log.Println("[Engine] VaR Engine enabled (95% confidence, 5% capital limit)")

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
	scanner.TokenToCompany = dataAgent.TokenToCompany

	// Preload daily cache (260d historical data)
	dailyCache := storage.NewDailyCache()
	log.Println("[Engine] Preloading daily cache (this takes ~2 minutes)...")
	cacheOK := dailyCache.Preload(dataAgent.Universe)
	if !cacheOK {
		log.Println("[Engine] WARNING: Daily cache incomplete. Continuing with available data.")
	}
	scanner.DailyCache = dailyCache.ToScannerCache()

	// Load VaR historical returns from daily close prices
	if scanner.DailyCache != nil && scanner.DailyCache.Closes != nil {
		varEngine.LoadHistoricalReturns(scanner.DailyCache.Closes)
	}

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
	//  PHASE 4.5: Scheduled Daily Token Refresh (24/7 Continuity)
	// ══════════════════════════════════════════════════════════════
	go func() {
		for {
			now := config.NowIST()
			// Zerodha resets tokens around 8:00 AM. We refresh exactly at 8:30 AM before market open.
			// This fixes the issue when engine is started before 8:30 AM or runs multi-day 24/7.
			if now.Hour() == 8 && now.Minute() == 30 {
				log.Println("[Engine] Executing scheduled 8:30 AM Auto-Login...")
				login := core.NewAutoLogin()
				if login.Run() {
					agents.SendTelegram("✅ *SCHEDULED AUTO LOGIN SUCCESS* — Token refreshed")
					if ws != nil {
						// Update WebSocket's token and force a reconnect
						ws.UpdateToken(config.KiteAccessToken)
						ws.Close() 
						log.Println("[Engine] Reset WebSocket to use new token.")
					}
				} else {
					agents.SendTelegram("🚨 *SCHEDULED AUTO LOGIN FAILED* — Check credentials")
				}
				time.Sleep(61 * time.Second) // Prevent multiple triggers in same minute
			}
			time.Sleep(20 * time.Second)
		}
	}()

	// ══════════════════════════════════════════════════════════════
	//  PHASE 5: Crash Recovery
	// ══════════════════════════════════════════════════════════════
	exec.RestoreFromState()

	// Backfill missing Tokens to restored trades (vital for live P&L streaming)
	symbolToToken := make(map[string]uint32)
	for token, sym := range dataAgent.Universe {
		symbolToToken[sym] = token
	}
	exec.Mu.Lock()
	for _, trade := range exec.ActiveTrades {
		if trade.Token == 0 {
			if matchedTok, exists := symbolToToken[trade.Symbol]; exists {
				trade.Token = matchedTok
				log.Printf("[Engine] Recovered missing token for %s: %d", trade.Symbol, matchedTok)
			}
		}
	}
	exec.Mu.Unlock()

	// Recover daily P&L and engine state
	dateStr := config.TodayIST().Format("2006-01-02")
	summary := journal.GetDailySummaryForDate(dateStr)
	if summary != nil {
		if pnl, ok := summary["gross_pnl"].(float64); ok {
			risk.DailyPnl = pnl
			scanner.RecordPnl(pnl)
		}
		if stopped, ok := summary["engine_stopped"].(bool); ok && stopped {
			risk.EngineStopped = true
			if reason, ok := summary["stop_reason"].(string); ok {
				risk.StopReason = reason
			}
		}
		log.Printf("[Engine] Restored daily state from DB: pnl=%.2f stopped=%v",
			summary["gross_pnl"], summary["engine_stopped"])
	}

	// Wire Paper Broker LTP (needs TickStore + Universe, so done after both are ready)
	if paperBroker != nil {
		// Maps already built globally in phase 5
		paperBroker.GetLTP = func(symbol string) float64 {
			if tok, ok := symbolToToken[symbol]; ok {
				return tickStore.GetLTPIfFresh(tok)
			}
			return 0
		}
		// Smart Execution: wire real bid/ask depth for spread-aware fills
		paperBroker.GetDepth = func(symbol string) (float64, float64) {
			if tok, ok := symbolToToken[symbol]; ok {
				depth := tickStore.GetDepth(tok)
				bidQty := depth["bid_qty"]
				askQty := depth["ask_qty"]
				ltp := tickStore.GetLTP(tok)
				if ltp <= 0 || (bidQty <= 0 && askQty <= 0) {
					return 0, 0
				}
				// Estimate bid/ask prices from LTP and ratio
				// Kite FULL mode gives qty but not price per level.
				// Typical spread for Nifty 250 stocks: 0.02-0.10%
				spread := ltp * 0.0003 // 0.03% half-spread
				return ltp - spread, ltp + spread
			}
			return 0, 0
		}
		log.Println("[Engine] Paper Broker wired with real LTP + bid/ask depth feed")
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
	const dashboardPort = "8085"
	const dashboardAddr = ":" + dashboardPort
	const dashboardURL = "http://127.0.0.1:" + dashboardPort

	// Kill any zombie process holding the port from a previous crash
	killOldProcess(dashboardPort)

	apiServer := api.NewServer(risk, journal, exec, scanner, tickStore, dailyCache, macroAgent)
	go apiServer.Start(dashboardAddr)
	log.Printf("[Engine] API server started on %s — Dashboard: %s", dashboardAddr, dashboardURL)

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
		fmt.Sprintf("Go Engine v2.0 | Capital=%.0f | Mode=%v | Universe=%d | Strategies=12 | Init=%dμs",
			risk.TotalCapital, config.PaperMode, len(dataAgent.Universe), initTime),
		config.NowIST().Format("15:04:05"))

	agents.SendTelegram(fmt.Sprintf(
		"🚀 *QUANTIX ENGINE v2.0*\nMode: `%s`\nCapital: `₹%.0f`\nUniverse: `%d stocks`\nStrategies: `12 (S1-S13)`\nMin Net Profit: `₹50/trade`\nInit: `%dμs`",
		map[bool]string{true: "PAPER", false: "LIVE"}[config.PaperMode],
		risk.TotalCapital, len(dataAgent.Universe), initTime))

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
			log.Printf("[Engine] Process sleeping. UI remains accessible at %s", dashboardURL)
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
		risk.ResetDaily()

		// Recover any trades already completed today so dashboard stats persist through restarts
		todayTrades := journal.GetAllTradesForDate("")
		if len(todayTrades) > 0 {
			risk.RestoreDailyTrades(todayTrades)
		}

		scanner.NewSession()
		tickStore.ResetDaily()
		holidayChecked := false
		killSwitchAlerted := false

		// ── Intraday Tick Loop (200ms for faster reaction) ──
		ticker := time.NewTicker(200 * time.Millisecond)
		scanCount := 0
		lastRegimeCheck := time.Time{}
		currentRegime := "UNKNOWN"
		
		// Anti-spam log cache
		recentlyLoggedSigs := make(map[string]time.Time)

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
				
				// Automatically persist EOD cache to Historical DB for Simulator use
				dailyCache.SyncEODToHistoricalDB(dataAgent.Universe)

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
				newRegime := scanner.DetectRegime()
				if newRegime != currentRegime {
					journal.LogAgentActivity("Scanner", "REGIME_CHANGE",
						fmt.Sprintf("%s → %s", currentRegime, newRegime),
						config.NowIST().Format("15:04:05"))
				}
				currentRegime = newRegime
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

			// Log scan results periodically (every 50 scans) or when signals found
			if len(signals) > 0 {
				for _, sig := range signals {
					sigKey := sig.Strategy + "_" + sig.Symbol
					lastLogTime, hasLogged := recentlyLoggedSigs[sigKey]
					
					// Only log if we haven't logged this exact signal in the last 5 minutes
					if !hasLogged || time.Since(lastLogTime) > 5*time.Minute {
						recentlyLoggedSigs[sigKey] = time.Now()
						journal.LogAgentActivity("Scanner", "SIGNAL_FOUND",
							fmt.Sprintf("%s %s Entry=%.1f SL=%.1f Target=%.1f (RSI=%.1f RVol=%.1f)",
								sig.Strategy, sig.Symbol, sig.EntryPrice, sig.StopPrice, sig.TargetPrice, sig.RSI, sig.RVol),
							config.NowIST().Format("15:04:05"))
					}
				}
			} else if scanCount%250 == 0 {
				journal.LogAgentActivity("Scanner", "SCAN_CYCLE",
					fmt.Sprintf("Completed %d scans | Regime=%s | Open=%d | %dms",
						scanCount, currentRegime, len(exec.ActiveTrades), scanElapsed/1e6),
					config.NowIST().Format("15:04:05"))
			}

		// Execute signals — STRIPPED PIPELINE
			// Only 2 checks: DisabledStrategies + VaR risk. Everything else was killing signals.
			// May 6: 13 signals entered the old 7-layer gauntlet, only 1 survived. Unacceptable.
			execStart := time.Now().UnixNano()
			executed := 0
			for _, sig := range signals {
				if config.DisabledStrategies[sig.Strategy] {
					continue
				}

				// VaR Portfolio Risk Check: block trades that would push total risk beyond limit
				exec.Mu.RLock()
				var currentVaRPositions []core.VaRPosition
				for _, t := range exec.ActiveTrades {
					currentVaRPositions = append(currentVaRPositions, core.VaRPosition{
						Token: t.Token, Symbol: t.Symbol,
						EntryPrice: t.EntryPrice, Qty: t.RemainingQty, IsShort: t.IsShort,
					})
				}
				exec.Mu.RUnlock()
				newVaRPos := core.VaRPosition{
					Token: sig.Token, Symbol: sig.Symbol,
					EntryPrice: sig.EntryPrice, Qty: sig.Qty, IsShort: sig.IsShort,
				}
				if varAllowed, varReason := varEngine.CheckNewTrade(currentVaRPositions, newVaRPos); !varAllowed {
					log.Printf("[VaR] BLOCKED %s %s: %s", sig.Strategy, sig.Symbol, varReason)
					continue
				}

				if exec.Execute(sig, currentRegime) {
					executed++
					journal.LogAgentActivity("Execution", "TRADE_OPENED",
						fmt.Sprintf("%s %s Qty=%d Entry=%.1f SL=%.1f Target=%.1f",
							sig.Strategy, sig.Symbol, sig.Qty, sig.EntryPrice, sig.StopPrice, sig.TargetPrice),
						config.NowIST().Format("15:04:05"))
				}
			}
			execElapsed := time.Now().UnixNano() - execStart
			tickElapsed := time.Now().UnixNano() - tickStart

			// Performance logging
			if len(signals) > 0 || len(exec.ActiveTrades) > 0 {
				log.Printf("[PERF] Tick:%dns Scan:%dns(%d) Mon:%dns Exec:%dns(%d) Open:%d",
					tickElapsed, scanElapsed, len(signals),
					monElapsed, execElapsed, executed,
					len(exec.ActiveTrades))
			}

			if risk.EngineStopped && !killSwitchAlerted {
				log.Printf("[Engine] RISK STOP: %s", risk.StopReason)
				agents.SendTelegram(fmt.Sprintf("🛑 *ENGINE STOPPED*: `%s`", risk.StopReason))
				killSwitchAlerted = true
			}
		}

		// ── Post-Market Cleanup ──
		log.Println("[Engine] Day complete. Running EOD Synchronization...")
		dbPath := config.BaseDir + "/data/historical.db"
		errSync := dataAgent.SyncHistoricalEODData(dbPath)
		if errSync != nil {
			log.Printf("[Engine] ERROR: EOD Sync failed: %v", errSync)
			agents.SendTelegram(fmt.Sprintf("⚠️ *EOD SYNC FAILED*: %v", errSync))
		}

		// ── Walk-Forward Re-Optimization (uses today's new trades) ──
		newParams, wfoErr := wfo.RunOptimization()
		if wfoErr != nil {
			log.Printf("[WFO] Re-optimization failed: %v", wfoErr)
		} else {
			log.Printf("[WFO] Re-optimized: IS_Sharpe=%.2f OS_Sharpe=%.2f Trades=%d",
				newParams.InSampleSharpe, newParams.OutSampleSharpe, newParams.TotalTradesUsed)
		}

		// ML Filter removed — no retrain needed

		log.Println("[Engine] Post-Market Cleanup complete. Trading paused. Sleeping until next morning...")
		agents.SendTelegram("🏁 *ENGINE SHUTDOWN* — Trading day complete. Process sleeping.")
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
	const url = "http://127.0.0.1:8085"
	time.Sleep(1 * time.Second) // Let API server bind

	// Verify the server is actually responding before opening the browser
	client := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < 5; i++ {
		resp, err := client.Get(url + "/api/health")
		if err == nil {
			resp.Body.Close()
			log.Printf("[UI] Dashboard ready — opening %s", url)
			exec.Command("cmd", "/c", "start", url).Start()
			return
		}
		log.Printf("[UI] Waiting for API server (attempt %d/5)...", i+1)
		time.Sleep(1 * time.Second)
	}
	log.Println("[UI] WARNING: API server not responding — dashboard may not load")
	// Still try to open it
	exec.Command("cmd", "/c", "start", url).Start()
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

// killOldProcess kills any zombie process occupying the given port.
// This handles the common case where a previous engine crash left a process
// holding the port, preventing the new server from binding.
func killOldProcess(port string) {
	// Use netstat to find the PID using this port
	out, err := exec.Command("cmd", "/c",
		fmt.Sprintf("netstat -ano | findstr :%s | findstr LISTENING", port)).Output()
	if err != nil || len(out) == 0 {
		return // Port is free
	}

	// Parse the PID from netstat output (last column)
	lines := fmt.Sprintf("%s", out)
	for _, line := range splitLines(lines) {
		fields := splitFields(line)
		if len(fields) < 5 {
			continue
		}
		pid := fields[len(fields)-1]
		if pid == "0" {
			continue
		}
		log.Printf("[Engine] Killing zombie process on port %s (PID=%s)", port, pid)
		exec.Command("taskkill", "/F", "/PID", pid).Run()
		time.Sleep(500 * time.Millisecond) // Let the OS release the port
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
