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
//
// Two layers, in order:
//  1. keyword hard-block — severe, unambiguous terms (fraud, raid, insolvency…),
//     always on, zero cost.
//  2. optional LLM second opinion (Haiku) — catches subtler materially-negative
//     news the keyword list misses. Enabled only when an LLM client is wired.
type News struct {
	http *http.Client
	LLM  *LLM // optional; nil = keyword-only
}

// NewNews builds the news filter. If BTST_NEWS_LLM=true and ANTHROPIC_API_KEY is
// set, an LLM second-opinion layer is attached automatically.
func NewNews() *News {
	n := &News{http: &http.Client{Timeout: 12 * time.Second}}
	if llm := NewLLMFromEnv(); llm != nil {
		n.LLM = llm
	}
	return n
}

// Filter scans recent headlines per stock and returns the symbols to DROP, mapped
// to the reason. names maps symbol → company name (for a better search query).
// Keyword blocks happen first; surviving stocks with headlines are then sent to
// the LLM (if configured) for a materially-negative second opinion.
func (n *News) Filter(ctx context.Context, names map[string]string) map[string]string {
	out := make(map[string]string)
	survivors := make(map[string][]string) // symbol → headlines, for the LLM pass
	var mu sync.Mutex

	sem := make(chan struct{}, 6) // cap concurrent RSS fetches
	var wg sync.WaitGroup
	for sym, name := range names {
		wg.Add(1)
		sem <- struct{}{}
		go func(sym, name string) {
			defer wg.Done()
			defer func() { <-sem }()
			headlines, reason := n.fetchAndScreen(ctx, sym, name)
			mu.Lock()
			defer mu.Unlock()
			if reason != "" {
				out[sym] = reason
			} else if len(headlines) > 0 {
				survivors[sym] = headlines
			}
		}(sym, name)
	}
	wg.Wait()

	// LLM second opinion on stocks that passed the keyword screen.
	if n.LLM != nil && len(survivors) > 0 {
		for sym, reason := range n.LLM.Classify(ctx, survivors) {
			if _, already := out[sym]; !already {
				out[sym] = reason
			}
		}
	}
	return out
}

// fetchAndScreen pulls recent headlines for a stock, returns them, and a non-empty
// keyword reason if a severe term matched a recent company-specific headline.
func (n *News) fetchAndScreen(ctx context.Context, symbol, name string) (headlines []string, keywordReason string) {
	q := name
	if q == "" {
		q = symbol
	}
	q = q + " share NSE"
	rssURL := "https://news.google.com/rss/search?q=" +
		url.QueryEscape(q) + "&hl=en-IN&gl=IN&ceid=IN:en"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rssURL, nil)
	if err != nil {
		return nil, ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := n.http.Do(req)
	if err != nil {
		return nil, "" // fail-open: news outage must not block trading
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ""
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 200*1024))
	if err != nil {
		return nil, ""
	}

	cutoff := time.Now().Add(-newsRecencyHours * time.Hour)
	company := strings.ToLower(firstWord(name))
	sym := strings.ToLower(symbol)

	for _, item := range itemRe.FindAllStringSubmatch(string(body), -1) {
		block := item[1]
		tm := titleRe.FindStringSubmatch(block)
		if tm == nil {
			continue
		}
		rawTitle := strings.TrimSpace(tm[1])
		title := strings.ToLower(rawTitle)

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
		headlines = append(headlines, rawTitle)
		for _, kw := range blockKeywords {
			if strings.Contains(title, kw) {
				return headlines, fmt.Sprintf("news: %q", strings.TrimSpace(kw))
			}
		}
	}
	return headlines, ""
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
