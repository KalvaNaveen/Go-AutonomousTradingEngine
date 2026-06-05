package agents

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// KronosClient calls the Python Kronos microservice to rank BUY signals
// by predicted 5-day upside. Falls back gracefully when service is offline.
type KronosClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

func NewKronosClient(baseURL string) *KronosClient {
	return &KronosClient{
		BaseURL: baseURL,
		HTTPClient: &http.Client{Timeout: 60 * time.Second},
	}
}

// ── Request / Response types ───────────────────────────────────────────────

type kronosOHLCVBar struct {
	Date   string  `json:"date"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
}

type kronosSignalInput struct {
	Symbol string           `json:"symbol"`
	OHLCV  []kronosOHLCVBar `json:"ohlcv"`
}

type kronosPredictRequest struct {
	Signals []kronosSignalInput `json:"signals"`
}

type KronosRankedSignal struct {
	Symbol            string  `json:"symbol"`
	PredictedClose5d  float64 `json:"predicted_close_5d"`
	CurrentClose      float64 `json:"current_close"`
	UpsidePct         float64 `json:"upside_pct"`
}

type kronosPredictResponse struct {
	Ranked []KronosRankedSignal `json:"ranked"`
	Model  string               `json:"model"`
}

// ── Health check ───────────────────────────────────────────────────────────

func (k *KronosClient) IsAlive() bool {
	resp, err := k.HTTPClient.Get(k.BaseURL + "/health")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// ── RankSignals ────────────────────────────────────────────────────────────
// Takes EOD BUY results with their DailyCache closes and returns them
// re-ranked by Kronos predicted 5-day upside. Returns original order
// unchanged if Kronos service is unavailable.

func (k *KronosClient) RankSignals(results []EODScanResult, cache *DailyCache) []EODScanResult {
	if len(results) == 0 {
		return results
	}

	var inputs []kronosSignalInput
	for _, r := range results {
		closes, ok := cache.Closes[r.Token]
		if !ok || len(closes) < 10 {
			continue
		}
		dates := cache.TradingDates

		bars := make([]kronosOHLCVBar, 0, len(closes))
		highs  := cache.Highs[r.Token]
		lows   := cache.Lows[r.Token]
		vols   := cache.Volumes[r.Token]

		for i, c := range closes {
			dateStr := ""
			if i < len(dates) {
				dateStr = dates[i]
			}
			bar := kronosOHLCVBar{
				Date:  dateStr,
				Close: c,
			}
			if i < len(highs)  { bar.High   = highs[i]  }
			if i < len(lows)   { bar.Low    = lows[i]   }
			if i < len(vols)   { bar.Volume = vols[i]   }
			// Open approximation: use previous close if not stored separately
			if i > 0 {
				bar.Open = closes[i-1]
			} else {
				bar.Open = c
			}
			bars = append(bars, bar)
		}
		inputs = append(inputs, kronosSignalInput{Symbol: r.Symbol, OHLCV: bars})
	}

	if len(inputs) == 0 {
		return results
	}

	reqBody, _ := json.Marshal(kronosPredictRequest{Signals: inputs})
	resp, err := k.HTTPClient.Post(k.BaseURL+"/predict_batch", "application/json", bytes.NewReader(reqBody))
	if err != nil {
		log.Printf("[Kronos] Service unreachable: %v — using original order", err)
		return results
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var kronResp kronosPredictResponse
	if err := json.Unmarshal(body, &kronResp); err != nil {
		log.Printf("[Kronos] Response parse error: %v", err)
		return results
	}

	// Build upside map
	upsideMap := make(map[string]float64, len(kronResp.Ranked))
	for _, r := range kronResp.Ranked {
		upsideMap[r.Symbol] = r.UpsidePct
	}

	// Apply upside to results and sort
	for i := range results {
		if up, ok := upsideMap[results[i].Symbol]; ok {
			results[i].KronosUpside = up
		}
	}

	// Sort: upside desc (Kronos ranked), ties broken by existing Score→RS
	sorted := make([]EODScanResult, len(results))
	copy(sorted, results)
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].KronosUpside > sorted[i].KronosUpside {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	log.Printf("[Kronos] Ranked %d signals via %s. Top: %s (+%.1f%%)",
		len(sorted), kronResp.Model, sorted[0].Symbol, sorted[0].KronosUpside)
	return sorted
}

// UpsideTag formats the Kronos upside for Telegram output.
func KronosUpsideTag(upside float64) string {
	if upside == 0 {
		return ""
	}
	switch {
	case upside >= 8:
		return fmt.Sprintf(" | 🤖 `+%.1f%%`🔥", upside)
	case upside >= 4:
		return fmt.Sprintf(" | 🤖 `+%.1f%%`", upside)
	case upside >= 0:
		return fmt.Sprintf(" | 🤖 `+%.1f%%`", upside)
	default:
		return fmt.Sprintf(" | 🤖 `%.1f%%`⚠️", upside)
	}
}
