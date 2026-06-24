// Package store persists BTST positions to SQLite (open trades, then closed
// trades with realised P&L). It is the single source of truth for the dashboard
// and for next-day square-off, surviving restarts on the cloud box.
package store

import (
	"database/sql"
	"fmt"
	"time"

	"bnf_go_engine/model"

	_ "modernc.org/sqlite"
)

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (and migrates) the positions database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
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
    pnl         REAL
);
CREATE INDEX IF NOT EXISTS idx_status ON positions(status);
CREATE INDEX IF NOT EXISTS idx_trade_date ON positions(trade_date);`)
	return err
}

// SaveOpen inserts a newly-opened position and returns its assigned ID.
func (s *Store) SaveOpen(p *model.Position) error {
	res, err := s.db.Exec(`
INSERT INTO positions (symbol, qty, entry_price, entry_time, sl_price, trade_date, paper, buy_order, status)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Symbol, p.Qty, p.EntryPrice, p.EntryTime.Format(time.RFC3339),
		p.SLPrice, p.TradeDate, boolToInt(p.Paper), p.BuyOrderID, model.StatusOpen)
	if err != nil {
		return err
	}
	p.ID, _ = res.LastInsertId()
	return nil
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
       COALESCE(exit_reason,''), COALESCE(pnl,0)
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
			&p.ExitPrice, &exitTime, &p.ExitReason, &p.PnL); err != nil {
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
