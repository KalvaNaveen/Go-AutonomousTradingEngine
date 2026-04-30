package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	homeDir, _ := os.UserHomeDir()
	dbPath := homeDir + "/.gemini/antigravity/scratch/bnf_go_engine/data/journal.db"

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	today := "2026-04-29"

	// All trades detail
	fmt.Println("═══ ALL TRADES ═══")
	rows, err := db.Query(`
		SELECT COALESCE(entry_time,'?'), strategy, symbol, 
			CASE WHEN is_short=1 THEN 'SHORT' ELSE 'LONG' END as dir,
			entry_price, exit_price, qty, gross_pnl, 
			COALESCE(exit_reason,'?'), COALESCE(regime,'?'),
			COALESCE(rvol,0), COALESCE(deviation_pct,0)
		FROM trades WHERE date=? ORDER BY gross_pnl ASC`, today)
	if err != nil {
		log.Fatal(err)
	}
	for rows.Next() {
		var entryTime, strat, sym, dir, exitReason, regime string
		var entry, exit, pnl, rvol, devPct float64
		var qty int
		rows.Scan(&entryTime, &strat, &sym, &dir, &entry, &exit, &qty, &pnl, &exitReason, &regime, &rvol, &devPct)
		fmt.Printf("  %s | %s | %s | %s | Entry=%.2f Exit=%.2f Qty=%d | P&L=%.2f | %s | Regime=%s RVol=%.2f\n",
			entryTime, strat, sym, dir, entry, exit, qty, pnl, exitReason, regime, rvol)
	}
	rows.Close()

	// Exit reason breakdown
	fmt.Println("\n═══ EXIT REASONS ═══")
	rows2, err := db.Query(`
		SELECT COALESCE(exit_reason,'UNKNOWN'), COUNT(*), SUM(gross_pnl), AVG(gross_pnl)
		FROM trades WHERE date=? GROUP BY exit_reason ORDER BY SUM(gross_pnl) ASC`, today)
	if err != nil {
		log.Fatal(err)
	}
	for rows2.Next() {
		var reason string
		var cnt int
		var total, avg float64
		rows2.Scan(&reason, &cnt, &total, &avg)
		fmt.Printf("  %-20s: %d trades, Total=₹%.2f, Avg=₹%.2f\n", reason, cnt, total, avg)
	}
	rows2.Close()

	// Regime
	fmt.Println("\n═══ REGIME ═══")
	rows3, err := db.Query(`
		SELECT COALESCE(regime,'?'), COUNT(*), SUM(gross_pnl)
		FROM trades WHERE date=? GROUP BY regime`, today)
	if err != nil {
		log.Fatal(err)
	}
	for rows3.Next() {
		var regime string
		var cnt int
		var total float64
		rows3.Scan(&regime, &cnt, &total)
		fmt.Printf("  %s: %d trades, P&L=₹%.2f\n", regime, cnt, total)
	}
	rows3.Close()

	// Agent activity - key events
	fmt.Println("\n═══ KEY AGENT EVENTS (signals, kills, blocks) ═══")
	rows4, err := db.Query(`
		SELECT timestamp, agent, action, SUBSTR(details, 1, 120) 
		FROM agent_activity 
		WHERE date(timestamp)=?
		AND (action IN ('SIGNAL_FOUND', 'TRADE_OPENED', 'STRATEGY_KILL')
			OR details LIKE '%REJECTED%' OR details LIKE '%BLOCKED%' OR details LIKE '%KILL%')
		ORDER BY timestamp
		LIMIT 60`, today)
	if err != nil {
		log.Fatal(err)
	}
	for rows4.Next() {
		var ts, agent, action, details string
		rows4.Scan(&ts, &agent, &action, &details)
		fmt.Printf("  %s [%s] %s: %s\n", ts, agent, action, details)
	}
	rows4.Close()

	// Scan cycle counts
	fmt.Println("\n═══ SCAN STATS ═══")
	var scanCount int
	db.QueryRow(`SELECT COUNT(*) FROM agent_activity WHERE date(timestamp)=? AND action='SCAN_CYCLE'`, today).Scan(&scanCount)
	fmt.Printf("  Total scan cycles logged: %d\n", scanCount)

	var signalCount int
	db.QueryRow(`SELECT COUNT(*) FROM agent_activity WHERE date(timestamp)=? AND action='SIGNAL_FOUND'`, today).Scan(&signalCount)
	fmt.Printf("  Total signals found: %d\n", signalCount)

	var tradeOpenCount int
	db.QueryRow(`SELECT COUNT(*) FROM agent_activity WHERE date(timestamp)=? AND action='TRADE_OPENED'`, today).Scan(&tradeOpenCount)
	fmt.Printf("  Total trades opened: %d\n", tradeOpenCount)

	// Daily summary
	fmt.Println("\n═══ DAILY SUMMARY ═══")
	var dsTrades int
	var dsPnl, dsFees float64
	err = db.QueryRow(`SELECT COALESCE(total_trades,0), COALESCE(gross_pnl,0), COALESCE(total_fees,0) 
		FROM daily_summary WHERE date=?`, today).Scan(&dsTrades, &dsPnl, &dsFees)
	if err != nil {
		fmt.Printf("  No daily_summary for %s\n", today)
	} else {
		fmt.Printf("  Trades=%d, Gross=₹%.2f, Fees=₹%.2f, Net=₹%.2f\n", dsTrades, dsPnl, dsFees, dsPnl-dsFees)
	}
}
