package broker

import (
	"fmt"
	"sync"

	"bnf_go_engine/model"
)

// PaperBroker simulates order placement for the 30-day paper run.
//
// Fills happen at the reference price injected via SetPrice (the 3:20 PM Chartink
// close on entry, or the next-day price on square-off). The stop-loss is NOT a
// resting order here — the engine tracks it in software against next-morning OHLC,
// which mirrors the safe live BTST pattern, so paper and live behave identically.
type PaperBroker struct {
	mu     sync.Mutex
	prices map[string]float64
	seq    int
}

// NewPaperBroker returns an empty paper broker.
func NewPaperBroker() *PaperBroker {
	return &PaperBroker{prices: make(map[string]float64)}
}

// SetPrice injects the reference (fill) price for a symbol.
func (p *PaperBroker) SetPrice(symbol string, price float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prices[symbol] = price
}

func (p *PaperBroker) price(symbol string) (float64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	px, ok := p.prices[symbol]
	if !ok || px <= 0 {
		return 0, fmt.Errorf("paper: no reference price for %s", symbol)
	}
	return px, nil
}

func (p *PaperBroker) nextID(prefix string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq++
	return fmt.Sprintf("%s-%06d", prefix, p.seq)
}

// PlaceMarketBuy fills immediately at the injected reference price.
func (p *PaperBroker) PlaceMarketBuy(symbol string, qty int) (string, float64, error) {
	px, err := p.price(symbol)
	if err != nil {
		return "", 0, err
	}
	return p.nextID("PBUY"), px, nil
}

// PlaceSLM is a no-op in paper mode (SL is software-tracked by the engine).
func (p *PaperBroker) PlaceSLM(symbol string, qty int, trigger float64) (string, error) {
	return p.nextID("PSLM"), nil
}

// SquareOff fills the sell at the injected reference price.
func (p *PaperBroker) SquareOff(symbol string, qty int) (float64, error) {
	return p.price(symbol)
}

// OpenPositions is unused in paper mode (the store is the source of truth).
func (p *PaperBroker) OpenPositions() ([]model.Position, error) { return nil, nil }

// IsPaper always returns true.
func (p *PaperBroker) IsPaper() bool { return true }
