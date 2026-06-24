// Smoke test for the ChartInk scraper: prints the live pur-ema10-20 list.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"bnf_go_engine/scanner"
)

func main() {
	s := scanner.NewScraper("pur-ema10-20")
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	stocks, err := s.Fetch(ctx, 20)
	if err != nil {
		log.Fatalf("fetch failed: %v", err)
	}
	fmt.Printf("pur-ema10-20 returned %d stocks (showing up to 20):\n", len(stocks))
	for i, st := range stocks {
		fmt.Printf("%2d. %-14s close=%-10.2f chg=%+.2f%% vol=%d\n",
			i+1, st.Symbol, st.Close, st.PerChange, st.Volume)
	}
}
