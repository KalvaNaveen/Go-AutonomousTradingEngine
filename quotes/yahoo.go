// Package quotes fetches free daily OHLC data from Yahoo Finance (no auth),
// the same source the original C# scraper used. It powers paper-mode exits:
// the engine needs each stock's next-day low to decide whether the software
// stop-loss was breached, and its current price for the 3:20 PM square-off.
package quotes

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// OHLC is one day's price summary plus the latest traded price.
type OHLC struct {
	Open      float64
	High      float64
	Low       float64
	Last      float64 // regularMarketPrice — used as the 3:20 square-off price
	PrevClose float64
}

const yahooBase = "https://query1.finance.yahoo.com/v8/finance/chart/"

// Client fetches Yahoo quotes.
type Client struct {
	http *http.Client
}

// New returns a quotes client.
func New() *Client {
	return &Client{http: &http.Client{Timeout: 15 * time.Second}}
}

// Daily returns today's OHLC for an NSE symbol (e.g. "NHPC" → NHPC.NS).
func (c *Client) Daily(ctx context.Context, nseSymbol string) (OHLC, error) {
	return c.fetch(ctx, nseSymbol+".NS")
}

// Index returns today's OHLC for a Yahoo index symbol as-is (e.g. "^NSEI",
// "^INDIAVIX") with no .NS suffix.
func (c *Client) Index(ctx context.Context, symbol string) (OHLC, error) {
	return c.fetch(ctx, symbol)
}

func (c *Client) fetch(ctx context.Context, fullSymbol string) (OHLC, error) {
	url := fmt.Sprintf("%s%s?interval=1d&range=1d", yahooBase, fullSymbol)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return OHLC{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := c.http.Do(req)
	if err != nil {
		return OHLC{}, fmt.Errorf("yahoo %s: %w", fullSymbol, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return OHLC{}, fmt.Errorf("yahoo %s: HTTP %d", fullSymbol, resp.StatusCode)
	}

	var raw struct {
		Chart struct {
			Result []struct {
				Meta struct {
					RegularMarketPrice float64 `json:"regularMarketPrice"`
					PreviousClose      float64 `json:"chartPreviousClose"`
					DayHigh            float64 `json:"regularMarketDayHigh"`
					DayLow             float64 `json:"regularMarketDayLow"`
					RegularMarketOpen  float64 `json:"regularMarketOpen"`
				} `json:"meta"`
			} `json:"result"`
			Error any `json:"error"`
		} `json:"chart"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return OHLC{}, fmt.Errorf("yahoo %s decode: %w", fullSymbol, err)
	}
	if len(raw.Chart.Result) == 0 {
		return OHLC{}, fmt.Errorf("yahoo %s: no data", fullSymbol)
	}
	m := raw.Chart.Result[0].Meta
	return OHLC{
		Open:      m.RegularMarketOpen,
		High:      m.DayHigh,
		Low:       m.DayLow,
		Last:      m.RegularMarketPrice,
		PrevClose: m.PreviousClose,
	}, nil
}
