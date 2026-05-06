package main

import (
	"database/sql"
	"fmt"
	"log"

	_ "modernc.org/sqlite"
)

func main() {
	dbPath := `c:\Users\Admin\.gemini\antigravity\scratch\bnf_go_engine\data\journal.db`
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	fmt.Println("═══ ALL SIGNALS FOUND ON MAY 5 ═══")
	rows, err := db.Query(`
		SELECT time, detail 
		FROM agent_logs 
		WHERE date='2026-05-05' AND action='SIGNAL_FOUND'
		ORDER BY time
	`)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	
	signalCount := 0
	stratCount := map[string]int{}
	for rows.Next() {
		var t, detail string
		rows.Scan(&t, &detail)
		fmt.Printf("  %s: %s\n", t, detail)
		signalCount++
		// Extract strategy name from detail
		parts := []byte(detail)
		strategy := ""
		for i, c := range parts {
			if c == ' ' && i > 0 {
				strategy = string(parts[:i])
				break
			}
		}
		stratCount[strategy]++
	}

	fmt.Printf("\nTotal signals: %d\n", signalCount)
	fmt.Println("\nBy strategy:")
	for s, c := range stratCount {
		fmt.Printf("  %s: %d\n", s, c)
	}

	// Check daily summary
	fmt.Println("\n═══ DAILY SUMMARY MAY 5 ═══")
	rows2, err := db.Query("SELECT * FROM daily_summary WHERE date='2026-05-05'")
	if err == nil {
		defer rows2.Close()
		cols, _ := rows2.Columns()
		fmt.Printf("  Columns: %v\n", cols)
		vals := make([]interface{}, len(cols))
		valPtrs := make([]interface{}, len(cols))
		for i := range vals {
			valPtrs[i] = &vals[i]
		}
		for rows2.Next() {
			rows2.Scan(valPtrs...)
			for i, col := range cols {
				fmt.Printf("  %s = %v\n", col, vals[i])
			}
		}
	}

	// Check regime changes
	fmt.Println("\n═══ REGIME CHANGES MAY 5 ═══")
	rows3, err := db.Query(`
		SELECT time, detail 
		FROM agent_logs 
		WHERE date='2026-05-05' AND action='REGIME_CHANGE'
		ORDER BY time
	`)
	if err == nil {
		defer rows3.Close()
		for rows3.Next() {
			var t, detail string
			rows3.Scan(&t, &detail)
			fmt.Printf("  %s: %s\n", t, detail)
		}
	}

	// Check ALL unique actions on May 5
	fmt.Println("\n═══ ALL ACTIONS MAY 5 ═══")
	rows4, err := db.Query(`
		SELECT action, COUNT(*) as cnt 
		FROM agent_logs 
		WHERE date='2026-05-05' 
		GROUP BY action 
		ORDER BY cnt DESC
	`)
	if err == nil {
		defer rows4.Close()
		for rows4.Next() {
			var action string
			var cnt int
			rows4.Scan(&action, &cnt)
			fmt.Printf("  %s: %d\n", action, cnt)
		}
	}

	// Check execution-related logs
	fmt.Println("\n═══ EXECUTION/RISK LOGS MAY 5 ═══")
	rows5, err := db.Query(`
		SELECT time, agent, action, detail 
		FROM agent_logs 
		WHERE date='2026-05-05' AND agent != 'Scanner'
		ORDER BY time
	`)
	if err == nil {
		defer rows5.Close()
		for rows5.Next() {
			var t, agent, action, detail string
			rows5.Scan(&t, &agent, &action, &detail)
			fmt.Printf("  %s [%s] %s: %s\n", t, agent, action, detail)
		}
	}
}
