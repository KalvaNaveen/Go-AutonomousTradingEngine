package agents

// ══════════════════════════════════════════════════════════════
//  EarningsFilter — NSE Corporate Actions Calendar
// ══════════════════════════════════════════════════════════════
//  Book Ch.7 p.191: "Avoid holding through earnings unless you
//  have a significant profit cushion to mitigate potential volatility."
//  Extended rule: block NEW entries within ResultsAvoidanceDays
//  of the board meeting / quarterly results announcement date.
//
//  Data source: NSE Corporate Actions API (board-meeting category).
//  Queries the next 60 days of board meetings, filters for
//  "Quarterly Results" / "Financial Results" purpose entries.
//
//  Fail-open policy: if NSE is unreachable the cache stays empty
//  and NO entries are blocked. Infrastructure failures never
//  silently prevent trading.
//
//  Cache TTL: 6 hours — refreshed at engine startup and mid-day.
//  Thread-safe: all reads/writes protected by earningsState.mu.

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"bnf_go_engine/config"
)

// ResultsAvoidanceDays: block new entries this many calendar days
// before a board meeting / quarterly results date.
// Book Ch.7 p.191 — no explicit number given; 7 days is the
// standard industry practice for Indian equity swing traders (ENGINE ASSUMPTION).
const ResultsAvoidanceDays = 7

const earningsCacheTTL = 6 * time.Hour

// NSE corporate actions endpoint — board-meeting category.
// date format for URL query params: "24-05-2026" (dd-MM-yyyy).
const nseCorpActionsURL = "https://www.nseindia.com/api/corporateActions?type=equities&category=board-meeting&fromDate=%s&toDate=%s"

// earningsState holds the global NSE results calendar cache.
var earningsState = struct {
	mu          sync.RWMutex
	nextResults map[string]time.Time // UPPER(symbol) → nearest upcoming results date
	lastFetch   time.Time
	attempted   bool // true after first attempt (even if it failed)
}{
	nextResults: make(map[string]time.Time),
}

// RefreshEarningsCalendar fetches NSE board-meeting calendar for the next 60 days
// and caches results dates per symbol.
// Fail-open: on any error the existing cache is left unchanged and no entries blocked.
// Call at engine startup and every earningsCacheTTL (6h).
func RefreshEarningsCalendar() {
	earningsState.mu.Lock()
	defer earningsState.mu.Unlock()

	// Skip if cache is still fresh
	if earningsState.attempted && time.Since(earningsState.lastFetch) < earningsCacheTTL {
		return
	}
	earningsState.attempted = true
	earningsState.lastFetch = time.Now()

	now := config.NowIST()
	// URL date format: "24-05-2026" (dd-MM-yyyy)
	from := now.Format("02-01-2006")
	to := now.AddDate(0, 0, 60).Format("02-01-2006")
	url := fmt.Sprintf(nseCorpActionsURL, from, to)

	events, err := fetchNSECorpActions(url)
	if err != nil {
		log.Printf("[EarningsFilter] NSE fetch failed: %v — no entries blocked (fail-open)", err)
		return
	}

	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, config.IST)
	newMap := make(map[string]time.Time)
	for _, ev := range events {
		if ev.date.IsZero() || ev.date.Before(today) {
			continue
		}
		p := strings.ToLower(ev.purpose)
		// Match board meetings specifically about quarterly / financial results
		if strings.Contains(p, "result") || strings.Contains(p, "financial") {
			sym := strings.ToUpper(strings.TrimSpace(ev.symbol))
			if existing, ok := newMap[sym]; !ok || ev.date.Before(existing) {
				newMap[sym] = ev.date // keep the nearest upcoming date
			}
		}
	}

	earningsState.nextResults = newMap
	log.Printf("[EarningsFilter] Calendar refreshed: %d symbols with upcoming results in next 60 days",
		len(newMap))
}

// HasUpcomingResults returns true if the symbol has quarterly results due within
// ResultsAvoidanceDays. Returns false (no block) when data is unavailable.
// Triggers a background refresh if the cache is stale — does not block execution.
func HasUpcomingResults(symbol string) bool {
	// Trigger background refresh when stale (non-blocking — uses current cache)
	earningsState.mu.RLock()
	stale := !earningsState.attempted || time.Since(earningsState.lastFetch) >= earningsCacheTTL
	earningsState.mu.RUnlock()
	if stale {
		go RefreshEarningsCalendar()
	}

	earningsState.mu.RLock()
	defer earningsState.mu.RUnlock()

	resultsDate, ok := earningsState.nextResults[strings.ToUpper(symbol)]
	if !ok {
		return false // No data → fail-open (don't block)
	}
	now := config.NowIST()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, config.IST)
	daysAway := int(resultsDate.Sub(today).Hours() / 24)
	return daysAway >= 0 && daysAway <= ResultsAvoidanceDays
}

// DaysToResults returns calendar days until the stock's next results date.
// Returns -1 when no data is available.
func DaysToResults(symbol string) int {
	earningsState.mu.RLock()
	defer earningsState.mu.RUnlock()

	resultsDate, ok := earningsState.nextResults[strings.ToUpper(symbol)]
	if !ok {
		return -1
	}
	now := config.NowIST()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, config.IST)
	return int(resultsDate.Sub(today).Hours() / 24)
}

// ── NSE API fetch ─────────────────────────────────────────────

type corpEvent struct {
	symbol  string
	date    time.Time
	purpose string
}

func fetchNSECorpActions(url string) ([]corpEvent, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	// NSE requires browser-like headers to serve JSON (anti-scraping)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("Referer", "https://www.nseindia.com/companies-listing/corporate-filings-event-calendar")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("NSE request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("NSE returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// NSE board-meeting response: array of corporate action objects.
	// Relevant fields: symbol, exDate (the board meeting date), purpose.
	var raw []struct {
		Symbol  string `json:"symbol"`
		ExDate  string `json:"exDate"` // "24-May-2026" or "24-05-2026"
		Purpose string `json:"purpose"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		preview := string(body)
		if len(preview) > 80 {
			preview = preview[:80]
		}
		return nil, fmt.Errorf("JSON parse: %w (body prefix: %q)", err, preview)
	}

	var events []corpEvent
	for _, r := range raw {
		sym := strings.TrimSpace(r.Symbol)
		if sym == "" {
			continue
		}
		// Parse date: NSE uses "24-May-2026" (dd-MMM-yyyy in Go: "02-Jan-2006")
		t, parseErr := time.ParseInLocation("02-Jan-2006", r.ExDate, config.IST)
		if parseErr != nil {
			// Fallback: "24-05-2026" (dd-MM-yyyy)
			t, parseErr = time.ParseInLocation("02-01-2006", r.ExDate, config.IST)
			if parseErr != nil {
				continue // Unrecognised date format — skip this event
			}
		}
		events = append(events, corpEvent{
			symbol:  sym,
			date:    t,
			purpose: r.Purpose,
		})
	}
	return events, nil
}
