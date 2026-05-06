package main

import (
	"database/sql"
	"fmt"
	"log"
	"math"

	_ "modernc.org/sqlite"
)

func main() {
	dbPath := `c:\Users\Admin\.gemini\antigravity\scratch\bnf_go_engine\data\journal.db`
	db, err := sql.Open("sqlite", dbPath)
	if err != nil { log.Fatal(err) }
	defer db.Close()

	// Find the most recent trading day
	var today string
	db.QueryRow(`SELECT date FROM trades ORDER BY date DESC LIMIT 1`).Scan(&today)
	if today == "" {
		fmt.Println("No trades found!")
		return
	}

	// Summary
	var total int; var grossPnl float64; var wins, losses int
	var winAmt, lossAmt float64
	db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(gross_pnl),0) FROM trades WHERE date=?`, today).Scan(&total, &grossPnl)
	db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(gross_pnl),0) FROM trades WHERE date=? AND gross_pnl>0`, today).Scan(&wins, &winAmt)
	db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(gross_pnl),0) FROM trades WHERE date=? AND gross_pnl<=0`, today).Scan(&losses, &lossAmt)

	wr := 0.0; if total > 0 { wr = float64(wins)/float64(total)*100 }
	avgW := 0.0; if wins > 0 { avgW = winAmt/float64(wins) }
	avgL := 0.0; if losses > 0 { avgL = lossAmt/float64(losses) }

	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Printf("  TRADE REVIEW — %s\n", today)
	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Printf("  Trades: %d | Wins: %d | Losses: %d | WR: %.1f%%\n", total, wins, losses, wr)
	fmt.Printf("  Gross P&L: ₹%.2f\n", grossPnl)
	fmt.Printf("  Avg Win: ₹%.2f | Avg Loss: ₹%.2f\n", avgW, avgL)
	if avgL != 0 { fmt.Printf("  Risk:Reward: 1:%.2f\n", math.Abs(avgW/avgL)) }

	// Per strategy
	fmt.Println("\n═══ BY STRATEGY ═══")
	r1, _ := db.Query(`SELECT strategy, COUNT(*), SUM(CASE WHEN gross_pnl>0 THEN 1 ELSE 0 END),
		SUM(gross_pnl), COALESCE(AVG(CASE WHEN gross_pnl>0 THEN gross_pnl END),0),
		COALESCE(AVG(CASE WHEN gross_pnl<=0 THEN gross_pnl END),0)
		FROM trades WHERE date=? GROUP BY strategy ORDER BY SUM(gross_pnl) ASC`, today)
	for r1.Next() {
		var s string; var c, w int; var t, aw, al float64
		r1.Scan(&s, &c, &w, &t, &aw, &al)
		fmt.Printf("  %-24s %d trades  %dW  P&L=₹%.0f  AvgW=%.0f  AvgL=%.0f\n", s, c, w, t, aw, al)
	}
	r1.Close()

	// All trades
	fmt.Println("\n═══ ALL TRADES (worst → best) ═══")
	r2, _ := db.Query(`SELECT entry_time, strategy, symbol, entry_price, full_exit_price,
		qty, gross_pnl, exit_reason, regime, COALESCE(rvol,0)
		FROM trades WHERE date=? ORDER BY gross_pnl ASC`, today)
	for r2.Next() {
		var et, s, sym, er, reg string; var ep, xp, pnl, rv float64; var q int
		r2.Scan(&et, &s, &sym, &ep, &xp, &q, &pnl, &er, &reg, &rv)
		t := et; if len(et) > 16 { t = et[11:16] }
		icon := "✅"; if pnl <= 0 { icon = "❌" }
		fmt.Printf("  %s %s %-20s %-12s E=%.1f X=%.1f Q=%d P&L=%.0f  %s [%s RV=%.1f]\n",
			icon, t, s, sym, ep, xp, q, pnl, er, reg, rv)
	}
	r2.Close()

	// Exit reasons
	fmt.Println("\n═══ EXIT REASONS ═══")
	r3, _ := db.Query(`SELECT exit_reason, COUNT(*), SUM(gross_pnl) FROM trades WHERE date=? GROUP BY exit_reason ORDER BY SUM(gross_pnl)`, today)
	for r3.Next() {
		var r string; var c int; var t float64
		r3.Scan(&r, &c, &t)
		fmt.Printf("  %-25s %d trades  ₹%.0f\n", r, c, t)
	}
	r3.Close()

	// Signals & regime
	fmt.Println("\n═══ SIGNAL FUNNEL ═══")
	var sigC, trC int
	db.QueryRow(`SELECT COUNT(*) FROM agent_logs WHERE date=? AND action='SIGNAL_FOUND'`, today).Scan(&sigC)
	db.QueryRow(`SELECT COUNT(*) FROM agent_logs WHERE date=? AND action='TRADE_OPENED'`, today).Scan(&trC)
	fmt.Printf("  Signals: %d → Trades: %d (%.1f%% conv)\n", sigC, trC, func()float64{if sigC>0{return float64(trC)/float64(sigC)*100}; return 0}())

	fmt.Println("\n═══ REGIME TIMELINE ═══")
	r4, _ := db.Query(`SELECT time, detail FROM agent_logs WHERE date=? AND action='REGIME_CHANGE' ORDER BY time`, today)
	for r4.Next() {
		var t, d string; r4.Scan(&t, &d); fmt.Printf("  %s  %s\n", t, d)
	}
	r4.Close()

	// Full history
	fmt.Println("\n═══ ALL DAILY P&L (full history) ═══")
	var cumPnl float64
	r5, _ := db.Query(`SELECT date, total_trades, wins, losses, win_rate, gross_pnl FROM daily_summary WHERE total_trades > 0 ORDER BY date ASC`)
	for r5.Next() {
		var d string; var tt, w, l int; var wr2, gp float64
		r5.Scan(&d, &tt, &w, &l, &wr2, &gp)
		cumPnl += gp
		icon := "🟢"; if gp < 0 { icon = "🔴" }
		fmt.Printf("  %s %s  %2d trades  %2dW/%2dL  WR=%3.0f%%  Day=₹%7.0f  Cum=₹%7.0f\n", icon, d, tt, w, l, wr2, gp, cumPnl)
	}
	r5.Close()
	fmt.Printf("\n  ═══ TOTAL CUMULATIVE P&L: ₹%.0f ═══\n", cumPnl)

	// Worst strategies all-time
	fmt.Println("\n═══ ALL-TIME BY STRATEGY ═══")
	r6, _ := db.Query(`SELECT strategy, COUNT(*), SUM(CASE WHEN gross_pnl>0 THEN 1 ELSE 0 END),
		SUM(gross_pnl) FROM trades GROUP BY strategy ORDER BY SUM(gross_pnl) ASC`)
	for r6.Next() {
		var s string; var c, w int; var t float64
		r6.Scan(&s, &c, &w, &t)
		wr := 0.0; if c > 0 { wr = float64(w)/float64(c)*100 }
		icon := "🟢"; if t < 0 { icon = "🔴" }
		fmt.Printf("  %s %-24s %3d trades  WR=%3.0f%%  P&L=₹%.0f\n", icon, s, c, wr, t)
	}
	r6.Close()
}
