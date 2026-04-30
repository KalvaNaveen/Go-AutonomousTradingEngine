package main

import (
	"database/sql"
	"fmt"
	"log"
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

	// 1. Signals found per strategy
	fmt.Println("═══ SIGNALS FOUND (by strategy) ═══")
	rows, _ := db.Query(`
		SELECT SUBSTR(details, 1, INSTR(details, ' ')-1) as strat, COUNT(*) 
		FROM agent_activity 
		WHERE date(timestamp)=? AND action='SIGNAL_FOUND'
		GROUP BY strat ORDER BY COUNT(*) DESC`, today)
	for rows.Next() {
		var strat string
		var cnt int
		rows.Scan(&strat, &cnt)
		fmt.Printf("  %-25s: %d signals\n", strat, cnt)
	}
	rows.Close()

	// 2. Trades opened per strategy
	fmt.Println("\n═══ TRADES OPENED (by strategy) ═══")
	rows2, _ := db.Query(`
		SELECT SUBSTR(details, 1, INSTR(details, ' ')-1) as strat, COUNT(*) 
		FROM agent_activity 
		WHERE date(timestamp)=? AND action='TRADE_OPENED'
		GROUP BY strat ORDER BY COUNT(*) DESC`, today)
	for rows2.Next() {
		var strat string
		var cnt int
		rows2.Scan(&strat, &cnt)
		fmt.Printf("  %-25s: %d trades\n", strat, cnt)
	}
	rows2.Close()

	// 3. Check for ML rejections
	fmt.Println("\n═══ ML REJECTIONS ═══")
	var mlCount int
	db.QueryRow(`SELECT COUNT(*) FROM agent_activity 
		WHERE date(timestamp)=? AND details LIKE '%ML%REJECTED%'`, today).Scan(&mlCount)
	fmt.Printf("  ML rejections: %d\n", mlCount)

	// 4. Check for OrderFlow blocks
	fmt.Println("\n═══ ORDERFLOW BLOCKS ═══")
	var ofCount int
	db.QueryRow(`SELECT COUNT(*) FROM agent_activity 
		WHERE date(timestamp)=? AND details LIKE '%OrderFlow%WEAK%'`, today).Scan(&ofCount)
	fmt.Printf("  OrderFlow blocks: %d\n", ofCount)

	// 5. Check for VaR blocks
	var varCount int
	db.QueryRow(`SELECT COUNT(*) FROM agent_activity 
		WHERE date(timestamp)=? AND details LIKE '%VaR%BLOCKED%'`, today).Scan(&varCount)
	fmt.Printf("  VaR blocks: %d\n", varCount)

	// 6. Check for Balance blocks
	var balCount int
	db.QueryRow(`SELECT COUNT(*) FROM agent_activity 
		WHERE date(timestamp)=? AND details LIKE '%Balance%SKIPPED%'`, today).Scan(&balCount)
	fmt.Printf("  Balance blocks: %d\n", balCount)

	// 7. Risk rejections
	fmt.Println("\n═══ RISK REJECTIONS ═══")
	rows3, _ := db.Query(`
		SELECT details, COUNT(*) 
		FROM agent_activity 
		WHERE date(timestamp)=? AND action='TRADE_REJECTED'
		GROUP BY details ORDER BY COUNT(*) DESC LIMIT 20`, today)
	for rows3.Next() {
		var details string
		var cnt int
		rows3.Scan(&details, &cnt)
		fmt.Printf("  %s: %d\n", details, cnt)
	}
	rows3.Close()

	// 8. All SIGNAL_FOUND entries (first 30)
	fmt.Println("\n═══ ALL SIGNALS FOUND (first 30) ═══")
	rows4, _ := db.Query(`
		SELECT timestamp, details 
		FROM agent_activity 
		WHERE date(timestamp)=? AND action='SIGNAL_FOUND'
		ORDER BY timestamp LIMIT 30`, today)
	for rows4.Next() {
		var ts, details string
		rows4.Scan(&ts, &details)
		timePart := ts
		if len(ts) > 16 {
			timePart = ts[11:16]
		}
		fmt.Printf("  %s  %s\n", timePart, details)
	}
	rows4.Close()

	// 9. Check the log for filter keywords — scan general log entries
	fmt.Println("\n═══ FILTER/BLOCK/REJECT LOG ENTRIES ═══")
	keywords := []string{"REJECTED", "BLOCKED", "SKIPPED", "KILL", "COOLDOWN", "WEAK"}
	for _, kw := range keywords {
		var cnt int
		db.QueryRow(`SELECT COUNT(*) FROM agent_activity 
			WHERE date(timestamp)=? AND details LIKE ?`, today, "%"+kw+"%").Scan(&cnt)
		if cnt > 0 {
			fmt.Printf("  '%s' mentions: %d\n", kw, cnt)

			// Show first 5 examples
			rows5, _ := db.Query(`SELECT timestamp, agent, action, SUBSTR(details,1,120) 
				FROM agent_activity WHERE date(timestamp)=? AND details LIKE ?
				ORDER BY timestamp LIMIT 5`, today, "%"+kw+"%")
			for rows5.Next() {
				var ts, agent, action, det string
				rows5.Scan(&ts, &agent, &action, &det)
				tp := ts
				if len(ts) > 16 {
					tp = ts[11:16]
				}
				fmt.Printf("    %s [%s] %s: %s\n", tp, agent, action, det)
			}
			rows5.Close()
		}
	}

	// 10. Total unique signals vs unique trades
	fmt.Println("\n═══ SIGNAL → TRADE CONVERSION ═══")
	var totalSignals int
	db.QueryRow(`SELECT COUNT(*) FROM agent_activity WHERE date(timestamp)=? AND action='SIGNAL_FOUND'`, today).Scan(&totalSignals)
	var totalOpened int
	db.QueryRow(`SELECT COUNT(*) FROM agent_activity WHERE date(timestamp)=? AND action='TRADE_OPENED'`, today).Scan(&totalOpened)
	fmt.Printf("  Signals found: %d\n", totalSignals)
	fmt.Printf("  Trades opened: %d\n", totalOpened)
	if totalSignals > 0 {
		fmt.Printf("  Conversion:    %.1f%%\n", float64(totalOpened)/float64(totalSignals)*100)
	}

	// 11. Check if ML filter was even trained
	fmt.Println("\n═══ ENGINE STARTUP LOGS ═══")
	rows6, _ := db.Query(`SELECT timestamp, details FROM agent_activity 
		WHERE date(timestamp)=? AND (details LIKE '%ML Filter%' OR details LIKE '%WFO%' OR details LIKE '%VaR%' OR details LIKE '%capital%')
		ORDER BY timestamp LIMIT 15`, today)
	for rows6.Next() {
		var ts, details string
		rows6.Scan(&ts, &details)
		tp := ts
		if len(ts) > 16 {
			tp = ts[11:16]
		}
		// Truncate long details
		if len(details) > 100 {
			details = details[:100]
		}
		fmt.Printf("  %s  %s\n", tp, details)
	}
	rows6.Close()

	// 12. Regime timeline
	fmt.Println("\n═══ REGIME TIMELINE ═══")
	rows7, _ := db.Query(`SELECT timestamp, details FROM agent_activity 
		WHERE date(timestamp)=? AND action='REGIME_CHANGE'
		ORDER BY timestamp`, today)
	for rows7.Next() {
		var ts, details string
		rows7.Scan(&ts, &details)
		tp := ts
		if len(ts) > 16 {
			tp = ts[11:16]
		}
		fmt.Printf("  %s  %s\n", tp, details)
	}
	rows7.Close()

	// 13. Check what log output looks like for ML/OrderFlow etc (these go to stdout, not journal)
	fmt.Println("\n═══ NOTE ═══")
	fmt.Println("  ML rejections, OrderFlow blocks, VaR blocks, and Balance skips")
	fmt.Println("  are logged via log.Printf() not journal.LogAgentActivity()")
	fmt.Println("  They appear in STDOUT/engine.log, NOT in journal.db")
	fmt.Println("  Check engine log file for [ML] REJECTED, [OrderFlow] WEAK, [VaR] BLOCKED, [Balance] SKIPPED")

	// 14. Check if engine.log exists
	logPaths := []string{
		homeDir + "/.gemini/antigravity/scratch/bnf_go_engine/engine.log",
		homeDir + "/.gemini/antigravity/scratch/bnf_go_engine/data/engine.log",
		homeDir + "/.gemini/antigravity/scratch/bnf_go_engine/logs/engine.log",
	}
	for _, p := range logPaths {
		if _, err := os.Stat(p); err == nil {
			fmt.Printf("\n  Found log file: %s\n", p)
			// Read last 100 lines
			data, _ := os.ReadFile(p)
			lines := strings.Split(string(data), "\n")
			start := len(lines) - 100
			if start < 0 {
				start = 0
			}
			fmt.Println("  Last 30 lines with filter keywords:")
			count := 0
			for _, line := range lines[start:] {
				if strings.Contains(line, "REJECTED") || strings.Contains(line, "BLOCKED") ||
					strings.Contains(line, "WEAK") || strings.Contains(line, "SKIPPED") ||
					strings.Contains(line, "[ML]") || strings.Contains(line, "[VaR]") ||
					strings.Contains(line, "[OrderFlow]") || strings.Contains(line, "[Balance]") {
					fmt.Printf("    %s\n", line)
					count++
					if count >= 30 {
						break
					}
				}
			}
		}
	}
}
