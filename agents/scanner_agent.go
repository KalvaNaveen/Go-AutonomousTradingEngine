package agents

import (
	"log"
	"math"
	"time"

	"bnf_go_engine/config"
)

// ══════════════════════════════════════════════════════════════
//  Signal — represents a trade entry signal
// ══════════════════════════════════════════════════════════════

type Signal struct {
	Strategy    string
	Symbol      string
	Token       uint32
	Regime      string
	EntryPrice  float64
	StopPrice   float64
	TargetPrice float64 // 0 = no fixed target (trailing exit)
	Qty         int
	Product     string // "CNC" for swing delivery
	IsShort     bool
	// Book Ch.12: large base setups trail with 50 EMA (not 20 EMA)
	// VCP (120-day lookback) = large base; EMA crossover = mini base
	IsLargeBase bool
}

// ══════════════════════════════════════════════════════════════
//  Candle + DailyCache (data containers)
// ══════════════════════════════════════════════════════════════

type Candle struct {
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume int64
}

type DailyCache struct {
	ATR          map[uint32]float64
	EMA10        map[uint32]float64 // Fast EMA — crossover entry
	EMA20        map[uint32]float64 // Trend EMA — confirmation + exit
	Opens        map[uint32][]float64 // Daily open prices — used by backtest for realistic entry
	Closes       map[uint32][]float64
	Highs        map[uint32][]float64
	Lows         map[uint32][]float64
	Volumes      map[uint32][]float64
	AvgVol       map[uint32]float64
	TurnoverCr   map[uint32]float64
	PivotSupport map[uint32]float64
	High52W      map[uint32]float64
	RSScore      map[uint32]int
	TradingDates []string // NSE trading dates aligned with Closes arrays (YYYY-MM-DD)
	Loaded       bool
}

// ══════════════════════════════════════════════════════════════
//  ScannerAgent — Harsh's Swing Trading Scanner
// ══════════════════════════════════════════════════════════════

type ScannerAgent struct {
	Universe       map[uint32]string
	TokenToCompany map[uint32]string

	// Live data feeds
	GetLTP       func(uint32) float64
	GetVWAP      func(uint32) float64
	GetVolume    func(uint32) int64
	GetDepth     func(uint32) map[string]float64
	GetCandles5m func(uint32) []Candle
	GetORB       func(uint32) (float64, float64)
	GetDayOpen   func(uint32) float64
	ComputeRVol  func(uint32) float64
	GetIndiaVIX  func() float64
	GetADRatio   func() float64

	// Precomputed daily data
	DailyCache *DailyCache


	// State
	LastRegime          string
	ConsecutiveSLs      int
	CapitalMultiplier   float64
	IsMajorEventDay     bool              // Section V.2: suppresses Bull Flag on major event days
	MajorEventName      string            // Name of the event if active
	IPOSymbols          map[string]bool    // FIX-08: Section V.3: only apply IPO Base to actual recent IPOs
	// Book Ch.8 p.196: sector concentration cap — token → sector name (e.g. "IT", "PHARMA").
	// Populated by DataAgent when loading universe. Empty = skip sector check.
	TokenSector         map[uint32]string

	// Book Ch.10 p.257: Net New 52W Highs history for 3-session confirmation.
	// Most-recent value at index len-1. Trimmed to last BreadthConfirmationSessions entries
	// by RunBirdsEyeView. Persistence: rebuilt at startup from the cached breadth history.
	NetNewHighsHistory []int
}

func NewScannerAgent() *ScannerAgent {
	return &ScannerAgent{
		CapitalMultiplier: 1.0,
	}
}

// ══════════════════════════════════════════════════════════════
//  Phase 1: Market Timing — ROC-Based Regime Detection
// ══════════════════════════════════════════════════════════════
//  Track Nifty 50 ROC (length 18) and Smallcap 100 ROC (length 20).
//  ROC near 0 = buy aggressively. ROC near 45/100 = reduce equity.
//  5 consecutive SL hits = reduce capital to 30-40%.

// DetectRegime classifies the current market environment using the 200-day SMA
// of the Nifty 50 index — the primary filter from Faber (2007).
//
//   DEFENSIVE      : Nifty 50 below its 200-day SMA (long-term downtrend confirmed).
//                    Reinforced when Nifty Smallcap 100 is also below its 200-SMA.
//   NORMAL         : Nifty 50 above 200-SMA, 21-day momentum not yet strong.
//   AGGRESSIVE     : Nifty 50 above 200-SMA AND 21-day ROC > 5% (momentum confirmed).
//   REDUCED_CAPITAL: Nifty above 200-SMA but 5+ consecutive SL hits — trade small.
func (s *ScannerAgent) DetectRegime() string {
	if s.DailyCache == nil || !s.DailyCache.Loaded {
		return "UNKNOWN"
	}

	niftyCloses, ok := s.DailyCache.Closes[config.NiftySpotToken]
	if !ok || len(niftyCloses) < config.RegimeSMAPeriod {
		return "NORMAL" // insufficient data → assume safe
	}

	// Current Nifty price — prefer live LTP so regime reflects intraday moves
	niftyCurrent := niftyCloses[len(niftyCloses)-1]
	if s.GetLTP != nil {
		if ltp := s.GetLTP(config.NiftySpotToken); ltp > 0 {
			niftyCurrent = ltp
		}
	}

	niftySMA200 := computeSMA(niftyCloses, config.RegimeSMAPeriod)
	candidate := "NORMAL"

	// ── DEFENSIVE: Nifty below its 200-day SMA ───────────────────────────────
	if niftySMA200 > 0 && niftyCurrent < niftySMA200 {
		candidate = "DEFENSIVE"
	}

	// ── Reinforce DEFENSIVE: Smallcap also below its 200-SMA ─────────────────
	// Both large-cap and small-cap in downtrend = broad market weakness.
	if candidate != "DEFENSIVE" {
		if scCloses, ok := s.DailyCache.Closes[config.SmallcapToken]; ok && len(scCloses) >= config.RegimeSMAPeriod {
			scCurrent := scCloses[len(scCloses)-1]
			if s.GetLTP != nil {
				if ltp := s.GetLTP(config.SmallcapToken); ltp > 0 {
					scCurrent = ltp
				}
			}
			scSMA200 := computeSMA(scCloses, config.RegimeSMAPeriod)
			if scSMA200 > 0 && scCurrent < scSMA200 {
				candidate = "DEFENSIVE"
			}
		}
	}

	// ── AGGRESSIVE: Nifty above 200-SMA AND 21-day momentum > 5% ─────────────
	// 21-day = one calendar month of trading days; 5% monthly gain = strong uptrend.
	// Book Ch.10 p.253: "When BOTH Nifty 50 AND Smallcap 100 are above their EMAs
	// and trending up together, it's a broad-based bull — deploy aggressively."
	// Requirement: Nifty ROC > 5% AND Smallcap EMA10 > EMA20 (broad participation).
	if candidate == "NORMAL" {
		roc21 := s.computeROC(config.NiftySpotToken, config.RegimeMomentumPeriod)
		if roc21 >= config.RegimeMomentumThreshold {
			// Verify Smallcap 100 is also in bullish EMA alignment
			smallcapBullish := false
			if scCloses, ok := s.DailyCache.Closes[config.SmallcapToken]; ok && len(scCloses) >= config.EMA20Period+5 {
				sc10s := computeEMASeries(scCloses, config.EMA10Period)
				sc20s := computeEMASeries(scCloses, config.EMA20Period)
				if len(sc10s) > 0 && len(sc20s) > 0 && sc10s[len(sc10s)-1] >= sc20s[len(sc20s)-1] {
					smallcapBullish = true
				}
			} else {
				smallcapBullish = true // No data — give benefit of the doubt
			}
			if smallcapBullish {
				candidate = "AGGRESSIVE"
			} else {
				log.Printf("[Regime] Nifty ROC=%.1f%% qualifies AGGRESSIVE but Smallcap EMA bearish — staying NORMAL",
					roc21)
			}
		}
	}

	// ── Book Ch.10: The 10 & 20 Rule (p.252-253) ─────────────────────────────
	// "Wait for the relevant indices to trade above the 10 and 20 EMAs,
	//  where the 10 EMA should be positioned above the 20 EMA."
	// More responsive than SMA200 — triggers earlier on market turns.
	// Applied AFTER SMA200 check: EMA10 < EMA20 overrides NORMAL/AGGRESSIVE → DEFENSIVE.
	if candidate != "DEFENSIVE" && len(niftyCloses) >= config.EMA20Period+5 {
		ema10s := computeEMASeries(niftyCloses, config.EMA10Period)
		ema20s := computeEMASeries(niftyCloses, config.EMA20Period)
		if len(ema10s) > 0 && len(ema20s) > 0 {
			niftyEMA10 := ema10s[len(ema10s)-1]
			niftyEMA20 := ema20s[len(ema20s)-1]
			if niftyEMA10 < niftyEMA20 {
				candidate = "DEFENSIVE"
				log.Printf("[Regime] 10&20 Rule: Nifty EMA10(%.0f) < EMA20(%.0f) → DEFENSIVE",
					niftyEMA10, niftyEMA20)
			}
		}
	}

	// ── Book Ch.10: Overheated market guard (p.258) ───────────────────────────
	// "Go defensive when market is over-heated / euphoric."
	// Operationalised: Nifty > 20% above SMA200 = extended/euphoric territory.
	// Downgrade AGGRESSIVE → NORMAL to reduce new position sizing.
	if candidate == "AGGRESSIVE" && niftySMA200 > 0 {
		overextension := (niftyCurrent - niftySMA200) / niftySMA200 * 100
		if overextension >= 20.0 {
			candidate = "NORMAL"
			log.Printf("[Regime] OVERHEATED: Nifty %.0f%% above SMA200 → downgraded AGGRESSIVE→NORMAL",
				overextension)
		}
	}

	// ── Contingency: risk management override (not market timing) ────────────
	// 5 consecutive SL hits → reduce position size regardless of market regime.
	if s.ConsecutiveSLs >= config.ConsecutiveSLCutoff {
		s.CapitalMultiplier = config.ReducedCapitalPct
		log.Printf("[Regime] CONTINGENCY: %d consecutive SL hits → capital reduced to %.0f%%",
			s.ConsecutiveSLs, s.CapitalMultiplier*100)
		if candidate != "DEFENSIVE" {
			candidate = "REDUCED_CAPITAL"
		}
	}

	if candidate != s.LastRegime {
		log.Printf("[Regime]  → %s (Nifty=%.0f SMA200=%.0f ConsecSL=%d)",
			candidate, niftyCurrent, niftySMA200, s.ConsecutiveSLs)
		s.LastRegime = candidate
	}
	if s.LastRegime == "" {
		s.LastRegime = candidate
	}
	return s.LastRegime
}

// computeROC calculates Rate of Change: ((current - past) / past) * 100
// Uses live LTP as "current" when available so regime reflects intraday index moves,
// not just the prior-day close loaded at startup.
func (s *ScannerAgent) computeROC(token uint32, length int) float64 {
	closes, ok := s.DailyCache.Closes[token]
	if !ok || len(closes) < length+1 {
		return 0
	}
	current := closes[len(closes)-1]
	if s.GetLTP != nil {
		if ltp := s.GetLTP(token); ltp > 0 {
			current = ltp
		}
	}
	past := closes[len(closes)-1-length]
	if past <= 0 {
		return 0
	}
	return ((current - past) / past) * 100
}

// RecordSLHit increments consecutive SL counter and applies progressive exposure reduction.
// Book Ch.8 p.202-203: "If recent trades are unprofitable, reduce your risk per trade."
// Progressive ladder: 3 consecutive losses → reduce to 50%; 5 → 35% (contingency).
func (s *ScannerAgent) RecordSLHit() {
	s.ConsecutiveSLs++
	switch {
	case s.ConsecutiveSLs >= config.ConsecutiveSLCutoff:
		// Hard contingency: 5+ SL hits → 35% capital
		if s.CapitalMultiplier > config.ReducedCapitalPct {
			s.CapitalMultiplier = config.ReducedCapitalPct
			log.Printf("[Scanner] CONTINGENCY: %d consecutive SLs → capital at %.0f%%",
				s.ConsecutiveSLs, s.CapitalMultiplier*100)
		}
	case s.ConsecutiveSLs >= 3:
		// Book Ch.8: 3 consecutive losses → halve risk (0.5× multiplier)
		if s.CapitalMultiplier > 0.50 {
			s.CapitalMultiplier = 0.50
			log.Printf("[Scanner] Progressive exposure: %d consecutive SLs → capital at 50%%",
				s.ConsecutiveSLs)
		}
	}
}

// RecordWin resets the consecutive SL counter and begins progressive recovery.
// Book Ch.8 p.203: "If recent trades are profitable, gradually increase your risk."
func (s *ScannerAgent) RecordWin() {
	s.ConsecutiveSLs = 0
	// Progressive recovery — don't snap back to 100% immediately
	// Let the execution agent's graduated recovery ladder handle the step-ups.
	// Here we simply clear the SL streak so the multiplier can be stepped up.
	if s.CapitalMultiplier >= 1.0 {
		s.CapitalMultiplier = 1.0 // already at full — no change
	}
}

func (s *ScannerAgent) NewSession() {
	// Daily reset — regime persists across days, only reset session-specific state
}

// ══════════════════════════════════════════════════════════════
//  Phase 2 + 3: RunAllScans — Sector Leaders + VCP Breakout
// ══════════════════════════════════════════════════════════════

func (s *ScannerAgent) RunAllScans(regime string) []*Signal {
	if s.DailyCache == nil || !s.DailyCache.Loaded {
		return nil
	}

	if regime == "DEFENSIVE" {
		log.Printf("[Scanner] DEFENSIVE regime — scan suppressed")
		return nil
	}

	// ── Book Ch.5 p.131: Market open noise filter ────────────────────────────
	// "Avoid the first 30 minutes — fake breakouts dominate at the open."
	{
		now := config.NowIST()
		openH, openM := config.ParseTime(config.MarketOpenTime)
		noiseEnd := time.Date(now.Year(), now.Month(), now.Day(),
			openH, openM+config.MarketOpenNoiseMins, 0, 0, now.Location())
		if now.Before(noiseEnd) {
			log.Printf("[Scanner] Market open noise window (first %d min) — scan suppressed", config.MarketOpenNoiseMins)
			return nil
		}
	}

	// Position-size multiplier (book: progressive risk via consecutive wins/losses, Ch.8 p.203).
	effectiveCapMult := s.CapitalMultiplier

	// ── Book Ch.5 p.131: F&O expiry week caution ─────────────────────────────
	// "During F&O expiry week (week of last Thursday), volatility spikes and
	//  stops get hit arbitrarily. Reduce new entries or tighten size."
	if isExpiryWeek() {
		effectiveCapMult *= 0.5
		log.Printf("[Scanner] F&O expiry week — halving position sizes (CapMult=%.0f%%)", effectiveCapMult*100)
	}

	// Section III.2: Detect strong sectors during sideways markets
	strongSectors := s.GetStrongSectors()
	if len(strongSectors) > 0 {
		log.Printf("[Scanner] Strong sectors: %v", strongSectors)
	}

	ctx := StrategyContext{
		Cache:             s.DailyCache,
		CapitalMultiplier: effectiveCapMult, // VIX-adjusted multiplier
		GetVolume:         s.GetVolume,
		IPOSymbols:        s.IPOSymbols,
		IsMajorEventDay:   s.IsMajorEventDay,
	}
	strategies := AllStrategies()

	var signals []*Signal

	// Diagnostic gate counters — logged every scan so you can see exactly
	// which filter is eliminating stocks on any given day.
	var (
		total         int
		skipNoLTP     int
		skipLowPrice  int // Book Ch.11: CMP < ₹30 filter
		skipLiquidity int
		skipATH       int
		skipThirdLeg  int // Book Ch.3/12: skip if stock in 3rd leg or beyond
		skipGapUp     int // Book Ch.5: skip gap-ups > 3%
		skipNoSignal    int
	)

	for token, symbol := range s.Universe {
		total++
		ltp := 0.0
		if s.GetLTP != nil {
			ltp = s.GetLTP(token)
		}
		if ltp <= 0 {
			skipNoLTP++
			continue
		}

		// Book Ch.11: Minimum price filter — "CMP > Number 30" (p.267)
		// Filters out penny / micro-cap stocks with wide spreads and low float.
		if ltp < config.MinStockPrice {
			skipLowPrice++
			continue
		}

		// Liquidity gate: skip stocks below ₹5Cr/day avg turnover
		if turn, ok := s.DailyCache.TurnoverCr[token]; ok && turn < config.LiquidityFilterCr {
			skipLiquidity++
			continue
		}

		// Near ATH filter (52-week high — see passesPhase2Filter)
		if !s.passesPhase2Filter(token, ltp) {
			skipATH++
			continue
		}


		// ── Book Ch.5 p.130: Gap-up filter — skip if gapped > 3% from prior close ──
		// "A stock that gaps up more than 3% at the open is already extended —
		//  the breakout happened overnight, not at a clean level. Wait or skip."
		if opens, oOk := s.DailyCache.Opens[token]; oOk && len(opens) >= 1 {
			closes2, c2Ok := s.DailyCache.Closes[token]
			if c2Ok && len(closes2) >= 2 {
				todayOpen := opens[len(opens)-1]
				priorClose := closes2[len(closes2)-2]
				if priorClose > 0 && todayOpen > 0 {
					gapPct := (todayOpen - priorClose) / priorClose * 100
					if gapPct > config.GapUpFilterPct {
						skipGapUp++
						continue
					}
				}
			}
		}

		// ── Book Ch.3 p.50-55 + Ch.12 p.281,283: Leg counter — skip 3rd-leg signals ───────
		// Ch.3: "Swing traders do best when they catch a stock in its early rally stages,
		//  usually within the first three rally legs."
		// Ch.12: "I usually avoid buying stocks that are above the second leg." Protect
		// capital by staying early in the move.
		if closes, ok := s.DailyCache.Closes[token]; ok {
			_, isDangerous := CountLegs(closes, config.LegCounterLookback)
			if isDangerous {
				skipThirdLeg++
				continue
			}
		}

		// Run all standalone strategy agents in priority order — first signal wins
		matched := false
		for _, strat := range strategies {
			if sig := strat.Detect(token, symbol, ltp, regime, ctx); sig != nil {
				signals = append(signals, sig)
				matched = true
				break
			}
		}
		if !matched {
			skipNoSignal++
		}
	}

	log.Printf("[Scanner] universe=%d noLTP=%d lowPrice=%d liquidity=%d ATH=%d gapUp=%d thirdLeg=%d noSignal=%d signals=%d",
		total, skipNoLTP, skipLowPrice, skipLiquidity, skipATH, skipGapUp, skipThirdLeg, skipNoSignal, len(signals))

	// ── Rank signals by conviction before returning ───────────────────────
	// Book Ch.2-3: volume expansion confirms a breakout. Rank by volume spike
	// (today's live volume / 20-day average) — strongest breakout first.
	if len(signals) > 1 {
		type scored struct {
			sig   *Signal
			score float64
		}
		ranked := make([]scored, len(signals))
		for i, sig := range signals {
			volSpike := 1.0
			if s.GetVolume != nil && s.DailyCache != nil {
				if vols, ok := s.DailyCache.Volumes[sig.Token]; ok && len(vols) >= 21 {
					var avg float64
					for _, v := range vols[len(vols)-21 : len(vols)-1] {
						avg += v
					}
					avg /= 20
					if avg > 0 {
						volSpike = float64(s.GetVolume(sig.Token)) / avg
					}
				}
			}
			ranked[i] = scored{sig: sig, score: volSpike}
		}

		// Stable insertion sort (signal count is small — typically <50)
		for i := 1; i < len(ranked); i++ {
			for j := i; j > 0 && ranked[j].score > ranked[j-1].score; j-- {
				ranked[j], ranked[j-1] = ranked[j-1], ranked[j]
			}
		}
		for i, r := range ranked {
			signals[i] = r.sig
		}

		log.Printf("[Scanner] ranked %d signals by volume-spike conviction", len(signals))
	}

	return signals
}


// computeCMF calculates Chaikin Money Flow over the last `period` bars.
// Retained only as a library helper for the backtest validation tool; not used by
// the live book-pure trading path (CMF is not a book indicator).
// CLV = (2×Close − High − Low) / (High − Low); ranges −1 to +1.
// CMF = Σ(CLV×Volume) / Σ(Volume). Source: Chaikin Analytics (the indicator's creator).
func computeCMF(closes, highs, lows, volumes []float64, period int) float64 {
	n := len(closes)
	if n < period || len(highs) < period || len(lows) < period || len(volumes) < period {
		return 0
	}
	var sumMFV, sumVol float64
	for i := n - period; i < n; i++ {
		h := highs[i]
		l := lows[i]
		rng := h - l
		if rng <= 0 || volumes[i] <= 0 {
			continue
		}
		clv := (2*closes[i] - h - l) / rng
		sumMFV += clv * volumes[i]
		sumVol += volumes[i]
	}
	if sumVol == 0 {
		return 0
	}
	return sumMFV / sumVol
}

// computeSMA calculates the simple moving average of the last `period` values.
func computeSMA(closes []float64, period int) float64 {
	if len(closes) < period {
		return 0
	}
	sum := 0.0
	for _, c := range closes[len(closes)-period:] {
		sum += c
	}
	return sum / float64(period)
}

// computeEMASeries returns the full EMA series for the given period using standard
// exponential smoothing: seed from first-N SMA, then apply multiplier forward.
func computeEMASeries(closes []float64, period int) []float64 {
	if len(closes) < period {
		return nil
	}
	result := make([]float64, len(closes))
	k := 2.0 / float64(period+1)
	// Seed: SMA of first `period` closes
	seed := 0.0
	for i := 0; i < period; i++ {
		seed += closes[i]
	}
	result[period-1] = seed / float64(period)
	for i := period; i < len(closes); i++ {
		result[i] = closes[i]*k + result[i-1]*(1-k)
	}
	// Return only the non-zero tail
	return result[period-1:]
}

// ══════════════════════════════════════════════════════════════
//  Phase 2: Stock Identification — Near ATH + High RS
// ══════════════════════════════════════════════════════════════
//  Blueprint: "Always buy at or near All-Time Highs."
//  Uses full available history (500 days) as ATH proxy.

func (s *ScannerAgent) passesPhase2Filter(token uint32, ltp float64) bool {
	// Blueprint says "Always buy at or near All-Time Highs."
	// Practical interpretation: use 52-week high (last 252 trading bars) as the ATH
	// reference, not the full 500-day cache. Using the 2-year peak would eliminate
	// ~90% of the universe after any correction, producing zero signals.
	// 52-week high is the industry-standard "near ATH" reference on NSE/BSE screeners.
	closes, cOk := s.DailyCache.Closes[token]
	if !cOk || len(closes) == 0 {
		return false
	}

	const lookback52W = 252 // ≈ 1 trading year
	window := closes
	if len(window) > lookback52W {
		window = window[len(window)-lookback52W:]
	}

	high52 := window[0]
	for _, c := range window {
		if c > high52 {
			high52 = c
		}
	}

	// Also check highs array (intraday peaks) within the same 52-week window
	if highs, hOk := s.DailyCache.Highs[token]; hOk {
		hWindow := highs
		if len(hWindow) > lookback52W {
			hWindow = hWindow[len(hWindow)-lookback52W:]
		}
		for _, h := range hWindow {
			if h > high52 {
				high52 = h
			}
		}
	}

	if high52 <= 0 {
		return false
	}
	distFrom52WH := ((high52 - ltp) / high52) * 100
	if distFrom52WH > config.ATHProximityPct {
		return false
	}

	// ── Book Ch.11 p.267: CMP > EMA20 — primary scan condition (Figure 11.1) ──
	// "CMP > EMA 20" is the first and most important condition in the EMA Scan.
	// A stock below its 20 EMA is in short-term downtrend — skip regardless of pattern.
	if len(closes) >= config.EMA20Period {
		ema20scan := computeEMASeries(closes, config.EMA20Period)
		if len(ema20scan) > 0 {
			ema20val := ema20scan[len(ema20scan)-1]
			if ema20val > 0 && ltp <= ema20val {
				return false // CMP below EMA20 — fails primary scan condition
			}
		}
	}

	// ── Book Ch.3: Extension filter — 50 EMA (p.49-52) ───────────────────────
	// "If a stock is 30–35% away from the 50 EMA, don't buy."
	// Stocks this far from their 50 EMA are parabolic — risk-reward is unfavourable.
	ema50 := 0.0
	if len(closes) >= config.EMA50Period {
		ema50s := computeEMASeries(closes, config.EMA50Period)
		if len(ema50s) > 0 {
			ema50 = ema50s[len(ema50s)-1]
			if ema50 > 0 && ltp > ema50*(1+config.Extension50EMAPct/100) {
				return false // Too extended above 50 EMA — parabolic, skip
			}
		}
	}

	// ── Book Ch.3 p.38: Per-stock SMA200 — only buy stocks above long-term trend ──
	// "If a stock is below its 200-day SMA it is in a long-term downtrend —
	//  avoid regardless of short-term pattern."
	if len(closes) >= config.RegimeSMAPeriod {
		sma200 := computeSMA(closes, config.RegimeSMAPeriod)
		if sma200 > 0 && ltp < sma200 {
			return false
		}
	}

	// ── Book Ch.5 p.125: EMA alignment — EMA10 > EMA20 > EMA50 ───────────────
	// "The stock must be in a proper bullish structure: fast EMA above slow EMA
	//  above trend EMA. Any inversion means momentum is degrading — skip."
	// Tolerance of 0.1% avoids rejecting stocks where EMAs are nearly equal
	// (flat consolidations, or floating-point noise on long flat series).
	const emaAlignTol = 0.001 // 0.1%
	if ema50 > 0 && len(closes) >= config.EMA20Period+5 {
		ema10s := computeEMASeries(closes, config.EMA10Period)
		ema20s := computeEMASeries(closes, config.EMA20Period)
		if len(ema10s) > 0 && len(ema20s) > 0 {
			ema10 := ema10s[len(ema10s)-1]
			ema20 := ema20s[len(ema20s)-1]
			// Reject only when strictly inverted beyond noise threshold
			if ema10 < ema20*(1-emaAlignTol) || ema20 < ema50*(1-emaAlignTol) {
				return false // EMAs clearly inverted — stock not in bullish structure
			}
		}
	}

	// ── Book Ch.11 p.268: Weekly scan — price > 10W EMA AND 10W EMA > 30W EMA ──
	// "Look for stocks that are trading above their 10-week EMA, and the 10 EMA
	//  is trading above the 30 EMA."
	// The 30W EMA filters out names not bullish on the longer time frame;
	// the 10W EMA filters out names not strong enough for swing trading.
	// Both conditions must hold; either failure = skip the daily setup.
	weeklyCloses := deriveWeeklyCloses(closes, s.DailyCache.TradingDates)
	if len(weeklyCloses) >= config.Weekly30EMAPeriod {
		w10emas := computeEMASeries(weeklyCloses, config.Weekly10EMAPeriod)
		w30emas := computeEMASeries(weeklyCloses, config.Weekly30EMAPeriod)
		if len(w10emas) > 0 && len(w30emas) > 0 {
			lastWeeklyClose := weeklyCloses[len(weeklyCloses)-1]
			weekly10EMA := w10emas[len(w10emas)-1]
			weekly30EMA := w30emas[len(w30emas)-1]
			if weekly10EMA > 0 && weekly30EMA > 0 {
				// Same 0.1% tolerance as the daily EMA alignment check above — avoids
				// false rejections on flat consolidations and floating-point noise.
				if lastWeeklyClose < weekly10EMA*(1-emaAlignTol) || weekly10EMA < weekly30EMA*(1-emaAlignTol) {
					return false // Weekly chart not in confirmed uptrend (Ch.11 p.268)
				}
			}
		}
	}

	// (RS-percentile rank filter removed — the book uses the Monthly Gainers % scan
	//  for relative strength, not a 1-99 RS percentile rank.)

	// ── Book Ch.4 p.133: Big Down Day red flag ────────────────────────────────
	// "A stock that has a big down day (≥5% single-day decline) in its recent base
	//  needs 5–10 sessions to digest the selling. Skip until it stabilises."
	// Volume confirmation: if volume data is aligned, require above-average volume
	// (0.8× avg) to confirm institutional selling — else flag on price alone.
	nCloses := len(closes)
	if nCloses > config.BigDownDaySkipBars+1 {
		volumes, vOk := s.DailyCache.Volumes[token]
		checkFrom := nCloses - config.BigDownDaySkipBars
		for i := checkFrom; i < nCloses; i++ {
			if closes[i-1] <= 0 {
				continue
			}
			dayChg := (closes[i] - closes[i-1]) / closes[i-1] * 100
			if dayChg <= -config.BigDownDayPct {
				// Volume check: if data available and aligned, require above-avg
				volConfirmed := true
				if vOk && len(volumes) == nCloses {
					avgStart := i - 20
					if avgStart < 0 {
						avgStart = 0
					}
					if i > avgStart {
						avgVol := 0.0
						for _, v := range volumes[avgStart:i] {
							avgVol += v
						}
						avgVol /= float64(i - avgStart)
						// Require ≥ 80% of avg volume to confirm institutional selling
						volConfirmed = avgVol <= 0 || volumes[i] >= avgVol*0.8
					}
				}
				if volConfirmed {
					return false // Big down day in base — skip (Ch.4 p.133)
				}
			}
		}
	}

	// ── Book Ch.4 p.135-136: Rejection Candle red flag ───────────────────────
	// "A rejection candle (upper wick ≥ 60% of total range) signals heavy supply
	//  at that level. Skip for 5–10 days OR until price reclaims the rejection high."
	// Candle must have a meaningful range (≥ 1% of close) to ignore doji/flat days.
	highs, hOk := s.DailyCache.Highs[token]
	lows, lOk := s.DailyCache.Lows[token]
	if hOk && lOk && len(highs) == nCloses && len(lows) == nCloses && nCloses >= config.RejectionSkipBars {
		rejectionHigh := 0.0
		checkFrom := nCloses - config.RejectionSkipBars
		for i := checkFrom; i < nCloses; i++ {
			totalRange := highs[i] - lows[i]
			if closes[i] <= 0 || totalRange < closes[i]*config.RejectionMinRange {
				continue // Doji or flat day — not a meaningful rejection
			}
			upperWick := highs[i] - closes[i]
			if upperWick/totalRange >= config.RejectionWickRatio {
				if highs[i] > rejectionHigh {
					rejectionHigh = highs[i] // Track the highest rejection level
				}
			}
		}
		// Block entry until price reclaims the rejection high (supply absorbed)
		if rejectionHigh > 0 && ltp < rejectionHigh {
			return false // Rejection candle not reclaimed — skip (Ch.4 p.135-136)
		}
	}

	// ── Book Ch.11 p.269 (Fig 11.2): Monthly Gainers / MOMO momentum filter ───
	// "Stocks that exhibit the strongest performance within a specific time period."
	// % Change from lows over 10/30/90/180 day windows (Kullamagi-style scan).
	// Skip-safe: only enforced when enough history is available.
	if !passesMonthlyGainersScan(closes) {
		return false
	}

	// ── Book Ch.11 p.271 (Fig 11.4): Tight Range scan ─────────────────────────
	// "Stocks in strong momentum currently forming a tight range or Range Contraction."
	// Today's % change ≤ 2.5%, previous day's ≤ 3.5% (both absolute).
	if !passesTightRangeScan(closes) {
		return false
	}

	// ── Book Ch.4 p.127 + p.137: Base-quality enhancers ───────────────────────
	// (a) Higher Low in the Base (p.127): "stock's price keeps forming higher lows
	//     ... confident investors are buying and holding."
	// (b) Breakout Attempts (p.137): "Focus on bases where the stock has made an
	//     attempt to breakout at least once or twice."
	if highs2, hOk2 := s.DailyCache.Highs[token]; hOk2 {
		if lows2, lOk2 := s.DailyCache.Lows[token]; lOk2 {
			if !hasHigherLowInBase(lows2) {
				return false
			}
			if !hasPriorBreakoutAttempt(highs2) {
				return false
			}
		}
	}

	// ── Book Ch.4 p.121: Pocket Pivot quality enhancer ────────────────────────
	// "When buy signals happen close to a pocket pivot, they're usually trustworthy."
	// Engine policy: require ≥1 pocket pivot in the recent base. This enforces the
	// "institutional footprint" rule the book identifies as a quality marker.
	// Skip-safe: only applied when volume data is aligned with closes.
	if volumes2, vOk2 := s.DailyCache.Volumes[token]; vOk2 && len(volumes2) == nCloses {
		if !hasRecentPocketPivot(closes, volumes2) {
			return false
		}
	}

	return true
}

// ══════════════════════════════════════════════════════════════
//  Book 1:1 Replica — helpers for the 10 gap-fix filters
// ══════════════════════════════════════════════════════════════

// passesMonthlyGainersScan implements Book Ch.11 p.269 (Fig 11.2).
// Returns true if the stock satisfies ALL four % change thresholds, or if
// there is not enough history to evaluate a window (skip-safe).
func passesMonthlyGainersScan(closes []float64) bool {
	if len(closes) == 0 {
		return true // skip-safe
	}
	cmp := closes[len(closes)-1]
	if cmp <= 0 {
		return true
	}
	checks := []struct {
		window int
		minPct float64
	}{
		{10, config.MonthlyGainers10DayMinPct},
		{30, config.MonthlyGainers30DayMinPct},
		{90, config.MonthlyGainers90DayMinPct},
		{180, config.MonthlyGainers180DayMinPct},
	}
	for _, c := range checks {
		if len(closes) < c.window {
			continue // not enough history — skip this check
		}
		low := closes[len(closes)-c.window]
		for _, v := range closes[len(closes)-c.window:] {
			if v < low && v > 0 {
				low = v
			}
		}
		if low <= 0 {
			continue
		}
		pctFromLow := (cmp - low) / low * 100
		if pctFromLow < c.minPct {
			return false
		}
	}
	return true
}

// passesTightRangeScan implements Book Ch.11 p.271 (Fig 11.4).
// Requires today's % change in [-2.5%, +2.5%] AND previous day's in [-3.5%, +3.5%].
func passesTightRangeScan(closes []float64) bool {
	if len(closes) < 3 {
		return true // skip-safe
	}
	n := len(closes)
	today, todayPrev := closes[n-1], closes[n-2]
	yesterday, yPrev := closes[n-2], closes[n-3]
	if todayPrev <= 0 || yPrev <= 0 {
		return true
	}
	todayPct := (today - todayPrev) / todayPrev * 100
	yPct := (yesterday - yPrev) / yPrev * 100
	if todayPct > config.TightRangeTodayMaxPct || todayPct < -config.TightRangeTodayMaxPct {
		return false
	}
	if yPct > config.TightRangeYesterdayMaxPct || yPct < -config.TightRangeYesterdayMaxPct {
		return false
	}
	return true
}

// hasRecentPocketPivot implements Book Ch.4 p.121.
// Pocket pivot = up day with volume > highest down-day volume in past 10 days.
// Returns true if at least one pocket pivot occurred in the last
// PocketPivotSignalDays bars, OR if no down-day volume exists (skip-safe).
func hasRecentPocketPivot(closes, volumes []float64) bool {
	n := len(closes)
	if n < config.PocketPivotLookback+config.PocketPivotSignalDays || len(volumes) != n {
		return true // skip-safe — not enough history
	}
	// Look at the last PocketPivotSignalDays bars for any pocket pivot
	startSignal := n - config.PocketPivotSignalDays
	for i := startSignal; i < n; i++ {
		if i == 0 || closes[i] <= closes[i-1] {
			continue // not an up day
		}
		// Find highest down-day volume in past 10 days BEFORE this bar
		windowStart := i - config.PocketPivotLookback
		if windowStart < 1 {
			windowStart = 1
		}
		maxDownVol := 0.0
		for j := windowStart; j < i; j++ {
			if closes[j] < closes[j-1] && volumes[j] > maxDownVol {
				maxDownVol = volumes[j]
			}
		}
		if maxDownVol == 0 {
			return true // no down days = pristine accumulation, accept
		}
		if volumes[i] > maxDownVol {
			return true // pocket pivot found
		}
	}
	return false
}

// hasHigherLowInBase implements Book Ch.4 p.127.
// Detects whether the recent base period shows higher lows (≥2 swing lows
// progressing upward). Skip-safe when insufficient history.
func hasHigherLowInBase(lows []float64) bool {
	n := len(lows)
	if n < config.HigherLowBaseLookback {
		return true
	}
	window := lows[n-config.HigherLowBaseLookback:]
	// Detect local minima (a bar lower than HigherLowMinSeparation neighbours on each side)
	sep := config.HigherLowMinSeparation
	var swingLows []float64
	for i := sep; i < len(window)-sep; i++ {
		isMin := true
		for j := i - sep; j <= i+sep; j++ {
			if j == i {
				continue
			}
			if window[j] <= window[i] {
				isMin = false
				break
			}
		}
		if isMin {
			swingLows = append(swingLows, window[i])
		}
	}
	if len(swingLows) < config.HigherLowMinSwings {
		return true // not enough swing structure — skip-safe
	}
	// Require the last swing low to be ≥ the first swing low (progressing up)
	return swingLows[len(swingLows)-1] >= swingLows[0]
}

// isWithinATHProximity returns true if `ltp` is within ATHProximityPct of the
// 52-week high computed from `closes` and (optionally) `highs`. Extracted from
// passesPhase2Filter so the ATH proximity rule can be unit-tested in isolation.
func isWithinATHProximity(closes, highs []float64, ltp float64) bool {
	if len(closes) == 0 {
		return false
	}
	const lookback52W = 252
	window := closes
	if len(window) > lookback52W {
		window = window[len(window)-lookback52W:]
	}
	high52 := window[0]
	for _, c := range window {
		if c > high52 {
			high52 = c
		}
	}
	if highs != nil {
		hWindow := highs
		if len(hWindow) > lookback52W {
			hWindow = hWindow[len(hWindow)-lookback52W:]
		}
		for _, h := range hWindow {
			if h > high52 {
				high52 = h
			}
		}
	}
	if high52 <= 0 {
		return false
	}
	distFrom52WH := ((high52 - ltp) / high52) * 100
	return distFrom52WH <= config.ATHProximityPct
}

// hasUnreclaimedRejection implements Book Ch.4 p.135-136 (extracted helper).
// Returns true if there is a rejection candle in the last RejectionSkipBars whose
// high has NOT been reclaimed by `ltp`. A rejection candle has upper wick ≥
// RejectionWickRatio of total range; range must be ≥ RejectionMinRange of close.
func hasUnreclaimedRejection(highs, lows, closes []float64, ltp float64) bool {
	n := len(closes)
	if n < config.RejectionSkipBars || len(highs) != n || len(lows) != n {
		return false
	}
	rejectionHigh := 0.0
	checkFrom := n - config.RejectionSkipBars
	for i := checkFrom; i < n; i++ {
		totalRange := highs[i] - lows[i]
		if closes[i] <= 0 || totalRange < closes[i]*config.RejectionMinRange {
			continue
		}
		upperWick := highs[i] - closes[i]
		if upperWick/totalRange >= config.RejectionWickRatio {
			if highs[i] > rejectionHigh {
				rejectionHigh = highs[i]
			}
		}
	}
	return rejectionHigh > 0 && ltp < rejectionHigh
}

// hasPriorBreakoutAttempt implements Book Ch.4 p.137.
// Counts how many times within BreakoutAttemptsLookback the stock has touched
// (within proximity %) the recent high and then closed back at least
// BreakoutAttemptsFailRetracePct below. Skip-safe when insufficient history.
func hasPriorBreakoutAttempt(highs []float64) bool {
	n := len(highs)
	if n < config.BreakoutAttemptsLookback+5 {
		return true
	}
	window := highs[n-config.BreakoutAttemptsLookback:]
	// Find the resistance level = max high in the window (excluding the very last bar)
	resistance := 0.0
	for i := 0; i < len(window)-2; i++ {
		if window[i] > resistance {
			resistance = window[i]
		}
	}
	if resistance <= 0 {
		return true
	}
	// Count attempts: bars that came within ProximityPct of resistance
	// then where the next-bar high closed at least FailRetracePct below resistance
	attempts := 0
	for i := 0; i < len(window)-3; i++ {
		dist := (resistance - window[i]) / resistance * 100
		if dist > config.BreakoutAttemptsProximityPct {
			continue
		}
		// Look ahead — if the high within next 3 bars retraced ≥ FailRetracePct, count as attempt
		for j := i + 1; j < i+4 && j < len(window); j++ {
			retrace := (resistance - window[j]) / resistance * 100
			if retrace >= config.BreakoutAttemptsFailRetracePct {
				attempts++
				break
			}
		}
	}
	return attempts >= config.BreakoutAttemptsMinRequired
}

// deriveWeeklyCloses converts daily closes to actual calendar-week closes.
//
// When TradingDates (YYYY-MM-DD strings, aligned with dailyCloses) are available,
// bars are grouped by ISO week number and the LAST close of each week is returned —
// the true Friday close, or the last trading day of the week when Friday is a holiday.
// This is how most technical analysis platforms define "weekly close" for Indian equities.
//
// Falls back to every-5th-bar approximation only when dates are unavailable.
func deriveWeeklyCloses(dailyCloses []float64, tradingDates []string) []float64 {
	n := len(dailyCloses)
	if n < 5 {
		return nil
	}

	// ── ISO-week calendar grouping (primary path) ────────────────────────────
	if len(tradingDates) > 0 {
		// Align: match the date slice to the closes slice (both end at today)
		dates := tradingDates
		if len(dates) > n {
			dates = dates[len(dates)-n:]
		} else if len(dates) < n {
			// Fewer dates than closes — offset closes so they align
			dailyCloses = dailyCloses[n-len(dates):]
			n = len(dailyCloses)
		}

		type weekKey struct{ year, week int }
		var weekly []float64
		var curKey weekKey
		var weekClose float64
		started := false

		for i, dateStr := range dates {
			t, err := time.Parse("2006-01-02", dateStr)
			if err != nil {
				continue // Skip malformed dates; don't abort
			}
			yr, wk := t.ISOWeek()
			key := weekKey{yr, wk}

			if !started {
				curKey = key
				started = true
			}
			if key != curKey {
				// New ISO week has started — save the completed week's last close
				weekly = append(weekly, weekClose)
				curKey = key
			}
			weekClose = dailyCloses[i] // keep updating → will be the week's last bar
		}
		if started {
			weekly = append(weekly, weekClose) // include the current (possibly partial) week
		}

		if len(weekly) >= 10 { // need ≥10 weekly bars for EMA20 to be meaningful
			return weekly
		}
		// Fewer than 10 weeks of data — fall through to every-5th approximation
	}

	// ── Fallback: every-5th-bar approximation ────────────────────────────────
	// Used only when TradingDates are not available (e.g. tests, backtest without dates).
	weekly := make([]float64, 0, n/5)
	for i := 4; i < n; i += 5 {
		weekly = append(weekly, dailyCloses[i])
	}
	return weekly
}

// ══════════════════════════════════════════════════════════════
//  Phase 2 (Method B): Sector Leader Detection
// ══════════════════════════════════════════════════════════════
//  Doc: "Look for sector indices making new All-Time Highs while the
//  broader index (Nifty/Smallcap) is consolidating or moving sideways."

func (s *ScannerAgent) GetStrongSectors() []string {
	if s.DailyCache == nil || !s.DailyCache.Loaded {
		return nil
	}

	// Check if Nifty is sideways (ROC near 0 → not trending)
	niftyROC := s.computeROC(config.NiftySpotToken, 21) // 1-month daily ROC
	niftySideways := math.Abs(niftyROC) < 5.0

	var strong []string
	for name, sectorToken := range config.SectorTokens {
		closes, ok := s.DailyCache.Closes[sectorToken]
		if !ok || len(closes) < 252 {
			continue
		}

		// Sector at or near 52-week high (within 2%)
		high52 := closes[0]
		for _, c := range closes {
			if c > high52 {
				high52 = c
			}
		}
		currentClose := closes[len(closes)-1]
		dist := ((high52 - currentClose) / high52) * 100

		// Sector making new ATH while Nifty is sideways
		if dist < 2.0 && niftySideways {
			strong = append(strong, name)
			log.Printf("[SectorLeader] %s at ATH (%.1f%% from high) while Nifty sideways (ROC=%.1f)",
				name, dist, niftyROC)
		}
	}

	return strong
}


// ══════════════════════════════════════════════════════════════
//  Phase 3: Top-Up / Pyramiding
// ══════════════════════════════════════════════════════════════
//  Doc: "Add more quantity when the stock breaks out past its initial
//  listing/issue price and successfully retests that level."
//  In practice: if an existing position's stock successfully retests
//  and holds above its breakout (entry) level, add to the position.

func (s *ScannerAgent) CheckTopUp(token uint32, symbol string, entryPrice float64, ltp float64, regime string) *Signal {
	if ltp <= 0 || entryPrice <= 0 {
		return nil
	}

	// Top-up condition: LTP pulled back to within 2% of entry, then bounced back above entry
	// This is the "retest of breakout level" from the document
	closes, ok := s.DailyCache.Closes[token]
	if !ok || len(closes) < 5 {
		return nil
	}

	// Check if recent daily close was near entry (within 2%) and now recovering
	recentPullback := false
	for i := len(closes) - 5; i < len(closes)-1; i++ {
		if i >= 0 && closes[i] <= entryPrice*1.02 && closes[i] >= entryPrice*0.98 {
			recentPullback = true
			break
		}
	}

	if !recentPullback || ltp < entryPrice*1.01 {
		return nil // Not retested, or still below entry
	}

	stopPrice := ltp * (1 - config.SLCeilingPct/100)
	// Book Ch.8: risk-based sizing for top-ups (smaller by design — entry+10% SL)
	qty := computeRiskBasedQty(config.TotalCapital, s.CapitalMultiplier, ltp, stopPrice)
	if qty <= 0 {
		log.Printf("[TopUp] %s: top-up qty=0 — skipping", symbol)
		return nil
	}

	log.Printf("[TopUp] %s retest confirmed: Entry=%.2f LTP=%.2f", symbol, entryPrice, ltp)

	return &Signal{
		Strategy:    "VCP_TOPUP",
		Symbol:      symbol,
		Token:       token,
		Regime:      regime,
		EntryPrice:  ltp,
		StopPrice:   stopPrice,
		TargetPrice: 0,
		Qty:         qty,
		Product:     "CNC",
		IsShort:     false,
	}
}

// ══════════════════════════════════════════════════════════════
//  Phase 5: Re-Entry Signal Generation
// ══════════════════════════════════════════════════════════════
//  "If stopped out by 21 EMA rule, but stock reclaims and closes
//   back above 21 EMA with a green candle → re-enter the trade."

func (s *ScannerAgent) CheckReEntry(token uint32, symbol string, regime string) *Signal {
	if s.DailyCache == nil || !s.DailyCache.Loaded {
		return nil
	}

	closes, cOk := s.DailyCache.Closes[token]
	ema20Val, eOk := s.DailyCache.EMA20[token]
	if !cOk || !eOk || len(closes) < 2 {
		return nil
	}

	lastClose := closes[len(closes)-1]
	prevClose := closes[len(closes)-2]

	// Green candle that closes above EMA20
	isGreen := lastClose > prevClose
	aboveEMA := lastClose > ema20Val

	if !isGreen || !aboveEMA {
		return nil
	}

	entryPrice := lastClose
	prevLow := closes[len(closes)-2] // use prev close as proxy for structural SL in re-entry
	stopPrice := config.ComputeStructuralSL(entryPrice, prevLow)

	// Book Ch.8: risk-based sizing for re-entries
	qty := computeRiskBasedQty(config.TotalCapital, s.CapitalMultiplier, entryPrice, stopPrice)
	if qty <= 0 {
		log.Printf("[ReEntry] %s: re-entry qty=0 — skipping", symbol)
		return nil
	}

	log.Printf("[ReEntry] %s reclaimed EMA20: Close=%.2f EMA20=%.2f", symbol, lastClose, ema20Val)

	return &Signal{
		Strategy:    "VCP_REENTRY",
		Symbol:      symbol,
		Token:       token,
		Regime:      regime,
		EntryPrice:  entryPrice,
		StopPrice:   stopPrice,
		TargetPrice: 0,
		Qty:         qty,
		Product:     "CNC",
		IsShort:     false,
	}
}

// ══════════════════════════════════════════════════════════════
//  Book Ch.8: Risk-Based Position Sizing
// ══════════════════════════════════════════════════════════════
//  "Quantity = Risk / (Entry − Stop-Loss)"  where Risk = Capital × 1%
//  Clamped: floor at MinAbsPositionSize; ceiling at MaxTradeAllocPct% of capital.

// computeRiskBasedQty is the Book Ch.8 position sizing formula (p.200-201).
// Quantity = Risk_Amount / (Entry - SL)
// Risk_Amount = Capital × CapitalMultiplier × RiskPerTradePct%
// Result is clamped between a minimum meaningful lot and MaxTradeAllocPct% of capital.
func computeRiskBasedQty(capital, capitalMultiplier, entryPrice, stopPrice float64) int {
	if entryPrice <= 0 {
		return 0
	}
	riskPerShare := entryPrice - stopPrice
	if riskPerShare <= 0 || stopPrice <= 0 {
		// No valid SL distance — fall back to fixed MaxTradeAllocPct allocation
		qty := int(capital * capitalMultiplier * config.MaxTradeAllocPct / 100 / entryPrice)
		if qty < 1 {
			qty = 1
		}
		return qty
	}
	// Core formula
	riskAmount := capital * capitalMultiplier * config.RiskPerTradePct / 100
	qty := int(riskAmount / riskPerShare)

	// Floor: minimum meaningful position (avoid placing 1-share orders)
	minQty := int(config.MinAbsPositionSize / entryPrice)
	if minQty < 1 {
		minQty = 1
	}
	if qty < minQty {
		qty = minQty
	}
	// Ceiling: never exceed MaxTradeAllocPct% of effective capital
	maxQty := int(capital * capitalMultiplier * config.MaxTradeAllocPct / 100 / entryPrice)
	if maxQty < 1 {
		maxQty = 1
	}
	if qty > maxQty {
		qty = maxQty
	}
	return qty
}

// ══════════════════════════════════════════════════════════════
//  isExpiryWeek — Book Ch.5 p.131 F&O expiry week detection
// ══════════════════════════════════════════════════════════════
//  NSE F&O contracts expire on the last Thursday of every month.
//  The engine halves position sizes during the Monday–Thursday of expiry week
//  to guard against the elevated volatility and stop-hunt behaviour.

func isExpiryWeek() bool {
	now := config.NowIST()
	// Find the last Thursday of the current month
	// Start from the last day of the month and walk backwards to Thursday
	lastOfMonth := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, config.IST)
	daysBack := int(lastOfMonth.Weekday()) - int(time.Thursday)
	if daysBack < 0 {
		daysBack += 7
	}
	lastThursday := lastOfMonth.AddDate(0, 0, -daysBack)
	// Expiry week = Monday through Thursday of that same week
	monday := lastThursday.AddDate(0, 0, -int(lastThursday.Weekday())+1)
	nowDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, config.IST)
	return !nowDate.Before(monday) && !nowDate.After(lastThursday)
}

// ComputeEMA21 is defined in execution_agent.go (same package)

// ComputeADRatio calculates the Advance/Decline ratio from the live universe.
// Compares each stock's live LTP to the prior-day close in DailyCache.
// Returns a value in [0,1]: >0.6 = broadly advancing, <0.4 = broadly declining.
func (s *ScannerAgent) ComputeADRatio() float64 {
	if s.DailyCache == nil || !s.DailyCache.Loaded || s.GetLTP == nil {
		return 0.5
	}
	advance, decline := 0, 0
	for token := range s.Universe {
		ltp := s.GetLTP(token)
		if ltp <= 0 {
			continue
		}
		closes, ok := s.DailyCache.Closes[token]
		if !ok || len(closes) == 0 {
			continue
		}
		prevClose := closes[len(closes)-1]
		if prevClose <= 0 {
			continue
		}
		switch {
		case ltp > prevClose*1.001:
			advance++
		case ltp < prevClose*0.999:
			decline++
		}
	}
	total := advance + decline
	if total == 0 {
		return 0.5
	}
	return float64(advance) / float64(total)
}

