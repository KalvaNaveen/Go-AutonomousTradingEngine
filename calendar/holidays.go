// Package calendar answers "is the market open today?" for the BTST scheduler.
// It combines a static NSE holiday fallback (2026–2027) with a daily refresh from
// NSE's public holiday API, plus weekend detection. Ported from the original
// engine's main.go so the scanner stack could be deleted without losing it.
package calendar

import (
	"log"
	"sync"
	"time"

	"bnf_go_engine/config"
	"bnf_go_engine/research"
)

var (
	mu      sync.RWMutex
	apiDays = map[string]string{} // YYYY-MM-DD → description (from NSE API)
)

// fallback covers 2026 + 2027; the API refresh overrides/extends it.
var fallback = map[string]bool{
	"2026-01-26": true, "2026-02-17": true, "2026-03-10": true,
	"2026-03-30": true, "2026-04-02": true, "2026-04-03": true,
	"2026-04-14": true, "2026-05-01": true, "2026-07-06": true,
	"2026-08-15": true, "2026-08-18": true, "2026-09-04": true,
	"2026-10-02": true, "2026-10-20": true, "2026-10-21": true,
	"2026-11-09": true, "2026-11-10": true, "2026-11-24": true,
	"2026-12-25": true,
	"2027-01-26": true, "2027-03-29": true, "2027-04-02": true,
	"2027-04-14": true, "2027-05-01": true, "2027-08-15": true,
	"2027-10-02": true, "2027-10-19": true, "2027-11-12": true,
	"2027-12-25": true,
}

// Refresh pulls the latest NSE holiday list. Safe to call at startup and daily.
func Refresh() {
	fetched := research.FetchNSEHolidays()
	if len(fetched) == 0 {
		log.Printf("[Calendar] NSE holiday fetch empty — keeping fallback")
		return
	}
	mu.Lock()
	for k, v := range fetched {
		apiDays[k] = v
	}
	mu.Unlock()
	log.Printf("[Calendar] NSE holidays refreshed: %d entries", len(fetched))
}

// IsHoliday reports whether t falls on an NSE trading holiday.
func IsHoliday(t time.Time) bool {
	d := t.Format("2006-01-02")
	mu.RLock()
	_, fromAPI := apiDays[d]
	mu.RUnlock()
	return fromAPI || fallback[d]
}

// IsTradingDay reports whether t is a weekday and not an NSE holiday.
func IsTradingDay(t time.Time) bool {
	if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		return false
	}
	return !IsHoliday(t)
}

// IsTradingToday is a convenience for "now, in IST".
func IsTradingToday() bool { return IsTradingDay(config.NowIST()) }
