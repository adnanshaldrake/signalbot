package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/adnanshaldrake/signalbot/pkg/store"
)

func TestSQLiteManager_PersistenceAcrossRuns(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test_state.db")

	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	defer db.Close()

	mgr := NewSQLiteManager(db)

	pos1 := Position{
		Symbol:        "BTCUSDT",
		Side:          "BUY",
		EntryPrice:    65000.0,
		Quantity:      0.001,
		QuoteSpent:    65.0,
		OrderID:       123456,
		OpenedAt:      time.Now().Add(-10 * time.Minute),
		EntryRSI:      28.5,
		EntryBBWidth:  0.035,
		EntryScore:    0.75,
		EntryVolRatio: 1.25,
	}
	pos2 := Position{
		Symbol:     "ETHUSDT",
		Side:       "BUY",
		EntryPrice: 3500.0,
		Quantity:   0.02,
		QuoteSpent: 70.0,
		OrderID:    123457,
		OpenedAt:   time.Now().Add(-5 * time.Minute),
	}

	// 1. Open positions
	if err := mgr.OpenPosition(ctx, pos1); err != nil {
		t.Fatalf("failed to open pos1: %v", err)
	}
	if err := mgr.OpenPosition(ctx, pos2); err != nil {
		t.Fatalf("failed to open pos2: %v", err)
	}

	// 2. Verify Count and Has
	count, err := mgr.CountOpenPositions(ctx)
	if err != nil || count != 2 {
		t.Fatalf("expected count 2, got %d, err: %v", count, err)
	}
	has, err := mgr.HasOpenPosition(ctx, "BTCUSDT")
	if err != nil || !has {
		t.Fatalf("expected has BTCUSDT true, got %v, err: %v", has, err)
	}

	// 3. Simulate bot restart by creating a new SQLiteManager on the same db
	restartMgr := NewSQLiteManager(db)

	resCount, err := restartMgr.CountOpenPositions(ctx)
	if err != nil || resCount != 2 {
		t.Fatalf("after restart: expected count 2, got %d, err: %v", resCount, err)
	}

	storedBTC, err := restartMgr.GetPosition(ctx, "BTCUSDT")
	if err != nil || storedBTC == nil {
		t.Fatalf("after restart: expected to find BTCUSDT position, err: %v", err)
	}
	if storedBTC.EntryPrice != 65000.0 || storedBTC.Quantity != 0.001 || storedBTC.EntryRSI != 28.5 {
		t.Errorf("after restart: corrupted position data: %+v", storedBTC)
	}

	// 4. Close BTCUSDT with profit
	pnl := 3.25
	exitPrice := 68250.0
	if err := restartMgr.ClosePositionWithDetails(ctx, "BTCUSDT", exitPrice, pnl, "TAKE_PROFIT"); err != nil {
		t.Fatalf("failed to close position: %v", err)
	}

	// Verify BTCUSDT is no longer in open positions
	hasBTC, _ := restartMgr.HasOpenPosition(ctx, "BTCUSDT")
	if hasBTC {
		t.Errorf("expected BTCUSDT to be closed, but still found in open positions")
	}

	// Verify ETHUSDT is still open
	hasETH, _ := restartMgr.HasOpenPosition(ctx, "ETHUSDT")
	if !hasETH {
		t.Errorf("expected ETHUSDT to still be open")
	}

	// Verify Daily PnL reflects the closed trade
	dailyPnL, err := restartMgr.GetDailyPnL(ctx)
	if err != nil {
		t.Fatalf("failed to get daily pnl: %v", err)
	}
	if dailyPnL != 3.25 {
		t.Errorf("expected daily PnL 3.25, got %f", dailyPnL)
	}

	// Verify trades table in DB contains the closed trade with indicators
	trades, err := db.GetAllTrades()
	if err != nil || len(trades) != 1 {
		t.Fatalf("expected 1 recorded trade, got %d, err: %v", len(trades), err)
	}
	t0 := trades[0]
	if t0.Symbol != "BTCUSDT" || t0.PnL != 3.25 || t0.ExitReason != "TAKE_PROFIT" || t0.ExitPrice != exitPrice || t0.EntryRSI != 28.5 || t0.HoldDurationSec <= 0 {
		t.Errorf("unexpected recorded trade details: %+v", t0)
	}
}

func TestNewStateManager_Selection(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_selection.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	defer db.Close()

	// When db is provided, must return SQLiteManager
	mgr, err := NewStateManager(db)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := mgr.(*SQLiteManager); !ok {
		t.Errorf("expected *SQLiteManager, got %T", mgr)
	}

	// When db is nil, falls back to MemoryManager
	memMgr, err := NewStateManager(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := memMgr.(*MemoryManager); !ok {
		t.Errorf("expected *MemoryManager, got %T", memMgr)
	}
}
