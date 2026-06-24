package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"bnf_go_engine/config"
	"bnf_go_engine/model"
)

// KiteBroker places real orders via the Zerodha Kite Connect REST API.
//
// ⚠️ DORMANT: this is written and compiled but NOT wired into cmd/btst. The engine
// runs the PaperBroker until the 30-day paper trial passes; only then should the
// entrypoint swap this in and PAPER_MODE be flipped to false.
//
// ⚠️ UNVERIFIED AGAINST LIVE API: the request shapes follow Kite Connect v3 docs
// but have not been exercised against a live account in this codebase. Before going
// live, smoke-test each method with a single small order and confirm:
//   - BTST stop-loss legality: a resting SELL SL-M on CNC for shares not yet in
//     demat may be rejected by Zerodha. If so, fall back to the software-tracked SL
//     (the safe pattern the paper engine already uses) instead of PlaceSLM.
//   - product type: CNC (delivery) is correct for BTST; MIS would auto-square-off intraday.
type KiteBroker struct {
	http     *http.Client
	apiKey   string
	token    string
	product  string // CNC for BTST; override via BTST_PRODUCT
	exchange string // NSE
}

const kiteBase = "https://api.kite.trade"

// NewKiteBroker builds a live broker from the configured Kite credentials.
func NewKiteBroker() *KiteBroker {
	product := os.Getenv("BTST_PRODUCT")
	if product == "" {
		product = "CNC"
	}
	return &KiteBroker{
		http:     &http.Client{Timeout: 15 * time.Second},
		apiKey:   config.KiteAPIKey,
		token:    config.KiteAccessToken,
		product:  product,
		exchange: "NSE",
	}
}

func (k *KiteBroker) authHeader() string {
	return fmt.Sprintf("token %s:%s", k.apiKey, k.token)
}

// place posts an order to /orders/regular and returns the order_id.
func (k *KiteBroker) place(ctx context.Context, form url.Values) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		kiteBase+"/orders/regular", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", k.authHeader())
	req.Header.Set("X-Kite-Version", "3")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := k.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var out struct {
		Status string `json:"status"`
		Data   struct {
			OrderID string `json:"order_id"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Status != "success" || out.Data.OrderID == "" {
		return "", fmt.Errorf("kite order rejected (HTTP %d): %s", resp.StatusCode, out.Message)
	}
	return out.Data.OrderID, nil
}

// avgFillPrice polls the order until it is COMPLETE and returns the average price.
// Market orders usually fill within a second; we poll briefly before giving up.
func (k *KiteBroker) avgFillPrice(ctx context.Context, orderID string) (float64, error) {
	for i := 0; i < 10; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			kiteBase+"/orders/"+orderID, nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", k.authHeader())
		req.Header.Set("X-Kite-Version", "3")

		resp, err := k.http.Do(req)
		if err != nil {
			return 0, err
		}
		var out struct {
			Data []struct {
				Status       string  `json:"status"`
				AveragePrice float64 `json:"average_price"`
			} `json:"data"`
		}
		err = json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if err != nil {
			return 0, err
		}
		if n := len(out.Data); n > 0 {
			last := out.Data[n-1]
			if last.Status == "COMPLETE" {
				return last.AveragePrice, nil
			}
			if last.Status == "REJECTED" || last.Status == "CANCELLED" {
				return 0, fmt.Errorf("order %s %s", orderID, last.Status)
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return 0, fmt.Errorf("order %s did not complete in time", orderID)
}

// PlaceMarketBuy places a market BUY and returns the filled average price.
func (k *KiteBroker) PlaceMarketBuy(symbol string, qty int) (string, float64, error) {
	ctx := context.Background()
	form := url.Values{
		"exchange":         {k.exchange},
		"tradingsymbol":    {symbol},
		"transaction_type": {"BUY"},
		"order_type":       {"MARKET"},
		"quantity":         {strconv.Itoa(qty)},
		"product":          {k.product},
		"validity":         {"DAY"},
	}
	orderID, err := k.place(ctx, form)
	if err != nil {
		return "", 0, err
	}
	fill, err := k.avgFillPrice(ctx, orderID)
	return orderID, fill, err
}

// PlaceSLM places a stop-loss-market SELL at the trigger price.
// See the BTST legality caveat on the type doc before relying on this live.
func (k *KiteBroker) PlaceSLM(symbol string, qty int, trigger float64) (string, error) {
	form := url.Values{
		"exchange":         {k.exchange},
		"tradingsymbol":    {symbol},
		"transaction_type": {"SELL"},
		"order_type":       {"SL-M"},
		"quantity":         {strconv.Itoa(qty)},
		"product":          {k.product},
		"validity":         {"DAY"},
		"trigger_price":    {strconv.FormatFloat(trigger, 'f', 2, 64)},
	}
	return k.place(context.Background(), form)
}

// SquareOff places a market SELL and returns the filled average price.
func (k *KiteBroker) SquareOff(symbol string, qty int) (float64, error) {
	ctx := context.Background()
	form := url.Values{
		"exchange":         {k.exchange},
		"tradingsymbol":    {symbol},
		"transaction_type": {"SELL"},
		"order_type":       {"MARKET"},
		"quantity":         {strconv.Itoa(qty)},
		"product":          {k.product},
		"validity":         {"DAY"},
	}
	orderID, err := k.place(ctx, form)
	if err != nil {
		return 0, err
	}
	return k.avgFillPrice(ctx, orderID)
}

// OpenPositions returns net positions held at the broker.
func (k *KiteBroker) OpenPositions() ([]model.Position, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		kiteBase+"/portfolio/positions", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", k.authHeader())
	req.Header.Set("X-Kite-Version", "3")

	resp, err := k.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out struct {
		Data struct {
			Net []struct {
				TradingSymbol string  `json:"tradingsymbol"`
				Quantity      int     `json:"quantity"`
				AveragePrice  float64 `json:"average_price"`
			} `json:"net"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	var positions []model.Position
	for _, p := range out.Data.Net {
		if p.Quantity == 0 {
			continue
		}
		positions = append(positions, model.Position{
			Symbol: p.TradingSymbol, Qty: p.Quantity, EntryPrice: p.AveragePrice,
			Status: model.StatusOpen, Paper: false,
		})
	}
	return positions, nil
}

// IsPaper always returns false.
func (k *KiteBroker) IsPaper() bool { return false }
