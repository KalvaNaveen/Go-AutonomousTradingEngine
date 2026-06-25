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
	// BTSTGateEnabled: master switch for the automated sentiment gate. Default OFF —
	// the manual Telegram BUY approval (below) replaces it. The gate code is kept
	// dormant and can be re-enabled with BTST_GATE_ENABLED=true.
	BTSTGateEnabled = envBool("BTST_GATE_ENABLED", false)

	// ── Manual BUY approval (Telegram) ──────────────────────────────────
	// When enabled, the engine proposes the day's basket to Telegram at the entry
	// time and places BUY orders ONLY after a PROCEED reply. No reply by the
	// deadline → auto-HOLD (skip the day). SELL (T+1 square-off) is never gated.
	BTSTApprovalEnabled = envBool("BTST_APPROVAL", true)
	// BTSTApprovalDeadline is the HH:MM IST cutoff for a reply; must leave room
	// before the 15:30 NSE close so market orders still execute. Default 15:28.
	BTSTApprovalDeadline = envStr("BTST_APPROVAL_DEADLINE", "15:28")

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
