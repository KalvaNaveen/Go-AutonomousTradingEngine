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

	// Per-symbol re-entry cooldown to prevent revenge trading
	lastExitTime map[string]time.Time

	// Broker interface (abstracted for paper/live switching)
	PlaceOrder   func(symbol string, qty int, isShort bool, orderType string, price float64) (string, error)
	CancelOrder  func(orderID string) error
	GetLTP       func(uint32) float64
}

func NewExecutionAgent(risk *RiskAgent, journal *core.Journal, state *core.StateManager) *ExecutionAgent {
	return &ExecutionAgent{
		Risk:         risk,
		Journal:      journal,
		State:        state,
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

	// Duplicate symbol guard
	e.Mu.RLock()
	for _, t := range e.ActiveTrades {
		if t.Symbol == sym {
			e.Mu.RUnlock()
			log.Printf("[Exec] REJECTED %s: Already holding position", sym)
			return false
		}
	}
	// Per-symbol re-entry cooldown (15 minutes after last exit)
	if lastExit, ok := e.lastExitTime[sym]; ok {
		if time.Since(lastExit).Minutes() < 15 {
			e.Mu.RUnlock()
			log.Printf("[Exec] REJECTED %s: Cooldown active (%.0fm since last exit)", sym, time.Since(lastExit).Minutes())
			return false
		}
	}
	e.Mu.RUnlock()

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

	SendTelegram(fmt.Sprintf(
		"*EXECUTED %s (%s) -- Qty:%d | R:R %.2f*\n`%s` | `%s`\nEntry: Rs.`%.2f` | Qty: `%d`\nTarget: Rs.`%.2f` | Stop: Rs.`%.2f`",
		sig.Strategy, direction, qty, rr,
		sym, regime, sig.EntryPrice, qty,
		sig.TargetPrice, sig.StopPrice))

	return true
}

// MonitorPositions checks all open positions for exits.
// Implements 5 layers of position management:
//   1. Trailing stop loss — locks in profits as price moves
//   2. Breakeven stop — once at 1R profit, stop moves to entry
//   3. Time decay exit — stale trades that go nowhere after 30 mins
//   4. Dynamic net-profit exit — exit when net profit (after charges) meets threshold
//   5. Fixed stop/target hit — original SL/TP from entry
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
		charges := core.ComputeChargesFromTrade(trade.EntryPrice, ltp, actQty, trade.IsShort, "MIS", 0)
		netPnl := gross - charges

		// ═══ LAYER 1: TRAILING STOP LOSS ═══
		// Once trade moves >1R in our favor, trail the stop at 50% of max favorable excursion
		initialRisk := 0.0
		if trade.IsShort {
			initialRisk = trade.StopPrice - trade.EntryPrice
		} else {
			initialRisk = trade.EntryPrice - trade.StopPrice
		}

		if initialRisk > 0 {
			var favorableMove float64
			if trade.IsShort {
				favorableMove = trade.EntryPrice - ltp // positive when price drops
			} else {
				favorableMove = ltp - trade.EntryPrice // positive when price rises
			}

			// Once at 1R profit: move stop to breakeven (LAYER 2)
			if favorableMove >= initialRisk {
				var newStop float64
				if trade.IsShort {
					// Trail at 30% of max favorable move from entry (loosened from 50%)
					newStop = trade.EntryPrice - favorableMove*0.30
					if newStop < trade.StopPrice { // Only tighten, never widen
						trade.StopPrice = newStop
					}
				} else {
					newStop = trade.EntryPrice + favorableMove*0.30
					if newStop > trade.StopPrice {
						trade.StopPrice = newStop
					}
				}
			}

			// Once at 2R profit: trail tighter at 65%
			if favorableMove >= 2*initialRisk {
				var newStop float64
				if trade.IsShort {
					newStop = trade.EntryPrice - favorableMove*0.50
					if newStop < trade.StopPrice {
						trade.StopPrice = newStop
					}
				} else {
					newStop = trade.EntryPrice + favorableMove*0.50
					if newStop > trade.StopPrice {
						trade.StopPrice = newStop
					}
				}
			}
		}

		// ═══ LAYER 3: TIME DECAY EXIT ═══
		// If a trade hasn't moved meaningfully, exit at breakeven/small loss to free up capital
		holdMinutes := now.Sub(trade.EntryTime).Minutes()
		maxHold := 180.0 // Default 3 hours
		if trade.MaxHoldMins > 0 {
			maxHold = float64(trade.MaxHoldMins)
		}

		// Decay limits depend heavily on the strategy paradigm
		// S10_ and S13_ are mean-reversion/rotation — they need 120m+ to work.
		// Only true momentum strategies (breakout/scalp) get the short 30m decay.
		isMomentum := strings.HasPrefix(trade.Strategy, "S1_") || 
					  strings.HasPrefix(trade.Strategy, "S3_") || 
					  strings.HasPrefix(trade.Strategy, "S6_") ||
					  strings.HasPrefix(trade.Strategy, "S14_") ||
					  strings.HasPrefix(trade.Strategy, "S15_")

		decayLimit := 30.0
		if !isMomentum {
			decayLimit = 120.0 // Give mean reverting / sectoral trades 2 hours to breathe
		}

		// Stale trade: held > decayLimit mins and P&L is near zero (< ₹100)
		if holdMinutes > decayLimit && gross > -100 && gross < 100 {
			
			// --- SMART DECAY ENHANCEMENT ---
			// If the timer runs out, check if favorable momentum is suddenly returning.
			isSurging := false
			if e.Scanner != nil && e.Scanner.ComputeRVol != nil && e.Scanner.GetVWAP != nil {
				rvol := e.Scanner.ComputeRVol(token)
				vwap := e.Scanner.GetVWAP(token)
				
				if rvol > 1.3 { // Relative volume is highly active
					if !trade.IsShort && ltp > vwap { // Long trade trending above VWAP
						isSurging = true
					} else if trade.IsShort && ltp < vwap { // Short trade trending below VWAP
						isSurging = true
					}
				}
			}

			// Grant a 60-minute grace period if the stock wakes up right at the decay boundary
			if isSurging && holdMinutes < decayLimit+60.0 {
				continue // Bypass the kill switch
			}
			
			// Trade is going nowhere — exit to free up capital for better setups
			log.Printf("[Exec] TIME_DECAY: %s (Strat: %s) held %.0f mins with flat PnL=%.0f — exiting", trade.Symbol, trade.Strategy, holdMinutes, gross)
			e.forceExit(oid, trade, "TIME_DECAY_EXIT", ltp)
			continue
		}

		// Max hold time exceeded
		if holdMinutes >= maxHold {
			e.forceExit(oid, trade, "MAX_HOLD_EXIT", ltp)
			continue
		}

		// ═══ LAYER 4: DYNAMIC NET-PROFIT EXIT ═══
		// Exit when net profit (after ALL charges) reaches a meaningful threshold
		// Scaled by position size: min(₹100, 1.5% of position value)
		positionValue := trade.EntryPrice * float64(actQty)
		minNetTarget := min64(100.0, positionValue*0.015)
		if minNetTarget < 30 {
			minNetTarget = 30 // Absolute minimum ₹30 net
		}

		if netPnl >= minNetTarget {
			log.Printf("[Exec] NET_PROFIT_EXIT: %s net=₹%.0f (gross=₹%.0f charges=₹%.0f)",
				trade.Symbol, netPnl, gross, charges)
			SendTelegram(fmt.Sprintf("💰 *NET PROFIT* `%s` net=₹%.0f — Exiting.", trade.Symbol, netPnl))
			e.forceExit(oid, trade, "NET_PROFIT_EXIT", ltp)
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

		// ═══ LAYER 5: FIXED STOP/TARGET HIT ═══
		if trade.IsShort {
			if ltp >= trade.StopPrice {
				e.forceExit(oid, trade, "STOP_HIT", ltp)
				continue
			}
			if ltp <= trade.TargetPrice {
				e.forceExit(oid, trade, "TARGET_HIT", ltp)
				continue
			}
		} else {
			if ltp <= trade.StopPrice {
				e.forceExit(oid, trade, "STOP_HIT", ltp)
				continue
			}
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
func SendTelegram(msg string) {
	if config.TelegramBotToken == "" || len(config.TelegramChatIDs) == 0 {
		log.Printf("[ALERT] %s", msg)
		return
	}
	base := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", config.TelegramBotToken)
	for _, chatID := range config.TelegramChatIDs {
		go func(cid string) {
			resp, err := http.PostForm(base, url.Values{
				"chat_id":    {cid},
				"text":       {msg},
				"parse_mode": {"Markdown"},
			})
			if err == nil {
				resp.Body.Close()
			}
		}(chatID)
	}
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
