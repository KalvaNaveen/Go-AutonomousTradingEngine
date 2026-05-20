package config

import (
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

// ── Zerodha ─────────────────────────────────────────────────
var (
	KiteAPIKey        = envStr("KITE_API_KEY", "")
	KiteAPISecret     = envStr("KITE_API_SECRET", "")
	KiteAccessToken   = envStr("KITE_ACCESS_TOKEN", "")
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

// Phase 1: Market Timing — ROC (Rate of Change) thresholds
// 18 monthly candles = 378 daily bars; 20 monthly = 420 daily bars
const (
	ROCNiftyLengthDaily      = 378   // 18 months × 21 trading days
	ROCSmallcapLengthDaily   = 420   // 20 months × 21 trading days
	ROCNiftyBuyThreshold     = 5.0
	ROCNiftySellThreshold    = 45.0
	ROCSmallcapBuyThreshold  = 5.0
	ROCSmallcapSellThreshold = 100.0
	ConsecutiveSLCutoff      = 5    // 5 consecutive SL hits → reduce capital
	ReducedCapitalPct        = 0.35 // Reduce to ~35% capital on contingency
)

// Data lookback windows
const (
	// EODLookbackDays is the history fetched for every equity in the universe.
	// 150 days covers: EMA10 warm-up (~50 bars), EMA20 warm-up (~100 bars),
	// VCP pattern window (60 bars), and 20-day avg-volume baseline.
	EODLookbackDays = 150

	// RegimeLookbackDays is used only for the two regime-index tokens
	// (NiftySpot, SmallcapToken) which need 420 bars for the ROC calculation.
	RegimeLookbackDays = 420
)

// Section V: Technical Entry Setups
const (
	VCPLookbackDays     = 60   // Days to look back for VCP pattern
	VCPMinPullbacks     = 3    // Min pullbacks for contraction
	VCPContractionRatio = 0.98 // Each pullback depth must be < 98% of the prior
	ATHProximityPct     = 5.0  // Stock must be within 5% of All-Time High
)

// Section VI: Risk Management & Portfolio Sizing
// Dynamic position count: floor(TotalCapital × 0.90 / MinAbsPositionSize), capped at MaxPositionsHardCap.
// At ₹1L + ₹15K min: 6 positions. At ₹2L: 12. At ₹3L+: 15 (hard cap).
const (
	MaxPositionsHardCap  = 15      // Absolute ceiling regardless of capital
	MinAbsPositionSize   = 15000.0 // ₹15,000 minimum capital per trade slot
	MaxTradeAllocPct     = 20.0    // Upper allocation per trade (% of TotalCapital)
	MinTradeAllocPct     = 15.0    // Lower allocation per trade (% of TotalCapital)
	LiquidityFilterCr    = 5.0     // Min avg daily turnover in ₹Cr to pass universe filter

	// Structural SL: max(entry×(1-SLCeilingPct/100), min(entry×(1-SLFloorPct/100), prev_low×0.998))
	// Keeps SL between 1.5% (noise floor) and 3% (max allowed loss).
	SLFloorPct  = 1.5 // SL never tighter than 1.5% (avoids noise stop-out)
	SLCeilingPct = 3.0 // SL never wider than 3% (user hard limit)
)

// ComputeMaxPositions returns the dynamic open-position cap for the given capital.
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

// Section VII: Exits — EMA-based mechanical exit
// EMA10 = fast trend filter; EMA20 = trend confirmation and exit trigger.
// 63 EMA is used ONLY for VCP invalidation, NOT for entries or exits.
const (
	EMA10Period        = 10 // Fast EMA — crossover entry signal
	EMA20Period        = 20 // Trend EMA — entry confirmation + exit trigger
	EMA63Period        = 63 // VCP invalidation check only
	RedCandlesBelowEMA = 2  // Two consecutive closes below EMA20 → exit next open

	// Volume direction: Close Position Ratio = (Close-Low)/(High-Low)
	// Checked over last 3 days to classify buying vs selling pressure.
	VolumePressureDays      = 3    // How many days to check for sustained pressure
	BuyPressureRatio        = 0.65 // CPR above this = buyers in control
	SellPressureRatio       = 0.35 // CPR below this = sellers in control
	VolumeSpikeMultiplier   = 1.5  // Volume must be >1.5× 20-day avg to confirm signal
	VolumePressureDaysNeeded = 2   // 2 of 3 days must confirm direction
)

// ── Instrument Tokens ───────────────────────────────────────
// Universe: Nifty Total Market 750 (Nifty 500 + Nifty Microcap 250).
// Equity tokens (~762): Full OHLCV history + live monitoring + pattern scans.
//   - 750 stocks from Nifty Total Market CSV
//   - 1 NIFTY 50 index (ROC regime)
//   - 1 NIFTY SMALLCAP 100 index (ROC regime)
// Benchmark tokens (12): Live monitoring ONLY — not in pattern scan.
//   - 10 sector indices + India VIX + Bank Nifty spot
// Regime tokens get RegimeLookbackDays (420); equity tokens get EODLookbackDays (150).
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

// ── Timing (Swing — no intraday squareoff) ──────────────────
const (
	MarketOpenTime      = "09:15"
	MarketCloseTime     = "15:30"
	EODCheckTime        = "15:31" // Run daily EMA exit check after market close (candle finalized at 15:30)
	EODScanTime         = "15:45" // Run full market scan near close
	EODScanCleanupDays  = 30      // Auto-delete CSV files older than this
)

// ── Paths ───────────────────────────────────────────────────
var (
	BaseDir   string
	StateDB   string
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
	StateDB = BaseDir + string(os.PathSeparator) + "data" + string(os.PathSeparator) + "engine_state.db"
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
	maxPos := ComputeMaxPositions(TotalCapital)
	fmt.Println("═══════════════════════════════════════════")
	fmt.Println("  QUANTIX ENGINE v5.0 — EMA + Volume System")
	fmt.Println("  Universe: Nifty Total Market 750")
	fmt.Println("═══════════════════════════════════════════")
	if PaperMode {
		fmt.Println("  Mode: PAPER (Virtual Fills)")
	} else {
		fmt.Println("  Mode: LIVE (Real Orders)")
	}
	fmt.Printf("  Capital: ₹%.0f | Max Positions: %d (dynamic)\n", TotalCapital, maxPos)
	fmt.Printf("  SL: %.1f-%.1f%% structural | Trade Size: %.0f-%.0f%%\n", SLFloorPct, SLCeilingPct, MinTradeAllocPct, MaxTradeAllocPct)
	fmt.Printf("  EMA%d (fast) + EMA%d (trend) | Exit: %d red candles below EMA%d\n", EMA10Period, EMA20Period, RedCandlesBelowEMA, EMA20Period)
	fmt.Printf("  Lookback: %d days equity | %d days regime\n", EODLookbackDays, RegimeLookbackDays)
	fmt.Println("═══════════════════════════════════════════")
}
