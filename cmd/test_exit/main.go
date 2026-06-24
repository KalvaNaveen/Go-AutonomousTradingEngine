// P3 test: square off the open positions left by cmd/test_entry, pulling live
// Yahoo prices, booking P&L, and printing the exit report. Run test_entry first.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"bnf_go_engine/broker"
	"bnf_go_engine/engine"
	"bnf_go_engine/quotes"
	"bnf_go_engine/scanner"
	"bnf_go_engine/store"
)

func main() {
	st, err := store.Open("test_btst.db")
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	open, _ := st.OpenPositions()
	fmt.Printf("Open positions to square off: %d\n", len(open))
	if len(open) == 0 {
		log.Fatal("no open positions — run cmd/test_entry first")
	}

	e := &engine.Engine{
		Scraper: scanner.NewScraper(""),
		Broker:  broker.NewPaperBroker(),
		Store:   st,
		Notify:  func(msg string) { fmt.Println("\n--- TELEGRAM ---\n" + msg) },
		Quotes:  quotes.New(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// includeToday=true so the same-session test can close today's entries.
	if err := e.RunExit(ctx, true); err != nil {
		log.Fatalf("exit: %v", err)
	}

	closed, _ := st.ClosedPositions(50)
	fmt.Printf("\n--- CLOSED: %d ---\n", len(closed))
}
