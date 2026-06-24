package gate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// LLM is the optional Tier-2 second-opinion layer. It sends the day's surviving
// stocks (with their recent headlines) to Claude Haiku in a single batched call
// and asks which are materially negative for a BTST hold. Raw HTTP is used to
// avoid adding an SDK dependency, matching the rest of this codebase.
//
// Model: claude-haiku-4-5 — cheapest tier, ample for headline classification.
type LLM struct {
	http   *http.Client
	apiKey string
	model  string
}

const (
	anthropicURL     = "https://api.anthropic.com/v1/messages"
	anthropicVersion = "2023-06-01"
	defaultLLMModel  = "claude-haiku-4-5"
)

// NewLLMFromEnv returns an LLM client when BTST_NEWS_LLM=true and ANTHROPIC_API_KEY
// is set; otherwise nil (so the news filter stays keyword-only). BTST_LLM_MODEL
// overrides the model.
func NewLLMFromEnv() *LLM {
	if strings.ToLower(os.Getenv("BTST_NEWS_LLM")) != "true" {
		return nil
	}
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return nil
	}
	model := os.Getenv("BTST_LLM_MODEL")
	if model == "" {
		model = defaultLLMModel
	}
	return &LLM{
		http:   &http.Client{Timeout: 30 * time.Second},
		apiKey: key,
		model:  model,
	}
}

var jsonBlockRe = regexp.MustCompile(`(?s)\{.*\}`)

// Classify asks the model which stocks are materially negative. headlines maps
// symbol → recent headlines. It returns symbol → reason for the ones to DROP.
// On any error it fails open (returns nothing) — the LLM must never block trading.
func (l *LLM) Classify(ctx context.Context, headlines map[string][]string) map[string]string {
	if len(headlines) == 0 {
		return nil
	}

	// Build a compact, deterministic prompt body.
	var b strings.Builder
	for sym, hs := range headlines {
		fmt.Fprintf(&b, "%s:\n", sym)
		for _, h := range hs {
			if len(h) > 200 {
				h = h[:200]
			}
			fmt.Fprintf(&b, "  - %s\n", h)
		}
	}

	system := "You are a risk filter for an overnight (buy-today-sell-tomorrow) Indian " +
		"equity strategy. For each stock, decide if its recent headlines contain " +
		"MATERIALLY NEGATIVE company-specific news that makes holding it overnight risky " +
		"(e.g. fraud, regulatory action, earnings miss, guidance cut, promoter/pledge " +
		"issues, debt default, major litigation, management exit). Ignore routine, " +
		"neutral, or positive coverage and broad market commentary. Be conservative: " +
		"only flag clearly material negatives. Respond ONLY with JSON of the form " +
		`{"negative":[{"symbol":"SYM","reason":"short reason"}]}. Empty list if none.`

	reqBody, _ := json.Marshal(map[string]any{
		"model":      l.model,
		"max_tokens": 1024,
		"system":     system,
		"messages": []map[string]any{
			{"role": "user", "content": b.String()},
		},
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil
	}
	req.Header.Set("x-api-key", l.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("content-type", "application/json")

	resp, err := l.http.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil
	}

	var text string
	for _, c := range out.Content {
		if c.Type == "text" {
			text += c.Text
		}
	}
	jsonStr := jsonBlockRe.FindString(text)
	if jsonStr == "" {
		return nil
	}

	var parsed struct {
		Negative []struct {
			Symbol string `json:"symbol"`
			Reason string `json:"reason"`
		} `json:"negative"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return nil
	}

	drops := make(map[string]string, len(parsed.Negative))
	for _, neg := range parsed.Negative {
		sym := strings.TrimSpace(neg.Symbol)
		if sym == "" {
			continue
		}
		reason := strings.TrimSpace(neg.Reason)
		if reason == "" {
			reason = "materially negative"
		}
		drops[sym] = "LLM: " + reason
	}
	return drops
}
