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

	SendTelegram(proposal + fmt.Sprintf(
		"\n\n*Reply* `PROCEED` to buy or `HOLD` to skip.\nAuto-HOLD at *%s* IST if no reply.",
		deadline.In(config.IST).Format("15:04")))
	log.Printf("[Approval] Awaiting PROCEED/HOLD until %s IST", deadline.In(config.IST).Format("15:04"))

	authorized := map[string]bool{}
	for _, id := range config.TelegramChatIDs {
		authorized[id] = true
	}

	for time.Now().Before(deadline) {
		updates, next := pollUpdates(offset)
		offset = next
		for _, u := range updates {
			chatID := fmt.Sprintf("%d", u.Message.Chat.ID)
			if !authorized[chatID] {
				continue
			}
			text := strings.ToLower(strings.TrimSpace(u.Message.Text))
			if matchesAny(text, proceedWords) {
				log.Printf("[Approval] PROCEED received from %s", chatID)
				SendTelegram("✅ *PROCEED received* — placing BUY orders.")
				return true
			}
			if matchesAny(text, holdWords) {
				log.Printf("[Approval] HOLD received from %s", chatID)
				SendTelegram("🛑 *HOLD received* — no trades today.")
				return false
			}
		}
		time.Sleep(2 * time.Second)
	}
	log.Println("[Approval] Deadline passed with no reply — auto-HOLD")
	SendTelegram("⏰ *No reply by deadline* — auto-HOLD, no trades today.")
	return false
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
