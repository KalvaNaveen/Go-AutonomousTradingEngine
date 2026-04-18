package main

import (
	"database/sql"
	"fmt"
	"log"
	"path/filepath"

	"bnf_go_engine/config"

	_ "modernc.org/sqlite"
)

func main() {
	dbPath := filepath.Join(config.BaseDir, "data", "historical.db")
	fmt.Println("DB:", dbPath)

	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=30000")
	if err != nil {
		log.Fatalf("Fatal: %v", err)
	}
	defer db.Close()

	// Check tables
	rows, _ := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	fmt.Println("Tables:")
	for rows.Next() {
		var name string
		rows.Scan(&name)
		fmt.Println("  ", name)
	}
	rows.Close()

	// Date range
	var minDT, maxDT sql.NullString
	var cnt int
	db.QueryRow("SELECT MIN(date_time), MAX(date_time), COUNT(*) FROM minute_data").Scan(&minDT, &maxDT, &cnt)
	fmt.Printf("minute_data: %d rows, from %s to %s\n", cnt, minDT.String, maxDT.String)

	var minD, maxD sql.NullString
	var cntD int
	db.QueryRow("SELECT MIN(date), MAX(date), COUNT(*) FROM daily_data").Scan(&minD, &maxD, &cntD)
	fmt.Printf("daily_data: %d rows, from %s to %s\n", cntD, minD.String, maxD.String)

	// Count distinct tokens in minute_data
	var tokenCnt int
	db.QueryRow("SELECT COUNT(DISTINCT token) FROM minute_data").Scan(&tokenCnt)
	fmt.Printf("Distinct tokens in minute_data: %d\n", tokenCnt)

	// Check for last 180 days
	var cnt180 int
	db.QueryRow("SELECT COUNT(*) FROM minute_data WHERE date_time >= date('now','-180 days')").Scan(&cnt180)
	fmt.Printf("minute_data rows in last 180 days: %d\n", cnt180)
}
