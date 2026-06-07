package agents

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// KronosClient calls the Python Kronos microservice to rank BUY signals
// by predicted 5-day upside. Falls back gracefully when service is offline.
type KronosClient struct {
	BaseURL      string
	HTTPClient   *http.Client
	trainedSyms  map[string]bool // symbols present in kronos/data/instruments.csv
}

func NewKronosClient(baseURL string) *KronosClient {
	k := &KronosClient{
		BaseURL:     baseURL,
		// 60s was too short — CPU inference takes ~12s/stock with 5 sample
		// paths, and a capped batch of 30 can take ~6 minutes worst case.
		// 12 minutes gives headroom without hanging forever if the service wedges.
		HTTPClient:  &http.Client{Timeout: 12 * time.Minute},
		trainedSyms: loadKronosTrainedSymbols(),
	}
	if len(k.trainedSyms) > 0 {
		log.Printf("[Kronos] Loaded %d trained symbols from instruments.csv — ranking gated to these stocks", len(k.trainedSyms))
	} else {
		log.Println("[Kronos] instruments.csv not found — Kronos will rank all BUY signals (no gating)")
	}
	return k
}

// loadKronosTrainedSymbols reads kronos/data/instruments.csv and returns
// a set of symbols that Kronos was fine-tuned on. Searches the same paths
// as findKronosScript so it works from the exe directory or project root.
func loadKronosTrainedSymbols() map[string]bool {
	candidates := []string{}

	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates,
			filepath.Join(filepath.Dir(exe), "kronos", "data", "instruments.csv"),
		)
	}
	if cwd, err := os.Getwd(); err == nil {
		dir := cwd
		for i := 0; i < 4; i++ {
			candidates = append(candidates,
				filepath.Join(dir, "kronos", "data", "instruments.csv"),
			)
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	for _, p := range candidates {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		defer f.Close()

		r := csv.NewReader(f)
		rows, err := r.ReadAll()
		if err != nil || len(rows) < 2 {
			continue
		}

		syms := make(map[string]bool, len(rows)-1)
		for _, row := range rows[1:] { // skip header
			if len(row) >= 2 {
				syms[row[1]] = true // column 1 = symbol
			}
		}
		log.Printf("[Kronos] instruments.csv loaded from %s", p)
		return syms
	}
	return nil
}

// isTrainedSymbol returns true if Kronos was fine-tuned on this symbol.
// When no instruments.csv was found, returns true for all symbols (no gating).
func (k *KronosClient) isTrainedSymbol(symbol string) bool {
	if len(k.trainedSyms) == 0 {
		return true
	}
	return k.trainedSyms[symbol]
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

// kronosMaxBatchSize caps how many signals get sent to Kronos for ranking
// per scan. On CPU, each prediction (5 sample paths) takes ~12s — ranking
// the full BUY list (often 100-190 stocks) would take 20-40 minutes and blow
// past the HTTP client timeout, silently falling back to "no AI ranking"
// (which is what was happening — see the "no Kronos identity" report).
// Only the top ~15 setups are ever shown in the summary anyway, so ranking
// more than this is wasted compute. The overflow is appended after the
// ranked subset, in original (Score/RS) order — nothing is hidden.
const kronosMaxBatchSize = 30

// ── RankSignals ────────────────────────────────────────────────────────────
// Takes EOD BUY results with their DailyCache closes and returns them
// re-ranked by Kronos predicted 5-day upside. Returns original order
// unchanged if Kronos service is unavailable.

func (k *KronosClient) RankSignals(results []EODScanResult, cache *DailyCache) []EODScanResult {
	if len(results) == 0 {
		return results
	}

	// Split into trained (Kronos ranks these) and untrained (appended after, original order).
	var trained, untrained []EODScanResult
	for _, r := range results {
		if k.isTrainedSymbol(r.Symbol) {
			trained = append(trained, r)
		} else {
			untrained = append(untrained, r)
		}
	}
	if len(untrained) > 0 {
		log.Printf("[Kronos] Gating: %d trained symbols sent to Kronos, %d untrained appended after",
			len(trained), len(untrained))
	}

	// Cap the batch — incoming `trained` is already sorted by Score/RS desc
	// (callers sort BUY results before invoking RankSignals), so truncating
	// keeps the highest-quality candidates and pushes the rest after,
	// unranked but still visible.
	if len(trained) > kronosMaxBatchSize {
		overflow := trained[kronosMaxBatchSize:]
		trained = trained[:kronosMaxBatchSize]
		untrained = append(overflow, untrained...)
		log.Printf("[Kronos] Batch cap: ranking top %d of %d trained signals (CPU latency guard); %d appended after",
			kronosMaxBatchSize, kronosMaxBatchSize+len(overflow), len(overflow))
	}

	if len(trained) == 0 {
		return results // nothing to rank
	}

	var inputs []kronosSignalInput
	for _, r := range trained {
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

	// Apply upside scores to the trained subset and sort it
	for i := range trained {
		if up, ok := upsideMap[trained[i].Symbol]; ok {
			trained[i].KronosUpside = up
		}
	}
	for i := 0; i < len(trained)-1; i++ {
		for j := i + 1; j < len(trained); j++ {
			if trained[j].KronosUpside > trained[i].KronosUpside {
				trained[i], trained[j] = trained[j], trained[i]
			}
		}
	}

	if len(trained) > 0 {
		log.Printf("[Kronos] Ranked %d trained signals via %s. Top: %s (+%.1f%%). %d untrained appended after.",
			len(trained), kronResp.Model, trained[0].Symbol, trained[0].KronosUpside, len(untrained))
	}

	// Final order: Kronos-ranked trained stocks first, then untrained in original order
	return append(trained, untrained...)
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
