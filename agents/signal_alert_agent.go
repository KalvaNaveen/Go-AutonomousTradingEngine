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

// RunEODBuyAlerts scans universe at 15:31 for 2-green-candles-above-EMA10.
// Sends one Telegram message per signal. Skips stocks with an active open alert.
func (a *SignalAlertAgent) RunEODBuyAlerts(universe map[uint32]string) {
	if a.Scanner == nil || a.Scanner.DailyCache == nil || !a.Scanner.DailyCache.Loaded {
		log.Println("[SignalAlert] Cache not loaded — skipping BUY scan")
		return
	}
	cache := a.Scanner.DailyCache
	today := config.TodayIST().Format("2006-01-02")

	var signals []SignalAlert

	// Use the engine's canonical EMA pullback strategy — same logic as the live scanner
	// and backtest. Previously used a separate 2-green-candles rule which diverged.
	strat := &EMAStrategy{}

	for token, symbol := range universe {
		closes, cOk := cache.Closes[token]
		if !cOk || len(closes) == 0 {
			continue
		}

		// Skip if stock already has an active (unsold) alert
		if a.hasActiveAlert(symbol) {
			continue
		}
		// Skip if already alerted today (idempotent)
		if a.alertedToday(symbol, today) {
			continue
		}

		ltp := closes[len(closes)-1]
		if a.GetLTP != nil {
			if live := a.GetLTP(token); live > 0 {
				ltp = live
			}
		}

		ctx := StrategyContext{Cache: cache, CapitalMultiplier: a.Scanner.CapitalMultiplier}
		sig := strat.Detect(token, symbol, ltp, ctx)
		if sig == nil {
			continue
		}

		ema10s := computeEMASeries(closes, config.EMA10Period)
		ema20s := computeEMASeries(closes, config.EMA20Period)
		ema10, ema20 := 0.0, 0.0
		if len(ema10s) > 0 {
			ema10 = ema10s[len(ema10s)-1]
		}
		if len(ema20s) > 0 {
			ema20 = ema20s[len(ema20s)-1]
		}

		sector := ""
		if a.Scanner.TokenSector != nil {
			sector = a.Scanner.TokenSector[token]
		}

		signals = append(signals, SignalAlert{
			Symbol:     symbol,
			Token:      token,
			EntryPrice: sig.EntryPrice,
			SLPrice:    sig.StopPrice,
			EMA10:      ema10,
			EMA20:      ema20,
			Sector:     sector,
			RSScore:    cache.RSScore[token],
			High52W:    cache.High52W[token],
			AvgVol:     cache.AvgVol[token],
			AlertedAt:  today,
		})
	}

	if len(signals) == 0 {
		log.Println("[SignalAlert] No BUY signals today")
		SendTelegram(fmt.Sprintf("📊 *EOD SCAN — %s*\nNo BUY signals today.", today))
		return
	}

	log.Printf("[SignalAlert] %d BUY signals", len(signals))
	SendTelegram(fmt.Sprintf("📈 *EOD BUY SIGNALS — %s*\n`%d stocks`", today, len(signals)))

	for _, sig := range signals {
		a.sendBuyMessage(sig)
		a.saveToDB(sig)
	}
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

func (a *SignalAlertAgent) sendBuyMessage(sig SignalAlert) {
	riskPct := 0.0
	if sig.EntryPrice > 0 {
		riskPct = (sig.EntryPrice - sig.SLPrice) / sig.EntryPrice * 100
	}
	distFrom52W := 0.0
	nearHighTag := ""
	if sig.High52W > 0 && sig.EntryPrice > 0 {
		distFrom52W = (sig.High52W - sig.EntryPrice) / sig.High52W * 100
		if distFrom52W <= 2.0 {
			nearHighTag = "🏔 Near ATH"
		} else {
			nearHighTag = fmt.Sprintf("%.1f%% from 52W High", distFrom52W)
		}
	}

	rsTag := ""
	switch {
	case sig.RSScore >= 95:
		rsTag = "🔥 Top 5%"
	case sig.RSScore >= 80:
		rsTag = "⭐ Top 20%"
	case sig.RSScore >= 60:
		rsTag = "✅ Above avg"
	}

	msg := fmt.Sprintf(
		"🟢 *%s* — EMA Pullback Setup\n"+
			"━━━━━━━━━━━━━━━━━━━━━━━━\n"+
			"📈 Entry: `₹%.0f` | SL: `₹%.0f` (`%.1f%%` risk)\n"+
			"📊 EMA10: `₹%.0f` | EMA20: `₹%.0f`\n\n"+
			"🏅 RS: `%d/100` %s\n"+
			"📍 %s",
		sig.Symbol,
		sig.EntryPrice, sig.SLPrice, riskPct,
		sig.EMA10, sig.EMA20,
		sig.RSScore, rsTag,
		nearHighTag)

	SendTelegram(msg)
	log.Printf("[SignalAlert] BUY %s entry=%.0f sl=%.0f ema10=%.0f rs=%d",
		sig.Symbol, sig.EntryPrice, sig.SLPrice, sig.EMA10, sig.RSScore)
}

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
