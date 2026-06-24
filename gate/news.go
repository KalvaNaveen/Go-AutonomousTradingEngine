package gate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// blockKeywords flag company-specific bad news that warrants dropping a stock
// from today's BTST basket. Deliberately SEVERE and unambiguous only — generic
// terms like "sebi", "downgrade", "resign", "ban" were removed because they match
// routine headlines (regulatory approvals, analyst notes, normal retirements) and
// produced massive false-positive drops. Keyword-on-RSS is inherently noisy; this
// curated list errs toward keeping a stock unless the news is clearly material.
var blockKeywords = []string{
	"fraud", "scam", "ponzi", "embezzle", "money laundering",
	"ed raid", "cbi raid", "income tax raid", "ed summon", "arrest",
	"insolvency", "ibc proceeding", "delisting", "sebi bars", "sebi ban",
	"default on debt", "loan default", "auditor resign", "books manipulat",
}

var (
	itemRe    = regexp.MustCompile(`(?s)<item>(.*?)</item>`)
	titleRe   = regexp.MustCompile(`(?s)<title>(.*?)</title>`)
	pubDateRe = regexp.MustCompile(`(?s)<pubDate>(.*?)</pubDate>`)
)

// newsRecencyHours bounds how old a headline can be to still gate a stock.
const newsRecencyHours = 48

// News is the Tier-2 per-stock gate backed by Google News RSS (free, no auth).
type News struct {
	http *http.Client
}

// NewNews builds the news filter.
func NewNews() *News {
	return &News{http: &http.Client{Timeout: 12 * time.Second}}
}

// Filter scans recent headlines for each stock and returns the symbols to DROP,
// mapped to the matched reason. names maps symbol → company name (used to build a
// better search query). Lookups run concurrently with a small worker bound.
func (n *News) Filter(ctx context.Context, names map[string]string) map[string]string {
	type result struct {
		symbol, reason string
	}
	out := make(map[string]string)
	var mu sync.Mutex

	sem := make(chan struct{}, 6) // cap concurrent RSS fetches
	var wg sync.WaitGroup
	for sym, name := range names {
		wg.Add(1)
		sem <- struct{}{}
		go func(sym, name string) {
			defer wg.Done()
			defer func() { <-sem }()
			if reason, bad := n.checkOne(ctx, sym, name); bad {
				mu.Lock()
				out[sym] = reason
				mu.Unlock()
			}
		}(sym, name)
	}
	wg.Wait()
	_ = result{}
	return out
}

func (n *News) checkOne(ctx context.Context, symbol, name string) (string, bool) {
	q := name
	if q == "" {
		q = symbol
	}
	q = q + " share NSE"
	rssURL := "https://news.google.com/rss/search?q=" +
		url.QueryEscape(q) + "&hl=en-IN&gl=IN&ceid=IN:en"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rssURL, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := n.http.Do(req)
	if err != nil {
		return "", false // fail-open: news outage must not block trading
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 200*1024))
	if err != nil {
		return "", false
	}

	// Scan each <item>: require recency (last 48h) AND a company mention AND a
	// severe keyword. All three must hold to drop the stock.
	cutoff := time.Now().Add(-newsRecencyHours * time.Hour)
	company := strings.ToLower(firstWord(name))
	sym := strings.ToLower(symbol)

	for _, item := range itemRe.FindAllStringSubmatch(string(body), -1) {
		block := item[1]
		tm := titleRe.FindStringSubmatch(block)
		if tm == nil {
			continue
		}
		title := strings.ToLower(tm[1])

		// Recency check — skip stale headlines.
		if pm := pubDateRe.FindStringSubmatch(block); pm != nil {
			if pub, err := time.Parse(time.RFC1123Z, strings.TrimSpace(pm[1])); err == nil && pub.Before(cutoff) {
				continue
			}
		}

		// Company mention check.
		if !strings.Contains(title, company) && !strings.Contains(title, sym) {
			continue
		}
		for _, kw := range blockKeywords {
			if strings.Contains(title, kw) {
				return fmt.Sprintf("news: %q", strings.TrimSpace(kw)), true
			}
		}
	}
	return "", false
}

func firstWord(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	if s == "" {
		return "\x00unlikely\x00" // never matches
	}
	return s
}
