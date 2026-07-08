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

// Aggregate per-side rates (fraction of turnover), derived from the constants
// above. Used to solve for exit prices that achieve a NET return target.
const (
	buySideRate  = cncSTTPct + cncTxnPct + cncSEBIPct + cncStampPct + cncGSTPct*(cncTxnPct+cncSEBIPct)
	sellSideRate = cncSTTPct + cncTxnPct + cncSEBIPct + cncGSTPct*(cncTxnPct+cncSEBIPct)
)

// SellPriceForNetPct returns the exit price X at which selling qty shares
// bought at entry yields netPct return AFTER all CNC charges:
//
//	(X−E)·Q − a·E·Q − b·X·Q − DP = t·E·Q   ⇒   X = (E(1+t+a) + DP/Q) / (1−b)
//
// where a/b are the buy/sell-side charge rates and DP the flat depository fee.
func SellPriceForNetPct(entry float64, qty int, netPct float64) float64 {
	if entry <= 0 || qty <= 0 {
		return 0
	}
	t := netPct / 100
	return (entry*(1+t+buySideRate) + cncDPChargeINR/float64(qty)) / (1 - sellSideRate)
}

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
