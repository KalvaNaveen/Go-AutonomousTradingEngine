package agents

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"bnf_go_engine/config"
	"bnf_go_engine/core"
)

// ExecutionAgent handles order placement, position monitoring, and trade lifecycle.
// Exact port of Python execution_agent.py
type ExecutionAgent struct {
	Risk         *RiskAgent
	Journal      *core.Journal
	State        *core.StateManager
	Scanner      *ScannerAgent
	ActiveTrades map[string]*core.Trade
	Mu           sync.RWMutex // Protects ActiveTrades from concurrent API/trading access

	// Per-symbol re-entry cooldown (3 minutes)
	lastExitTime map[string]time.Time

	// Broker interface (abstracted for paper/live switching)
	PlaceOrder  func(symbol string, qty int, isShort bool, orderType string, price float64) (string, error)
	CancelOrder func(orderID string) error
	GetLTP      func(uint32) float64
}

func NewExecutionAgent(risk *RiskAgent, journal *core.Journal, state *core.StateManager) *ExecutionAgent {
	return &ExecutionAgent{
		Risk:           risk,
		Journal:        journal,
		State:          state,
		ActiveTrades: make(map[string]*core.Trade),
		lastExitTime: make(map[string]time.Time),
	}
}

func (e *ExecutionAgent) RestoreFromState() {
	openPositions := e.State.LoadOpenPositions()
	if len(openPositions) == 0 {
		return
	}
	for _, trade := range openPositions {
		oid := trade.EntryOID
		e.ActiveTrades[oid] = trade
		e.Risk.RegisterOpen(oid, trade)
	}
	SendTelegram(fmt.Sprintf(
		"*CRASH RECOVERY*\nRestored `%d` open position(s) from state.",
		len(openPositions)))
	log.Printf("[ExecutionAgent] Restored %d positions from state", len(openPositions))
}

func (e *ExecutionAgent) Execute(sig *Signal, regime string) bool {
	sym := sig.Symbol

	// 3-minute per-symbol cooldown to prevent revenge trading
	if exitTime, ok := e.lastExitTime[sym]; ok {
		if time.Since(exitTime) < 3*time.Minute {
			log.Printf("[Exec] COOLDOWN %s: exited %.0fs ago, need 180s", sym, time.Since(exitTime).Seconds())
			return false
		}
	}

	// Duplicate symbol guard
	e.Mu.RLock()
	for _, t := range e.ActiveTrades {
		if t.Symbol == sym {
			e.Mu.RUnlock()
			log.Printf("[Exec] REJECTED %s: Already holding position", sym)
			return false
		}
	}
	e.Mu.RUnlock()

	// Trust the scanner's stop price — it's computed from ATR/pivot/structure.
	// Only sanity-check: stop must be within 5% of entry (prevents data errors).
	maxSL := sig.EntryPrice * 0.05
	if sig.IsShort {
		if sig.StopPrice - sig.EntryPrice > maxSL {
			sig.StopPrice = sig.EntryPrice + maxSL
		}
	} else {
		if sig.EntryPrice - sig.StopPrice > maxSL {
			sig.StopPrice = sig.EntryPrice - maxSL
		}
	}

	approved, reason := e.Risk.ApproveTrade(map[string]interface{}{
		"strategy":     sig.Strategy,
		"symbol":       sig.Symbol,
		"regime":       regime,
		"entry_price":  sig.EntryPrice,
		"stop_price":   sig.StopPrice,
		"target_price": sig.TargetPrice,
		"is_short":     sig.IsShort,
	})
	if !approved {
		log.Printf("[Exec] REJECTED %s: %s", sym, reason)
		return false
	}

	qty := e.Risk.CalculatePositionSize(
		sig.EntryPrice, sig.StopPrice,
		regime, sig.Strategy, sym)
	if qty <= 0 {
		return false
	}

	// Determine order type: momentum strategies use MARKET
	isMomentum := strings.Contains(sig.Strategy, "S3") ||
		strings.Contains(sig.Strategy, "S6") ||
		strings.Contains(sig.Strategy, "S8") ||
		strings.Contains(sig.Strategy, "MACRO")

	orderType := "LIMIT"
	price := sig.EntryPrice
	if isMomentum {
		orderType = "MARKET"
		price = 0
	} else {
		if sig.IsShort {
			price = sig.EntryPrice * 0.998
		} else {
			price = sig.EntryPrice * 1.002
		}
	}

	// Place order
	var oid string
	var err error
	if e.PlaceOrder != nil {
		oid, err = e.PlaceOrder(sym, qty, sig.IsShort, orderType, price)
		if err != nil {
			SendTelegram(fmt.Sprintf("ORDER FAILED: `%s`\n`%v`", sym, err))
			return false
		}
	} else {
		// Paper mode fallback
		oid = fmt.Sprintf("GO_%s_%s_%d", sig.Strategy, sym, time.Now().UnixNano())
		log.Printf("[Paper] Order filled: %s", oid)
	}

	now := config.NowIST()
	trade := &core.Trade{
		EntryOID:      oid,
		Symbol:        sym,
		Strategy:      sig.Strategy,
		Product:       "MIS",
		Regime:        regime,
		EntryPrice:    sig.EntryPrice,
		StopPrice:     sig.StopPrice,
		PartialTarget: sig.PartialTarget,
		TargetPrice:   sig.TargetPrice,
		Qty:           qty,
		RemainingQty:  qty,
		Token:         sig.Token,
		IsShort:       sig.IsShort,
		EntryTime:     now,
		EntryDate:     config.TodayIST(),
		RVol:          sig.RVol,
		RSI:           sig.RSI,
		ADX:           sig.ADX,
		VIX:           e.Scanner.GetCurrentVIX(),
		ADRatio:       e.Scanner.GetCurrentADRatio(),
		MaxHoldMins:   sig.MaxHoldMins,
	}

	// Persist immediately (crash-safe)
	e.State.Save(oid, trade)
	e.Mu.Lock()
	e.ActiveTrades[oid] = trade
	e.Mu.Unlock()
	e.Risk.RegisterOpen(oid, trade)

	// R:R calculation for alert
	var riskPts, rewardPts float64
	if sig.IsShort {
		riskPts = sig.StopPrice - sig.EntryPrice
		rewardPts = sig.EntryPrice - sig.TargetPrice
	} else {
		riskPts = sig.EntryPrice - sig.StopPrice
		rewardPts = sig.TargetPrice - sig.EntryPrice
	}
	rr := rewardPts / max64(riskPts, 0.01)

	direction := "LONG"
	if sig.IsShort {
		direction = "SHORT"
	}

	leverage := e.Risk.GetMISLeverage(sym)

	SendTelegram(fmt.Sprintf(
		"*EXECUTED %s (%s) -- Qty:%d | Lev: %.1fx | R:R %.2f*\n`%s` | `%s`\nEntry: Rs.`%.2f` | Qty: `%d`\nTarget: Rs.`%.2f` | Stop: Rs.`%.2f`",
		sig.Strategy, direction, qty, leverage, rr,
		sym, regime, sig.EntryPrice, qty,
		sig.TargetPrice, sig.StopPrice))

	return true
}

// MonitorPositions — the core position management loop.
// Rules:
//  1. Hard stop: cut trade if gross PnL crosses -₹50
//  2. Trailing stop: once gross hits +₹30, trail at 50% of peak
//  3. Profit-lock: protect gains with 3-tier high-water mark
//  4. EOD squareoff & preemptive loss exit
//  5. Fixed target hit from original signal
func (e *ExecutionAgent) MonitorPositions(regime string) {
	now := config.NowIST()
	h, m := now.Hour(), now.Minute()
	t := h*100 + m

	e.Mu.Lock()
	defer e.Mu.Unlock()

	for oid, trade := range e.ActiveTrades {
		token := trade.Token
		ltp := 0.0
		if e.GetLTP != nil && token > 0 {
			ltp = e.GetLTP(token)
		}
		if ltp <= 0 {
			continue
		}

		actQty := trade.RemainingQty
		if actQty == 0 {
			actQty = trade.Qty
		}

		// Compute current P&L
		var gross float64
		if trade.IsShort {
			gross = (trade.EntryPrice - ltp) * float64(actQty)
		} else {
			gross = (ltp - trade.EntryPrice) * float64(actQty)
		}

		// Compute the original risk for this trade (entry-to-stop distance × qty)
		var entryRiskPts float64
		if trade.IsShort {
			entryRiskPts = trade.StopPrice - trade.EntryPrice
		} else {
			entryRiskPts = trade.EntryPrice - trade.StopPrice
		}
		entryRisk := entryRiskPts * float64(actQty) // Total ₹ risk for this position
		if entryRisk < 10 {
			entryRisk = 10 // Safety floor
		}

		// ═══ RULE 1: HARD STOP — 1.5× the original risk ═══
		// Gives trades room to breathe past the scanner's stop before cutting.
		// E.g., 20-share position with ₹15 stop = ₹300 risk → hard stop at -₹450
		hardStopLimit := -1.5 * entryRisk
		if gross <= hardStopLimit {
			log.Printf("[Exec] HARD_STOP: %s gross=₹%.0f (limit=₹%.0f, risk=₹%.0f) — cutting loss",
				trade.Symbol, gross, hardStopLimit, entryRisk)
			SendTelegram(fmt.Sprintf("🛑 *HARD STOP* `%s` gross=₹%.0f — Cutting loss.", trade.Symbol, gross))
			e.forceExit(oid, trade, "HARD_STOP", ltp)
			continue
		}

		// Track peak gross P&L for trailing stop
		if gross > trade.PeakPnl {
			trade.PeakPnl = gross
		}

		// ═══ RULE 2: TRAILING STOP — activates at 1R profit, trails at 50% ═══
		// Once you've made back your full risk amount, protect 50% of peak.
		trailActivation := entryRisk * 0.8 // Activate trailing at 0.8R
		if trade.PeakPnl >= trailActivation {
			trailFloor := trade.PeakPnl * 0.50
			if gross <= trailFloor {
				log.Printf("[Exec] TRAIL_STOP: %s peak=₹%.0f now=₹%.0f trail=₹%.0f",
					trade.Symbol, trade.PeakPnl, gross, trailFloor)
				SendTelegram(fmt.Sprintf("📉 *TRAIL STOP* `%s` Peak ₹%.0f → Now ₹%.0f. Locking gains.",
					trade.Symbol, trade.PeakPnl, gross))
				e.forceExit(oid, trade, "TRAIL_STOP", ltp)
				continue
			}
		}

		// ═══ RULE 3: PROFIT-LOCK / HIGH-WATER MARK (scaled to risk) ═══
		// Tiers based on R-multiples, not flat ₹ amounts
		lockedFloor := -999999.0
		switch {
		case trade.PeakPnl >= 4.0*entryRisk: // 4R peak → lock 2.5R
			lockedFloor = 2.5 * entryRisk
		case trade.PeakPnl >= 2.0*entryRisk: // 2R peak → lock 1R
			lockedFloor = 1.0 * entryRisk
		case trade.PeakPnl >= 1.0*entryRisk: // 1R peak → lock 0.3R
			lockedFloor = 0.3 * entryRisk
		}

		if lockedFloor > -999999.0 && gross <= lockedFloor {
			log.Printf("[Exec] PROFIT_LOCK: %s peak=₹%.0f now=₹%.0f floor=₹%.0f",
				trade.Symbol, trade.PeakPnl, gross, lockedFloor)
			SendTelegram(fmt.Sprintf("🔒 *PROFIT LOCK* `%s` Peak ₹%.0f → Now ₹%.0f (floor ₹%.0f).",
				trade.Symbol, trade.PeakPnl, gross, lockedFloor))
			e.forceExit(oid, trade, "PROFIT_LOCK_EXIT", ltp)
			continue
		}

		// ═══ THESIS EXPIRED — 2hr max hold if losing ═══
		// If the trade hasn't moved to profit in 2 hours, the setup is dead.
		// Apr 29: LENSKART held 5.5hrs losing (-₹1,616), AUROPHARMA 4.3hrs (-₹1,516)
		holdDuration := config.NowIST().Sub(trade.EntryTime)
		if holdDuration > 2*time.Hour && gross < 0 {
			log.Printf("[Exec] THESIS_EXPIRED: %s held %.1fhrs while losing ₹%.0f — cutting dead trade",
				trade.Symbol, holdDuration.Hours(), gross)
			SendTelegram(fmt.Sprintf("⏰ *THESIS EXPIRED* `%s` held %.1fhrs losing ₹%.0f — Dead trade cut.",
				trade.Symbol, holdDuration.Hours(), gross))
			e.forceExit(oid, trade, "THESIS_EXPIRED", ltp)
			continue
		}

		// ═══ DAMAGE CONTROL — tighten stop at -1R ═══
		// Once a trade is losing by 1× its original risk, the thesis is weak.
		// Start a tight trailing stop: if it recovers to -0.7R from -1R, let it ride.
		// If it deteriorates further past -1R, the hard stop at -1.5R catches it.
		// This prevents the "slow bleed" where trades sit at -0.8R to -1.2R for hours.
		if gross < -1.0*entryRisk && trade.PeakPnl < 0.3*entryRisk {
			// The trade never really worked (peak was <0.3R) and now it's -1R deep
			log.Printf("[Exec] DAMAGE_CONTROL: %s at -%.1fR (gross=₹%.0f, risk=₹%.0f) — never worked, cutting",
				trade.Symbol, -gross/entryRisk, gross, entryRisk)
			SendTelegram(fmt.Sprintf("🩹 *DAMAGE CTRL* `%s` at -%.1fR — Trade never worked, cutting.",
				trade.Symbol, -gross/entryRisk))
			e.forceExit(oid, trade, "DAMAGE_CONTROL", ltp)
			continue
		}

		// ═══ EOD SQUAREOFF ═══
		sqH, sqM := config.ParseTime(config.EODSquareoffTime)
		if t >= sqH*100+sqM {
			e.forceExit(oid, trade, "MIS_EOD_SQUAREOFF", ltp)
			continue
		}

		// ═══ PREEMPTIVE LOSS EXIT (14:50+) ═══
		peH, peM := config.ParseTime(config.PreemptiveExitTime)
		if t >= peH*100+peM && gross < 0 {
			e.forceExit(oid, trade, "PREEMPTIVE_LOSS_EXIT", ltp)
			continue
		}

		// ═══ FIXED TARGET HIT ═══
		if trade.IsShort {
			if ltp <= trade.TargetPrice {
				e.forceExit(oid, trade, "TARGET_HIT", ltp)
				continue
			}
		} else {
			if ltp >= trade.TargetPrice {
				e.forceExit(oid, trade, "TARGET_HIT", ltp)
				continue
			}
		}
	}
}

func (e *ExecutionAgent) forceExit(oid string, trade *core.Trade, reason string, exitPrice float64) {
	// Cancel pending orders
	if e.CancelOrder != nil {
		if trade.SLOID != "" {
			e.CancelOrder(trade.SLOID)
		}
		if trade.PartialOID != "" {
			e.CancelOrder(trade.PartialOID)
		}
		if trade.TargetOID != "" {
			e.CancelOrder(trade.TargetOID)
		}
	}

	exitQty := trade.RemainingQty
	if exitQty == 0 {
		exitQty = trade.Qty
	}

	// Place exit order
	if e.PlaceOrder != nil {
		_, err := e.PlaceOrder(trade.Symbol, exitQty, !trade.IsShort, "MARKET", 0)
		if err != nil {
			SendTelegram(fmt.Sprintf("FORCE EXIT FAILED: `%s` -- %v", trade.Symbol, err))
			return
		}
	}

	if exitPrice <= 0 {
		exitPrice = trade.EntryPrice // safe fallback
	}

	pnl := e.Risk.ClosePosition(oid, exitPrice)
	e.State.Close(oid)
	e.Journal.LogTrade(&core.TradeLog{
		Symbol:        trade.Symbol,
		Strategy:      trade.Strategy,
		Regime:        trade.Regime,
		RVol:          trade.RVol,
		DeviationPct:  trade.DeviationPct,
		EntryPrice:    trade.EntryPrice,
		FullExitPrice: exitPrice,
		Qty:           exitQty,
		GrossPnl:      pnl,
		ExitReason:    reason,
		EntryTime:     trade.EntryTime,
		ExitTime:      config.NowIST(),
		DailyPnlAfter: e.Risk.DailyPnl,
		IsShort:       trade.IsShort,
	})

	// Track scanner state
	if e.Scanner != nil {
		e.Scanner.RecordPnl(pnl)
		e.Scanner.RecordStrategyResult(trade.Strategy, pnl)
		if reason == "STOP_HIT" {
			e.Scanner.symbolCooldown[trade.Symbol] = 2
		}
	}

	streakMsg := ""
	if e.Risk.ConsecutiveLosses > 0 {
		streakMsg = fmt.Sprintf("\nStreak: `%d/%d`", e.Risk.ConsecutiveLosses, config.MaxConsecutiveLosses)
	}
	SendTelegram(fmt.Sprintf(
		"*FORCE EXIT*\n`%s` | `%s`\nEst. PnL: Rs.`%+.0f`%s",
		trade.Symbol, reason, pnl, streakMsg))

	// Record exit time for per-symbol cooldown
	e.lastExitTime[trade.Symbol] = config.NowIST()
	delete(e.ActiveTrades, oid)
}

func (e *ExecutionAgent) FlattenAll(reason string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()

	flattened := 0
	for oid, trade := range e.ActiveTrades {
		ltp := 0.0
		if e.GetLTP != nil && trade.Token > 0 {
			ltp = e.GetLTP(trade.Token)
		}
		e.forceExit(oid, trade, reason, ltp)
		flattened++
	}
	if flattened > 0 {
		SendTelegram(fmt.Sprintf("*[flatten_all]* Force exited `%d` positions: `%s`", flattened, reason))
	}
}

func (e *ExecutionAgent) DailySummaryAlert(regime string) {
	stats := e.Risk.GetDailyStats()
	e.Journal.LogDailySummary(stats, regime, e.Risk.EngineStopped, e.Risk.StopReason)

	SendTelegram(fmt.Sprintf(
		"📊 *DAILY SUMMARY*\nRegime: `%s`\nTrades: `%v` | Wins: `%v` | WR: `%.1f%%`\nP&L: Rs.`%.2f`\nCapital: Rs.`%.0f`",
		regime, stats["total"], stats["wins"], stats["win_rate"],
		stats["gross_pnl"], stats["capital"]))
}

// ── Telegram Alert ──────────────────────────────────────────
// Rate-limited sender: Telegram enforces ~30 msg/sec but can silently drop
// rapid-fire messages to the same chat. We serialize sends with a 300ms gap.
var telegramMu sync.Mutex

func SendTelegram(msg string) {
	if config.TelegramBotToken == "" || len(config.TelegramChatIDs) == 0 {
		log.Printf("[ALERT] %s", msg)
		return
	}
	// Send in background but serialize to prevent rate-limit drops
	go func() {
		telegramMu.Lock()
		defer telegramMu.Unlock()

		base := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", config.TelegramBotToken)
		for _, chatID := range config.TelegramChatIDs {
			sent := false
			for attempt := 0; attempt < 2; attempt++ {
				resp, err := http.PostForm(base, url.Values{
					"chat_id":    {chatID},
					"text":       {msg},
					"parse_mode": {"Markdown"},
				})
				if err != nil {
					log.Printf("[Telegram] ERROR (attempt %d): %v", attempt+1, err)
					time.Sleep(1 * time.Second) // Retry after 1s
					continue
				}
				resp.Body.Close()
				if resp.StatusCode == 200 {
					sent = true
					break
				}
				log.Printf("[Telegram] HTTP %d (attempt %d) for chat %s", resp.StatusCode, attempt+1, chatID)
				time.Sleep(1 * time.Second)
			}
			if !sent {
				log.Printf("[Telegram] FAILED to send to chat %s: %s", chatID, msg[:min(len(msg), 80)])
			}
			time.Sleep(300 * time.Millisecond) // Rate limit gap between chats
		}
	}()
}

func min(a, b int) int {
	if a < b { return a }
	return b
}

// SectorCount returns how many active trades are in the same sector as the symbol.
// Uses a simple heuristic: symbols sharing the same sector suffix pattern.
var sectorMap = map[string]string{
	// Banking
	"HDFCBANK": "BANK", "ICICIBANK": "BANK", "SBIN": "BANK", "KOTAKBANK": "BANK",
	"AXISBANK": "BANK", "INDUSINDBK": "BANK", "BANDHANBNK": "BANK", "FEDERALBNK": "BANK",
	"PNB": "BANK", "BANKBARODA": "BANK", "IDFCFIRSTB": "BANK", "AUBANK": "BANK",
	// IT
	"TCS": "IT", "INFY": "IT", "WIPRO": "IT", "HCLTECH": "IT", "TECHM": "IT",
	"LTIM": "IT", "MPHASIS": "IT", "COFORGE": "IT", "PERSISTENT": "IT",
	// Pharma
	"SUNPHARMA": "PHARMA", "DRREDDY": "PHARMA", "CIPLA": "PHARMA", "DIVISLAB": "PHARMA",
	"APOLLOHOSP": "PHARMA", "BIOCON": "PHARMA", "LUPIN": "PHARMA",
	// Auto
	"MARUTI": "AUTO", "TATAMOTORS": "AUTO", "M&M": "AUTO", "BAJAJ-AUTO": "AUTO",
	"HEROMOTOCO": "AUTO", "EICHERMOT": "AUTO", "ASHOKLEY": "AUTO",
	// Metal
	"TATASTEEL": "METAL", "JSWSTEEL": "METAL", "HINDALCO": "METAL", "VEDL": "METAL",
	"COALINDIA": "METAL", "NMDC": "METAL",
	// Oil & Gas
	"RELIANCE": "ENERGY", "ONGC": "ENERGY", "BPCL": "ENERGY", "IOC": "ENERGY",
	"GAIL": "ENERGY", "POWERGRID": "ENERGY", "NTPC": "ENERGY", "ADANIGREEN": "ENERGY",
	// FMCG
	"HINDUNILVR": "FMCG", "ITC": "FMCG", "NESTLEIND": "FMCG", "BRITANNIA": "FMCG",
	"DABUR": "FMCG", "GODREJCP": "FMCG", "MARICO": "FMCG", "TATACONSUM": "FMCG",
	// Financials (non-bank)
	"BAJFINANCE": "NBFC", "BAJAJFINSV": "NBFC", "HDFCLIFE": "NBFC", "SBILIFE": "NBFC",
	"ICICIPRULI": "NBFC", "CHOLAFIN": "NBFC", "SHRIRAMFIN": "NBFC",
}

func getSector(symbol string) string {
	if s, ok := sectorMap[symbol]; ok {
		return s
	}
	return "OTHER_" + symbol // unique sector for unknown symbols
}

func (e *ExecutionAgent) SectorCount(symbol string) int {
	sector := getSector(symbol)
	count := 0
	e.Mu.RLock()
	for _, trade := range e.ActiveTrades {
		if getSector(trade.Symbol) == sector {
			count++
		}
	}
	e.Mu.RUnlock()
	return count
}

func max64(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func min64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
