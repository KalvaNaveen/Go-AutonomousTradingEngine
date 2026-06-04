package agents

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	"bnf_go_engine/config"

	_ "modernc.org/sqlite"
)

// SignalAlertAgent detects EMA10 buy/sell signals at EOD and delivers them
// via Telegram. No orders are placed — this is an alert-only engine.
//
// BUY rule  (Book Ch.3): 2 consecutive green candles both closing above EMA10.
// SELL rule (Book Ch.6): any candle closes below EMA10 for a previously alerted stock.
type SignalAlertAgent struct {
	db      *sql.DB
	Scanner *ScannerAgent
	GetLTP  func(uint32) float64
}

type SignalAlert struct {
	ID         int64
	Symbol     string
	Token      uint32
	EntryPrice float64
	SLPrice    float64
	EMA10      float64
	EMA20      float64
	Sector     string
	RSScore    int
	High52W    float64
	AvgVol     float64
	AlertedAt  string
}

func NewSignalAlertAgent() *SignalAlertAgent {
	db, err := sql.Open("sqlite", config.JournalDB+"?_journal_mode=WAL&_busy_timeout=30000")
	if err != nil {
		log.Printf("[SignalAlert] DB open failed: %v", err)
		return &SignalAlertAgent{}
	}
	db.Exec(`CREATE TABLE IF NOT EXISTS signal_alerts (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		symbol      TEXT    NOT NULL,
		token       INTEGER NOT NULL,
		entry_price REAL    NOT NULL,
		sl_price    REAL    NOT NULL,
		ema10       REAL    NOT NULL,
		ema20       REAL    DEFAULT 0,
		sector      TEXT    DEFAULT '',
		rs_score    INTEGER DEFAULT 0,
		high52w     REAL    DEFAULT 0,
		avg_vol     REAL    DEFAULT 0,
		alerted_at  TEXT    NOT NULL,
		sold_at     TEXT    DEFAULT '',
		sell_price  REAL    DEFAULT 0,
		sell_reason TEXT    DEFAULT ''
	)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_sa_symbol ON signal_alerts (symbol, sold_at)`)
	return &SignalAlertAgent{db: db}
}

// RunEODSellAlerts checks all open BUY alerts. Sends SELL when close < EMA10.
func (a *SignalAlertAgent) RunEODSellAlerts(universe map[uint32]string) {
	if a.db == nil || a.Scanner == nil || a.Scanner.DailyCache == nil {
		return
	}
	cache := a.Scanner.DailyCache
	today := config.TodayIST().Format("2006-01-02")

	rows, err := a.db.Query(`
		SELECT id, symbol, token, entry_price, sl_price, ema10, alerted_at
		FROM signal_alerts
		WHERE sold_at = '' OR sold_at IS NULL
		ORDER BY alerted_at ASC`)
	if err != nil {
		log.Printf("[SignalAlert] SELL query failed: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var symbol, alertedAt string
		var token uint32
		var entryPrice, slPrice, alertedEMA10 float64
		rows.Scan(&id, &symbol, &token, &entryPrice, &slPrice, &alertedEMA10, &alertedAt)

		closes, ok := cache.Closes[token]
		if !ok || len(closes) < config.EMA10Period+1 {
			continue
		}
		n := len(closes)
		ema10s := computeEMASeries(closes, config.EMA10Period)
		if len(ema10s) == 0 {
			continue
		}
		ema10 := ema10s[len(ema10s)-1]
		todayClose := closes[n-1]

		// ── SELL rule: close below EMA10 ──
		if todayClose >= ema10 {
			continue
		}

		// Mark sold
		a.db.Exec(`UPDATE signal_alerts SET sold_at=?, sell_price=?, sell_reason=? WHERE id=?`,
			today, todayClose, "EMA10_BELOW", id)

		// Send SELL alert
		pnlPct := (todayClose - entryPrice) / entryPrice * 100
		sign := "+"
		if pnlPct < 0 {
			sign = ""
		}
		held := 0
		if t1, err := time.Parse("2006-01-02", alertedAt); err == nil {
			held = int(config.NowIST().Sub(t1).Hours() / 24)
		}

		msg := fmt.Sprintf(
			"🔴 *SELL — %s*\n"+
				"Close ₹`%.2f` crossed below EMA10 ₹`%.2f`\n"+
				"Alerted at: ₹`%.2f` on `%s` (%d days ago)\n"+
				"Return if held: `%s%.1f%%`",
			symbol, todayClose, ema10,
			entryPrice, alertedAt, held,
			sign, pnlPct)
		SendTelegram(msg)
		log.Printf("[SignalAlert] SELL %s close=%.2f ema10=%.2f pnl=%.1f%%", symbol, todayClose, ema10, pnlPct)
	}
}

// ── Helpers ──────────────────────────────────────────────────────────────────


func (a *SignalAlertAgent) saveToDB(sig SignalAlert) {
	if a.db == nil {
		return
	}
	_, err := a.db.Exec(`
		INSERT INTO signal_alerts
			(symbol, token, entry_price, sl_price, ema10, ema20, sector, rs_score, high52w, avg_vol, alerted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sig.Symbol, sig.Token, sig.EntryPrice, sig.SLPrice,
		sig.EMA10, sig.EMA20, sig.Sector, sig.RSScore,
		sig.High52W, sig.AvgVol, sig.AlertedAt)
	if err != nil {
		log.Printf("[SignalAlert] save failed for %s: %v", sig.Symbol, err)
	}
}

func (a *SignalAlertAgent) hasActiveAlert(symbol string) bool {
	if a.db == nil {
		return false
	}
	var count int
	a.db.QueryRow(`SELECT COUNT(*) FROM signal_alerts WHERE symbol=? AND (sold_at='' OR sold_at IS NULL)`, symbol).Scan(&count)
	return count > 0
}

func (a *SignalAlertAgent) alertedToday(symbol, today string) bool {
	if a.db == nil {
		return false
	}
	var count int
	a.db.QueryRow(`SELECT COUNT(*) FROM signal_alerts WHERE symbol=? AND alerted_at=?`, symbol, today).Scan(&count)
	return count > 0
}

// LoadRecentAlerts returns the last N signal alerts for the dashboard API.
func (a *SignalAlertAgent) LoadRecentAlerts(limit int) ([]map[string]interface{}, error) {
	if a.db == nil {
		return nil, nil
	}
	rows, err := a.db.Query(`
		SELECT symbol, entry_price, sl_price, ema10, sector, rs_score,
		       alerted_at, sold_at, sell_price, sell_reason
		FROM signal_alerts
		ORDER BY alerted_at DESC, id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []map[string]interface{}
	for rows.Next() {
		var symbol, sector, alertedAt, soldAt, sellReason string
		var entryPrice, slPrice, ema10, sellPrice float64
		var rsScore int
		rows.Scan(&symbol, &entryPrice, &slPrice, &ema10, &sector, &rsScore,
			&alertedAt, &soldAt, &sellPrice, &sellReason)
		out = append(out, map[string]interface{}{
			"symbol":      symbol,
			"entry_price": entryPrice,
			"sl_price":    slPrice,
			"ema10":       ema10,
			"sector":      sector,
			"rs_score":    rsScore,
			"alerted_at":  alertedAt,
			"sold_at":     soldAt,
			"sell_price":  sellPrice,
			"sell_reason": sellReason,
			"is_open":     soldAt == "",
		})
	}
	return out, nil
}
