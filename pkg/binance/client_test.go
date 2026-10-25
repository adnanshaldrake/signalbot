package binance

import (
	"adnan/binance-bot/pkg/config"
	"log"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestExchangeRulesDryRun(t *testing.T) {

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config error", "error", err)
		os.Exit(1)
	}
	slog.Info("Configuration loaded successfully")
	client := NewClient(cfg)

	// 1. Load public exchange rules into memory
	log.Println("Fetching exchange info from Binance...")
	if err := client.LoadExchangeInfo(); err != nil {
		t.Fatalf("Failed to load exchange info: %v", err)
	}
	log.Printf("Successfully loaded rules for %d symbols.", len(client.exchangeRules))

	// 2. Test symbols with various expected step sizes and precisions
	testCases := []struct {
		symbol string
		rawQty float64
	}{
		{"BTCUSDT", 0.12345678},
		{"NEARUSDT", 15.678912},
		{"ETHUSDT", 1.234567},
		{"DOGEUSDT", 1234.5678},
	}

	for _, tc := range testCases {
		formatted, err := client.FormatQuantity(tc.symbol, tc.rawQty)
		if err != nil {
			log.Printf("❌ [%s] Error formatting %f: %v", tc.symbol, tc.rawQty, err)
			continue
		}

		rule := client.exchangeRules[tc.symbol]
		log.Printf("[%s] Raw: %f -> Formatted: %s (StepSize: %f, Decimals: %d)",
			tc.symbol, tc.rawQty, formatted, rule.StepSize, rule.Decimals)
	}
}

func TestLiveBuyAndSellIntegration(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("Skipping integration test: %v", err)
	}

	if cfg.BinanceAPIKey == "" || cfg.BinanceAPISecret == "" || strings.HasPrefix(cfg.BinanceAPIKey, "YOUR_") {
		t.Skip("Skipping live integration test: BINANCE_API_KEY and BINANCE_API_SECRET are not set or using placeholders")
	}
	slog.Info("Configuration loaded successfully")
	client := NewClient(cfg)

	// 3. Sync Time to prevent -1021 timestamp drift
	if err := client.SyncTime(); err != nil {
		t.Fatalf("Failed to sync time with Testnet: %v", err)
	}

	// 4. Load Exchange Info for LOT_SIZE step sizes
	if err := client.LoadExchangeInfo(); err != nil {
		t.Fatalf("Failed to load exchange info: %v", err)
	}

	// Choose a liquid testnet pair (ensure your testnet account has test USDT)
	symbol := "BTCUSDT"
	testBudgetUSDT := 15.0 // $15 USDT test buy

	// --- STEP A: EXECUTE MARKET BUY ---
	t.Logf("Placing test Market BUY for %s with budget %.2f USDT...", symbol, testBudgetUSDT)

	buyResp, err := client.PlaceMarketOrder(symbol, Buy, testBudgetUSDT)
	if err != nil {
		t.Fatalf("Live BUY failed: %v", err)
	}
	t.Logf("BUY Success! OrderID: %d, ExecutedQty: %s", buyResp.OrderID, buyResp.ExecutedQty)

	executedQty, _ := strconv.ParseFloat(buyResp.ExecutedQty, 64)

	// Calculate base asset commission deductions
	var baseAssetCommission float64
	for _, fill := range buyResp.Fills {
		if fill.CommissionAsset != "USDT" && fill.CommissionAsset != "BNB" {
			comm, _ := strconv.ParseFloat(fill.Commission, 64)
			baseAssetCommission += comm
		}
	}
	netQuantity := executedQty - baseAssetCommission
	t.Logf("Net token quantity acquired (minus fees): %f", netQuantity)

	// Brief pause to let the testnet ledger settle
	time.Sleep(1 * time.Second)

	// --- STEP B: EXECUTE MARKET SELL ---
	t.Logf("Placing test Market SELL for %s using net quantity...", symbol)

	// This will invoke your dynamic FormatQuantity logic and use the 'quantity' parameter
	sellResp, err := client.PlaceMarketOrder(symbol, Sell, netQuantity)
	if err != nil {
		t.Fatalf("Live SELL failed (likely a LOT_SIZE or balance issue): %v", err)
	}

	t.Logf("SELL Success! OrderID: %d, ExecutedQty: %s", sellResp.OrderID, sellResp.ExecutedQty)
	t.Logf("Full Buy-Sell lifecycle test completed successfully without filter errors!")
}

func TestFormatQuantityEdgeCases(t *testing.T) {
	client := &Client{
		exchangeRules: map[string]SymbolFilter{
			"BTCUSDT":  {StepSize: 0.00001, MinQty: 0.0001, Decimals: 5},
			"NEARUSDT": {StepSize: 1.0, MinQty: 1.0, Decimals: 0},
		},
	}

	tests := []struct {
		symbol string
		input  float64
		want   string
	}{
		{"BTCUSDT", 0.12345678, "0.12345"}, // Truncates/floors to stepSize
		{"NEARUSDT", 15.89, "15"},          // Forces integer for whole-number tokens
	}

	for _, tc := range tests {
		got, err := client.FormatQuantity(tc.symbol, tc.input)
		if err != nil {
			t.Errorf("[%s] unexpected error: %v", tc.symbol, err)
		}
		if got != tc.want {
			t.Errorf("[%s] got %s, want %s", tc.symbol, got, tc.want)
		}
	}
}
