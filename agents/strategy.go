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

// AllStrategies returns the engine's entry setups. This is a PURE-EMA engine:
// the only entry is the EMA pullback/bounce from "Swing Trading Simplified"
// (Ankur Patel) Ch.3 p.44-49 — a stock in a confirmed uptrend (10 EMA > 20 EMA,
// both rising) pulls back to a key moving average on light volume and bounces.
// All chart-pattern strategies (VCP, Cup & Handle, Flat Base, Bull Flag, Trend
// Channel, IPO Base) were removed in favour of this single EMA-based setup.
func AllStrategies() []StrategyAgent {
	return []StrategyAgent{
		&EMAStrategy{},
	}
}
