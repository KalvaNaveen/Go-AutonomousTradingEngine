package engine

import (
	"context"
	"testing"

	"bnf_go_engine/broker"
	"bnf_go_engine/model"
	"bnf_go_engine/quotes"
	"bnf_go_engine/scanner"
)

func TestTrail(t *testing.T) {
	cases := []struct {
		name                 string
		peak, sl, last, high float64
		entryDay             bool
		wantPeak, wantSL     float64
	}{
		// User's spec: entry 10 → SL 9.8; price 12 → SL 11.76.
		{"initial", 10, 9.8, 10, 10, true, 10, 9.8},
		{"rises to 12", 10, 9.8, 12, 12, false, 12, 11.76},
		{"never trails down", 12, 11.76, 11, 12, false, 12, 11.76},
		{"day high counts after entry day", 10, 9.8, 10.5, 13, false, 13, 12.74},
		{"day high IGNORED on entry day (pre-ownership)", 10, 9.8, 10.5, 13, true, 10.5, 10.29},
	}
	for _, c := range cases {
		gotPeak, gotSL := Trail(c.peak, c.sl, c.last, c.high, c.entryDay, 2.0)
		if gotPeak != c.wantPeak {
			t.Errorf("%s: peak = %.4f, want %.4f", c.name, gotPeak, c.wantPeak)
		}
		if diff := gotSL - c.wantSL; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("%s: sl = %.4f, want %.4f", c.name, gotSL, c.wantSL)
		}
	}
}

// stubScanner returns a fixed list — no network.
type stubScanner struct{ stocks []scanner.Stock }

func (s stubScanner) Fetch(_ context.Context, _ int) ([]scanner.Stock, error) {
	return s.stocks, nil
}

func TestRunCycle_CarryNetting(t *testing.T) {
	eng, st := newTestEngine(t, fakeQuotes{quotes.OHLC{Low: 95, Last: 100, High: 101}})
	// Held from a prior day: AAA (will be re-listed → carried), BBB (fell off → sold).
	seedOpen(t, st, model.Position{Symbol: "AAA", Qty: 10, EntryPrice: 100, SLPrice: 90, PeakPrice: 100})
	seedOpen(t, st, model.Position{Symbol: "BBB", Qty: 10, EntryPrice: 100, SLPrice: 90, PeakPrice: 100})

	// Today's scan: AAA (held) + CCC (new).
	eng.Scanner = stubScanner{stocks: []scanner.Stock{
		{Symbol: "AAA", Name: "AAA Ltd", Close: 100, Source: "s1"},
		{Symbol: "CCC", Name: "CCC Ltd", Close: 50, Source: "s2"},
	}}

	if err := eng.RunCycle(context.Background()); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	open, _ := st.OpenPositions()
	openBy := map[string]model.Position{}
	for _, p := range open {
		openBy[p.Symbol] = p
	}

	// AAA carried: still open, not re-bought (one row), carry_count bumped.
	if p, ok := openBy["AAA"]; !ok {
		t.Fatalf("AAA should still be open (carried)")
	} else {
		if p.CarryCount != 1 {
			t.Errorf("AAA carry_count = %d, want 1", p.CarryCount)
		}
		if p.EntryPrice != 100 {
			t.Errorf("AAA must keep original entry (got %.2f)", p.EntryPrice)
		}
	}
	count := 0
	for _, p := range open {
		if p.Symbol == "AAA" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("AAA re-bought: %d open rows, want 1", count)
	}

	// BBB sold (fell off the list).
	if _, ok := openBy["BBB"]; ok {
		t.Errorf("BBB should have been squared off")
	}
	closed, _ := st.ClosedPositions(10)
	if len(closed) != 1 || closed[0].Symbol != "BBB" || closed[0].ExitReason != model.ExitSquareOff {
		t.Errorf("expected exactly BBB closed via squareoff, got %+v", closed)
	}

	// CCC bought fresh with a 2%% trailing stop seeded from entry.
	p, ok := openBy["CCC"]
	if !ok {
		t.Fatalf("CCC should have been bought")
	}
	if wantSL := 50 * 0.98; p.SLPrice != wantSL {
		t.Errorf("CCC SL = %.2f, want %.2f", p.SLPrice, wantSL)
	}
	if p.PeakPrice != 50 {
		t.Errorf("CCC peak = %.2f, want 50 (entry)", p.PeakPrice)
	}

	// Scan audit: AAA carried, CCC traded.
	scan, _ := st.ScanByDate(open[0].TradeDate)
	// (dates differ: AAA seeded with old date; use today's scan via LatestScanDate)
	d, _ := st.LatestScanDate()
	scan, _ = st.ScanByDate(d)
	outcomes := map[string]string{}
	for _, r := range scan {
		outcomes[r.Symbol] = r.Outcome
	}
	if outcomes["AAA"] != "carried" {
		t.Errorf("scan outcome AAA = %q, want carried", outcomes["AAA"])
	}
	if outcomes["CCC"] != "traded" {
		t.Errorf("scan outcome CCC = %q, want traded", outcomes["CCC"])
	}
}

func TestMonitorOnce_TrailsAndExits(t *testing.T) {
	// Position from a prior day, entry 100, SL 98 (2%). Price ran to 120:
	// SL should trail to 117.6; then a drop to 117 breaches → exit.
	eng, st := newTestEngine(t, fakeQuotes{quotes.OHLC{Low: 110, Last: 120, High: 120}})
	seedOpen(t, st, model.Position{Symbol: "TRL", Qty: 10, EntryPrice: 100, SLPrice: 98, PeakPrice: 100})

	eng.MonitorOnce(context.Background())
	open, _ := st.OpenPositions()
	if len(open) != 1 {
		t.Fatalf("still open after rally, got %d", len(open))
	}
	if diff := open[0].SLPrice - 117.6; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("trailed SL = %.4f, want 117.60", open[0].SLPrice)
	}
	if open[0].PeakPrice != 120 {
		t.Errorf("peak = %.2f, want 120", open[0].PeakPrice)
	}
	ev, _ := st.SLEvents(5)
	if len(ev) != 1 || ev[0].NewSL < 117.59 {
		t.Errorf("expected one sl_event with new_sl 117.6, got %+v", ev)
	}

	// Price falls to 117 (≤ 117.6) → monitor must exit at the observed price.
	eng.Quotes = fakeQuotes{quotes.OHLC{Low: 116, Last: 117, High: 121}}
	eng.MonitorOnce(context.Background())
	open, _ = st.OpenPositions()
	if len(open) != 0 {
		t.Fatalf("position should be closed on breach")
	}
	closed, _ := st.ClosedPositions(5)
	if len(closed) != 1 || closed[0].ExitReason != model.ExitStopLoss {
		t.Fatalf("expected stoploss close, got %+v", closed)
	}
	if closed[0].ExitPrice != 117 {
		t.Errorf("exit at %.2f, want 117 (observed price)", closed[0].ExitPrice)
	}
}

func TestMonitorOnce_ProfitFloorLocks(t *testing.T) {
	// Entry 200 × 75. Price rises ~2.5% (net > activate 2%) → the stop must be
	// raised to the +1%-net floor price, ABOVE the plain 2% trail, and a later
	// dip to just under the floor must exit with net ≈ +1%.
	entry, qty := 200.0, 75
	activate := model.SellPriceForNetPct(entry, qty, 2.0) // ≈ 204.66
	floor := model.SellPriceForNetPct(entry, qty, 1.0)    // ≈ 202.65

	eng, st := newTestEngine(t, fakeQuotes{quotes.OHLC{Low: 199, Last: activate + 0.5, High: activate + 0.5}})
	seedOpen(t, st, model.Position{Symbol: "PBK", Qty: qty, EntryPrice: entry,
		SLPrice: entry * 0.98, PeakPrice: entry})

	eng.MonitorOnce(context.Background())
	open, _ := st.OpenPositions()
	if len(open) != 1 {
		t.Fatalf("should still be open, got %d", len(open))
	}
	plainTrail := (activate + 0.5) * 0.98
	if open[0].SLPrice < floor-0.01 {
		t.Errorf("SL %.4f below profit floor %.4f", open[0].SLPrice, floor)
	}
	if open[0].SLPrice < plainTrail && open[0].SLPrice < floor {
		t.Errorf("SL %.4f raised by neither trail nor floor", open[0].SLPrice)
	}

	// Dip below the floor → exit; realised NET must be ≈ +1% (not a loss).
	eng.Quotes = fakeQuotes{quotes.OHLC{Low: floor - 1, Last: floor - 0.05, High: activate + 1}}
	eng.MonitorOnce(context.Background())
	closed, _ := st.ClosedPositions(5)
	if len(closed) != 1 {
		t.Fatalf("expected profit-floor exit, got %d closed", len(closed))
	}
	netPct := closed[0].NetPct()
	if netPct < 0.85 || netPct > 1.1 {
		t.Errorf("locked net %.3f%%, want ≈ +1%%", netPct)
	}
}

var _ = broker.NewPaperBroker // silence unused-import edge in some build modes
