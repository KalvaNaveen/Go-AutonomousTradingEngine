package core

import (
	"fmt"
	"log"
	"strings"
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

	// GTT interfaces — when set, SL is placed as a GTT (persists overnight, survives bot restarts).
	// PlaceGTT creates a single-leg SL GTT for a long position; returns Kite trigger ID.
	// GTT is preferred over SL-M for swing positions held overnight.
	PlaceGTT  func(symbol, exchange string, token uint32, lastPrice float64, qty int, slPrice float64) (int, error)
	CancelGTT func(triggerID int) error
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
	timeoutAt := config.NowIST().Add(30 * time.Minute) // 30 min fill timeout

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

		time.Sleep(10 * time.Second) // Poll every 10s — 30s was too slow for fast breakout reversals
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

// adjustOrderQuantities places exit orders after entry fills.
// If PlaceGTT is wired, places a single-leg SL GTT (persists overnight).
// Otherwise falls back to SL-M + partial + target regular orders.
func (fm *FillMonitor) adjustOrderQuantities(trade *Trade, actualQty int, actualPrice float64) *Trade {
	symbol := trade.Symbol

	// Cancel existing exit orders if any (partial fill re-adjustment)
	for _, oid := range []string{trade.SLOID, trade.PartialOID, trade.TargetOID} {
		if oid != "" {
			if strings.HasPrefix(oid, "GTT_") {
				if fm.CancelGTT != nil {
					var gttID int
					fmt.Sscanf(oid[4:], "%d", &gttID)
					fm.CancelGTT(gttID)
				}
			} else if fm.CancelOrder != nil {
				fm.CancelOrder(oid)
			}
		}
	}

	// ── GTT path: preferred for swing positions held overnight ──
	if fm.PlaceGTT != nil {
		gttID, err := fm.PlaceGTT(symbol, "NSE", trade.Token, actualPrice, actualQty, trade.StopPrice)
		if err != nil {
			log.Printf("[FillMonitor] GTT SL place failed for %s: %v — falling back to SL-M", symbol, err)
			// fall through to SL-M path below
		} else {
			trade.Qty = actualQty
			trade.PartialQty = 0
			trade.RemainingQty = actualQty
			trade.EntryPrice = actualPrice
			trade.SLOID = fmt.Sprintf("GTT_%d", gttID)
			trade.PartialOID = ""
			trade.TargetOID = ""
			fm.State.Save(trade.EntryOID, trade)
			log.Printf("[FillMonitor] GTT SL placed for %s: trigger=₹%.2f ID=%d", symbol, trade.StopPrice, gttID)
			fm.AlertFn(fmt.Sprintf("🛡 *GTT SL SET*: `%s` trigger=₹`%.2f` ID=`%d`", symbol, trade.StopPrice, gttID))
			return trade
		}
	}

	// ── SL-M path: fallback when GTT not available ──
	partialQty := actualQty / 2
	if partialQty < 1 {
		partialQty = 1
	}
	remainingQty := actualQty - partialQty

	var newSLOID string
	if fm.PlaceOrder != nil {
		oid, err := fm.PlaceOrder(symbol, actualQty, !trade.IsShort, "SL-M", trade.StopPrice)
		if err != nil {
			log.Printf("[FillMonitor] SL-M place failed: %v", err)
		} else {
			newSLOID = oid
		}
	}

	var newPartialOID string
	if partialQty > 0 && trade.PartialTarget > 0 && fm.PlaceOrder != nil {
		oid, err := fm.PlaceOrder(symbol, partialQty, !trade.IsShort, "LIMIT", trade.PartialTarget)
		if err != nil {
			log.Printf("[FillMonitor] Partial place failed: %v", err)
		} else {
			newPartialOID = oid
		}
	}

	var newTargetOID string
	targetQty := remainingQty
	if newPartialOID == "" {
		targetQty = actualQty
	}
	if fm.PlaceOrder != nil && trade.TargetPrice > 0 {
		oid, err := fm.PlaceOrder(symbol, targetQty, !trade.IsShort, "LIMIT", trade.TargetPrice)
		if err != nil {
			log.Printf("[FillMonitor] Target place failed: %v", err)
		} else {
			newTargetOID = oid
		}
	}

	trade.Qty = actualQty
	trade.PartialQty = partialQty
	trade.RemainingQty = remainingQty
	trade.EntryPrice = actualPrice
	trade.SLOID = newSLOID
	trade.PartialOID = newPartialOID
	trade.TargetOID = newTargetOID

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
