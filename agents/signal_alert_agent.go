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
		high52w     REAL    DEFAULT 0,
		avg_vol     REAL    DEFAULT 0,
		alerted_at  TEXT    NOT NULL,
		sold_at     TEXT    DEFAULT '',
		sell_price  REAL    DEFAULT 0,
		sell_reason TEXT    DEFAULT ''
	)`)
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_sa_symbol ON signal_alerts (symbol, sold_at)`)
	// Migration: drop the now-unused rs_score column from pre-existing DBs
	// (RS scoring was removed entirely — it isn't part of the book's strategy).
	// SQLite ≥3.35 supports DROP COLUMN; ignore the error on older DBs/engines
	// where the column doesn't exist or the syntax isn't supported — harmless.
	if _, err := db.Exec(`ALTER TABLE signal_alerts DROP COLUMN rs_score`); err == nil {
		log.Println("[SignalAlert] migrated: dropped unused rs_score column")
	}
	return &SignalAlertAgent{db: db}
}

// RunEODSellAlerts checks all open BUY alerts and fires exit signals:
//   - SL_HIT  : close <= sl_price (hard stop-loss breached) → SELL immediately
//   - EMA10_BELOW : close < EMA10 (soft exit rule from Book Ch.6) → SELL
//   - EMA_CROSS   : EMA10 crossed below EMA21 (trend weakening) → CAUTION (no sell)
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

		held := 0
		if t1, err2 := time.Parse("2006-01-02", alertedAt); err2 == nil {
			held = int(config.NowIST().Sub(t1).Hours() / 24)
		}
		pnlPct := 0.0
		if entryPrice > 0 {
			pnlPct = (todayClose - entryPrice) / entryPrice * 100
		}
		sign := "+"
		if pnlPct < 0 {
			sign = ""
		}

		// ── Rule 1: Hard SL breach ──────────────────────────────────────────
		if slPrice > 0 && todayClose <= slPrice {
			a.db.Exec(`UPDATE signal_alerts SET sold_at=?, sell_price=?, sell_reason=? WHERE id=?`,
				today, todayClose, "SL_HIT", id)
			msg := fmt.Sprintf(
				"🚨 *STOP-LOSS HIT — %s*\n"+
					"Close ₹`%.2f` ≤ SL ₹`%.2f`\n"+
					"Alerted: ₹`%.2f` on `%s` (%d days ago)\n"+
					"Return: `%s%.1f%%`",
				symbol, todayClose, slPrice,
				entryPrice, alertedAt, held, sign, pnlPct)
			SendTelegram(msg)
			log.Printf("[SignalAlert] SL_HIT %s close=%.2f sl=%.2f pnl=%.1f%%", symbol, todayClose, slPrice, pnlPct)
			continue
		}

		// ── Rule 2: Close below EMA10 (soft exit) ──────────────────────────
		if todayClose < ema10 {
			a.db.Exec(`UPDATE signal_alerts SET sold_at=?, sell_price=?, sell_reason=? WHERE id=?`,
				today, todayClose, "EMA10_BELOW", id)
			msg := fmt.Sprintf(
				"🔴 *EXIT SIGNAL — %s*\n"+
					"Close ₹`%.2f` below EMA10 ₹`%.2f`\n"+
					"Alerted: ₹`%.2f` on `%s` (%d days ago)\n"+
					"Return if held: `%s%.1f%%`",
				symbol, todayClose, ema10,
				entryPrice, alertedAt, held, sign, pnlPct)
			SendTelegram(msg)
			log.Printf("[SignalAlert] EMA10_BELOW %s close=%.2f ema10=%.2f pnl=%.1f%%", symbol, todayClose, ema10, pnlPct)
			continue
		}

		// ── Rule 3: EMA10 crossed below EMA21 (trend caution) ──────────────
		// Only fire if we have enough history for EMA21
		if len(closes) >= config.EMA20Period+2 {
			ema21s := computeEMASeries(closes, config.EMA20Period)
			if len(ema21s) >= 2 && len(ema10s) >= 2 {
				// Cross detected: today ema10 < ema21, yesterday ema10 >= ema21(prev)
				ema10Prev := ema10s[len(ema10s)-2]
				ema21Curr := ema21s[len(ema21s)-1]
				ema21Prev := ema21s[len(ema21s)-2]
				if ema10 < ema21Curr && ema10Prev >= ema21Prev {
					msg := fmt.Sprintf(
						"⚠️ *CAUTION — %s* (EMA cross)\n"+
							"EMA10 ₹`%.2f` crossed below EMA21 ₹`%.2f` — trend weakening\n"+
							"Close ₹`%.2f` | Still above SL — no action yet\n"+
							"Alerted: ₹`%.2f` on `%s` (%d days ago) | Return: `%s%.1f%%`",
						symbol, ema10, ema21Curr,
						todayClose, entryPrice, alertedAt, held, sign, pnlPct)
					SendTelegram(msg)
					log.Printf("[SignalAlert] EMA_CROSS (caution) %s ema10=%.2f ema21=%.2f", symbol, ema10, ema21Curr)
				}
			}
		}
	}
}

// ── Helpers ──────────────────────────────────────────────────────────────────


func (a *SignalAlertAgent) saveToDB(sig SignalAlert) {
	if a.db == nil {
		return
	}
	_, err := a.db.Exec(`
		INSERT INTO signal_alerts
			(symbol, token, entry_price, sl_price, ema10, ema20, sector, high52w, avg_vol, alerted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sig.Symbol, sig.Token, sig.EntryPrice, sig.SLPrice,
		sig.EMA10, sig.EMA20, sig.Sector,
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

// recentlyClosed returns true if the symbol had an alert that was closed within
// the last `days` calendar days — used to enforce the 3-day cooldown so we don't
// re-alert the same stock shortly after an exit.
func (a *SignalAlertAgent) recentlyClosed(symbol string, days int) bool {
	if a.db == nil {
		return false
	}
	cutoff := config.NowIST().AddDate(0, 0, -days).Format("2006-01-02")
	var count int
	a.db.QueryRow(`
		SELECT COUNT(*) FROM signal_alerts
		WHERE symbol=? AND sold_at != '' AND sold_at IS NOT NULL AND sold_at >= ?`,
		symbol, cutoff).Scan(&count)
	return count > 0
}

// IsOnCooldown returns true if a new BUY alert should be suppressed for this symbol:
// - it already has an open (unsold) alert, OR
// - it was sold/exited within the last 3 calendar days.
func (a *SignalAlertAgent) IsOnCooldown(symbol string) bool {
	return a.hasActiveAlert(symbol) || a.recentlyClosed(symbol, 3)
}

// RecordBuySignal persists a new BUY signal to the DB. Call after sending the alert.
// SLPrice is approximated as LTP - 1.5×ATR if ATR is available.
func (a *SignalAlertAgent) RecordBuySignal(r EODScanResult) {
	today := config.TodayIST().Format("2006-01-02")
	if a.alertedToday(r.Symbol, today) {
		return // already recorded today (e.g. bot scan + EOD scan both fired)
	}
	slPrice := r.LTP * 0.95 // fallback: 5% below entry
	if r.ATR > 0 {
		slPrice = r.LTP - 1.5*r.ATR
	}
	a.saveToDB(SignalAlert{
		Symbol:     r.Symbol,
		Token:      r.Token,
		EntryPrice: r.LTP,
		SLPrice:    slPrice,
		EMA10:      r.EMA10,
		EMA20:      r.EMA20,
		High52W:    r.High52W,
		AvgVol:     r.AvgVolume,
		AlertedAt:  today,
	})
}

// LoadRecentAlerts returns the last N signal alerts for the dashboard API.
func (a *SignalAlertAgent) LoadRecentAlerts(limit int) ([]map[string]interface{}, error) {
	if a.db == nil {
		return nil, nil
	}
	rows, err := a.db.Query(`
		SELECT symbol, entry_price, sl_price, ema10, sector,
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
		rows.Scan(&symbol, &entryPrice, &slPrice, &ema10, &sector,
			&alertedAt, &soldAt, &sellPrice, &sellReason)
		out = append(out, map[string]interface{}{
			"symbol":      symbol,
			"entry_price": entryPrice,
			"sl_price":    slPrice,
			"ema10":       ema10,
			"sector":      sector,
			"alerted_at":  alertedAt,
			"sold_at":     soldAt,
			"sell_price":  sellPrice,
			"sell_reason": sellReason,
			"is_open":     soldAt == "",
		})
	}
	return out, nil
}
