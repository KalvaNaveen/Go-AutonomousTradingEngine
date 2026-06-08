package agents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bnf_go_engine/config"
)

// ══════════════════════════════════════════════════════════════
//  Earnings Watch — "why might this move (or reverse)?" context
// ══════════════════════════════════════════════════════════════
//
// This is NOT a strategy gate — the book ("Swing Trading Simplified") is a
// pure price/volume system and never screens on fundamentals. This agent adds
// a single, narrow, *informational* caution tag to BUY alerts: how many days
// until the company's next quarterly results.
//
// Why this slice and not a full news/catalyst scanner: earnings dates are
// calendar facts (stable, low-noise), unlike general news/management/expansion
// announcements which would require a live NSE/BSE scrape (fragile — same
// class of risk as the existing Screener.in dependency) or a paid tagged-feed
// vendor. Starting here is the highest-value, lowest-fragility slice, and it
// is directly book-aligned: p.191 explicitly warns against holding through
// earnings without a meaningful profit cushion.
//
// Data source: data/earnings_cache/<SYMBOL>.json — a small on-disk cache, the
// same pattern as loadMarketCapFromCache (screener_cache). Populate it from
// NSE's free "Corporate Filings Event Calendar"
// (nseindia.com/companies-listing/corporate-filings-event-calendar) — manually
// or via a small periodic job — with:
//   {"next_result_date": "2026-06-15"}
// A missing/stale cache file simply means no tag is shown — fails silently,
// never blocks or alters a signal.

type earningsCacheEntry struct {
	NextResultDate string `json:"next_result_date"` // "YYYY-MM-DD"
}

// EarningsCaution returns a short Telegram-ready tag like " | 📅 Earnings in 3d"
// if the symbol's next quarterly results fall within the next 7 calendar days,
// or "" if no cache entry exists, the date is invalid, or it's further out.
// Purely advisory — never affects Score, Signal, or any filter decision.
func EarningsCaution(symbol string) string {
	entry, ok := loadEarningsCacheEntry(symbol)
	if !ok || entry.NextResultDate == "" {
		return ""
	}
	resultDate, err := time.Parse("2006-01-02", entry.NextResultDate)
	if err != nil {
		return ""
	}
	days := int(resultDate.Sub(config.TodayIST()).Hours() / 24)
	switch {
	case days < 0:
		return "" // stale entry — already reported
	case days == 0:
		return " | 📅 *Results today* — book p.191: mind the gap risk"
	case days == 1:
		return " | 📅 Results in 1d — book p.191: avoid fresh entries pre-earnings"
	case days <= 7:
		return fmt.Sprintf(" | 📅 Results in %dd — book p.191: avoid fresh entries pre-earnings", days)
	default:
		return ""
	}
}

func loadEarningsCacheEntry(symbol string) (earningsCacheEntry, bool) {
	cachePath := filepath.Join(".", "data", "earnings_cache", strings.ToUpper(symbol)+".json")
	raw, err := os.ReadFile(cachePath)
	if err != nil {
		return earningsCacheEntry{}, false
	}
	var entry earningsCacheEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return earningsCacheEntry{}, false
	}
	return entry, true
}
