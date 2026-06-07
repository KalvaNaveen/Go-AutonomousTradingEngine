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

// handleFullScan triggers the on-demand scan. It now runs the EXACT SAME
// pipeline as the 16:00 auto EOD scan (RunEODMarketScan) — same ~750-stock
// Nifty Total Market universe, same Kronos AI ranking, same 3-day cooldown
// dedup + persistence, same CSV export. No parallel/lighter implementation.
//
// The only difference from the scheduled run: this one can be triggered any
// time by texting "scan" (or "signals", "setups", etc.) to the bot.
func handleFullScan(chatID, msgText string, scanner *ScannerAgent, signalAgent *SignalAlertAgent) {
	if scanner == nil || scanner.DailyCache == nil || !scanner.DailyCache.Loaded {
		replyToChat(chatID, "⚠️ Cache not loaded yet — try again in a moment.")
		return
	}
	if scanner.EODDeps.LoadUniverse == nil {
		replyToChat(chatID, "⚠️ Scan pipeline not wired yet — try again in a moment.")
		return
	}

	now := config.NowIST()
	hhmm := now.Hour()*100 + now.Minute()
	marketOpen := hhmm >= 915 && hhmm <= 1530

	dataTag := "📂 refreshing data..."
	if marketOpen {
		dataTag = "📡 live Kite data"
	}

	replyToChat(chatID, fmt.Sprintf(
		"🔍 *Manual scan triggered* — %s\n%s | Nifty Total Market (~750 stocks)\n"+
			"_Running the full EOD pipeline: AI ranking, dedup, CSV..._",
		config.NowIST().Format("02 Jan 2006 15:04 IST"), dataTag))

	log.Printf("[Bot] Manual scan requested (chat=%s) — running RunEODMarketScan (same as 16:00 auto scan)", chatID)

	// Run the IDENTICAL pipeline used by the scheduled 16:00 EOD scan.
	// This guarantees feature parity by construction — there's no second
	// implementation to drift out of sync.
	RunEODMarketScan(scanner.EODDeps, scanner)
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
