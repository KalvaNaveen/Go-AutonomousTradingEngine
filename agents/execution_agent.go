package agents

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
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
	GetLTP      func(uint32) float64

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
		Journal:             journal,
		State:               state,
		ActiveTrades:        make(map[string]*core.Trade),
		RedCandlesBelow:     make(map[string]int),
		RecentlyExitedByEMA: make(map[string]uint32),
		EMAExitPrices:       make(map[string]float64),
		LastKnownLTP:        make(map[uint32]float64),
		LastLTPChangeAt:     make(map[uint32]time.Time),
		CircuitAlerted:      make(map[uint32]bool),
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
	}
	log.Printf("[ExecutionAgent] Restored %d swing positions from state", len(openPositions))
}

// ══════════════════════════════════════════════════════════════
//  Execute — Open a new swing position
// ══════════════════════════════════════════════════════════════

func (e *ExecutionAgent) Execute(sig *Signal, regime string) bool {
	sym := sig.Symbol

	// Section VI.1: Max 6 positions (top-ups don't count as new)
	e.Mu.RLock()
	if sig.Strategy != "VCP_TOPUP" && len(e.ActiveTrades) >= config.MaxOpenPositions {
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
	e.Mu.RUnlock()

	qty := sig.Qty
	if qty <= 0 {
		qty = 1
	}

	var oid string
	var err error
	if e.PlaceOrder != nil {
		oid, err = e.PlaceOrder(sym, qty, false, "LIMIT", sig.EntryPrice)
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
		EntryPrice:   sig.EntryPrice,
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
		if !cOk || len(closes) < config.EMA21Period+1 {
			continue
		}

		// Doc Section VII: "Apply the 21-day EMA to your daily chart"
		ema21 := ComputeEMA21(closes)
		if ema21 <= 0 {
			continue
		}

		lastClose := closes[len(closes)-1]
		prevClose := closes[len(closes)-2]

		isRedCandle := lastClose < prevClose
		belowEMA := lastClose < ema21

		if isRedCandle && belowEMA {
			e.RedCandlesBelow[oid]++
			log.Printf("[EMA21] %s: Red candle #%d below EMA (Close=%.2f EMA=%.2f)",
				trade.Symbol, e.RedCandlesBelow[oid], lastClose, ema21)

			// Doc: "two continuous red candles that CLOSE below the 21 EMA"
			if e.RedCandlesBelow[oid] >= config.RedCandlesBelowEMA {
				log.Printf("[EMA21] %s: EXIT — %d red candles below 21 EMA",
					trade.Symbol, e.RedCandlesBelow[oid])
				e.forceExit(oid, trade, "EMA21_2RED_BELOW", lastClose)

				e.RecentlyExitedByEMA[trade.Symbol] = token
				// FIX-05: Save exit price for re-entry cap
				e.EMAExitPrices[trade.Symbol] = lastClose
				continue
			}
		} else {
			// FIX-11: Configurable reset behavior for EMA counter
			// Blueprint says "two continuous red candles" — interpret as:
			// When EMAResetBuffer = 0.0 (default): any non-red-below-EMA day resets
			//   counter (Blueprint literal interpretation of "continuous").
			// When EMAResetBuffer > 0 (e.g. 0.005): only resets if close > EMA×(1+buffer)
			//   AND green candle. A weak close near EMA neither increments nor resets.
			if EMAResetBuffer > 0 {
				// Stricter mode: require meaningful recovery
				if lastClose > ema21*(1+EMAResetBuffer) && lastClose > prevClose {
					if e.RedCandlesBelow[oid] > 0 {
						log.Printf("[EMA21] %s: Recovered above EMA+buffer, reset red counter (Close=%.2f EMA=%.2f)",
							trade.Symbol, lastClose, ema21)
					}
					e.RedCandlesBelow[oid] = 0
				}
				// Weak close near EMA: neither increment nor reset
			} else {
				// Blueprint-literal mode: any interruption resets counter
				if e.RedCandlesBelow[oid] > 0 {
					log.Printf("[EMA21] %s: Streak broken, reset red counter (Close=%.2f EMA=%.2f)",
						trade.Symbol, lastClose, ema21)
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
	if e.CancelOrder != nil {
		if trade.SLOID != "" {
			e.CancelOrder(trade.SLOID)
		}
		if trade.TargetOID != "" {
			e.CancelOrder(trade.TargetOID)
		}
	}

	exitQty := trade.RemainingQty
	if exitQty == 0 {
		exitQty = trade.Qty
	}

	if e.PlaceOrder != nil {
		e.PlaceOrder(trade.Symbol, exitQty, true, "MARKET", 0) // Sell to close
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
		regime, openCount, config.MaxOpenPositions,
		e.Scanner.CapitalMultiplier*100))
}

// ComputeEMA21 computes 21-day EMA (primary exit indicator)
func ComputeEMA21(closes []float64) float64 {
	return computeEMAForPeriod(closes, config.EMA21Period)
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
