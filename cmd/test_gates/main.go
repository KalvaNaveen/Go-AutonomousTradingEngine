// P4 test: exercise the Tier-1 macro gate and Tier-2 news filter against live
// data, then run a full gated paper entry.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"bnf_go_engine/broker"
	"bnf_go_engine/config"
	"bnf_go_engine/engine"
	"bnf_go_engine/gate"
	"bnf_go_engine/quotes"
	"bnf_go_engine/scanner"
	"bnf_go_engine/store"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	q := quotes.New()

	// ── Tier-1 macro gate ──────────────────────────────────────────────
	macro := gate.NewMacro(q)
	ok, reason := macro.Check(ctx)
	fmt.Printf("[Tier-1 Macro] tradeToday=%v — %s\n", ok, reason)

	// ── Tier-2 news filter (small live sample) ─────────────────────────
	news := gate.NewNews()
	sample := map[string]string{
		"RELIANCE": "Reliance Industries",
		"NHPC":     "NHPC Limited",
		"DRREDDY":  "Dr Reddys Laboratories",
	}
	dropped := news.Filter(ctx, sample)
	fmt.Printf("[Tier-2 News] checked %d, dropped %d: %v\n", len(sample), len(dropped), dropped)

	// ── Full gated paper entry ─────────────────────────────────────────
	dbPath := "test_btst_gated.db"
	_ = os.Remove(dbPath)
	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	e := &engine.Engine{
		Scanner:    scanner.NewMulti(config.BTSTScreeners),
		Broker:     broker.NewPaperBroker(),
		Store:      st,
		Notify:     func(msg string) { fmt.Println("\n--- REPORT ---\n" + msg) },
		Quotes:     q,
		MacroGate:  macro.Check,
		NewsFilter: news.Filter,
	}
	if err := e.RunCycle(ctx); err != nil {
		log.Fatalf("gated entry: %v", err)
	}
}
