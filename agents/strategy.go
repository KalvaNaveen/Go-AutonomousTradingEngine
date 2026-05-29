package agents

// StrategyContext provides runtime data to each strategy agent.
// Live scanner populates all fields; backtest leaves live-feed funcs nil.
type StrategyContext struct {
	Cache             *DailyCache
	CapitalMultiplier float64
	GetVolume         func(uint32) int64
	IPOSymbols        map[string]bool
	IsMajorEventDay   bool
}

// StrategyAgent is the contract every standalone strategy implements.
type StrategyAgent interface {
	Name() string
	Detect(token uint32, symbol string, ltp float64, regime string, ctx StrategyContext) *Signal
}

// AllStrategies returns every registered standalone strategy agent in scan priority order.
// Book Ch.4+5+11: Priority = strongest pattern → most autonomous entry.
//
// Order rationale:
//  1. VCP        — highest win-rate; large base, clean risk/reward (Ch.4 p.55)
//  2. Cup&Handle — next most reliable; rounded base, institutional accumulation (Ch.4 p.86)
//  3. Flat Base  — very tight range above SMA200; reliable continuation (Ch.4 p.120)
//  4. Bull Flag  — continuation after momentum run; high probability (Ch.4 p.68)
//  5. Trend Channel — pullback to support; works in calm uptrends (Ch.4 p.103)
//  6. IPO Base   — niche; only for genuine IPO stocks within 40-day window (Ch.4 p.119)
//
// These six are the only setups in "Swing Trading Simplified" (Ankur Patel).
// Non-book setups (EMA-crossover entry, CMF confirmation) were removed entirely.
func AllStrategies() []StrategyAgent {
	return []StrategyAgent{
		&VCPStrategy{},
		&CupHandleStrategy{},
		&FlatBaseStrategy{},
		&BullFlagStrategy{},
		&TrendChannelStrategy{},
		&IPOBaseStrategy{},
	}
}
