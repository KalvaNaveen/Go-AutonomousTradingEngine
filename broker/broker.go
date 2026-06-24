// Package broker abstracts order placement so the exact same engine code runs
// in paper mode (simulated fills) and live mode (real Kite orders). The 30-day
// paper run and the live run differ only by which Broker implementation is wired.
package broker

import "bnf_go_engine/model"

// Broker places and queries BTST orders.
//
// In paper mode PlaceMarketBuy fills at the injected reference price and PlaceSLM
// is a no-op (the stop-loss is tracked in software by the engine, matching the
// safe live BTST pattern). In live mode these call the Kite Connect order API.
type Broker interface {
	// PlaceMarketBuy buys qty of symbol at market and returns the fill price.
	PlaceMarketBuy(symbol string, qty int) (orderID string, fillPrice float64, err error)

	// PlaceSLM registers a stop-loss for an open position at the trigger price.
	PlaceSLM(symbol string, qty int, trigger float64) (orderID string, err error)

	// SquareOff sells qty of symbol at market and returns the fill price.
	SquareOff(symbol string, qty int) (fillPrice float64, err error)

	// OpenPositions returns positions currently held at the broker.
	OpenPositions() ([]model.Position, error)

	// IsPaper reports whether this broker simulates orders.
	IsPaper() bool
}

// PriceSetter is implemented by the paper broker so the engine can inject the
// reference fill price (the live broker ignores this — Kite reports the fill).
type PriceSetter interface {
	SetPrice(symbol string, price float64)
}
