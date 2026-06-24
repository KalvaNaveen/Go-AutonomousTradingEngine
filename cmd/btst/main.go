// Command btst is the BTST auto-trade engine entrypoint.
//
// Each trading day at the configured entry time (default 15:20 IST) it:
//  1. squares off the prior day's open positions (next-day BTST exit),
//  2. runs the Tier-1 macro gate; if it passes, scrapes pur-ema10-20,
//  3. applies the Tier-2 news filter and places equal-weight ₹5L/N orders.
//
// It serves the dashboard on $PORT (default 8085) and reports to Telegram.
// PAPER_MODE controls paper vs live; live order placement (KiteBroker) lands in
// a later phase — until then the engine runs paper-only regardless of the flag.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"bnf_go_engine/agents"
	"bnf_go_engine/broker"
	"bnf_go_engine/calendar"
	"bnf_go_engine/config"
	"bnf_go_engine/engine"
	"bnf_go_engine/gate"
	"bnf_go_engine/quotes"
	"bnf_go_engine/scanner"
	"bnf_go_engine/store"
	"bnf_go_engine/web"

	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load("./.env")
	config.Reload()

	dbPath := config.BaseDir + string(os.PathSeparator) + "data" +
		string(os.PathSeparator) + "btst.db"
	if env := os.Getenv("BTST_DB"); env != "" {
		dbPath = env
	}
	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("[BTST] store: %v", err)
	}
	defer st.Close()

	calendar.Refresh()

	q := quotes.New()
	macro := gate.NewMacro(q)
	news := gate.NewNews()

	eng := &engine.Engine{
		Scraper:    scanner.NewScraper(config.BTSTScreener),
		Broker:     broker.NewPaperBroker(), // live KiteBroker swaps in here later
		Store:      st,
		Notify:     agents.SendTelegram,
		Quotes:     q,
		MacroGate:  macro.Check,
		NewsFilter: news.Filter,
	}

	// ── Dashboard ──────────────────────────────────────────────────────
	port := os.Getenv("PORT")
	if port == "" {
		port = "8085"
	}
	srv := web.New(st, config.PaperMode)
	go func() {
		log.Printf("[BTST] dashboard on :%s", port)
		if err := http.ListenAndServe(":"+port, srv.Handler()); err != nil {
			log.Printf("[BTST] dashboard stopped: %v", err)
		}
	}()

	// ── Daily holiday refresh at 06:00 IST ─────────────────────────────
	go dailyAt(6, 0, calendar.Refresh)

	agents.SendTelegram("🚀 *BTST Engine online* [" + modeTag() + "]\n" +
		"Screener: `" + config.BTSTScreener + "` · Entry/Exit: `" +
		config.BTSTEntryTime + "` IST\nDashboard on :" + port)
	log.Printf("[BTST] online [%s] — entry/exit at %s IST", modeTag(), config.BTSTEntryTime)

	runScheduler(eng)
}

// runScheduler drives the once-per-day exit+entry at the configured time.
func runScheduler(eng *engine.Engine) {
	entryH, entryM := config.ParseTime(config.BTSTEntryTime)
	entryHHMM := entryH*100 + entryM

	for {
		if !calendar.IsTradingToday() {
			sleepUntilMorning()
			continue
		}
		log.Printf("[BTST] trading day %s", config.NowIST().Format("2006-01-02"))
		done := false
		ticker := time.NewTicker(30 * time.Second)

	day:
		for range ticker.C {
			now := config.NowIST()
			hhmm := now.Hour()*100 + now.Minute()

			// Only trade inside the entry window. A late start (process restarted
			// after the window) must NOT fire on a stale EOD list at a wrong price.
			if hhmm >= entryHHMM && hhmm < entryHHMM+5 && !done {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				// 1) square off prior-day positions (BTST T+1 exit)
				if err := eng.RunExit(ctx, false); err != nil {
					log.Printf("[BTST] exit error: %v", err)
				}
				// 2) place today's new entries
				if err := eng.RunEntry(ctx); err != nil {
					log.Printf("[BTST] entry error: %v", err)
				}
				cancel()
				done = true
			}
			if hhmm >= entryHHMM+5 { // window passed — wrap up the day
				ticker.Stop()
				break day
			}
		}
		sleepUntilMorning()
	}
}

func sleepUntilMorning() {
	now := config.NowIST()
	next := now.AddDate(0, 0, 1)
	wake := time.Date(next.Year(), next.Month(), next.Day(), 8, 30, 0, 0, config.IST)
	d := wake.Sub(now)
	if d > 0 {
		log.Printf("[BTST] sleeping %.1fh until %s", d.Hours(), wake.Format("2006-01-02 15:04"))
		time.Sleep(d)
	}
}

// dailyAt runs fn once per day at the given IST hour:minute.
func dailyAt(hour, min int, fn func()) {
	for {
		now := config.NowIST()
		next := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, config.IST)
		if !next.After(now) {
			next = next.AddDate(0, 0, 1)
		}
		time.Sleep(time.Until(next))
		fn()
	}
}

func modeTag() string {
	if config.PaperMode {
		return "PAPER"
	}
	return "LIVE"
}
