// P5: run the BTST dashboard. Seeds a demo DB (open + closed trades) on first
// run so the UI can be exercised, then serves on :8085.
//
//	go run ./cmd/dashboard          # seeds demo.db and serves
//	go run ./cmd/dashboard live.db  # serve an existing DB as-is (no seeding)
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
	"bnf_go_engine/model"
	"bnf_go_engine/scanner"
	"bnf_go_engine/store"
	"bnf_go_engine/web"
)

func main() {
	seed := len(os.Args) < 2
	dbPath := "demo.db"
	if !seed {
		dbPath = os.Args[1]
	}
	if seed {
		_ = os.Remove(dbPath)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	if seed {
		if err := seedDemo(st); err != nil {
			log.Fatalf("seed: %v", err)
		}
	}

	srv := web.New(st, config.PaperMode)
	addr := ":8085"
	log.Printf("BTST dashboard on http://127.0.0.1%s", addr)
	log.Fatal(srv.ListenAndServe(addr))
}

// seedDemo runs a real paper entry, then closes a few positions with synthetic
// exits (a win, a loss, and a stop-loss) so the dashboard shows all states.
func seedDemo(st *store.Store) error {
	e := &engine.Engine{
		Scraper: scanner.NewScraper(config.BTSTScreener),
		Broker:  broker.NewPaperBroker(),
		Store:   st,
		Notify:  func(string) {},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := e.RunEntry(ctx); err != nil {
		return err
	}

	open, err := st.OpenPositions()
	if err != nil {
		return err
	}
	// Close up to 6 with varied synthetic outcomes; leave the rest open.
	moves := []struct {
		pct    float64
		reason string
	}{
		{+2.4, model.ExitSquareOff}, {-1.1, model.ExitSquareOff},
		{+0.8, model.ExitSquareOff}, {-6.5, model.ExitStopLoss},
		{+3.1, model.ExitSquareOff}, {-0.4, model.ExitSquareOff},
	}
	for i, m := range moves {
		if i >= len(open) {
			break
		}
		p := open[i]
		p.ExitPrice = p.EntryPrice * (1 + m.pct/100)
		if m.reason == model.ExitStopLoss {
			p.ExitPrice = p.SLPrice
		}
		p.ExitReason = m.reason
		p.ExitTime = config.NowIST()
		p.PnL = (p.ExitPrice - p.EntryPrice) * float64(p.Qty)
		p.Status = model.StatusClosed
		if err := st.ClosePosition(&p); err != nil {
			return err
		}
	}
	fmt.Println("seeded demo.db: ~6 closed, rest open")
	return nil
}
