// Package store persists BTST positions (open trades, then closed trades with
// realised P&L). It is the single source of truth for the dashboard and for the
// next-day square-off.
//
// Backend is chosen at Open: if TURSO_DATABASE_URL is set it uses Turso (libSQL,
// durable, free tier) so position records survive restarts/redeploys — required
// for an overnight BTST hold on an ephemeral free cloud box. Otherwise it falls
// back to a local SQLite file (dev/paper). libSQL is SQLite-compatible, so the
// same schema and queries work unchanged.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"time"

	"bnf_go_engine/model"

	_ "github.com/tursodatabase/libsql-client-go/libsql"
	_ "modernc.org/sqlite"
)

// Store wraps the database.
type Store struct {
	db *sql.DB
}

// Open opens (and migrates) the positions database. If TURSO_DATABASE_URL is set,
// it connects to Turso (durable); otherwise it opens the local SQLite file at path.
func Open(path string) (*Store, error) {
	driver, dsn := "sqlite", path
	if url := os.Getenv("TURSO_DATABASE_URL"); url != "" {
		driver = "libsql"
		dsn = url
		if tok := os.Getenv("TURSO_AUTH_TOKEN"); tok != "" {
			sep := "?"
			if containsRune(url, '?') {
				sep = "&"
			}
			dsn = url + sep + "authToken=" + tok
		}
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS positions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    symbol      TEXT    NOT NULL,
    qty         INTEGER NOT NULL,
    entry_price REAL    NOT NULL,
    entry_time  TEXT    NOT NULL,
    sl_price    REAL    NOT NULL,
    trade_date  TEXT    NOT NULL,
    paper       INTEGER NOT NULL,
    buy_order   TEXT,
    status      TEXT    NOT NULL,
    exit_price  REAL,
    exit_time   TEXT,
    exit_reason TEXT,
    pnl         REAL,
    peak_price  REAL,             -- trailing-SL watermark (highest seen since entry)
    last_price  REAL,             -- most recent monitored price
    carry_count INTEGER DEFAULT 0 -- times the screener re-listed this holding
);
CREATE INDEX IF NOT EXISTS idx_status ON positions(status);
CREATE INDEX IF NOT EXISTS idx_trade_date ON positions(trade_date);

CREATE TABLE IF NOT EXISTS scans (
    scan_date  TEXT    NOT NULL,
    symbol     TEXT    NOT NULL,
    close      REAL    NOT NULL,
    outcome    TEXT    NOT NULL,  -- traded | carried | dropped | held
    reason     TEXT,
    scanned_at TEXT,              -- HH:MM:SS IST when the scan ran
    source     TEXT,              -- screener slug(s) the stock came from
    per_chg    REAL,              -- daily %-change at scan time (list is sorted by this)
    PRIMARY KEY (scan_date, symbol)
);
CREATE INDEX IF NOT EXISTS idx_scan_date ON scans(scan_date);

CREATE TABLE IF NOT EXISTS sl_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    position_id INTEGER NOT NULL,
    symbol      TEXT    NOT NULL,
    at          TEXT    NOT NULL, -- RFC3339 IST
    price       REAL    NOT NULL, -- price that caused the update
    old_sl      REAL    NOT NULL,
    new_sl      REAL    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sl_pos ON sl_events(position_id);`)
	if err != nil {
		return err
	}
	// Best-effort adds for pre-existing tables; ignore "duplicate column" errors.
	for _, stmt := range []string{
		`ALTER TABLE scans ADD COLUMN scanned_at TEXT`,
		`ALTER TABLE scans ADD COLUMN source TEXT`,
		`ALTER TABLE scans ADD COLUMN per_chg REAL`,
		`ALTER TABLE positions ADD COLUMN peak_price REAL`,
		`ALTER TABLE positions ADD COLUMN last_price REAL`,
		`ALTER TABLE positions ADD COLUMN carry_count INTEGER DEFAULT 0`,
	} {
		_, _ = s.db.Exec(stmt)
	}
	return nil
}

// ScanTime returns the HH:MM:SS IST time the scan for date ran ("" if none).
func (s *Store) ScanTime(date string) (string, error) {
	var t sql.NullString
	err := s.db.QueryRow(`SELECT scanned_at FROM scans WHERE scan_date=? LIMIT 1`, date).Scan(&t)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return t.String, err
}

// ScanRow is one stock from a day's ChartInk scan and what happened to it.
type ScanRow struct {
	Date      string  `json:"date"`
	Symbol    string  `json:"symbol"`
	Close     float64 `json:"close"`
	PerChange float64 `json:"per_chg"`
	Outcome   string  `json:"outcome"` // traded | carried | dropped | held
	Reason    string  `json:"reason,omitempty"`
	Source    string  `json:"source,omitempty"` // screener slug(s)
}

// SaveScan replaces the scan record for a date (idempotent across re-runs).
// scannedAt records the moment the scan ran, shown on the dashboard so it can be
// compared against ChartInk at the same instant.
func (s *Store) SaveScan(date string, scannedAt time.Time, rows []ScanRow) error {
	at := scannedAt.Format("15:04:05")
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM scans WHERE scan_date=?`, date); err != nil {
		tx.Rollback()
		return err
	}
	for _, r := range rows {
		if _, err := tx.Exec(`INSERT INTO scans (scan_date, symbol, close, outcome, reason, scanned_at, source, per_chg)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, date, r.Symbol, r.Close, r.Outcome, r.Reason, at, r.Source, r.PerChange); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// LatestScanDate returns the most recent scan_date, or "" if none.
func (s *Store) LatestScanDate() (string, error) {
	var d sql.NullString
	err := s.db.QueryRow(`SELECT MAX(scan_date) FROM scans`).Scan(&d)
	if err != nil {
		return "", err
	}
	return d.String, nil
}

// ScanByDate returns all scanned rows for a date (preserving insertion order).
func (s *Store) ScanByDate(date string) ([]ScanRow, error) {
	rows, err := s.db.Query(`SELECT scan_date, symbol, close, outcome, COALESCE(reason,''), COALESCE(source,''), COALESCE(per_chg,0)
		FROM scans WHERE scan_date=? ORDER BY rowid`, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScanRow
	for rows.Next() {
		var r ScanRow
		if err := rows.Scan(&r.Date, &r.Symbol, &r.Close, &r.Outcome, &r.Reason, &r.Source, &r.PerChange); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Dates returns every date that has a scan or any positions, newest first —
// used to populate the dashboard's history selector.
func (s *Store) Dates() ([]string, error) {
	rows, err := s.db.Query(`
SELECT d FROM (
    SELECT scan_date AS d FROM scans
    UNION
    SELECT trade_date AS d FROM positions
) GROUP BY d ORDER BY d DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// PurgeDate removes all positions and scan rows for a date — used by a forced
// manual re-run so today's data starts clean.
func (s *Store) PurgeDate(date string) error {
	if _, err := s.db.Exec(`DELETE FROM positions WHERE trade_date=?`, date); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM scans WHERE scan_date=?`, date)
	return err
}

// SaveOpen inserts a newly-opened position and returns its assigned ID.
// The trailing-SL watermark starts at the entry price.
func (s *Store) SaveOpen(p *model.Position) error {
	if p.PeakPrice <= 0 {
		p.PeakPrice = p.EntryPrice
	}
	if p.LastPrice <= 0 {
		p.LastPrice = p.EntryPrice
	}
	res, err := s.db.Exec(`
INSERT INTO positions (symbol, qty, entry_price, entry_time, sl_price, trade_date, paper, buy_order, status, peak_price, last_price, carry_count)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		p.Symbol, p.Qty, p.EntryPrice, p.EntryTime.Format(time.RFC3339),
		p.SLPrice, p.TradeDate, boolToInt(p.Paper), p.BuyOrderID, model.StatusOpen,
		p.PeakPrice, p.LastPrice)
	if err != nil {
		return err
	}
	p.ID, _ = res.LastInsertId()
	return nil
}

// UpdateTrail persists a monitor tick: the latest price, watermark, and (when it
// ratcheted) the new trailing stop. When the SL moved, an sl_events audit row is
// written so the trail history is fully reconstructable.
func (s *Store) UpdateTrail(p *model.Position, at time.Time, oldSL float64) error {
	if _, err := s.db.Exec(`
UPDATE positions SET peak_price=?, last_price=?, sl_price=? WHERE id=?`,
		p.PeakPrice, p.LastPrice, p.SLPrice, p.ID); err != nil {
		return err
	}
	if p.SLPrice > oldSL {
		_, err := s.db.Exec(`
INSERT INTO sl_events (position_id, symbol, at, price, old_sl, new_sl)
VALUES (?, ?, ?, ?, ?, ?)`,
			p.ID, p.Symbol, at.Format(time.RFC3339), p.LastPrice, oldSL, p.SLPrice)
		return err
	}
	return nil
}

// MarkCarried increments a holding's carry counter (screener re-listed it, so
// the scheduled sell was skipped).
func (s *Store) MarkCarried(id int64) error {
	_, err := s.db.Exec(`UPDATE positions SET carry_count = carry_count + 1 WHERE id=?`, id)
	return err
}

// SLEvent is one trailing-stop adjustment (audit trail).
type SLEvent struct {
	Symbol string  `json:"symbol"`
	At     string  `json:"at"`
	Price  float64 `json:"price"`
	OldSL  float64 `json:"old_sl"`
	NewSL  float64 `json:"new_sl"`
}

// SLEvents returns the most recent `limit` trailing-stop adjustments.
func (s *Store) SLEvents(limit int) ([]SLEvent, error) {
	rows, err := s.db.Query(`
SELECT symbol, at, price, old_sl, new_sl FROM sl_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SLEvent
	for rows.Next() {
		var e SLEvent
		if err := rows.Scan(&e.Symbol, &e.At, &e.Price, &e.OldSL, &e.NewSL); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ClosePosition records the exit fill, reason, and realised P&L.
func (s *Store) ClosePosition(p *model.Position) error {
	_, err := s.db.Exec(`
UPDATE positions
   SET status=?, exit_price=?, exit_time=?, exit_reason=?, pnl=?
 WHERE id=?`,
		model.StatusClosed, p.ExitPrice, p.ExitTime.Format(time.RFC3339),
		p.ExitReason, p.PnL, p.ID)
	return err
}

// OpenPositions returns all currently-open positions.
func (s *Store) OpenPositions() ([]model.Position, error) {
	return s.query(`WHERE status='open' ORDER BY id`)
}

// PositionsByDate returns all positions entered on the given YYYY-MM-DD.
func (s *Store) PositionsByDate(date string) ([]model.Position, error) {
	return s.query(`WHERE trade_date=? ORDER BY id`, date)
}

// HasEntryFor reports whether any position was already entered on the date —
// used to make the 3:20 entry idempotent across restarts.
func (s *Store) HasEntryFor(date string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM positions WHERE trade_date=?`, date).Scan(&n)
	return n > 0, err
}

// ClosedPositions returns the most recent `limit` closed trades (newest first).
func (s *Store) ClosedPositions(limit int) ([]model.Position, error) {
	return s.query(fmt.Sprintf(`WHERE status='closed' ORDER BY id DESC LIMIT %d`, limit))
}

func (s *Store) query(where string, args ...any) ([]model.Position, error) {
	rows, err := s.db.Query(`
SELECT id, symbol, qty, entry_price, entry_time, sl_price, trade_date, paper,
       COALESCE(buy_order,''), status, COALESCE(exit_price,0), COALESCE(exit_time,''),
       COALESCE(exit_reason,''), COALESCE(pnl,0),
       COALESCE(peak_price,entry_price), COALESCE(last_price,entry_price), COALESCE(carry_count,0)
  FROM positions `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Position
	for rows.Next() {
		var p model.Position
		var paper int
		var entryTime, exitTime string
		if err := rows.Scan(&p.ID, &p.Symbol, &p.Qty, &p.EntryPrice, &entryTime,
			&p.SLPrice, &p.TradeDate, &paper, &p.BuyOrderID, &p.Status,
			&p.ExitPrice, &exitTime, &p.ExitReason, &p.PnL,
			&p.PeakPrice, &p.LastPrice, &p.CarryCount); err != nil {
			return nil, err
		}
		p.Paper = paper == 1
		p.EntryTime, _ = time.Parse(time.RFC3339, entryTime)
		if exitTime != "" {
			p.ExitTime, _ = time.Parse(time.RFC3339, exitTime)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
