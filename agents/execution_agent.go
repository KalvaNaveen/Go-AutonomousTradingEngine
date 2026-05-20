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

	qty := sig.Qty
	if qty <= 0 {
		// Capital sizing produced 0 shares — likely below per-trade allocation
		// or the stock is too expensive for the configured capital. Skip rather
		// than placing a meaningless 1-share trade.
		log.Printf("[Execute] Skipping %s — computed qty=%d (capital/price mismatch)", sym, qty)
		return false
	}

	// Pre-GTT news check — block on NEGATIVE BSE announcements.
	// Fail-open: any network error returns NEUTRAL and trading proceeds.
	if !CheckNewsBeforeEntry(sym) {
		return false
	}

	// For breakout strategies the signal fires at the live LTP, which is the breakout tick.
	// By the time the LIMIT order reaches the exchange, price has moved. Add a small buffer
	// above LTP so the order has a realistic chance of filling on fast breakout moves.
	limitPrice := sig.EntryPrice
	switch sig.Strategy {
	case "VCP_BREAKOUT", "BULL_FLAG", "IPO_BASE", "CUP_HANDLE":
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

		// Trailing stop — protect profits on winning positions.
		// Tier 1: gain ≥15% → trail SL to breakeven (entry price).
		// Tier 2: gain ≥25% → trail SL to entry+10% (lock in partial gain).
		// Only trail upward — never move SL against the position.
		if ltp > trade.EntryPrice && trade.EntryPrice > 0 {
			gainPct := (ltp - trade.EntryPrice) / trade.EntryPrice * 100
			var newStop float64
			switch {
			case gainPct >= 25:
				newStop = trade.EntryPrice * 1.10
			case gainPct >= 15:
				newStop = trade.EntryPrice // breakeven
			}
			if newStop > trade.StopPrice {
				log.Printf("[Trail] %s SL trailed ₹%.2f → ₹%.2f (gain=%.1f%%)",
					trade.Symbol, trade.StopPrice, newStop, gainPct)
				trade.StopPrice = newStop
				trade.TrailStop = newStop
				e.State.Save(oid, trade)
			}
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

		highs, hOk := dailyCache.Highs[token]
		lows, lOk := dailyCache.Lows[token]
		volumes, vOk := dailyCache.Volumes[token]

		ema20 := ComputeEMA20(closes)
		if ema20 <= 0 {
			continue
		}

		lastClose := closes[len(closes)-1]
		prevClose := closes[len(closes)-2]

		// ── Rule 4: Volume+Price sell pressure exit ────────────────
		// Exit next open if: 2 of last 3 days have CPR<0.35 + volume spike,
		// AND LTP < yesterday's low (today's price confirming breakdown).
		if hOk && lOk && vOk && len(highs) >= 4 && len(lows) >= 4 && len(volumes) >= 20 {
			if e.Scanner != nil {
				n := len(closes)
				sellPressureDays := 0
				for i := n - 3; i < n; i++ {
					h := highs[i]
					l := lows[i]
					c := closes[i]
					rng := h - l
					if rng <= 0 {
						continue
					}
					cpr := (c - l) / rng
					// Volume spike: compare to 20-day avg
					avgVol := 0.0
					if len(volumes) >= 20 {
						for _, v := range volumes[i-20 : i] {
							avgVol += v
						}
						avgVol /= 20
					}
					volSpike := avgVol > 0 && volumes[i] > avgVol*config.VolumeSpikeMultiplier
					if cpr < config.SellPressureRatio && volSpike {
						sellPressureDays++
					}
				}
				// Get live LTP to check against yesterday's low
				ltp := 0.0
				if e.GetLTP != nil && token > 0 {
					ltp = e.GetLTP(token)
				}
				yesterdayLow := lows[len(lows)-2]
				if sellPressureDays >= config.VolumePressureDaysNeeded && ltp > 0 && ltp < yesterdayLow {
					log.Printf("[SellPressure] %s: EXIT — %d days CPR<%.2f+volume spike, LTP=%.2f < yesterdayLow=%.2f",
						trade.Symbol, sellPressureDays, config.SellPressureRatio, ltp, yesterdayLow)
					SendTelegram(fmt.Sprintf(
						"🔴 *SELL PRESSURE EXIT — %s*\nCPR+Volume: `%d/3` days | LTP ₹`%.2f` < PrevLow ₹`%.2f`",
						trade.Symbol, sellPressureDays, ltp, yesterdayLow))
					e.forceExit(oid, trade, "SELL_PRESSURE_CPR", ltp)
					e.RecentlyExitedByEMA[trade.Symbol] = token
					e.EMAExitPrices[trade.Symbol] = ltp
					e.RecentlyExitedByEMAAt[trade.Symbol] = time.Now()
					continue
				}
			}
		}

		// ── Rule 5: EMA20 trailing exit (2 red candles below EMA) ──
		isRedCandle := lastClose < prevClose
		belowEMA := lastClose < ema20

		if isRedCandle && belowEMA {
			e.RedCandlesBelow[oid]++
			log.Printf("[EMA20] %s: Red candle #%d below EMA20 (Close=%.2f EMA=%.2f)",
				trade.Symbol, e.RedCandlesBelow[oid], lastClose, ema20)

			if e.RedCandlesBelow[oid] >= config.RedCandlesBelowEMA {
				log.Printf("[EMA20] %s: EXIT — %d red candles below EMA20",
					trade.Symbol, e.RedCandlesBelow[oid])
				e.forceExit(oid, trade, "EMA20_2RED_BELOW", lastClose)

				e.RecentlyExitedByEMA[trade.Symbol] = token
				e.EMAExitPrices[trade.Symbol] = lastClose
				e.RecentlyExitedByEMAAt[trade.Symbol] = time.Now()
				continue
			}
		} else {
			if EMAResetBuffer > 0 {
				if lastClose > ema20*(1+EMAResetBuffer) && lastClose > prevClose {
					if e.RedCandlesBelow[oid] > 0 {
						log.Printf("[EMA20] %s: Recovered above EMA20+buffer, reset counter (Close=%.2f EMA=%.2f)",
							trade.Symbol, lastClose, ema20)
					}
					e.RedCandlesBelow[oid] = 0
				}
			} else {
				if e.RedCandlesBelow[oid] > 0 {
					log.Printf("[EMA20] %s: Streak broken, reset counter (Close=%.2f EMA=%.2f)",
						trade.Symbol, lastClose, ema20)
				}
				e.RedCandlesBelow[oid] = 0
			}
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
