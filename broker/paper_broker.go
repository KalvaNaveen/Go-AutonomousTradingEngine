package broker

import (
	"fmt"
	"log"
	"math"
	"sync"
	"time"
)

// SlippagePct simulates market impact — buy costs more, sell receives less
const SlippagePct = 0.0004 // 0.04%

// BrokerageFee per order (Zerodha flat fee)
const BrokerageFee = 20.0

// PaperOrder represents a virtual order in the paper order book
type PaperOrder struct {
	OrderID         string
	Symbol          string
	TransactionType string // "BUY" or "SELL"
	Quantity        int
	OrderType       string  // "MARKET", "LIMIT", "SL-M", "SL"
	Price           float64 // limit price
	TriggerPrice    float64 // SL trigger price
	Status          string  // "OPEN", "COMPLETE", "CANCELLED"
	FilledQty       int
	PendingQty      int
	AveragePrice    float64
	PlacedAt        time.Time
}

// PaperPosition tracks a virtual position for a symbol
type PaperPosition struct {
	Qty      int     // signed: +ve = long, -ve = short
	AvgPrice float64
	Product  string
}

// RealisticPaperBroker simulates order fills with real market data.
// Port of Python core/paper_broker.py (349 lines).
//
// Fill simulation rules:
//   MARKET      → fills immediately at real LTP ± slippage
//   LIMIT BUY   → fills when real LTP <= limit price
//   LIMIT SELL  → fills when real LTP >= limit price
//   SL-M SELL   → fills when real LTP <= trigger price
//   SL-M BUY    → fills when real LTP >= trigger price
type RealisticPaperBroker struct {
	mu sync.Mutex

	orders    map[string]*PaperOrder
	positions map[string]*PaperPosition
	orderSeq  int64

	// Capital tracking
	InitialCapital  float64
	AvailableMargin float64
	RealisedPnl     float64
	TotalBrokerage  float64
	TradesCompleted int

	// Data feed for realistic fill prices
	GetLTP   func(symbol string) float64
	GetDepth func(symbol string) (bidPrice, askPrice float64) // Spread-aware pricing

	// Background fill loop control
	running bool
	stopCh  chan struct{}
}

// NewRealisticPaperBroker creates a paper broker with full order simulation
func NewRealisticPaperBroker(capital float64) *RealisticPaperBroker {
	pb := &RealisticPaperBroker{
		orders:          make(map[string]*PaperOrder),
		positions:       make(map[string]*PaperPosition),
		InitialCapital:  capital,
		AvailableMargin: capital,
		running:         true,
		stopCh:          make(chan struct{}),
	}
	// Start background fill loop for pending LIMIT/SL orders
	go pb.fillLoop()
	return pb
}

// PlaceOrder places a virtual order. MARKET orders fill immediately.
func (pb *RealisticPaperBroker) PlaceOrder(symbol string, qty int, isShort bool, orderType string, price float64) (string, error) {
	pb.mu.Lock()
	defer pb.mu.Unlock()

	pb.orderSeq++
	oid := fmt.Sprintf("PAPER_%d_%d", time.Now().UnixNano(), pb.orderSeq)

	txnType := "BUY"
	if isShort {
		txnType = "SELL"
	}

	order := &PaperOrder{
		OrderID:         oid,
		Symbol:          symbol,
		TransactionType: txnType,
		Quantity:        qty,
		OrderType:       orderType,
		Price:           price,
		Status:          "OPEN",
		FilledQty:       0,
		PendingQty:      qty,
		PlacedAt:        time.Now(),
	}
	pb.orders[oid] = order

	// MARKET orders fill immediately at real LTP
	if orderType == "MARKET" {
		ltp := pb.getLTPLocked(symbol)
		if ltp > 0 {
			pb.fillOrder(oid, ltp)
		} else {
			// Fallback: fill at declared price if LTP unavailable
			if price > 0 {
				pb.fillOrder(oid, price)
			} else {
				log.Printf("[Paper] WARNING: MARKET order %s has no LTP and no price — left OPEN", oid)
			}
		}
	}

	direction := "BUY"
	if isShort {
		direction = "SELL"
	}
	fillStatus := "OPEN"
	if order.Status == "COMPLETE" {
		fillStatus = fmt.Sprintf("FILLED@%.2f", order.AveragePrice)
	}
	log.Printf("[Paper] %s %s x%d %s → %s [%s]", direction, symbol, qty, orderType, oid, fillStatus)
	return oid, nil
}

// CancelOrder cancels a pending order
func (pb *RealisticPaperBroker) CancelOrder(orderID string) error {
	pb.mu.Lock()
	defer pb.mu.Unlock()

	o, ok := pb.orders[orderID]
	if !ok {
		return fmt.Errorf("order %s not found", orderID)
	}
	if o.Status == "OPEN" {
		o.Status = "CANCELLED"
		o.PendingQty = 0
		log.Printf("[Paper] Cancelled: %s (%s %s x%d)", orderID, o.TransactionType, o.Symbol, o.Quantity)
	}
	return nil
}

// fillLoop polls LTP every 500ms to check if pending LIMIT/SL orders should fill.
// This background goroutine ensures LIMIT orders only fill when the market
// actually reaches your price — not instantly like the old stub broker.
func (pb *RealisticPaperBroker) fillLoop() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-pb.stopCh:
			return
		case <-ticker.C:
			pb.checkPendingFills()
		}
	}
}

// checkPendingFills checks all OPEN non-MARKET orders against current LTP
func (pb *RealisticPaperBroker) checkPendingFills() {
	pb.mu.Lock()
	defer pb.mu.Unlock()

	for oid, o := range pb.orders {
		if o.Status != "OPEN" || o.OrderType == "MARKET" {
			continue
		}

		ltp := pb.getLTPLocked(o.Symbol)
		if ltp <= 0 {
			continue
		}

		shouldFill := false
		fillPrice := ltp

		switch o.OrderType {
		case "LIMIT":
			if o.TransactionType == "BUY" && ltp <= o.Price {
				shouldFill = true
				fillPrice = math.Min(ltp, o.Price) // fill at better price
			} else if o.TransactionType == "SELL" && ltp >= o.Price {
				shouldFill = true
				fillPrice = math.Max(ltp, o.Price)
			}

		case "SL-M", "SL":
			if o.TransactionType == "SELL" && ltp <= o.TriggerPrice {
				shouldFill = true
				// SL (stop-limit): cap at limit price
				if o.OrderType == "SL" && o.Price > 0 {
					fillPrice = math.Max(ltp, o.Price)
				}
			} else if o.TransactionType == "BUY" && ltp >= o.TriggerPrice {
				shouldFill = true
				if o.OrderType == "SL" && o.Price > 0 {
					fillPrice = math.Min(ltp, o.Price)
				}
			}
		}

		if shouldFill {
			pb.fillOrder(oid, fillPrice)
		}
	}
}

// fillOrder executes the fill with slippage and updates positions/margin.
// Must be called with pb.mu held.
func (pb *RealisticPaperBroker) fillOrder(oid string, fillPrice float64) {
	o, ok := pb.orders[oid]
	if !ok || o.Status != "OPEN" {
		return
	}

	// Smart Execution: use real bid/ask spread when available
	// Buys fill at ASK price (what sellers demand), sells fill at BID price (what buyers offer)
	// This is MORE realistic than flat slippage — it's how real orders work
	execPrice := fillPrice
	if pb.GetDepth != nil {
		bidPrice, askPrice := pb.GetDepth(o.Symbol)
		if bidPrice > 0 && askPrice > 0 {
			if o.TransactionType == "BUY" {
				execPrice = askPrice // Pay the ask
			} else {
				execPrice = bidPrice // Receive the bid
			}
		} else {
			// Fallback to slippage model
			if o.TransactionType == "BUY" {
				execPrice = fillPrice * (1 + SlippagePct)
			} else {
				execPrice = fillPrice * (1 - SlippagePct)
			}
		}
	} else {
		// No depth data — use slippage model
		if o.TransactionType == "BUY" {
			execPrice = fillPrice * (1 + SlippagePct)
		} else {
			execPrice = fillPrice * (1 - SlippagePct)
		}
	}

	o.Status = "COMPLETE"
	o.FilledQty = o.Quantity
	o.PendingQty = 0
	o.AveragePrice = execPrice

	// Charge brokerage
	pb.TotalBrokerage += BrokerageFee
	pb.RealisedPnl -= BrokerageFee

	sym := o.Symbol
	qty := o.Quantity

	// Initialize position if needed
	if _, exists := pb.positions[sym]; !exists {
		pb.positions[sym] = &PaperPosition{Qty: 0, AvgPrice: 0, Product: "MIS"}
	}
	pos := pb.positions[sym]
	prevQty := pos.Qty

	if o.TransactionType == "BUY" {
		if prevQty < 0 {
			// Closing/reducing a short position → realise PnL
			closingQty := intMin(qty, intAbs(prevQty))
			// Short PnL = (entry - exit) * qty
			realised := (pos.AvgPrice - execPrice) * float64(closingQty)
			pb.RealisedPnl += realised
			pos.Qty += qty
			pb.AvailableMargin += execPrice * float64(closingQty) // margin released
			if pos.Qty >= 0 {
				pb.TradesCompleted++
				if pos.Qty > 0 {
					pos.AvgPrice = execPrice
				} else {
					pos.AvgPrice = 0
				}
			}
		} else {
			// Opening/adding to long
			totalCost := pos.AvgPrice*float64(pos.Qty) + execPrice*float64(qty)
			pos.Qty += qty
			if pos.Qty > 0 {
				pos.AvgPrice = totalCost / float64(pos.Qty)
			}
			pb.AvailableMargin -= execPrice * float64(qty) // margin consumed
		}
	} else { // SELL
		if prevQty > 0 {
			// Closing/reducing a long position → realise PnL
			closingQty := intMin(qty, prevQty)
			realised := (execPrice - pos.AvgPrice) * float64(closingQty)
			pb.RealisedPnl += realised
			pos.Qty -= qty
			pb.AvailableMargin += execPrice * float64(closingQty)
			if pos.Qty <= 0 {
				pb.TradesCompleted++
				if pos.Qty < 0 {
					pos.AvgPrice = execPrice
				} else {
					pos.AvgPrice = 0
				}
			}
		} else {
			// Opening/adding to short
			totalCost := pos.AvgPrice*float64(intAbs(pos.Qty)) + execPrice*float64(qty)
			pos.Qty -= qty
			if pos.Qty != 0 {
				pos.AvgPrice = totalCost / float64(intAbs(pos.Qty))
			}
			pb.AvailableMargin -= execPrice * float64(qty)
		}
	}

	log.Printf("[Paper] FILLED %s %s x%d @ %.2f (slippage=%.2f%%) margin=₹%.0f",
		o.TransactionType, sym, qty, execPrice, SlippagePct*100, pb.AvailableMargin)
}

// getLTPLocked fetches LTP. Must be called with pb.mu held — delegates to
// the externally-injected GetLTP function.
func (pb *RealisticPaperBroker) getLTPLocked(symbol string) float64 {
	if pb.GetLTP != nil {
		return pb.GetLTP(symbol)
	}
	return 0
}

// GetSummary returns paper trading statistics
func (pb *RealisticPaperBroker) GetSummary() map[string]interface{} {
	pb.mu.Lock()
	defer pb.mu.Unlock()

	totalOrders := len(pb.orders)
	filled := 0
	cancelled := 0
	open := 0
	for _, o := range pb.orders {
		switch o.Status {
		case "COMPLETE":
			filled++
		case "CANCELLED":
			cancelled++
		case "OPEN":
			open++
		}
	}

	openExposure := 0.0
	for _, pos := range pb.positions {
		if pos.Qty != 0 {
			openExposure += math.Abs(float64(pos.Qty)) * pos.AvgPrice
		}
	}

	return map[string]interface{}{
		"total_orders":     totalOrders,
		"filled":           filled,
		"cancelled":        cancelled,
		"open":             open,
		"trades_completed": pb.TradesCompleted,
		"realised_pnl":     math.Round(pb.RealisedPnl*100) / 100,
		"total_brokerage":  math.Round(pb.TotalBrokerage*100) / 100,
		"available_margin": math.Round(pb.AvailableMargin*100) / 100,
		"capital_deployed": math.Round(openExposure*100) / 100,
	}
}

// Stop terminates the background fill loop
func (pb *RealisticPaperBroker) Stop() {
	close(pb.stopCh)
}

func intMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func intAbs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}
