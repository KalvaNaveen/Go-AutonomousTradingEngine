package scanner

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// Multi fetches several saved screeners and returns the distinct union of their
// results: top maxPerScreener stocks from EACH screener, de-duplicated by symbol
// (first screener's row wins; Source becomes "a+b" for overlaps).
//
// One screener failing is tolerated (logged, the rest proceed); only when EVERY
// screener fails does Fetch return an error — a single flaky scan page must not
// cost the trading day.
type Multi struct {
	scrapers []*Scraper
	slugs    []string
}

// NewMulti builds a multi-screener fetcher from saved-screener slugs.
func NewMulti(slugs []string) *Multi {
	m := &Multi{slugs: slugs}
	for _, s := range slugs {
		m.scrapers = append(m.scrapers, NewScraper(s))
	}
	return m
}

// Fetch returns the distinct union (order preserved: screener 1's list first).
// maxPerScreener applies per screener, so the union can be up to N× that size.
func (m *Multi) Fetch(ctx context.Context, maxPerScreener int) ([]Stock, error) {
	var union []Stock
	index := map[string]int{} // symbol → position in union
	failures := 0
	var lastErr error

	for i, s := range m.scrapers {
		stocks, err := s.Fetch(ctx, maxPerScreener)
		if err != nil {
			failures++
			lastErr = err
			log.Printf("[Scan] screener %s failed: %v", m.slugs[i], err)
			continue
		}
		for _, st := range stocks {
			if j, dup := index[st.Symbol]; dup {
				// Seen in an earlier screener — record the extra source only.
				if !strings.Contains(union[j].Source, st.Source) {
					union[j].Source += "+" + st.Source
				}
				continue
			}
			index[st.Symbol] = len(union)
			union = append(union, st)
		}
	}

	if failures == len(m.scrapers) {
		return nil, fmt.Errorf("all %d screeners failed: %w", failures, lastErr)
	}
	return union, nil
}
