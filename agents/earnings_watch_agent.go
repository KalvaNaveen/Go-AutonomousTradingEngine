package agents

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
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

// ── Auto-population: NSE board-meeting/event-calendar fetch ──────────────────
//
// Removes the manual data-entry step: pulls NSE's free, official board-meeting
// calendar (the same data source the README pointed users to), filters to
// "Financial Results" purpose entries, and writes one cache file per symbol
// that's actually in our universe. Runs once per trading day from the EOD
// pipeline — cheap (single HTTP call), and silently no-ops on failure (NSE
// occasionally rate-limits/bot-blocks; a missed refresh just means yesterday's
// cache persists, which is harmless since EarningsCaution() already treats
// stale/past dates as "no tag").
type nseBoardMeetingEntry struct {
	Symbol  string `json:"symbol"`
	Purpose string `json:"purpose"`
	Date    string `json:"date"` // "DD-Mon-YYYY", e.g. "11-Jun-2026"
}

// RefreshEarningsCache fetches NSE's board-meeting event calendar and writes
// data/earnings_cache/<SYMBOL>.json for every "Financial Results" entry whose
// symbol is present in the given universe. Call once daily before the EOD scan.
func RefreshEarningsCache(universe map[uint32]string) {
	symbols := make(map[string]bool, len(universe))
	for _, sym := range universe {
		symbols[strings.ToUpper(sym)] = true
	}

	entries, err := fetchNSEBoardMeetings()
	if err != nil {
		log.Printf("[EarningsWatch] refresh skipped (fetch failed): %v", err)
		return
	}

	dir := filepath.Join(".", "data", "earnings_cache")
	os.MkdirAll(dir, 0755)

	written := 0
	for _, e := range entries {
		if !strings.Contains(e.Purpose, "Financial Results") {
			continue
		}
		sym := strings.ToUpper(strings.TrimSpace(e.Symbol))
		if !symbols[sym] {
			continue
		}
		parsed, err := time.Parse("02-Jan-2006", e.Date)
		if err != nil {
			continue
		}
		entry := earningsCacheEntry{NextResultDate: parsed.Format("2006-01-02")}
		raw, _ := json.Marshal(entry)
		path := filepath.Join(dir, sym+".json")
		if err := os.WriteFile(path, raw, 0644); err == nil {
			written++
		}
	}
	log.Printf("[EarningsWatch] cache refreshed: %d symbols updated from %d board-meeting entries", written, len(entries))
}

// fetchNSEBoardMeetings hits NSE's public event-calendar API. NSE requires a
// warm session cookie from the main site before its /api/* endpoints respond
// (otherwise returns 403) — so we GET the homepage first to collect cookies,
// then reuse that client for the API call. This is the same handshake every
// NSE-scraping tool (nsepython, jugaad-data, etc.) has to perform; there is no
// authenticated/official alternative for free access.
func fetchNSEBoardMeetings() ([]nseBoardMeetingEntry, error) {
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Timeout: 20 * time.Second, Jar: jar}

	const ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36"

	warmup, _ := http.NewRequest("GET", "https://www.nseindia.com/companies-listing/corporate-filings-event-calendar", nil)
	warmup.Header.Set("User-Agent", ua)
	warmup.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if resp, err := client.Do(warmup); err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	time.Sleep(1 * time.Second)

	req, _ := http.NewRequest("GET", "https://www.nseindia.com/api/event-calendar", nil)
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Referer", "https://www.nseindia.com/companies-listing/corporate-filings-event-calendar")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("NSE API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var entries []nseBoardMeetingEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, err
	}
	return entries, nil
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
