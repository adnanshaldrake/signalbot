package risk

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/adnanshaldrake/signalbot/pkg/config"
	"github.com/adnanshaldrake/signalbot/pkg/state"
)

func TestCalcPnL(t *testing.T) {
	// Bought 100 USDT at 10.0 → 10 units; 0.1% fee taken in base → net 9.99 held.
	pos := &state.Position{EntryPrice: 10, Quantity: 9.99, QuoteSpent: 100}

	tests := []struct {
		name  string
		exit  float64
		fee   float64
		wantP float64
	}{
		// flat price still loses buy + sell fees
		{"flat price loses fees", 10, 0.1, 9.99*10*0.999 - 100},
		{"take profit 4%", 10.4, 0.1, 9.99*10.4*0.999 - 100},
		{"no fee, net qty only", 10, 0, 9.99*10 - 100},
	}
	for _, tc := range tests {
		got := CalcPnL(pos, tc.exit, tc.fee)
		if math.Abs(got-tc.wantP) > 1e-9 {
			t.Errorf("%s: got %.6f want %.6f", tc.name, got, tc.wantP)
		}
	}

	if got := CalcPnL(pos, 10, 0.1); got >= 0 {
		t.Errorf("flat exit should be a net loss, got %.6f", got)
	}
}

func TestCheckStopLoss(t *testing.T) {
	cfg := &config.BotConfig{
		StopLossPct: 2.0, // 2% stop-loss
	}
	mgr := NewManager(cfg, state.NewMemoryManager())

	pos := &state.Position{
		Symbol:     "BTCUSDT",
		Side:       "BUY",
		EntryPrice: 100.0,
	}

	tests := []struct {
		name         string
		currentPrice float64
		wantHit      bool
	}{
		{"above stop-loss", 98.5, false},
		{"exact stop-loss price", 98.0, true},
		{"below stop-loss price", 97.5, true},
		{"in profit", 105.0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mgr.CheckStopLoss(pos, tc.currentPrice)
			if got != tc.wantHit {
				t.Errorf("CheckStopLoss(price=%.2f) = %v; want %v", tc.currentPrice, got, tc.wantHit)
			}
		})
	}

	// Verify non-BUY position returns false
	sellPos := &state.Position{
		Symbol:     "BTCUSDT",
		Side:       "SELL",
		EntryPrice: 100.0,
	}
	if mgr.CheckStopLoss(sellPos, 90.0) {
		t.Errorf("expected CheckStopLoss to return false for non-BUY position")
	}
}

func TestCheckTakeProfit(t *testing.T) {
	cfg := &config.BotConfig{
		TakeProfitPct: 4.0, // 4% take-profit
	}
	mgr := NewManager(cfg, state.NewMemoryManager())

	pos := &state.Position{
		Symbol:     "BTCUSDT",
		Side:       "BUY",
		EntryPrice: 100.0,
	}

	tests := []struct {
		name         string
		currentPrice float64
		wantHit      bool
	}{
		{"below take-profit", 103.5, false},
		{"exact take-profit price", 104.0, true},
		{"above take-profit price", 105.0, true},
		{"in loss", 95.0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mgr.CheckTakeProfit(pos, tc.currentPrice)
			if got != tc.wantHit {
				t.Errorf("CheckTakeProfit(price=%.2f) = %v; want %v", tc.currentPrice, got, tc.wantHit)
			}
		})
	}

	// Verify non-BUY position returns false
	sellPos := &state.Position{
		Symbol:     "BTCUSDT",
		Side:       "SELL",
		EntryPrice: 100.0,
	}
	if mgr.CheckTakeProfit(sellPos, 110.0) {
		t.Errorf("expected CheckTakeProfit to return false for non-BUY position")
	}
}

func TestCheckEntry_MaxPositionsAndDuplicates(t *testing.T) {
	ctx := context.Background()
	cfg := &config.BotConfig{
		MaxPositions:    2,
		MaxPositionUSDT: 50.0,
		MaxDailyLossPct: 10.0,
	}
	mem := state.NewMemoryManager()
	mgr := NewManager(cfg, mem)

	// 1. Initial check - allowed
	d := mgr.CheckEntry(ctx, "BTCUSDT", 50000.0, 0)
	if !d.Allowed {
		t.Fatalf("expected BTCUSDT entry to be allowed, got denied: %s", d.Reason)
	}
	if d.Quantity != 50.0 {
		t.Errorf("expected quantity 50.0, got %f", d.Quantity)
	}

	// Open BTCUSDT
	_ = mem.OpenPosition(ctx, state.Position{
		Symbol:     "BTCUSDT",
		Side:       "BUY",
		EntryPrice: 50000.0,
		Quantity:   0.001,
		OpenedAt:   time.Now(),
	})

	// 2. Duplicate symbol check - denied
	dDup := mgr.CheckEntry(ctx, "BTCUSDT", 50000.0, 0)
	if dDup.Allowed {
		t.Errorf("expected duplicate BTCUSDT entry to be denied")
	}

	// 3. Second symbol - allowed
	d2 := mgr.CheckEntry(ctx, "ETHUSDT", 3000.0, 0)
	if !d2.Allowed {
		t.Fatalf("expected ETHUSDT entry to be allowed, got denied: %s", d2.Reason)
	}

	// Open ETHUSDT
	_ = mem.OpenPosition(ctx, state.Position{
		Symbol:     "ETHUSDT",
		Side:       "BUY",
		EntryPrice: 3000.0,
		Quantity:   0.01,
		OpenedAt:   time.Now(),
	})

	// 4. Max positions reached (2/2) - denied
	d3 := mgr.CheckEntry(ctx, "SOLUSDT", 150.0, 0)
	if d3.Allowed {
		t.Errorf("expected SOLUSDT entry to be denied when max positions reached")
	}
}

func TestCheckEntry_DailyLossKillSwitch(t *testing.T) {
	ctx := context.Background()
	cfg := &config.BotConfig{
		MaxPositions:    2,
		MaxPositionUSDT: 50.0, // Total capacity = 100 USDT
		MaxDailyLossPct: 5.0,  // Kill-switch triggers at >= 5% loss (>= $5 loss)
	}
	mem := state.NewMemoryManager()
	mgr := NewManager(cfg, mem)

	// Profit or zero loss -> Allowed
	d := mgr.CheckEntry(ctx, "BTCUSDT", 50000.0, 0)
	if !d.Allowed {
		t.Fatalf("expected allowed when P&L is 0, got denied: %s", d.Reason)
	}

	// Simulate $3 loss (3% of capacity, below 5% threshold) -> Allowed
	_ = mem.OpenPosition(ctx, state.Position{Symbol: "TMP1"})
	_ = mem.ClosePosition(ctx, "TMP1", -3.0)

	dLossSmall := mgr.CheckEntry(ctx, "BTCUSDT", 50000.0, 0)
	if !dLossSmall.Allowed {
		t.Fatalf("expected allowed when loss is below threshold, got: %s", dLossSmall.Reason)
	}

	// Simulate additional $2 loss (total -5.0 = 5% loss, hits threshold) -> Denied
	_ = mem.OpenPosition(ctx, state.Position{Symbol: "TMP2"})
	_ = mem.ClosePosition(ctx, "TMP2", -2.0)

	dKillSwitch := mgr.CheckEntry(ctx, "BTCUSDT", 50000.0, 0)
	if dKillSwitch.Allowed {
		t.Errorf("expected daily loss kill-switch to trigger at >= 5%% loss")
	}
}

func TestCheckEntry_PositionSizing(t *testing.T) {
	ctx := context.Background()
	cfg := &config.BotConfig{
		MaxPositions:    5,
		MaxPositionUSDT: 80.0,
		MaxDailyLossPct: 10.0,
	}
	mgr := NewManager(cfg, state.NewMemoryManager())

	// Default size
	dDefault := mgr.CheckEntry(ctx, "BTCUSDT", 50000.0, 0)
	if dDefault.Quantity != 80.0 {
		t.Errorf("expected default quantity 80.0, got %f", dDefault.Quantity)
	}

	// Override size
	dOverride := mgr.CheckEntry(ctx, "BTCUSDT", 50000.0, 150.0)
	if dOverride.Quantity != 150.0 {
		t.Errorf("expected override quantity 150.0, got %f", dOverride.Quantity)
	}
}
