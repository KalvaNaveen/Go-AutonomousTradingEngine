package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"

	"bnf_go_engine/config"
	"github.com/joho/godotenv"
)

func main() {
	godotenv.Load("./.env")
	config.Reload()

	fmt.Printf("Bot Token: %s...\n", config.TelegramBotToken[:5])
	fmt.Printf("Chat IDs: %v\n", config.TelegramChatIDs)

	if len(config.TelegramChatIDs) == 0 {
		fmt.Println("No chat IDs configured.")
		return
	}

	base := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", config.TelegramBotToken)
	resp, err := http.PostForm(base, url.Values{
		"chat_id":    {config.TelegramChatIDs[0]},
		"text":       {"Test message from script"},
		"parse_mode": {"Markdown"},
	})
	
	if err != nil {
		log.Fatalf("Error: %v", err)
	}
	defer resp.Body.Close()
	
	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("Status: %s\n", resp.Status)
	fmt.Printf("Body: %s\n", string(body))
}
