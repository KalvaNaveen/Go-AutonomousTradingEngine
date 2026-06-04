package backtest

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"sort"
	"time"

	"bnf_go_engine/agents"
	"bnf_go_engine/config"

	_ "modernc.org/sqlite"
)

// ══════════════════════════════════════════════════════════════
//  Backtest Engine — simulates the live strategy on historical data.
//  Uses identical indicator math as the live scanner so results
//  translate directly to live performance expectations.
// ══════════════════════════════════════════════════════════════

// Config holds all tunable parameters for one backtest run.
type Config struct {
	Strategy         string  `json:"strategy"` // "ALL" or "EMA_PULLBACK"
	StartBarOffset   int     `json:"start_bar_offset"`    // bars from end to simulate (e.g. 250 = ~1 year)
	Capital          float64 `json:"capital"`
	SLFloorPct       float64 `json:"sl_floor_pct"`        // default 1.5
	SLCeilingPct     float64 `json:"sl_ceiling_pct"`      // default 3.0
	MaxTradeAllocPct float64 `json:"max_trade_alloc_pct"` // default 20.0
	MaxPositions     int     `json:"max_positions"`       // 0 = dynamic from capital
	SlippagePct      float64 `json:"slippage_pct"`        // default 0.3
}

// Trade represents one simulated closed trade.
type Trade struct {
	Symbol          string    `json:"symbol"`
	Strategy        string    `json:"strategy"`
	EntryBar        int       `json:"entry_bar"`
	ExitBar         int       `json:"exit_bar"`
	EntryDate       string    `json:"entry_date"`
	ExitDate        string    `json:"exit_date"`
	EntryPrice      float64   `json:"entry_price"`
	ExitPrice       float64   `json:"exit_price"`
	SL              float64   `json:"sl"`
	Qty             int       `json:"qty"`
	GrossPnl        float64   `json:"gross_pnl"`
	Charges         float64   `json:"charges"`   // STT + exchange + SEBI + stamp + GST
	NetPnl          float64   `json:"net_pnl"`   // GrossPnl − Charges
	PnlPct          float64   `json:"pnl_pct"`   // net %
	ExitReason      string    `json:"exit_reason"`
	HoldingBars     int       `json:"holding_bars"`
	Token           uint32    `json:"token"`
	PriceSlice      []float64 `json:"price_slice"`
	PriceSliceStart int       `json:"price_slice_start"`
}

// Result is the complete output of one backtest run.
type Result struct {
	ID              string    `json:"id"`
	RunAt           string    `json:"run_at"`
	Config          Config    `json:"config"`
	TotalBars       int       `json:"total_bars"`
	TotalTrades     int       `json:"total_trades"`
	Wins            int       `json:"wins"`
	Losses          int       `json:"losses"`
	WinRate         float64   `json:"win_rate"`
	TotalGrossPnl   float64   `json:"total_gross_pnl"`
	TotalCharges    float64   `json:"total_charges"`
	TotalPnl        float64   `json:"total_pnl"`        // net (after charges)
	ReturnPct       float64   `json:"return_pct"`       // net %
	MaxDrawdown     float64   `json:"max_drawdown"`
	SharpeRatio     float64   `json:"sharpe_ratio"`
	ProfitFactor    float64   `json:"profit_factor"`
	AvgWin          float64   `json:"avg_win"`          // avg net win
	AvgLoss         float64   `json:"avg_loss"`         // avg net loss
	MaxConsecLosses int       `json:"max_consec_losses"`
	AvgHoldingBars  float64   `json:"avg_holding_bars"`
	EquityCurve     []float64 `json:"equity_curve"`
	Trades          []Trade   `json:"trades"`

	// Diagnostics — same guards as live engine
	RawSignals        int `json:"raw_signals"`         // pattern detections before guards
	DrawdownHalts     int `json:"drawdown_halts"`      // entries blocked by drawdown halt
	OpenRiskBlocks    int `json:"open_risk_blocks"`    // entries blocked by open risk cap
	UnderwaterBlocks  int `json:"underwater_blocks"`   // entries blocked by ≥2 underwater positions
	SLHits            int `json:"sl_hits"`             // hard SL exits
}

// simPosition tracks one open simulated position.
type simPosition struct {
	token      uint32
	symbol     string
	strategy   string
	entryBar   int
	entryPrice float64
	sl         float64
	qty        int
}

// ══════════════════════════════════════════════════════════════
//  Run — main backtest loop
// ══════════════════════════════════════════════════════════════

func Run(cache *agents.DailyCache, universe map[uint32]string, cfg Config) *Result {
	if cfg.SlippagePct == 0 {
		cfg.SlippagePct = 0.3
	}
	if cfg.StartBarOffset <= 0 || cfg.StartBarOffset > config.EODLookbackDays {
		cfg.StartBarOffset = config.EODLookbackDays - 10
	}
	maxPos := cfg.MaxPositions
	if maxPos <= 0 {
		maxPos = config.ComputeMaxPositions(cfg.Capital)
	}

	result := &Result{
		ID:     fmt.Sprintf("bt_%d", time.Now().UnixNano()),
		RunAt:  config.NowIST().Format(time.RFC3339),
		Config: cfg,
	}

	// Sort universe tokens so scan order is deterministic across runs.
	// Go map iteration is random — without this, different stocks get
	// picked when multiple qualify on the same day, giving different results.
	tokens := make([]uint32, 0, len(universe))
	for token := range universe {
		tokens = append(tokens, token)
	}
	sort.Slice(tokens, func(i, j int) bool { return tokens[i] < tokens[j] })

	// Determine simulation length from the token with the most bars
	totalBars := 0
	for _, token := range tokens {
		if closes, ok := cache.Closes[token]; ok && len(closes) > totalBars {
			totalBars = len(closes)
		}
	}
	if totalBars < config.EMA20Period+20 {
		result.Trades = []Trade{}
		result.EquityCurve = []float64{cfg.Capital}
		return result
	}

	startBar := totalBars - cfg.StartBarOffset
	if startBar < config.EMA20Period+10 {
		startBar = config.EMA20Period + 10
	}
	result.TotalBars = totalBars - startBar

	var closedTrades []Trade
	openPositions := make(map[uint32]*simPosition)
	equityCurve := []float64{cfg.Capital}
	capital := cfg.Capital

	// ── Live-engine risk state (mirrors ExecutionAgent + ScannerAgent) ──
	capitalMultiplier := 1.0
	consecSLs := 0
	consecutiveWins := 0
	peakCapital := cfg.Capital
	drawdownHalted := false

	for day := startBar; day < totalBars-1; day++ {
		// ── Recompute maxPos based on current capital (dynamic, same as live) ──
		if cfg.MaxPositions > 0 {
			maxPos = cfg.MaxPositions
		} else {
			maxPos = config.ComputeMaxPositions(capital)
		}

		// ── Manage open positions — process in deterministic token order ──
		// Go map iteration is random; sorting ensures same params → same result.
		openTokens := make([]uint32, 0, len(openPositions))
		for token := range openPositions {
			openTokens = append(openTokens, token)
		}
		sort.Slice(openTokens, func(i, j int) bool { return openTokens[i] < openTokens[j] })

		for _, token := range openTokens {
			pos := openPositions[token]
			exit, reason := checkExit(cache, token, pos, day, cfg)
			if exit > 0 {
				t := buildTrade(pos, day, exit, reason, cache.Closes[token], cache.TradingDates)
				capital += t.NetPnl
				closedTrades = append(closedTrades, t)
				delete(openPositions, token)

				// ── Mirror live RecordSLHit / RecordWin + recovery ladder ──
				if reason == "HARD_SL" {
					result.SLHits++
					consecSLs++
					consecutiveWins = 0
					switch {
					case consecSLs >= config.ConsecutiveSLCutoff:
						if capitalMultiplier > config.ReducedCapitalPct {
							capitalMultiplier = config.ReducedCapitalPct
						}
					case consecSLs >= 3:
						if capitalMultiplier > 0.50 {
							capitalMultiplier = 0.50
						}
					}
				} else {
					consecSLs = 0
					consecutiveWins++
					switch {
					case consecutiveWins >= 7 && capitalMultiplier < 1.0:
						capitalMultiplier = 1.0
					case consecutiveWins >= 5 && capitalMultiplier < 0.80:
						capitalMultiplier = 0.80
					case consecutiveWins >= 3 && capitalMultiplier < 0.60:
						capitalMultiplier = 0.60
					}
				}

				// ── Mirror drawdown halt ──
				if capital > peakCapital {
					peakCapital = capital
				}
				if peakCapital > 0 {
					dd := (peakCapital - capital) / peakCapital * 100
					drawdownHalted = dd >= config.MaxDrawdownHaltPct
				}
			}
		}

		// ── Drawdown halt — no new entries (mirrors ExecutionAgent) ──
		if drawdownHalted {
			result.DrawdownHalts++
			equityCurve = append(equityCurve, capital+unrealizedAt(cache, openPositions, day))
			continue
		}

		// ── Underwater gate — ≥2 positions below entry = no new entries ──
		underwaterCount := 0
		for _, pos := range openPositions {
			if closes, ok := cache.Closes[pos.token]; ok && day < len(closes) {
				if closes[day] < pos.entryPrice {
					underwaterCount++
				}
			}
		}
		if underwaterCount >= 2 {
			result.UnderwaterBlocks++
			equityCurve = append(equityCurve, capital+unrealizedAt(cache, openPositions, day))
			continue
		}

		// ── Scan for new entries ──
		if len(openPositions) < maxPos {
			type candidate struct {
				token    uint32
				symbol   string
				strat    string
				nextOpen float64
				sl       float64
				qty      int
				volSpike float64
			}
			var candidates []candidate

			for _, token := range tokens {
				symbol := universe[token]
				if _, open := openPositions[token]; open {
					continue
				}
				closes, cOk := cache.Closes[token]
				highs, hOk := cache.Highs[token]
				lows, lOk := cache.Lows[token]
				volumes, vOk := cache.Volumes[token]
				if !cOk || !hOk || !lOk || !vOk {
					continue
				}
				minLen := min4(len(closes), len(highs), len(lows), len(volumes))
				if day >= minLen || day < config.EMA20Period+5 {
					continue
				}

				oSlice := ([]float64)(nil)
				if opens := cache.Opens[token]; opens != nil && day+1 <= len(opens) {
					oSlice = opens[:day+1]
				}
				cSlice := closes[:day+1]
				hSlice := highs[:day+1]
				lSlice := lows[:day+1]
				vSlice := volumes[:day+1]

				// ATH filter
				const lookback52W = 252
				athWindow := cSlice
				if len(athWindow) > lookback52W {
					athWindow = athWindow[len(athWindow)-lookback52W:]
				}
				high52 := athWindow[0]
				for _, c := range athWindow {
					if c > high52 {
						high52 = c
					}
				}
				ltp := cSlice[len(cSlice)-1]
				if high52 > 0 && (high52-ltp)/high52*100 > config.ATHProximityPct {
					continue
				}

				strat := detectSignalAt(oSlice, cSlice, hSlice, lSlice, vSlice, cfg)
				if strat == "" {
					continue
				}
				if cfg.Strategy != "ALL" && cfg.Strategy != strat {
					continue
				}

				if day+1 >= len(closes) {
					continue
				}
				opens := cache.Opens[token]
				var nextOpen float64
				if opens != nil && day+1 < len(opens) && opens[day+1] > 0 {
					nextOpen = opens[day+1]
				} else {
					nextOpen = closes[day+1]
				}
				entryPrice := nextOpen * (1 + cfg.SlippagePct/100)

				prevLow := lSlice[len(lSlice)-2]
				sl := computeSL(entryPrice, prevLow, cfg)
				if sl >= entryPrice {
					continue
				}

				// ── Risk-based sizing (mirrors computeRiskBasedQty in live engine) ──
				riskPerShare := entryPrice - sl
				riskAmount := capital * capitalMultiplier * config.RiskPerTradePct / 100
				qty := 0
				if riskPerShare > 0 {
					qty = int(riskAmount / riskPerShare)
				}
				// Floor: minimum lot
				minQty := int(config.MinAbsPositionSize / entryPrice)
				if minQty < 1 {
					minQty = 1
				}
				if qty < minQty {
					qty = minQty
				}
				// Ceiling: MaxTradeAllocPct of current capital
				maxQty := int(capital * capitalMultiplier * cfg.MaxTradeAllocPct / 100 / entryPrice)
				if maxQty < 1 {
					maxQty = 1
				}
				if qty > maxQty {
					qty = maxQty
				}
				if qty < 1 {
					continue
				}

				volSpike := 1.0
				if day >= 20 {
					var avg float64
					for i := day - 20; i < day; i++ {
						avg += volumes[i]
					}
					avg /= 20
					if avg > 0 {
						volSpike = volumes[day] / avg
					}
				}

				candidates = append(candidates, candidate{
					token: token, symbol: symbol, strat: strat,
					nextOpen: entryPrice, sl: sl, qty: qty, volSpike: volSpike,
				})
			}

			result.RawSignals += len(candidates)

			// ── Open risk cap check before entering (mirrors live engine) ──
			openRisk := 0.0
			for _, pos := range openPositions {
				openRisk += (pos.entryPrice - pos.sl) * float64(pos.qty)
			}

			// ── Rank by volume spike, enter top N respecting all caps ──
			sort.Slice(candidates, func(i, j int) bool {
				return candidates[i].volSpike > candidates[j].volSpike
			})

			for _, c := range candidates {
				if len(openPositions) >= maxPos {
					break
				}
				// Open risk cap (mirrors live engine Ch.8 p.194)
				tradeRisk := (c.nextOpen - c.sl) * float64(c.qty)
				if capital > 0 && (openRisk+tradeRisk)/capital*100 >= config.MaxOpenRiskPct {
					result.OpenRiskBlocks++
					continue
				}
				openPositions[c.token] = &simPosition{
					token: c.token, symbol: c.symbol, strategy: c.strat,
					entryBar: day + 1, entryPrice: c.nextOpen, sl: c.sl, qty: c.qty,
				}
				openRisk += tradeRisk
			}
		}

		equityCurve = append(equityCurve, capital+unrealizedAt(cache, openPositions, day))
	}

	// Force-close remaining positions at end of simulation — deterministic order
	lastBar := totalBars - 2
	endTokens := make([]uint32, 0, len(openPositions))
	for token := range openPositions {
		endTokens = append(endTokens, token)
	}
	sort.Slice(endTokens, func(i, j int) bool { return endTokens[i] < endTokens[j] })
	for _, token := range endTokens {
		pos := openPositions[token]
		closes := cache.Closes[token]
		if lastBar < len(closes) {
			t := buildTrade(pos, lastBar, closes[lastBar], "BACKTEST_END", closes, cache.TradingDates)
			capital += t.NetPnl
			closedTrades = append(closedTrades, t)
		}
	}
	equityCurve = append(equityCurve, capital)

	// ── Statistics ────────────────────────────────────────────
	result.Trades = closedTrades
	result.TotalTrades = len(closedTrades)
	result.EquityCurve = equityCurve

	var netWin, netLoss, totalCharges, totalGross float64
	maxConsec, curConsec, totalHold := 0, 0, 0

	for _, t := range closedTrades {
		totalHold += t.HoldingBars
		totalCharges += t.Charges
		totalGross += t.GrossPnl
		if t.NetPnl > 0 {
			result.Wins++
			netWin += t.NetPnl
			curConsec = 0
		} else {
			result.Losses++
			netLoss += math.Abs(t.NetPnl)
			curConsec++
			if curConsec > maxConsec {
				maxConsec = curConsec
			}
		}
	}

	result.MaxConsecLosses = maxConsec
	result.TotalGrossPnl = math.Round(totalGross*100) / 100
	result.TotalCharges = math.Round(totalCharges*100) / 100
	result.TotalPnl = capital - cfg.Capital // net (charges already deducted in capital tracking)
	result.ReturnPct = result.TotalPnl / cfg.Capital * 100
	if result.TotalTrades > 0 {
		result.WinRate = float64(result.Wins) / float64(result.TotalTrades) * 100
		result.AvgHoldingBars = float64(totalHold) / float64(result.TotalTrades)
	}
	if result.Wins > 0 {
		result.AvgWin = netWin / float64(result.Wins)
	}
	if result.Losses > 0 {
		result.AvgLoss = netLoss / float64(result.Losses)
	}
	if netLoss > 0 {
		result.ProfitFactor = netWin / netLoss
	}

	// Max drawdown
	peak := equityCurve[0]
	for _, v := range equityCurve {
		if v > peak {
			peak = v
		}
		if peak > 0 {
			dd := (peak - v) / peak * 100
			if dd > result.MaxDrawdown {
				result.MaxDrawdown = dd
			}
		}
	}

	// Annualized Sharpe (daily returns, √252 scaling)
	if len(equityCurve) > 1 {
		rets := make([]float64, 0, len(equityCurve)-1)
		for i := 1; i < len(equityCurve); i++ {
			if equityCurve[i-1] > 0 {
				rets = append(rets, (equityCurve[i]-equityCurve[i-1])/equityCurve[i-1])
			}
		}
		m, s := meanStd(rets)
		if s > 0 {
			result.SharpeRatio = m / s * math.Sqrt(252)
		}
	}

	log.Printf("[Backtest] Complete: %d trades | WinRate=%.1f%% | Gross=%.0f Charges=%.0f Net=%.0f | Return=%.1f%% | MaxDD=%.1f%% | Sharpe=%.2f",
		result.TotalTrades, result.WinRate,
		result.TotalGrossPnl, result.TotalCharges, result.TotalPnl,
		result.ReturnPct, result.MaxDrawdown, result.SharpeRatio)
	return result
}

// ── Signal detection ─────────────────────────────────────────────

// detectSignalAt checks the engine's sole entry setup on pre-sliced OHLCV arrays.
// Returns the strategy name if the EMA pullback/bounce fired, or "".
// Uses the same detection math as the live scanner (agents.DetectEMAPullbackFromSlice).
func detectSignalAt(opens, closes, highs, lows, volumes []float64, cfg Config) string {
	strat := cfg.Strategy
	if strat == "ALL" || strat == "EMA_PULLBACK" {
		if _, formed := agents.DetectEMAPullbackFromSlice(opens, closes, highs, lows, volumes); formed {
			return "EMA_PULLBACK"
		}
	}
	return ""
}

// ── Exit logic ───────────────────────────────────────────────────

func checkExit(cache *agents.DailyCache, token uint32, pos *simPosition, day int, cfg Config) (float64, string) {
	closes := cache.Closes[token]
	if day >= len(closes) {
		return 0, ""
	}

	ltp := closes[day]

	// Hard SL — exit at the SL price, not the EOD close.
	// In real CNC/GTT trading the stop triggers intraday when price hits pos.sl.
	// Using ltp (EOD close) as exit price overstates the loss when a stock gaps
	// down or closes well below the trigger level.
	if ltp <= pos.sl {
		return pos.sl, "HARD_SL"
	}

	// EMA20 exit — Book Ch.6 p.167-168: "Closed below EMA — Sell on this day."
	// Single EOD close below 20 EMA = exit immediately. No red-candle filter.
	if day >= config.EMA20Period {
		ema20 := computeLastEMA(closes[:day+1], config.EMA20Period)
		if ema20 > 0 && ltp < ema20 {
			return ltp, "EMA20_EXIT"
		}
	}

	return 0, ""
}

// ── Math helpers ─────────────────────────────────────────────────

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

func computeEMASeries(closes []float64, period int) []float64 {
	if len(closes) < period {
		return nil
	}
	result := make([]float64, len(closes))
	k := 2.0 / float64(period+1)
	seed := 0.0
	for i := 0; i < period; i++ {
		seed += closes[i]
	}
	result[period-1] = seed / float64(period)
	for i := period; i < len(closes); i++ {
		result[i] = closes[i]*k + result[i-1]*(1-k)
	}
	return result[period-1:]
}

func computeLastEMA(closes []float64, period int) float64 {
	s := computeEMASeries(closes, period)
	if len(s) == 0 {
		return 0
	}
	return s[len(s)-1]
}

func computeSL(entry, prevLow float64, cfg Config) float64 {
	structural := prevLow * 0.998
	floor := entry * (1 - cfg.SLFloorPct/100)
	ceiling := entry * (1 - cfg.SLCeilingPct/100)
	sl := structural
	if sl > floor {
		sl = floor
	}
	if sl < ceiling {
		sl = ceiling
	}
	return sl
}

// computeCharges returns the total transaction cost for one round trip on NSE equity
// delivery (CNC) via Zerodha. Applied to both wins and losses — charges are unavoidable.
//
// Zerodha CNC breakdown (per round trip):
//   STT         0.100% of sell value              (Securities Transaction Tax)
//   ETC         0.00335% of buy + sell value      (Exchange Transaction Charge, NSE equity)
//   SEBI        0.0001% of buy + sell value        (SEBI turnover fee)
//   Stamp       0.015% of buy value               (Stamp duty — varies by state; using standard)
//   Brokerage   ₹0                                (Zerodha CNC is zero brokerage)
//   GST         18% on (ETC + SEBI + brokerage)   (Goods & Services Tax)
//
// Total ≈ 0.13–0.16% per round trip on a flat trade. Rises slightly on winning trades
// (sell value is higher → more STT) and falls on losing trades.
func computeCharges(entryPrice, exitPrice float64, qty int) float64 {
	buyValue := entryPrice * float64(qty)
	sellValue := exitPrice * float64(qty)
	turnover := buyValue + sellValue

	stt := 0.001 * sellValue           // 0.1% on sell only
	etc := 0.0000335 * turnover        // 0.00335% both sides
	sebi := 0.000001 * turnover        // 0.0001% both sides
	stamp := 0.00015 * buyValue        // 0.015% on buy only
	brokerage := 0.0                   // Zerodha CNC: free
	gst := 0.18 * (etc + sebi + brokerage)

	total := stt + etc + sebi + stamp + gst
	return math.Round(total*100) / 100
}

func buildTrade(pos *simPosition, exitBar int, exitPrice float64, reason string, closes []float64, dates []string) Trade {
	grossPnl := (exitPrice - pos.entryPrice) * float64(pos.qty)
	charges := computeCharges(pos.entryPrice, exitPrice, pos.qty)
	netPnl := grossPnl - charges
	pnlPct := 0.0
	if pos.entryPrice > 0 {
		pnlPct = netPnl / (pos.entryPrice * float64(pos.qty)) * 100
	}

	entryDate, exitDate := "", ""
	if pos.entryBar < len(dates) {
		entryDate = dates[pos.entryBar]
	}
	if exitBar < len(dates) {
		exitDate = dates[exitBar]
	}

	sliceStart := pos.entryBar - 3
	if sliceStart < 0 {
		sliceStart = 0
	}
	sliceEnd := exitBar + 3
	if sliceEnd >= len(closes) {
		sliceEnd = len(closes) - 1
	}
	var priceSlice []float64
	if sliceEnd >= sliceStart && sliceEnd < len(closes) {
		priceSlice = make([]float64, sliceEnd-sliceStart+1)
		copy(priceSlice, closes[sliceStart:sliceEnd+1])
	}

	return Trade{
		Symbol: pos.symbol, Strategy: pos.strategy,
		EntryBar: pos.entryBar, ExitBar: exitBar,
		EntryDate: entryDate, ExitDate: exitDate,
		EntryPrice: pos.entryPrice, ExitPrice: exitPrice, SL: pos.sl,
		Qty: pos.qty, GrossPnl: grossPnl, Charges: charges, NetPnl: netPnl, PnlPct: pnlPct,
		ExitReason: reason, HoldingBars: exitBar - pos.entryBar, Token: pos.token,
		PriceSlice: priceSlice, PriceSliceStart: sliceStart,
	}
}

func unrealizedAt(cache *agents.DailyCache, positions map[uint32]*simPosition, day int) float64 {
	total := 0.0
	for token, pos := range positions {
		if closes, ok := cache.Closes[token]; ok && day < len(closes) {
			total += float64(pos.qty) * (closes[day] - pos.entryPrice)
		}
	}
	return total
}

func meanStd(vals []float64) (float64, float64) {
	if len(vals) == 0 {
		return 0, 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	mean := sum / float64(len(vals))
	variance := 0.0
	for _, v := range vals {
		d := v - mean
		variance += d * d
	}
	return mean, math.Sqrt(variance / float64(len(vals)))
}

// ══════════════════════════════════════════════════════════════
//  Persistence
// ══════════════════════════════════════════════════════════════

func initBacktestTable(db *sql.DB) {
	db.Exec(`CREATE TABLE IF NOT EXISTS backtest_results (
		id           TEXT PRIMARY KEY,
		run_at       TEXT NOT NULL,
		config_json  TEXT NOT NULL,
		summary_json TEXT NOT NULL,
		trades_json  TEXT NOT NULL,
		equity_json  TEXT NOT NULL
	)`)
}

// SaveResult persists a backtest result to the journal DB.
func SaveResult(r *Result) error {
	db, err := sql.Open("sqlite", config.JournalDB)
	if err != nil {
		return err
	}
	defer db.Close()
	db.Exec("PRAGMA journal_mode=WAL")
	initBacktestTable(db)

	cfgB, _ := json.Marshal(r.Config)
	sumB, _ := json.Marshal(map[string]interface{}{
		"total_trades": r.TotalTrades, "wins": r.Wins, "losses": r.Losses,
		"win_rate": r.WinRate,
		"total_gross_pnl": r.TotalGrossPnl, "total_charges": r.TotalCharges,
		"total_pnl": r.TotalPnl, "return_pct": r.ReturnPct,
		"max_drawdown": r.MaxDrawdown, "sharpe_ratio": r.SharpeRatio,
		"profit_factor": r.ProfitFactor, "avg_win": r.AvgWin, "avg_loss": r.AvgLoss,
		"max_consec_losses": r.MaxConsecLosses, "avg_holding_bars": r.AvgHoldingBars,
	})
	trdB, _ := json.Marshal(r.Trades)
	eqB, _ := json.Marshal(r.EquityCurve)

	_, err = db.Exec(
		`INSERT OR REPLACE INTO backtest_results (id, run_at, config_json, summary_json, trades_json, equity_json) VALUES (?,?,?,?,?,?)`,
		r.ID, r.RunAt, string(cfgB), string(sumB), string(trdB), string(eqB),
	)
	return err
}

// HistorySummary is a lightweight record for the history list.
type HistorySummary struct {
	ID          string  `json:"id"`
	RunAt       string  `json:"run_at"`
	Config      Config  `json:"config"`
	TotalTrades int     `json:"total_trades"`
	Wins        int     `json:"wins"`
	Losses      int     `json:"losses"`
	WinRate     float64 `json:"win_rate"`
	TotalPnl    float64 `json:"total_pnl"`
	ReturnPct   float64 `json:"return_pct"`
	MaxDrawdown float64 `json:"max_drawdown"`
	SharpeRatio float64 `json:"sharpe_ratio"`
}

// LoadHistory returns the last N backtest runs newest-first.
func LoadHistory(limit int) ([]HistorySummary, error) {
	db, err := sql.Open("sqlite", config.JournalDB)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.Exec("PRAGMA journal_mode=WAL")
	initBacktestTable(db)

	rows, err := db.Query(
		`SELECT id, run_at, config_json, summary_json FROM backtest_results ORDER BY run_at DESC LIMIT ?`, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []HistorySummary
	for rows.Next() {
		var id, runAt, cfgJSON, sumJSON string
		if err := rows.Scan(&id, &runAt, &cfgJSON, &sumJSON); err != nil {
			continue
		}
		var cfg Config
		var sum map[string]interface{}
		json.Unmarshal([]byte(cfgJSON), &cfg)
		json.Unmarshal([]byte(sumJSON), &sum)

		hs := HistorySummary{ID: id, RunAt: runAt, Config: cfg}
		if sum != nil {
			hs.TotalTrades = int(toF(sum["total_trades"]))
			hs.Wins = int(toF(sum["wins"]))
			hs.Losses = int(toF(sum["losses"]))
			hs.WinRate = toF(sum["win_rate"])
			hs.TotalPnl = toF(sum["total_pnl"]) // net
			hs.ReturnPct = toF(sum["return_pct"])
			hs.MaxDrawdown = toF(sum["max_drawdown"])
			hs.SharpeRatio = toF(sum["sharpe_ratio"])
		}
		out = append(out, hs)
	}
	return out, nil
}

func min4(a, b, c, d int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	if d < m {
		m = d
	}
	return m
}

func toF(v interface{}) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	}
	return 0
}
