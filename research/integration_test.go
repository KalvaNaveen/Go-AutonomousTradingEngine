package research

import (
	"testing"
)

// ══════════════════════════════════════════════════════════════
//  REAL DATA INTEGRATION TESTS — hits actual websites
// ══════════════════════════════════════════════════════════════

func TestScreener_RealStock_RELIANCE(t *testing.T) {
	// Hit real Screener.in for RELIANCE
	f, err := FetchFundamentals("RELIANCE")
	if err != nil {
		t.Fatalf("Screener.in fetch failed: %v", err)
	}

	t.Logf("RELIANCE: MCap=%.0f Cr, ROCE=%.1f%%, ROE=%.1f%%, Sales=%.1f%%, Profit=%.1f%%, Passed=%v",
		f.MarketCap, f.ROCE, f.ROE, f.SalesGrowth, f.ProfitGrowth, f.Passed)

	// RELIANCE should have Market Cap > 1000 Cr (it's ~18 lakh Cr)
	if f.MarketCap < 1000 {
		t.Errorf("RELIANCE MarketCap should be > 1000 Cr, got %.0f", f.MarketCap)
	}

	// ROCE and ROE should be parsed (non-zero)
	if f.ROCE == 0 && f.ROE == 0 {
		t.Error("Both ROCE and ROE are 0 — scraper might be broken")
	}
}

func TestScreener_RealStock_TCS(t *testing.T) {
	f, err := FetchFundamentals("TCS")
	if err != nil {
		t.Fatalf("Screener.in fetch failed: %v", err)
	}

	t.Logf("TCS: MCap=%.0f Cr, ROCE=%.1f%%, ROE=%.1f%%, Sales=%.1f%%, Profit=%.1f%%, Passed=%v",
		f.MarketCap, f.ROCE, f.ROE, f.SalesGrowth, f.ProfitGrowth, f.Passed)

	if f.MarketCap < 1000 {
		t.Errorf("TCS MarketCap should be > 1000 Cr, got %.0f", f.MarketCap)
	}
}

func TestScreener_SmallUnknownStock(t *testing.T) {
	// Test with a stock that likely doesn't exist
	_, err := FetchFundamentals("ZZZZZZNOTASTOCK")
	if err == nil {
		t.Log("Unexpected success for fake stock — screener may have redirected")
	} else {
		t.Logf("Correctly failed for fake stock: %v", err)
	}
}

func TestChittorgarh_RealIPOScrape(t *testing.T) {
	entries, err := FetchRecentIPOs()
	if err != nil {
		t.Fatalf("Chittorgarh scrape failed: %v", err)
	}

	t.Logf("Scraped %d total IPO entries", len(entries))

	if len(entries) == 0 {
		t.Error("Got 0 IPO entries — scraper might be broken")
	}

	// Print first 5 entries
	for i, e := range entries {
		if i >= 5 {
			break
		}
		t.Logf("  IPO %d: %s (%s) listed %s gain=%s",
			i+1, e.CompanyName, e.Symbol, e.ListingDate, e.ListingGain)
	}

	// Test filtering for recent IPOs (last 90 days)
	recent := FilterRecentIPOs(entries, 90)
	t.Logf("Recent IPOs (last 90 days): %d", len(recent))
}

func TestGoldRatio_WithSyntheticData(t *testing.T) {
	// Can't hit Kite without credentials, but verify the math works
	nifty := make([]float64, 260)
	gold := make([]float64, 260)
	for i := range nifty {
		nifty[i] = 22000 + float64(i)*10
		gold[i] = 50 + float64(i)*0.1
	}

	result := ComputeNiftyGoldRatio(nifty, gold)
	if result == nil {
		t.Fatal("Gold ratio returned nil")
	}

	t.Logf("Gold Ratio: %.2f (%.1f%% in channel) Signal=%s",
		result.CurrentRatio, result.Percentile, result.Signal)

	if result.CurrentRatio <= 0 {
		t.Error("Ratio should be positive")
	}
	if result.Signal == "" {
		t.Error("Signal should not be empty")
	}
}
