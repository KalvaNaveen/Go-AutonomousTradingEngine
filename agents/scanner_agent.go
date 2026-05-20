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
	Closes       map[uint32][]float64
	Highs        map[uint32][]float64
	Lows         map[uint32][]float64
	Volumes      map[uint32][]float64
	AvgVol       map[uint32]float64
	TurnoverCr   map[uint32]float64
	PivotSupport map[uint32]float64
	High52W      map[uint32]float64
	RSScore      map[uint32]int
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
	FundamentalPassed   map[string]bool    // Section V: only trade stocks that passed Screener.in filter
	IPOSymbols          map[string]bool    // FIX-08: Section V.3: only apply IPO Base to actual recent IPOs
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

func (s *ScannerAgent) DetectRegime() string {
	if s.DailyCache == nil || !s.DailyCache.Loaded {
		return "UNKNOWN"
	}

	// Compute ROC for Nifty 50 (monthly equivalent: 378 daily bars)
	niftyROC := s.computeROC(config.NiftySpotToken, config.ROCNiftyLengthDaily)
	// Compute ROC for Smallcap 100 (monthly equivalent: 420 daily bars)
	smallcapROC := s.computeROC(config.SmallcapToken, config.ROCSmallcapLengthDaily)

	candidate := "NORMAL"

	// --- DEFENSIVE: Blueprint-mandated upper bounds ---
	// Blueprint: "Reduce equity/switch to bonds/gold near 45 (Nifty), near 100 (Smallcap)"
	if niftyROC >= config.ROCNiftySellThreshold || smallcapROC >= config.ROCSmallcapSellThreshold {
		candidate = "DEFENSIVE"
	}

	// --- DEFENSIVE: Engineering addition — not in Blueprint ---
	// The Blueprint gives no guidance for deeply negative ROC.
	// A NiftyROC of -30% is not "near 0" and should not trigger AGGRESSIVE buys.
	// ⚠️ EXTENSION: Thresholds (-20, -35) must be back-tested against 2008, 2011, 2020 data.
	if niftyROC <= -20 || smallcapROC <= -35 {
		candidate = "DEFENSIVE" // bear market protection
	}

	// --- REDUCED_CAPITAL: Blueprint-mandated ---
	// Blueprint (Section VI.4): "5 consecutive SL hits → reduce capital to 30-40%"
	// IMPORTANT: This reduces capital independently — it does NOT override DEFENSIVE.
	// If we're in a DEFENSIVE market AND have 5 SLs, we stay DEFENSIVE (no signals)
	// but the capital multiplier is still reduced for when regime changes.
	if s.ConsecutiveSLs >= config.ConsecutiveSLCutoff {
		s.CapitalMultiplier = config.ReducedCapitalPct
		log.Printf("[Regime] CONTINGENCY: %d consecutive SL hits → capital reduced to %.0f%%",
			s.ConsecutiveSLs, s.CapitalMultiplier*100)
		if candidate != "DEFENSIVE" {
			candidate = "REDUCED_CAPITAL"
		}
	}

	// --- AGGRESSIVE: Blueprint-mandated ---
	// Blueprint (Section II): "Buy aggressively near 0."
	// Rationale: When the index has gone nowhere for 18 months, institutional money
	// hasn't committed. Individual breakouts in this environment face less crowding
	// and tend to have cleaner moves (Stage 1 → Stage 2 transition).
	if candidate == "NORMAL" && niftyROC >= -config.ROCNiftyBuyThreshold && niftyROC <= config.ROCNiftyBuyThreshold {
		candidate = "AGGRESSIVE"
	}

	if candidate != s.LastRegime {
		log.Printf("[Regime] %s → %s (NiftyROC=%.1f SmallcapROC=%.1f ConsecSL=%d)",
			s.LastRegime, candidate, niftyROC, smallcapROC, s.ConsecutiveSLs)
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

// RecordSLHit increments consecutive SL counter
func (s *ScannerAgent) RecordSLHit() {
	s.ConsecutiveSLs++
	if s.ConsecutiveSLs >= config.ConsecutiveSLCutoff && s.CapitalMultiplier > config.ReducedCapitalPct {
		s.CapitalMultiplier = config.ReducedCapitalPct
		log.Printf("[Scanner] CONTINGENCY TRIGGERED: %d consecutive SLs → capital at %.0f%%",
			s.ConsecutiveSLs, s.CapitalMultiplier*100)
	}
}

// RecordWin resets the consecutive SL counter
func (s *ScannerAgent) RecordWin() {
	s.ConsecutiveSLs = 0
	s.CapitalMultiplier = 1.0
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
		return nil
	}

	// Section III.2: Detect strong sectors during sideways markets
	strongSectors := s.GetStrongSectors()
	if len(strongSectors) > 0 {
		log.Printf("[Scanner] Strong sectors: %v", strongSectors)
	}

	var signals []*Signal

	for token, symbol := range s.Universe {
		ltp := 0.0
		if s.GetLTP != nil {
			ltp = s.GetLTP(token)
		}
		if ltp <= 0 {
			continue
		}

		// Liquidity gate: skip stocks below ₹5Cr/day avg turnover (critical for Microcap 250 half)
		if turn, ok := s.DailyCache.TurnoverCr[token]; ok && turn < config.LiquidityFilterCr {
			continue
		}

		// Near ATH filter
		if !s.passesPhase2Filter(token, ltp) {
			continue
		}

		// Only trade stocks that passed the fundamental screens (if screener is active)
		if s.FundamentalPassed != nil {
			if passed, exists := s.FundamentalPassed[symbol]; exists && !passed {
				continue
			}
		}

		// EMA crossover signal (primary — checked first; broader universe makes this the main driver)
		if sig := s.detectEMACrossover(token, symbol, ltp, regime); sig != nil {
			signals = append(signals, sig)
			continue
		}
		// VCP breakout (structural pattern — higher conviction when it fires)
		if sig := s.detectVCPBreakout(token, symbol, ltp, regime); sig != nil {
			signals = append(signals, sig)
			continue
		}
		if sig := s.detectBullFlag(token, symbol, ltp, regime); sig != nil {
			signals = append(signals, sig)
			continue
		}
		if sig := s.detectIPOBaseBreakout(token, symbol, ltp, regime); sig != nil {
			signals = append(signals, sig)
			continue
		}
		if sig := s.detectCupWithHandle(token, symbol, ltp, regime); sig != nil {
			signals = append(signals, sig)
			continue
		}
		if sig := s.detectTrendChannel(token, symbol, ltp, regime); sig != nil {
			signals = append(signals, sig)
		}
	}

	return signals
}

// ══════════════════════════════════════════════════════════════
//  EMA Crossover + Volume Direction Signal
// ══════════════════════════════════════════════════════════════

// detectEMACrossover fires when EMA10 freshly crosses above EMA20 (within last 3 bars)
// AND at least 2 of the last 3 days show buying pressure via Close Position Ratio.
func (s *ScannerAgent) detectEMACrossover(token uint32, symbol string, ltp float64, regime string) *Signal {
	closes, cOk := s.DailyCache.Closes[token]
	highs, hOk := s.DailyCache.Highs[token]
	lows, lOk := s.DailyCache.Lows[token]
	volumes, vOk := s.DailyCache.Volumes[token]
	if !cOk || !hOk || !lOk || !vOk {
		return nil
	}
	if len(closes) < config.EMA20Period+5 {
		return nil
	}

	// Compute EMA series to detect fresh crossover
	ema10s := computeEMASeries(closes, config.EMA10Period)
	ema20s := computeEMASeries(closes, config.EMA20Period)
	n := len(ema10s)
	if n < 2 || len(ema20s) < 2 {
		return nil
	}

	// Fresh crossover: EMA10 crossed above EMA20 in last 1-3 bars
	crossed := false
	lookback := 3
	if lookback > n-1 {
		lookback = n - 1
	}
	for i := n - lookback - 1; i < n-1; i++ {
		if i < 0 {
			continue
		}
		if ema10s[i] <= ema20s[i] && ema10s[i+1] > ema20s[i+1] {
			crossed = true
			break
		}
	}
	if !crossed {
		return nil
	}

	// Volume direction: ≥2 of last 3 days must show buying pressure
	if !s.checkBuyPressure(closes, highs, lows, volumes) {
		return nil
	}

	// Volume spike vs 20-day avg (time-paced for intraday)
	avgVol := s.DailyCache.AvgVol[token]
	if avgVol > 0 && s.GetVolume != nil {
		currentVol := float64(s.GetVolume(token))
		if currentVol > 0 {
			now := config.NowIST()
			open := time.Date(now.Year(), now.Month(), now.Day(), 9, 15, 0, 0, now.Location())
			fraction := now.Sub(open).Minutes() / 375.0
			if fraction > 1.0 {
				fraction = 1.0
			}
			if fraction < 0.05 {
				fraction = 0.05
			}
			if currentVol/fraction < avgVol*config.VolumeSpikeMultiplier {
				return nil
			}
		}
	}

	// Structural SL using previous candle's low
	prevLow := lows[len(lows)-2]
	entryPrice := ltp * 1.003 // 0.3% above LTP for GTT trigger
	stopPrice := config.ComputeStructuralSL(entryPrice, prevLow)
	if stopPrice >= entryPrice {
		return nil
	}

	capital := config.TotalCapital * config.MaxTradeAllocPct / 100
	qty := int(capital / entryPrice)
	if qty < 1 {
		return nil
	}

	return &Signal{
		Strategy:    "EMA_CROSS_BUY",
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

// checkBuyPressure returns true if ≥VolumePressureDaysNeeded of the last VolumePressureDays
// days show buying pressure: CPR=(Close-Low)/(High-Low) ≥ BuyPressureRatio with volume spike.
func (s *ScannerAgent) checkBuyPressure(closes, highs, lows, volumes []float64) bool {
	return s.countPressureDays(closes, highs, lows, volumes, true) >= config.VolumePressureDaysNeeded
}

// CheckSellPressure is exported for use by the exit monitor in execution_agent.
func (s *ScannerAgent) CheckSellPressure(closes, highs, lows, volumes []float64) bool {
	return s.countPressureDays(closes, highs, lows, volumes, false) >= config.VolumePressureDaysNeeded
}

func (s *ScannerAgent) countPressureDays(closes, highs, lows, volumes []float64, buyDirection bool) int {
	n := len(closes)
	if n < config.VolumePressureDays {
		return 0
	}
	avgVol := 0.0
	if len(volumes) >= 20 {
		for _, v := range volumes[len(volumes)-20:] {
			avgVol += v
		}
		avgVol /= 20
	}
	days := 0
	for i := n - config.VolumePressureDays; i < n; i++ {
		rng := highs[i] - lows[i]
		if rng <= 0 {
			continue
		}
		cpr := (closes[i] - lows[i]) / rng
		volSpike := avgVol <= 0 || volumes[i] >= avgVol*config.VolumeSpikeMultiplier
		if buyDirection && cpr >= config.BuyPressureRatio && volSpike {
			days++
		} else if !buyDirection && cpr <= config.SellPressureRatio && volSpike {
			days++
		}
	}
	return days
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
	// FIX-02: Blueprint says "All-Time Highs", not 52-week highs.
	// Use max of all closes in our 500-day cache as ATH proxy.
	closes, cOk := s.DailyCache.Closes[token]
	if !cOk || len(closes) == 0 {
		return false
	}

	// Compute true ATH from full available history
	ath := closes[0]
	for _, c := range closes {
		if c > ath {
			ath = c
		}
	}
	// Also check highs array for intraday peaks
	highs, hOk := s.DailyCache.Highs[token]
	if hOk {
		for _, h := range highs {
			if h > ath {
				ath = h
			}
		}
	}

	if ath <= 0 {
		return false
	}
	distFromATH := ((ath - ltp) / ath) * 100
	if distFromATH > config.ATHProximityPct {
		return false
	}
	return true
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
//  Phase 3: VCP Breakout Entry
// ══════════════════════════════════════════════════════════════
//  Spot the VCP (Volatility Contraction Pattern):
//  - Stock hits resistance and pulls back
//  - Each subsequent pullback depth is shallower than the previous
//  - Buy when stock breaks out of the tight consolidation base

func (s *ScannerAgent) detectVCPBreakout(token uint32, symbol string, ltp float64, regime string) *Signal {
	highs, hOk := s.DailyCache.Highs[token]
	lows, lOk := s.DailyCache.Lows[token]
	closes, cOk := s.DailyCache.Closes[token]
	volumes, vOk := s.DailyCache.Volumes[token]
	if !hOk || !lOk || !cOk {
		return nil
	}

	lookback := config.VCPLookbackDays
	if len(highs) < lookback || len(lows) < lookback || len(closes) < lookback {
		return nil
	}

	// VCP invalidation: setup is dead if stock closes below EMA20 or EMA63
	lastClose := closes[len(closes)-1]
	ema20, e20Ok := s.DailyCache.EMA20[token]
	if e20Ok && lastClose < ema20 {
		return nil
	}
	// Also check 63 EMA (compute from closes)
	if len(closes) > config.EMA63Period {
		ema63 := ComputeEMA63(closes)
		if ema63 > 0 && lastClose < ema63 {
			return nil
		}
	}

	hWindow := highs[len(highs)-lookback:]
	lWindow := lows[len(lows)-lookback:]

	// Find resistance (highest high in lookback)
	resistance := hWindow[0]
	for _, h := range hWindow {
		if h > resistance {
			resistance = h
		}
	}

	// Identify pullback depths measured from each LOCAL HIGH (not always from the original ATH).
	// Correct VCP structure: lower-high → lower-low → lower-high, each pullback shallower.
	// Bug fix: previous code reset currentLow=resistance after each pullback, causing all depths
	// to be measured from the same ATH instead of the most recent bounce high.
	var pullbackDepths []float64
	lastPullbackLow := resistance
	localHigh := resistance
	inPullback := false
	currentLow := resistance

	for i := 1; i < len(hWindow); i++ {
		if !inPullback {
			// Track new local highs before next pullback starts
			if hWindow[i] > localHigh {
				localHigh = hWindow[i]
			}
			// Pullback starts when price drops >2% from the local high
			if lWindow[i] < localHigh*0.98 {
				inPullback = true
				currentLow = lWindow[i]
			}
		} else {
			// Track the lowest point of this pullback
			if lWindow[i] < currentLow {
				currentLow = lWindow[i]
			}
			// Pullback ends when price bounces >2% above the low
			if hWindow[i] > currentLow*1.02 {
				depth := ((localHigh - currentLow) / localHigh) * 100
				if depth > 1.0 {
					pullbackDepths = append(pullbackDepths, depth)
					lastPullbackLow = currentLow
				}
				inPullback = false
				localHigh = hWindow[i] // Next pullback measured from this bounce high
			}
		}
	}
	// Capture any in-progress pullback (price still contracting at end of window)
	if inPullback {
		depth := ((localHigh - currentLow) / localHigh) * 100
		if depth > 1.0 {
			pullbackDepths = append(pullbackDepths, depth)
			lastPullbackLow = currentLow
		}
	}

	if len(pullbackDepths) < config.VCPMinPullbacks {
		return nil
	}

	// Doc: "Each pullback depth must be strictly smaller than the previous"
	for i := 1; i < len(pullbackDepths); i++ {
		if pullbackDepths[i] >= pullbackDepths[i-1]*config.VCPContractionRatio {
			return nil
		}
	}

	// FIX-06: Doc V.1: "volume MUST dry up during the contraction phase"
	// Early = first 30 candles (sellers active), Late = last 20 candles (tight contraction)
	// ⚠️ EXTENSION: 20% threshold not in Blueprint — tune via back-test.
	if vOk && len(volumes) >= lookback {
		vWindow := volumes[len(volumes)-lookback:]
		var earlyVol, lateVol float64
		earlyCount := 30
		if earlyCount > len(vWindow)/2 {
			earlyCount = len(vWindow) / 2
		}
		lateStart := len(vWindow) - 20
		if lateStart < earlyCount {
			lateStart = earlyCount
		}
		for i := 0; i < earlyCount; i++ {
			earlyVol += vWindow[i]
		}
		earlyVol /= float64(earlyCount)
		for i := lateStart; i < len(vWindow); i++ {
			lateVol += vWindow[i]
		}
		lateVol /= float64(len(vWindow) - lateStart)
		// Late volume must be at least 20% lower than early volume
		if lateVol >= earlyVol*0.80 {
			return nil // Volume did NOT dry up sufficiently
		}
	}

	// Check breakout: LTP breaks above resistance
	if ltp < resistance {
		return nil
	}

	// Doc V.1: "Buy exactly at the Pivot Point breakout on high volume"
	// Kite's tick.Volume is cumulative day-volume — comparing it raw against the
	// 20-day average is wrong early in the session (trivially smaller). Scale by
	// the fraction of the trading day elapsed so an intraday breakout passes when
	// it's pacing above the 20-day average.
	if vOk && len(volumes) > 20 {
		var avgVol float64
		for i := len(volumes) - 21; i < len(volumes)-1; i++ {
			avgVol += volumes[i]
		}
		avgVol /= 20
		currentVol := 0.0
		if s.GetVolume != nil {
			currentVol = float64(s.GetVolume(token))
		}
		if currentVol > 0 && avgVol > 0 {
			// Trading day = 09:15–15:30 IST = 375 minutes. Cap at 1.0 to avoid
			// over-rewarding late-day breakouts; floor at 0.05 to avoid div-by-tiny.
			now := config.NowIST()
			marketOpen := time.Date(now.Year(), now.Month(), now.Day(), 9, 15, 0, 0, now.Location())
			elapsed := now.Sub(marketOpen).Minutes()
			fraction := elapsed / 375.0
			if fraction > 1.0 {
				fraction = 1.0
			}
			if fraction < 0.05 {
				fraction = 0.05
			}
			pacedVol := currentVol / fraction
			if pacedVol < avgVol {
				return nil // Pace says today won't reach average → not high-volume breakout
			}
		}
	}

	// Generate Signal — Doc VI: "5% to 10% of total portfolio per trade"
	entryPrice := ltp
	// Structural SL: 2% below the final VCP contraction pullback, capped at HardStopLossPct
	stopPrice := math.Max(lastPullbackLow*0.98, entryPrice*(1-config.SLCeilingPct/100))

	effectiveCapital := config.TotalCapital * s.CapitalMultiplier
	positionSize := effectiveCapital * config.MaxTradeAllocPct / 100
	qty := int(math.Floor(positionSize / entryPrice))
	if qty <= 0 {
		// Stock too expensive for the per-trade allocation — drop the signal.
		log.Printf("[VCP] %s: position size ₹%.0f / ₹%.2f = 0 shares — skipping",
			symbol, positionSize, entryPrice)
		return nil
	}

	log.Printf("[VCP] BREAKOUT: %s LTP=%.2f Resistance=%.2f Pullbacks=%v",
		symbol, ltp, resistance, pullbackDepths)

	return &Signal{
		Strategy:    "VCP_BREAKOUT",
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

	effectiveCapital := config.TotalCapital * s.CapitalMultiplier
	positionSize := effectiveCapital * config.MinTradeAllocPct / 100 // Smaller allocation for top-up
	qty := int(math.Floor(positionSize / ltp))
	if qty <= 0 {
		log.Printf("[TopUp] %s: top-up size ₹%.0f / ₹%.2f = 0 shares — skipping",
			symbol, positionSize, ltp)
		return nil
	}

	stopPrice := ltp * (1 - config.SLCeilingPct/100)

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

	effectiveCapital := config.TotalCapital * s.CapitalMultiplier
	positionSize := effectiveCapital * config.MaxTradeAllocPct / 100
	qty := int(math.Floor(positionSize / entryPrice))
	if qty <= 0 {
		log.Printf("[ReEntry] %s: re-entry size ₹%.0f / ₹%.2f = 0 shares — skipping",
			symbol, positionSize, entryPrice)
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

