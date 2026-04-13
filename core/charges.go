package core

import (
	"math"
)

// Zerodha Equity Intraday (MIS) Charges — As per https://zerodha.com/charges/#tab-equities
//
// WHAT IS CHARGED ON WHAT (for intraday equity MIS):
//
// ┌─────────────────────────┬──────────────────────────────────────────────────┐
// │ Charge                  │ Applied on                                      │
// ├─────────────────────────┼──────────────────────────────────────────────────┤
// │ Brokerage               │ ₹20 or 0.03% per EXECUTED ORDER (buy & sell)    │
// │ STT                     │ 0.025% on SELL SIDE ONLY                        │
// │ Exchange Txn Charges    │ 0.00297% on TOTAL TURNOVER (buy + sell)         │
// │ SEBI Charges            │ ₹10 per crore on TURNOVER + GST                │
// │ Stamp Duty              │ 0.003% on BUY SIDE ONLY                        │
// │ GST                     │ 18% of (Brokerage + Exchange Txn + SEBI)        │
// │ DP Charges              │ ₹0 (no delivery in intraday)                   │
// │ IPFT (NSE)              │ ₹0.01 per crore on TURNOVER                    │
// └─────────────────────────┴──────────────────────────────────────────────────┘
//
// "Round-trip" means one buy + one sell = one complete intraday trade.
// You PAY brokerage on BOTH buy and sell orders (up to ₹20 each).
// You PAY STT only when SELLING.
// You PAY stamp duty only when BUYING.

// ComputeTradeCharges calculates all statutory charges for a round-trip intraday MIS trade.
// buyValue = entry_price * qty (or exit_price * qty for short)
// sellValue = exit_price * qty (or entry_price * qty for short)
func ComputeTradeCharges(buyValue, sellValue float64, product string, slippagePct float64) map[string]float64 {
	turnover := buyValue + sellValue

	// Exchange transaction charges (NSE): 0.00297% of total turnover
	txnChg := turnover * 0.0000297

	// SEBI charges: ₹10 per crore of turnover
	sebi := turnover * 0.000001

	// IPFT (NSE Investor Protection Fund Trust): ₹0.01 per crore
	ipft := turnover * 0.0000001

	var brok, stt, stamp, dpCharge float64

	if product == "CNC" {
		// Delivery: ₹0 brokerage, STT on both sides, stamp on buy, DP on sell
		brok = 0.0
		stt = turnover * 0.001 // 0.1% on both buy and sell for delivery
		stamp = buyValue * 0.00015 // 0.015% on buy side
		dpCharge = 15.34 // ₹3.5 CDSL + ₹9.5 Zerodha + ₹2.34 GST (per scrip on sell)
	} else {
		// Intraday (MIS): ₹20 or 0.03% per order on EACH side
		brok = math.Min(buyValue*0.0003, 20.0) + math.Min(sellValue*0.0003, 20.0)

		// STT: 0.025% on SELL side ONLY for intraday
		stt = sellValue * 0.00025

		// Stamp duty: 0.003% on BUY side ONLY
		stamp = buyValue * 0.00003

		// No DP charges for intraday
		dpCharge = 0.0
	}

	// GST: 18% of (brokerage + exchange txn charges + SEBI charges)
	gst := (brok + txnChg + sebi) * 0.18

	// Slippage estimate (market impact)
	slippage := turnover * slippagePct

	total := brok + stt + txnChg + sebi + stamp + gst + dpCharge + ipft + slippage

	return map[string]float64{
		"brokerage":   math.Round(brok*100) / 100,
		"stt":         math.Round(stt*100) / 100,
		"txn_charges": math.Round(txnChg*100) / 100,
		"sebi_fee":    math.Round(sebi*100) / 100,
		"stamp_duty":  math.Round(stamp*100) / 100,
		"gst":         math.Round(gst*100) / 100,
		"dp_charge":   math.Round(dpCharge*100) / 100,
		"ipft":        math.Round(ipft*100) / 100,
		"slippage":    math.Round(slippage*100) / 100,
		"total":       math.Round(total*100) / 100,
	}
}

// ComputeChargesFromTrade convenience function — determines buy/sell values from direction
func ComputeChargesFromTrade(entry, exit float64, qty int, isShort bool, product string, slippagePct float64) float64 {
	if entry <= 0 || exit <= 0 || qty <= 0 {
		return 0.0
	}
	var buyVal, sellVal float64
	if isShort {
		// Short: sell first (at entry), buy back later (at exit)
		buyVal = exit * float64(qty)
		sellVal = entry * float64(qty)
	} else {
		// Long: buy first (at entry), sell later (at exit)
		buyVal = entry * float64(qty)
		sellVal = exit * float64(qty)
	}
	result := ComputeTradeCharges(buyVal, sellVal, product, slippagePct)
	return result["total"]
}
