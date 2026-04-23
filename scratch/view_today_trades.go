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

	fmt.Println("=== ALL TRADES FOR 2026-04-22 ===")
	fmt.Printf("%-4s %-14s %-8s %-15s %-10s %-10s %-10s %-4s %-20s %-10s\n",
		"ID", "SYMBOL", "STRATEGY", "ENTRY TIME", "ENTRY", "EXIT", "PnL", "QTY", "EXIT_REASON", "MINS")
	fmt.Println("---- -------------- -------- --------------- ---------- ---------- ---------- ---- -------------------- ----------")

	rows, _ := db.Query(`SELECT id, symbol, strategy, entry_time, entry_price, full_exit_price, gross_pnl, qty, exit_reason, hold_minutes
		FROM trades WHERE date = '2026-04-22' ORDER BY entry_time`)
	if rows == nil {
		fmt.Println("No rows returned from query")
		return
	}
	defer rows.Close()

	totalPnl := 0.0
	count := 0
	for rows.Next() {
		var id, qty int
		var symbol, strategy, entryTime, exitReason string
		var entryPrice, exitPrice, pnl, holdMin float64
		
		err := rows.Scan(&id, &symbol, &strategy, &entryTime, &entryPrice, &exitPrice, &pnl, &qty, &exitReason, &holdMin)
		if err != nil {
			fmt.Println("error scanning:", err)
			continue
		}
		
		// extract just the HH:MM:SS from entry_time if it's long
		shortTime := entryTime
		if len(entryTime) > 19 {
			shortTime = entryTime[11:19]
		}
		
		fmt.Printf("%-4d %-14s %-8s %-15s %-10.2f %-10.2f %-10.2f %-4d %-20s %-6.0f\n",
			id, symbol, strategy, shortTime, entryPrice, exitPrice, pnl, qty, exitReason, holdMin)
		totalPnl += pnl
		count++
	}
	
	fmt.Printf("\nTotal Trades: %d | Total PnL: %.2f\n", count, totalPnl)
}
