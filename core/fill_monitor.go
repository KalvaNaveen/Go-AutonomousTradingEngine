package core

import (
	"fmt"
	"log"
	"sync"
	"time"

	"bnf_go_engine/config"
)

// FillMonitor polls order status after entry placement.
// Port of Python scripts/fill_monitor.py
type FillMonitor struct {
	mu      sync.Mutex
	wsCache map[string]*OrderStatus // WebSocket order update cache

	// Broker interfaces
	GetOrderStatus func(orderID string) (*OrderStatus, error)
	PlaceOrder     func(symbol string, qty int, isShort bool, orderType string, price float64) (string, error)
	CancelOrder    func(orderID string) error
	ModifyOrder    func(orderID string, qty int) error
	State          *StateManager
	AlertFn        func(msg string)
}

type OrderStatus struct {
	Status       string
	FilledQty    int
	PendingQty   int
	AveragePrice float64
}

func NewFillMonitor(state *StateManager) *FillMonitor {
	return &FillMonitor{
		wsCache: make(map[string]*OrderStatus),
		State:   state,
		AlertFn: func(msg string) { log.Printf("[FillMonitor] %s", msg) },
	}
}

// OnOrderUpdate is the WebSocket callback for order updates
func (fm *FillMonitor) OnOrderUpdate(orderID string, status string, filledQty int, pendingQty int, avgPrice float64) {
	fm.mu.Lock()
	defer fm.mu.Unlock()
	fm.wsCache[orderID] = &OrderStatus{
		Status:       status,
		FilledQty:    filledQty,
		PendingQty:   pendingQty,
		AveragePrice: avgPrice,
	}
}

// GetStatus checks WebSocket cache first, then REST fallback
func (fm *FillMonitor) GetStatus(orderID string) *OrderStatus {
	fm.mu.Lock()
	cached, ok := fm.wsCache[orderID]
	fm.mu.Unlock()

	if ok {
		return cached
	}

	// REST fallback
	if fm.GetOrderStatus != nil {
		status, err := fm.GetOrderStatus(orderID)
		if err == nil && status != nil {
			fm.mu.Lock()
			fm.wsCache[orderID] = status
			fm.mu.Unlock()
			return status
		}
	}

	return &OrderStatus{Status: "UNKNOWN"}
}

// WaitForFill polls entry order until filled, partial fill+timeout, or timeout.
// On fill: places SL-M + partial + target orders.
// Runs in a goroutine per trade.
func (fm *FillMonitor) WaitForFill(trade *Trade) *Trade {
	entryOID := trade.EntryOID
	symbol := trade.Symbol
	timeoutAt := config.NowIST().Add(time.Duration(config.FillTimeoutMinutes) * time.Minute)

	for config.NowIST().Before(timeoutAt) {
		status := fm.GetStatus(entryOID)

		// Fully filled
		if status.Status == "COMPLETE" {
			actualQty := status.FilledQty
			actualPrice := status.AveragePrice
			if actualPrice <= 0 {
				actualPrice = trade.EntryPrice
			}

			trade = fm.adjustOrderQuantities(trade, actualQty, actualPrice)
			fm.AlertFn(fmt.Sprintf("✅ *FILLED*: `%s` Qty:`%d` @ Rs.`%.2f`",
				symbol, actualQty, actualPrice))
			return trade
		}

		// Cancelled/Rejected
		if status.Status == "CANCELLED" || status.Status == "REJECTED" {
			fm.AlertFn(fmt.Sprintf("⚠️ *ENTRY %s*: `%s` — No position opened",
				status.Status, symbol))
			trade.EntryCancelled = true
			return trade
		}

		time.Sleep(time.Duration(config.FillPollIntervalSec) * time.Second)
	}

	// Timeout
	status := fm.GetStatus(entryOID)
	filledQty := status.FilledQty

	if filledQty == 0 {
		// Cancel entry — no position
		if fm.CancelOrder != nil {
			fm.CancelOrder(entryOID)
		}
		fm.AlertFn(fmt.Sprintf("⏱️ *ENTRY TIMEOUT (0 filled)*: `%s` — Order cancelled", symbol))
		trade.EntryCancelled = true
		return trade
	}

	// Partial fill — cancel remaining, adjust SL/target
	if fm.CancelOrder != nil {
		fm.CancelOrder(entryOID)
	}

	actualPrice := status.AveragePrice
	if actualPrice <= 0 {
		actualPrice = trade.EntryPrice
	}

	trade = fm.adjustOrderQuantities(trade, filledQty, actualPrice)
	fm.AlertFn(fmt.Sprintf("⚠️ *PARTIAL FILL*: `%s` Filled: `%d`/`%d` @ Rs.`%.2f`",
		symbol, filledQty, trade.Qty, actualPrice))
	return trade
}

// adjustOrderQuantities places SL-M + partial + target orders after entry fills
func (fm *FillMonitor) adjustOrderQuantities(trade *Trade, actualQty int, actualPrice float64) *Trade {
	symbol := trade.Symbol

	// Cancel existing exit orders if any (partial fill re-adjustment)
	for _, oid := range []string{trade.SLOID, trade.PartialOID, trade.TargetOID} {
		if oid != "" && fm.CancelOrder != nil {
			fm.CancelOrder(oid)
		}
	}

	partialQty := actualQty / 2
	if partialQty < 1 {
		partialQty = 1
	}
	remainingQty := actualQty - partialQty

	// Place SL-M order
	var newSLOID string
	if fm.PlaceOrder != nil {
		oid, err := fm.PlaceOrder(symbol, actualQty, !trade.IsShort, "SL-M", trade.StopPrice)
		if err != nil {
			log.Printf("[FillMonitor] SL place failed: %v", err)
		} else {
			newSLOID = oid
		}
	}

	// Place partial target
	var newPartialOID string
	if partialQty > 0 && trade.PartialTarget > 0 && fm.PlaceOrder != nil {
		oid, err := fm.PlaceOrder(symbol, partialQty, !trade.IsShort, "LIMIT", trade.PartialTarget)
		if err != nil {
			log.Printf("[FillMonitor] Partial place failed: %v", err)
		} else {
			newPartialOID = oid
		}
	}

	// Place full target
	var newTargetOID string
	targetQty := remainingQty
	if newPartialOID == "" {
		targetQty = actualQty
	}
	if fm.PlaceOrder != nil {
		oid, err := fm.PlaceOrder(symbol, targetQty, !trade.IsShort, "LIMIT", trade.TargetPrice)
		if err != nil {
			log.Printf("[FillMonitor] Target place failed: %v", err)
		} else {
			newTargetOID = oid
		}
	}

	// Update trade
	trade.Qty = actualQty
	trade.PartialQty = partialQty
	trade.RemainingQty = remainingQty
	trade.EntryPrice = actualPrice
	trade.SLOID = newSLOID
	trade.PartialOID = newPartialOID
	trade.TargetOID = newTargetOID

	// Persist adjusted state
	fm.State.Save(trade.EntryOID, trade)
	return trade
}

// CheckPartialExitFilled checks if partial target has been filled
func (fm *FillMonitor) CheckPartialExitFilled(trade *Trade) bool {
	if trade.PartialOID == "" || trade.PartialFilled {
		return false
	}

	status := fm.GetStatus(trade.PartialOID)
	if status.Status == "COMPLETE" {
		trade.PartialFilled = true
		trade.RemainingQty = trade.Qty - trade.PartialQty

		// Reduce SL quantity to remaining shares
		if trade.SLOID != "" && trade.RemainingQty > 0 && fm.ModifyOrder != nil {
			if err := fm.ModifyOrder(trade.SLOID, trade.RemainingQty); err != nil {
				fm.AlertFn(fmt.Sprintf("⚠️ *SL MODIFY FAILED* `%s` remains at full qty. %v",
					trade.Symbol, err))
			}
		}
		return true
	}
	return false
}
