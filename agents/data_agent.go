package agents

import (
	"database/sql"
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

	_ "modernc.org/sqlite"
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

// LoadUniverse fetches NSE instruments from Kite and filters to Nifty 250
func (d *DataAgent) LoadUniverse() error {
	log.Println("[DataAgent] Loading universe...")

	// Step 1: Sync Nifty 250 CSV from NSE
	d.syncNSEUniverse()

	// Step 2: Load target symbols from CSV
	targets := d.loadNifty250CSV()
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

// ── Historical EOD Synchronization ────────────────────────────────

type KiteCandle struct {
	Timestamp string
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    int64
}

// fetchKiteHistorical fetches daily or minute bars from the Kite API
func (d *DataAgent) fetchKiteHistorical(token uint32, interval, from, to string) ([]KiteCandle, error) {
	url := fmt.Sprintf("https://api.kite.trade/instruments/historical/%d/%s?from=%s+09:15:00&to=%s+15:30:00", token, interval, from, to)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("X-Kite-Version", "3")
	req.Header.Set("Authorization", fmt.Sprintf("token %s:%s", d.apiKey, d.accessToken))

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Kite API returned HTTP %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)

	var r struct {
		Status string `json:"status"`
		Data   struct {
			Candles [][]interface{} `json:"candles"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if r.Status != "success" {
		return nil, fmt.Errorf("Kite API error payload")
	}

	var res []KiteCandle
	for _, raw := range r.Data.Candles {
		if len(raw) >= 6 {
			ts, _ := raw[0].(string)
			o, _ := raw[1].(float64)
			h, _ := raw[2].(float64)
			l, _ := raw[3].(float64)
			c, _ := raw[4].(float64)
			v, _ := raw[5].(float64)
			res = append(res, KiteCandle{Timestamp: ts, Open: o, High: h, Low: l, Close: c, Volume: int64(v)})
		}
	}
	return res, nil
}

// SyncHistoricalEODData fetches daily and minute bars for all tracked tokens for the last 3 days
// and inserts/appends them into historical.db. 
func (d *DataAgent) SyncHistoricalEODData(dbPath string) error {
	return d.SyncHistoricalCustom(dbPath, 3)
}

// SyncHistoricalCustom allows fetching a custom number of lookback days to backfill historical missing data
func (d *DataAgent) SyncHistoricalCustom(dbPath string, lookbackDays int) error {
	log.Printf("[DataAgent] ═══ STARTING EOD DATA SYNC (%d days) ═══", lookbackDays)
	SendTelegram(fmt.Sprintf("⏳ *EOD SYNC STARTED*: Downloading intraday data (%d days) to `historical.db`.", lookbackDays))

	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=30000")
	if err != nil {
		return err
	}
	defer db.Close()

	now := config.NowIST()
	fromDate := now.AddDate(0, 0, -lookbackDays).Format("2006-01-02")
	toDate := now.Format("2006-01-02")

	tokens := d.GetAllTokens()
	log.Printf("[DataAgent] Syncing historical data for %d tokens (%s to %s)", len(tokens), fromDate, toDate)

	countDay := 0
	countMin := 0

	for i, token := range tokens {
		// 1. Fetch Day Candles
		dayCandles, err := d.fetchKiteHistorical(token, "day", fromDate, toDate)
		if err == nil {
			for _, c := range dayCandles {
				// Convert 2026-04-18T00:00:00+0530 -> "2026-04-18"
				dateStr := ""
				if len(c.Timestamp) >= 10 {
					dateStr = c.Timestamp[:10]
				}
				if dateStr != "" {
					_, _ = db.Exec(`INSERT OR REPLACE INTO daily_data (token, date, open, high, low, close, volume) VALUES (?, ?, ?, ?, ?, ?, ?)`,
						token, dateStr, c.Open, c.High, c.Low, c.Close, c.Volume)
					countDay++
				}
			}
		}

		// 2. Fetch Minute Candles
		minCandles, err := d.fetchKiteHistorical(token, "minute", fromDate, toDate)
		if err == nil {
			for _, c := range minCandles {
				// Convert 2026-04-17T15:20:00+0530 -> "2026-04-17 15:20:00"
				ts := ""
				if len(c.Timestamp) >= 19 {
					ts = strings.Replace(c.Timestamp[:19], "T", " ", 1)
				}
				if ts != "" {
					// Use INSERT OR IGNORE rather than REPLACE because volume might have been accumulated differently live vs historical API
					_, _ = db.Exec(`INSERT OR IGNORE INTO minute_data (token, date_time, open, high, low, close, volume) VALUES (?, ?, ?, ?, ?, ?, ?)`,
						token, ts, c.Open, c.High, c.Low, c.Close, c.Volume)
					countMin++
				}
			}
		}

		// Kite Rate Limit: 3 requests per second. We do 2 per token.
		// Sleep 800ms to stay extremely safe under the 3 req/sec limit.
		time.Sleep(800 * time.Millisecond)

		if (i+1)%50 == 0 {
			log.Printf("[DataAgent] Synced %d/%d tokens...", i+1, len(tokens))
		}
	}

	log.Printf("[DataAgent] ═══ EOD SYNC COMPLETE: %d Daily, %d Minute candles ═══", countDay, countMin)
	SendTelegram(fmt.Sprintf("✅ *EOD SYNC COMPLETE*: Inserted %d Min bounds and %d Daily bounds.", countMin, countDay))
	return nil
}
