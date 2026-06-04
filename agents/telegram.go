package agents

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"bnf_go_engine/config"
)

// ComputeEMA21 — alias for EMA20 (kept for test compatibility).
func ComputeEMA21(closes []float64) float64 {
	s := computeEMASeries(closes, config.EMA20Period)
	if len(s) == 0 {
		return 0
	}
	return s[len(s)-1]
}

var telegramMu sync.Mutex

// SendTelegram sends a Markdown-formatted message to all configured Telegram chat IDs.
func SendTelegram(msg string) {
	if config.TelegramBotToken == "" || len(config.TelegramChatIDs) == 0 {
		log.Printf("[ALERT] %s", msg)
		return
	}
	go func() {
		telegramMu.Lock()
		defer telegramMu.Unlock()
		base := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", config.TelegramBotToken)
		for _, chatID := range config.TelegramChatIDs {
			resp, err := http.PostForm(base, url.Values{
				"chat_id":    {chatID},
				"text":       {msg},
				"parse_mode": {"Markdown"},
			})
			if err != nil {
				log.Printf("[Telegram] Send failed (chat=%s): %v", chatID, err)
			} else {
				body, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 {
					log.Printf("[Telegram] API error (chat=%s status=%d): %s", chatID, resp.StatusCode, string(body))
				}
			}
			time.Sleep(300 * time.Millisecond)
		}
	}()
}

// SendTelegramDocument sends a file (e.g. CSV) with a caption to all configured chat IDs.
func SendTelegramDocument(filePath, caption string) {
	if config.TelegramBotToken == "" || len(config.TelegramChatIDs) == 0 {
		log.Printf("[Report] Would send %s", filePath)
		return
	}
	for _, chatID := range config.TelegramChatIDs {
		go func(cid string) {
			file, err := os.Open(filePath)
			if err != nil {
				log.Printf("[Report] Failed to open %s: %v", filePath, err)
				return
			}
			defer file.Close()

			body := &bytes.Buffer{}
			writer := multipart.NewWriter(body)
			writer.WriteField("chat_id", cid)
			writer.WriteField("caption", caption)
			writer.WriteField("parse_mode", "Markdown")

			fileName := filePath
			if idx := strings.LastIndex(filePath, string(os.PathSeparator)); idx >= 0 {
				fileName = filePath[idx+1:]
			}
			part, err := writer.CreateFormFile("document", fileName)
			if err != nil {
				log.Printf("[Report] CreateFormFile: %v", err)
				return
			}
			io.Copy(part, file)
			writer.Close()

			apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument", config.TelegramBotToken)
			resp, err := http.Post(apiURL, writer.FormDataContentType(), body)
			if err != nil {
				log.Printf("[Report] Send document failed: %v", err)
				return
			}
			resp.Body.Close()
		}(chatID)
	}
}
