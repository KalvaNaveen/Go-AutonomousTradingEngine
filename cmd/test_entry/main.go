// P2 test: run a full paper-mode 3:20 entry against the live pur-ema10-20 list.
// Prints the report to stdout and dumps the persisted open positions.
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
	"bnf_go_engine/scanner"
	"bnf_go_engine/store"
)

func main() {
	dbPath := "test_btst.db"
	_ = os.Remove(dbPath) // fresh run each time

	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	e := &engine.Engine{
		Scraper: scanner.NewScraper(config.BTSTScreener),
		Broker:  broker.NewPaperBroker(),
		Store:   st,
		Notify:  func(msg string) { fmt.Println("\n--- TELEGRAM ---\n" + msg) },
	}

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := e.RunEntry(ctx); err != nil {
		log.Fatalf("entry: %v", err)
	}

	open, _ := st.OpenPositions()
	fmt.Printf("\n--- PERSISTED OPEN POSITIONS: %d ---\n", len(open))
	var total float64
	for _, p := range open {
		fmt.Printf("%-12s qty=%-4d entry=%.2f SL=%.2f invested=%.0f\n",
			p.Symbol, p.Qty, p.EntryPrice, p.SLPrice, p.Invested())
		total += p.Invested()
	}
	fmt.Printf("Total deployed: ₹%.0f (cap/day ₹%.0f)\n", total, config.BTSTCapitalPerDay)
}
