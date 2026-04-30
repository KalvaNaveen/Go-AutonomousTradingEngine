package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	dbPath := `c:\Users\Admin\.gemini\antigravity\scratch\bnf_go_engine\data\journal.db`
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
	defer db.Close()

	// Get column names for trades
	fmt.Println("=== TRADES TABLE SCHEMA ===")
	rows, _ := db.Query("PRAGMA table_info(trades)")
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt sql.NullString
		var pk int
		rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk)
		fmt.Printf("  %s (%s)\n", name, typ)
	}
	rows.Close()

	// Get agent_logs schema and recent entries
	fmt.Println("\n=== AGENT_LOGS TABLE SCHEMA ===")
	rows, _ = db.Query("PRAGMA table_info(agent_logs)")
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt sql.NullString
		var pk int
		rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk)
		fmt.Printf("  %s (%s)\n", name, typ)
	}
	rows.Close()

	// Agent logs from April 27-28 
	fmt.Println("\n=== AGENT LOGS (April 27-28, last 40) ===")
	rows, err = db.Query(`SELECT * FROM agent_logs ORDER BY id DESC LIMIT 40`)
	if err != nil {
		fmt.Println("Error:", err)
	} else {
		cols, _ := rows.Columns()
		fmt.Println("  Columns:", cols)
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals { ptrs[i] = &vals[i] }
		for rows.Next() {
			rows.Scan(ptrs...)
			for i, c := range cols {
				if vals[i].Valid {
					fmt.Printf("  %s=%s", c, vals[i].String)
				}
			}
			fmt.Println()
		}
		rows.Close()
	}

	// Last trades with correct columns
	fmt.Println("\n=== LAST 15 TRADES ===")
	rows, err = db.Query(`SELECT id, strategy, symbol, COALESCE(gross_pnl, 0), COALESCE(exit_reason, ''),
		COALESCE(regime, ''), COALESCE(timestamp, ''), COALESCE(entry_time, '')
		FROM trades ORDER BY id DESC LIMIT 15`)
	if err != nil {
		fmt.Println("Error:", err)
	} else {
		for rows.Next() {
			var id int
			var strat, sym, exit, regime, ts, entry string
			var pnl float64
			rows.Scan(&id, &strat, &sym, &pnl, &exit, &regime, &ts, &entry)
			fmt.Printf("  #%d %s %s PnL=%.1f Exit=%s Regime=%s Entry=%s TS=%s\n", id, strat, sym, pnl, exit, regime, entry, ts)
		}
		rows.Close()
	}

	// Daily summary
	fmt.Println("\n=== DAILY SUMMARY TABLE ===")
	rows, err = db.Query(`SELECT * FROM daily_summary ORDER BY rowid DESC LIMIT 10`)
	if err != nil {
		fmt.Println("Error:", err)
	} else {
		cols, _ := rows.Columns()
		fmt.Println("  Columns:", cols)
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals { ptrs[i] = &vals[i] }
		for rows.Next() {
			rows.Scan(ptrs...)
			for i, c := range cols {
				if vals[i].Valid {
					fmt.Printf("  %s=%s", c, vals[i].String)
				}
			}
			fmt.Println()
		}
		rows.Close()
	}

	// Summary: 4/28
	fmt.Println("\n=== TRADES ON 2026-04-28 ===")
	var count int
	db.QueryRow(`SELECT COUNT(*) FROM trades WHERE date(COALESCE(entry_time, timestamp)) = '2026-04-28'`).Scan(&count)
	fmt.Printf("  Count: %d\n", count)

	// 4/27 trades detail
	fmt.Println("\n=== TRADES ON 2026-04-27 (detail) ===")
	rows, _ = db.Query(`SELECT strategy, symbol, gross_pnl, exit_reason, regime, entry_time, qty, entry_price, full_exit_price
		FROM trades WHERE date(COALESCE(entry_time, timestamp)) = '2026-04-27'`)
	for rows.Next() {
		var strat, sym, exit, regime, entry string
		var pnl, entryP, exitP float64
		var qty int
		rows.Scan(&strat, &sym, &pnl, &exit, &regime, &entry, &qty, &entryP, &exitP)
		fmt.Printf("  %s %s Qty=%d Entry=%.1f Exit=%.1f PnL=%.1f Reason=%s Regime=%s Time=%s\n",
			strat, sym, qty, entryP, exitP, pnl, exit, regime, entry)
	}
	rows.Close()
}
