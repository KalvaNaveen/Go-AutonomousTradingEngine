package main

import (
	"database/sql"
	"fmt"
	"log"
	"strings"

	_ "modernc.org/sqlite"
)

func main() {
	db, err := sql.Open("sqlite", "data/journal.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT strategy, COUNT(*) as trades,
		       SUM(CASE WHEN gross_pnl > 0 THEN 1 ELSE 0 END) as wins,
		       ROUND(SUM(gross_pnl), 2) as total_pnl,
		       ROUND(AVG(gross_pnl), 2) as avg_pnl
		FROM trades
		GROUP BY strategy
		ORDER BY total_pnl DESC
	`)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()

	fmt.Printf("%-30s %6s %5s %5s %10s %8s\n", "STRATEGY", "TRADES", "WINS", "W%", "TOTAL_PNL", "AVG_PNL")
	fmt.Println(strings.Repeat("-", 75))
	for rows.Next() {
		var strat string
		var trades, wins int
		var totalPnl, avgPnl float64
		rows.Scan(&strat, &trades, &wins, &totalPnl, &avgPnl)
		wr := 0.0
		if trades > 0 {
			wr = float64(wins) / float64(trades) * 100
		}
		fmt.Printf("%-30s %6d %5d %4.0f%% %10.2f %8.2f\n", strat, trades, wins, wr, totalPnl, avgPnl)
	}
}
