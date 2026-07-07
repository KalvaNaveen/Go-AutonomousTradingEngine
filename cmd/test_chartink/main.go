// Smoke test for the ChartInk multi-scanner: prints the live distinct union of
// the configured screeners (default ema-reversal-93 + pvema-3) with sources.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"bnf_go_engine/config"
	"bnf_go_engine/scanner"
)

func main() {
	m := scanner.NewMulti(config.BTSTScreeners)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	stocks, err := m.Fetch(ctx, config.BTSTMaxStocks)
	if err != nil {
		log.Fatalf("fetch failed: %v", err)
	}
	fmt.Printf("union of %v returned %d distinct stocks:\n", config.BTSTScreeners, len(stocks))
	for i, st := range stocks {
		fmt.Printf("%2d. %-14s close=%-10.2f chg=%+.2f%%  src=%s\n",
			i+1, st.Symbol, st.Close, st.PerChange, st.Source)
	}
}
