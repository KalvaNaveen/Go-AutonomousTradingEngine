package agents

// ══════════════════════════════════════════════════════════════
//  Telegram Bot — Manual Command Handler
//
//  Polls Telegram for incoming messages (long-polling).
//  Any message containing a scan intent keyword triggers a
//  FULL EOD-identical scan:
//    1. 🟢 EMA Pullback BUY signals  (same as RunEODBuyAlerts)
//    2. 🔴 SELL alerts for open positions (same as RunEODSellAlerts)
//    3. 🚀 MOMO Leaders              (same as EODBookScans MOMO part)
//    4. 🔥 Trigger Candles           (same as EODBookScans trigger candles)
//    5. 🦅 Bird's Eye View           (same as RunBirdsEyeView)
//
//  Command matching is intent-based — "scan", "full scan",
//  "get latest signals", "check market" etc. all trigger the scan.
//  All BUY output is the consolidated EOD summary — no per-stock spam.
//
//  Security: only responds to chat IDs in TELEGRAM_CHAT_IDS.
// ══════════════════════════════════════════════════════════════

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"bnf_go_engine/config"
)

// ── Telegram Update types ─────────────────────────────────────────────────────

type tgUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
		MessageID int64 `json:"message_id"`
		Chat      struct {
			ID int64 `json:"id"`
		} `json:"chat"`
		From *struct {
			FirstName string `json:"first_name"`
		} `json:"from"`
		Text string `json:"text"`
		Date int64  `json:"date"`
	} `json:"message"`
}

// ── Bot lifecycle ─────────────────────────────────────────────────────────────

// StartTelegramBot starts long-polling in a background goroutine.
// Call once from main.go after cache is ready.
func StartTelegramBot(scanner *ScannerAgent, signalAgent *SignalAlertAgent) {
	if config.TelegramBotToken == "" {
		log.Println("[Bot] No TELEGRAM_BOT_TOKEN — bot disabled")
		return
	}
	go func() {
		log.Println("[Bot] Telegram bot started — polling for commands")
		runBotLoop(scanner, signalAgent)
	}()
}

func runBotLoop(scanner *ScannerAgent, signalAgent *SignalAlertAgent) {
	offset := int64(0)
	apiBase := fmt.Sprintf("https://api.telegram.org/bot%s", config.TelegramBotToken)
	client := &http.Client{Timeout: 40 * time.Second}

	allowedChats := make(map[string]bool)
	for _, id := range config.TelegramChatIDs {
		allowedChats[strings.TrimSpace(id)] = true
	}

	for {
		updates, nextOffset, err := pollUpdates(client, apiBase, offset)
		if err != nil {
			log.Printf("[Bot] Poll error: %v — retrying in 5s", err)
			time.Sleep(5 * time.Second)
			continue
		}
		offset = nextOffset

		for _, u := range updates {
			if u.Message == nil || u.Message.Text == "" {
				continue
			}
			chatID := fmt.Sprintf("%d", u.Message.Chat.ID)
			if !allowedChats[chatID] {
				log.Printf("[Bot] Ignored message from unknown chat %s", chatID)
				continue
			}
			name := ""
			if u.Message.From != nil {
				name = u.Message.From.FirstName
			}
			text := strings.TrimSpace(u.Message.Text)
			log.Printf("[Bot] Message from %s (chat=%s): %q", name, chatID, text)
			go handleBotMessage(chatID, text, scanner, signalAgent)
		}
	}
}

func pollUpdates(client *http.Client, apiBase string, offset int64) ([]tgUpdate, int64, error) {
	endpoint := fmt.Sprintf("%s/getUpdates?timeout=30&offset=%d", apiBase, offset)
	resp, err := client.Get(endpoint)
	if err != nil {
		return nil, offset, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result struct {
		OK     bool       `json:"ok"`
		Result []tgUpdate `json:"result"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, offset, fmt.Errorf("json: %v", err)
	}
	if !result.OK {
		return nil, offset, fmt.Errorf("telegram not ok: %s", string(body))
	}

	nextOffset := offset
	for _, u := range result.Result {
		if u.UpdateID >= nextOffset {
			nextOffset = u.UpdateID + 1
		}
	}
	return result.Result, nextOffset, nil
}

// ── Message routing ───────────────────────────────────────────────────────────

// handleBotMessage routes any Telegram message to the right handler.
// Intent-based: checks for keywords anywhere in the message.
func handleBotMessage(chatID, text string, scanner *ScannerAgent, signalAgent *SignalAlertAgent) {
	lower := strings.ToLower(strings.TrimSpace(text))

	switch {
	// ── Scan intent
	case containsAnyWord(lower,
		"scan", "signal", "signals", "latest", "setup", "setups",
		"buy", "market", "check", "run", "full", "eod"):
		handleFullScan(chatID, lower, scanner, signalAgent)

	// ── Status
	case containsAnyWord(lower, "status", "health", "engine"):
		handleStatusCommand(chatID, scanner)

	// ── Help / start
	case containsAnyWord(lower, "help", "start", "hi", "hello", "/help", "/start"):
		replyToChat(chatID,
			"👋 *Zenith Trading Engine*\n\n"+
				"Just say what you want:\n\n"+
				"📡 *scan* — Full market scan\n"+
				"⚙️ *status* — Engine health\n"+
				"❓ *help* — This message\n\n"+
				"_EOD scan auto-runs at 16:00 IST_")

	default:
		replyToChat(chatID,
			"❓ I didn't understand that. Try:\n"+
				"• *scan* — scan the market\n"+
				"• *status* — engine health\n"+
				"• *help* — all commands")
	}
}

// parseRSThreshold extracts an RS threshold from a message.
// Supports: "scan 85", "scan 90+", "rs=85", "rs85", "rs 90"
// Falls back to config.MinRSScore (default 80) if nothing found.
func parseRSThreshold(text string) int {
	text = strings.ToLower(text)

	// Try "rs=85" or "rs=90"
	if idx := strings.Index(text, "rs="); idx >= 0 {
		rest := strings.TrimLeft(text[idx+3:], " ")
		num := extractLeadingInt(rest)
		if num >= 50 && num <= 99 {
			return num
		}
	}

	// Try "rs 85" or "rs85"
	if idx := strings.Index(text, "rs"); idx >= 0 {
		rest := strings.TrimLeft(text[idx+2:], " ")
		num := extractLeadingInt(rest)
		if num >= 50 && num <= 99 {
			return num
		}
	}

	// Try bare number: "scan 85" or "scan 90+"
	// Look for any 2-digit number in range 50–99 in the message
	words := strings.Fields(text)
	for _, w := range words {
		w = strings.TrimRight(w, "+%")
		if n, err := strconv.Atoi(w); err == nil && n >= 50 && n <= 99 {
			return n
		}
	}

	return config.MinRSScore // default
}

// extractLeadingInt reads a leading integer from a string (stops at non-digit).
func extractLeadingInt(s string) int {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n, _ := strconv.Atoi(s[:end])
	return n
}

// containsAnyWord returns true if `text` contains any of the given words.
func containsAnyWord(text string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// ── Full EOD-style scan ───────────────────────────────────────────────────────

// handleFullScan runs the complete scan pipeline on demand:
//  1. 📊 EOD Summary: top BUY picks (score-based + EMA pullback pattern)
//  2. 🚀 MOMO Leaders + 🔥 Trigger Candles (EODBookScans)
//  3. 🦅 Bird's Eye View market health report
func handleFullScan(chatID, msgText string, scanner *ScannerAgent, signalAgent *SignalAlertAgent) {
	if scanner == nil || scanner.DailyCache == nil || !scanner.DailyCache.Loaded {
		replyToChat(chatID, "⚠️ Cache not loaded yet — try again in a moment.")
		return
	}

	// Parse RS threshold from message — supports: "scan 85", "scan 90+", "rs=85", "rs85"
	rsMin := parseRSThreshold(msgText)

	now := config.NowIST()
	hhmm := now.Hour()*100 + now.Minute()
	marketOpen := hhmm >= 915 && hhmm <= 1530

	dataTag := "📂 refreshing data..."
	if marketOpen {
		dataTag = "📡 live Kite data"
	}

	replyToChat(chatID, fmt.Sprintf(
		"🔍 *Scanning* — %s\n%s | RS≥%d | %d stocks",
		config.NowIST().Format("02 Jan 2006 15:04 IST"),
		dataTag, rsMin, len(scanner.Universe)))

	// Outside market hours: refresh cache so we always have the latest EOD candles
	// (same as what the auto EOD scan does before running)
	if !marketOpen && scanner.RefreshCache != nil {
		scanner.RefreshCache()
	}

	start := time.Now()
	cache := scanner.DailyCache

	// Wire live LTP from the WebSocket if available (market hours 9:15-15:30).
	// Outside market hours GetLTP returns 0 and analyzeStock falls back to cache.
	liveDeps := EODScanDeps{
		GetLiveLTP: scanner.GetLTP, // nil-safe: returns 0 when WS disconnected
	}

	// Detect if we have live data (at least one stock has a fresh tick)
	hasLive := false
	if scanner.GetLTP != nil {
		for token := range scanner.Universe {
			if ltp := scanner.GetLTP(token); ltp > 0 {
				hasLive = true
				break
			}
		}
	}
	dataSource := "historical data (market closed)"
	if hasLive {
		dataSource = "live Kite WebSocket data ✅"
	}
	log.Printf("[Bot] Scan data source: %s", dataSource)

	// ── 1. Score-based classifier scan ───────────────────────────────────────
	// Uses live LTP during market hours, cached close otherwise.
	log.Println("[Bot] Running score-based market scan...")
	var results []EODScanResult
	scanned := 0
	for token, symbol := range scanner.Universe {
		scanned++
		r := analyzeStock(token, symbol, symbol, cache, liveDeps, scanner)
		if r != nil {
			results = append(results, *r)
		}
	}

	// Keep only BUY results with RS >= rsMin, sort by RS desc
	var buyResults []EODScanResult
	for _, r := range results {
		if r.Signal == "BUY" && r.RSScore >= rsMin {
			buyResults = append(buyResults, r)
		}
	}
	sort.Slice(buyResults, func(i, j int) bool {
		return buyResults[i].RSScore > buyResults[j].RSScore
	})
	results = buyResults
	buyCount := len(results)
	sellCount := 0

	elapsed := time.Since(start)

	// Build and send the EOD summary (top BUY + momentum) — same format as EOD
	summary := buildEODSummary(results, scanned, buyCount, sellCount, elapsed)

	// ── 2. MOMO Leaders + Trigger Candles (EODBookScans) ─────────────────────
	bookScans := EODBookScans(cache, scanner.Universe, scanner.GetLTP, nil)
	if bookScans != "" {
		summary += bookScans
	}

	SendTelegram(summary)

	// ── 3. Bird's Eye View ────────────────────────────────────────────────────
	log.Println("[Bot] Running Bird's Eye View...")
	scanner.RunBirdsEyeView()

	log.Printf("[Bot] Full scan complete: %d stocks, %d BUY setups, %.1fs",
		scanned, buyCount, elapsed.Seconds())
}

// ── Status command ────────────────────────────────────────────────────────────

func handleStatusCommand(chatID string, scanner *ScannerAgent) {
	if scanner == nil || scanner.DailyCache == nil {
		replyToChat(chatID, "⚠️ Engine not fully initialised yet.")
		return
	}
	cacheStatus := "❌ Not loaded"
	if scanner.DailyCache.Loaded {
		cacheStatus = fmt.Sprintf("✅ %d tokens loaded", len(scanner.DailyCache.Closes))
	}
	msg := fmt.Sprintf(
		"⚙️ *ENGINE STATUS*\n"+
			"━━━━━━━━━━━━━━━━━━━━━━━━\n"+
			"📦 Cache: %s\n"+
			"🕐 Time: `%s`\n"+
			"⏰ Next EOD scan: `16:00 IST`\n"+
			"📡 Type *scan* to scan now",
		cacheStatus,
		config.NowIST().Format("02 Jan 2006 15:04 IST"),
	)
	replyToChat(chatID, msg)
}

// ── Reply helper ──────────────────────────────────────────────────────────────

// replyToChat sends a Markdown message to one specific chat ID (synchronous).
func replyToChat(chatID, msg string) {
	if config.TelegramBotToken == "" {
		log.Printf("[Bot→%s] %s", chatID, msg)
		return
	}
	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", config.TelegramBotToken)
	resp, err := http.PostForm(apiURL, url.Values{
		"chat_id":    {chatID},
		"text":       {msg},
		"parse_mode": {"Markdown"},
	})
	if err != nil {
		log.Printf("[Bot] replyToChat error (chat=%s): %v", chatID, err)
		return
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		log.Printf("[Bot] Telegram API error (chat=%s status=%d): %s",
			chatID, resp.StatusCode, string(body))
	}
}
