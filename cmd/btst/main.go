// Command btst is the BTST auto-trade engine entrypoint.
//
// Each trading day at the configured entry time (default 15:20 IST) it runs the
// full cycle: scan the configured ChartInk screeners (distinct union), CARRY
// holdings the scan re-listed (skip their sell + skip re-buying them), square
// off the rest, then place equal-weight ₹5L/N buys on the NEW names — each with
// a 2% trailing stop that an intraday monitor ratchets every few minutes.
//
// It serves the dashboard on $PORT (default 8085). PAPER_MODE controls paper vs
// live; live order placement (KiteBroker) is selected only when PAPER_MODE=false
// and Kite credentials are present.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

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
	if os.Getenv("TURSO_DATABASE_URL") != "" {
		log.Printf("[BTST] storage: Turso (durable)")
	} else {
		log.Printf("[BTST] storage: local SQLite %s (EPHEMERAL — set TURSO_* for durability)", dbPath)
	}

	calendar.Refresh()

	q := quotes.New()
	// Broker selection: live Kite orders only when PAPER_MODE=false AND credentials
	// are present; otherwise the simulated paper broker. The 30-day trial runs paper.
	var b broker.Broker = broker.NewPaperBroker()
	if !config.PaperMode && config.KiteAPIKey != "" && config.KiteAccessToken != "" {
		b = broker.NewKiteBroker()
		log.Printf("[BTST] LIVE broker active — real Kite orders will be placed")
	}

	eng := &engine.Engine{
		Scanner: scanner.NewMulti(config.BTSTScreeners),
		Broker:  b,
		Store:   st,
		Notify:  func(msg string) { log.Printf("[Report] %s", msg) },
		Quotes:  q,
	}

	// Automated sentiment gates — OFF by default.
	if config.BTSTGateEnabled {
		eng.MacroGate = gate.NewMacro(q).Check
		eng.NewsFilter = gate.NewNews().Filter
		log.Printf("[BTST] sentiment gates ENABLED")
	}

	// ── Dashboard ──────────────────────────────────────────────────────
	port := os.Getenv("PORT")
	if port == "" {
		port = "8085"
	}
	srv := web.New(st, config.PaperMode)

	// Manual scan+trade trigger (testing outside 15:20). Token-protected.
	if config.BTSTTriggerToken != "" {
		srv.SetTrigger(config.BTSTTriggerToken, func(force bool) string {
			go func() {
				ctx := context.Background()
				if force {
					ctx = engine.WithForceEntry(ctx)
				}
				cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
				defer cancel()
				log.Printf("[BTST] manual trigger fired (force=%v)", force)
				if err := eng.RunCycle(cctx); err != nil {
					log.Printf("[BTST] manual run error: %v", err)
				}
			}()
			return "triggered — scan running, watch the dashboard"
		})
		log.Printf("[BTST] manual /api/run trigger ENABLED")
	}
	go func() {
		log.Printf("[BTST] dashboard on :%s", port)
		if err := http.ListenAndServe(":"+port, srv.Handler()); err != nil {
			log.Printf("[BTST] dashboard stopped: %v", err)
		}
	}()

	// ── Daily holiday refresh at 06:00 IST ─────────────────────────────
	go dailyAt(6, 0, calendar.Refresh)

	// ── Intraday trailing-SL monitor ────────────────────────────────────
	go runMonitor(eng)

	log.Printf("[BTST] online [%s] — screeners %v, cycle at %s IST, trail %.1f%% every %dm, dashboard on :%s",
		modeTag(), config.BTSTScreeners, config.BTSTEntryTime, config.BTSTStopLossPct,
		config.BTSTMonitorIntervalMin, port)

	runScheduler(eng)
}

// runScheduler drives the once-per-day cycle (carry netting → sell → buy) at the
// configured time.
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
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
				if err := eng.RunCycle(ctx); err != nil {
					log.Printf("[BTST] cycle error: %v", err)
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

// runMonitor ticks the trailing-SL monitor during market hours (09:15–15:30 IST,
// trading days). Each tick reloads open positions from the store, so it is
// restart-safe and needs no coordination with the scheduler.
func runMonitor(eng *engine.Engine) {
	interval := time.Duration(config.BTSTMonitorIntervalMin) * time.Minute
	if interval < time.Minute {
		interval = time.Minute
	}
	openH, openM := config.ParseTime(config.NSEOpenTime)
	closeH, closeM := config.ParseTime(config.NSECloseTime)
	openHHMM, closeHHMM := openH*100+openM, closeH*100+closeM

	for {
		now := config.NowIST()
		hhmm := now.Hour()*100 + now.Minute()
		if calendar.IsTradingToday() && hhmm >= openHHMM && hhmm < closeHHMM {
			ctx, cancel := context.WithTimeout(context.Background(), interval)
			eng.MonitorOnce(ctx)
			cancel()
		}
		time.Sleep(interval)
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
