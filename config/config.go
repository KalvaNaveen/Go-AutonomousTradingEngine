package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// IST timezone
var IST *time.Location

func init() {
	IST = time.FixedZone("IST", 5*3600+30*60)
}

func NowIST() time.Time {
	return time.Now().In(IST)
}

func TodayIST() time.Time {
	now := NowIST()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, IST)
}

// ── Environment helpers ──────────────────────────────────────
func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err == nil {
			return f
		}
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	return strings.ToLower(v) == "true"
}

// ── Paper Trading Mode ──────────────────────────────────────
var PaperMode = envBool("PAPER_MODE", true)

// ── True Book Replica ───────────────────────────────────────
// This engine contains ONLY rules that appear in "Swing Trading Simplified"
// by Ankur Patel. All non-book features (India VIX guard, fixed cash reserve,
// per-sector cap, Chaikin Money Flow, OI/open-interest ranking, fundamental
// screen, news filter, EMA-crossover entry, time-stop, gain-% trailing tiers,
// RS-percentile rank) have been removed from the trading path.

// ── Zerodha ─────────────────────────────────────────────────
var (
	KiteAPIKey        = envStr("KITE_API_KEY", "")
	KiteAPISecret     = envStr("KITE_API_SECRET", "")
	KiteAccessToken   = envStr("KITE_ACCESS_TOKEN", "")
	KiteEncToken      = "" // captured from kite.zerodha.com session cookie after login
	KiteRedirectURL   = envStr("KITE_REDIRECT_URL", "https://127.0.0.1")
	ZerodhaUserID     = envStr("ZERODHA_USER_ID", "")
	ZerodhaPassword   = envStr("ZERODHA_PASSWORD", "")
	ZerodhaTOTPSecret = envStr("ZERODHA_TOTP_SECRET", "")
)

// ── Telegram ────────────────────────────────────────────────
var (
	TelegramBotToken = envStr("TELEGRAM_BOT_TOKEN", "")
	TelegramChatIDs  []string
)

// ── Dashboard / API ─────────────────────────────────────────
// DashboardAPIToken protects the JSON endpoints exposed by api.Server
// (positions, trades, status, ws/live). When empty, auth is disabled —
// recommended only for local-only paper-mode runs. In live mode, set this
// in .env and pass `?token=…` (or Authorization: Bearer …) on requests.
var DashboardAPIToken = envStr("DASHBOARD_API_TOKEN", "")

// DashboardAllowedOrigin restricts CORS on the dashboard API. Defaults to
// localhost; override via env when serving the dashboard from a different host.
var DashboardAllowedOrigin = envStr("DASHBOARD_ALLOWED_ORIGIN", "http://127.0.0.1:8085")

func init() {
	Reload()
}

// Reload re-evaluates variables which might have changed after godotenv is loaded.
func Reload() {
	PaperMode = envBool("PAPER_MODE", true)
	KiteAPIKey = envStr("KITE_API_KEY", "")
	KiteAPISecret = envStr("KITE_API_SECRET", "")
	KiteAccessToken = envStr("KITE_ACCESS_TOKEN", "")
	KiteRedirectURL = envStr("KITE_REDIRECT_URL", "https://127.0.0.1")
	ZerodhaUserID = envStr("ZERODHA_USER_ID", "")
	ZerodhaPassword = envStr("ZERODHA_PASSWORD", "")
	ZerodhaTOTPSecret = envStr("ZERODHA_TOTP_SECRET", "")

	TelegramBotToken = envStr("TELEGRAM_BOT_TOKEN", "")
	DashboardAPIToken = envStr("DASHBOARD_API_TOKEN", "")
	DashboardAllowedOrigin = envStr("DASHBOARD_ALLOWED_ORIGIN", "http://127.0.0.1:8085")
	TelegramChatIDs = nil
	raw := envStr("TELEGRAM_CHAT_IDS", "")
	for _, id := range strings.Split(raw, ",") {
		id = strings.TrimSpace(id)
		if id != "" {
			TelegramChatIDs = append(TelegramChatIDs, id)
		}
	}
}

// ── Capital ─────────────────────────────────────────────────
var TotalCapital = envFloat("TRADING_CAPITAL", 100000)

// ══════════════════════════════════════════════════════════════
//  SWING TRADING STRATEGY CONSTANTS
// ══════════════════════════════════════════════════════════════

// Phase 1: Market Timing — Book Ch.10 p.252-253: The 10 & 20 Rule
// "If one is a long-only swing trader, they should wait for the relevant indices
//  to trade above the 10 and the 20 EMAs, where the 10 EMA should be positioned
//  above the 20 EMA."
// NORMAL: Nifty 50 passes the rule. AGGRESSIVE: both Nifty + Smallcap pass.
// DEFENSIVE: rule fails.
const (
	// RegimeSMAPeriod / RegimeMomentumPeriod: informational only — used in the
	// Bird's Eye report for context display. The trading regime is driven solely
	// by the book's 10 & 20 Rule (Ch.10 p.252-253).
	RegimeSMAPeriod      = 200 // informational: SMA200 shown in bird's eye report
	RegimeMomentumPeriod = 21  // informational: 21-day ROC shown in bird's eye report

	ConsecutiveSLCutoff = 5    // 5 consecutive SL hits → reduce capital
	ReducedCapitalPct   = 0.35 // Reduce to ~35% capital on contingency
)

// Data lookback windows
const (
	// EODLookbackDays: calendar days used as fallback when historical.db is unavailable.
	// 1825 calendar days = 5 years. The DB-first Preload uses its own 1825-day constant;
	// this value is only hit when the DB cannot be opened.
	EODLookbackDays = 1825

	// RegimeLookbackDays kept equal to EODLookbackDays for date-alignment in backtest.
	RegimeLookbackDays = 1825
)


// ATHProximityPct: stock must be within this % of its 52-week high to pass the
// Phase 2 filter. Configurable via env ATH_PROXIMITY_PCT (default 10%).
// BOOK IS SILENT on a number — Ankur Patel says only "buy near highs" and frames it as
// a monthly→3M→6M→52-week-high progression (p.266-270). Value set by TA expertise:
// momentum breakouts coil within single-digit-% of the 52W high; 10% catches stocks
// just under the high without chasing extended names (Minervini's template allows 25%).
var ATHProximityPct = envFloat("ATH_PROXIMITY_PCT", 10.0)

// Section VI: Risk Management & Portfolio Sizing
// Dynamic position count: floor(TotalCapital × 0.90 / MinAbsPositionSize), capped at MaxPositionsHardCap.
// At ₹1L + ₹15K min: 6 positions. At ₹2L: 12 (hard cap).
// Book Ch.8 p.195 (Ankur Patel): "For swing traders, the sweet spot lies between
// having 8 to 12 open trades at a time... It is too hard to keep track of 20 or 25."
// Hard cap set to 12 = top of the book's stated sweet spot (was 15, now aligned to book).
const (
	MaxPositionsHardCap = 12      // Top of book's 8-12 sweet spot (Ch.8 p.195)
	MinAbsPositionSize  = 15000.0 // ₹15,000 minimum capital per trade slot
	LiquidityFilterCr   = 5.0     // Min avg daily turnover in ₹Cr to pass universe filter
)

// These vars are overridable at runtime via Apply Config (data/config_override.json).
// Best backtest params (2yr, 139 trades): SL 3–5%, Alloc 20%, MaxPos 10, Sharpe 1.14.
var (
	MaxTradeAllocPct = 20.0 // Upper allocation per trade (% of TotalCapital)
	MinTradeAllocPct = 15.0 // Lower allocation per trade (% of TotalCapital)

	// Structural SL — clamped between floor and ceiling below entry.
	// Book gives ranges (p.181): short-term 1-2%, positional 4-8%; author personal 2.5%.
	// TA expertise: swing trades sit between intraday and positional → 3-5% is the correct
	// band (survives normal noise without whipsaw). Also the best fit from backtest history.
	SLFloorPct  = 3.0 // SL never tighter than this % (avoids noise stop-out)
	SLCeilingPct = 5.0 // SL never wider than this % (user hard limit)

)

// ComputeMaxPositions returns the open-position cap for the given capital.
// Formula: floor(capital × 0.90 / ₹15K), capped at MaxPositionsHardCap.
func ComputeMaxPositions(capital float64) int {
	if capital <= 0 {
		return 1
	}
	n := int(capital * 0.90 / MinAbsPositionSize)
	if n < 1 {
		n = 1
	}
	if n > MaxPositionsHardCap {
		n = MaxPositionsHardCap
	}
	return n
}

// LoadOverride reads data/config_override.json (written by "Apply Config" in the dashboard)
// and applies non-zero values to the running config vars immediately.
// Safe to call at startup and at runtime — the live EMA agent picks up the new values
// on the very next scan cycle without a restart.
func LoadOverride(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return // file absent — keep current defaults, no error
	}
	var ov struct {
		Capital          float64 `json:"capital"`
		SLFloorPct       float64 `json:"sl_floor_pct"`
		SLCeilingPct     float64 `json:"sl_ceiling_pct"`
		MaxTradeAllocPct float64 `json:"max_trade_alloc_pct"`
	}
	if err := json.Unmarshal(data, &ov); err != nil {
		return
	}
	if ov.Capital > 0 {
		TotalCapital = ov.Capital
	}
	if ov.SLFloorPct > 0 {
		SLFloorPct = ov.SLFloorPct
	}
	if ov.SLCeilingPct > 0 {
		SLCeilingPct = ov.SLCeilingPct
	}
	if ov.MaxTradeAllocPct > 0 {
		MaxTradeAllocPct = ov.MaxTradeAllocPct
	}
}

// ComputeStructuralSL returns the stop-loss price for a long entry.
// It uses the previous candle's low as a structural reference, clamped
// between SLFloorPct and SLCeilingPct below the entry price.
func ComputeStructuralSL(entryPrice, prevCandleLow float64) float64 {
	structural := prevCandleLow * 0.998
	floor := entryPrice * (1 - SLFloorPct/100)   // tightest allowed
	ceiling := entryPrice * (1 - SLCeilingPct/100) // widest allowed
	sl := structural
	if sl > floor {
		sl = floor // structural is too tight — use floor
	}
	if sl < ceiling {
		sl = ceiling // structural is too wide — cap at ceiling
	}
	return sl
}

// ComputeATRStopLoss implements Book Ch.7 p.179-180 ATR-based stop method.
// Example: "If you enter at 100 and ATR=1.84, your stop-loss should be 98.16."
// For mini-base setups, the book recommends ≤1 ATR (so multiplier capped at
// ATRMiniBaseMaxMult). Same SLFloorPct/SLCeilingPct clamp is applied so this
// method composes safely with the rest of the risk framework.
func ComputeATRStopLoss(entryPrice, atr float64, isMiniBase bool) float64 {
	if atr <= 0 || entryPrice <= 0 {
		return entryPrice * (1 - SLCeilingPct/100) // fallback
	}
	mult := ATRStopMultiplier
	if isMiniBase && mult > ATRMiniBaseMaxMult {
		mult = ATRMiniBaseMaxMult
	}
	sl := entryPrice - mult*atr
	floor := entryPrice * (1 - SLFloorPct/100)
	ceiling := entryPrice * (1 - SLCeilingPct/100)
	if sl > floor {
		sl = floor
	}
	if sl < ceiling {
		sl = ceiling
	}
	return sl
}

// ComputeATR calculates Average True Range over `period` bars (default ATRPeriod).
// Standard Wilder ATR formula: TR = max(H-L, |H-Cprev|, |L-Cprev|), smoothed.
func ComputeATR(highs, lows, closes []float64, period int) float64 {
	n := len(highs)
	if n < period+1 || len(lows) != n || len(closes) != n {
		return 0
	}
	// Seed with simple average of first `period` TRs
	sum := 0.0
	for i := 1; i <= period; i++ {
		h, l, cp := highs[i], lows[i], closes[i-1]
		tr := h - l
		if x := h - cp; x > tr {
			tr = x
		}
		if x := cp - l; x > tr {
			tr = x
		}
		sum += tr
	}
	atr := sum / float64(period)
	// Wilder smoothing for remaining bars
	for i := period + 1; i < n; i++ {
		h, l, cp := highs[i], lows[i], closes[i-1]
		tr := h - l
		if x := h - cp; x > tr {
			tr = x
		}
		if x := cp - l; x > tr {
			tr = x
		}
		atr = (atr*float64(period-1) + tr) / float64(period)
	}
	return atr
}

// Section VII: Exits — EMA-based mechanical exit
// EMA10 = fast trend filter; EMA20 = trend confirmation and exit trigger.
const (
	EMA10Period        = 10 // Fast EMA — entry signal
	EMA20Period        = 20 // Trend EMA — exit trigger (close below = sell)
	// RedCandlesBelowEMA: book Ch.6 — single EOD close below EMA = exit.
	RedCandlesBelowEMA = 1
)


// ── Book Ch.3 / Ch.11: Trigger Candle thresholds ────────────────────────────
// A trigger candle marks the START of momentum — a young stock's launch day.
// Book (p.272): "Volume > 3× 50-day avg AND price change > 6.5%"
// These differ from the intraday VolumeSpikeMultiplier (1.5×) which confirms
// an existing setup. Trigger candle is used to discover NEW momentum entries.
const (
	TriggerCandleVolMultiplier = 3.0 // volume ≥ 3× 50-day average on trigger day
	TriggerCandlePricePct      = 6.5 // price must move ≥ 6.5% on the trigger day
)

// ── Book Ch.3: Extension Filter — 50 EMA ────────────────────────────────────
// "If a stock is 30–35% away from the 50 EMA, don't buy." (Ch.3 p.49-52)
// Prevents buying into parabolic/extended stocks with unfavorable risk-reward.
const (
	EMA50Period       = 50   // 50-day EMA — extension reference

	// MACD — fast=EMA10, slow=EMA20 (same as our trend EMAs), signal=EMA9
	// MACD line = EMA10 - EMA20. Histogram = MACD - Signal.
	// We only use histogram DIRECTION on the bounce bar (not crossover).
	MACDSignalPeriod = 9 // EMA of the MACD line → signal line
	Extension50EMAPct = 30.0 // skip if LTP > EMA50 × 1.30
)

// ── Book Ch.11: Minimum stock price filter ───────────────────────────────────
// EMA scan criterion: "CMP > Number 30" (p.267). Filters out penny/micro stocks.
var MinStockPrice = envFloat("MIN_STOCK_PRICE", 30.0)

// ── Relative Strength minimum threshold ──────────────────────────────────────
// Only stocks with RS Score >= this value appear in BUY signals and EOD summary.
// William O'Neil: "Only buy RS 80+". Minervini: "RS 90+".
// Set MIN_RS_SCORE in .env to override. Default: 80.
var MinRSScore = int(envFloat("MIN_RS_SCORE", 80))

// (RS-percentile rank, India VIX guards, and per-sector cap removed — none appear
//  in the book. RS is expressed via the Monthly Gainers scan; VIX is never mentioned;
//  sector concentration is not capped by the book.)

// ── Book Ch.3 p.50-55 + Ch.12 p.281: Leg counter — 3rd leg filter ────────────
// "Swing traders do best within the first three rally legs"; "I avoid buying above
// the second leg." LegCounterLookback: number of bars to look back when counting legs.
const LegCounterLookback = 90 // ~4 trading months — same as findSharpestRise window

// ── Book Ch.5: Market open noise filter (p.131) ───────────────────────────────
// "Avoid the first 30 minutes — fake breakouts are common at the open."
// Signals fired before MarketOpenNoiseMins have elapsed are suppressed.
const MarketOpenNoiseMins = 30

// ── Book Ch.5: Gap-up entry filter (p.130) ────────────────────────────────────
// "If a stock gaps up more than 3% above the prior close on breakout day,
//  it is already extended — wait for a pullback or skip."
const GapUpFilterPct = 3.0

// (Minimum cash-reserve constant removed — the book caps deployment only via
//  open-risk 4-5% and position count 8-12, not a fixed cash buffer.)


// ── Book Ch.9: Weekly chart alignment (p.220) ─────────────────────────────────
// "Before entering on the daily chart, check the weekly chart is also in uptrend
//  (last weekly close > weekly EMA20)."
// Weekly EMA periods — Book Ch.11 p.268: "10-week EMA above 30-week EMA"
// Weekly scan: price > 10W EMA AND 10W EMA > 30W EMA.
const (
	Weekly10EMAPeriod = 10 // Ch.11 p.268: 10-week EMA (fast trend filter)
	Weekly30EMAPeriod = 30 // Ch.11 p.268: 30-week EMA (long-term trend filter)
)

// ── Book Ch.4 p.133: Big Down Day red flag ──────────────────────────────────
// "When a stock has a big down day (≥5% single-day decline) in its recent base,
//  give it a skip for 5–10 trading sessions. These stocks need time to digest
//  the selling pressure before they can attempt a clean breakout."
// Volume confirmation: above-average volume on the down day makes it more severe.
const (
	BigDownDayPct      = 5.0 // ≥5% close-to-close decline = "big down day"
	BigDownDaySkipBars = 10  // skip stock for 10 trading days after a big down day
)

// ── Book Ch.4 p.135-136: Rejection Candle red flag ──────────────────────────
// "A rejection candle (stock tried to break out but closed well below the intraday
//  high) signals heavy supply at that level. Skip for 5–10 days or wait for the
//  stock to reclaim the rejection high before entering."
// BOOK IS SILENT on a wick %: it says only "closed well below the intraday high."
// Value set by TA expertise — the classic shooting-star/rejection candle closes in the
// lower third with the upper wick dominating; 60% upper-wick-to-range is the standard
// threshold (inside the textbook ">2× body" definition). SkipBars=10 = conservative end
// of the book's "5–10 days"; the reclaim-the-high trigger is the primary signal anyway.
const (
	RejectionWickRatio = 0.60 // upper wick ≥ 60% of H-L range = rejection (TA default; book silent)
	RejectionSkipBars  = 10   // skip for 10 trading days after rejection candle
	RejectionMinRange  = 0.01 // candle range must be ≥ 1% of close to count
)

// ══════════════════════════════════════════════════════════════════════════════
//  BOOK 1:1 REPLICA — Gaps closed (10 new rules from "Swing Trading Simplified")
// ══════════════════════════════════════════════════════════════════════════════

// ── Book Ch.11 p.269 (Fig 11.2): Monthly Gainers Scan — "Kullamagi MOMO" ──────
// "These are the stocks that exhibit the strongest performance within a specific
//  time period. Kullamagi zeroes in on the gainers of one, three, and six months."
// Conditions a stock must satisfy to appear in this scan (verbatim from book):
//   1. CMP > 30 (covered by MinStockPrice)
//   2. Market Cap > 1 Cr (covered by universe selection)
//   3. % Change in last 10 Days > 20% from the Lows
//   4. % Change in last 30 Days > 20% from the Lows
//   5. % Change in last 90 Days > 30% from the Lows
//   6. % Change in last 180 Days > 90% from the Lows
const (
	MonthlyGainers10DayMinPct  = 20.0
	MonthlyGainers30DayMinPct  = 20.0
	MonthlyGainers90DayMinPct  = 30.0
	MonthlyGainers180DayMinPct = 90.0
)

// ── Book Ch.11 p.271 (Fig 11.4): Tight Range Scan ─────────────────────────────
// "One of the main ingredients that will help you get to those quick and big
//  movers is focusing on stocks that have formed a tight range."
// Conditions verbatim:
//   1. Today's % Change ≤ 2.5%
//   2. Today's % Change ≥ -2.5%
//   3. Previous Day's % Change ≤ 3.5%
//   4. Previous Day's % Change ≥ -3.5%
const (
	TightRangeTodayMaxPct  = 2.5
	TightRangeYesterdayMaxPct = 3.5
)

// ── Book Ch.4 p.121: Pocket Pivot ─────────────────────────────────────────────
// "A pocket pivot day is when a stock goes up, and the volume is higher than the
//  highest volume on a down day in the past 10 days. It shows institutional
//  footprints. When buy signals happen close to a pocket pivot, they're usually
//  trustworthy."
// Used as a base-quality enhancer: signals near a recent pocket pivot get a boost.
const (
	PocketPivotLookback   = 10  // window of past N days to scan for highest down-day vol
	PocketPivotSignalDays = 5   // signal is considered "close to" pivot if within N days
)

// ── Book Ch.6 p.171-172: Downside Pivot Exit ──────────────────────────────────

// ── Book Ch.7 p.179-180: ATR-Based Stop Method ────────────────────────────────
// "You might set your stop-loss at 1 ATR away from your entry point." Example:
// "If you enter at 100 and ATR=1.84, your stop-loss should be 98.16."
// "If you're trading a mini-base setup, don't set your stop-loss too wide, or you
//  might lose out on potential profits. For example, a stop-loss larger than 1 ATR
//  could be too big for this type of trade."
// Used as an OPTIONAL alternative to structural SL; selected per-strategy.
const (
	ATRPeriod            = 14  // standard ATR window
	ATRStopMultiplier    = 1.0 // 1 ATR away from entry
	ATRMiniBaseMaxMult   = 1.0 // mini bases: SL ≤ 1 ATR
)

// ── Book Ch.10 p.257: 3-Session Net New Highs Breadth Confirmation ────────────
// "If the net new highs remain below the zero line for three consecutive sessions,
//  it signals the market's potential weakening. Conversely, if the net new highs
//  have been trading below zero but now has consistently crossed above it for
//  three consecutive sessions, it suggests a potential bullish market shift."
const (
	BreadthConfirmationSessions = 3 // need 3 consecutive sessions for a regime shift
)


// ── Book Ch.4 p.127: Higher Low in Base (quality filter) ──────────────────────
// "During a basing period, it's important to spot signs of buying interest. One
//  clear signal is when the stock's price keeps forming higher lows. This pattern
//  shows that confident investors are buying and holding onto the stock."
// Quality enhancer: require at least one higher low in the recent base window.
const (
	HigherLowBaseLookback   = 40 // ~2 months of base bars to scan
	HigherLowMinSwings      = 2  // need ≥2 swing lows to check progression
	HigherLowMinSeparation  = 5  // min bars between successive swing lows
)

// ── Book Ch.4 p.137: Breakout Attempts Quality Filter ─────────────────────────
// "Focus on bases where the stock has made an attempt to breakout at least once
//  or twice. The more times it has tried, the better chance it has this time."
// TATAINVEST example (Ch.4 p.138): "tried breaking out twice before finally doing
// so on 17 November 2023. The breakout led to a 44% surge in just three days."
const (
	BreakoutAttemptsLookback        = 60  // ~3 months base window to scan
	BreakoutAttemptsMinRequired     = 1   // require ≥1 prior attempt for higher conviction
	BreakoutAttemptsProximityPct    = 1.0 // touching within 1% of recent high counts as "attempt"
	BreakoutAttemptsFailRetracePct  = 2.0 // attempt must have closed back ≥2% below the high
)


// ── Book Ch.8: Risk-based position sizing ────────────────────────────────────
// "Quantity = Risk / (Entry − Stop-Loss)"  where Risk = Capital × RiskPerTradePct
// Default: 1% of capital at risk per trade. (p.200-201)
// Progressive exposure (p.202): baseline 0.5% → adjust per recent performance.
// Open risk cap (p.194): total open risk across all positions ≤ MaxOpenRiskPct.
const (
	RiskPerTradePct    = 1.0 // % of capital risked per single trade
	MaxOpenRiskPct     = 4.0 // cap: total open risk across all positions ≤ 4%
	MaxDrawdownHaltPct = 7.0 // halt new trades when drawdown > 7% (p.205: "keep below 5-7%")
)


// ── Instrument Tokens ───────────────────────────────────────
// Universe: Nifty Total Market 750 (Nifty 500 + Nifty Microcap 250).
// Equity tokens (~762): Full OHLCV history + live monitoring + pattern scans.
//   - 750 stocks from Nifty Total Market CSV
//   - 1 NIFTY 50 index (ROC regime)
//   - 1 NIFTY SMALLCAP 100 index (ROC regime)
// Benchmark tokens (12): Live monitoring ONLY — not in pattern scan.
//   - 10 sector indices + India VIX + Bank Nifty spot
// Regime tokens get RegimeLookbackDays (420); equity tokens get EODLookbackDays (500).
const (
	NiftySpotToken     = 256265 // NIFTY 50 spot → ROC regime
	SmallcapToken      = 264713 // NIFTY SMALLCAP 100 → Smallcap ROC
	IndiaVIXToken      = 264969 // India VIX → informational
	BankNiftySpotToken = 260105 // Bank Nifty → informational
)

// NiftyTotalMarketCSVURL is where NSE publishes the Nifty Total Market constituent list.
const NiftyTotalMarketCSVURL = "https://nsearchives.nseindia.com/content/indices/ind_niftytotalmarket_list.csv"

var SectorTokens = map[string]uint32{
	"NIFTY BANK":   260105,
	"NIFTY IT":     259849,
	"NIFTY AUTO":   263433,
	"NIFTY FMCG":   261129,
	"NIFTY METAL":  263689,
	"NIFTY REALTY": 261897,
	"NIFTY ENERGY": 261385,
	"NIFTY PHARMA": 262153,
	"NIFTY INFRA":  261641,
	"NIFTY PSE":    262665,
}

// ── Timing ──────────────────────────────────────────────────
const (
	MarketOpenTime     = "09:15"
	MarketCloseTime    = "15:30"
	EODScanCleanupDays = 30 // Auto-delete CSV files older than this
)

// ── Paths ───────────────────────────────────────────────────
var (
	BaseDir   string
	JournalDB string
)

func init() {
	if exe, err := os.Executable(); err == nil {
		BaseDir = filepath.Dir(exe)
		if strings.Contains(BaseDir, "go-build") || strings.Contains(BaseDir, "Temp") {
			if cwd, err := os.Getwd(); err == nil {
				BaseDir = cwd
			}
		}
	} else {
		BaseDir = "."
	}
	JournalDB = BaseDir + string(os.PathSeparator) + "data" + string(os.PathSeparator) + "journal.db"
}

// ParseTime parses "HH:MM" into hour, minute
func ParseTime(s string) (int, int) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0
	}
	h, _ := strconv.Atoi(parts[0])
	m, _ := strconv.Atoi(parts[1])
	return h, m
}

func PrintBanner() {
	fmt.Println("═══════════════════════════════════════════")
	fmt.Println("  ZENITH SIGNAL ENGINE v5.0")
	fmt.Println("  Swing Trading Simplified — Signal Only")
	fmt.Println("  Universe: Nifty Total Market 750")
	fmt.Println("═══════════════════════════════════════════")
	fmt.Printf("  Strategy: EMA Pullback Bounce (Ch.3)\n")
	fmt.Printf("  SL: %.1f-%.1f%% structural\n", SLFloorPct, SLCeilingPct)
	fmt.Printf("  EMA%d (fast) + EMA%d (trend) | Exit: close below EMA%d\n", EMA10Period, EMA20Period, EMA20Period)
	fmt.Printf("  Lookback: %d days | Alerts via Telegram\n", EODLookbackDays)
	fmt.Println("═══════════════════════════════════════════")
}
