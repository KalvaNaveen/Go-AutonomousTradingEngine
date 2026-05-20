package broker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"bnf_go_engine/config"
)

// ══════════════════════════════════════════════════════════════
//  Section VII.1: GTT & OCO Broker Automation
// ══════════════════════════════════════════════════════════════
//  Doc: "GTT — Set orders in advance so entries trigger automatically
//  when breakout prices are hit."
//  Doc: "OCO — Automate exits by setting Target and Stop-Loss
//  simultaneously. If Target hits, SL cancels (and vice versa)."

type GTTClient struct {
	APIKey      string
	AccessToken string
	BaseURL     string
	HTTPClient  *http.Client
}

func NewGTTClient() *GTTClient {
	return &GTTClient{
		APIKey:      config.KiteAPIKey,
		AccessToken: config.KiteAccessToken,
		BaseURL:     "https://api.kite.trade",
		HTTPClient:  &http.Client{Timeout: 10 * time.Second},
	}
}

// GTT trigger types
const (
	GTTTypeSingle = "single"   // Single price trigger (for entry)
	GTTTypeTwoLeg = "two-leg"  // OCO: target + SL simultaneously
)

type GTTOrder struct {
	Exchange        string  `json:"exchange"`
	TradingSymbol   string  `json:"tradingsymbol"`
	TransactionType string  `json:"transaction_type"` // BUY or SELL
	Quantity        int     `json:"quantity"`
	Price           float64 `json:"price"`
	OrderType       string  `json:"order_type"` // LIMIT or MARKET
	Product         string  `json:"product"`     // CNC
}

type gttPayload struct {
	TriggerType   string     `json:"trigger_type"`
	TradingSymbol string     `json:"tradingsymbol"`
	Exchange      string     `json:"exchange"`
	TriggerValues []float64  `json:"trigger_values"`
	LastPrice     float64    `json:"last_price"`
	Orders        []GTTOrder `json:"orders"`
}

type gttResponse struct {
	Status string `json:"status"`
	Data   struct {
		TriggerID int `json:"trigger_id"`
	} `json:"data"`
	Message string `json:"message"`
}

// PlaceGTTEntry places a single-leg GTT for auto-entry at breakout price.
// Doc: "Set these orders in advance so entries trigger automatically
// when breakout prices are hit, preventing FOMO or missed chances."
func (g *GTTClient) PlaceGTTEntry(symbol string, triggerPrice, limitPrice float64, qty int, lastPrice float64) (int, error) {
	payload := gttPayload{
		TriggerType:   GTTTypeSingle,
		TradingSymbol: symbol,
		Exchange:      "NSE",
		TriggerValues: []float64{triggerPrice},
		LastPrice:     lastPrice,
		Orders: []GTTOrder{
			{
				Exchange:        "NSE",
				TradingSymbol:   symbol,
				TransactionType: "BUY",
				Quantity:        qty,
				Price:           limitPrice,
				OrderType:       "LIMIT",
				Product:         "CNC",
			},
		},
	}

	triggerID, err := g.postGTT(payload)
	if err != nil {
		return 0, err
	}

	log.Printf("[GTT] Entry placed: %s trigger=%.2f limit=%.2f qty=%d → ID=%d",
		symbol, triggerPrice, limitPrice, qty, triggerID)
	return triggerID, nil
}

// PlaceOCO places a two-leg GTT (One Cancels Other) for auto-exit.
// Doc: "Automate exits by setting Target and Stop-Loss simultaneously.
// If the Target hits, the Stop-Loss cancels (and vice versa)."
func (g *GTTClient) PlaceOCO(symbol string, qty int, targetPrice, slPrice, lastPrice float64) (int, error) {
	payload := gttPayload{
		TriggerType:   GTTTypeTwoLeg,
		TradingSymbol: symbol,
		Exchange:      "NSE",
		TriggerValues: []float64{slPrice, targetPrice},
		LastPrice:     lastPrice,
		Orders: []GTTOrder{
			// Leg 1: Stop-Loss (triggers when price falls to slPrice)
			{
				Exchange:        "NSE",
				TradingSymbol:   symbol,
				TransactionType: "SELL",
				Quantity:        qty,
				Price:           slPrice,
				OrderType:       "LIMIT",
				Product:         "CNC",
			},
			// Leg 2: Target (triggers when price rises to targetPrice)
			{
				Exchange:        "NSE",
				TradingSymbol:   symbol,
				TransactionType: "SELL",
				Quantity:        qty,
				Price:           targetPrice,
				OrderType:       "LIMIT",
				Product:         "CNC",
			},
		},
	}

	triggerID, err := g.postGTT(payload)
	if err != nil {
		return 0, err
	}

	log.Printf("[OCO] Exit placed: %s SL=%.2f Target=%.2f qty=%d → ID=%d",
		symbol, slPrice, targetPrice, qty, triggerID)
	return triggerID, nil
}

// PlaceSLGTT places a single-leg SELL GTT for SL exit of a long CNC position.
// Matches the FillMonitor.PlaceGTT function signature.
// The limit price is set 1% below the trigger so a gap-down still fills.
func (g *GTTClient) PlaceSLGTT(symbol, exchange string, token uint32, lastPrice float64, qty int, slPrice float64) (int, error) {
	limitPrice := slPrice * 0.99 // 1% below trigger — guarantees fill on gap downs
	payload := gttPayload{
		TriggerType:   GTTTypeSingle,
		TradingSymbol: symbol,
		Exchange:      exchange,
		TriggerValues: []float64{slPrice},
		LastPrice:     lastPrice,
		Orders: []GTTOrder{
			{
				Exchange:        exchange,
				TradingSymbol:   symbol,
				TransactionType: "SELL",
				Quantity:        qty,
				Price:           limitPrice,
				OrderType:       "LIMIT",
				Product:         "CNC",
			},
		},
	}

	triggerID, err := g.postGTT(payload)
	if err != nil {
		return 0, err
	}
	log.Printf("[GTT] SL placed: %s trigger=%.2f limit=%.2f qty=%d → ID=%d",
		symbol, slPrice, limitPrice, qty, triggerID)
	return triggerID, nil
}

// CancelGTT cancels a GTT trigger by ID
func (g *GTTClient) CancelGTT(triggerID int) error {
	url := fmt.Sprintf("%s/gtt/triggers/%d", g.BaseURL, triggerID)

	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return err
	}
	g.setHeaders(req)

	resp, err := g.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("GTT cancel failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GTT cancel returned %d: %s", resp.StatusCode, string(body))
	}

	log.Printf("[GTT] Cancelled trigger ID=%d", triggerID)
	return nil
}

func (g *GTTClient) postGTT(payload gttPayload) (int, error) {
	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}

	url := fmt.Sprintf("%s/gtt/triggers", g.BaseURL)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return 0, err
	}
	g.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.HTTPClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("GTT POST failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("GTT API returned %d: %s", resp.StatusCode, string(body))
	}

	var gttResp gttResponse
	if err := json.Unmarshal(body, &gttResp); err != nil {
		return 0, fmt.Errorf("GTT response parse error: %w", err)
	}

	if gttResp.Status != "success" {
		return 0, fmt.Errorf("GTT failed: %s", gttResp.Message)
	}

	return gttResp.Data.TriggerID, nil
}

func (g *GTTClient) setHeaders(req *http.Request) {
	req.Header.Set("X-Kite-Version", "3")
	req.Header.Set("Authorization", fmt.Sprintf("token %s:%s", g.APIKey, g.AccessToken))
}
