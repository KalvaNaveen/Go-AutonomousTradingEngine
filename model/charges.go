package model

// CNC (equity delivery) charge rates — Zerodha official pricing, verified from
// https://zerodha.com/charges/ on 2026-07-07. Brokerage on delivery is ZERO;
// everything below is statutory/exchange/depository.
const (
	cncSTTPct      = 0.001     // 0.1%   STT on buy AND sell
	cncTxnPct      = 0.0000307 // 0.00307% NSE exchange transaction charge, both sides
	cncSEBIPct     = 0.000001  // ₹10/crore SEBI turnover fee, both sides
	cncStampPct    = 0.00015   // 0.015% stamp duty, BUY side only
	cncGSTPct      = 0.18      // 18% GST on (brokerage + SEBI + txn charges)
	cncDPChargeINR = 15.34     // per-scrip DP charge on the SELL day (CDSL+Zerodha+GST)
)

// CNCCharges returns the total round-trip cost of a delivery trade with the
// given buy and sell turnover (price × qty). Pass sellValue 0 for the buy leg
// alone (open positions).
func CNCCharges(buyValue, sellValue float64) float64 {
	var total float64
	if buyValue > 0 {
		txn, sebi := buyValue*cncTxnPct, buyValue*cncSEBIPct
		total += buyValue*cncSTTPct + txn + sebi + buyValue*cncStampPct + cncGSTPct*(txn+sebi)
	}
	if sellValue > 0 {
		txn, sebi := sellValue*cncTxnPct, sellValue*cncSEBIPct
		total += sellValue*cncSTTPct + txn + sebi + cncGSTPct*(txn+sebi) + cncDPChargeINR
	}
	return total
}
