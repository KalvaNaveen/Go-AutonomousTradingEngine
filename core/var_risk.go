package core

import (
	"fmt"
	"log"
	"math"
	"sort"
	"sync"
)

// PortfolioVaR implements Historical Simulation Value-at-Risk for the portfolio.
//
// VaR answers: "What is the maximum loss I can expect in 1 day with 95% confidence?"
//
// Implementation:
// 1. Collect daily returns for each held symbol over the last 30 trading days
// 2. Compute portfolio-weighted historical returns
// 3. Sort returns and find the 5th percentile (95% VaR)
// 4. Before each new trade, check if adding it would push VaR beyond the limit
//
// This matches the Historical Simulation VaR used by QuantConnect's risk management
// module and is the standard approach at Goldman Sachs, Morgan Stanley, etc.

const (
	VaRConfidence    = 0.95   // 95% confidence level
	VaRLookbackDays  = 30     // Number of historical days to use
	VaRMaxPct        = 0.05   // Max 5% of capital at risk (1-day 95% VaR)
)

// VaREngine computes portfolio-level Value at Risk
type VaREngine struct {
	mu             sync.RWMutex
	DailyReturns   map[uint32][]float64 // token -> daily return series
	LastVaR        float64              // Last computed VaR in Rs.
	LastVaRPct     float64              // VaR as percentage of capital
	Capital        float64
	Enabled        bool
}

// Position represents a current holding for VaR calculation
type VaRPosition struct {
	Token      uint32
	Symbol     string
	EntryPrice float64
	Qty        int
	IsShort    bool
}

func NewVaREngine(capital float64) *VaREngine {
	return &VaREngine{
		DailyReturns: make(map[uint32][]float64),
		Capital:      capital,
		Enabled:      true,
	}
}

// LoadHistoricalReturns computes daily returns from the DailyCache close prices
func (v *VaREngine) LoadHistoricalReturns(closesMap map[uint32][]float64) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.DailyReturns = make(map[uint32][]float64)

	for token, closes := range closesMap {
		if len(closes) < VaRLookbackDays+1 {
			continue
		}

		// Use the last VaRLookbackDays+1 closes to compute VaRLookbackDays returns
		start := len(closes) - VaRLookbackDays - 1
		returns := make([]float64, 0, VaRLookbackDays)
		for i := start + 1; i < len(closes); i++ {
			if closes[i-1] > 0 {
				ret := (closes[i] - closes[i-1]) / closes[i-1]
				returns = append(returns, ret)
			}
		}
		if len(returns) >= 10 { // Need at least 10 data points
			v.DailyReturns[token] = returns
		}
	}

	log.Printf("[VaR] Loaded historical returns for %d symbols", len(v.DailyReturns))
}

// ComputePortfolioVaR calculates the current portfolio's 1-day 95% VaR
func (v *VaREngine) ComputePortfolioVaR(positions []VaRPosition) (varRs float64, varPct float64) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	if len(positions) == 0 || len(v.DailyReturns) == 0 {
		return 0, 0
	}

	// Step 1: Compute each position's notional exposure
	type posWeight struct {
		token    uint32
		notional float64 // absolute exposure in Rs.
		sign     float64 // +1 for long, -1 for short
	}

	var posWeights []posWeight
	totalNotional := 0.0
	for _, pos := range positions {
		notional := pos.EntryPrice * float64(pos.Qty)
		sign := 1.0
		if pos.IsShort {
			sign = -1.0
		}
		posWeights = append(posWeights, posWeight{
			token: pos.Token, notional: notional, sign: sign,
		})
		totalNotional += notional
	}

	if totalNotional == 0 {
		return 0, 0
	}

	// Step 2: Compute portfolio returns for each historical day
	// Find minimum number of return observations across all positions
	minObs := VaRLookbackDays
	for _, pw := range posWeights {
		returns, ok := v.DailyReturns[pw.token]
		if !ok {
			continue
		}
		if len(returns) < minObs {
			minObs = len(returns)
		}
	}

	if minObs < 5 {
		return 0, 0
	}

	portfolioReturns := make([]float64, minObs)
	for _, pw := range posWeights {
		returns, ok := v.DailyReturns[pw.token]
		if !ok {
			continue
		}
		// Align to the most recent observations
		offset := len(returns) - minObs
		weight := (pw.notional * pw.sign) / totalNotional
		for i := 0; i < minObs; i++ {
			portfolioReturns[i] += weight * returns[offset+i]
		}
	}

	// Step 3: Sort and find VaR percentile
	sorted := make([]float64, len(portfolioReturns))
	copy(sorted, portfolioReturns)
	sort.Float64s(sorted) // Ascending: worst returns first

	// 5th percentile index for 95% VaR
	percentileIdx := int(math.Floor(float64(len(sorted)) * (1.0 - VaRConfidence)))
	if percentileIdx >= len(sorted) {
		percentileIdx = len(sorted) - 1
	}
	if percentileIdx < 0 {
		percentileIdx = 0
	}

	// VaR is the loss at the percentile (negative return)
	varReturn := sorted[percentileIdx]

	// Convert to Rs.
	varRs = math.Abs(varReturn * totalNotional)
	varPct = varRs / v.Capital * 100

	v.mu.RUnlock()
	v.mu.Lock()
	v.LastVaR = varRs
	v.LastVaRPct = varPct
	v.mu.Unlock()
	v.mu.RLock()

	return varRs, varPct
}

// CheckNewTrade returns (allowed, reason) — checks if adding a new trade
// would push portfolio VaR beyond the limit
func (v *VaREngine) CheckNewTrade(currentPositions []VaRPosition, newPos VaRPosition) (bool, string) {
	if !v.Enabled {
		return true, "VAR_DISABLED"
	}

	// Compute VaR with the new position included
	allPositions := append(currentPositions, newPos)
	varRs, varPct := v.ComputePortfolioVaR(allPositions)

	maxVaR := v.Capital * VaRMaxPct

	if varRs > maxVaR {
		log.Printf("[VaR] BLOCKED: Adding %s would push VaR to Rs.%.0f (%.1f%%) > limit Rs.%.0f (%.0f%%)",
			newPos.Symbol, varRs, varPct, maxVaR, VaRMaxPct*100)
		return false, fmt.Sprintf("VAR_BREACH_%.0f_RS", varRs)
	}

	log.Printf("[VaR] APPROVED: %s | PortfolioVaR=Rs.%.0f (%.1f%%), Limit=Rs.%.0f",
		newPos.Symbol, varRs, varPct, maxVaR)
	return true, "VAR_OK"
}

// GetVaRSummary returns the current VaR stats for dashboard display
func (v *VaREngine) GetVaRSummary() map[string]interface{} {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return map[string]interface{}{
		"var_rs":     v.LastVaR,
		"var_pct":    v.LastVaRPct,
		"max_var_rs": v.Capital * VaRMaxPct,
		"confidence": VaRConfidence,
		"lookback":   VaRLookbackDays,
		"enabled":    v.Enabled,
		"symbols":    len(v.DailyReturns),
	}
}

// Suppress unused import lint
var _ = fmt.Sprintf
