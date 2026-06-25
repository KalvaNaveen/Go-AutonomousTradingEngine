package agents

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"bnf_go_engine/config"
)

// proceedWords / holdWords are the accepted replies (case-insensitive).
var (
	proceedWords = []string{"proceed", "yes", "go", "buy", "ok", "confirm"}
	holdWords    = []string{"hold", "no", "skip", "stop", "cancel"}
)

// RequestApproval sends the proposed BUY basket to Telegram and waits for a
// PROCEED / HOLD reply from an authorized chat, until the deadline. It returns
// true only on an explicit PROCEED; a HOLD reply or no reply by the deadline
// returns false (auto-HOLD). It only reads messages that arrive AFTER the
// proposal is sent, so a stale earlier reply can't trigger a trade.
func RequestApproval(proposal string, deadline time.Time) bool {
	if config.TelegramBotToken == "" || len(config.TelegramChatIDs) == 0 {
		// Can't ask — fail safe to HOLD in this configuration.
		log.Println("[Approval] Telegram not configured — auto-HOLD")
		return false
	}

	// Establish the update offset BEFORE sending, so we ignore old messages.
	offset := latestUpdateID() + 1

	hhmm := deadline.In(config.IST).Format("15:04")
	text := proposal + fmt.Sprintf("\n\nTap a button below (or reply PROCEED/HOLD).\nAuto-HOLD at *%s* IST if no response.", hhmm)
	sendApprovalKeyboard(text)
	log.Printf("[Approval] Awaiting PROCEED/HOLD until %s IST", hhmm)

	authorized := map[string]bool{}
	for _, id := range config.TelegramChatIDs {
		authorized[id] = true
	}

	for time.Now().Before(deadline) {
		updates, next := pollUpdates(offset)
		offset = next
		for _, u := range updates {
			// Inline-button tap (preferred).
			if cq := u.CallbackQuery; cq != nil {
				chatID := fmt.Sprintf("%d", cq.Message.Chat.ID)
				if !authorized[chatID] {
					continue
				}
				answerCallback(cq.ID)
				switch cq.Data {
				case "btst_proceed":
					log.Printf("[Approval] PROCEED (button) from %s", chatID)
					SendTelegram("✅ *PROCEED* — placing BUY orders.")
					return true
				case "btst_hold":
					log.Printf("[Approval] HOLD (button) from %s", chatID)
					SendTelegram("🛑 *HOLD* — no trades today.")
					return false
				}
				continue
			}
			// Typed reply (fallback).
			chatID := fmt.Sprintf("%d", u.Message.Chat.ID)
			if !authorized[chatID] {
				continue
			}
			reply := strings.ToLower(strings.TrimSpace(u.Message.Text))
			if matchesAny(reply, proceedWords) {
				log.Printf("[Approval] PROCEED (text) from %s", chatID)
				SendTelegram("✅ *PROCEED received* — placing BUY orders.")
				return true
			}
			if matchesAny(reply, holdWords) {
				log.Printf("[Approval] HOLD (text) from %s", chatID)
				SendTelegram("🛑 *HOLD received* — no trades today.")
				return false
			}
		}
		time.Sleep(2 * time.Second)
	}
	log.Println("[Approval] Deadline passed with no response — auto-HOLD")
	SendTelegram("⏰ *No response by deadline* — auto-HOLD, no trades today.")
	return false
}

// sendApprovalKeyboard posts the proposal with PROCEED / HOLD inline buttons to
// every authorized chat.
func sendApprovalKeyboard(text string) {
	if config.TelegramBotToken == "" {
		log.Printf("[ALERT] %s", text)
		return
	}
	markup := `{"inline_keyboard":[[{"text":"✅ PROCEED","callback_data":"btst_proceed"},{"text":"🛑 HOLD","callback_data":"btst_hold"}]]}`
	api := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", config.TelegramBotToken)
	for _, chatID := range config.TelegramChatIDs {
		http.PostForm(api, url.Values{
			"chat_id":      {chatID},
			"text":         {text},
			"parse_mode":   {"Markdown"},
			"reply_markup": {markup},
		})
		time.Sleep(200 * time.Millisecond)
	}
}

// answerCallback acknowledges a button tap so Telegram clears the loading state.
func answerCallback(callbackID string) {
	if config.TelegramBotToken == "" {
		return
	}
	api := fmt.Sprintf("https://api.telegram.org/bot%s/answerCallbackQuery", config.TelegramBotToken)
	http.PostForm(api, url.Values{"callback_query_id": {callbackID}})
}

func matchesAny(text string, words []string) bool {
	for _, w := range words {
		if text == w || strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// ── Telegram getUpdates plumbing ────────────────────────────────────────────

type tgUpdate struct {
	UpdateID int `json:"update_id"`
	Message  struct {
		Text string `json:"text"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
	CallbackQuery *struct {
		ID      string `json:"id"`
		Data    string `json:"data"`
		Message struct {
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"message"`
	} `json:"callback_query"`
}

// latestUpdateID returns the highest pending update_id (0 if none), used to skip
// messages that arrived before the proposal.
func latestUpdateID() int {
	updates, _ := pollUpdates(0)
	max := 0
	for _, u := range updates {
		if u.UpdateID > max {
			max = u.UpdateID
		}
	}
	return max
}

// pollUpdates fetches updates from `offset` and returns them plus the next offset.
func pollUpdates(offset int) ([]tgUpdate, int) {
	api := fmt.Sprintf("https://api.telegram.org/bot%s/getUpdates", config.TelegramBotToken)
	q := url.Values{"timeout": {"1"}}
	if offset > 0 {
		q.Set("offset", fmt.Sprintf("%d", offset))
	}
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get(api + "?" + q.Encode())
	if err != nil {
		return nil, offset
	}
	defer resp.Body.Close()

	var out struct {
		OK     bool       `json:"ok"`
		Result []tgUpdate `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || !out.OK {
		return nil, offset
	}
	next := offset
	for _, u := range out.Result {
		if u.UpdateID >= next {
			next = u.UpdateID + 1
		}
	}
	return out.Result, next
}
