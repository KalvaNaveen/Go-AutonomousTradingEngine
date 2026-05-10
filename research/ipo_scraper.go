package research

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// ══════════════════════════════════════════════════════════════
//  Section IV: IPO Research — Chittorgarh Scraper
// ══════════════════════════════════════════════════════════════
//  Doc: "Use the Chittorgarh website to find recently listed IPOs."

type IPOEntry struct {
	CompanyName string
	Symbol      string
	ListingDate string
	IssueType   string
	IssueSize   string
	ListingGain string
}

// FetchRecentIPOs scrapes Chittorgarh IPO performance tracker
func FetchRecentIPOs() ([]IPOEntry, error) {
	// The IPO Performance Tracker has actual tabular data
	urls := []string{
		"https://www.chittorgarh.com/report/ipo-listing-day-performance-nse-bse/83/",
		"https://www.chittorgarh.com/report/mainboard-ipo-listing-day-performance/83/",
	}

	for _, url := range urls {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
		req.Header.Set("Accept", "text/html,application/xhtml+xml")

		resp, err := httpClient.Do(req)
		if err != nil {
			log.Printf("[IPO] Failed to fetch %s: %v", url, err)
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			log.Printf("[IPO] %s returned %d", url, resp.StatusCode)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			continue
		}

		entries := parseIPOPage(string(body))
		if len(entries) > 0 {
			log.Printf("[IPO] Scraped %d IPO entries from Chittorgarh", len(entries))
			return entries, nil
		}
	}

	// Fallback: try parsing from the markdown content structure
	// Chittorgarh pages often have data in link text patterns
	return nil, fmt.Errorf("could not scrape IPO data from any Chittorgarh URL")
}

// parseIPOPage extracts IPO entries from Chittorgarh HTML
func parseIPOPage(html string) []IPOEntry {
	var entries []IPOEntry

	// Chittorgarh uses table rows with IPO data
	// Try multiple patterns to find the data

	// Pattern 1: Look for links to individual IPO pages
	// Format: /ipo/COMPANY-ipo/XXXX/ with surrounding text
	ipoLinkRe := regexp.MustCompile(`(?i)/ipo/([a-z0-9-]+)-ipo/\d+/`)
	linkMatches := ipoLinkRe.FindAllStringSubmatch(html, -1)

	seen := make(map[string]bool)
	for _, m := range linkMatches {
		slug := m[1]
		if seen[slug] {
			continue
		}
		seen[slug] = true

		// Convert slug to company name
		name := strings.ReplaceAll(slug, "-", " ")
		name = strings.Title(name)

		entry := IPOEntry{
			CompanyName: name,
			Symbol:      strings.ToUpper(strings.ReplaceAll(slug, "-", "")),
		}

		// Try to extract listing date and gain near this link
		idx := strings.Index(html, m[0])
		if idx >= 0 {
			// Look at surrounding 500 chars for date and percentage
			start := idx
			end := idx + 500
			if end > len(html) {
				end = len(html)
			}
			context := html[start:end]

			// Date patterns: "May 02, 2026" or "02-May-2026"
			dateRe := regexp.MustCompile(`(\d{1,2}[-/]\w{3}[-/]\d{4}|\w{3}\s+\d{1,2},?\s+\d{4})`)
			if dm := dateRe.FindString(context); dm != "" {
				entry.ListingDate = dm
			}

			// Percentage patterns: "+45.2%" or "-12.3%"
			pctRe := regexp.MustCompile(`([+-]?\d+\.?\d*)\s*%`)
			if pm := pctRe.FindStringSubmatch(context); len(pm) >= 2 {
				entry.ListingGain = pm[0]
			}
		}

		entries = append(entries, entry)
	}

	// Pattern 2: Look for table data with <td> elements
	if len(entries) == 0 {
		tdRe := regexp.MustCompile(`(?s)<td[^>]*>(.*?)</td>`)
		tagRe := regexp.MustCompile(`<[^>]+>`)
		tds := tdRe.FindAllStringSubmatch(html, -1)

		var row []string
		for _, td := range tds {
			clean := strings.TrimSpace(tagRe.ReplaceAllString(td[1], ""))
			if clean != "" {
				row = append(row, clean)
			}
			// Assume 6-8 columns per row
			if len(row) >= 6 {
				entry := IPOEntry{
					CompanyName: row[0],
					ListingDate: row[1],
					IssueSize:   row[2],
				}
				if len(row) > 5 {
					entry.ListingGain = row[5]
				}
				entries = append(entries, entry)
				row = nil
			}
		}
	}

	log.Printf("[IPO] Parsed %d IPO entries", len(entries))
	return entries
}

// FilterRecentIPOs returns IPOs listed in the last N days
func FilterRecentIPOs(entries []IPOEntry, withinDays int) []IPOEntry {
	var recent []IPOEntry
	cutoff := time.Now().AddDate(0, 0, -withinDays)

	for _, e := range entries {
		var listDate time.Time
		for _, layout := range []string{
			"Jan 02, 2006", "02-Jan-2006", "2006-01-02",
			"January 02, 2006", "02 Jan 2006",
			"Jan 2, 2006", "2-Jan-2006",
		} {
			if t, err := time.Parse(layout, e.ListingDate); err == nil {
				listDate = t
				break
			}
		}

		if !listDate.IsZero() && listDate.After(cutoff) {
			recent = append(recent, e)
		}
	}

	return recent
}
