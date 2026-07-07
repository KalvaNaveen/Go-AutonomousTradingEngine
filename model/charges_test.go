package model

import "testing"

// Hand-computed against Zerodha's published CNC rates (zerodha.com/charges):
// buy ₹10,000 / sell ₹11,000 round trip.
//
//	buy : STT 10.00 + txn 0.307 + SEBI 0.010 + stamp 1.50 + GST 0.18×(0.317) ≈ 11.874
//	sell: STT 11.00 + txn 0.338 + SEBI 0.011 + GST 0.18×(0.349) + DP 15.34  ≈ 26.751
//	total ≈ 38.625
func TestCNCCharges(t *testing.T) {
	got := CNCCharges(10000, 11000)
	if got < 38.5 || got > 38.8 {
		t.Errorf("CNCCharges(10000,11000) = %.3f, want ≈38.62", got)
	}

	// Buy leg only (open position): no DP, no sell-side STT.
	buyOnly := CNCCharges(10000, 0)
	if buyOnly < 11.8 || buyOnly > 12.0 {
		t.Errorf("buy-leg charges = %.3f, want ≈11.87", buyOnly)
	}

	if CNCCharges(0, 0) != 0 {
		t.Errorf("zero turnover must cost zero")
	}
}

func TestNetPnL(t *testing.T) {
	p := Position{Qty: 100, EntryPrice: 100, ExitPrice: 110,
		PnL: 1000, Charges: 38.62, Status: StatusClosed}
	if p.NetPnL() != 1000-38.62 {
		t.Errorf("NetPnL = %.2f", p.NetPnL())
	}
	if pct := p.NetPct(); pct < 9.60 || pct > 9.62 {
		t.Errorf("NetPct = %.3f, want ≈9.614", pct)
	}
}
