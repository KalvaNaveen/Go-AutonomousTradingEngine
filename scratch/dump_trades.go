package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

func main() {
	dbPath := "journal.db"
	if len(os.Args) > 1 {
		dbPath = os.Args[1]
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Find the most recent trading date
	var latestDate string
	err = db.QueryRow("SELECT date FROM trades ORDER BY date DESC LIMIT 1").Scan(&latestDate)
	if err != nil {
		log.Fatal("No trades found:", err)
	}

	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Println("  TRADE AUDIT REPORT")
	fmt.Println("═══════════════════════════════════════════════════════════════")
	fmt.Printf("Latest trading date in DB: %s\n", latestDate)
	fmt.Printf("Report generated: %s\n\n", time.Now().Format("2006-01-02 15:04:05"))

	// List all available dates
	fmt.Println("── ALL TRADING DATES ──")
	drows, _ := db.Query("SELECT date, COUNT(*), SUM(gross_pnl) FROM trades GROUP BY date ORDER BY date DESC")
	for drows.Next() {
		var d string
		var cnt int
		var pnl float64
		drows.Scan(&d, &cnt, &pnl)
		fmt.Printf("  %s  | Trades: %d  | Gross P&L: ₹%.2f\n", d, cnt, pnl)
	}
	drows.Close()

	fmt.Println()

	// Dump ALL trades for latest date (full detail)
	fmt.Printf("── DETAILED TRADES FOR %s ──\n", latestDate)
	fmt.Println("─────────────────────────────────────────────────────────────────────────────────────────────────────────────")
	fmt.Printf("%-4s | %-12s | %-8s | %-9s | %-9s | %-5s | %-10s | %-12s | %-20s | %-20s\n",
		"#", "SYMBOL", "STRATEGY", "ENTRY", "EXIT", "QTY", "GROSS_PNL", "EXIT_REASON", "ENTRY_TIME", "EXIT_TIME")
	fmt.Println("─────────────────────────────────────────────────────────────────────────────────────────────────────────────")

	rows, err := db.Query(`
		SELECT COALESCE(symbol,''), COALESCE(strategy,''), COALESCE(entry_price,0), 
		       COALESCE(full_exit_price,0), COALESCE(gross_pnl,0), COALESCE(exit_reason,''),
		       COALESCE(qty,0), COALESCE(entry_time,''), COALESCE(timestamp,''),
			   COALESCE(regime,''), COALESCE(rvol,0), COALESCE(hold_minutes,0),
			   COALESCE(stop_hit,0), COALESCE(time_stop_hit,0),
			   COALESCE(deviation_pct,0), COALESCE(daily_pnl_after,0)
		FROM trades WHERE date = ? ORDER BY timestamp ASC
	`, latestDate)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()

	var totalPnl float64
	var wins, losses, stops, timeStops int
	var totalQty int
	stratCount := map[string]int{}
	stratPnl := map[string]float64{}
	symbolCount := map[string]int{}
	symbolPnl := map[string]float64{}

	type tradeRow struct {
		sym, strat, reason, entryTime, exitTime, regime string
		ep, xp, pnl, rvol, hold, devPct, dailyPnlAfter float64
		qty, stopHit, timeStopHit                        int
	}
	var allTrades []tradeRow
	i := 0

	for rows.Next() {
		var t tradeRow
		rows.Scan(&t.sym, &t.strat, &t.ep, &t.xp, &t.pnl, &t.reason,
			&t.qty, &t.entryTime, &t.exitTime, &t.regime, &t.rvol, &t.hold,
			&t.stopHit, &t.timeStopHit, &t.devPct, &t.dailyPnlAfter)
		allTrades = append(allTrades, t)
		i++

		fmt.Printf("%-4d | %-12s | %-8s | %-9.2f | %-9.2f | %-5d | %-10.2f | %-12s | %-20s | %-20s\n",
			i, t.sym, t.strat, t.ep, t.xp, t.qty, t.pnl, t.reason,
			truncTime(t.entryTime), truncTime(t.exitTime))

		totalPnl += t.pnl
		totalQty += t.qty
		if t.pnl > 0 {
			wins++
		} else {
			losses++
		}
		if t.stopHit == 1 {
			stops++
		}
		if t.timeStopHit == 1 {
			timeStops++
		}
		stratCount[t.strat]++
		stratPnl[t.strat] += t.pnl
		symbolCount[t.sym]++
		symbolPnl[t.sym] += t.pnl
	}

	fmt.Println("─────────────────────────────────────────────────────────────────────────────────────────────────────────────")

	fmt.Println()
	fmt.Println("═══ SUMMARY ═══")
	fmt.Printf("Total Trades: %d  |  Wins: %d  |  Losses: %d  |  Win Rate: %.1f%%\n",
		len(allTrades), wins, losses, float64(wins)/float64(max(len(allTrades),1))*100)
	fmt.Printf("Gross P&L: ₹%.2f  |  Total Qty: %d\n", totalPnl, totalQty)
	fmt.Printf("Stop Hits: %d  |  Time Stops: %d\n", stops, timeStops)

	fmt.Println()
	fmt.Println("═══ STRATEGY BREAKDOWN ═══")
	for strat, count := range stratCount {
		fmt.Printf("  %-12s  | Trades: %d  |  P&L: ₹%.2f  |  Avg P&L/trade: ₹%.2f\n",
			strat, count, stratPnl[strat], stratPnl[strat]/float64(count))
	}

	fmt.Println()
	fmt.Println("═══ SYMBOL BREAKDOWN ═══")
	for sym, count := range symbolCount {
		fmt.Printf("  %-12s  | Trades: %d  |  P&L: ₹%.2f\n", sym, count, symbolPnl[sym])
	}

	// Sanity checks
	fmt.Println()
	fmt.Println("═══ SANITY / FRAUD CHECKS ═══")

	// Check 1: Are entry/exit prices realistic (non-zero, entry != exit for profitable trades)?
	suspicious := 0
	for _, t := range allTrades {
		if t.ep <= 0 || t.xp <= 0 {
			fmt.Printf("  ⚠️ SUSPICIOUS: %s %s has zero price (entry=%.2f exit=%.2f)\n", t.strat, t.sym, t.ep, t.xp)
			suspicious++
		}

		// Verify PnL math: (exit - entry) * qty should equal gross_pnl for long
		expectedPnl := (t.xp - t.ep) * float64(t.qty)
		// For short trades, it would be (entry - exit) * qty
		expectedPnlShort := (t.ep - t.xp) * float64(t.qty)

		if abs(t.pnl-expectedPnl) > 0.01 && abs(t.pnl-expectedPnlShort) > 0.01 {
			fmt.Printf("  ⚠️ PNL MISMATCH: %s %s pnl=%.2f expected_long=%.2f expected_short=%.2f\n",
				t.strat, t.sym, t.pnl, expectedPnl, expectedPnlShort)
			suspicious++
		}

		// Check for unrealistically large profits (> ₹500 per trade with small qty)
		if t.pnl > 500 && t.qty <= 5 {
			pctMove := abs(t.xp-t.ep) / t.ep * 100
			fmt.Printf("  ⚠️ LARGE PROFIT: %s %s pnl=₹%.2f with qty=%d (%.2f%% move)\n",
				t.strat, t.sym, t.pnl, t.qty, pctMove)
			suspicious++
		}

		// Check hold time (< 1 minute is suspicious)
		if t.hold < 1 && t.hold > 0 {
			fmt.Printf("  ⚠️ MICRO HOLD: %s %s held for %.1f minutes\n", t.strat, t.sym, t.hold)
			suspicious++
		}
	}

	if suspicious == 0 {
		fmt.Println("  ✅ No obvious anomalies found in P&L calculations")
	}

	// Check: Price movement plausibility
	fmt.Println()
	fmt.Println("═══ PRICE MOVE ANALYSIS (for market verification) ═══")
	for _, t := range allTrades {
		pctMove := (t.xp - t.ep) / t.ep * 100
		fmt.Printf("  %s: entry=₹%.2f → exit=₹%.2f  (%.2f%% move, %.1f min hold, RVol=%.2f, Regime=%s)\n",
			t.sym, t.ep, t.xp, pctMove, t.hold, t.rvol, t.regime)
	}

	// Check: daily_pnl_after progression
	fmt.Println()
	fmt.Println("═══ CUMULATIVE P&L PROGRESSION ═══")
	runningPnl := 0.0
	for i, t := range allTrades {
		runningPnl += t.pnl
		marker := ""
		if abs(runningPnl-t.dailyPnlAfter) > 1 && t.dailyPnlAfter != 0 {
			marker = " ⚠️ MISMATCH with recorded daily_pnl_after"
		}
		fmt.Printf("  Trade %d: +₹%.2f → Running=₹%.2f (DB recorded: ₹%.2f)%s\n",
			i+1, t.pnl, runningPnl, t.dailyPnlAfter, marker)
	}
}

func truncTime(s string) string {
	if len(s) > 19 {
		return s[:19]
	}
	return s
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
