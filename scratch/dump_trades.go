package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	db, err := sql.Open("sqlite", `data/journal.db`)
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
	defer db.Close()

	fmt.Println("=== ALL S6_TREND_SHORT TRADES (ALL TIME) ===")
	fmt.Printf("%-4s %-14s %-10s %-10s %-10s %-10s %-4s %-20s %-3s %-10s %-6s\n",
		"#", "SYMBOL", "ENTRY", "EXIT", "PnL", "REGIME", "QTY", "EXIT_REASON", "SL?", "DATE", "HOLD_M")
	fmt.Println("---- -------------- ---------- ---------- ---------- ---------- ---- -------------------- --- ---------- ------")

	rows, _ := db.Query(`SELECT id, symbol, entry_price, full_exit_price, gross_pnl, regime, qty, exit_reason, stop_hit, date, hold_minutes, rvol, deviation_pct
		FROM trades WHERE strategy = 'S6_TREND_SHORT' ORDER BY entry_time`)
	if rows == nil {
		fmt.Println("No rows")
		return
	}
	defer rows.Close()

	totalPnl := 0.0
	wins, losses := 0, 0
	var slHits, timeDecays, targetHits, preemptive, maxHold, netProfit int
	for rows.Next() {
		var id, qty, stopHit int
		var symbol, regime, exitReason, date string
		var entryPrice, exitPrice, pnl, holdMin, rvol, devPct float64
		rows.Scan(&id, &symbol, &entryPrice, &exitPrice, &pnl, &regime, &qty, &exitReason, &stopHit, &date, &holdMin, &rvol, &devPct)
		sl := ""
		if stopHit == 1 { sl = "SL" }
		fmt.Printf("%-4d %-14s %-10.2f %-10.2f %-10.2f %-10s %-4d %-20s %-3s %-10s %-6.0f RVol=%.1f\n",
			id, symbol, entryPrice, exitPrice, pnl, regime, qty, exitReason, sl, date, holdMin, rvol)
		totalPnl += pnl
		if pnl > 0 { wins++ } else { losses++ }
		switch exitReason {
		case "STOP_HIT": slHits++
		case "TIME_DECAY_EXIT": timeDecays++
		case "TARGET_HIT": targetHits++
		case "PREEMPTIVE_LOSS_EXIT": preemptive++
		case "MAX_HOLD_EXIT": maxHold++
		case "NET_PROFIT_EXIT": netProfit++
		}
	}
	fmt.Printf("\nTOTAL P&L: %.2f | W: %d | L: %d | WR: %.1f%%\n", totalPnl, wins, losses, float64(wins)/float64(wins+losses)*100)
	fmt.Printf("Exit breakdown: SL=%d TimeDecay=%d Target=%d Preemptive=%d MaxHold=%d NetProfit=%d\n",
		slHits, timeDecays, targetHits, preemptive, maxHold, netProfit)

	// Average winning vs losing trade
	var avgWin, avgLoss float64
	db.QueryRow(`SELECT AVG(gross_pnl) FROM trades WHERE strategy='S6_TREND_SHORT' AND gross_pnl > 0`).Scan(&avgWin)
	db.QueryRow(`SELECT AVG(gross_pnl) FROM trades WHERE strategy='S6_TREND_SHORT' AND gross_pnl <= 0`).Scan(&avgLoss)
	fmt.Printf("Avg Win: %.2f | Avg Loss: %.2f | Win/Loss ratio: %.2f\n", avgWin, avgLoss, avgWin/(-avgLoss))

	// Regime breakdown
	fmt.Println("\n=== BY REGIME ===")
	rows2, _ := db.Query(`SELECT regime, COUNT(*), SUM(gross_pnl), SUM(CASE WHEN gross_pnl>0 THEN 1 ELSE 0 END) FROM trades WHERE strategy='S6_TREND_SHORT' GROUP BY regime`)
	if rows2 != nil {
		defer rows2.Close()
		for rows2.Next() {
			var r string; var c, w int; var p float64
			rows2.Scan(&r, &c, &p, &w)
			fmt.Printf("  %-12s Trades=%d W=%d PnL=%.2f\n", r, c, w, p)
		}
	}

	// Symbols traded most
	fmt.Println("\n=== BY SYMBOL ===")
	rows3, _ := db.Query(`SELECT symbol, COUNT(*), SUM(gross_pnl) FROM trades WHERE strategy='S6_TREND_SHORT' GROUP BY symbol ORDER BY SUM(gross_pnl) ASC`)
	if rows3 != nil {
		defer rows3.Close()
		for rows3.Next() {
			var sym string; var c int; var p float64
			rows3.Scan(&sym, &c, &p)
			fmt.Printf("  %-14s Trades=%d PnL=%.2f\n", sym, c, p)
		}
	}
}
