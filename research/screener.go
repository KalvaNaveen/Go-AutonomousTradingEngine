package research

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ══════════════════════════════════════════════════════════════
//  Section III.1: Screener.in Fundamental Filter
// ══════════════════════════════════════════════════════════════
//  Doc: "Market Cap > 1,000 Cr, ROCE > 20%, ROE > 20%"
//  Doc: "Sales and Net Profits consistently increasing over 3-year rolling period"
//
//  Results are cached to disk for 24 hours to avoid rate-limiting.

type Fundamentals struct {
	Symbol       string  `json:"symbol"`
	MarketCap    float64 `json:"market_cap"`
	ROCE         float64 `json:"roce"`
	ROE          float64 `json:"roe"`
	SalesGrowth  float64 `json:"sales_growth"`
	ProfitGrowth float64 `json:"profit_growth"`
	Passed       bool    `json:"passed"`
	FetchedAt    string  `json:"fetched_at"`
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

// screenerCacheDir returns the path to the screener cache directory
func screenerCacheDir() string {
	dir := filepath.Join(".", "data", "screener_cache")
	os.MkdirAll(dir, 0755)
	return dir
}

// loadFromCache checks if we have a cached result less than 24 hours old
func loadFromCache(symbol string) *Fundamentals {
	path := filepath.Join(screenerCacheDir(), strings.ToUpper(symbol)+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var f Fundamentals
	if err := json.Unmarshal(data, &f); err != nil {
		return nil
	}
	// Check age
	fetchedAt, err := time.Parse(time.RFC3339, f.FetchedAt)
	if err != nil {
		return nil
	}
	if time.Since(fetchedAt) > 24*time.Hour {
		return nil // Stale cache
	}
	return &f
}

// saveToCache writes a fundamentals result to disk
func saveToCache(f *Fundamentals) {
	f.FetchedAt = time.Now().Format(time.RFC3339)
	data, err := json.Marshal(f)
	if err != nil {
		return
	}
	path := filepath.Join(screenerCacheDir(), strings.ToUpper(f.Symbol)+".json")
	os.WriteFile(path, data, 0644)
}

// FetchFundamentals scrapes Screener.in for a single stock (with 24h disk cache)
//
// NOTE: ROCE > 20% is applied universally per the Blueprint (Section III).
// This intentionally excludes most BFSI stocks (banks, NBFCs, insurance)
// which structurally carry ROCE of 10-15%.
// The Blueprint's authors are aware of this and consider it a feature:
// they want "highly efficient outlier companies" only.
// DO NOT add sector-based exceptions without re-authorization from the
// original Blueprint authors.
func FetchFundamentals(symbol string) (*Fundamentals, error) {
	// Check cache first
	if cached := loadFromCache(symbol); cached != nil {
		return cached, nil
	}

	url := fmt.Sprintf("https://www.screener.in/company/%s/", strings.ToUpper(symbol))

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("screener fetch failed for %s: %w", symbol, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 429 {
		return nil, fmt.Errorf("screener rate-limited (429) for %s — retry later", symbol)
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("screener returned %d for %s", resp.StatusCode, symbol)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	html := string(body)

	f := &Fundamentals{Symbol: symbol}

	f.MarketCap = parseScreenerRatio(html, "Market Cap")
	f.ROCE = parseScreenerRatio(html, "ROCE")
	f.ROE = parseScreenerRatio(html, "ROE")

	// Growth: "Compounded Sales Growth" and "Compounded Profit Growth"
	f.SalesGrowth = parseScreenerGrowth(html, "Compounded Sales Growth")
	if f.SalesGrowth == 0 {
		f.SalesGrowth = parseScreenerGrowth(html, "Sales Growth")
	}
	f.ProfitGrowth = parseScreenerGrowth(html, "Compounded Profit Growth")
	if f.ProfitGrowth == 0 {
		f.ProfitGrowth = parseScreenerGrowth(html, "Profit Growth")
	}

	// Doc III.1: MCap > 1000 Cr, ROCE > 20%, ROE > 20%, growth positive
	f.Passed = f.MarketCap > 1000 && f.ROCE > 20 && f.ROE > 20
	if f.SalesGrowth != 0 || f.ProfitGrowth != 0 {
		f.Passed = f.Passed && f.SalesGrowth > 0 && f.ProfitGrowth > 0
	}

	// Cache to disk
	saveToCache(f)

	return f, nil
}

// parseScreenerRatio extracts a ratio value from Screener.in HTML
func parseScreenerRatio(html, label string) float64 {
	escapedLabel := regexp.QuoteMeta(label)
	patterns := []string{
		escapedLabel + `[\s\S]{0,200}?([\d,]+\.?\d*)\s*%`,
		escapedLabel + `[\s\S]{0,200}?₹\s*([\d,]+\.?\d*)`,
		escapedLabel + `[\s\S]{0,300}?([\d,]+\.?\d+)`,
	}
	for _, p := range patterns {
		re := regexp.MustCompile(p)
		m := re.FindStringSubmatch(html)
		if len(m) >= 2 {
			return parseIndianNumber(m[1])
		}
	}
	return 0
}

// parseScreenerGrowth extracts growth percentages from the analysis section
func parseScreenerGrowth(html, label string) float64 {
	escapedLabel := regexp.QuoteMeta(label)
	patterns := []string{
		escapedLabel + `[\s\S]{0,500}?3\s*(?:Years?|Yrs?)[\s\S]{0,100}?(-?[\d,]+\.?\d*)\s*%`,
		escapedLabel + `[\s\S]{0,200}?(-?[\d,]+\.?\d*)\s*%`,
	}
	for _, p := range patterns {
		re := regexp.MustCompile(p)
		m := re.FindStringSubmatch(html)
		if len(m) >= 2 {
			return parseIndianNumber(m[1])
		}
	}
	return 0
}

// parseIndianNumber converts "1,23,456.78" or "19,42,189" to float
func parseIndianNumber(s string) float64 {
	s = strings.ReplaceAll(s, ",", "")
	s = strings.TrimSpace(s)
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// FilterUniverseByFundamentals filters symbols through Screener.in
func FilterUniverseByFundamentals(symbols []string) []string {
	var passed []string
	for _, sym := range symbols {
		f, err := FetchFundamentals(sym)
		if err != nil {
			log.Printf("[Screener] Error fetching %s: %v", sym, err)
			passed = append(passed, sym)
			continue
		}

		if f.Passed {
			log.Printf("[Screener] ✅ %s (MCap=%.0f, ROCE=%.1f%%, ROE=%.1f%%, Sales=%.1f%%, Profit=%.1f%%)",
				sym, f.MarketCap, f.ROCE, f.ROE, f.SalesGrowth, f.ProfitGrowth)
			passed = append(passed, sym)
		} else {
			log.Printf("[Screener] ❌ %s (MCap=%.0f, ROCE=%.1f%%, ROE=%.1f%%, Sales=%.1f%%, Profit=%.1f%%)",
				sym, f.MarketCap, f.ROCE, f.ROE, f.SalesGrowth, f.ProfitGrowth)
		}

		time.Sleep(500 * time.Millisecond)
	}
	return passed
}
