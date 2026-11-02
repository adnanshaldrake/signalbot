package risk

import (
	"math"
	"testing"
	"time"

	"adnan/binance-bot/pkg/config"
	"adnan/binance-bot/pkg/state"
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

func TestCheckMaxHold(t *testing.T) {
	cfg := &config.BotConfig{
		MaxHoldHours: 20.0,
	}
	mgr := NewManager(cfg, nil)

	openedAt := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	pos := &state.Position{
		Symbol:   "BTCUSDT",
		OpenedAt: openedAt,
	}

	// 1. Within max hold duration (15 hours held < 20 hours limit)
	nowBefore := openedAt.Add(15 * time.Hour)
	if mgr.CheckMaxHold(pos, nowBefore) {
		t.Errorf("expected CheckMaxHold to return false at 15h, got true")
	}

	// 2. Exactly at max hold duration (20 hours held == 20 hours limit)
	nowExact := openedAt.Add(20 * time.Hour)
	if !mgr.CheckMaxHold(pos, nowExact) {
		t.Errorf("expected CheckMaxHold to return true at 20h, got false")
	}

	// 3. Exceeded max hold duration (25 hours held > 20 hours limit)
	nowAfter := openedAt.Add(25 * time.Hour)
	if !mgr.CheckMaxHold(pos, nowAfter) {
		t.Errorf("expected CheckMaxHold to return true at 25h, got false")
	}

	// 4. Disabled when MaxHoldHours <= 0
	cfgDisabled := &config.BotConfig{MaxHoldHours: 0}
	mgrDisabled := NewManager(cfgDisabled, nil)
	if mgrDisabled.CheckMaxHold(pos, nowAfter) {
		t.Errorf("expected CheckMaxHold to return false when MaxHoldHours=0, got true")
	}

	// 5. Uninitialized OpenedAt should not trigger
	posEmpty := &state.Position{Symbol: "BTCUSDT"}
	if mgr.CheckMaxHold(posEmpty, nowAfter) {
		t.Errorf("expected CheckMaxHold to return false for uninitialized OpenedAt, got true")
	}
}

func TestCheckStopLossAndTakeProfit(t *testing.T) {
	cfg := &config.BotConfig{
		StopLossPct:   2.0,
		TakeProfitPct: 4.0,
	}
	mgr := NewManager(cfg, nil)

	pos := &state.Position{
		Symbol:     "ETHUSDT",
		Side:       "BUY",
		EntryPrice: 100.0,
	}

	// Stop price is 100 * (1 - 0.02) = 98.0
	// Target price is 100 * (1 + 0.04) = 104.0

	// Normal price inside bounds (99.0)
	if mgr.CheckStopLoss(pos, 99.0) {
		t.Errorf("did not expect stop loss at 99.0")
	}
	if mgr.CheckTakeProfit(pos, 99.0) {
		t.Errorf("did not expect take profit at 99.0")
	}

	// Hit stop loss at 98.0
	if !mgr.CheckStopLoss(pos, 98.0) {
		t.Errorf("expected stop loss to trigger at 98.0")
	}
	// Below stop loss at 97.5
	if !mgr.CheckStopLoss(pos, 97.5) {
		t.Errorf("expected stop loss to trigger at 97.5")
	}

	// Hit take profit at 104.0
	if !mgr.CheckTakeProfit(pos, 104.0) {
		t.Errorf("expected take profit to trigger at 104.0")
	}
	// Above take profit at 105.0
	if !mgr.CheckTakeProfit(pos, 105.0) {
		t.Errorf("expected take profit to trigger at 105.0")
	}
}
