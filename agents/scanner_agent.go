package agents

import (
	"log"
	"math"

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
	SMA200       map[uint32]float64
	ATR          map[uint32]float64
	EMA25        map[uint32]float64
	EMA21        map[uint32]float64
	BBLower      map[uint32]float64
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
func (s *ScannerAgent) computeROC(token uint32, length int) float64 {
	closes, ok := s.DailyCache.Closes[token]
	if !ok || len(closes) < length+1 {
		return 0
	}
	current := closes[len(closes)-1]
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

		// Section III.2: Near ATH filter
		if !s.passesPhase2Filter(token, ltp) {
			continue
		}

		// Section V header: "Only execute these on stocks that passed the fundamental screens"
		if s.FundamentalPassed != nil {
			if passed, exists := s.FundamentalPassed[symbol]; exists && !passed {
				continue
			}
		}

		// Section V: All 5 technical entry setups
		if sig := s.detectVCPBreakout(token, symbol, ltp, regime); sig != nil {
			signals = append(signals, sig)
			continue // One signal per stock per scan
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

	// Doc V.1 Invalidation: "setup is dead if stock closes below 21 EMA or 63 EMA"
	lastClose := closes[len(closes)-1]
	ema21, e21Ok := s.DailyCache.EMA21[token]
	if e21Ok && lastClose < ema21 {
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

	// Identify pullback depths
	var pullbackDepths []float64
	inPullback := false
	currentLow := resistance

	for i := 1; i < len(hWindow); i++ {
		if hWindow[i] < resistance*0.98 {
			inPullback = true
			if lWindow[i] < currentLow {
				currentLow = lWindow[i]
			}
		} else if inPullback {
			depth := ((resistance - currentLow) / resistance) * 100
			if depth > 1.0 {
				pullbackDepths = append(pullbackDepths, depth)
			}
			inPullback = false
			currentLow = resistance
		}
	}
	if inPullback {
		depth := ((resistance - currentLow) / resistance) * 100
		if depth > 1.0 {
			pullbackDepths = append(pullbackDepths, depth)
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
		if currentVol > 0 && currentVol < avgVol {
			return nil // Breakout NOT on high volume
		}
	}

	// Generate Signal — Doc VI: "5% to 10% of total portfolio per trade"
	entryPrice := ltp
	stopPrice := entryPrice * (1 - config.HardStopLossPct/100)

	effectiveCapital := config.TotalCapital * s.CapitalMultiplier
	positionSize := effectiveCapital * config.MaxTradeAllocPct / 100
	qty := int(math.Floor(positionSize / entryPrice))
	if qty <= 0 {
		qty = 1
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
		qty = 1
	}

	stopPrice := ltp * (1 - config.HardStopLossPct/100)

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
	ema21Val, eOk := s.DailyCache.EMA21[token]
	if !cOk || !eOk || len(closes) < 2 {
		return nil
	}

	lastClose := closes[len(closes)-1]
	prevClose := closes[len(closes)-2]

	// Green candle (close > previous close) that closes above 21 EMA
	isGreen := lastClose > prevClose
	aboveEMA := lastClose > ema21Val

	if !isGreen || !aboveEMA {
		return nil
	}

	entryPrice := lastClose
	stopPrice := entryPrice * (1 - config.HardStopLossPct/100)

	effectiveCapital := config.TotalCapital * s.CapitalMultiplier
	positionSize := effectiveCapital * config.MaxTradeAllocPct / 100
	qty := int(math.Floor(positionSize / entryPrice))
	if qty <= 0 {
		qty = 1
	}

	log.Printf("[ReEntry] %s reclaimed 21 EMA: Close=%.2f EMA21=%.2f", symbol, lastClose, ema21Val)

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

