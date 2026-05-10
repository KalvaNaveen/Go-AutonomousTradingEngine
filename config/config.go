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
var TotalCapital = envFloat("TRADING_CAPITAL", 500000)

// ══════════════════════════════════════════════════════════════
//  SWING TRADING STRATEGY CONSTANTS (Harsh's System)
// ══════════════════════════════════════════════════════════════

// Phase 1: Market Timing — ROC (Rate of Change) thresholds
// Doc says: "Timeframe 1 Month, Length 18" → 18 monthly candles = 18 × 21 trading days = 378 daily bars
// Doc says: "Smallcap Length 20" → 20 × 21 = 420 daily bars
const (
	ROCNiftyLengthDaily        = 378   // 18 months × 21 trading days
	ROCSmallcapLengthDaily     = 420   // 20 months × 21 trading days
	ROCNiftyBuyThreshold       = 5.0   // Buy signal: ROC near 0 (within this band)
	ROCNiftySellThreshold      = 45.0  // Sell signal: ROC reaches near 45
	ROCSmallcapBuyThreshold    = 5.0   // Buy signal: ROC near 0
	ROCSmallcapSellThreshold   = 100.0 // Sell signal: ROC reaches near 100
	ConsecutiveSLCutoff        = 5     // 5 consecutive SL hits → reduce capital
	ReducedCapitalPct          = 0.35  // Reduce to 30-40% capital on contingency
)

// Section V: Technical Entry Setups
const (
	VCPLookbackDays       = 60   // Days to look back for VCP pattern
	VCPMinPullbacks       = 3    // Min pullbacks for contraction (doc example: 25%->10%->5% = 3)
	VCPContractionRatio   = 0.7  // Each pullback depth must be < previous
	ATHProximityPct       = 5.0  // Stock must be at or near All-Time Highs
)

// Section VI: Risk Management & Portfolio Sizing
// Doc: "Absolute Maximum SL: 7%"
// Doc: "Ideal Active SL: 3% to 5%"
// Doc: "5% to 10% of total portfolio per trade"
// Doc: "Maximum 5 to 6 stocks"
const (
	MaxOpenPositions      = 6     // "Maximum 5 to 6 stocks at any given time"
	HardStopLossPct       = 7.0   // "Absolute Maximum SL: 7%"
	IdealSLPct            = 5.0   // "Ideal Active SL: 3% to 5%" (upper)
	TightSLPct            = 3.0   // "3% SL for 6-9% target"
	TightTargetPct        = 7.5   // "6-9% target" (midpoint)
	IdealTargetPct        = 12.5  // "5% SL for 10-15% target" (midpoint)
	MaxTradeAllocPct      = 10.0  // "5% to 10% of total portfolio per trade"
	MinTradeAllocPct      = 5.0   // Lower bound of allocation
)

// Section VII: Exits — 21 EMA Mechanical Exit
// Doc: "Apply the 21-day EMA to your daily chart to ride the trend"
// 63 EMA is used ONLY for VCP invalidation (Section V.1), NOT for exits
const (
	EMA21Period           = 21 // Exit trailing indicator: 21-day EMA
	EMA63Period           = 63 // VCP invalidation check only
	RedCandlesBelowEMA    = 2  // "two continuous red candles that CLOSE below the 21 EMA"
)

// ── Instrument Tokens ───────────────────────────────────────
// FIX-12: Token Count Documentation
// Equity tokens (253): Full OHLCV history + live monitoring + pattern scans
//   - 250 Nifty 250 stocks from NSE universe CSV
//   - 1 GOLDBEES ETF (Gold ratio computation)
//   - 1 NIFTY 50 index (ROC regime + Gold ratio)
//   - 1 NIFTY SMALLCAP 100 index (ROC regime)
// Benchmark tokens (12): Live monitoring ONLY — WebSocket subscribed but NOT in pattern scan
//   - 10 sector indices (Bank, IT, Auto, FMCG, Metal, Realty, Energy, Pharma, Infra, PSE)
//   - 1 India VIX
//   - 1 Bank Nifty spot (informational)
// Total WebSocket tokens ≈ 265 (253 equity + 12 benchmark)
const (
	Nifty250Token       = 289545
	NiftySpotToken      = 256265  // NIFTY 50 spot → ROC regime + Gold ratio
	SmallcapToken       = 264713  // NIFTY SMALLCAP 100 → Smallcap ROC
	IndiaVIXToken       = 264969  // India VIX → informational
	BankNiftySpotToken  = 260105  // Bank Nifty → informational
)

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
	MarketOpenTime  = "09:15"
	MarketCloseTime = "15:30"
	EODCheckTime    = "15:20" // Run daily EMA exit check near close
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
	fmt.Println("═══════════════════════════════════════════")
	fmt.Println("  SWING ENGINE v4.0 — Final Verified Blueprint")
	fmt.Println("  Harsh & Apoorva's System (Strict 1:1)")
	fmt.Println("═══════════════════════════════════════════")
	if PaperMode {
		fmt.Println("  Mode: PAPER (Virtual Fills)")
	} else {
		fmt.Println("  Mode: LIVE (Real Orders)")
	}
	fmt.Printf("  Capital: ₹%.0f | Max Positions: %d\n", TotalCapital, MaxOpenPositions)
	fmt.Printf("  Max SL: %.0f%% | Ideal SL: %.0f-%.0f%%\n", HardStopLossPct, TightSLPct, IdealSLPct)
	fmt.Printf("  Trade Size: %.0f-%.0f%% | Exit: %d-EMA (%d red candles)\n", MinTradeAllocPct, MaxTradeAllocPct, EMA21Period, RedCandlesBelowEMA)
	fmt.Println("═══════════════════════════════════════════")
}
