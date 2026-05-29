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
	"bnf_go_engine/data"
)

// ══════════════════════════════════════════════════════════════
//  ExecutionAgent — Harsh's Swing Trading Execution
// ══════════════════════════════════════════════════════════════
//  Phase 4: Max 5 stocks, 10% hard SL.
//  Phase 5: 21 EMA trailing exit (2 red candles below EMA).
//           Re-entry when stock reclaims EMA.

type ExecutionAgent struct {
	Journal      *core.Journal
	State        *core.StateManager
	Scanner      *ScannerAgent
	ActiveTrades map[string]*core.Trade
	Mu           sync.RWMutex

	PlaceOrder  func(symbol string, qty int, isShort bool, orderType string, price float64) (string, error)
	CancelOrder func(orderID string) error
	CancelGTT   func(triggerID int) error // nil if GTT not configured
	GetLTP      func(uint32) float64

	// FillMonitor is wired by main.go in live mode to poll entry order status,
	// detect partial fills / cancellations, and place SL+target after the entry fills.
	// When nil (paper mode), Execute treats the LIMIT entry as immediately filled.
	FillMonitor *core.FillMonitor

	// Phase 5: Track daily closes below 21 EMA per position
	RedCandlesBelow map[string]int // entryOID → consecutive red candles below 21 EMA

	// Phase 5: Track recently exited symbols for re-entry
	RecentlyExitedByEMA map[string]uint32  // symbol → token (exited by 21 EMA rule)
	EMAExitPrices       map[string]float64 // FIX-05: symbol → exit price (for re-entry cap)

	// FIX-10: Graduated recovery tracking
	ConsecutiveWins int

	// FIX-13: Circuit detection — track last known price per token
	LastKnownLTP    map[uint32]float64    // token → last seen LTP
	LastLTPChangeAt map[uint32]time.Time  // token → time when LTP last changed
	CircuitAlerted  map[uint32]bool       // token → already sent circuit alert

	// ExitInProgress prevents placing a second exit order while one is in-flight.
	// Guards against the engine calling forceExit concurrently with a broker SL-M fill.
	ExitInProgress map[string]bool

	// RecentlyExitedByEMAAt tracks when each symbol was EMA-exited, for stale-entry cleanup.
	RecentlyExitedByEMAAt map[string]time.Time

	// Book Ch.8: Drawdown tracking (p.205 — "keep max drawdown below 5-7%")
	// PeakCapital = highest realised equity since engine start.
	// CumulativeRealizedPnL = net closed-trade PnL (positive or negative).
	// DrawdownHalted = true when drawdown from peak exceeds MaxDrawdownHaltPct.
	PeakCapital           float64
	CumulativeRealizedPnL float64
	DrawdownHalted        bool
}

// FIX-05: Re-entry max premium cap
// Blueprint says "3-4% premium" is expected cost. 6% gives buffer.
// ⚠️ EXTENSION: Cap is not in Blueprint. Set to 0 to disable.
const ReEntryMaxPremiumPct = 6.0

// FIX-10: Capital recovery ladder — ⚠️ EXTENSION: NOT in Blueprint
const (
	RecoveryTo60PctAfterWins  = 3 // 3 consecutive wins → 0.60
	RecoveryTo80PctAfterWins  = 5 // 5 consecutive wins → 0.80
	RecoveryTo100PctAfterWins = 7 // 7 consecutive wins → 1.00
)

// FIX-11: EMA reset buffer — how far above 21 EMA the close must be to reset
// the red-candle counter. Set to 0.0 to follow Blueprint literally (any
// non-red-below-EMA day resets). Set to 0.005 (0.5%) for stricter recovery.
// ⚠️ EXTENSION: Buffer value is not in Blueprint. Default 0.0 = Blueprint literal.
const EMAResetBuffer = 0.0

func NewExecutionAgent(journal *core.Journal, state *core.StateManager) *ExecutionAgent {
	return &ExecutionAgent{
		Journal:               journal,
		State:                 state,
		ActiveTrades:          make(map[string]*core.Trade),
		RedCandlesBelow:       make(map[string]int),
		RecentlyExitedByEMA:   make(map[string]uint32),
		EMAExitPrices:         make(map[string]float64),
		LastKnownLTP:          make(map[uint32]float64),
		LastLTPChangeAt:       make(map[uint32]time.Time),
		CircuitAlerted:        make(map[uint32]bool),
		ExitInProgress:        make(map[string]bool),
		RecentlyExitedByEMAAt: make(map[string]time.Time),
		PeakCapital:           config.TotalCapital, // initialise to starting capital
	}
}

// computeOpenRiskPct returns the total open risk across all active positions
// as a percentage of TotalCapital.
// Open risk = Σ (EntryPrice − StopPrice) × RemainingQty  (Book Ch.8 p.194)
func (e *ExecutionAgent) computeOpenRiskPct() float64 {
	total := 0.0
	for _, trade := range e.ActiveTrades {
		if trade.EntryPrice > trade.StopPrice && trade.StopPrice > 0 {
			total += (trade.EntryPrice - trade.StopPrice) * float64(trade.RemainingQty)
		}
	}
	if config.TotalCapital <= 0 {
		return 0
	}
	return total / config.TotalCapital * 100
}

// updateDrawdown recomputes the current drawdown from peak equity and sets DrawdownHalted.
// Called after every trade close so the guard is always current.
func (e *ExecutionAgent) updateDrawdown(closedPnL float64) {
	e.CumulativeRealizedPnL += closedPnL
	currentCapital := config.TotalCapital + e.CumulativeRealizedPnL
	if currentCapital > e.PeakCapital {
		e.PeakCapital = currentCapital
	}
	if e.PeakCapital <= 0 {
		return
	}
	drawdownPct := (e.PeakCapital - currentCapital) / e.PeakCapital * 100
	wasHalted := e.DrawdownHalted
	e.DrawdownHalted = drawdownPct >= config.MaxDrawdownHaltPct
	if e.DrawdownHalted && !wasHalted {
		log.Printf("[Drawdown] HALT: drawdown %.1f%% ≥ %.0f%% — no new positions until recovery",
			drawdownPct, config.MaxDrawdownHaltPct)
		SendTelegram(fmt.Sprintf(
			"🛑 *DRAWDOWN HALT*\nDrawdown: `%.1f%%` ≥ `%.0f%%`\nNo new positions until equity recovers.\nBook Ch.8 rule: keep max drawdown < 7%%",
			drawdownPct, config.MaxDrawdownHaltPct))
	} else if !e.DrawdownHalted && wasHalted {
		log.Printf("[Drawdown] RESUMED: drawdown recovered below %.0f%%", config.MaxDrawdownHaltPct)
		SendTelegram(fmt.Sprintf("✅ *DRAWDOWN HALT LIFTED* — resuming normal trading (drawdown recovered below %.0f%%)", config.MaxDrawdownHaltPct))
	}
}

func (e *ExecutionAgent) RestoreFromState() {
	openPositions := e.State.LoadOpenPositions()
	if len(openPositions) == 0 {
		return
	}
	for _, trade := range openPositions {
		oid := trade.EntryOID
		// Defensive fix: Recompute target if it was stored as 0 (data corruption / old bug)
		if trade.TargetPrice == 0 {
			// TargetPrice=0 means trailing exit (no fixed target) — this is correct, no repair needed
		}
		e.ActiveTrades[oid] = trade
	}
	log.Printf("[ExecutionAgent] Restored %d swing positions from state", len(openPositions))
}

// ══════════════════════════════════════════════════════════════
//  Execute — Open a new swing position
// ══════════════════════════════════════════════════════════════

func (e *ExecutionAgent) Execute(sig *Signal, regime string) bool {
	sym := sig.Symbol

	// Defense: warn and attempt self-heal if token is missing.
	// A token=0 position won't receive LTP ticks → SL monitoring silently skips it.
	if sig.Token == 0 {
		if e.Scanner != nil {
			for tok, s := range e.Scanner.Universe {
				if s == sym {
					sig.Token = tok
					log.Printf("[Execute] Self-healed token for %s: %d", sym, tok)
					break
				}
			}
		}
		if sig.Token == 0 {
			log.Printf("[Execute] ⚠️ Signal for %s has token=0 — LTP monitoring will be DISABLED!", sym)
		}
	}

	// ── Book Ch.7 p.191 — Survive & Thrive: 2-underwater-positions gate ────────
	// "If two of your open stock positions are not performing well, you have no
	//  reason to open a third position."
	// Count open positions where current LTP < entry price (underwater).
	// When ≥ 2 are underwater, block all new non-topup entries.
	if sig.Strategy != "VCP_TOPUP" && e.GetLTP != nil {
		e.Mu.RLock()
		underwaterCount := 0
		for _, t := range e.ActiveTrades {
			if ltp := e.GetLTP(t.Token); ltp > 0 && ltp < t.EntryPrice {
				underwaterCount++
			}
		}
		e.Mu.RUnlock()
		if underwaterCount >= 2 {
			log.Printf("[Execute] Ch.7 gate: %d underwater positions — blocking new entry for %s",
				underwaterCount, sym)
			return false
		}
	}

	// Book Ch.8: Drawdown halt — no new positions when drawdown > 7% (p.205)
	if e.DrawdownHalted && sig.Strategy != "VCP_TOPUP" {
		log.Printf("[Execute] DRAWDOWN HALT active — skipping %s (drawdown > %.0f%%)",
			sym, config.MaxDrawdownHaltPct)
		return false
	}

	// Book Ch.8: Open risk cap — total open risk across positions ≤ 4% of capital (p.194)
	// Checked before position count so we stop earlier when risk is saturated.
	e.Mu.RLock()
	openRisk := e.computeOpenRiskPct()
	e.Mu.RUnlock()
	if openRisk >= config.MaxOpenRiskPct && sig.Strategy != "VCP_TOPUP" {
		log.Printf("[Execute] Open risk cap reached (%.1f%% ≥ %.0f%%) — skipping %s",
			openRisk, config.MaxOpenRiskPct, sym)
		return false
	}

	// Dynamic position cap based on current capital
	maxPos := config.ComputeMaxPositions(config.TotalCapital)
	e.Mu.RLock()
	if sig.Strategy != "VCP_TOPUP" && len(e.ActiveTrades) >= maxPos {
		e.Mu.RUnlock()
		return false
	}
	// Don't double-buy same symbol (except for top-ups)
	if sig.Strategy != "VCP_TOPUP" {
		for _, t := range e.ActiveTrades {
			if t.Symbol == sym {
				e.Mu.RUnlock()
				return false
			}
		}
	}
	// Available capital check — ensure deployed positions haven't consumed the minimum
	// per-trade allocation. Prevents over-extension on top-ups beyond the 6-position limit.
	deployedCapital := 0.0
	for _, t := range e.ActiveTrades {
		deployedCapital += t.EntryPrice * float64(t.RemainingQty)
	}
	e.Mu.RUnlock()

	availableCapital := config.TotalCapital - deployedCapital
	minRequired := config.TotalCapital * config.MinTradeAllocPct / 100
	if availableCapital < minRequired {
		log.Printf("[Execute] Skipping %s — available capital ₹%.0f below minimum ₹%.0f (deployed=₹%.0f)",
			sym, availableCapital, minRequired, deployedCapital)
		return false
	}

	// (Book has no fixed cash-reserve rule; deployment is bounded by open-risk 4-5%
	// and position count 8-12 only — Ch.7 p.191, Ch.8 p.194-195.)

	qty := sig.Qty
	if qty <= 0 {
		// Capital sizing produced 0 shares — likely below per-trade allocation
		// or the stock is too expensive for the configured capital. Skip rather
		// than placing a meaningless 1-share trade.
		log.Printf("[Execute] Skipping %s — computed qty=%d (capital/price mismatch)", sym, qty)
		return false
	}


	// Book Ch.7 p.191: Block new entries within ResultsAvoidanceDays of quarterly results.
	// Book says: "Avoid holding through earnings unless you have a significant profit
	// cushion to mitigate potential volatility." Extended here to also block new entries
	// before known results dates (NSE corporate actions calendar). ENGINE EXTENSION on
	// the spirit of the rule.
	// Fail-open: if NSE calendar is unavailable, never blocks.
	if HasUpcomingResults(sym) {
		days := DaysToResults(sym)
		log.Printf("[EarningsFilter] BLOCKED %s — results/board meeting in %d days (within %d-day window)",
			sym, days, ResultsAvoidanceDays)
		SendTelegram(fmt.Sprintf(
			"⚠️ *EARNINGS BLOCK — %s*\nBoard meeting / results in `%d` days.\nBook Ch.10: avoid new entries near results. Wait until post-result stability.",
			sym, days))
		return false
	}

	// For breakout strategies the signal fires at the live LTP, which is the breakout tick.
	// By the time the LIMIT order reaches the exchange, price has moved. Add a small buffer
	// above LTP so the order has a realistic chance of filling on fast breakout moves.
	limitPrice := sig.EntryPrice
	switch sig.Strategy {
	case "VCP_BREAKOUT", "IPO_BASE":
		limitPrice = sig.EntryPrice * 1.001 // 0.1% buffer — negligible cost, avoids misses
	}

	var oid string
	var err error
	if e.PlaceOrder != nil {
		oid, err = e.PlaceOrder(sym, qty, false, "LIMIT", limitPrice)
		if err != nil {
			SendTelegram(fmt.Sprintf("❌ ORDER FAILED: `%s`\n`%v`", sym, err))
			return false
		}
	} else {
		oid = fmt.Sprintf("GO_%s_%s_%d", sig.Strategy, sym, time.Now().UnixNano())
	}

	// Book Ch.12 p.283: 2R partial profit target.
	// "When profit = 2× initial risk, sell 50% of position, let rest run."
	// 2R target = entryPrice + 2 × (entryPrice - stopPrice)
	twoRTarget := 0.0
	twoRQty := 0
	if sig.StopPrice > 0 && sig.StopPrice < limitPrice {
		riskPerShare := limitPrice - sig.StopPrice
		twoRTarget = limitPrice + 2*riskPerShare
		twoRQty = int(float64(qty) * config.TwoRPartialSellPct / 100)
		if twoRQty < 1 && qty >= 2 {
			twoRQty = 1
		}
	}

	now := config.NowIST()
	trade := &core.Trade{
		EntryOID:     oid,
		Symbol:       sym,
		Strategy:     sig.Strategy,
		Product:      "CNC", // Always delivery for swing
		Regime:       regime,
		EntryPrice:   limitPrice,
		StopPrice:    sig.StopPrice,
		TargetPrice:  sig.TargetPrice,
		PartialTarget: twoRTarget, // 2R level for partial exit
		PartialQty:   twoRQty,    // qty to sell at 2R
		Qty:          qty,
		RemainingQty: qty,
		Token:        sig.Token,
		IsShort:      false, // Long only
		EntryTime:    now,
		EntryDate:    config.TodayIST(),
	}

	e.State.Save(oid, trade)
	e.Mu.Lock()
	e.ActiveTrades[oid] = trade
	e.RedCandlesBelow[oid] = 0
	e.Mu.Unlock()

	// Clear from recently-exited list if re-entering
	delete(e.RecentlyExitedByEMA, sym)

	SendTelegram(fmt.Sprintf(
		"🟢 *SWING BUY — %s*\n`%s` | `%s`\nEntry: ₹`%.2f` | SL: ₹`%.2f`\nQty: `%d` | Product: `CNC`",
		sig.Strategy, sym, regime, sig.EntryPrice, sig.StopPrice, qty))

	// Live mode: poll the entry order until it fills (or partials / cancels / times out).
	// On fill, FillMonitor adjusts qty/price to actuals and places SL+partial+target.
	// Paper mode (FillMonitor nil) treats the LIMIT entry as immediately filled.
	if e.FillMonitor != nil {
		go func() {
			updated := e.FillMonitor.WaitForFill(trade)
			if updated == nil {
				return
			}
			if updated.EntryCancelled {
				// Entry rejected, cancelled, or 0-fill timeout — purge from active state.
				e.Mu.Lock()
				delete(e.ActiveTrades, oid)
				delete(e.RedCandlesBelow, oid)
				e.Mu.Unlock()
				e.State.Close(oid)
				return
			}
			// FillMonitor adjusts qty / SL / target OIDs; persist the updated trade.
			e.Mu.Lock()
			e.ActiveTrades[oid] = updated
			e.Mu.Unlock()
			e.State.Save(oid, updated)
		}()
	} else {
		// Paper mode: simulate realistic fill with 0.3% market-impact slippage.
		// Real limit orders on liquid NSE stocks face ~0.1–0.3% adverse slippage at entry.
		slippedPrice := trade.EntryPrice * (1 + config.PaperSlippagePct/100)
		trade.EntryPrice = slippedPrice
		e.Mu.Lock()
		e.ActiveTrades[oid] = trade
		e.Mu.Unlock()
		e.State.Save(oid, trade)
		log.Printf("[Paper] %s filled at ₹%.2f (%.1f%% slippage applied)", sym, slippedPrice, config.PaperSlippagePct)
	}

	return true
}

// ══════════════════════════════════════════════════════════════
//  MonitorPositions — Intraday Hard SL Check (Section VI.2)
// ══════════════════════════════════════════════════════════════
//  During market hours, check the 7% absolute maximum stop loss.
//  The 21 EMA trailing exit is checked once daily at EOD.

func (e *ExecutionAgent) MonitorPositions(regime string) {
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

		// Section VI.2: Hard Stop Loss (7% absolute max)
		if ltp <= trade.StopPrice {
			// FIX-13: Circuit detection — if LTP frozen for 60s, alert but DON'T exit
			// A stock in circuit can't fill the SL order, so don't mark as SL hit.
			if e.isStuckInCircuit(token, ltp) {
				if !e.CircuitAlerted[token] {
					SendTelegram(fmt.Sprintf(
						"⚠️ CIRCUIT: `%s` LTP ₹%.2f frozen >60s — SL order may not execute. Manual review required.",
						trade.Symbol, ltp))
					e.CircuitAlerted[token] = true
				}
				continue // Do NOT increment ConsecutiveSLHits
			}
			e.CircuitAlerted[token] = false // Reset if price is moving
			e.forceExit(oid, trade, "HARD_SL_7PCT", ltp)
			if e.Scanner != nil {
				e.Scanner.RecordSLHit()
			}
			continue
		}

		// Track LTP changes for circuit detection
		e.trackLTPChange(token, ltp)

		// Book Ch.12 p.283: 2R partial profit — sell 50% when gain = 2× initial risk.
		// "When you reach 2R, take half off the table. The worst that can happen
		//  now is a breakeven trade on the remaining shares."
		if !trade.PartialFilled && trade.PartialTarget > 0 && trade.PartialQty > 0 &&
			ltp >= trade.PartialTarget && trade.RemainingQty > trade.PartialQty {
			log.Printf("[2R] %s: LTP ₹%.2f ≥ 2R target ₹%.2f — partial exit %d shares",
				trade.Symbol, ltp, trade.PartialTarget, trade.PartialQty)
			e.executePartialExit(oid, trade, "2R_PARTIAL_PROFIT", ltp, trade.PartialQty)
			continue
		}

	}
}

// FIX-13: Circuit detection helpers
func (e *ExecutionAgent) trackLTPChange(token uint32, ltp float64) {
	if prev, ok := e.LastKnownLTP[token]; !ok || prev != ltp {
		e.LastKnownLTP[token] = ltp
		e.LastLTPChangeAt[token] = time.Now()
		e.CircuitAlerted[token] = false
	}
}

func (e *ExecutionAgent) isStuckInCircuit(token uint32, ltp float64) bool {
	lastChange, ok := e.LastLTPChangeAt[token]
	if !ok {
		// First check — initialize tracking
		e.LastKnownLTP[token] = ltp
		e.LastLTPChangeAt[token] = time.Now()
		return false
	}
	// LTP hasn't changed for 60 seconds → likely in circuit
	return e.LastKnownLTP[token] == ltp && time.Since(lastChange) > 60*time.Second
}

// ══════════════════════════════════════════════════════════════
//  RunDailyEMACheck — Phase 5: 21 EMA Trailing Exit (EOD)
// ══════════════════════════════════════════════════════════════
//  Called once daily near market close (15:20).
//  "Exit only if 2 continuous red candles close below 21 EMA."
//  "Do not exit on a single red candle below EMA if it recovers."

func (e *ExecutionAgent) RunDailyEMACheck(dailyCache *DailyCache) {
	if dailyCache == nil || !dailyCache.Loaded {
		return
	}

	e.Mu.Lock()
	defer e.Mu.Unlock()

	for oid, trade := range e.ActiveTrades {
		token := trade.Token
		closes, cOk := dailyCache.Closes[token]
		if !cOk || len(closes) < config.EMA20Period+1 {
			continue
		}

		lows, lOk := dailyCache.Lows[token]

		ema20 := ComputeEMA20(closes)
		if ema20 <= 0 {
			continue
		}

		lastClose := closes[len(closes)-1]

		// ── Book Ch.7 p.191: Earnings / board meeting alert (NSE calendar) ────
		// "Avoid holding through earnings unless you have a significant profit cushion."
		// Uses real NSE corporate actions calendar (RefreshEarningsCalendar at startup).
		// Alerts if results are within ResultsAvoidanceDays; doesn't force exit —
		// the profit cushion rule means the trader decides whether to hold.
		if HasUpcomingResults(trade.Symbol) {
			days := DaysToResults(trade.Symbol)
			pnlCushion := 0.0
			if trade.EntryPrice > 0 {
				pnlCushion = (lastClose - trade.EntryPrice) / trade.EntryPrice * 100
			}
			log.Printf("[Earnings] %s: results in %d days, profit cushion=%.1f%%",
				trade.Symbol, days, pnlCushion)
			if pnlCushion < 20.0 {
				SendTelegram(fmt.Sprintf(
					"⚠️ *EARNINGS ALERT — %s*\nBoard meeting / results in `%d` days.\nProfit cushion: `%.1f%%` (below 20%% threshold).\nBook Ch.10: consider exiting before results unless cushion ≥ 20%%.\nCurrent P&L: ₹`%+.0f`",
					trade.Symbol, days, pnlCushion,
					(lastClose-trade.EntryPrice)*float64(trade.RemainingQty)))
			} else {
				log.Printf("[Earnings] %s: results in %d days but profit cushion %.1f%% ≥ 20%% — holding",
					trade.Symbol, days, pnlCushion)
			}
		}

		// ── Book Ch.12 p.283: 3 consecutive strong up days → autonomous partial exit ──
		// "Three back-to-back strong up days (≥1.5% each) = sell 25% into strength.
		//  Don't wait for the EMA to break — sell into the euphoria."
		if len(closes) >= 5 {
			consecutiveStrongUp := 0
			for k := 1; k <= 4; k++ {
				if len(closes) < k+2 {
					break
				}
				c := closes[len(closes)-k]
				cPrev := closes[len(closes)-k-1]
				if cPrev > 0 && (c-cPrev)/cPrev*100 >= 1.5 {
					consecutiveStrongUp++
				} else {
					break
				}
			}
			if consecutiveStrongUp >= 3 && !trade.PartialFilled {
				partialQty := int(float64(trade.RemainingQty) * config.StrongDayPartialSellPct / 100)
				if partialQty < 1 && trade.RemainingQty >= 2 {
					partialQty = 1
				}
				if partialQty >= 1 && partialQty < trade.RemainingQty {
					log.Printf("[StrongMove] %s: %d consecutive strong up days — AUTO partial exit %d shares",
						trade.Symbol, consecutiveStrongUp, partialQty)
					e.executePartialExit(oid, trade, "STRONG_DAYS_PARTIAL", lastClose, partialQty)
				} else {
					// Not enough qty for a partial — just alert
					SendTelegram(fmt.Sprintf(
						"📈 *STRONG MOVE ALERT — %s*\n`%d` consecutive strong up days (≥1.5%%)\nBook Ch.12: consider booking partial profits. P&L: ₹`%.0f`",
						trade.Symbol, consecutiveStrongUp,
						(lastClose-trade.EntryPrice)*float64(trade.RemainingQty)))
				}
			}
		}

		// ── Book Ch.6 p.163: Extended-Move Sell (Selling into Strength) ────────
		// "If you see a stock moving 25-30% in a matter of a few sessions, consider
		//  it extended. Take profits — the stock is more likely to pull back or
		//  consolidate after such a substantial run-up."
		// Applies to LATE-stage extensions only (book p.163-166): early-stage
		// extensions in a new uptrend are NOT sell signals. Heuristic: a position
		// already up ≥15% from entry is treated as "late-stage" for this check.
		if !trade.PartialFilled && trade.EntryPrice > 0 &&
			len(closes) >= config.ExtendedMoveSessionsWindow+1 {
			windowStart := len(closes) - config.ExtendedMoveSessionsWindow - 1
			startPrice := closes[windowStart]
			if startPrice > 0 {
				movePct := (lastClose - startPrice) / startPrice * 100
				gainFromEntry := (lastClose - trade.EntryPrice) / trade.EntryPrice * 100
				if movePct >= config.ExtendedMoveMinPct && gainFromEntry >= 15.0 {
					partialQty := int(float64(trade.RemainingQty) * config.ExtendedMovePartialSellPct / 100)
					if partialQty < 1 && trade.RemainingQty >= 2 {
						partialQty = 1
					}
					if partialQty >= 1 && partialQty < trade.RemainingQty {
						log.Printf("[ExtendedMove] %s: %.1f%% in %d sessions — partial exit %d shares (Ch.6 p.163)",
							trade.Symbol, movePct, config.ExtendedMoveSessionsWindow, partialQty)
						e.executePartialExit(oid, trade, "EXTENDED_MOVE_SELL", lastClose, partialQty)
					}
				}
			}
		}

		// ── Book Ch.6 p.173: Hybrid Selling Technique — 25-35% partial ─────────
		// "Sell a portion of your position—typically around 25–35%—into strength.
		//  For the remaining shares, you set a stop-loss at your buy price,
		//  effectively making the rest of your position risk-free."
		// Fires BEFORE 2R target is reached, capturing earlier strength.
		if !trade.PartialFilled && trade.EntryPrice > 0 && trade.PartialTarget > 0 &&
			lastClose < trade.PartialTarget {
			gainFromEntry := (lastClose - trade.EntryPrice) / trade.EntryPrice * 100
			if gainFromEntry >= config.HybridStrongMoveGainPct {
				partialQty := int(float64(trade.RemainingQty) * config.HybridPartialSellPct / 100)
				if partialQty < 1 && trade.RemainingQty >= 2 {
					partialQty = 1
				}
				if partialQty >= 1 && partialQty < trade.RemainingQty {
					log.Printf("[Hybrid] %s: %.1f%% gain — partial %d shares + SL→breakeven (Ch.6 p.173)",
						trade.Symbol, gainFromEntry, partialQty)
					e.executePartialExit(oid, trade, "HYBRID_25_35_PARTIAL", lastClose, partialQty)
				}
			}
		}

		// ── Book Ch.6 p.171-172: Downside Pivot Exit ────────────────────────────
		// "Wait for the stock to form a downside pivot on the daily time frame.
		//  This pivot indicates a shift in momentum." (BEL 2015 example: broke
		//  below pivot low on 9 March → exit signal)
		// Pivot low = lowest low in the past N bars (excluding the very latest bar).
		// Exit when today's close violates that pivot AND we're already profitable
		// (selling-into-weakness only locks in gains; full SL handles raw losses).
		if lOk && len(lows) >= config.DownsidePivotLookback+1 && lastClose > trade.EntryPrice {
			n := len(lows)
			pivotLow := lows[n-config.DownsidePivotLookback-1]
			for i := n - config.DownsidePivotLookback - 1; i < n-1; i++ {
				if lows[i] < pivotLow {
					pivotLow = lows[i]
				}
			}
			if pivotLow > 0 && lastClose < pivotLow {
				log.Printf("[DownsidePivot] %s: close ₹%.2f broke pivot low ₹%.2f — EXIT (Ch.6 p.171-172)",
					trade.Symbol, lastClose, pivotLow)
				e.forceExit(oid, trade, "DOWNSIDE_PIVOT_EXIT", lastClose)
				e.RecentlyExitedByEMA[trade.Symbol] = token
				e.EMAExitPrices[trade.Symbol] = lastClose
				e.RecentlyExitedByEMAAt[trade.Symbol] = time.Now()
				continue
			}
		}

		// ── Book Ch.6 p.167-168 + Ch.12: EMA trailing exit ─────────────────────
		// "Wait until the market closes. If the stock closes below the key moving
		//  average — sell on that day. A stock might dip intraday but close above;
		//  that is NOT an exit. A single EOD close below = exit."
		//                                  — Ch.6, Figs 6.4 & 6.5 (TECHM, FINCABLES)
		// Large base (VCP / Cup / VCP_REENTRY / VCP_TOPUP) → trail with EMA50.
		// Mini base (Flag, Channel, EMA cross, IPO, Flat) → trail with EMA20.
		isLargeBaseStrategy := trade.Strategy == "VCP_BREAKOUT" ||
			trade.Strategy == "VCP_TOPUP" ||
			trade.Strategy == "VCP_REENTRY" ||
			trade.Strategy == "CUP_HANDLE"
		if isLargeBaseStrategy {
			ema50 := computeEMAForPeriod(closes, config.EMA50Period)
			if ema50 > 0 {
				if lastClose < ema50 {
					log.Printf("[EMA50] %s (%s): EXIT — EOD close ₹%.2f below EMA50 ₹%.2f",
						trade.Symbol, trade.Strategy, lastClose, ema50)
					e.forceExit(oid, trade, "EMA50_CLOSE_BELOW_LARGEBASE", lastClose)
					e.RecentlyExitedByEMA[trade.Symbol] = token
					e.EMAExitPrices[trade.Symbol] = lastClose
					e.RecentlyExitedByEMAAt[trade.Symbol] = time.Now()
					continue
				}
				continue // Large base uses EMA50 — skip EMA20 check below
			}
		}

		// ── Rule 5: EMA20 trailing exit — single EOD close below EMA ────────────
		// Book Ch.6 p.167: "Close below the key moving average — sell on that day."
		// Figures 6.4 (TECHM) and 6.5 (FINCABLES): single close = exit signal.
		if lastClose < ema20 {
			log.Printf("[EMA20] %s: EXIT — EOD close ₹%.2f below EMA20 ₹%.2f",
				trade.Symbol, lastClose, ema20)
			e.forceExit(oid, trade, "EMA20_CLOSE_BELOW", lastClose)
			e.RecentlyExitedByEMA[trade.Symbol] = token
			e.EMAExitPrices[trade.Symbol] = lastClose
			e.RecentlyExitedByEMAAt[trade.Symbol] = time.Now()
			continue
		}
	}
}

// ══════════════════════════════════════════════════════════════
//  CheckReEntries — Phase 5: Re-entry after EMA reclaim
// ══════════════════════════════════════════════════════════════
//  "If stopped out by 21 EMA rule, but stock subsequently reclaims
//   and closes back above 21 EMA with a green candle → re-enter."

func (e *ExecutionAgent) CheckReEntries(scanner *ScannerAgent, regime string) {
	// Purge stale EMA-exit entries. If no re-entry signal fires within 30 days, the setup
	// has expired. Without this, the map grows indefinitely and generates spurious re-entry checks.
	const maxReEntryWindow = 30 * 24 * time.Hour
	for symbol, exitAt := range e.RecentlyExitedByEMAAt {
		if time.Since(exitAt) > maxReEntryWindow {
			delete(e.RecentlyExitedByEMA, symbol)
			delete(e.EMAExitPrices, symbol)
			delete(e.RecentlyExitedByEMAAt, symbol)
			log.Printf("[ReEntry] %s re-entry window expired (>30d) — removed from watch list", symbol)
		}
	}

	for symbol, token := range e.RecentlyExitedByEMA {
		sig := scanner.CheckReEntry(token, symbol, regime)
		if sig != nil {
			// FIX-05: Cap re-entry premium
			if ReEntryMaxPremiumPct > 0 {
				if exitPrice, ok := e.EMAExitPrices[symbol]; ok && exitPrice > 0 {
					maxPrice := exitPrice * (1 + ReEntryMaxPremiumPct/100)
					if sig.EntryPrice > maxPrice {
						premium := (sig.EntryPrice - exitPrice) / exitPrice * 100
						log.Printf("[ReEntry] BLOCKED %s: price %.2f is %.1f%% above exit %.2f (cap: %.1f%%)",
							symbol, sig.EntryPrice, premium, exitPrice, ReEntryMaxPremiumPct)
						continue
					}
				}
			}
			if e.Execute(sig, regime) {
				log.Printf("[ReEntry] Successfully re-entered %s after EMA reclaim", symbol)
				delete(e.EMAExitPrices, symbol) // Clean up
			}
		}
	}
}

// ══════════════════════════════════════════════════════════════
//  CheckTopUps — Phase 3: Add to existing positions on retest
// ══════════════════════════════════════════════════════════════
//  Doc: "Add more quantity when the stock breaks out past its initial
//  listing/issue price and successfully retests that level."

func (e *ExecutionAgent) CheckTopUps(scanner *ScannerAgent, regime string) {
	e.Mu.RLock()
	var candidates []struct {
		token      uint32
		symbol     string
		entryPrice float64
		pyramided  bool
	}
	for _, trade := range e.ActiveTrades {
		if trade.PyramidAdded == 0 { // Only top-up once per position
			candidates = append(candidates, struct {
				token      uint32
				symbol     string
				entryPrice float64
				pyramided  bool
			}{trade.Token, trade.Symbol, trade.EntryPrice, false})
		}
	}
	e.Mu.RUnlock()

	for _, c := range candidates {
		ltp := 0.0
		if e.GetLTP != nil {
			ltp = e.GetLTP(c.token)
		}
		if ltp <= 0 {
			continue
		}
		// Doc VI.3: "Always add to your winners. NEVER average down."
		if ltp <= c.entryPrice {
			continue // Position is a loser — do NOT add
		}
		sig := scanner.CheckTopUp(c.token, c.symbol, c.entryPrice, ltp, regime)
		if sig != nil {
			if e.Execute(sig, regime) {
				// Mark the original position as pyramided
				e.Mu.Lock()
				for _, trade := range e.ActiveTrades {
					if trade.Symbol == c.symbol && trade.PyramidAdded == 0 {
						trade.PyramidAdded = 1
						e.State.Save(trade.EntryOID, trade)
						break
					}
				}
				e.Mu.Unlock()
				log.Printf("[TopUp] Added to %s position on breakout retest", c.symbol)
			}
		}
	}
}

// ══════════════════════════════════════════════════════════════
//  executePartialExit — Book Ch.12: sell a portion of a position
//  Used for 2R partial profit and 3-strong-day partial exit.
//  Updates RemainingQty + PartialFilled in state. Thread-safe:
//  caller must already hold e.Mu.Lock() (MonitorPositions does).
// ══════════════════════════════════════════════════════════════

func (e *ExecutionAgent) executePartialExit(oid string, trade *core.Trade, reason string, exitPrice float64, qty int) {
	if qty <= 0 || qty >= trade.RemainingQty {
		return
	}
	pnlPartial := (exitPrice - trade.EntryPrice) * float64(qty)

	if e.PlaceOrder != nil {
		_, err := e.PlaceOrder(trade.Symbol, qty, true, "MARKET", 0)
		if err != nil {
			log.Printf("[PartialExit] %s sell %d shares FAILED: %v", trade.Symbol, qty, err)
			SendTelegram(fmt.Sprintf("⚠️ PARTIAL EXIT FAILED: `%s` qty=%d reason=%s err=%v",
				trade.Symbol, qty, reason, err))
			return
		}
	}

	trade.RemainingQty -= qty
	trade.PartialFilled = true
	trade.RealisedPnl += pnlPartial

	// ── Book Ch.6 p.173 — Hybrid Technique: move SL to breakeven after partial ──
	// "Sell 25% into strength, then move your stop-loss to your buy price.
	//  The remaining position is now risk-free."
	breakevenMoved := false
	if exitPrice > trade.EntryPrice && trade.StopPrice < trade.EntryPrice {
		trade.StopPrice = trade.EntryPrice
		breakevenMoved = true
		log.Printf("[Breakeven] %s: SL moved to entry ₹%.2f after partial profit — remaining position is risk-free",
			trade.Symbol, trade.EntryPrice)
	}

	e.State.Save(oid, trade)

	msg := fmt.Sprintf(
		"💰 *PARTIAL EXIT — %s*\n`%s` sold `%d` shares @ ₹`%.2f`\nPartial P&L: ₹`%+.0f` | Remaining: `%d` shares\nReason: `%s`",
		trade.Symbol, reason, qty, exitPrice, pnlPartial, trade.RemainingQty, reason)
	if breakevenMoved {
		msg += fmt.Sprintf("\n🔒 SL moved to breakeven ₹`%.2f` — remaining position risk-free (Ch.6 Hybrid)", trade.EntryPrice)
	}
	log.Printf("[PartialExit] %s: sold %d shares @ ₹%.2f (%s) partial P&L=₹%.0f remaining=%d breakevenMoved=%v",
		trade.Symbol, qty, exitPrice, reason, pnlPartial, trade.RemainingQty, breakevenMoved)
	SendTelegram(msg)
}

// ══════════════════════════════════════════════════════════════
//  forceExit — Close a swing position
// ══════════════════════════════════════════════════════════════

func (e *ExecutionAgent) forceExit(oid string, trade *core.Trade, reason string, exitPrice float64) {
	// Guard: if an exit is already in-flight for this position, do not place a second one.
	if e.ExitInProgress[oid] {
		log.Printf("[Exit] %s exit already in-progress — skipping duplicate", trade.Symbol)
		return
	}
	e.ExitInProgress[oid] = true

	// Cancel broker SL (GTT or SL-M) and target orders.
	// If cancel fails, the order may have already triggered/filled → skip our market exit to avoid double-sell.
	slHandledByBroker := false
	if trade.SLOID != "" {
		if strings.HasPrefix(trade.SLOID, "GTT_") {
			// GTT SL — cancel the trigger
			if e.CancelGTT != nil {
				var gttID int
				fmt.Sscanf(trade.SLOID[4:], "%d", &gttID)
				if err := e.CancelGTT(gttID); err != nil {
					slHandledByBroker = true
					log.Printf("[Exit] %s GTT cancel failed (%v) — GTT may have already triggered, skipping market exit",
						trade.Symbol, err)
				}
			}
		} else if e.CancelOrder != nil {
			// Regular SL-M order
			if err := e.CancelOrder(trade.SLOID); err != nil {
				slHandledByBroker = true
				log.Printf("[Exit] %s SL cancel failed (%v) — broker SL-M likely already filled, skipping market exit",
					trade.Symbol, err)
			}
		}
	}
	if trade.TargetOID != "" && e.CancelOrder != nil {
		e.CancelOrder(trade.TargetOID)
	}

	exitQty := trade.RemainingQty
	if exitQty == 0 {
		exitQty = trade.Qty
	}

	// Place exit order only if the broker SL hasn't already handled it.
	// If broker rejects (circuit / connectivity), bail out without closing state —
	// the next monitor tick will retry rather than leaving shares unhedged.
	if e.PlaceOrder != nil && !slHandledByBroker {
		exitOID, err := e.PlaceOrder(trade.Symbol, exitQty, true, "MARKET", 0)
		if err != nil || exitOID == "" {
			log.Printf("[Exit] ⚠️ %s exit REJECTED (%s): %v — keeping position OPEN for retry",
				trade.Symbol, reason, err)
			SendTelegram(fmt.Sprintf(
				"⚠️ EXIT REJECTED: `%s` (%s)\nReason: `%v`\nPosition still OPEN — manual intervention may be required.",
				trade.Symbol, reason, err))
			delete(e.ExitInProgress, oid) // Reset so next tick can retry
			return
		}
	} else if slHandledByBroker {
		SendTelegram(fmt.Sprintf("ℹ️ EXIT: `%s` (%s) — broker SL-M already filled, no duplicate order placed.",
			trade.Symbol, reason))
	}

	if exitPrice <= 0 {
		exitPrice = trade.EntryPrice
	}

	pnl := (exitPrice - trade.EntryPrice) * float64(exitQty) // Long-only

	// Book Ch.8: Update drawdown tracking after every close (p.205)
	e.updateDrawdown(pnl)

	e.State.Close(oid)
	e.Journal.LogTrade(&core.TradeLog{
		Symbol:        trade.Symbol,
		Strategy:      trade.Strategy,
		Regime:        trade.Regime,
		EntryPrice:    trade.EntryPrice,
		FullExitPrice: exitPrice,
		Qty:           exitQty,
		GrossPnl:      pnl,
		ExitReason:    reason,
		EntryTime:     trade.EntryTime,
		ExitTime:      config.NowIST(),
		IsShort:       false,
	})

	emoji := "🔴"
	if pnl > 0 {
		emoji = "🟢"
		if e.Scanner != nil {
			e.Scanner.RecordWin()
			// FIX-10: Graduated recovery ladder — ⚠️ EXTENSION: NOT in Blueprint
			e.ConsecutiveWins++
			switch {
			case e.ConsecutiveWins >= RecoveryTo100PctAfterWins && e.Scanner.CapitalMultiplier < 1.0:
				e.Scanner.CapitalMultiplier = 1.0
				log.Printf("[Recovery] Capital recovered to 100%% after %d consecutive wins", e.ConsecutiveWins)
			case e.ConsecutiveWins >= RecoveryTo80PctAfterWins && e.Scanner.CapitalMultiplier < 0.80:
				e.Scanner.CapitalMultiplier = 0.80
				log.Printf("[Recovery] Capital recovered to 80%% after %d consecutive wins", e.ConsecutiveWins)
			case e.ConsecutiveWins >= RecoveryTo60PctAfterWins && e.Scanner.CapitalMultiplier < 0.60:
				e.Scanner.CapitalMultiplier = 0.60
				log.Printf("[Recovery] Capital recovered to 60%% after %d consecutive wins", e.ConsecutiveWins)
			}
		}
	} else {
		e.ConsecutiveWins = 0 // Reset on loss
	}
	SendTelegram(fmt.Sprintf("%s *SWING EXIT — %s*\n`%s` | `%s`\nP&L: ₹`%+.0f`",
		emoji, reason, trade.Symbol, trade.Strategy, pnl))

	delete(e.ActiveTrades, oid)
	delete(e.RedCandlesBelow, oid)
	delete(e.ExitInProgress, oid)
}

// FlattenAll closes all positions (kill switch / emergency)
func (e *ExecutionAgent) FlattenAll(reason string) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	for oid, trade := range e.ActiveTrades {
		ltp := 0.0
		if e.GetLTP != nil && trade.Token > 0 {
			ltp = e.GetLTP(trade.Token)
		}
		e.forceExit(oid, trade, reason, ltp)
	}
}

// DailySummaryAlert sends end-of-day summary via Telegram
func (e *ExecutionAgent) DailySummaryAlert(regime string) {
	e.Mu.RLock()
	openCount := len(e.ActiveTrades)
	e.Mu.RUnlock()

	SendTelegram(fmt.Sprintf(
		"📊 *EOD SWING SUMMARY*\nRegime: `%s`\nOpen Positions: `%d/%d`\nCapital Multiplier: `%.0f%%`",
		regime, openCount, config.ComputeMaxPositions(config.TotalCapital),
		e.Scanner.CapitalMultiplier*100))
}

// ComputeEMA20 computes 20-day EMA (trend EMA — entry confirmation + exit trigger)
func ComputeEMA20(closes []float64) float64 {
	return computeEMAForPeriod(closes, config.EMA20Period)
}

// ComputeEMA21 kept as alias so existing tests compile unchanged.
func ComputeEMA21(closes []float64) float64 {
	return computeEMAForPeriod(closes, config.EMA20Period)
}

// ComputeEMA63 computes 63-day EMA (alternative longer-trend exit indicator)
// Doc: "Track using 21-day EMA or 63-day EMA"
func ComputeEMA63(closes []float64) float64 {
	return computeEMAForPeriod(closes, config.EMA63Period)
}

func computeEMAForPeriod(closes []float64, period int) float64 {
	ema := data.ComputeEMA(closes, period)
	if len(ema) == 0 {
		return 0
	}
	return ema[len(ema)-1]
}

// ══════════════════════════════════════════════════════════════
//  Telegram
// ══════════════════════════════════════════════════════════════

var telegramMu sync.Mutex

func SendTelegram(msg string) {
	if config.TelegramBotToken == "" || len(config.TelegramChatIDs) == 0 {
		log.Printf("[ALERT] %s", msg)
		return
	}
	go func() {
		telegramMu.Lock()
		defer telegramMu.Unlock()
		base := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", config.TelegramBotToken)
		for _, chatID := range config.TelegramChatIDs {
			http.PostForm(base, url.Values{
				"chat_id":    {chatID},
				"text":       {msg},
				"parse_mode": {"Markdown"},
			})
			time.Sleep(300 * time.Millisecond)
		}
	}()
}
