package agents

import (
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

type NewsItem struct {
	Link      string `json:"link"`
	Image     string `json:"image"`
	Sentiment string `json:"sentiment"`
	Symbol    string `json:"symbol"`
	Title     string `json:"title"`
	Source    string `json:"source"`
	Time      string `json:"time"`
}

// MacroAgent — Real-time News Intelligence Layer.
// Port of Python agents/macro_agent.py
type MacroAgent struct {
	mu             sync.Mutex
	universe       map[uint32]string
	signalQueue    []*Signal
	sentimentCache map[string]*sentimentEntry
	seenURLs       map[string]bool
	running        bool
	feedETags      map[string]string
	recentNews     []NewsItem

	// Data interfaces
	GetLTPIfFresh func(uint32) float64
	GetDayOpen    func(uint32) float64
	ComputeRVol   func(uint32) float64
	GetATR        func(uint32) float64
}

type sentimentEntry struct {
	Bias      string // "BULLISH" or "BEARISH"
	Timestamp time.Time
}

// Sentiment keyword dictionaries
var strongBullish = []string{
	"beats estimate", "profit surges", "profit jumps", "revenue surges",
	"record profit", "record revenue", "strong earnings", "beats expectations",
	"margin expansion", "ebitda growth", "pat jumps", "pat surges",
	"net profit rises", "net profit jumps", "bags order", "bagged order",
	"wins contract", "wins order", "new order", "order inflow",
	"deal signed", "partnership with", "acquisition", "acquires",
	"upgrade", "target raised", "initiates buy", "outperform",
	"strong buy", "top pick", "approval received", "fda approval",
	"all-time high", "52-week high", "breakout", "rally",
	"surges", "soars", "spikes", "zooms",
	"rate cut", "repo rate cut", "rbi cuts", "stimulus",
	"reform", "rupee strengthens", "inflation eases", "gdp beats",
}

var strongBearish = []string{
	"profit falls", "profit drops", "profit declines", "revenue falls",
	"revenue misses", "misses estimate", "weak earnings", "disappointing results",
	"margin contraction", "ebitda drops", "pat falls", "pat declines",
	"net loss", "net profit falls", "widening losses", "loss widens",
	"fraud", "scam", "cbi raid", "ed raid", "sebi ban",
	"insider trading", "accounting fraud", "promoter pledge",
	"auditor resigns", "cfo resigns", "ceo resigns",
	"downgrade", "target cut", "initiates sell", "underperform",
	"strong sell", "penalty imposed", "fine by sebi",
	"52-week low", "crashes", "plunges", "slumps", "tanks",
	"falls sharply", "heavy selling", "lower circuit",
	"order cancelled", "contract terminated", "plant shutdown",
	"layoffs", "job cuts", "default", "debt restructuring",
	"rate hike", "repo rate hike", "rbi hikes",
	"rupee weakens", "rupee falls", "crude surges",
	"inflation surges", "gdp misses", "war", "terror attack",
}

const (
	macroMinPriceMove  = 0.005
	macroMaxPriceMove  = 0.030
	macroMinRVol       = 1.5
	macroMaxSLPct      = 0.015
	macroPollInterval  = 5 * time.Second
)

var rssFeeds = []string{
	"https://www.thehindubusinessline.com/markets/feeder/default.rss",
	"https://economictimes.indiatimes.com/markets/rssfeeds/1977021501.cms",
	"https://economictimes.indiatimes.com/markets/stocks/rssfeeds/2146842.cms",
	"https://www.cnbctv18.com/commonfeeds/v1/cne/rss/market.xml",
	"https://www.livemint.com/rss/markets",
}

func NewMacroAgent(universe map[uint32]string) *MacroAgent {
	return &MacroAgent{
		universe:       universe,
		sentimentCache: make(map[string]*sentimentEntry),
		seenURLs:       make(map[string]bool),
		feedETags:      make(map[string]string),
	}
}

func (m *MacroAgent) Start() {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.mu.Unlock()

	go m.pollLoop()
	log.Println("[MacroAgent] Background news scanner started (5s poll)")
}

func (m *MacroAgent) Stop() {
	m.mu.Lock()
	m.running = false
	m.mu.Unlock()
	log.Println("[MacroAgent] Stopped")
}

func (m *MacroAgent) pollLoop() {
	for {
		m.mu.Lock()
		if !m.running {
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()

		m.fetchAndProcess()
		time.Sleep(macroPollInterval)
	}
}

func (m *MacroAgent) fetchAndProcess() {
	// Prune dedup set
	m.mu.Lock()
	if len(m.seenURLs) > 5000 {
		m.seenURLs = make(map[string]bool)
	}
	m.mu.Unlock()

	// Fetch all feeds concurrently
	type feedResult struct {
		items []rssItem
	}
	results := make(chan feedResult, len(rssFeeds))

	for _, feedURL := range rssFeeds {
		go func(u string) {
			items := m.fetchFeed(u)
			results <- feedResult{items}
		}(feedURL)
	}

	var allItems []rssItem
	for i := 0; i < len(rssFeeds); i++ {
		r := <-results
		allItems = append(allItems, r.items...)
	}

	// Build symbol mapping
	mapping := make(map[string]uint32) // symbol -> token
	for token, symbol := range m.universe {
		mapping[symbol] = token
	}

	// Score and confirm
	for _, item := range allItems {
		m.mu.Lock()
		if m.seenURLs[item.Link] {
			m.mu.Unlock()
			continue
		}
		m.seenURLs[item.Link] = true
		m.mu.Unlock()

		combined := strings.ToLower(item.Title + " " + item.Description)
		bullScore := 0
		bearScore := 0
		for _, phrase := range strongBullish {
			if strings.Contains(combined, phrase) {
				bullScore++
			}
		}
		for _, phrase := range strongBearish {
			if strings.Contains(combined, phrase) {
				bearScore++
			}
		}

		sentiment := "neutral"
		var matchedSymbol string
		for symbol, token := range mapping {
			// Strict word boundary match to prevent 'ACC' matching 'according'
			matched, _ := regexp.MatchString(`\b`+strings.ToLower(regexp.QuoteMeta(symbol))+`\b`, combined)
			if !matched {
				continue
			}
			matchedSymbol = symbol

			m.mu.Lock()
			if bullScore > 0 && bearScore == 0 {
				m.sentimentCache[symbol] = &sentimentEntry{Bias: "BULLISH", Timestamp: time.Now()}
				sentiment = "bullish"
				sig := m.confirmAndBuild(token, symbol, item.Title, false)
				if sig != nil {
					m.signalQueue = append(m.signalQueue, sig)
				}
			} else if bearScore > 0 && bullScore == 0 {
				m.sentimentCache[symbol] = &sentimentEntry{Bias: "BEARISH", Timestamp: time.Now()}
				sentiment = "bearish"
				sig := m.confirmAndBuild(token, symbol, item.Title, true)
				if sig != nil {
					m.signalQueue = append(m.signalQueue, sig)
				}
			}
			m.mu.Unlock()
			break
		}

		if bullScore > bearScore {
			sentiment = "bullish"
		} else if bearScore > bullScore {
			sentiment = "bearish"
		}

		// Save to recent news
		news := NewsItem{
			Link:      item.Link,
			Image:     item.Image,
			Sentiment: sentiment,
			Symbol:    matchedSymbol,
			Title:     item.Title,
			Source:    item.Source,
			Time:      item.PubDate,
		}
		
		if news.Time == "" {
			news.Time = time.Now().Format("15:04")
		}

		m.mu.Lock()
		m.recentNews = append([]NewsItem{news}, m.recentNews...)
		if len(m.recentNews) > 100 {
			m.recentNews = m.recentNews[:100] // keep last 100
		}
		m.mu.Unlock()
	}
}

func (m *MacroAgent) confirmAndBuild(token uint32, symbol, headline string, isShort bool) *Signal {
	if m.GetLTPIfFresh == nil || m.GetDayOpen == nil {
		return nil
	}

	current := m.GetLTPIfFresh(token)
	dayOpen := m.GetDayOpen(token)
	if current <= 0 || dayOpen <= 0 {
		return nil
	}

	priceChg := (current - dayOpen) / dayOpen

	if !isShort && priceChg < macroMinPriceMove {
		return nil
	}
	if isShort && priceChg > -macroMinPriceMove {
		return nil
	}

	absMove := priceChg
	if absMove < 0 {
		absMove = -absMove
	}
	if absMove > macroMaxPriceMove {
		return nil
	}

	rvol := 0.0
	if m.ComputeRVol != nil {
		rvol = m.ComputeRVol(token)
	}
	if rvol < macroMinRVol {
		return nil
	}

	atr := current * 0.02
	if m.GetATR != nil {
		a := m.GetATR(token)
		if a > 0 {
			atr = a
		}
	}

	atrStop := atr * 0.5
	maxStop := current * macroMaxSLPct
	stopDist := atrStop
	if maxStop < atrStop {
		stopDist = maxStop
	}

	var stopPrice, targetPrice, partialTarget float64
	if isShort {
		stopPrice = current + stopDist
		targetPrice = current - atr*2.0
		partialTarget = (current + targetPrice) / 2
	} else {
		stopPrice = current - stopDist
		targetPrice = current + atr*2.0
		partialTarget = (current + targetPrice) / 2
	}

	stratName := "S8_MACRO_LONG"
	if isShort {
		stratName = "S8_MACRO_SHORT"
	}

	return &Signal{
		Strategy:      stratName,
		Symbol:        symbol,
		Token:         token,
		Regime:        "NEWS_OVERRIDE",
		EntryPrice:    current,
		IsShort:       isShort,
		StopPrice:     stopPrice,
		TargetPrice:   targetPrice,
		PartialTarget: partialTarget,
		RVol:          rvol,
		ATR:           atr,
		Product:       "MIS",
	}
}

// CheckVeto blocks trades that conflict with active sentiment
func (m *MacroAgent) CheckVeto(symbol string, isLong bool, regime string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	cache, ok := m.sentimentCache[symbol]
	if !ok {
		return false // No active news, let technical signals ride unhindered
	}

	// Aggressive regimes shrink the freshness window for how long news is relevant
	aggressive := regime == "VOLATILE" || regime == "CHOP" || regime == "BEAR_PANIC" || regime == "EXTREME_PANIC"
	maxHours := 4.0
	if aggressive {
		maxHours = 2.0
	}

	if time.Since(cache.Timestamp).Hours() > maxHours {
		return false // News is stale, let technicals ride
	}

	// Active conflict check
	if isLong && cache.Bias == "BEARISH" {
		return true // Veto: Going long into fresh bearish news
	}
	if !isLong && cache.Bias == "BULLISH" {
		return true // Veto: Shorting into fresh bullish news
	}
	
	return false
}

// DrainSignals atomically drains all confirmed signals
func (m *MacroAgent) DrainSignals() []*Signal {
	m.mu.Lock()
	defer m.mu.Unlock()
	signals := m.signalQueue
	m.signalQueue = nil
	return signals
}

// GetNewsFeed returns recent news
func (m *MacroAgent) GetNewsFeed() []NewsItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	res := make([]NewsItem, len(m.recentNews))
	copy(res, m.recentNews)
	return res
}

// ── RSS parsing ──────────────────────────────────────────────

type rssItem struct {
	Title       string
	Description string
	Link        string
	Image       string
	Source      string
	PubDate     string
}

type rssFeed struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Title string `xml:"title"`
		Items []struct {
			Title       string `xml:"title"`
			Link        string `xml:"link"`
			Description string `xml:"description"`
			PubDate     string `xml:"pubDate"`
			Enclosure   struct {
				URL string `xml:"url,attr"`
			} `xml:"enclosure"`
		} `xml:"item"`
	} `xml:"channel"`
}

var imgRegex = regexp.MustCompile(`<img[^>]+src="([^">]+)"`)

func (m *MacroAgent) fetchFeed(feedURL string) []rssItem {
	client := &http.Client{Timeout: 6 * time.Second}
	req, _ := http.NewRequest("GET", feedURL, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/122.0.0.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	m.mu.Lock()
	etag := m.feedETags[feedURL]
	m.mu.Unlock()
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == 304 {
		return nil
	}
	if resp.StatusCode != 200 {
		return nil
	}

	if e := resp.Header.Get("ETag"); e != "" {
		m.mu.Lock()
		m.feedETags[feedURL] = e
		m.mu.Unlock()
	}

	body, _ := io.ReadAll(resp.Body)
	var feed rssFeed
	xml.Unmarshal(body, &feed)

	sourceName := "NEWS"
	if strings.Contains(feed.Channel.Title, "Economic Times") {
		sourceName = "ET"
	} else if strings.Contains(feed.Channel.Title, "Livemint") || strings.Contains(feedURL, "livemint") {
		sourceName = "MINT"
	} else if strings.Contains(feed.Channel.Title, "CNBC") || strings.Contains(feedURL, "cnbc") {
		sourceName = "CNBC"
	} else if strings.Contains(feedURL, "thehindubusinessline") {
		sourceName = "HBL"
	}

	var items []rssItem
	for _, item := range feed.Channel.Items {
		if item.Title != "" {
			imageURL := item.Enclosure.URL
			if imageURL == "" {
				matches := imgRegex.FindStringSubmatch(item.Description)
				if len(matches) > 1 {
					imageURL = matches[1]
				}
			}

			pubTime := item.PubDate
			if len(pubTime) > 0 {
				if t, err := time.Parse(time.RFC1123Z, pubTime); err == nil {
					pubTime = t.Local().Format("15:04")
				} else if t, err := time.Parse(time.RFC1123, pubTime); err == nil {
					pubTime = t.Local().Format("15:04")
				}
			}

			items = append(items, rssItem{
				Title:       item.Title,
				Description: item.Description,
				Link:        item.Link,
				Image:       imageURL,
				Source:      sourceName,
				PubDate:     pubTime,
			})
		}
	}
	return items
}

// Suppress unused import
var _ = fmt.Sprintf
