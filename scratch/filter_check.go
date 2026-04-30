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
	if err != nil { log.Fatal(err) }
	defer db.Close()

	today := "2026-04-29"

	// 1. Action counts
	fmt.Println("═══ ACTION COUNTS ═══")
	r1, err := db.Query(`SELECT action, COUNT(*) FROM agent_logs WHERE date=? GROUP BY action ORDER BY COUNT(*) DESC`, today)
	if err != nil { log.Fatal(err) }
	for r1.Next() {
		var a string; var c int
		r1.Scan(&a, &c)
		fmt.Printf("  %-25s %d\n", a, c)
	}
	r1.Close()

	// 2. All signals found
	fmt.Println("\n═══ ALL SIGNALS FOUND ═══")
	r2, err := db.Query(`SELECT date||' '||time as ts, detail FROM agent_logs WHERE date=? AND action='SIGNAL_FOUND' ORDER BY time`, today)
	if err != nil { log.Fatal(err) }
	for r2.Next() {
		var ts, d string
		r2.Scan(&ts, &d)
		t := ts; if len(ts) > 16 { t = ts[11:16] }
		fmt.Printf("  %s  %s\n", t, d)
	}
	r2.Close()

	// 3. All trades opened
	fmt.Println("\n═══ ALL TRADES OPENED ═══")
	r3, err := db.Query(`SELECT date||' '||time as ts, detail FROM agent_logs WHERE date=? AND action='TRADE_OPENED' ORDER BY time`, today)
	if err != nil { log.Fatal(err) }
	for r3.Next() {
		var ts, d string
		r3.Scan(&ts, &d)
		t := ts; if len(ts) > 16 { t = ts[11:16] }
		fmt.Printf("  %s  %s\n", t, d)
	}
	r3.Close()

	// 4. Regime timeline
	fmt.Println("\n═══ REGIME CHANGES ═══")
	r4, err := db.Query(`SELECT date||' '||time as ts, detail FROM agent_logs WHERE date=? AND action='REGIME_CHANGE' ORDER BY time`, today)
	if err != nil { log.Fatal(err) }
	for r4.Next() {
		var ts, d string
		r4.Scan(&ts, &d)
		t := ts; if len(ts) > 16 { t = ts[11:16] }
		fmt.Printf("  %s  %s\n", t, d)
	}
	r4.Close()

	// 5. Search for filter/block entries in journal
	fmt.Println("\n═══ FILTER/BLOCK ENTRIES IN JOURNAL ═══")
	keywords := []string{"REJECTED", "BLOCKED", "SKIPPED", "KILL", "WEAK"}
	for _, kw := range keywords {
		var cnt int
		db.QueryRow(`SELECT COUNT(*) FROM agent_logs WHERE date=? AND detail LIKE ?`, today, "%"+kw+"%").Scan(&cnt)
		fmt.Printf("  '%s': %d entries\n", kw, cnt)
	}

	// 6. Check engine.log for filter blocks (ML, OrderFlow, VaR, Balance are logged to stdout)
	fmt.Println("\n═══ ENGINE LOG FILE SEARCH ═══")
	logPaths := []string{
		homeDir + "/.gemini/antigravity/scratch/bnf_go_engine/engine.log",
		homeDir + "/.gemini/antigravity/scratch/bnf_go_engine/nohup.out",
		homeDir + "/.gemini/antigravity/scratch/bnf_go_engine/output.log",
	}
	found := false
	for _, p := range logPaths {
		if info, err := os.Stat(p); err == nil {
			fmt.Printf("  Found: %s (%.1f KB)\n", p, float64(info.Size())/1024)
			found = true
			data, _ := os.ReadFile(p)
			lines := strings.Split(string(data), "\n")
			// Count filter blocks
			mlReject, ofBlock, varBlock, balBlock := 0, 0, 0, 0
			for _, l := range lines {
				if strings.Contains(l, "[ML] REJECTED") { mlReject++ }
				if strings.Contains(l, "[OrderFlow] WEAK") { ofBlock++ }
				if strings.Contains(l, "[VaR] BLOCKED") { varBlock++ }
				if strings.Contains(l, "[Balance] SKIPPED") { balBlock++ }
			}
			fmt.Printf("  [ML] REJECTED:       %d\n", mlReject)
			fmt.Printf("  [OrderFlow] WEAK:    %d\n", ofBlock)
			fmt.Printf("  [VaR] BLOCKED:       %d\n", varBlock)
			fmt.Printf("  [Balance] SKIPPED:   %d\n", balBlock)
			
			// Show last 20 filter lines
			fmt.Println("\n  Last 20 filter-related lines:")
			count := 0
			for i := len(lines) - 1; i >= 0 && count < 20; i-- {
				l := lines[i]
				if strings.Contains(l, "[ML]") || strings.Contains(l, "[OrderFlow]") || 
				   strings.Contains(l, "[VaR]") || strings.Contains(l, "[Balance]") ||
				   strings.Contains(l, "REJECTED") || strings.Contains(l, "BLOCKED") {
					fmt.Printf("    %s\n", l)
					count++
				}
			}
		}
	}
	if !found {
		fmt.Println("  No log files found. Engine output likely went to console (stdout).")
		fmt.Println("  ML/OrderFlow/VaR/Balance blocks are logged via log.Printf, not journal.db")
		fmt.Println("  → We need to redirect engine output: quantix_engine.exe > engine.log 2>&1")
	}

	// 7. Summary
	fmt.Println("\n═══ SIGNAL→TRADE FUNNEL ═══")
	var sigCount, tradeCount int
	db.QueryRow(`SELECT COUNT(*) FROM agent_logs WHERE date=? AND action='SIGNAL_FOUND'`, today).Scan(&sigCount)
	db.QueryRow(`SELECT COUNT(*) FROM agent_logs WHERE date=? AND action='TRADE_OPENED'`, today).Scan(&tradeCount)
	fmt.Printf("  Signals found:   %d\n", sigCount)
	fmt.Printf("  Trades executed: %d\n", tradeCount)
	fmt.Printf("  Blocked/filtered: %d\n", sigCount - tradeCount)
	if sigCount > 0 {
		fmt.Printf("  Conversion rate: %.1f%%\n", float64(tradeCount)/float64(sigCount)*100)
	}
}
