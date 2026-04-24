package agents

import (
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"bnf_go_engine/config"
	"bnf_go_engine/core"
)

// RiskAgent — exact port of Python risk_agent.py
type RiskAgent struct {
	TotalCapital      float64
	ActiveCapital     float64
	RiskReserve       float64
	DailyPnl          float64
	OpenPositions     map[string]*core.Trade
	DailyTrades       []*core.Trade
	EngineStopped     bool
	StopReason        string
	ConsecutiveLosses int
	WeeklyPnl         float64

	misMarginCache map[string]float64
	misCacheDate   string
}

func NewRiskAgent(capital float64) *RiskAgent {
	return &RiskAgent{
		TotalCapital:   capital,
		ActiveCapital:  capital * config.ActiveCapitalPct,
		RiskReserve:    capital * config.RiskReservePct,
		OpenPositions:  make(map[string]*core.Trade),
		misMarginCache: make(map[string]float64),
	}
}

func (r *RiskAgent) ResetDaily() {
    r.DailyPnl = 0
    r.DailyTrades = nil
    r.EngineStopped = false
    r.StopReason = ""
    r.ConsecutiveLosses = 0
    
    // Only wipe OpenPositions if it's empty, so we don't accidentally
    // delete trades that were just restored via crash recovery.
    if len(r.OpenPositions) == 0 {
        r.OpenPositions = make(map[string]*core.Trade)
    }
    log.Println("[Risk] Daily stats reset successfully.")
}

func (r *RiskAgent) ApproveTrade(signal map[string]interface{}) (bool, string) {
	if r.EngineStopped {
		return false, fmt.Sprintf("ENGINE_STOPPED: %s", r.StopReason)
	}

	strategy, _ := signal["strategy"].(string)
	regime, _ := signal["regime"].(string)

	// Cooldown file check
	cooldownFile := config.BaseDir + string(os.PathSeparator) + "data" + string(os.PathSeparator) + "cooldown.txt"
	if data, err := os.ReadFile(cooldownFile); err == nil {
		if expiry, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64); err == nil {
			if float64(time.Now().Unix()) < expiry {
				r.EngineStopped = true
				return false, "ENFORCED_3_DAY_COOLDOWN"
			}
		}
	}

	// Weekly drawdown
	weeklyDrawdownPct := 0.08
	if r.WeeklyPnl <= -(r.ActiveCapital * weeklyDrawdownPct) {
		r.EngineStopped = true
		r.StopReason = "WEEKLY_DRAWDOWN_8%"
		os.MkdirAll(config.BaseDir+string(os.PathSeparator)+"data", 0755)
		os.WriteFile(cooldownFile,
			[]byte(strconv.FormatFloat(float64(time.Now().Unix())+3*24*3600, 'f', 0, 64)),
			0644)
		return false, r.StopReason
	}

	// Sector check (simplified — full implementation requires DataAgent reference)
	// TODO: port sector check when DataAgent is fully integrated

	// VIX check would go here when data agent is connected
	// Skipped for now — will be wired once DataAgent is ported

	// Daily loss limit
	if r.DailyPnl <= -(r.ActiveCapital * config.DailyLossLimitPct) {
		r.EngineStopped = true
		r.StopReason = fmt.Sprintf("DAILY_LOSS_LIMIT Rs.%.0f", math.Abs(r.DailyPnl))
		return false, r.StopReason
	}

	// Consecutive losses
	if r.ConsecutiveLosses >= config.MaxConsecutiveLosses {
		r.EngineStopped = true
		r.StopReason = fmt.Sprintf("%d_CONSECUTIVE_LOSSES", config.MaxConsecutiveLosses)
		return false, r.StopReason
	}

	// Max open positions
	if len(r.OpenPositions) >= config.MaxOpenPositions {
		return false, fmt.Sprintf("MAX_%d_POSITIONS", config.MaxOpenPositions)
	}

	// Capital availability (MIS margin)
	newSymbol, _ := signal["symbol"].(string)
	newLeverage := r.GetMISLeverage(newSymbol)

	deployedMargin := 0.0
	for _, pos := range r.OpenPositions {
		posLev := r.GetMISLeverage(pos.Symbol)
		deployedMargin += (pos.EntryPrice * float64(pos.Qty)) / posLev
	}

	entryPrice, _ := signal["entry_price"].(float64)
	estimatedMargin := entryPrice / newLeverage
	if deployedMargin+estimatedMargin > r.ActiveCapital {
		return false, "INSUFFICIENT_CAPITAL"
	}

	// Duplicate symbol check
	symbolStr, _ := signal["symbol"].(string)
	for _, pos := range r.OpenPositions {
		if pos.Symbol == symbolStr {
			return false, fmt.Sprintf("DUPLICATE_%s", symbolStr)
		}
	}

	// Stop price validation
	stopPrice, _ := signal["stop_price"].(float64)
	if stopPrice <= 0 {
		return false, "NO_STOP_DEFINED"
	}

	isShort, _ := signal["is_short"].(bool)
	targetPrice, _ := signal["target_price"].(float64)

	var reward, risk float64
	if isShort {
		if stopPrice <= entryPrice {
			return false, "STOP_BELOW_ENTRY_SHORT"
		}
		if targetPrice >= entryPrice {
			return false, "TARGET_ABOVE_ENTRY_SHORT"
		}
		reward = entryPrice - targetPrice
		risk = stopPrice - entryPrice
	} else {
		if stopPrice >= entryPrice {
			return false, "STOP_ABOVE_ENTRY"
		}
		if targetPrice <= entryPrice {
			return false, "TARGET_BELOW_ENTRY"
		}
		reward = targetPrice - entryPrice
		risk = entryPrice - stopPrice
	}

	rr := 0.0
	if risk > 0 {
		rr = reward / risk
	}

	// Strategy-aware RR minimums
	meanRevStrategies := map[string]bool{
		"S2_BB_MEAN_REV": true, "S6_VWAP_BAND": true, "S7_MEAN_REV_LONG": true,
		"S14_RSI_SCALP": true, "S15_RSI_SWING": true,
	}

	if strategy == "S3_ORB" && rr < 1.0 {
		return false, fmt.Sprintf("S3_RR_%.2f_BELOW_1.0", rr)
	} else if meanRevStrategies[strategy] {
		if rr < 0.5 {
			return false, fmt.Sprintf("MR_RR_%.2f_BELOW_0.5", rr)
		}
	} else if regime == "CHOP" && rr < 1.5 {
		// Strict RR enforcement in sideways markets
		return false, fmt.Sprintf("CHOP_RR_%.2f_BELOW_1.5", rr)
	} else if rr < 1.0 {
		return false, fmt.Sprintf("RR_%.2f_BELOW_1.0", rr)
	}

	return true, "APPROVED"
}

func (r *RiskAgent) CalculatePositionSize(entry, stop float64, regime, strategy, symbol string) int {
	baseScale := map[string]float64{
		"BULL": 1.0, "NORMAL": 1.0, "VOLATILE": 0.75,
		"BEAR_PANIC": 0.45, "EXTREME_PANIC": 0.25, "CHOP": 0.30,
	}
	scale := baseScale[regime]
	if scale == 0 {
		scale = 1.0
	}

	// Mean-reversion strategies scaling
	meanRev := map[string]bool{
		"S2_BB_MEAN_REV": true, "S6_VWAP_BAND": true, "S7_MEAN_REV_LONG": true,
	}
	if meanRev[strategy] {
		if regime == "CHOP" {
			scale *= 0.60
		} else {
			scale *= 0.85
		}
	}

	// Macro strategy scaling
	if strategy == "S8_MACRO_LONG" || strategy == "S8_MACRO_SHORT" {
		macroScale := map[string]float64{
			"VOLATILE": 0.60, "CHOP": 0.50, "BEAR_PANIC": 0.50, "EXTREME_PANIC": 0.30,
		}
		ms := macroScale[regime]
		if ms == 0 {
			ms = 0.85
		}
		scale *= ms
	}

	leverage := r.GetMISLeverage(symbol)

	riskRs := r.TotalCapital * config.MaxRiskPerTradePct * scale * config.STTBuffer
	rps := math.Abs(entry - stop)
	if rps <= 0 {
		return 0
	}

	sharesByRisk := int(riskRs / rps)

	// Margin-based cap
	marginPerShare := entry / leverage
	sharesByMargin := int((r.ActiveCapital * config.MaxPositionPct) / (marginPerShare * 1.001))

	// Notional exposure cap
	maxNotional := r.TotalCapital * config.MaxPositionPct
	sharesByNotional := int(maxNotional / (entry * 1.001))

	// Available margin cap
	deployedMargin := 0.0
	for _, pos := range r.OpenPositions {
		posLev := r.GetMISLeverage(pos.Symbol)
		deployedMargin += (pos.EntryPrice * float64(pos.Qty)) / posLev
	}
	freeMargin := math.Max(r.ActiveCapital-deployedMargin, 0)
	sharesByFree := int(freeMargin / (marginPerShare * 1.001))

	qty := sharesByRisk
	if sharesByMargin < qty {
		qty = sharesByMargin
	}
	if sharesByNotional < qty {
		qty = sharesByNotional
	}
	if sharesByFree < qty {
		qty = sharesByFree
	}

	if qty <= 0 {
		return 0
	}

	log.Printf("[Risk] %s sizing: risk=%d, margin=%d, notional=%d, free=%d, lev=%.1fx → qty=%d",
		symbol, sharesByRisk, sharesByMargin, sharesByNotional, sharesByFree, leverage, qty)
	return qty
}

func (r *RiskAgent) GetMISLeverage(symbol string) float64 {
	today := time.Now().Format("2006-01-02")
	if r.misCacheDate != today {
		r.misMarginCache = make(map[string]float64)
		r.misCacheDate = today
		// TODO: Fetch from Kite margins API when broker client is wired
	}
	if lev, ok := r.misMarginCache[symbol]; ok {
		return lev
	}
	return 5.0 // Default MIS leverage
}

func (r *RiskAgent) RegisterOpen(oid string, pos *core.Trade) {
	if !pos.IsShort {
		// Default false is fine
	}
	r.OpenPositions[oid] = pos
}

func (r *RiskAgent) ClosePosition(oid string, exitPrice float64) float64 {
	pos, exists := r.OpenPositions[oid]
	if !exists {
		log.Printf("[Risk] WARNING: ClosePosition called for OID=%s but position NOT FOUND in OpenPositions (PnL will be 0)", oid)
		return 0.0
	}
	delete(r.OpenPositions, oid)

	var grossLegPnl, buyVal, sellVal float64
	if pos.IsShort {
		grossLegPnl = (pos.EntryPrice - exitPrice) * float64(pos.Qty)
		buyVal = exitPrice * float64(pos.Qty)
		sellVal = pos.EntryPrice * float64(pos.Qty)
	} else {
		grossLegPnl = (exitPrice - pos.EntryPrice) * float64(pos.Qty)
		buyVal = pos.EntryPrice * float64(pos.Qty)
		sellVal = exitPrice * float64(pos.Qty)
	}

	product := pos.Product
	if product == "" {
		product = "MIS"
	}
	chargeResult := core.ComputeTradeCharges(buyVal, sellVal, product, 0)
	charges := chargeResult["total"]

	finalLegPnl := grossLegPnl - charges
	r.DailyPnl += finalLegPnl
	r.WeeklyPnl += finalLegPnl

	totalTradePnl := pos.RealisedPnl + finalLegPnl
	pos.RealisedPnl = totalTradePnl // Persist for GetDailyStats()
	if totalTradePnl > 0 {
		r.ConsecutiveLosses = 0
	} else {
		r.ConsecutiveLosses++
	}

	r.DailyTrades = append(r.DailyTrades, pos)
	return totalTradePnl
}

// RestoreDailyTrades loads today's previously completed trades into memory from the DB
func (r *RiskAgent) RestoreDailyTrades(trades []map[string]interface{}) {
	for _, tMap := range trades {
		pnl, _ := tMap["gross_pnl"].(float64)
		pos := &core.Trade{
			Symbol:      tMap["symbol"].(string),
			Strategy:    tMap["strategy"].(string),
			RealisedPnl: pnl,
		}
		r.DailyTrades = append(r.DailyTrades, pos)
		r.DailyPnl += pnl // Explicitly accumulate PnL here
	}
	log.Printf("[Risk] Restored %d completed trades from journal for daily stats (PnL=%.2f)", len(trades), r.DailyPnl)
}

func (r *RiskAgent) GetDailyStats() map[string]interface{} {
	t := r.DailyTrades
	if len(t) == 0 {
		return map[string]interface{}{
			"total": 0, "wins": 0, "losses": 0, "win_rate": 0.0,
			"gross_pnl": 0.0, "avg_win": 0.0, "avg_loss": 0.0,
			"loss_streak": r.ConsecutiveLosses, "capital": r.TotalCapital,
		}
	}

	wins := 0
	totalPnl := 0.0
	winPnl := 0.0
	lossPnl := 0.0
	winCount := 0
	lossCount := 0

	for _, trade := range t {
		pnl := trade.RealisedPnl
		totalPnl += pnl
		if pnl > 0 {
			wins++
			winPnl += pnl
			winCount++
		} else {
			lossPnl += pnl
			lossCount++
		}
	}

	avgWin := 0.0
	if winCount > 0 {
		avgWin = winPnl / float64(winCount)
	}
	avgLoss := 0.0
	if lossCount > 0 {
		avgLoss = lossPnl / float64(lossCount)
	}

	profitFactor := 0.0
	if lossPnl != 0 {
		profitFactor = math.Abs(winPnl / lossPnl)
	} else if winPnl > 0 {
		profitFactor = 99.0 // All wins, no losses
	}

	bestTrade := 0.0
	for _, trade := range t {
		pnl := trade.RealisedPnl
		if pnl > bestTrade {
			bestTrade = pnl
		}
	}

	return map[string]interface{}{
		"total":         len(t),
		"wins":          wins,
		"losses":        len(t) - wins,
		"win_rate":      float64(wins) / float64(len(t)) * 100,
		"gross_pnl":     totalPnl,
		"avg_win":       avgWin,
		"avg_loss":      avgLoss,
		"loss_streak":   r.ConsecutiveLosses,
		"capital":       r.TotalCapital,
		"profit_factor": math.Round(profitFactor*100) / 100,
		"best_trade":    math.Round(bestTrade*100) / 100,
	}
}

func (r *RiskAgent) ResetWeeklyPnl() {
	r.WeeklyPnl = 0.0
	log.Println("[Risk] Weekly PnL reset. New week starting.")
}
