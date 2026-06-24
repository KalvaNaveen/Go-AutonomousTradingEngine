// Package model holds the core BTST domain types shared across the broker,
// store, and engine packages (kept separate to avoid import cycles).
package model

import "time"

// Status of a BTST position.
const (
	StatusOpen   = "open"
	StatusClosed = "closed"
)

// Exit reasons recorded when a position is closed.
const (
	ExitSquareOff = "squareoff" // sold at next-day 3:20 PM
	ExitStopLoss  = "stoploss"  // SL breached overnight/next morning
)

// Position is one BTST trade: bought today at 3:20 PM, sold the next trading
// day at 3:20 PM (or earlier if the stop-loss is hit).
type Position struct {
	ID         int64
	Symbol     string
	Qty        int
	EntryPrice float64
	EntryTime  time.Time
	SLPrice    float64
	TradeDate  string // YYYY-MM-DD of entry (the day the stock was bought)
	Paper      bool   // true = simulated, false = real Kite order
	BuyOrderID string

	Status     string // open | closed
	ExitPrice  float64
	ExitTime   time.Time
	ExitReason string  // squareoff | stoploss
	PnL        float64 // (ExitPrice - EntryPrice) * Qty, set on close
}

// Invested returns the capital deployed into this position at entry.
func (p Position) Invested() float64 { return p.EntryPrice * float64(p.Qty) }

// PnLPct returns the percentage return once closed.
func (p Position) PnLPct() float64 {
	if p.EntryPrice == 0 {
		return 0
	}
	return (p.ExitPrice - p.EntryPrice) / p.EntryPrice * 100
}
