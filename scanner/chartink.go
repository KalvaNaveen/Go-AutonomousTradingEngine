// Package scanner fetches the live stock list from a saved ChartInk screener.
//
// It replaces the previous C# Playwright scraper entirely: no headless browser
// is needed. ChartInk's own /screener/process JSON endpoint returns the scan
// result directly. We GET the screener page once to obtain the CSRF token, the
// session cookies, and the saved scan_clause, then POST that clause to /process.
//
// Because the clause is read from the page at runtime, editing the screener on
// chartink.com automatically changes what the engine trades — nothing to redeploy.
package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Stock is one row returned by the screener.
type Stock struct {
	Symbol    string  `json:"nsecode"`
	Name      string  `json:"name"`
	BSECode   *string `json:"bsecode"` // nil for index pseudo-rows — filtered out
	Close     float64 `json:"close"`
	PerChange float64 `json:"per_chg"`
	Volume    int64   `json:"volume"`
}

const (
	defaultScreener = "pur-ema10-20"
	baseURL         = "https://chartink.com"
	userAgent       = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
)

var (
	csrfRe = regexp.MustCompile(`name="csrf-token"\s+content="([^"]+)"`)
	// The saved screener's clause is embedded in the page JSON under the
	// "atlas_query" key (HTML-entity encoded). The POST to /process still
	// sends it as the "scan_clause" form field.
	clauseRe = regexp.MustCompile(`(?:atlas_query|scan_clause)&quot;:&quot;(.*?)&quot;,&quot;`)
)

// Scraper holds a cookie-jar HTTP client reused across the GET + POST.
type Scraper struct {
	client   *http.Client
	screener string
}

// NewScraper builds a scraper for the given saved screener slug (e.g.
// "pur-ema10-20"). An empty slug defaults to the configured BTST screener.
func NewScraper(screener string) *Scraper {
	if screener == "" {
		screener = defaultScreener
	}
	jar, _ := cookiejar.New(nil)
	return &Scraper{
		client:   &http.Client{Jar: jar, Timeout: 30 * time.Second},
		screener: screener,
	}
}

// Fetch runs the screener and returns up to maxStocks cash-equity rows.
// Index/ETF rows (bsecode == nil) are dropped. Order is preserved from ChartInk.
func (s *Scraper) Fetch(ctx context.Context, maxStocks int) ([]Stock, error) {
	csrf, clause, err := s.bootstrap(ctx)
	if err != nil {
		return nil, err
	}

	form := url.Values{}
	form.Set("scan_clause", clause)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/screener/process", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("X-CSRF-TOKEN", csrf)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Referer", baseURL+"/screener/"+s.screener)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("process POST: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("process returned HTTP %d: %s", resp.StatusCode, body)
	}

	var out struct {
		Data []Stock `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode process JSON: %w", err)
	}

	stocks := make([]Stock, 0, maxStocks)
	for _, st := range out.Data {
		if st.BSECode == nil || st.Symbol == "" {
			continue // index/ETF pseudo-row
		}
		stocks = append(stocks, st)
		if len(stocks) >= maxStocks {
			break
		}
	}
	return stocks, nil
}

// bootstrap fetches the screener page and extracts the CSRF token + scan clause.
func (s *Scraper) bootstrap(ctx context.Context) (csrf, clause string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		baseURL+"/screener/"+s.screener, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := s.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("screener GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("screener page HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	page := string(body)

	m := csrfRe.FindStringSubmatch(page)
	if m == nil {
		return "", "", fmt.Errorf("csrf-token not found on screener page")
	}
	csrf = m[1]

	c := clauseRe.FindStringSubmatch(page)
	if c == nil {
		return "", "", fmt.Errorf("scan_clause not found on screener page")
	}
	clause = html.UnescapeString(c[1])

	return csrf, clause, nil
}
