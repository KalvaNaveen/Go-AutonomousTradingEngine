package research

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"bnf_go_engine/config"
)

// ══════════════════════════════════════════════════════════════
//  Section V.2: Event Calendar for Bull Flag Invalidation
// ══════════════════════════════════════════════════════════════
//  Doc: "Do not trade this pattern if there is a massive
//  global/domestic fundamental trigger occurring (e.g., Budget Day,
//  Trade Deals, Corporate Governance news)."

// MajorEvents — hardcoded known high-impact dates (updated annually)
// These are dates where massive market-moving news is expected.
var MajorEvents = map[string]string{
	// Budget
	"02-01": "Union Budget Day",
	"02-02": "Budget Day (alt)",
	// RBI Monetary Policy (bi-monthly, approximate)
	"02-07": "RBI Policy",
	"04-05": "RBI Policy",
	"06-06": "RBI Policy",
	"08-08": "RBI Policy",
	"10-04": "RBI Policy",
	"12-06": "RBI Policy",
	// Election results (add specific dates when known)
	// US Fed meetings (market impact)
	"01-31": "US Fed Decision",
	"03-19": "US Fed Decision",
	"05-07": "US Fed Decision",
	"06-18": "US Fed Decision",
	"07-30": "US Fed Decision",
	"09-17": "US Fed Decision",
	"11-05": "US Fed Decision",
	"12-17": "US Fed Decision",
}

// NSE holiday API response
type nseHolidayResp struct {
	CM []struct {
		TradingDate string `json:"tradingDate"`
		Description string `json:"description"`
	} `json:"CM"`
}

// FetchNSEHolidays fetches trading holidays from NSE
func FetchNSEHolidays() map[string]string {
	holidays := make(map[string]string)

	req, err := http.NewRequest("GET",
		"https://www.nseindia.com/api/holiday-master?type=trading", nil)
	if err != nil {
		log.Printf("[Events] Failed to create NSE request: %v", err)
		return holidays
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("[Events] NSE holidays fetch failed: %v", err)
		return holidays
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return holidays
	}

	var data nseHolidayResp
	if err := json.Unmarshal(body, &data); err != nil {
		log.Printf("[Events] NSE holidays parse failed: %v", err)
		return holidays
	}

	for _, h := range data.CM {
		// Parse "02-Jan-2026" format → store as YYYY-MM-DD for unambiguous lookup
		t, err := time.Parse("02-Jan-2006", h.TradingDate)
		if err == nil {
			key := t.Format("2006-01-02")
			holidays[key] = h.Description
		}
	}

	log.Printf("[Events] Loaded %d NSE holidays", len(holidays))
	return holidays
}

// IsMajorEventDay checks if today (or ±1 day) is a major event day.
// Returns true if Bull Flag signals should be suppressed.
func IsMajorEventDay() (bool, string) {
	now := config.NowIST()

	// Check today, yesterday, and tomorrow
	for _, offset := range []int{-1, 0, 1} {
		day := now.AddDate(0, 0, offset)
		key := fmt.Sprintf("%02d-%02d", day.Month(), day.Day())

		if event, ok := MajorEvents[key]; ok {
			log.Printf("[Events] ⚠️ Major event nearby: %s (%s)", event, day.Format("02-Jan"))
			return true, event
		}
	}

	return false, ""
}

