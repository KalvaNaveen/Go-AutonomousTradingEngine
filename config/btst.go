package config

import "strings"

// envList reads a comma-separated env var into a trimmed, non-empty slice.
func envList(key, fallback string) []string {
	raw := envStr(key, fallback)
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ── BTST Auto-Trade parameters (see BTST_SPEC.md) ───────────────────────────
// All overridable via env so paper/live and cloud deploys need no recompile.
var (
	// BTSTCapitalPerDay is the fixed rupee amount deployed each trading day,
	// split equally across the day's stocks. T+1 sale proceeds are NOT reused
	// the same day — total ₹10L is split into two ₹5L day-buckets.
	BTSTCapitalPerDay = envFloat("BTST_CAPITAL_PER_DAY", 500000)

	// BTSTMaxStocks caps how many stocks are taken from EACH screener before the
	// distinct union (so two screeners can yield up to 2× this many names).
	BTSTMaxStocks = int(envFloat("BTST_MAX_STOCKS", 20))

	// BTSTStopLossPct is the TRAILING stop distance: the stop always sits this %
	// below the highest price seen since entry (the watermark) and only ratchets
	// up, never down. Initial stop = entry × (1 − pct/100).
	BTSTStopLossPct = envFloat("BTST_STOP_LOSS_PCT", 2.0)

	// BTSTScreeners is the comma-separated list of ChartInk saved-screener slugs.
	// Top BTSTMaxStocks from each are fetched and de-duplicated (first list wins).
	BTSTScreeners = envList("BTST_SCREENERS", "ema-reversal-93,pvema-3")

	// BTSTMonitorIntervalMin is how often (minutes) the intraday monitor polls
	// held positions during market hours to trail the stop and exit on breach.
	BTSTMonitorIntervalMin = int(envFloat("BTST_MONITOR_INTERVAL_MIN", 5))

	// ── Profit booking (trailing floor, NET of charges) ─────────────────
	// Once a position's watermark reaches a price worth BTSTProfitActivatePct
	// net return (charges included), the stop is raised to at least the price
	// worth BTSTProfitFloorPct net — locking a worst-case profit instead of
	// waiting for the day's square-off. Set activate to 0 to disable.
	BTSTProfitActivatePct = envFloat("BTST_PROFIT_ACTIVATE_PCT", 2.0)
	BTSTProfitFloorPct    = envFloat("BTST_PROFIT_FLOOR_PCT", 1.0)

	// BTSTEntryTime / BTSTExitTime are the HH:MM IST trigger times.
	BTSTEntryTime = envStr("BTST_ENTRY_TIME", "15:20")
	BTSTExitTime  = envStr("BTST_EXIT_TIME", "15:20")

	// ── Tier-1 macro gate thresholds ────────────────────────────────────
	// BTSTVIXMax: skip the whole day if India VIX closes above this (fear gauge).
	BTSTVIXMax = envFloat("BTST_VIX_MAX", 20.0)
	// BTSTNiftyDropPct: skip if Nifty 50 is down more than this % intraday.
	BTSTNiftyDropPct = envFloat("BTST_NIFTY_DROP_PCT", 1.5)
	// BTSTGateEnabled: master switch for the automated sentiment gate. Default OFF.
	// The gate code is kept dormant and can be re-enabled with BTST_GATE_ENABLED=true.
	BTSTGateEnabled = envBool("BTST_GATE_ENABLED", false)

	// BTSTTriggerToken protects the manual /api/run endpoint (scan+trade on demand,
	// for testing outside 15:20). Empty = endpoint DISABLED (safe default — the
	// dashboard URL is public). Set a long random value to enable.
	BTSTTriggerToken = envStr("BTST_TRIGGER_TOKEN", "")
)

// ── NSE session timings (verified against nseindia.com, not assumed) ─────────
// Pre-open 09:00–09:15; NORMAL continuous trading 09:15–15:30 (the only window
// for regular market orders); closing session 15:40–16:00 (closing-price orders
// only). BTST entry/exit at 15:20 sit just inside the 15:30 hard close.
const (
	NSEOpenTime  = "09:15"
	NSECloseTime = "15:30"
)
