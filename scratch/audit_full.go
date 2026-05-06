package main

import (
	"database/sql"
	"fmt"
	"log"
	_ "modernc.org/sqlite"
)

func main() {
	db, err := sql.Open("sqlite", `c:\Users\Admin\.gemini\antigravity\scratch\bnf_go_engine\data\journal.db`)
	if err != nil { log.Fatal(err) }
	defer db.Close()

	// Check trades table schema
	fmt.Println("═══ TRADES TABLE SCHEMA ═══")
	rows0, err := db.Query("PRAGMA table_info(trades)")
	if err != nil { log.Fatal(err) }
	for rows0.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		rows0.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk)
		fmt.Printf("  %s (%s)\n", name, ctype)
	}
	rows0.Close()

	// Today's trades
	fmt.Println("\n═══ MAY 6 TRADES ═══")
	rows, err := db.Query(`SELECT symbol, strategy, entry_price, exit_price, qty, gross_pnl, exit_reason 
		FROM trades WHERE date='2026-05-06' ORDER BY entry_time`)
	if err != nil {
		fmt.Printf("  Error: %v\n", err)
	} else {
		for rows.Next() {
			var sym, strat, reason string
			var entry, exit, pnl float64
			var qty int
			rows.Scan(&sym, &strat, &entry, &exit, &qty, &pnl, &reason)
			fmt.Printf("  %s [%s] Entry=%.1f Exit=%.1f Qty=%d PnL=₹%.0f Exit=%s\n",
				sym, strat, entry, exit, qty, pnl, reason)
		}
		rows.Close()
	}

	// Signals found
	fmt.Println("\n═══ MAY 6 SIGNALS ═══")
	rows2, err := db.Query(`SELECT time, detail FROM agent_logs 
		WHERE date='2026-05-06' AND action='SIGNAL_FOUND' ORDER BY time`)
	if err != nil {
		fmt.Printf("  Error: %v\n", err)
	} else {
		cnt := 0
		for rows2.Next() {
			var t, d string
			rows2.Scan(&t, &d)
			fmt.Printf("  %s: %s\n", t, d)
			cnt++
		}
		rows2.Close()
		fmt.Printf("Total signals: %d\n", cnt)
	}

	// Regime changes
	fmt.Println("\n═══ MAY 6 REGIMES ═══")
	rows3, err := db.Query(`SELECT time, detail FROM agent_logs 
		WHERE date='2026-05-06' AND action='REGIME_CHANGE' ORDER BY time`)
	if err != nil {
		fmt.Printf("  Error: %v\n", err)
	} else {
		for rows3.Next() {
			var t, d string
			rows3.Scan(&t, &d)
			fmt.Printf("  %s: %s\n", t, d)
		}
		rows3.Close()
	}

	// Action summary
	fmt.Println("\n═══ MAY 6 ACTIONS ═══")
	rows4, err := db.Query(`SELECT action, COUNT(*) FROM agent_logs 
		WHERE date='2026-05-06' GROUP BY action ORDER BY COUNT(*) DESC`)
	if err != nil {
		fmt.Printf("  Error: %v\n", err)
	} else {
		for rows4.Next() {
			var a string
			var c int
			rows4.Scan(&a, &c)
			fmt.Printf("  %s: %d\n", a, c)
		}
		rows4.Close()
	}

	// FULL HISTORY by date
	fmt.Println("\n═══ FULL TRADE HISTORY BY DATE ═══")
	rows5, err := db.Query(`SELECT date, COUNT(*), 
		SUM(CASE WHEN gross_pnl > 0 THEN 1 ELSE 0 END),
		ROUND(SUM(gross_pnl), 0)
		FROM trades GROUP BY date ORDER BY date`)
	if err != nil {
		fmt.Printf("  Error: %v\n", err)
	} else {
		var grandTotal float64
		var grandTrades int
		for rows5.Next() {
			var d string
			var trades, wins int
			var pnl float64
			rows5.Scan(&d, &trades, &wins, &pnl)
			grandTotal += pnl
			grandTrades += trades
			wr := float64(wins) / float64(trades) * 100
			fmt.Printf("  %s: %2d trades  %2d wins  WR=%2.0f%%  PnL=₹%+6.0f  Running=₹%+.0f\n", 
				d, trades, wins, wr, pnl, grandTotal)
		}
		rows5.Close()
		fmt.Printf("\n  TOTAL: %d trades, PnL=₹%.0f\n", grandTrades, grandTotal)
	}

	// Strategy performance ALL TIME
	fmt.Println("\n═══ STRATEGY P&L (ALL TIME) ═══")
	rows6, err := db.Query(`SELECT strategy, COUNT(*), 
		SUM(CASE WHEN gross_pnl > 0 THEN 1 ELSE 0 END),
		ROUND(SUM(gross_pnl), 0)
		FROM trades GROUP BY strategy ORDER BY SUM(gross_pnl) DESC`)
	if err != nil {
		fmt.Printf("  Error: %v\n", err)
	} else {
		for rows6.Next() {
			var s string
			var t, w int
			var p float64
			rows6.Scan(&s, &t, &w, &p)
			wr := float64(w) / float64(t) * 100
			fmt.Printf("  %-20s %3d trades  %3d wins  WR=%2.0f%%  PnL=₹%+6.0f\n", s, t, w, wr, p)
		}
		rows6.Close()
	}

	// Check what Nifty 50 references exist
	fmt.Println("\n═══ NIFTY 50 USAGE AUDIT ═══")
	fmt.Println("  (Run grep separately for code audit)")
}
