package main

import (
	"fmt"
	"log"
	"os"
	"time"

	"bnf_go_engine/agents"
	"bnf_go_engine/config"
	"bnf_go_engine/storage"

	"github.com/joho/godotenv"
)

// Standalone test runner for the EOD Market Scanner.
// Loads real credentials, fetches real Kite historical data,
// runs the full scan, generates CSV, and sends via Telegram.

func main() {
	// Load environment
	envPaths := []string{"./.env"}
	bnfRoot := os.Getenv("ENGINE_ROOT")
	if bnfRoot != "" {
		envPaths = append([]string{bnfRoot + "/.env"}, envPaths...)
	}
	for _, p := range envPaths {
		if err := godotenv.Load(p); err == nil {
			log.Printf("[Test] Loaded .env from %s", p)
			break
		}
	}
	config.Reload()

	log.Println("═══════════════════════════════════════════")
	log.Println("  EOD MARKET SCANNER — LIVE TEST")
	log.Println("═══════════════════════════════════════════")

	if config.KiteAPIKey == "" || config.KiteAccessToken == "" {
		log.Fatal("[Test] FATAL: Kite API credentials not set in .env")
	}
	log.Printf("[Test] Kite API Key: %s...", config.KiteAPIKey[:6])
	log.Printf("[Test] Telegram Bot: %v configured", config.TelegramBotToken != "")
	log.Printf("[Test] Telegram Chats: %v", config.TelegramChatIDs)

	// Step 1: Load universe via DataAgent
	log.Println("\n[Test] ═══ STEP 1: Loading Universe ═══")
	dataAgent := agents.NewDataAgent()
	if err := dataAgent.LoadUniverse(); err != nil {
		log.Printf("[Test] WARNING: Universe load failed: %v — using fallback", err)
	}
	log.Printf("[Test] Live trading universe: %d stocks", len(dataAgent.Universe))

	// Step 2: Preload daily cache
	log.Println("\n[Test] ═══ STEP 2: Preloading Daily Cache ═══")
	dailyCache := storage.NewDailyCache()
	startCache := time.Now()

	// For testing, preload the existing universe first (to seed the cache)
	dailyCache.Preload(dataAgent.Universe)
	log.Printf("[Test] Daily cache preloaded in %.1f sec", time.Since(startCache).Seconds())

	// Step 3: Create scanner with cache
	scanner := agents.NewScannerAgent()
	scanner.Universe = dataAgent.Universe
	scanner.TokenToCompany = dataAgent.TokenToCompany
	scanner.DailyCache = dailyCache.ToScannerCache()
	scanner.FundamentalPassed = make(map[string]bool)

	// Step 4: Run the EOD scan
	log.Println("\n[Test] ═══ STEP 3: Running EOD Market Scan ═══")
	agents.SendTelegram("🧪 *TEST RUN* — EOD Market Scanner starting (manual test)")

	deps := agents.EODScanDeps{
		LoadUniverse:    dataAgent.LoadEODScanUniverse,
		PreloadCache:    dailyCache.Preload,
		GetScannerCache: func() *agents.DailyCache { return dailyCache.ToScannerCache() },
		GetLiveLTP:      nil, // No live ticks on Sunday
		GetLiveVolume:   nil, // No live ticks on Sunday
	}

	agents.RunEODMarketScan(deps, scanner)

	fmt.Println("\n═══════════════════════════════════════════")
	fmt.Println("  TEST COMPLETE — Check Telegram!")
	fmt.Println("═══════════════════════════════════════════")

	// Wait for background Telegram goroutines to finish sending
	time.Sleep(3 * time.Second)
}
