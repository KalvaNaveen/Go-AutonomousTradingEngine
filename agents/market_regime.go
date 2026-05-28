package agents

import (
	"fmt"
	"log"
	"strings"

	"bnf_go_engine/config"
)

// ══════════════════════════════════════════════════════════════
//  Bird's Eye View — Book Ch.10 (p.247-270)
//  "Before looking at individual stocks, always check the health
//   of the overall market. Trade only when the market is healthy."
// ══════════════════════════════════════════════════════════════

// MarketHealthReport is the full Bird's Eye View snapshot.
// Generated daily by RunBirdsEyeView() and sent via Telegram.
type MarketHealthReport struct {
	// Regime
	Regime      string // DEFENSIVE / NORMAL / AGGRESSIVE / REDUCED_CAPITAL
	NiftyCurrent float64
	NiftySMA200  float64
	NiftyEMA10   float64
	NiftyEMA20   float64
	AboveSMA200  bool
	EMA10AboveEMA20 bool

	// Market Breadth (Book Ch.10 p.255-260)
	TotalStocks       int     // Universe size
	PctAboveEMA20     float64 // % of stocks above their 20 EMA
	PctAboveEMA50     float64 // % of stocks above their 50 EMA
	New52WHigh        int     // stocks making new 52-week highs today
	New52WLow         int     // stocks making new 52-week lows today
	NetNew52WHigh     int     // New52WHigh - New52WLow (breadth signal)
	ADRatio           float64 // Advance/Decline ratio (from live feed)

	// Book Ch.10 p.257: Net new highs 3-session confirmation
	//   "If the net new highs remain below the zero line for three consecutive
	//    sessions, it signals the market's potential weakening. Conversely, if
	//    the net new highs ... has consistently crossed above it for three
	//    consecutive sessions, it suggests a potential bullish market shift."
	BreadthConfirmation string // "BULLISH_3SESSIONS" / "BEARISH_3SESSIONS" / "NEUTRAL"

	// Trend context
	NiftyROC21  float64 // 21-day momentum (one calendar month)
	OverheatPct float64 // how far Nifty is above SMA200

	// Sector strength
	StrongSectors []string
	WeakSectors   []string

	// Overall verdict (autonomous trading decision)
	Verdict    string // "BUY_AGGRESSIVELY" / "BUY_NORMALLY" / "WATCH_ONLY" / "STAY_OUT"
	VerdictMsg string // Human-readable explanation
}

// RunBirdsEyeView computes the full market health snapshot.
// Called once daily by the EOD scanner after market close.
// Returns a rich MarketHealthReport and sends it to Telegram.
func (s *ScannerAgent) RunBirdsEyeView() *MarketHealthReport {
	if s.DailyCache == nil || !s.DailyCache.Loaded {
		return nil
	}

	r := &MarketHealthReport{}

	// ── 1. Regime & Nifty indicators ─────────────────────────────────────────
	r.Regime = s.DetectRegime()

	niftyCloses, ok := s.DailyCache.Closes[config.NiftySpotToken]
	if ok && len(niftyCloses) > 0 {
		r.NiftyCurrent = niftyCloses[len(niftyCloses)-1]
		if s.GetLTP != nil {
			if ltp := s.GetLTP(config.NiftySpotToken); ltp > 0 {
				r.NiftyCurrent = ltp
			}
		}
	}

	if ok && len(niftyCloses) >= config.RegimeSMAPeriod {
		r.NiftySMA200 = computeSMA(niftyCloses, config.RegimeSMAPeriod)
		r.AboveSMA200 = r.NiftyCurrent > r.NiftySMA200
		if r.NiftySMA200 > 0 {
			r.OverheatPct = (r.NiftyCurrent - r.NiftySMA200) / r.NiftySMA200 * 100
		}
	}

	if ok && len(niftyCloses) >= config.EMA20Period+5 {
		ema10s := computeEMASeries(niftyCloses, config.EMA10Period)
		ema20s := computeEMASeries(niftyCloses, config.EMA20Period)
		if len(ema10s) > 0 {
			r.NiftyEMA10 = ema10s[len(ema10s)-1]
		}
		if len(ema20s) > 0 {
			r.NiftyEMA20 = ema20s[len(ema20s)-1]
		}
		r.EMA10AboveEMA20 = r.NiftyEMA10 > r.NiftyEMA20
	}

	r.NiftyROC21 = s.computeROC(config.NiftySpotToken, config.RegimeMomentumPeriod)

	// ── 2. Market Breadth ─────────────────────────────────────────────────────
	// Book Ch.10 p.255: "Look at what percentage of stocks are above their 20 and 50 EMAs.
	// If the majority are, it confirms strength. If most are below, it's a weak market."
	aboveEMA20Count := 0
	aboveEMA50Count := 0
	new52WHigh := 0
	new52WLow := 0
	total := 0

	for token, _ := range s.Universe {
		closes, ok := s.DailyCache.Closes[token]
		if !ok || len(closes) < config.EMA20Period+1 {
			continue
		}
		total++
		lastClose := closes[len(closes)-1]

		// % above EMA20
		ema20s := computeEMASeries(closes, config.EMA20Period)
		if len(ema20s) > 0 && lastClose > ema20s[len(ema20s)-1] {
			aboveEMA20Count++
		}

		// % above EMA50
		if len(closes) >= config.EMA50Period {
			ema50s := computeEMASeries(closes, config.EMA50Period)
			if len(ema50s) > 0 && lastClose > ema50s[len(ema50s)-1] {
				aboveEMA50Count++
			}
		}

		// Net new 52-week highs / lows
		// Book Ch.10 p.257: "More stocks making new highs than lows = expanding breadth"
		const lookback52W = 252
		window := closes
		if len(window) > lookback52W {
			window = window[len(window)-lookback52W:]
		}
		prevHigh52 := window[0]
		prevLow52 := window[0]
		for _, c := range window[:len(window)-1] { // exclude today
			if c > prevHigh52 {
				prevHigh52 = c
			}
			if c < prevLow52 {
				prevLow52 = c
			}
		}
		// Also include highs array for 52W high
		if h52, ok := s.DailyCache.High52W[token]; ok && h52 > prevHigh52 {
			prevHigh52 = h52
		}
		if lastClose >= prevHigh52 {
			new52WHigh++
		}
		if lastClose <= prevLow52 {
			new52WLow++
		}
	}

	r.TotalStocks = total
	if total > 0 {
		r.PctAboveEMA20 = float64(aboveEMA20Count) / float64(total) * 100
		r.PctAboveEMA50 = float64(aboveEMA50Count) / float64(total) * 100
	}
	r.New52WHigh = new52WHigh
	r.New52WLow = new52WLow
	r.NetNew52WHigh = new52WHigh - new52WLow

	// ── Book Ch.10 p.257: 3-session breadth confirmation ─────────────────────
	// Append today's NetNew52WHigh to history, keep last BreadthConfirmationSessions.
	// BULLISH if all N values > 0; BEARISH if all N values < 0; else NEUTRAL.
	s.NetNewHighsHistory = append(s.NetNewHighsHistory, r.NetNew52WHigh)
	if len(s.NetNewHighsHistory) > config.BreadthConfirmationSessions {
		s.NetNewHighsHistory = s.NetNewHighsHistory[len(s.NetNewHighsHistory)-config.BreadthConfirmationSessions:]
	}
	if len(s.NetNewHighsHistory) == config.BreadthConfirmationSessions {
		allPos, allNeg := true, true
		for _, v := range s.NetNewHighsHistory {
			if v <= 0 {
				allPos = false
			}
			if v >= 0 {
				allNeg = false
			}
		}
		switch {
		case allPos:
			r.BreadthConfirmation = "BULLISH_3SESSIONS"
		case allNeg:
			r.BreadthConfirmation = "BEARISH_3SESSIONS"
		default:
			r.BreadthConfirmation = "NEUTRAL"
		}
	} else {
		r.BreadthConfirmation = "WARMING_UP"
	}

	// AD Ratio from the live feed (if available) or computed from cache
	if s.GetADRatio != nil {
		r.ADRatio = s.GetADRatio()
	} else {
		r.ADRatio = s.ComputeADRatio()
	}

	// ── 3. Sector Strength ───────────────────────────────────────────────────
	// Book Ch.10 p.261: "Rotate into strong sectors; avoid weak ones."
	r.StrongSectors, r.WeakSectors = s.computeSectorStrength()

	// ── 4. Overall Verdict ───────────────────────────────────────────────────
	r.Verdict, r.VerdictMsg = computeMarketVerdict(r)

	// ── 5. Log & Telegram ────────────────────────────────────────────────────
	report := formatBirdsEyeReport(r)
	log.Printf("[BirdsEye] %s", strings.ReplaceAll(report, "\n", " | "))
	SendTelegram(report)

	return r
}

// computeSectorStrength returns strong and weak sectors based on their
// proximity to 52-week highs relative to the overall market.
func (s *ScannerAgent) computeSectorStrength() (strong, weak []string) {
	if s.DailyCache == nil {
		return
	}
	for name, sectorToken := range config.SectorTokens {
		closes, ok := s.DailyCache.Closes[sectorToken]
		if !ok || len(closes) < 100 {
			continue
		}
		current := closes[len(closes)-1]
		// Find 52-week high
		window := closes
		if len(window) > 252 {
			window = window[len(window)-252:]
		}
		high52 := window[0]
		for _, c := range window {
			if c > high52 {
				high52 = c
			}
		}
		if high52 <= 0 {
			continue
		}
		distFromHigh := (high52 - current) / high52 * 100
		roc21 := 0.0
		if len(closes) > 21 {
			past := closes[len(closes)-22]
			if past > 0 {
				roc21 = (current - past) / past * 100
			}
		}
		// Strong: within 5% of 52W high AND positive 21-day momentum
		if distFromHigh <= 5.0 && roc21 >= 2.0 {
			strong = append(strong, fmt.Sprintf("%s(+%.1f%%)", name, roc21))
		}
		// Weak: more than 15% below 52W high AND negative momentum
		if distFromHigh > 15.0 && roc21 < 0 {
			weak = append(weak, fmt.Sprintf("%s(%.1f%%)", name, roc21))
		}
	}
	return
}

// computeMarketVerdict returns the autonomous trading verdict.
// Book Ch.10 p.248: "Your number one job is to assess the health of the market
// before placing any trade. The market environment determines everything."
func computeMarketVerdict(r *MarketHealthReport) (verdict, msg string) {
	// Count positive/negative signals
	positives := 0
	negatives := 0

	if r.AboveSMA200 {
		positives++
	} else {
		negatives++
	}
	if r.EMA10AboveEMA20 {
		positives++
	} else {
		negatives++
	}
	if r.PctAboveEMA20 >= 60 {
		positives++
	} else if r.PctAboveEMA20 < 40 {
		negatives++
	}
	if r.NetNew52WHigh > 20 {
		positives++
	} else if r.NetNew52WHigh < -20 {
		negatives++
	}
	if r.ADRatio > 0.6 {
		positives++
	} else if r.ADRatio < 0.4 {
		negatives++
	}

	switch {
	case r.Regime == "DEFENSIVE":
		verdict = "STAY_OUT"
		msg = "🛑 Market in DEFENSIVE regime — no new buys. Protect capital."
	case negatives >= 3:
		verdict = "STAY_OUT"
		msg = fmt.Sprintf("🛑 STAY OUT: %d/5 breadth signals negative. Cash is a position.", negatives)
	case r.Regime == "AGGRESSIVE" && positives >= 4:
		verdict = "BUY_AGGRESSIVELY"
		msg = fmt.Sprintf("🚀 BUY AGGRESSIVELY: %d/5 signals positive. Deploy full capital.", positives)
	case positives >= 3:
		verdict = "BUY_NORMALLY"
		msg = fmt.Sprintf("✅ BUY NORMALLY: %d/5 signals positive. Selective entries only.", positives)
	default:
		verdict = "WATCH_ONLY"
		msg = "👁 WATCH ONLY: Mixed signals. Wait for clarity before committing capital."
	}
	return
}

// formatBirdsEyeReport generates the Telegram-formatted bird's eye view report.
func formatBirdsEyeReport(r *MarketHealthReport) string {
	ema10Str := "🔴"
	if r.EMA10AboveEMA20 {
		ema10Str = "🟢"
	}
	sma200Str := "🔴"
	if r.AboveSMA200 {
		sma200Str = "🟢"
	}

	breadth20Str := "🔴"
	if r.PctAboveEMA20 >= 60 {
		breadth20Str = "🟢"
	} else if r.PctAboveEMA20 >= 40 {
		breadth20Str = "🟡"
	}
	breadth50Str := "🔴"
	if r.PctAboveEMA50 >= 60 {
		breadth50Str = "🟢"
	} else if r.PctAboveEMA50 >= 40 {
		breadth50Str = "🟡"
	}

	newHighStr := "🔴"
	if r.NetNew52WHigh > 20 {
		newHighStr = "🟢"
	} else if r.NetNew52WHigh >= 0 {
		newHighStr = "🟡"
	}

	strongStr := "none"
	if len(r.StrongSectors) > 0 {
		strongStr = strings.Join(r.StrongSectors, ", ")
	}
	weakStr := "none"
	if len(r.WeakSectors) > 0 {
		weakStr = strings.Join(r.WeakSectors, ", ")
	}

	return fmt.Sprintf(`🦅 *BIRD'S EYE VIEW — Market Health Report*

*Regime:* `+"`"+`%s`+"`"+`
*Nifty:* `+"`"+`%.0f`+"`"+` | SMA200: `+"`"+`%.0f`+"`"+` %s | EMA10>EMA20: %s
*Overheat:* `+"`"+`%.1f%%`+"`"+` above SMA200 | 21d ROC: `+"`"+`%.1f%%`+"`"+`

📊 *Market Breadth:*
• Above EMA20: `+"`"+`%.1f%%`+"`"+` %s
• Above EMA50: `+"`"+`%.1f%%`+"`"+` %s
• Net New 52W Highs: `+"`"+`%+d`+"`"+` %s (High:%d / Low:%d)
• AD Ratio: `+"`"+`%.2f`+"`"+`

🏭 *Strong Sectors:* %s
⚠️ *Weak Sectors:* %s

%s`,
		r.Regime,
		r.NiftyCurrent, r.NiftySMA200, sma200Str, ema10Str,
		r.OverheatPct, r.NiftyROC21,
		r.PctAboveEMA20, breadth20Str,
		r.PctAboveEMA50, breadth50Str,
		r.NetNew52WHigh, newHighStr, r.New52WHigh, r.New52WLow,
		r.ADRatio,
		strongStr,
		weakStr,
		r.VerdictMsg,
	)
}

// ══════════════════════════════════════════════════════════════
//  Leg Counter — Book Ch.3 (p.50-55) "Rally Legs" + Ch.12 (p.281, 283)
//  Ch.3 p.53: "Swing traders do best when they catch a stock in its early
//   rally stages, usually within the first three rally legs."
//  Ch.12 p.281: "I usually avoid buying stocks that are above the second leg."
//  Ch.12 p.283: "I try to hold until the stock makes its third leg up."
//  The leg counter helps identify where we are in the move.
// ══════════════════════════════════════════════════════════════

// CountLegs counts the number of distinct up-legs in the recent trend.
// An up-leg is a sustained move from a swing low to a swing high.
// Returns (legCount, isThirdLegOrBeyond).
func CountLegs(closes []float64, lookbackBars int) (legCount int, isDangerous bool) {
	n := len(closes)
	if n < lookbackBars+5 {
		return 0, false
	}

	start := n - lookbackBars
	if start < 0 {
		start = 0
	}
	window := closes[start:]
	wLen := len(window)
	if wLen < 5 {
		return 0, false
	}

	// Detect swing lows and highs within the window
	inUpleg := false
	legLow := window[0]
	for i := 1; i < wLen-1; i++ {
		if !inUpleg {
			// Looking for a swing low to start a leg
			if window[i] < window[i-1] && window[i] < window[i+1] {
				legLow = window[i]
				inUpleg = true
			}
		} else {
			// In a leg — looking for a swing high to end it
			if window[i] > window[i-1] && window[i] > window[i+1] {
				// Valid leg: must gain at least 5% from the low
				gainPct := (window[i] - legLow) / legLow * 100
				if gainPct >= 5.0 {
					legCount++
				}
				inUpleg = false
			}
		}
	}

	isDangerous = legCount >= 3
	return legCount, isDangerous
}
