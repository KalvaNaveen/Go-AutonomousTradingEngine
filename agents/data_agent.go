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
	Universe       map[uint32]string // token -> symbol
	TokenToCompany map[uint32]string // token -> Company Name
	apiKey         string
	accessToken    string
}

func NewDataAgent() *DataAgent {
	return &DataAgent{
		Universe:       make(map[uint32]string),
		TokenToCompany: make(map[uint32]string),
		apiKey:         config.KiteAPIKey,
		accessToken:    config.KiteAccessToken,
	}
}

// LoadUniverse fetches NSE instruments from Kite and filters to Nifty Total Market 750.
func (d *DataAgent) LoadUniverse() error {
	log.Println("[DataAgent] Loading universe (Nifty Total Market 750)...")

	// Step 1: Sync Nifty Total Market CSV from NSE
	d.syncNifty750Universe()

	// Step 2: Load target symbols from CSV
	targets := d.loadNifty750CSV()
	if len(targets) == 0 {
		// Fallback to hardcoded symbols with dummy company names
		targets = map[string]string{
			"RELIANCE": "Reliance Industries Limited", "TCS": "Tata Consultancy Services Limited", "HDFCBANK": "HDFC Bank Limited", "INFY": "Infosys Limited",
			"HINDUNILVR": "Hindustan Unilever Limited", "ICICIBANK": "ICICI Bank Limited", "KOTAKBANK": "Kotak Mahindra Bank Limited",
			"SBIN": "State Bank of India", "BHARTIARTL": "Bharti Airtel Limited", "ITC": "ITC Limited", "AXISBANK": "Axis Bank Limited",
			"LT": "Larsen & Toubro Limited", "WIPRO": "Wipro Limited", "HCLTECH": "HCL Technologies Limited", "ASIANPAINT": "Asian Paints Limited",
			"BAJFINANCE": "Bajaj Finance Limited", "MARUTI": "Maruti Suzuki India Limited", "SUNPHARMA": "Sun Pharmaceutical Industries Limited",
			"TITAN": "Titan Company Limited", "NTPC": "NTPC Limited", "TATAMOTORS": "Tata Motors Limited",
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
		
		companyName, exists := targets[symbol]
		if !exists {
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
			d.TokenToCompany[token] = companyName
		}
	}

	log.Printf("[DataAgent] Universe loaded: %d/%d symbols", len(d.Universe), len(targets))
	return nil
}

// ── EOD Scan Universe (Nifty Total Market ~750) ──────────────────────

// LoadEODScanUniverse loads the broader Nifty Total Market (~750 stocks)
// for the end-of-day market scan. Returns token→symbol and token→company maps.
// Falls back to the existing Nifty 250 universe if the 750 download fails.
func (d *DataAgent) LoadEODScanUniverse() (map[uint32]string, map[uint32]string) {
	log.Println("[DataAgent] Loading Nifty 750 (Total Market) universe for EOD scan...")

	d.syncNifty750Universe()
	targets := d.loadNifty750CSV()

	if len(targets) < 100 {
		log.Printf("[DataAgent] Nifty 750 CSV only has %d stocks — falling back to Nifty 250", len(targets))
		targets = d.loadNifty250CSV()
	}
	if len(targets) == 0 {
		log.Println("[DataAgent] No EOD scan universe available — using live trading universe")
		tokenToCompany := make(map[uint32]string)
		for tok, sym := range d.Universe {
			tokenToCompany[tok] = sym
			if company, ok := d.TokenToCompany[tok]; ok {
				tokenToCompany[tok] = company
			}
		}
		return d.Universe, tokenToCompany
	}

	// Resolve symbols to instrument tokens via Kite
	instruments, err := d.fetchInstruments()
	if err != nil {
		log.Printf("[DataAgent] EOD scan instrument fetch failed: %v — using live universe", err)
		tokenToCompany := make(map[uint32]string)
		for tok := range d.Universe {
			if company, ok := d.TokenToCompany[tok]; ok {
				tokenToCompany[tok] = company
			}
		}
		return d.Universe, tokenToCompany
	}

	universe := make(map[uint32]string)
	tokenToCompany := make(map[uint32]string)

	for _, inst := range instruments {
		symbol, _ := inst["tradingsymbol"].(string)
		instType, _ := inst["instrument_type"].(string)
		segment, _ := inst["segment"].(string)

		if instType != "EQ" || segment != "NSE" {
			continue
		}

		companyName, exists := targets[symbol]
		if !exists {
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
			universe[token] = symbol
			tokenToCompany[token] = companyName
		}
	}

	log.Printf("[DataAgent] EOD scan universe loaded: %d/%d symbols", len(universe), len(targets))
	return universe, tokenToCompany
}

func (d *DataAgent) syncNifty750Universe() {
	csvPath := filepath.Join(config.BaseDir, "data", "nifty750.csv")
	url := config.NiftyTotalMarketCSVURL

	client := &http.Client{Timeout: 15 * time.Second}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "text/csv")

	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[DataAgent] Nifty 750 sync failed: %v, will try fallback", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if len(body) > 500 {
		os.MkdirAll(filepath.Dir(csvPath), 0755)
		os.WriteFile(csvPath, body, 0644)
		log.Printf("[DataAgent] Nifty 750 (Total Market) synced from NSE (%d bytes)", len(body))
	}
}

func (d *DataAgent) loadNifty750CSV() map[string]string {
	csvPath := filepath.Join(config.BaseDir, "data", "nifty750.csv")
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
	companyIdx := -1
	for i, h := range header {
		if strings.TrimSpace(h) == "Symbol" {
			symbolIdx = i
		} else if strings.TrimSpace(h) == "Company Name" {
			companyIdx = i
		}
	}
	if symbolIdx < 0 {
		return nil
	}

	targets := make(map[string]string)
	for {
		record, err := reader.Read()
		if err != nil {
			break
		}
		if symbolIdx < len(record) {
			sym := strings.TrimSpace(record[symbolIdx])
			company := sym
			if companyIdx >= 0 && companyIdx < len(record) {
				company = strings.TrimSpace(record[companyIdx])
			}
			if sym != "" {
				targets[sym] = company
			}
		}
	}
	log.Printf("[DataAgent] Loaded %d symbols from nifty750.csv", len(targets))
	return targets
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

func (d *DataAgent) loadNifty250CSV() map[string]string {
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
	companyIdx := -1
	for i, h := range header {
		if strings.TrimSpace(h) == "Symbol" {
			symbolIdx = i
		} else if strings.TrimSpace(h) == "Company Name" {
			companyIdx = i
		}
	}
	if symbolIdx < 0 {
		return nil
	}

	targets := make(map[string]string)
	for {
		record, err := reader.Read()
		if err != nil {
			break
		}
		if symbolIdx < len(record) {
			sym := strings.TrimSpace(record[symbolIdx])
			company := sym // default to symbol code
			if companyIdx >= 0 && companyIdx < len(record) {
				company = strings.TrimSpace(record[companyIdx])
			}
			if sym != "" {
				targets[sym] = company
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

// FetchKiteQuotes fetches live LTP for all tokens from the Kite Quote API.
// Batches 500 tokens per request (Kite limit). Returns token → LTP.
// When passing integer tokens, Kite keys the response by token string (e.g. "738561").
func (d *DataAgent) FetchKiteQuotes(tokens []uint32) map[uint32]float64 {
	result := make(map[uint32]float64, len(tokens))
	const batchSize = 500

	for i := 0; i < len(tokens); i += batchSize {
		end := i + batchSize
		if end > len(tokens) {
			end = len(tokens)
		}
		batch := tokens[i:end]

		// Build query string: ?i=TOKEN1&i=TOKEN2...
		queryStr := ""
		for _, tok := range batch {
			queryStr += fmt.Sprintf("&i=%d", tok)
		}
		queryStr = "?" + queryStr[1:]

		req, err := http.NewRequest("GET", "https://api.kite.trade/quote"+queryStr, nil)
		if err != nil {
			continue
		}
		req.Header.Set("X-Kite-Version", "3")
		req.Header.Set("Authorization", fmt.Sprintf("token %s:%s", d.apiKey, d.accessToken))

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("[DataAgent] Quote batch %d failed: %v", i/batchSize+1, err)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		// Kite returns data keyed by token string when queried by integer token
		var payload struct {
			Status string                     `json:"status"`
			Data   map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(body, &payload); err != nil || payload.Status != "success" {
			log.Printf("[DataAgent] Quote parse failed (status=%s): %v", payload.Status, err)
			continue
		}

		for keyStr, raw := range payload.Data {
			var q struct {
				LastPrice float64 `json:"last_price"`
			}
			if err := json.Unmarshal(raw, &q); err != nil || q.LastPrice <= 0 {
				continue
			}
			var tok uint32
			fmt.Sscanf(keyStr, "%d", &tok)
			if tok > 0 {
				result[tok] = q.LastPrice
			}
		}
	}

	log.Printf("[DataAgent] FetchKiteQuotes: %d/%d tokens quoted", len(result), len(tokens))
	return result
}

// GetAllTokens returns all universe tokens plus benchmark index tokens for WebSocket subscription.
func (d *DataAgent) GetAllTokens() []uint32 {
	var tokens []uint32
	for t := range d.Universe {
		tokens = append(tokens, t)
	}
	tokens = append(tokens,
		config.IndiaVIXToken,
		config.NiftySpotToken,    // ROC regime detection
		config.SmallcapToken,     // Smallcap ROC regime detection
		config.BankNiftySpotToken,
	)
	for _, t := range config.SectorTokens {
		tokens = append(tokens, t)
	}
	return tokens
}

