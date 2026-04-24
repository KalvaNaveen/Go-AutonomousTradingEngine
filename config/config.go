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
	KiteAPIKey       = envStr("KITE_API_KEY", "")
	KiteAPISecret    = envStr("KITE_API_SECRET", "")
	KiteAccessToken  = envStr("KITE_ACCESS_TOKEN", "")
	KiteRedirectURL  = envStr("KITE_REDIRECT_URL", "https://127.0.0.1")
	ZerodhaUserID    = envStr("ZERODHA_USER_ID", "")
	ZerodhaPassword  = envStr("ZERODHA_PASSWORD", "")
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
var TotalCapital = envFloat("TRADING_CAPITAL", 100000)

// ── Risk — Adaptive Intraday System ─────────────────────────
const (
	MaxRiskPerTradePct = 0.005  // 0.5% of capital per trade (was 0.05% — too small to cover charges)
	DailyLossLimitPct  = 0.04  // 4% daily loss limit (was 6% — tighter circuit breaker with larger sizing)
	MaxConsecutiveLosses = 8   // Stop after 8 consecutive losses (was 15)
	MaxOpenPositions   = 10    // Max 10 concurrent positions (was 20 — focus capital on best setups)
	MaxPositionsPerStrat = 3   // Max 3 per strategy (was 5)
	MaxPositionPct     = 0.15
	EODSquareoffTime   = "15:15"
	EODSquareoffFinal  = "15:15"
	PreemptiveExitTime = "14:50"

	STTBuffer         = 0.997
	ActiveCapitalPct  = 0.80
	RiskReservePct    = 0.20
)

// ── Instrument Tokens ───────────────────────────────────────
const (
	Nifty50Token  = 256265
	IndiaVIXToken = 264969
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

// ── Regime Thresholds ───────────────────────────────────────
const (
	VIXBearPanic   = 22.5
	VIXNormalHigh  = 22.0
	VIXNormalLow   = 12.0
	VIXBullMax     = 18.0
	VIXExtremeStop = 30.0
)

// ══════════════════════════════════════════════════════════════
//  STRATEGY CONFIGURATIONS  (var, not const — enables runtime optimization)
// ══════════════════════════════════════════════════════════════

// S1: Moving Average Crossover
var (
	S1_EMA_FAST    = 9
	S1_EMA_SLOW    = 21
	S1_EMA_TREND   = 200
	S1_ADX_PERIOD  = 14
	S1_ADX_MIN     = 20
	S1_ATR_SL_MULT = 1.5
	S1_RR          = 3.0
	S1_RISK_PCT    = 0.01
)

// S2: BB + RSI Mean Reversion
var (
	S2_BB_PERIOD      = 20
	S2_BB_SD          = 2.0
	S2_RSI_PERIOD     = 14
	S2_RSI_OVERSOLD   = 30  // Standard 30 (was 32)
	S2_RSI_OVERBOUGHT = 70  // Standard 70 (was 68)
	S2_ATR_SL_MULT    = 1.0
	S2_RR             = 1.2
	S2_RISK_PCT       = 0.005
	S2_MAX_HOLD_MINS  = 45
	S2_VIX_MAX        = 30.0
)

// S3: Opening Range Breakout
var (
	S3_RISK_PCT    = 0.005
	S3_MAX_TRADES  = 2
	S3_ENTRY_END   = "11:30"
	S3_EXIT_TIME   = "15:20"
	S3_TARGET_MULT = 1.0
)

// S6 Trend Short (Intraday VWAP Breakdown)
var (
	S6_EMA_FAST          = 9
	S6_EMA_SLOW          = 20
	S6_RSI_PERIOD        = 14
	S6_RSI_ENTRY_LOW     = 35
	S6_RSI_ENTRY_HIGH    = 60
	S6_RSI_EXIT          = 25
	S6_COOLDOWN_DAYS     = 1
	S6_MIN_TURNOVER_CR   = 30.0
	S6_RELATIVE_WEAKNESS = 0.005 // 0.5% weaker than Nifty
	S6_RVOL_MIN          = 1.5
	S6_VWAP_FILTER       = true
	S6_MIN_PRICE         = 200.0
)

// S6 VWAP Band
var (
	S6_VWAP_SD       = 2.0
	S6_VWAP_RISK_PCT = 0.005
	S6_VWAP_RR       = 1.5
)

// S7: Mean Reversion Long
var (
	S7_RSI_PERIOD         = 14
	S7_RSI_OVERSOLD       = 28
	S7_RSI_EXIT           = 45
	S7_VWAP_DEVIATION_PCT = 0.020
	S7_MIN_TURNOVER_CR    = 50.0
	S7_RVOL_MIN           = 1.5
	S7_ATR_PERIOD         = 14
)

// S8: Volume Profile + Pivot
var (
	S8_VOL_SPIKE_MULT = 2.5
	S8_RISK_PCT       = 0.0075
)

// S9: Multi-Timeframe Momentum
var (
	S9_EMA_TREND     = 200
	S9_RSI_PERIOD    = 14
	S9_RSI_THRESHOLD = 45
	S9_ATR_SL_MULT   = 2.0
	S9_RR            = 3.0
)

// S14: RSI Scalper (RSI-2 on 5min — Larry Connors adapted for intraday)
var (
	S14_RSI_PERIOD     = 2
	S14_RSI_OVERSOLD   = 5   // Connors research: <5 for high-probability entries (was 10)
	S14_RSI_OVERBOUGHT = 95  // Connors research: >95 for high-probability entries (was 90)
	S14_RSI_EXIT       = 50
	S14_STOP_PCT       = 0.005 // 0.5% tight scalp stop
	S14_MAX_HOLD_MINS  = 30
	S14_MIN_PRICE      = 50.0
)

// S15: RSI Swing (RSI-14 pullback with EMA20 trend confirmation)
var (
	S15_RSI_PERIOD     = 14
	S15_RSI_OVERSOLD   = 30  // Standard 30 (was 35)
	S15_RSI_OVERBOUGHT = 70  // Standard 70 (was 65)
	S15_EMA_TREND      = 20
	S15_ATR_SL_MULT    = 1.0
	S15_RR             = 2.0
	S15_MAX_HOLD_MINS  = 120
	S15_MIN_PRICE      = 100.0
)

// ── Timing ──────────────────────────────────────────────────
const (
	TradeWindowStart    = "09:16"
	TradeWindowEnd      = "15:15"
	IntradaySquareoff   = "15:15"
	HuntWindowStart     = "09:16"
	LastEntryTime       = "14:50"
	FillPollIntervalSec = 30
	FillTimeoutMinutes  = 30
)

const (
	MinATRPercentile = 50
	MinDailyVolume   = 500000
)

// ── Paths ───────────────────────────────────────────────────
var (
	BaseDir  string
	StateDB  string
	JournalDB string
)

func init() {
	// BaseDir is set to the executable's directory
	if exe, err := os.Executable(); err == nil {
		BaseDir = filepath.Dir(exe)
		
		// If running via 'go run', the exe is in a temp directory, so fallback to current working directory
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

// DisabledStrategies — empty: all strategies active (S6 enabled after rework)
var DisabledStrategies = map[string]bool{}

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
	fmt.Println("  QUANTIX ENGINE v2.0 — Full Native Golang")
	fmt.Println("  Ultra HFT Nanosecond Trading Platform")
	fmt.Println("═══════════════════════════════════════════")
	if PaperMode {
		fmt.Println("  Mode: PAPER (Virtual Fills)")
	} else {
		fmt.Println("  Mode: LIVE (Real Orders)")
	}
	fmt.Printf("  Capital: ₹%.0f\n", TotalCapital)
	fmt.Println("═══════════════════════════════════════════")
}
