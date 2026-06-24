package config

// ── BTST Auto-Trade parameters (see BTST_SPEC.md) ───────────────────────────
// All overridable via env so paper/live and cloud deploys need no recompile.
var (
	// BTSTCapitalPerDay is the fixed rupee amount deployed each trading day,
	// split equally across the day's stocks. T+1 sale proceeds are NOT reused
	// the same day — total ₹10L is split into two ₹5L day-buckets.
	BTSTCapitalPerDay = envFloat("BTST_CAPITAL_PER_DAY", 500000)

	// BTSTMaxStocks caps how many of the screener's stocks we actually trade.
	BTSTMaxStocks = int(envFloat("BTST_MAX_STOCKS", 20))

	// BTSTStopLossPct is the stop-loss distance below entry (configurable).
	BTSTStopLossPct = envFloat("BTST_STOP_LOSS_PCT", 6.5)

	// BTSTScreener is the ChartInk saved-screener slug that sources the stock list.
	BTSTScreener = envStr("BTST_SCREENER", "pur-ema10-20")

	// BTSTEntryTime / BTSTExitTime are the HH:MM IST trigger times.
	BTSTEntryTime = envStr("BTST_ENTRY_TIME", "15:20")
	BTSTExitTime  = envStr("BTST_EXIT_TIME", "15:20")

	// ── Tier-1 macro gate thresholds ────────────────────────────────────
	// BTSTVIXMax: skip the whole day if India VIX closes above this (fear gauge).
	BTSTVIXMax = envFloat("BTST_VIX_MAX", 20.0)
	// BTSTNiftyDropPct: skip if Nifty 50 is down more than this % intraday.
	BTSTNiftyDropPct = envFloat("BTST_NIFTY_DROP_PCT", 1.5)
	// BTSTGateEnabled: master switch for the sentiment gate (set false to trade unconditionally).
	BTSTGateEnabled = envBool("BTST_GATE_ENABLED", true)
)
