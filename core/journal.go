package core

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	"bnf_go_engine/config"

	_ "modernc.org/sqlite"
)

// Journal persists all trade history to SQLite.
// Exact port of Python core/journal.py
type Journal struct {
	dbPath string
}

func NewJournal() *Journal {
	j := &Journal{dbPath: config.JournalDB}
	j.initDB()
	return j
}

func (j *Journal) initDB() {
	db, err := sql.Open("sqlite", j.dbPath)
	if err != nil {
		log.Printf("[Journal] DB open error: %v", err)
		return
	}
	defer db.Close()

	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA busy_timeout=3000")

	db.Exec(`
		CREATE TABLE IF NOT EXISTS trades (
			id                INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp         TEXT, date TEXT,
			entry_time        TEXT,
			symbol            TEXT, strategy TEXT, regime TEXT,
			rvol              REAL, deviation_pct REAL,
			entry_price       REAL, partial_exit_price REAL,
			partial_exit_qty  INTEGER, full_exit_price REAL,
			qty               INTEGER, gross_pnl REAL,
			stop_hit          INTEGER DEFAULT 0,
			time_stop_hit     INTEGER DEFAULT 0,
			exit_reason       TEXT, hold_minutes REAL,
			daily_pnl_after   REAL
		)
	`)

	db.Exec("ALTER TABLE trades ADD COLUMN entry_time TEXT")

	db.Exec("CREATE INDEX IF NOT EXISTS idx_trades_date ON trades(date)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_trades_strategy ON trades(strategy, date)")

	db.Exec(`
		CREATE TABLE IF NOT EXISTS daily_summary (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			date            TEXT UNIQUE, regime TEXT,
			total_trades    INTEGER, wins INTEGER, losses INTEGER,
			win_rate        REAL, gross_pnl REAL,
			max_loss_streak INTEGER,
			engine_stopped  INTEGER DEFAULT 0, stop_reason TEXT
		)
	`)

	db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_logs (
			id      INTEGER PRIMARY KEY AUTOINCREMENT,
			date    TEXT, time TEXT,
			agent   TEXT, action TEXT, detail TEXT
		)
	`)
	db.Exec("CREATE INDEX IF NOT EXISTS idx_agent_logs_date ON agent_logs(date)")
}

func (j *Journal) LogAgentActivity(agent, action, detail, timestampStr string) {
	db, err := sql.Open("sqlite", j.dbPath)
	if err != nil {
		return
	}
	defer db.Close()

	dateStr := config.TodayIST().Format("2006-01-02")
	db.Exec(`INSERT INTO agent_logs (date, time, agent, action, detail) VALUES (?,?,?,?,?)`,
		dateStr, timestampStr, agent, action, detail)
}

// TradeLog represents a completed trade for journaling
type TradeLog struct {
	Symbol           string
	Strategy         string
	Regime           string
	RVol             float64
	DeviationPct     float64
	EntryPrice       float64
	PartialExitPrice float64
	PartialExitQty   int
	FullExitPrice    float64
	Qty              int
	GrossPnl         float64
	ExitReason       string
	EntryTime        time.Time
	ExitTime         time.Time
	DailyPnlAfter   float64
	IsShort          bool
}

func (j *Journal) LogTrade(t *TradeLog) {
	db, err := sql.Open("sqlite", j.dbPath)
	if err != nil {
		return
	}
	defer db.Close()

	now := config.NowIST()
	hold := 0.0
	if !t.EntryTime.IsZero() {
		hold = t.ExitTime.Sub(t.EntryTime).Minutes()
	}

	stopHit := 0
	timeStopHit := 0
	if t.ExitReason == "STOP_HIT" {
		stopHit = 1
	}
	if t.ExitReason == "TIME_STOP" {
		timeStopHit = 1
	}

	db.Exec(`
		INSERT INTO trades
		(timestamp, date, entry_time, symbol, strategy, regime, rvol, deviation_pct,
		 entry_price, partial_exit_price, partial_exit_qty,
		 full_exit_price, qty, gross_pnl, stop_hit, time_stop_hit,
		 exit_reason, hold_minutes, daily_pnl_after)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`,
		now.Format(time.RFC3339), now.Format("2006-01-02"),
		t.EntryTime.Format(time.RFC3339),
		t.Symbol, t.Strategy, t.Regime, t.RVol, t.DeviationPct,
		t.EntryPrice, t.PartialExitPrice, t.PartialExitQty,
		t.FullExitPrice, t.Qty, t.GrossPnl,
		stopHit, timeStopHit, t.ExitReason, hold, t.DailyPnlAfter,
	)
}

func (j *Journal) LogExit(trade *Trade, exitPrice float64, reason string) {
	qty := trade.RemainingQty
	if qty == 0 {
		qty = trade.Qty
	}
	var pnl float64
	if trade.IsShort {
		pnl = (trade.EntryPrice - exitPrice) * float64(qty)
	} else {
		pnl = (exitPrice - trade.EntryPrice) * float64(qty)
	}
	j.LogTrade(&TradeLog{
		Symbol:        trade.Symbol,
		Strategy:      trade.Strategy,
		Regime:        trade.Regime,
		RVol:          trade.RVol,
		DeviationPct:  trade.DeviationPct,
		EntryPrice:    trade.EntryPrice,
		FullExitPrice: exitPrice,
		Qty:           qty,
		GrossPnl:      pnl,
		ExitReason:    reason,
		EntryTime:     trade.EntryTime,
		ExitTime:      config.NowIST(),
		IsShort:       trade.IsShort,
	})
}

func (j *Journal) LogDailySummary(stats map[string]interface{}, regime string, stopped bool, reason string) {
	db, err := sql.Open("sqlite", j.dbPath)
	if err != nil {
		return
	}
	defer db.Close()

	stoppedInt := 0
	if stopped {
		stoppedInt = 1
	}

	db.Exec(`
		INSERT OR REPLACE INTO daily_summary
		(date, regime, total_trades, wins, losses, win_rate,
		 gross_pnl, max_loss_streak, engine_stopped, stop_reason)
		VALUES (?,?,?,?,?,?,?,?,?,?)
	`,
		config.TodayIST().Format("2006-01-02"), regime,
		stats["total"], stats["wins"], stats["losses"], stats["win_rate"],
		stats["gross_pnl"], stats["loss_streak"], stoppedInt, reason,
	)
}

// PeriodSummary returns aggregated stats for a date range
type PeriodSummary struct {
	Total      int
	Wins       int
	Losses     int
	WinRate    float64
	GrossPnl   float64
	EstCharges float64
	BestRegime string
	WorstRegime string
	Top5Symbols []struct {
		Symbol string
		Pnl    float64
	}
}

func (j *Journal) GetPeriodSummary(fromDate, toDate string) *PeriodSummary {
	db, err := sql.Open("sqlite", j.dbPath)
	if err != nil {
		return &PeriodSummary{}
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT gross_pnl, strategy, regime, symbol, entry_price, full_exit_price, qty
		FROM trades WHERE date >= ? AND date <= ?
	`, fromDate, toDate)
	if err != nil {
		return &PeriodSummary{}
	}
	defer rows.Close()

	type row struct {
		pnl, ep, xp float64
		strat, regime, sym string
		qty int
	}
	var data []row
	for rows.Next() {
		var r row
		rows.Scan(&r.pnl, &r.strat, &r.regime, &r.sym, &r.ep, &r.xp, &r.qty)
		data = append(data, r)
	}

	if len(data) == 0 {
		return &PeriodSummary{}
	}

	s := &PeriodSummary{Total: len(data)}
	regimePnl := map[string]float64{}
	symPnl := map[string]float64{}

	for _, r := range data {
		s.GrossPnl += r.pnl
		if r.pnl > 0 {
			s.Wins++
		}
		regimePnl[r.regime] += r.pnl
		symPnl[r.sym] += r.pnl

		// Charge calc
		isShort := false
		if len(r.strat) > 0 {
			for _, kw := range []string{"SHORT"} {
				if len(r.strat) >= len(kw) {
					// simple contains check
					for i := 0; i <= len(r.strat)-len(kw); i++ {
						if r.strat[i:i+len(kw)] == kw {
							isShort = true
							break
						}
					}
				}
			}
		}
		s.EstCharges += ComputeChargesFromTrade(r.ep, r.xp, r.qty, isShort, "MIS", 0)
	}
	s.Losses = s.Total - s.Wins
	if s.Total > 0 {
		s.WinRate = float64(s.Wins) / float64(s.Total) * 100
	}

	// Best/worst regime
	bestPnl := -1e18
	worstPnl := 1e18
	for reg, pnl := range regimePnl {
		if pnl > bestPnl {
			bestPnl = pnl
			s.BestRegime = reg
		}
		if pnl < worstPnl {
			worstPnl = pnl
			s.WorstRegime = reg
		}
	}

	return s
}

func (j *Journal) GetTodayTopActions(dateStr string, n int) []map[string]interface{} {
	if dateStr == "" {
		dateStr = config.TodayIST().Format("2006-01-02")
	}
	db, err := sql.Open("sqlite", j.dbPath)
	if err != nil {
		return nil
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT symbol, strategy, gross_pnl, exit_reason
		FROM trades WHERE date = ?
		ORDER BY ABS(gross_pnl) DESC LIMIT ?
	`, dateStr, n)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var sym, strat, reason string
		var pnl float64
		rows.Scan(&sym, &strat, &pnl, &reason)
		results = append(results, map[string]interface{}{
			"symbol": sym, "strategy": strat,
			"gross_pnl": pnl, "exit_reason": reason,
		})
	}
	return results
}

func (j *Journal) GetAllTradesForDate(dateStr string) []map[string]interface{} {
	if dateStr == "" {
		dateStr = config.TodayIST().Format("2006-01-02")
	}
	db, err := sql.Open("sqlite", j.dbPath)
	if err != nil {
		return nil
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT symbol, strategy, entry_price, full_exit_price,
		       gross_pnl, exit_reason, qty, entry_time, timestamp
		FROM trades WHERE date = ? ORDER BY timestamp ASC
	`, dateStr)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var sym, strat, reason, entryTime, exitTime string
		var ep, xp, pnl float64
		var qty int
		rows.Scan(&sym, &strat, &ep, &xp, &pnl, &reason, &qty, &entryTime, &exitTime)
		results = append(results, map[string]interface{}{
			"symbol": sym, "strategy": strat, "entry_price": ep,
			"full_exit_price": xp, "gross_pnl": pnl, "exit_reason": reason,
			"qty": qty, "entry_time": entryTime, "exit_time": exitTime,
		})
	}
	return results
}

func (j *Journal) GetAvailableDates() []string {
	db, err := sql.Open("sqlite", j.dbPath)
	if err != nil {
		return nil
	}
	defer db.Close()

	rows, err := db.Query("SELECT DISTINCT date FROM trades ORDER BY date DESC")
	if err != nil {
		return nil
	}
	defer rows.Close()

	var dates []string
	for rows.Next() {
		var d string
		rows.Scan(&d)
		dates = append(dates, d)
	}

	if len(dates) == 0 {
		rows2, _ := db.Query("SELECT DISTINCT date FROM daily_summary ORDER BY date DESC")
		if rows2 != nil {
			defer rows2.Close()
			for rows2.Next() {
				var d string
				rows2.Scan(&d)
				dates = append(dates, d)
			}
		}
	}
	return dates
}

func (j *Journal) GetLogsForDate(dateStr string) []map[string]string {
	db, err := sql.Open("sqlite", j.dbPath)
	if err != nil {
		return nil
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT time, agent, action, detail
		FROM agent_logs WHERE date = ? ORDER BY id ASC
	`, dateStr)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var results []map[string]string
	for rows.Next() {
		var t, agent, action, detail string
		rows.Scan(&t, &agent, &action, &detail)
		results = append(results, map[string]string{
			"time": t, "agent": agent, "action": action, "detail": detail,
		})
	}
	return results
}

func (j *Journal) GetDailySummaryForDate(dateStr string) map[string]interface{} {
	db, err := sql.Open("sqlite", j.dbPath)
	if err != nil {
		return nil
	}
	defer db.Close()

	var total, wins, losses, stopped int
	var winRate, pnl float64
	var reason, regime string
	err = db.QueryRow(`
		SELECT total_trades, wins, losses, win_rate, gross_pnl, stop_reason, engine_stopped, regime
		FROM daily_summary WHERE date = ?
	`, dateStr).Scan(&total, &wins, &losses, &winRate, &pnl, &reason, &stopped, &regime)
	if err != nil {
		return nil
	}
	return map[string]interface{}{
		"total_trades": total, "wins": wins, "losses": losses,
		"win_rate": winRate, "gross_pnl": pnl, "stop_reason": reason,
		"engine_stopped": stopped == 1, "regime": regime,
	}
}

// Suppress unused import warning
var _ = fmt.Sprint
