package main

import (
	"database/sql"
	"fmt"
	"log"

	_ "modernc.org/sqlite"
)

func main() {
	db, err := sql.Open("sqlite", "c:\\Projects\\bnf_go_engine\\journal.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_logs (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			date    TEXT, time TEXT,
			agent   TEXT, action TEXT, detail TEXT
		)
	`)
	if err != nil {
		log.Fatal(err)
	}

	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM agent_logs").Scan(&count)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Total logs: %d\n", count)
}
