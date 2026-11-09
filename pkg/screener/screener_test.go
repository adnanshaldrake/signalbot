package screener_test

import (
	"errors"
	"testing"

	"github.com/adnanshaldrake/signalbot/pkg/config"
	"github.com/adnanshaldrake/signalbot/pkg/screener"
	"github.com/adnanshaldrake/signalbot/pkg/store"
)

// mockCustomScreener implements screener.Screener for testing.
type mockCustomScreener struct {
	symbols []string
	err     error
}

func (m *mockCustomScreener) Screen() ([]string, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.symbols, nil
}

func TestScreenerInterfaceCompliance(t *testing.T) {
	// 1. Verify StaticScreener satisfies Screener
	var _ screener.Screener = screener.NewStaticScreener([]string{"BTCUSDT", "ETHUSDT"})

	// 2. Verify VolumeBacktestScreener satisfies Screener
	var _ screener.Screener = screener.NewVolumeBacktestScreener(&config.BotConfig{}, &store.DB{})

	// 3. Test StaticScreener
	expected := []string{"BTCUSDT", "SOLUSDT"}
	staticScr := screener.NewStaticScreener(expected)
	got, err := staticScr.Screen()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(expected) {
		t.Fatalf("expected %d symbols, got %d", len(expected), len(got))
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("expected %s at index %d, got %s", expected[i], i, got[i])
		}
	}

	// 4. Test custom mock screener
	custom := &mockCustomScreener{symbols: []string{"NEARUSDT"}}
	symbols, err := custom.Screen()
	if err != nil || len(symbols) != 1 || symbols[0] != "NEARUSDT" {
		t.Errorf("custom screener failed, got symbols: %v, err: %v", symbols, err)
	}

	// 5. Test custom mock screener returning error
	customErr := &mockCustomScreener{err: errors.New("screener network failure")}
	_, err = customErr.Screen()
	if err == nil {
		t.Errorf("expected error from customErr, got nil")
	}
}

func TestFilterCandidateTickers_VolumeBoundariesAndTopN(t *testing.T) {
	tickers := []screener.TickerCandidate{
		{Symbol: "BTCUSDT", QuoteVolume: 100_000_000},
		{Symbol: "ETHUSDT", QuoteVolume: 50_000_000},
		{Symbol: "SOLUSDT", QuoteVolume: 10_000_000},
		{Symbol: "NEARUSDT", QuoteVolume: 2_000_000},
		{Symbol: "TINYUSDT", QuoteVolume: 100_000},
		{Symbol: "BTCUPUSDT", QuoteVolume: 5_000_000},   // leveraged -> exclude
		{Symbol: "ETHBEARUSDT", QuoteVolume: 3_000_000}, // leveraged -> exclude
		{Symbol: "BTCFDUSD", QuoteVolume: 20_000_000},   // non-USDT -> exclude
	}

	// Filter: volume between 1M and 60M, top 2
	got := screener.FilterCandidateTickers(tickers, nil, 2, 1_000_000, 60_000_000)

	// Expected candidates in range: ETHUSDT (50M), SOLUSDT (10M), NEARUSDT (2M)
	// Top 2: ETHUSDT, SOLUSDT
	if len(got) != 2 {
		t.Fatalf("expected 2 symbols, got %d (%v)", len(got), got)
	}
	if got[0] != "ETHUSDT" || got[1] != "SOLUSDT" {
		t.Errorf("expected [ETHUSDT, SOLUSDT], got %v", got)
	}
}

func TestFilterCandidateTickers_ValidMapFilter(t *testing.T) {
	tickers := []screener.TickerCandidate{
		{Symbol: "BTCUSDT", QuoteVolume: 10_000_000},
		{Symbol: "DELISTEDUSDT", QuoteVolume: 9_000_000},
	}
	validMap := map[string]bool{
		"BTCUSDT": true,
	}

	got := screener.FilterCandidateTickers(tickers, validMap, 0, 0, 0)
	if len(got) != 1 || got[0] != "BTCUSDT" {
		t.Errorf("expected only BTCUSDT, got %v", got)
	}
}

func TestFilterAndRankResults_StrictThresholds(t *testing.T) {
	cfg := &config.BotConfig{
		ScreenMinTrades:  3,
		ScreenMinWinRate: 50.0,
		ScreenMinPnL:     5.0,
		MaxWatchlistSize: 2,
	}

	results := []screener.Result{
		{Symbol: "PERFECT", Trades: 10, WinRate: 70.0, PnLUSDT: 50.0},
		{Symbol: "GOOD", Trades: 5, WinRate: 60.0, PnLUSDT: 20.0},
		{Symbol: "LOW_TRADES", Trades: 2, WinRate: 100.0, PnLUSDT: 30.0},
		{Symbol: "LOW_WINRATE", Trades: 10, WinRate: 40.0, PnLUSDT: 15.0},
		{Symbol: "LOW_PNL", Trades: 8, WinRate: 60.0, PnLUSDT: 2.0},
	}

	got := screener.FilterAndRankResults(results, cfg)

	if len(got) != 2 {
		t.Fatalf("expected 2 symbols capped by MaxWatchlistSize, got %d (%v)", len(got), got)
	}
	if got[0] != "PERFECT" || got[1] != "GOOD" {
		t.Errorf("expected [PERFECT, GOOD], got %v", got)
	}
}

func TestFilterAndRankResults_FallbackToPositivePnL(t *testing.T) {
	cfg := &config.BotConfig{
		ScreenMinTrades:  10,
		ScreenMinWinRate: 80.0, // strict: none pass
		ScreenMinPnL:     100.0,
	}

	results := []screener.Result{
		{Symbol: "POS1", Trades: 2, WinRate: 50.0, PnLUSDT: 10.0},
		{Symbol: "POS2", Trades: 1, WinRate: 50.0, PnLUSDT: 5.0},
		{Symbol: "NEG", Trades: 5, WinRate: 20.0, PnLUSDT: -20.0},
	}

	got := screener.FilterAndRankResults(results, cfg)

	// Fallback picks up to 3 positive PnL performers
	if len(got) != 2 {
		t.Fatalf("expected 2 positive performers in fallback, got %d (%v)", len(got), got)
	}
	if got[0] != "POS1" || got[1] != "POS2" {
		t.Errorf("expected [POS1, POS2], got %v", got)
	}
}

func TestFilterAndRankResults_EmptyWhenAllNegative(t *testing.T) {
	cfg := &config.BotConfig{
		ScreenMinTrades:  5,
		ScreenMinWinRate: 50.0,
		ScreenMinPnL:     10.0,
	}

	results := []screener.Result{
		{Symbol: "LOSS1", Trades: 5, WinRate: 20.0, PnLUSDT: -10.0},
		{Symbol: "LOSS2", Trades: 10, WinRate: 0.0, PnLUSDT: -50.0},
	}

	got := screener.FilterAndRankResults(results, cfg)
	if len(got) != 0 {
		t.Errorf("expected empty watchlist when all symbols lose money, got %v", got)
	}
}
