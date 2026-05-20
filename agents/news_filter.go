package agents

// ══════════════════════════════════════════════════════════════
//  NewsFilter — Pre-GTT BSE Announcement Check
// ══════════════════════════════════════════════════════════════
//  Fetches the last 10 corporate announcements from BSE for a
//  given NSE symbol, classifies as POSITIVE / NEUTRAL / NEGATIVE,
//  and blocks GTT entry on NEGATIVE news.
//
//  Fail-open policy: any network/parse error returns NEUTRAL so
//  infrastructure failures never silently block trading.
//  Cache TTL: 30 minutes per symbol.

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// NewsClassification is the result of a news check.
type NewsClassification string

const (
	NewsPositive NewsClassification = "POSITIVE"
	NewsNeutral  NewsClassification = "NEUTRAL"
	NewsNegative NewsClassification = "NEGATIVE"
)

const newsCacheTTL = 30 * time.Minute

// ── BSE API URLs ──────────────────────────────────────────────
// Step 1: resolve NSE symbol → BSE scrip code
const bseScripSearchURL = "https://api.bseindia.com/BseIndiaAPI/api/ScrripSearch/w?strSearch=%s"

// Step 2: fetch latest announcements for the scrip code
const bseAnnURL = "https://api.bseindia.com/BseIndiaAPI/api/AnnSubCategoryGetData/w?pageno=1&category=-1&subcategory=-1&scripcode=%s&strdate=&enddate=&annexure=0"

// ── Keyword lists for classification ─────────────────────────

var negativeKeywords = []string{
	"fraud", "scam", "insolvency", "nclt", "npa", "default", "defaulter",
	"money laundering", "audit qualified", "sebi action", "suspension", "delisting",
	"criminal", "cbi", "enforcement directorate", "income tax", "seizure",
	"fir", "whistleblower", "plant shutdown", "penalty", "show cause",
	"winding up", "liquidation", "esma", "debarment", "embezzlement",
	"corporate fraud", "promoter fraud", "loss widened",
}

var positiveKeywords = []string{
	"record profit", "profit beat", "buyback", "bonus share", "dividend",
	"acquisition", "order win", "new contract", "capacity expansion",
	"qip", "rights issue", "strategic acquisition", "merger approved",
	"debt free", "rating upgrade", "ipo", "listing",
}

// ── Cache ─────────────────────────────────────────────────────

type newsCacheEntry struct {
	result   NewsClassification
	scraCode string
	at       time.Time
}

var (
	newsCacheMu    sync.Mutex
	newsCache      = make(map[string]*newsCacheEntry) // symbol → entry
	bseScripCodeMu sync.Mutex
	bseScripCodes  = make(map[string]string) // symbol → BSE scrip code (persists for session)
)

// sharedHTTPClient with a short timeout so news checks don't stall the engine.
var newsHTTPClient = &http.Client{Timeout: 8 * time.Second}

// CheckNewsBeforeEntry is the entry point called from Execute() before placing GTT.
// Returns true if the trade can proceed (POSITIVE or NEUTRAL), false if NEGATIVE.
func CheckNewsBeforeEntry(symbol string) bool {
	classification := classifyNews(symbol)
	if classification == NewsNegative {
		log.Printf("[NewsFilter] ❌ BLOCKED %s — NEGATIVE news detected", symbol)
		SendTelegram(fmt.Sprintf("🚫 *NEWS BLOCK*: `%s` — NEGATIVE announcement detected. GTT not placed.", symbol))
		return false
	}
	if classification == NewsPositive {
		log.Printf("[NewsFilter] ✅ %s — POSITIVE news (catalysed setup)", symbol)
	}
	return true
}

// classifyNews fetches and classifies announcements, using the 30-min cache.
func classifyNews(symbol string) NewsClassification {
	newsCacheMu.Lock()
	if e, ok := newsCache[symbol]; ok && time.Since(e.at) < newsCacheTTL {
		newsCacheMu.Unlock()
		return e.result
	}
	newsCacheMu.Unlock()

	result := fetchAndClassify(symbol)

	newsCacheMu.Lock()
	newsCache[symbol] = &newsCacheEntry{result: result, at: time.Now()}
	newsCacheMu.Unlock()

	return result
}

// fetchAndClassify performs the actual BSE API calls and keyword classification.
func fetchAndClassify(symbol string) NewsClassification {
	scripCode, err := getBSEScripCode(symbol)
	if err != nil || scripCode == "" {
		log.Printf("[NewsFilter] %s: BSE scrip code lookup failed (%v) — NEUTRAL", symbol, err)
		return NewsNeutral
	}

	subjects, err := fetchBSEAnnouncements(scripCode)
	if err != nil {
		log.Printf("[NewsFilter] %s: announcement fetch failed (%v) — NEUTRAL", symbol, err)
		return NewsNeutral
	}

	return classifySubjects(subjects)
}

// getBSEScripCode resolves NSE symbol → BSE 6-digit scrip code.
// Results are session-cached since scrip codes don't change.
func getBSEScripCode(symbol string) (string, error) {
	bseScripCodeMu.Lock()
	if code, ok := bseScripCodes[symbol]; ok {
		bseScripCodeMu.Unlock()
		return code, nil
	}
	bseScripCodeMu.Unlock()

	url := fmt.Sprintf(bseScripSearchURL, symbol)
	body, err := bseFetch(url)
	if err != nil {
		return "", err
	}

	// Response: {"Table":[{"scripCode":"500325","SECURITY_NAME":"RELIANCE INDUSTRIES LTD",...}],...}
	var resp struct {
		Table []struct {
			ScripCode string `json:"scripCode"`
		} `json:"Table"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || len(resp.Table) == 0 {
		return "", fmt.Errorf("no scrip code found for %s", symbol)
	}

	code := resp.Table[0].ScripCode
	bseScripCodeMu.Lock()
	bseScripCodes[symbol] = code
	bseScripCodeMu.Unlock()
	return code, nil
}

// fetchBSEAnnouncements fetches the latest announcement subjects for a BSE scrip code.
func fetchBSEAnnouncements(scripCode string) ([]string, error) {
	url := fmt.Sprintf(bseAnnURL, scripCode)
	body, err := bseFetch(url)
	if err != nil {
		return nil, err
	}

	// Response: {"Table":[{"HEADLINE":"...", "ATTACHMENTNAME":"...", "NEWSSUB":"Subject text",...}],...}
	var resp struct {
		Table []struct {
			Headline string `json:"HEADLINE"`
			NewsSub  string `json:"NEWSSUB"`
		} `json:"Table"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parse error: %w", err)
	}

	subjects := make([]string, 0, len(resp.Table))
	for _, row := range resp.Table {
		if row.Headline != "" {
			subjects = append(subjects, row.Headline)
		} else if row.NewsSub != "" {
			subjects = append(subjects, row.NewsSub)
		}
	}
	return subjects, nil
}

// bseFetch performs a GET request with browser-like headers to avoid BSE bot blocks.
func bseFetch(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("Referer", "https://www.bseindia.com/")
	req.Header.Set("Origin", "https://www.bseindia.com")

	resp, err := newsHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d from BSE", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

// classifySubjects applies keyword matching to announcement subjects.
// Returns NEGATIVE if ANY negative keyword found.
// Returns POSITIVE if any positive keyword found (and no negatives).
// Returns NEUTRAL otherwise.
func classifySubjects(subjects []string) NewsClassification {
	allText := strings.ToLower(strings.Join(subjects, " "))

	for _, kw := range negativeKeywords {
		if strings.Contains(allText, kw) {
			log.Printf("[NewsFilter] Negative keyword matched: %q", kw)
			return NewsNegative
		}
	}

	for _, kw := range positiveKeywords {
		if strings.Contains(allText, kw) {
			return NewsPositive
		}
	}

	return NewsNeutral
}

// InvalidateNewsCache clears the cache for a symbol (call after a manual override).
func InvalidateNewsCache(symbol string) {
	newsCacheMu.Lock()
	delete(newsCache, symbol)
	newsCacheMu.Unlock()
}

// PreloadNewsForUniverse pre-fetches news for all symbols in the universe.
// Run at 15:45 (EOD) and 09:00 (pre-market) to warm the cache before market open.
func PreloadNewsForUniverse(universe map[uint32]string) {
	log.Printf("[NewsFilter] Pre-loading news for %d symbols...", len(universe))
	done := 0
	for _, sym := range universe {
		classifyNews(sym)
		done++
		if done%50 == 0 {
			log.Printf("[NewsFilter] %d/%d symbols checked", done, len(universe))
			time.Sleep(200 * time.Millisecond) // gentle rate limiting
		}
	}
	log.Printf("[NewsFilter] Pre-load complete: %d symbols", done)
}
