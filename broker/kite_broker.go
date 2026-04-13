package broker

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"bnf_go_engine/config"
)

// KiteBroker handles order placement via Kite REST API
type KiteBroker struct {
	APIKey      string
	AccessToken string
	BaseURL     string
	mu          sync.Mutex
}

func NewKiteBroker() *KiteBroker {
	return &KiteBroker{
		APIKey:      config.KiteAPIKey,
		AccessToken: config.KiteAccessToken,
		BaseURL:     "https://api.kite.trade",
	}
}

// PlaceOrder places an order via Kite REST API
func (k *KiteBroker) PlaceOrder(symbol string, qty int, isShort bool, orderType string, price float64) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	txnType := "BUY"
	if isShort {
		txnType = "SELL"
	}

	kiteOrderType := "MARKET"
	if strings.ToUpper(orderType) == "LIMIT" {
		kiteOrderType = "LIMIT"
	}

	params := url.Values{
		"exchange":         {"NSE"},
		"tradingsymbol":    {symbol},
		"transaction_type": {txnType},
		"quantity":         {strconv.Itoa(qty)},
		"product":          {"MIS"},
		"order_type":       {kiteOrderType},
		"validity":         {"DAY"},
	}
	if kiteOrderType == "LIMIT" && price > 0 {
		params.Set("price", fmt.Sprintf("%.1f", price))
	}

	start := time.Now().UnixNano()
	resp, err := k.doPost("/orders/regular", params)
	latency := time.Now().UnixNano() - start

	if err != nil {
		return "", fmt.Errorf("kite order failed: %v", err)
	}

	var result struct {
		Status string `json:"status"`
		Data   struct {
			OrderID string `json:"order_id"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", fmt.Errorf("kite response parse error: %v", err)
	}

	if result.Status != "success" {
		return "", fmt.Errorf("kite rejected: %s", result.Message)
	}

	log.Printf("[Broker] Order placed: %s %s %s x%d in %dμs → OID=%s",
		txnType, symbol, kiteOrderType, qty, latency/1000, result.Data.OrderID)
	return result.Data.OrderID, nil
}

// CancelOrder cancels an order
func (k *KiteBroker) CancelOrder(orderID string) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	endpoint := fmt.Sprintf("/orders/regular/%s", orderID)
	_, err := k.doDelete(endpoint)
	return err
}

// GetOrders fetches all orders for today
func (k *KiteBroker) GetOrders() ([]map[string]interface{}, error) {
	resp, err := k.doGet("/orders")
	if err != nil {
		return nil, err
	}

	var result struct {
		Data []map[string]interface{} `json:"data"`
	}
	json.Unmarshal(resp, &result)
	return result.Data, nil
}

// GetQuote fetches LTP for a symbol
func (k *KiteBroker) GetQuote(symbol string) (float64, error) {
	key := fmt.Sprintf("NSE:%s", symbol)
	resp, err := k.doGet(fmt.Sprintf("/quote?i=%s", url.QueryEscape(key)))
	if err != nil {
		return 0, err
	}

	var result map[string]interface{}
	json.Unmarshal(resp, &result)

	data, ok := result["data"].(map[string]interface{})
	if !ok {
		return 0, fmt.Errorf("no data in quote response")
	}
	quoteData, ok := data[key].(map[string]interface{})
	if !ok {
		return 0, fmt.Errorf("no quote for %s", key)
	}
	ltp, _ := quoteData["last_price"].(float64)
	return ltp, nil
}

// GetMargins fetches account margins
func (k *KiteBroker) GetMargins() (float64, error) {
	resp, err := k.doGet("/user/margins/equity")
	if err != nil {
		return 0, err
	}

	var result struct {
		Data struct {
			Available struct {
				LiveBalance float64 `json:"live_balance"`
			} `json:"available"`
		} `json:"data"`
	}
	json.Unmarshal(resp, &result)
	return result.Data.Available.LiveBalance, nil
}

// ── HTTP helpers ─────────────────────────────────────────────
func (k *KiteBroker) authHeader() string {
	return fmt.Sprintf("token %s:%s", k.APIKey, k.AccessToken)
}

func (k *KiteBroker) doPost(endpoint string, params url.Values) ([]byte, error) {
	req, _ := http.NewRequest("POST", k.BaseURL+endpoint, strings.NewReader(params.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Kite-Version", "3")
	req.Header.Set("Authorization", k.authHeader())

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (k *KiteBroker) doGet(endpoint string) ([]byte, error) {
	req, _ := http.NewRequest("GET", k.BaseURL+endpoint, nil)
	req.Header.Set("X-Kite-Version", "3")
	req.Header.Set("Authorization", k.authHeader())

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (k *KiteBroker) doDelete(endpoint string) ([]byte, error) {
	req, _ := http.NewRequest("DELETE", k.BaseURL+endpoint, nil)
	req.Header.Set("X-Kite-Version", "3")
	req.Header.Set("Authorization", k.authHeader())

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// PaperBroker simulates order fills for paper trading
type PaperBroker struct {
	mu      sync.Mutex
	orderID int64
}

func NewPaperBroker() *PaperBroker {
	return &PaperBroker{}
}

func (p *PaperBroker) PlaceOrder(symbol string, qty int, isShort bool, orderType string, price float64) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.orderID++

	direction := "BUY"
	if isShort {
		direction = "SELL"
	}
	oid := fmt.Sprintf("PAPER_%d_%d", time.Now().UnixNano(), p.orderID)
	log.Printf("[Paper] %s %s x%d @ %.2f → %s", direction, symbol, qty, price, oid)
	return oid, nil
}

func (p *PaperBroker) CancelOrder(orderID string) error {
	log.Printf("[Paper] Cancel order: %s", orderID)
	return nil
}
