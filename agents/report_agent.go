package agents

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"sort"
	"strings"
	"time"

	"bnf_go_engine/config"
	"bnf_go_engine/core"
)

// ReportAgent builds and sends daily/weekly/monthly reports via Telegram.
// Port of Python agents/report_agent.py
// Note: PDF generation is replaced with rich Markdown Telegram messages since
// Go doesn't have fpdf2. For full PDF support, we'd integrate go-fpdf.

// BuildDailyReport creates a rich Telegram daily summary
func BuildDailyReport(
	stats map[string]interface{},
	regime string,
	trades []*core.TradeLog,
	capital float64,
	totalScans int,
) string {
	dateStr := config.NowIST().Format("02 Jan 2006")
	pnl := toF64(stats["gross_pnl"])
	total := toInt(stats["total"])
	wins := toInt(stats["wins"])
	losses := toInt(stats["losses"])
	wr := toF64(stats["win_rate"])

	if total == 0 {
		return fmt.Sprintf("📅 *DAILY REPORT* : %s\nNo trades executed today. Engine healthy.", dateStr)
	}

	// Compute charges
	var totalCharges float64
	for _, t := range trades {
		totalCharges += core.ComputeChargesFromTrade(t.EntryPrice, t.FullExitPrice, t.Qty, t.IsShort, "MIS", 0)
	}

	dayROI := 0.0
	if capital > 0 {
		dayROI = pnl / capital * 100
	}

	sep := "━━━━━━━━━━━━━━━━━━━━━━━━"
	emoji := "📈"
	if pnl < 0 {
		emoji = "📉"
	}

	// Strategy breakdown
	stratStats := make(map[string]*stratStat)
	for _, t := range trades {
		ss, ok := stratStats[t.Strategy]
		if !ok {
			ss = &stratStat{}
			stratStats[t.Strategy] = ss
		}
		ss.Count++
		ss.PnL += t.GrossPnl
		if t.GrossPnl > 0 {
			ss.Wins++
		}
	}

	stratLines := ""
	if len(stratStats) > 0 {
		stratLines = "\n\n📊 *STRATEGY BREAKDOWN*\n"
		type stratEntry struct {
			name string
			stat *stratStat
		}
		var entries []stratEntry
		for name, ss := range stratStats {
			entries = append(entries, stratEntry{name, ss})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].stat.PnL > entries[j].stat.PnL })
		for _, e := range entries {
			swr := float64(e.stat.Wins) / float64(max1(e.stat.Count)) * 100
			icon := "✅"
			if e.stat.PnL < 0 {
				icon = "❌"
			}
			stratLines += fmt.Sprintf("%s `%s`: %dT %dW (%.0f%%) PnL: `Rs.%+.0f`\n",
				icon, e.name, e.stat.Count, e.stat.Wins, swr, e.stat.PnL)
		}
	}

	// Top winners/losers
	topLines := ""
	if len(trades) > 0 {
		sorted := make([]*core.TradeLog, len(trades))
		copy(sorted, trades)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].GrossPnl > sorted[j].GrossPnl })

		if sorted[0].GrossPnl > 0 {
			topLines += "\n🏆 *TOP WINNERS*\n"
			for i, t := range sorted {
				if i >= 3 || t.GrossPnl <= 0 {
					break
				}
				topLines += fmt.Sprintf("  %d. `%s` +Rs.%.0f (%s)\n", i+1, t.Symbol, t.GrossPnl, t.Strategy)
			}
		}
		if sorted[len(sorted)-1].GrossPnl < 0 {
			topLines += "\n💀 *TOP LOSERS*\n"
			for i := len(sorted) - 1; i >= 0 && i > len(sorted)-4; i-- {
				t := sorted[i]
				if t.GrossPnl >= 0 {
					break
				}
				topLines += fmt.Sprintf("  • `%s` Rs.%.0f (%s)\n", t.Symbol, t.GrossPnl, t.Strategy)
			}
		}
	}

	// Notes
	notes := ""
	if wr >= 60 {
		notes = "\n📝 🎯 Strong day — strategy aligned well"
	} else if wr < 40 {
		notes = "\n📝 ⚠️ Below target WR. Review SL and entry criteria"
	}
	if pnl > 0 {
		notes += "\n💪 Profitable day — lock in gains"
	}

	return fmt.Sprintf(
		"📅 *DAILY REPORT* : %s\n%s\n%s Net P&L: `Rs.%+.0f`\n💸 Est. Charges: `Rs.%.0f`\n📊 Day ROI: `%+.2f%%`\n\nTrades: `%d` (%dW / %dL)\n🎯 Win Rate: `%.1f%%`\n🧠 Regime: `%s`\nScans: `%d`%s%s%s",
		dateStr, sep, emoji, pnl, totalCharges, dayROI,
		total, wins, losses, wr, regime, totalScans,
		stratLines, topLines, notes)
}

// BuildWeeklyReport creates weekly summary
func BuildWeeklyReport(stats map[string]interface{}, from, to string, capital float64) string {
	pnl := toF64(stats["gross_pnl"])
	total := toInt(stats["total"])
	wins := toInt(stats["wins"])
	losses := toInt(stats["losses"])
	wr := toF64(stats["win_rate"])
	charges := toF64(stats["est_charges"])
	roi := 0.0
	if capital > 0 {
		roi = pnl / capital * 100
	}

	emoji := "📈"
	if pnl < 0 {
		emoji = "📉"
	}

	return fmt.Sprintf(
		"📅 *WEEKLY REPORT*\n%s → %s\n━━━━━━━━━━━━━━━━━━━━━━━━\n\n%s Net P&L: `Rs.%+.0f`\n💸 Est. Charges: `Rs.%.0f`\nTrades: `%d` (%dW / %dL)\n🎯 Win Rate: `%.1f%%`\n📊 ROI: `%+.2f%%`\n\n💼 Capital: `Rs.%.0f` → `Rs.%.0f`",
		from, to, emoji, pnl, charges, total, wins, losses, wr, roi,
		capital, capital+pnl)
}

// BuildMonthlyReport creates monthly summary
func BuildMonthlyReport(stats map[string]interface{}, from, to string, capital float64) string {
	pnl := toF64(stats["gross_pnl"])
	total := toInt(stats["total"])
	wins := toInt(stats["wins"])
	losses := toInt(stats["losses"])
	wr := toF64(stats["win_rate"])

	emoji := "📈"
	if pnl < 0 {
		emoji = "📉"
	}

	return fmt.Sprintf(
		"📊 *MONTHLY REPORT*\n%s → %s\n━━━━━━━━━━━━━━━━━━━━━━━━\n\n%s Net P&L: `Rs.%+.0f`\nTrades: `%d` (%dW / %dL)\n🎯 Win Rate: `%.1f%%`\n\n💼 Capital: `Rs.%.0f`\nClosing: `Rs.%.0f`",
		from, to, emoji, pnl, total, wins, losses, wr, capital, capital+pnl)
}

// SendTelegramDocument sends a file to Telegram
func SendTelegramDocument(filepath string, caption string) {
	if config.TelegramBotToken == "" || len(config.TelegramChatIDs) == 0 {
		log.Printf("[Report] Would send %s", filepath)
		return
	}

	for _, chatID := range config.TelegramChatIDs {
		go func(cid string) {
			body := &bytes.Buffer{}
			writer := multipart.NewWriter(body)
			writer.WriteField("chat_id", cid)
			writer.WriteField("caption", caption)
			writer.WriteField("parse_mode", "Markdown")

			part, err := writer.CreateFormFile("document", filepath)
			if err != nil {
				return
			}
			// Read file content
			file, err := http.Get("file://" + filepath)
			if err != nil {
				return
			}
			defer file.Body.Close()
			io.Copy(part, file.Body)
			writer.Close()

			url := fmt.Sprintf("https://api.telegram.org/bot%s/sendDocument", config.TelegramBotToken)
			resp, err := http.Post(url, writer.FormDataContentType(), body)
			if err == nil {
				resp.Body.Close()
			}
		}(chatID)
	}
}

type stratStat struct {
	Count int
	Wins  int
	PnL   float64
}

func max1(a int) int {
	if a < 1 {
		return 1
	}
	return a
}

func toF64(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case int:
		return float64(val)
	case int64:
		return float64(val)
	}
	return 0
}

func toInt(v interface{}) int {
	switch val := v.(type) {
	case int:
		return val
	case float64:
		return int(val)
	case int64:
		return int(val)
	}
	return 0
}

// Suppress unused import warnings
var (
	_ = strings.TrimSpace
	_ = time.Now
)
