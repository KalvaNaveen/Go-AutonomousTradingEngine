package core

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"bnf_go_engine/config"

	_ "modernc.org/sqlite"
)

// StateManager persists all active trade state to SQLite.
// Exact port of Python core/state_manager.py
type StateManager struct {
	dbPath string
}

func NewStateManager() *StateManager {
	sm := &StateManager{dbPath: config.StateDB}
	sm.initDB()
	return sm
}

func (sm *StateManager) initDB() {
	db, err := sql.Open("sqlite", sm.dbPath)
	if err != nil {
		log.Printf("[StateManager] DB open error: %v", err)
		return
	}
	defer db.Close()

	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA busy_timeout=3000")

	db.Exec(`
		CREATE TABLE IF NOT EXISTS active_positions (
			entry_oid       TEXT PRIMARY KEY,
			symbol          TEXT NOT NULL,
			strategy        TEXT NOT NULL,
			product         TEXT NOT NULL,
			regime          TEXT,
			entry_price     REAL NOT NULL,
			stop_price      REAL NOT NULL,
			partial_target  REAL,
			target_price    REAL NOT NULL,
			qty             INTEGER NOT NULL,
			partial_qty     INTEGER DEFAULT 0,
			remaining_qty   INTEGER NOT NULL,
			partial_filled  INTEGER DEFAULT 0,
			sl_oid          TEXT,
			partial_oid     TEXT,
			target_oid      TEXT,
			entry_time      TEXT NOT NULL,
			entry_date      TEXT NOT NULL,
			rvol            REAL DEFAULT 0,
			deviation_pct   REAL DEFAULT 0,
			status          TEXT DEFAULT 'OPEN',
			last_updated    TEXT,
			trail_stop      REAL DEFAULT 0,
			pyramid_added   INTEGER DEFAULT 0,
			rs_score        INTEGER DEFAULT 0,
			market_status   TEXT DEFAULT '',
			weeks_no_progress INTEGER DEFAULT 0,
			token           INTEGER DEFAULT 0
		)
	`)

	db.Exec(`
		CREATE TABLE IF NOT EXISTS kv_store (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)
	`)

	// Migration columns (idempotent)
	migrations := []struct{ col, def string }{
		{"trail_stop", "REAL DEFAULT 0"},
		{"pyramid_added", "INTEGER DEFAULT 0"},
		{"rs_score", "INTEGER DEFAULT 0"},
		{"market_status", "TEXT DEFAULT ''"},
		{"weeks_no_progress", "INTEGER DEFAULT 0"},
		{"token", "INTEGER DEFAULT 0"},
	}
	for _, m := range migrations {
		db.Exec(fmt.Sprintf("ALTER TABLE active_positions ADD COLUMN %s %s", m.col, m.def))
	}
}

// Trade represents a position stored in SQLite
type Trade struct {
	EntryOID      string
	Symbol        string
	Strategy      string
	Product       string
	Regime        string
	EntryPrice    float64
	StopPrice     float64
	PartialTarget float64
	TargetPrice   float64
	Qty           int
	PartialQty    int
	RemainingQty  int
	PartialFilled bool
	SLOID         string
	PartialOID    string
	TargetOID     string
	EntryTime     time.Time
	EntryDate     time.Time
	RVol          float64
	DeviationPct  float64
	TrailStop     float64
	PyramidAdded  int
	RSScore       int
	MarketStatus  string
	WeeksNoProg   int
	Token          uint32
	IsShort        bool
	EntryCancelled bool
	RealisedPnl    float64
	MaxHoldMins    int
}

func (sm *StateManager) Save(entryOID string, t *Trade) {
	db, err := sql.Open("sqlite", sm.dbPath)
	if err != nil {
		return
	}
	defer db.Close()

	now := config.NowIST().Format(time.RFC3339)
	pf := 0
	if t.PartialFilled {
		pf = 1
	}

	db.Exec(`
		INSERT OR REPLACE INTO active_positions VALUES
		(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`,
		entryOID, t.Symbol, t.Strategy, t.Product, t.Regime,
		t.EntryPrice, t.StopPrice, t.PartialTarget, t.TargetPrice,
		t.Qty, t.PartialQty, t.RemainingQty, pf,
		t.SLOID, t.PartialOID, t.TargetOID,
		t.EntryTime.Format(time.RFC3339), t.EntryDate.Format("2006-01-02"),
		t.RVol, t.DeviationPct, "OPEN", now,
		t.TrailStop, t.PyramidAdded, t.RSScore, t.MarketStatus,
		t.WeeksNoProg, t.Token,
	)
}

func (sm *StateManager) MarkPartialFilled(entryOID string, remainingQty int) {
	db, err := sql.Open("sqlite", sm.dbPath)
	if err != nil {
		return
	}
	defer db.Close()

	db.Exec(`
		UPDATE active_positions
		SET partial_filled=1, remaining_qty=?, last_updated=?
		WHERE entry_oid=?
	`, remainingQty, config.NowIST().Format(time.RFC3339), entryOID)
}

func (sm *StateManager) Close(entryOID string) {
	db, err := sql.Open("sqlite", sm.dbPath)
	if err != nil {
		return
	}
	defer db.Close()

	db.Exec(`
		UPDATE active_positions
		SET status='CLOSED', last_updated=?
		WHERE entry_oid=?
	`, config.NowIST().Format(time.RFC3339), entryOID)
}

func (sm *StateManager) SetKV(key, value string) {
	db, err := sql.Open("sqlite", sm.dbPath)
	if err != nil {
		return
	}
	defer db.Close()
	db.Exec("INSERT OR REPLACE INTO kv_store VALUES (?,?)", key, value)
}

func (sm *StateManager) GetKV(key, defaultVal string) string {
	db, err := sql.Open("sqlite", sm.dbPath)
	if err != nil {
		return defaultVal
	}
	defer db.Close()

	var val string
	err = db.QueryRow("SELECT value FROM kv_store WHERE key=?", key).Scan(&val)
	if err != nil {
		return defaultVal
	}
	return val
}

func (sm *StateManager) LoadOpenPositions() []*Trade {
	db, err := sql.Open("sqlite", sm.dbPath)
	if err != nil {
		return nil
	}
	defer db.Close()

	cutoff := config.TodayIST().AddDate(0, 0, -1).Format("2006-01-02")
	rows, err := db.Query(`
		SELECT entry_oid, symbol, strategy, product, regime,
		       entry_price, stop_price, partial_target, target_price,
		       qty, partial_qty, remaining_qty, partial_filled,
		       sl_oid, partial_oid, target_oid,
		       entry_time, entry_date,
		       rvol, deviation_pct,
		       COALESCE(trail_stop,0), COALESCE(pyramid_added,0),
		       COALESCE(rs_score,0), COALESCE(market_status,''),
		       COALESCE(weeks_no_progress,0), COALESCE(token,0)
		FROM active_positions
		WHERE status='OPEN' AND entry_date >= ?
		ORDER BY entry_time ASC
	`, cutoff)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var trades []*Trade
	for rows.Next() {
		t := &Trade{}
		var entryTimeStr, entryDateStr string
		var pf int
		err := rows.Scan(
			&t.EntryOID, &t.Symbol, &t.Strategy, &t.Product, &t.Regime,
			&t.EntryPrice, &t.StopPrice, &t.PartialTarget, &t.TargetPrice,
			&t.Qty, &t.PartialQty, &t.RemainingQty, &pf,
			&t.SLOID, &t.PartialOID, &t.TargetOID,
			&entryTimeStr, &entryDateStr,
			&t.RVol, &t.DeviationPct,
			&t.TrailStop, &t.PyramidAdded, &t.RSScore, &t.MarketStatus,
			&t.WeeksNoProg, &t.Token,
		)
		if err != nil {
			continue
		}
		t.PartialFilled = pf == 1
		t.EntryTime, _ = time.Parse(time.RFC3339, entryTimeStr)
		t.EntryDate, _ = time.Parse("2006-01-02", entryDateStr)
		t.IsShort = strings.Contains(strings.ToUpper(t.Strategy), "SHORT")
		trades = append(trades, t)
	}
	return trades
}
