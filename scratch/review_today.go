// +build ignore

package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

func main() {
	exe, _ := os.Executable()
	baseDir := filepath.Dir(exe)
	if strings.Contains(baseDir, "go-build") || strings.Contains(baseDir, "Temp") {
		baseDir, _ = os.Getwd()
	}
	dbPath := filepath.Join(baseDir, "data", "journal.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		fmt.Printf("ERROR: %v\n", err)
		return
	}
	defer db.Close()

	today := "2026-05-07"

	// 1. All agent log entries for today — grouped by agent and action
	fmt.Println("═══ AGENT LOG SUMMARY (May 7) ═══")
	rows, _ := db.Query(`
		SELECT agent, action, COUNT(*) as cnt 
		FROM agent_logs 
		WHERE date=? 
		GROUP BY agent, action 
		ORDER BY cnt DESC`, today)
	for rows.Next() {
		var agent, action string
		var cnt int
		rows.Scan(&agent, &action, &cnt)
		fmt.Printf("  %-15s %-25s : %d entries\n", agent, action, cnt)
	}
	rows.Close()

	// 2. Signal generation logs — any mention of strategy names
	fmt.Println("\n═══ STRATEGY SIGNAL MENTIONS ═══")
	strategies := []string{"S1_", "S2_", "S3_", "S6_", "S8_", "S9_", "S10_", "S12_", "S13_", "S14_", "S15_"}
	for _, s := range strategies {
		var cnt int
		db.QueryRow(`SELECT COUNT(*) FROM agent_logs WHERE date=? AND detail LIKE ?`, today, "%"+s+"%").Scan(&cnt)
		fmt.Printf("  %-20s : %d log entries\n", s, cnt)
	}

	// 3. Look for SIGNAL, ENTRY, REJECT, SKIP, BLOCK mentions
	fmt.Println("\n═══ SIGNAL PIPELINE KEYWORDS ═══")
	keywords := []string{"SIGNAL", "ENTRY", "REJECT", "SKIP", "BLOCK", "KILL", "DISABLED", "APPROVE", "EXECUTE", "EXIT", "STOP", "TARGET", "REGIME"}
	for _, kw := range keywords {
		var cnt int
		db.QueryRow(`SELECT COUNT(*) FROM agent_logs WHERE date=? AND (detail LIKE ? OR action LIKE ?)`, today, "%"+kw+"%", "%"+kw+"%").Scan(&cnt)
		fmt.Printf("  %-15s : %d entries\n", kw, cnt)
	}

	// 4. Show signal-related logs (not SCAN_CYCLE spam)
	fmt.Println("\n═══ NON-SCAN-CYCLE LOGS (all interesting activity) ═══")
	rows2, _ := db.Query(`
		SELECT time, agent, action, detail 
		FROM agent_logs 
		WHERE date=? AND action != 'SCAN_CYCLE'
		ORDER BY time`, today)
	cnt := 0
	for rows2.Next() {
		var t, agent, action, detail string
		rows2.Scan(&t, &agent, &action, &detail)
		cnt++
		if len(detail) > 150 {
			detail = detail[:150] + "..."
		}
		fmt.Printf("  %s [%-10s] %-20s %s\n", t, agent, action, detail)
	}
	rows2.Close()
	fmt.Printf("  Total non-scan entries: %d\n", cnt)

	// 5. Show a sample of SCAN_CYCLE logs to see what strategies are being scanned
	fmt.Println("\n═══ SCAN_CYCLE SAMPLES (every 1000th) ═══")
	rows3, _ := db.Query(`
		SELECT time, detail 
		FROM agent_logs 
		WHERE date=? AND action='SCAN_CYCLE'
		ORDER BY time`, today)
	i := 0
	for rows3.Next() {
		var t, detail string
		rows3.Scan(&t, &detail)
		i++
		if i == 1 || i%1000 == 0 {
			fmt.Printf("  %s %s\n", t, detail)
		}
	}
	rows3.Close()
	fmt.Printf("  Total scan cycles: %d\n", i)

	// 6. Look for any signal generation or emission
	fmt.Println("\n═══ SIGNAL EMISSION LOGS ═══")
	rows4, _ := db.Query(`
		SELECT time, agent, action, detail 
		FROM agent_logs 
		WHERE date=? AND (
			action LIKE '%SIGNAL%' OR action LIKE '%EMIT%' OR action LIKE '%GENERATE%'
			OR detail LIKE '%signal%' OR detail LIKE '%emit%'
			OR detail LIKE '%candidate%' OR detail LIKE '%approved%'
		)
		ORDER BY time`, today)
	for rows4.Next() {
		var t, agent, action, detail string
		rows4.Scan(&t, &agent, &action, &detail)
		if len(detail) > 150 {
			detail = detail[:150] + "..."
		}
		fmt.Printf("  %s [%-10s] %-20s %s\n", t, agent, action, detail)
	}
	rows4.Close()

	// 7. Show ENTRY logs
	fmt.Println("\n═══ TRADE ENTRY/EXIT LOGS ═══")
	rows5, _ := db.Query(`
		SELECT time, agent, action, detail 
		FROM agent_logs 
		WHERE date=? AND (
			action LIKE '%ENTRY%' OR action LIKE '%EXIT%' OR action LIKE '%TRADE%' OR action LIKE '%ORDER%'
			OR action LIKE '%OPEN%' OR action LIKE '%CLOSE%'
			OR detail LIKE '%ENTRY%' OR detail LIKE '%placed%' OR detail LIKE '%filled%'
		)
		ORDER BY time`, today)
	for rows5.Next() {
		var t, agent, action, detail string
		rows5.Scan(&t, &agent, &action, &detail)
		if len(detail) > 180 {
			detail = detail[:180] + "..."
		}
		fmt.Printf("  %s [%-10s] %-20s %s\n", t, agent, action, detail)
	}
	rows5.Close()

	// 8. What regime was active at different times
	fmt.Println("\n═══ REGIME THROUGHOUT THE DAY ═══")
	rows6, _ := db.Query(`
		SELECT time, detail 
		FROM agent_logs 
		WHERE date=? AND detail LIKE '%Regime=%'
		ORDER BY time`, today)
	lastRegime := ""
	for rows6.Next() {
		var t, detail string
		rows6.Scan(&t, &detail)
		// Extract regime from "Regime=XXX"
		idx := strings.Index(detail, "Regime=")
		if idx >= 0 {
			regime := detail[idx+7:]
			if spaceIdx := strings.IndexAny(regime, " |"); spaceIdx >= 0 {
				regime = regime[:spaceIdx]
			}
			if regime != lastRegime {
				fmt.Printf("  %s Regime changed to: %s\n", t, regime)
				lastRegime = regime
			}
		}
	}
	rows6.Close()
}
