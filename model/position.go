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

// Position is one BTST trade: bought at 3:20 PM, sold the next trading day at
// 3:20 PM — unless the screener re-lists it (carried, hold continues) or the
// trailing stop is breached intraday (exited early).
type Position struct {
	ID         int64
	Symbol     string
	Qty        int
	EntryPrice float64
	EntryTime  time.Time
	SLPrice    float64 // CURRENT trailing stop = PeakPrice × (1 − pct); only ratchets up
	TradeDate  string  // YYYY-MM-DD of entry (the day the stock was bought)
	Paper      bool    // true = simulated, false = real Kite order
	BuyOrderID string

	PeakPrice  float64 // watermark: highest price seen since entry (drives the trail)
	LastPrice  float64 // most recent monitored price (dashboard / unrealised P&L)
	CarryCount int     // times the screener re-listed this holding (skipped the sell)

	Status     string // open | closed
	ExitPrice  float64
	ExitTime   time.Time
	ExitReason string  // squareoff | stoploss
	PnL        float64 // GROSS: (ExitPrice - EntryPrice) * Qty, set on close
	Charges    float64 // round-trip CNC charges (STT/txn/SEBI/stamp/GST/DP), set on close
}

// NetPnL returns the realised P&L after broker/statutory charges.
func (p Position) NetPnL() float64 { return p.PnL - p.Charges }

// NetPct returns the post-charges return % on invested capital (closed trades).
func (p Position) NetPct() float64 {
	if inv := p.Invested(); inv > 0 {
		return p.NetPnL() / inv * 100
	}
	return 0
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

// UnrealPnL returns the mark-to-market P&L of an OPEN position using the last
// monitored price (0 until the monitor has polled at least once).
func (p Position) UnrealPnL() float64 {
	if p.LastPrice <= 0 {
		return 0
	}
	return (p.LastPrice - p.EntryPrice) * float64(p.Qty)
}
