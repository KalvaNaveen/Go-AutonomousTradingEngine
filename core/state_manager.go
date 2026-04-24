package core

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	"bnf_go_engine/config"

	_ "modernc.org/sqlite"
)

// StateManager persists all active trade state to SQLite.
// Uses a persistent connection pool for performance.
type StateManager struct {
	dbPath string
	db     *sql.DB
}

func NewStateManager() *StateManager {
	sm := &StateManager{dbPath: config.StateDB}
	db, err := sql.Open("sqlite", sm.dbPath)
	if err != nil {
		log.Printf("[StateManager] DB open error: %v", err)
	} else {
		db.SetMaxOpenConns(1) // SQLite only supports one writer
		db.Exec("PRAGMA journal_mode=WAL")
		db.Exec("PRAGMA busy_timeout=5000")
		sm.db = db
	}
	sm.initDB()
	return sm
}

func (sm *StateManager) initDB() {
	if sm.db == nil {
		return
	}
	db := sm.db

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
			token           INTEGER DEFAULT 0,
			is_short        INTEGER DEFAULT 0,
			rsi             REAL DEFAULT 0,
			adx             REAL DEFAULT 0,
			vix             REAL DEFAULT 0,
			ad_ratio        REAL DEFAULT 0
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
		{"is_short", "INTEGER DEFAULT 0"},
		{"rsi", "REAL DEFAULT 0"},
		{"adx", "REAL DEFAULT 0"},
		{"vix", "REAL DEFAULT 0"},
		{"ad_ratio", "REAL DEFAULT 0"},
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
	RSI           float64
	ADX           float64
	VIX           float64
	ADRatio       float64
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
	PeakPnl        float64 // High-water mark: maximum net P&L reached during trade lifetime
}

func (sm *StateManager) Save(entryOID string, t *Trade) {
	if sm.db == nil {
		return
	}

	now := config.NowIST().Format(time.RFC3339)
	pf := 0
	if t.PartialFilled {
		pf = 1
	}

	isShortInt := 0
	if t.IsShort {
		isShortInt = 1
	}

	sm.db.Exec(`
		INSERT OR REPLACE INTO active_positions VALUES
		(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`,
		entryOID, t.Symbol, t.Strategy, t.Product, t.Regime,
		t.EntryPrice, t.StopPrice, t.PartialTarget, t.TargetPrice,
		t.Qty, t.PartialQty, t.RemainingQty, pf,
		t.SLOID, t.PartialOID, t.TargetOID,
		t.EntryTime.Format(time.RFC3339), t.EntryDate.Format("2006-01-02"),
		t.RVol, t.DeviationPct, "OPEN", now,
		t.TrailStop, t.PyramidAdded, t.RSScore, t.MarketStatus,
		t.WeeksNoProg, t.Token, isShortInt, t.RSI, t.ADX, t.VIX, t.ADRatio,
	)
}

func (sm *StateManager) MarkPartialFilled(entryOID string, remainingQty int) {
	if sm.db == nil {
		return
	}

	sm.db.Exec(`
		UPDATE active_positions
		SET partial_filled=1, remaining_qty=?, last_updated=?
		WHERE entry_oid=?
	`, remainingQty, config.NowIST().Format(time.RFC3339), entryOID)
}

func (sm *StateManager) Close(entryOID string) {
	if sm.db == nil {
		return
	}

	sm.db.Exec(`
		UPDATE active_positions
		SET status='CLOSED', last_updated=?
		WHERE entry_oid=?
	`, config.NowIST().Format(time.RFC3339), entryOID)
}

func (sm *StateManager) SetKV(key, value string) {
	if sm.db == nil {
		return
	}
	sm.db.Exec("INSERT OR REPLACE INTO kv_store VALUES (?,?)", key, value)
}

func (sm *StateManager) GetKV(key, defaultVal string) string {
	if sm.db == nil {
		return defaultVal
	}

	var val string
	err := sm.db.QueryRow("SELECT value FROM kv_store WHERE key=?", key).Scan(&val)
	if err != nil {
		return defaultVal
	}
	return val
}

func (sm *StateManager) LoadOpenPositions() []*Trade {
	if sm.db == nil {
		return nil
	}

	cutoff := config.TodayIST().AddDate(0, 0, -1).Format("2006-01-02")
	rows, err := sm.db.Query(`
		SELECT entry_oid, symbol, strategy, product, COALESCE(regime,''),
		       entry_price, stop_price, COALESCE(partial_target,0), target_price,
		       qty, partial_qty, remaining_qty, partial_filled,
		       COALESCE(sl_oid,''), COALESCE(partial_oid,''), COALESCE(target_oid,''),
		       entry_time, entry_date,
		       CAST(COALESCE(NULLIF(rvol,''),0) AS REAL), CAST(COALESCE(NULLIF(deviation_pct,''),0) AS REAL),
		       CAST(COALESCE(NULLIF(trail_stop,''),0) AS REAL), CAST(COALESCE(NULLIF(pyramid_added,''),0) AS INTEGER),
		       CAST(COALESCE(NULLIF(rs_score,''),0) AS INTEGER), COALESCE(market_status,''),
		       CAST(COALESCE(NULLIF(weeks_no_progress,''),0) AS INTEGER), CAST(COALESCE(NULLIF(token,''),0) AS INTEGER),
		       CAST(COALESCE(NULLIF(is_short,''),0) AS INTEGER),
		       CAST(COALESCE(NULLIF(rsi,''),0) AS REAL), CAST(COALESCE(NULLIF(adx,''),0) AS REAL),
		       CAST(COALESCE(NULLIF(vix,''),0) AS REAL), CAST(COALESCE(NULLIF(ad_ratio,''),0) AS REAL)
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
		var pf, isShortInt int
		err := rows.Scan(
			&t.EntryOID, &t.Symbol, &t.Strategy, &t.Product, &t.Regime,
			&t.EntryPrice, &t.StopPrice, &t.PartialTarget, &t.TargetPrice,
			&t.Qty, &t.PartialQty, &t.RemainingQty, &pf,
			&t.SLOID, &t.PartialOID, &t.TargetOID,
			&entryTimeStr, &entryDateStr,
			&t.RVol, &t.DeviationPct,
			&t.TrailStop, &t.PyramidAdded, &t.RSScore, &t.MarketStatus,
			&t.WeeksNoProg, &t.Token, &isShortInt,
			&t.RSI, &t.ADX, &t.VIX, &t.ADRatio,
		)
		if err != nil {
			continue
		}
		t.PartialFilled = pf == 1
		t.EntryTime, _ = time.Parse(time.RFC3339, entryTimeStr)
		t.EntryDate, _ = time.Parse("2006-01-02", entryDateStr)
		t.IsShort = isShortInt == 1
		trades = append(trades, t)
	}
	return trades
}
