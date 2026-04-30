package main

import (
	"database/sql"
	"fmt"
	"log"
	"math"
	"os"
	"strings"

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

	fmt.Println("═══════════════════════════════════════════════════════════")
	fmt.Printf("  FORENSIC TRADE ANALYSIS — %s\n", today)
	fmt.Println("═══════════════════════════════════════════════════════════")

	// 1. Summary
	var totalTrades int
	var totalPnl float64
	var wins, losses int
	var totalWinAmt, totalLossAmt float64
	db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(gross_pnl),0) FROM trades WHERE date=?`, today).Scan(&totalTrades, &totalPnl)
	db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(gross_pnl),0) FROM trades WHERE date=? AND gross_pnl>0`, today).Scan(&wins, &totalWinAmt)
	db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(gross_pnl),0) FROM trades WHERE date=? AND gross_pnl<0`, today).Scan(&losses, &totalLossAmt)

	winRate := 0.0
	if totalTrades > 0 {
		winRate = float64(wins) / float64(totalTrades) * 100
	}
	avgWin := 0.0
	if wins > 0 {
		avgWin = totalWinAmt / float64(wins)
	}
	avgLoss := 0.0
	if losses > 0 {
		avgLoss = totalLossAmt / float64(losses)
	}

	fmt.Printf("\n📊 SUMMARY\n")
	fmt.Printf("  Total Trades:  %d\n", totalTrades)
	fmt.Printf("  Wins/Losses:   %d/%d (%.1f%% win rate)\n", wins, losses, winRate)
	fmt.Printf("  Gross P&L:     ₹%.2f\n", totalPnl)
	fmt.Printf("  Avg Win:       ₹%.2f\n", avgWin)
	fmt.Printf("  Avg Loss:      ₹%.2f\n", avgLoss)
	if avgLoss != 0 {
		fmt.Printf("  Risk:Reward:   1:%.2f\n", math.Abs(avgWin/avgLoss))
	}

	// 2. Per-strategy breakdown
	fmt.Println("\n📋 PER-STRATEGY BREAKDOWN")
	fmt.Println("  Strategy                | Trades | Wins | W%     | Gross P&L  | Avg Win  | Avg Loss")
	fmt.Println("  ─────────────────────────+────────+──────+────────+────────────+──────────+──────────")
	rows, _ := db.Query(`
		SELECT strategy, COUNT(*) as cnt, 
			SUM(CASE WHEN gross_pnl>0 THEN 1 ELSE 0 END) as w,
			SUM(gross_pnl) as total,
			COALESCE(AVG(CASE WHEN gross_pnl>0 THEN gross_pnl END),0) as avg_w,
			COALESCE(AVG(CASE WHEN gross_pnl<0 THEN gross_pnl END),0) as avg_l
		FROM trades WHERE date=? GROUP BY strategy ORDER BY total ASC`, today)
	defer rows.Close()
	for rows.Next() {
		var strat string
		var cnt, w int
		var total, avgW, avgL float64
		rows.Scan(&strat, &cnt, &w, &total, &avgW, &avgL)
		wr := 0.0
		if cnt > 0 {
			wr = float64(w) / float64(cnt) * 100
		}
		fmt.Printf("  %-24s| %6d | %4d | %5.1f%% | %10.2f | %8.2f | %8.2f\n",
			strat, cnt, w, wr, total, avgW, avgL)
	}

	// 3. Every trade detail
	fmt.Println("\n📝 ALL TRADES (sorted by P&L)")
	fmt.Println("  Time     | Strategy            | Symbol      | Dir   | Entry    | Exit     | Qty | P&L       | Exit Reason")
	fmt.Println("  ─────────+─────────────────────+─────────────+───────+──────────+──────────+─────+───────────+────────────")
	rows2, _ := db.Query(`
		SELECT COALESCE(entry_time,''), strategy, symbol, 
			CASE WHEN is_short=1 THEN 'SHORT' ELSE 'LONG' END,
			entry_price, exit_price, qty, gross_pnl, 
			COALESCE(exit_reason,'')
		FROM trades WHERE date=? ORDER BY gross_pnl ASC`, today)
	defer rows2.Close()
	for rows2.Next() {
		var entryTime, strat, sym, dir, exitReason string
		var entry, exit, pnl float64
		var qty int
		rows2.Scan(&entryTime, &strat, &sym, &dir, &entry, &exit, &qty, &pnl, &exitReason)
		// Extract just HH:MM from entry time
		timePart := entryTime
		if len(entryTime) > 16 {
			timePart = entryTime[11:16]
		}
		fmt.Printf("  %-9s| %-20s| %-12s| %-6s| %8.2f | %8.2f | %3d | %9.2f | %s\n",
			timePart, strat, sym, dir, entry, exit, qty, pnl, exitReason)
	}

	// 4. Exit reason breakdown
	fmt.Println("\n🚪 EXIT REASON ANALYSIS")
	rows3, _ := db.Query(`
		SELECT COALESCE(exit_reason,'UNKNOWN'), COUNT(*), SUM(gross_pnl), AVG(gross_pnl)
		FROM trades WHERE date=? GROUP BY exit_reason ORDER BY SUM(gross_pnl) ASC`, today)
	defer rows3.Close()
	for rows3.Next() {
		var reason string
		var cnt int
		var total, avg float64
		rows3.Scan(&reason, &cnt, &total, &avg)
		fmt.Printf("  %-20s: %d trades, Total=₹%.2f, Avg=₹%.2f\n", reason, cnt, total, avg)
	}

	// 5. Regime analysis
	fmt.Println("\n🌡️ REGIME ANALYSIS")
	rows4, _ := db.Query(`
		SELECT COALESCE(regime,'UNKNOWN'), COUNT(*), SUM(gross_pnl)
		FROM trades WHERE date=? GROUP BY regime`, today)
	defer rows4.Close()
	for rows4.Next() {
		var regime string
		var cnt int
		var total float64
		rows4.Scan(&regime, &cnt, &total)
		fmt.Printf("  %s: %d trades, P&L=₹%.2f\n", regime, cnt, total)
	}

	// 6. Hourly P&L
	fmt.Println("\n⏰ HOURLY P&L")
	rows5, _ := db.Query(`
		SELECT SUBSTR(entry_time, 12, 2) as hr, COUNT(*), SUM(gross_pnl)
		FROM trades WHERE date=? AND entry_time IS NOT NULL
		GROUP BY hr ORDER BY hr`, today)
	defer rows5.Close()
	for rows5.Next() {
		var hr string
		var cnt int
		var total float64
		rows5.Scan(&hr, &cnt, &total)
		bar := ""
		if total > 0 {
			bar = strings.Repeat("█", int(math.Min(total/50, 20)))
		} else {
			bar = strings.Repeat("░", int(math.Min(math.Abs(total)/50, 20)))
		}
		fmt.Printf("  %s:00  %3d trades  ₹%8.2f  %s\n", hr, cnt, total, bar)
	}

	// 7. Agent activity log summary
	fmt.Println("\n📡 AGENT ACTIVITY (key events)")
	rows6, _ := db.Query(`
		SELECT timestamp, agent, action, details 
		FROM agent_activity 
		WHERE date(timestamp)=? 
		AND (action IN ('STRATEGY_KILL', 'SIGNAL_FOUND', 'TRADE_OPENED', 'ENGINE_STOP', 'RISK_STOP')
			OR agent='ML' OR details LIKE '%REJECTED%' OR details LIKE '%BLOCKED%' OR details LIKE '%KILL%')
		ORDER BY timestamp
		LIMIT 50`, today)
	defer rows6.Close()
	for rows6.Next() {
		var ts, agent, action, details string
		rows6.Scan(&ts, &agent, &action, &details)
		timePart := ts
		if len(ts) > 16 {
			timePart = ts[11:16]
		}
		fmt.Printf("  %s [%s] %s: %s\n", timePart, agent, action, details)
	}

	// 8. Check daily_summary table
	fmt.Println("\n📊 DAILY SUMMARY (from engine)")
	var ds_trades int
	var ds_pnl, ds_fees float64
	err = db.QueryRow(`SELECT COALESCE(total_trades,0), COALESCE(gross_pnl,0), COALESCE(total_fees,0) 
		FROM daily_summary WHERE date=?`, today).Scan(&ds_trades, &ds_pnl, &ds_fees)
	if err != nil {
		fmt.Printf("  No daily_summary record found for %s\n", today)
	} else {
		fmt.Printf("  Trades: %d, Gross P&L: ₹%.2f, Fees: ₹%.2f, Net: ₹%.2f\n",
			ds_trades, ds_pnl, ds_fees, ds_pnl-ds_fees)
	}
}
