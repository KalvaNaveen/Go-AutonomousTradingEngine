package agents

import (
	"fmt"
	"log"
	"math"
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

	// Consecutive loss circuit breaker
	if r.ConsecutiveLosses >= config.MaxConsecutiveLosses {
		r.EngineStopped = true
		r.StopReason = fmt.Sprintf("%d_CONSECUTIVE_LOSSES", r.ConsecutiveLosses)
		return false, r.StopReason
	}

	// Daily loss circuit breaker
	if r.DailyPnl < -(r.TotalCapital * config.DailyLossLimitPct) {
		r.EngineStopped = true
		r.StopReason = fmt.Sprintf("DAILY_LOSS_LIMIT_%.0f", r.DailyPnl)
		return false, r.StopReason
	}

	// Max open positions
	if len(r.OpenPositions) >= config.MaxOpenPositions {
		return false, "MAX_OPEN_POSITIONS"
	}

	// Max positions per strategy
	strategy, _ := signal["strategy"].(string)
	stratCount := 0
	for _, pos := range r.OpenPositions {
		if pos.Strategy == strategy {
			stratCount++
		}
	}
	if stratCount >= config.MaxPositionsPerStrat {
		return false, fmt.Sprintf("MAX_PER_STRAT_%s", strategy)
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
	if isShort {
		if stopPrice <= entryPrice {
			return false, "STOP_BELOW_ENTRY_SHORT"
		}
	} else {
		if stopPrice >= entryPrice {
			return false, "STOP_ABOVE_ENTRY"
		}
	}

	return true, "APPROVED"
}

func (r *RiskAgent) CalculatePositionSize(entry, stop float64, regime, strategy, symbol string) int {
	// ═══ STRATEGY-AWARE REGIME SCALING ═══
	// Key insight: Mean reversion LOVES volatility (bigger swings = better entries).
	// Momentum/trend HATES chop (false breakouts, whipsaws).
	// Scaling must match strategy type to regime, not apply one-size-fits-all.

	meanRevStrategies := map[string]bool{
		"S2_BB_MEAN_REV": true, "S6_VWAP_BAND": true, "S7_MEAN_REV_LONG": true,
		"S11_VWAP_REVERT": true, "S12_EOD_REVERT": true, "S14_RSI_SCALP": true,
		"S15_RSI_SWING": true,
	}

	momentumStrategies := map[string]bool{
		"S1_MA_CROSS": true, "S3_ORB": true, "S9_MTF_MOMENTUM": true,
		"S13_SECTOR_ROT": true, "S6_TREND_SHORT": true,
	}

	var scale float64
	if meanRevStrategies[strategy] {
		// Mean reversion: SCALE UP in volatile (more reversion), DOWN in trend
		switch regime {
		case "BULL":
			scale = 0.85 // Trending market — reversion less reliable
		case "NORMAL":
			scale = 1.0
		case "VOLATILE":
			scale = 1.15 // Bigger swings = better reversion entries
		case "CHOP":
			scale = 1.0 // Choppy = good for mean reversion
		case "BEAR_PANIC":
			scale = 0.70 // Extreme moves may not revert intraday
		case "EXTREME_PANIC":
			scale = 0.25
		default:
			scale = 1.0
		}
	} else if momentumStrategies[strategy] {
		// Momentum: SCALE UP in trends, BLOCK in chop/panic
		switch regime {
		case "BULL":
			scale = 1.0
		case "NORMAL":
			scale = 1.0
		case "VOLATILE":
			scale = 0.50 // Volatile = risky for momentum
		case "CHOP":
			scale = 0.0 // BLOCK: chop kills momentum — S13 lost ₹2,347 in chop on Apr 29
		case "BEAR_PANIC":
			scale = 0.0 // BLOCK: panic reverses too fast for momentum
		case "EXTREME_PANIC":
			scale = 0.0
		default:
			scale = 1.0
		}
	} else {
		// Default (S8_VOL_PIVOT, macro, etc.)
		switch regime {
		case "BULL", "NORMAL":
			scale = 1.0
		case "VOLATILE":
			scale = 0.85
		case "CHOP":
			scale = 0.60
		case "BEAR_PANIC":
			scale = 0.50
		case "EXTREME_PANIC":
			scale = 0.25
		default:
			scale = 1.0
		}
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
