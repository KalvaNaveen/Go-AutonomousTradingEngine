package agents

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bnf_go_engine/config"
)

// DataAgent manages the universe of tradeable symbols.
// Port of Python agents/data_agent.py
type DataAgent struct {
	Universe map[uint32]string // token -> symbol
	apiKey      string
	accessToken string
}

func NewDataAgent() *DataAgent {
	return &DataAgent{
		Universe:    make(map[uint32]string),
		apiKey:      config.KiteAPIKey,
		accessToken: config.KiteAccessToken,
	}
}

// LoadUniverse fetches NSE instruments from Kite and filters to Nifty 250
func (d *DataAgent) LoadUniverse() error {
	log.Println("[DataAgent] Loading universe...")

	// Step 1: Sync Nifty 250 CSV from NSE
	d.syncNSEUniverse()

	// Step 2: Load target symbols from CSV
	targets := d.loadNifty250CSV()
	if len(targets) == 0 {
		// Fallback to hardcoded symbols
		targets = map[string]bool{
			"RELIANCE": true, "TCS": true, "HDFCBANK": true, "INFY": true,
			"HINDUNILVR": true, "ICICIBANK": true, "KOTAKBANK": true,
			"SBIN": true, "BHARTIARTL": true, "ITC": true, "AXISBANK": true,
			"LT": true, "WIPRO": true, "HCLTECH": true, "ASIANPAINT": true,
			"BAJFINANCE": true, "MARUTI": true, "SUNPHARMA": true,
			"TITAN": true, "NTPC": true, "TATAMOTORS": true,
		}
		log.Printf("[DataAgent] Using %d fallback symbols", len(targets))
	}

	// Step 3: Fetch instruments from Kite
	instruments, err := d.fetchInstruments()
	if err != nil {
		return fmt.Errorf("fetch instruments failed: %v", err)
	}

	for _, inst := range instruments {
		symbol, _ := inst["tradingsymbol"].(string)
		instType, _ := inst["instrument_type"].(string)
		segment, _ := inst["segment"].(string)

		if instType != "EQ" || segment != "NSE" {
			continue
		}
		if !targets[symbol] {
			continue
		}

		var token uint32
		switch v := inst["instrument_token"].(type) {
		case float64:
			token = uint32(v)
		case json.Number:
			n, _ := v.Int64()
			token = uint32(n)
		}

		if token > 0 {
			d.Universe[token] = symbol
		}
	}

	log.Printf("[DataAgent] Universe loaded: %d/%d symbols", len(d.Universe), len(targets))
	return nil
}

func (d *DataAgent) syncNSEUniverse() {
	csvPath := filepath.Join(config.BaseDir, "data", "nifty250.csv")
	url := "https://archives.nseindia.com/content/indices/ind_niftylargemidcap250list.csv"

	client := &http.Client{Timeout: 15 * time.Second}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "text/csv")

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[DataAgent] NSE sync failed: %v, using local fallback", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if len(body) > 500 {
		os.MkdirAll(filepath.Dir(csvPath), 0755)
		os.WriteFile(csvPath, body, 0644)
		log.Printf("[DataAgent] Nifty 250 synced from NSE (%d bytes)", len(body))
	}
}

func (d *DataAgent) loadNifty250CSV() map[string]bool {
	csvPath := filepath.Join(config.BaseDir, "data", "nifty250.csv")
	f, err := os.Open(csvPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	reader := csv.NewReader(f)
	header, err := reader.Read()
	if err != nil {
		return nil
	}

	symbolIdx := -1
	for i, h := range header {
		if strings.TrimSpace(h) == "Symbol" {
			symbolIdx = i
			break
		}
	}
	if symbolIdx < 0 {
		return nil
	}

	targets := make(map[string]bool)
	for {
		record, err := reader.Read()
		if err != nil {
			break
		}
		if symbolIdx < len(record) {
			sym := strings.TrimSpace(record[symbolIdx])
			if sym != "" {
				targets[sym] = true
			}
		}
	}
	log.Printf("[DataAgent] Loaded %d symbols from nifty250.csv", len(targets))
	return targets
}

func (d *DataAgent) fetchInstruments() ([]map[string]interface{}, error) {
	url := "https://api.kite.trade/instruments/NSE"
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Kite-Version", "3")
	req.Header.Set("Authorization", fmt.Sprintf("token %s:%s", d.apiKey, d.accessToken))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	// Kite returns CSV for instruments, parse it
	reader := csv.NewReader(strings.NewReader(string(body)))
	headers, err := reader.Read()
	if err != nil {
		return nil, err
	}

	var results []map[string]interface{}
	for {
		record, err := reader.Read()
		if err != nil {
			break
		}
		row := make(map[string]interface{})
		for i, h := range headers {
			if i < len(record) {
				row[h] = record[i]
			}
		}
		// Parse instrument_token as float for compatibility
		if tokenStr, ok := row["instrument_token"].(string); ok {
			var tokenVal float64
			fmt.Sscanf(tokenStr, "%f", &tokenVal)
			row["instrument_token"] = tokenVal
		}
		results = append(results, row)
	}
	return results, nil
}

// GetAllTokens returns all universe tokens plus index tokens
func (d *DataAgent) GetAllTokens() []uint32 {
	var tokens []uint32
	for t := range d.Universe {
		tokens = append(tokens, t)
	}
	// Add index tokens
	tokens = append(tokens, config.Nifty50Token, config.IndiaVIXToken)
	for _, t := range config.SectorTokens {
		tokens = append(tokens, t)
	}
	return tokens
}
