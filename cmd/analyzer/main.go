package main

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"bnf_go_engine/config"

	_ "modernc.org/sqlite"
)

type Trade struct {
	ID         int
	Symbol     string
	Strategy   string
	EntryTime  string
	EntryPrice float64
	ExitPrice  float64
	PnL        float64
	Qty        int
	ExitReason string
	HoldMins   float64
}

func main() {
	config.Reload()

	dateStr := config.TodayIST().Format("2006-01-02")
	if len(os.Args) > 1 {
		dateStr = os.Args[1]
	}

	dbPath := filepath(config.BaseDir, "data", "journal.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fmt.Printf("Error opening database at %s: %v\n", dbPath, err)
		os.Exit(1)
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT id, symbol, strategy, entry_time, entry_price, full_exit_price, gross_pnl, qty, exit_reason, hold_minutes
		FROM trades WHERE date = ? ORDER BY entry_time`, dateStr)
	if err != nil {
		fmt.Println("Error executing query:", err)
		return
	}
	defer rows.Close()

	var trades []Trade
	for rows.Next() {
		var t Trade
		err := rows.Scan(&t.ID, &t.Symbol, &t.Strategy, &t.EntryTime, &t.EntryPrice, &t.ExitPrice, &t.PnL, &t.Qty, &t.ExitReason, &t.HoldMins)
		if err == nil {
			trades = append(trades, t)
		}
	}

	if len(trades) == 0 {
		fmt.Printf("No trades found for %s. (Engine didn't trade or was off)\n", dateStr)
		return
	}

	fmt.Printf("\n══════════════════════════════════════════════════════════\n")
	fmt.Printf("  POST-MARKET ANALYSIS REPORT — %s\n", dateStr)
	fmt.Printf("══════════════════════════════════════════════════════════\n\n")

	// 1. Overall Metrics
	var totalPnL, grossWin, grossLoss float64
	wins, losses := 0, 0
	for _, t := range trades {
		totalPnL += t.PnL
		if t.PnL > 0 {
			wins++
			grossWin += t.PnL
		} else {
			losses++
			grossLoss += math.Abs(t.PnL)
		}
	}

	winRate := 0.0
	if len(trades) > 0 {
		winRate = float64(wins) / float64(len(trades)) * 100
	}
	pf := 0.0
	if grossLoss > 0 {
		pf = grossWin / grossLoss
	}

	fmt.Printf("▶ DAILY SUMMARY\n")
	fmt.Printf("  Total PnL      : ₹%.2f\n", totalPnL)
	fmt.Printf("  Total Trades   : %d\n", len(trades))
	fmt.Printf("  Win / Loss     : %d W / %d L (%.1f%% Win Rate)\n", wins, losses, winRate)
	fmt.Printf("  Profit Factor  : %.2f\n", pf)
	fmt.Println()

	// 2. Strategy Breakdown
	stratPnl := make(map[string]float64)
	stratWins := make(map[string]int)
	stratLosses := make(map[string]int)
	stratExitReasons := make(map[string]map[string]int)

	symbolLosses := make(map[string]float64)
	symbolLossCount := make(map[string]int)

	for _, t := range trades {
		stratPnl[t.Strategy] += t.PnL
		if t.PnL > 0 {
			stratWins[t.Strategy]++
		} else {
			stratLosses[t.Strategy]++
			symbolLosses[t.Symbol] += t.PnL
			symbolLossCount[t.Symbol]++

			if stratExitReasons[t.Strategy] == nil {
				stratExitReasons[t.Strategy] = make(map[string]int)
			}
			stratExitReasons[t.Strategy][t.ExitReason]++
		}
	}

	fmt.Printf("▶ STRATEGY BREAKDOWN\n")
	for s, pnl := range stratPnl {
		w := stratWins[s]
		l := stratLosses[s]
		total := w + l
		wr := 0.0
		if total > 0 {
			wr = float64(w) / float64(total) * 100
		}
		fmt.Printf("  %-16s : PnL: %6.0f | Trades: %2d | Win Rate: %4.0f%%\n", s, pnl, total, wr)
	}
	fmt.Println()

	// 3. Loss Diagnostics
	fmt.Printf("▶ LOSS DIAGNOSTICS & RECOMMENDATIONS\n")
	hasRecommendations := false

	for s, lCount := range stratLosses {
		if lCount >= 2 {
			fmt.Printf("  [⚠] %s had %d losses.\n", s, lCount)
			
			// Analyze exit reasons
			reasons := stratExitReasons[s]
			for reason, rCount := range reasons {
				if strings.Contains(reason, "STOP_LOSS") && rCount >= 2 {
					fmt.Printf("      → %d losses due to Stop Loss hit. Recommendation: Market may be too choppy for %s today, or consider widening the SL (ATR multiplier).\n", rCount, s)
					hasRecommendations = true
				}
				if strings.Contains(reason, "EOD_SQUAREOFF") && rCount >= 1 {
					fmt.Printf("      → %d losses due to EOD Squareoff. Recommendation: This trade didn't move. Consider filtering out low-momentum setups or restricting late entries for %s.\n", rCount, s)
					hasRecommendations = true
				}
				if strings.Contains(reason, "TRAILING_SL") && rCount >= 2 {
					fmt.Printf("      → %d losses due to Trailing SL. Recommendation: You were likely in profit but got wicked out. Consider loosening trailing step or locking partials earlier.\n", rCount)
					hasRecommendations = true
				}
			}
		}
	}

	// 4. Choppy Symbols Analysis
	type symLoss struct {
		sym   string
		loss  float64
		count int
	}
	var slList []symLoss
	for sym, loss := range symbolLosses {
		if symbolLossCount[sym] >= 2 {
			slList = append(slList, symLoss{sym, loss, symbolLossCount[sym]})
		}
	}
	sort.Slice(slList, func(i, j int) bool {
		return slList[i].loss < slList[j].loss // More negative first
	})

	if len(slList) > 0 {
		fmt.Printf("\n  [⚠] Toxic Symbols Detected (Multiple Losses):\n")
		for _, sl := range slList {
			fmt.Printf("      → %-10s : %d losses, Total drag: ₹%.0f. Recommendation: Remove from Universe if trend continues.\n", sl.sym, sl.count, sl.loss)
			hasRecommendations = true
		}
	}

	if !hasRecommendations {
		if totalPnL > 0 {
			fmt.Printf("  [✅] Excellent day. No major systematic bleed detected. Continue as normal.\n")
		} else {
			fmt.Printf("  [ℹ] Normal trading distribution. Single losses across strategies. No systemic weakness detected.\n")
		}
	}

	fmt.Printf("\n══════════════════════════════════════════════════════════\n")
}

func filepath(base, folder, file string) string {
	return base + string(os.PathSeparator) + folder + string(os.PathSeparator) + file
}
