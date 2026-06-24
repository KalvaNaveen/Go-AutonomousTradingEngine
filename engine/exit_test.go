package engine

import (
	"context"
	"path/filepath"
	"testing"

	"bnf_go_engine/broker"
	"bnf_go_engine/config"
	"bnf_go_engine/model"
	"bnf_go_engine/quotes"
	"bnf_go_engine/store"
)

// fakeQuotes returns a fixed OHLC for every symbol — lets the exit logic be
// tested deterministically without hitting Yahoo.
type fakeQuotes struct{ ohlc quotes.OHLC }

func (f fakeQuotes) Daily(_ context.Context, _ string) (quotes.OHLC, error) {
	return f.ohlc, nil
}

// newTestEngine wires an engine over a fresh temp DB with the given quote source.
func newTestEngine(t *testing.T, q quoteSource) (*Engine, *store.Store) {
	t.Helper()
	db := filepath.Join(t.TempDir(), "exit_test.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return &Engine{Broker: broker.NewPaperBroker(), Store: st, Quotes: q}, st
}

// seedOpen inserts a prior-day open position (so RunExit treats it as eligible).
func seedOpen(t *testing.T, st *store.Store, p model.Position) model.Position {
	t.Helper()
	// trade date strictly before today so the BTST T+1 guard lets it exit.
	p.TradeDate = "2000-01-01"
	p.Status = model.StatusOpen
	if err := st.SaveOpen(&p); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return p
}

func TestRunExit_StopLossBreached(t *testing.T) {
	// Entry 100, SL 93.5 (6.5%). Next-day low 90 → SL was breached.
	eng, st := newTestEngine(t, fakeQuotes{quotes.OHLC{Low: 90, Last: 95}})
	seedOpen(t, st, model.Position{Symbol: "TEST", Qty: 10, EntryPrice: 100, SLPrice: 93.5})

	if err := eng.RunExit(context.Background(), false); err != nil {
		t.Fatalf("RunExit: %v", err)
	}

	closed, _ := st.ClosedPositions(10)
	if len(closed) != 1 {
		t.Fatalf("expected 1 closed position, got %d", len(closed))
	}
	got := closed[0]
	if got.ExitReason != model.ExitStopLoss {
		t.Errorf("exit reason = %q, want %q", got.ExitReason, model.ExitStopLoss)
	}
	// Exit must be booked at the SL trigger, not the day's last price.
	if got.ExitPrice != 93.5 {
		t.Errorf("exit price = %.2f, want 93.50 (SL)", got.ExitPrice)
	}
	wantPnL := (93.5 - 100) * 10
	if got.PnL != wantPnL {
		t.Errorf("pnl = %.2f, want %.2f", got.PnL, wantPnL)
	}
}

func TestRunExit_SquareOffWhenSLNotHit(t *testing.T) {
	// Entry 100, SL 93.5, next-day low 96 (above SL) → square off at Last 104.
	eng, st := newTestEngine(t, fakeQuotes{quotes.OHLC{Low: 96, Last: 104}})
	seedOpen(t, st, model.Position{Symbol: "TEST", Qty: 10, EntryPrice: 100, SLPrice: 93.5})

	if err := eng.RunExit(context.Background(), false); err != nil {
		t.Fatalf("RunExit: %v", err)
	}

	closed, _ := st.ClosedPositions(10)
	if len(closed) != 1 {
		t.Fatalf("expected 1 closed position, got %d", len(closed))
	}
	got := closed[0]
	if got.ExitReason != model.ExitSquareOff {
		t.Errorf("exit reason = %q, want %q", got.ExitReason, model.ExitSquareOff)
	}
	if got.ExitPrice != 104 {
		t.Errorf("exit price = %.2f, want 104.00 (last)", got.ExitPrice)
	}
	if want := (104.0 - 100.0) * 10; got.PnL != want {
		t.Errorf("pnl = %.2f, want %.2f", got.PnL, want)
	}
}

func TestRunExit_HoldsSameDayPosition(t *testing.T) {
	// A position entered TODAY must not be squared off (BTST holds to next day).
	eng, st := newTestEngine(t, fakeQuotes{quotes.OHLC{Low: 50, Last: 60}})
	today := config.NowIST().Format("2006-01-02")
	p := model.Position{Symbol: "TEST", Qty: 10, EntryPrice: 100, SLPrice: 93.5,
		TradeDate: today, Status: model.StatusOpen}
	if err := st.SaveOpen(&p); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := eng.RunExit(context.Background(), false); err != nil {
		t.Fatalf("RunExit: %v", err)
	}
	if closed, _ := st.ClosedPositions(10); len(closed) != 0 {
		t.Fatalf("today's position should not be closed, got %d closed", len(closed))
	}
}
