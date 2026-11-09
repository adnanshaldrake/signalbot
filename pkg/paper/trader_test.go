package paper

import (
	"context"
	"math"
	"testing"

	"github.com/adnanshaldrake/signalbot/pkg/binance"
	"github.com/adnanshaldrake/signalbot/pkg/config"
	"github.com/adnanshaldrake/signalbot/pkg/state"
)

func TestPlaceMarketOrder_BuyAndSellLifecycle(t *testing.T) {
	ctx := context.Background()
	cfg := &config.BotConfig{
		TakerFeePct: 0.1, // 0.1% fee
	}
	mem := state.NewMemoryManager()
	trader := NewTrader(cfg, mem)

	// --- 1. Place BUY Order ---
	entryPrice := 50000.0
	quoteSpent := 100.0
	resp, err := trader.PlaceMarketOrder(ctx, "BTCUSDT", binance.Buy, entryPrice, quoteSpent)
	if err != nil {
		t.Fatalf("unexpected error on paper buy: %v", err)
	}

	if resp.Symbol != "BTCUSDT" || resp.Status != "FILLED" || resp.Side != "BUY" {
		t.Errorf("unexpected order response: %+v", resp)
	}

	// Verify position in state manager
	pos, err := mem.GetPosition(ctx, "BTCUSDT")
	if err != nil || pos == nil {
		t.Fatalf("expected position in state, got pos=%v, err=%v", pos, err)
	}

	baseQty := quoteSpent / entryPrice // 0.002
	wantNetQty := baseQty * (1 - 0.1/100)
	if math.Abs(pos.Quantity-wantNetQty) > 1e-9 {
		t.Errorf("expected net quantity %f, got %f", wantNetQty, pos.Quantity)
	}
	if pos.QuoteSpent != quoteSpent {
		t.Errorf("expected quoteSpent %f, got %f", quoteSpent, pos.QuoteSpent)
	}

	// Check virtual balance (1000 - 100 quoteSpent = 900)
	bal, err := trader.GetVirtualBalance(ctx)
	if err != nil {
		t.Fatalf("unexpected balance error: %v", err)
	}
	if math.Abs(bal-900.0) > 1e-6 {
		t.Errorf("expected balance 900.0, got %f", bal)
	}

	// --- 2. Place SELL Order (Take Profit) ---
	exitPrice := 52000.0
	sellResp, err := trader.PlaceMarketOrderWithReason(ctx, "BTCUSDT", binance.Sell, exitPrice, 0, "take_profit")
	if err != nil {
		t.Fatalf("unexpected error on paper sell: %v", err)
	}
	if sellResp.Status != "FILLED" || sellResp.Side != "SELL" {
		t.Errorf("unexpected sell response: %+v", sellResp)
	}

	// Verify position is now closed
	has, err := mem.HasOpenPosition(ctx, "BTCUSDT")
	if err != nil || has {
		t.Errorf("expected position to be closed, got has=%v", has)
	}

	// Verify daily PnL is updated in state manager
	dailyPnL, err := mem.GetDailyPnL(ctx)
	if err != nil {
		t.Fatalf("unexpected dailyPnL error: %v", err)
	}
	// Proceeds = 0.001998 * 52000 * 0.999 = 103.79208
	// PnL = 103.79208 - 100 = 3.79208
	if dailyPnL <= 0 {
		t.Errorf("expected positive realised PnL on sell, got %f", dailyPnL)
	}
}

func TestPlaceBuyOrderWithIndicators(t *testing.T) {
	ctx := context.Background()
	cfg := &config.BotConfig{
		TakerFeePct: 0.1,
	}
	mem := state.NewMemoryManager()
	trader := NewTrader(cfg, mem)

	resp, err := trader.PlaceBuyOrderWithIndicators(
		ctx,
		"ETHUSDT",
		3000.0,
		60.0,
		24.5, // RSI
		0.03, // BBWidth
		0.85, // Score
		1.5,  // VolRatio
	)
	if err != nil {
		t.Fatalf("unexpected error on buy with indicators: %v", err)
	}
	if resp.Status != "FILLED" {
		t.Errorf("expected status FILLED, got %s", resp.Status)
	}

	pos, err := mem.GetPosition(ctx, "ETHUSDT")
	if err != nil || pos == nil {
		t.Fatalf("expected position in state, got %v", pos)
	}

	if pos.EntryRSI != 24.5 {
		t.Errorf("expected EntryRSI 24.5, got %f", pos.EntryRSI)
	}
	if pos.EntryBBWidth != 0.03 {
		t.Errorf("expected EntryBBWidth 0.03, got %f", pos.EntryBBWidth)
	}
	if pos.EntryScore != 0.85 {
		t.Errorf("expected EntryScore 0.85, got %f", pos.EntryScore)
	}
	if pos.EntryVolRatio != 1.5 {
		t.Errorf("expected EntryVolRatio 1.5, got %f", pos.EntryVolRatio)
	}
}

func TestPlaceMarketOrder_SellWithoutPosition(t *testing.T) {
	ctx := context.Background()
	cfg := &config.BotConfig{TakerFeePct: 0.1}
	trader := NewTrader(cfg, state.NewMemoryManager())

	_, err := trader.PlaceMarketOrder(ctx, "UNKNOWN", binance.Sell, 100.0, 0)
	if err == nil {
		t.Errorf("expected error when selling non-existent position, got nil")
	}
}

