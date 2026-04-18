package main

import (
	"log"
	"path/filepath"

	"bnf_go_engine/agents"
	"bnf_go_engine/config"
	"github.com/joho/godotenv"
)

func main() {
	absPath, _ := filepath.Abs("../../")
	config.BaseDir = absPath
	
	err := godotenv.Load(filepath.Join(absPath, ".env"))
	if err != nil {
		log.Printf("Failed to load .env: %v", err)
	}
	config.Reload()
	
	log.Println("Initializing DataAgent for backfill...")
	dataAgent := agents.NewDataAgent()
	if err := dataAgent.LoadUniverse(); err != nil {
		log.Fatalf("Failed to load universe: %v", err)
	}

	dbPath := filepath.Join(config.BaseDir, "data", "historical.db")
	log.Println("Starting 20-day historical backfill to capture missing intraday and daily data...")
	
	if err := dataAgent.SyncHistoricalCustom(dbPath, 20); err != nil {
		log.Fatalf("Error syncing: %v", err)
	}
	
	log.Println("Successfully backfilled 20 days!")
}
