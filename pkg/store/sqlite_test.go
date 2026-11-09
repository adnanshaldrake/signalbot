package store

import (
	"math"
	"path/filepath"
	"testing"
	"time"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func TestCandles_UpsertAndQuery(t *testing.T) {
	db := newTestDB(t)

	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := []Candle{
		{
			Symbol:   "BTCUSDT",
			Interval: "15m",
			OpenTime: baseTime,
			Open:     100.0,
			High:     105.0,
			Low:      98.0,
			Close:    102.0,
			Volume:   50.0,
		},
		{
			Symbol:   "BTCUSDT",
			Interval: "15m",
			OpenTime: baseTime.Add(15 * time.Minute),
			Open:     102.0,
			High:     108.0,
			Low:      101.0,
			Close:    107.0,
			Volume:   60.0,
		},
		{
			Symbol:   "BTCUSDT",
			Interval: "15m",
			OpenTime: baseTime.Add(30 * time.Minute),
			Open:     107.0,
			High:     110.0,
			Low:      106.0,
			Close:    109.0,
			Volume:   70.0,
		},
	}

	// 1. Upsert
	if err := db.UpsertCandles(candles); err != nil {
		t.Fatalf("failed to upsert candles: %v", err)
	}

	// 2. Count
	count, err := db.CountCandles("BTCUSDT", "15m")
	if err != nil || count != 3 {
		t.Errorf("expected count 3, got count=%d, err=%v", count, err)
	}

	// 3. Get all candles
	got, err := db.GetCandles("BTCUSDT", "15m", time.Time{}, time.Time{})
	if err != nil || len(got) != 3 {
		t.Fatalf("expected 3 candles, got %d, err=%v", len(got), err)
	}
	if got[0].Close != 102.0 || got[2].Close != 109.0 {
		t.Errorf("candle content mismatch: got %+v", got)
	}

	// 4. Date range filter
	from := baseTime.Add(10 * time.Minute)
	to := baseTime.Add(20 * time.Minute)
	ranged, err := db.GetCandles("BTCUSDT", "15m", from, to)
	if err != nil || len(ranged) != 1 {
		t.Fatalf("expected 1 ranged candle, got %d, err=%v", len(ranged), err)
	}
	if ranged[0].OpenTime.UnixMilli() != baseTime.Add(15*time.Minute).UnixMilli() {
		t.Errorf("unexpected candle in range: %+v", ranged[0])
	}

	// 5. Candle range min/max
	minTime, maxTime, err := db.GetCandleRange("BTCUSDT", "15m")
	if err != nil {
		t.Fatalf("failed to get candle range: %v", err)
	}
	if minTime.UnixMilli() != baseTime.UnixMilli() {
		t.Errorf("minTime mismatch: got %v, want %v", minTime, baseTime)
	}
	if maxTime.UnixMilli() != baseTime.Add(30*time.Minute).UnixMilli() {
		t.Errorf("maxTime mismatch: got %v, want %v", maxTime, baseTime.Add(30*time.Minute))
	}

	// 6. List symbols
	symbols, err := db.ListSymbols()
	if err != nil || len(symbols) != 1 || symbols[0] != "BTCUSDT" {
		t.Errorf("expected [BTCUSDT], got symbols=%v, err=%v", symbols, err)
	}

	// 7. Upsert replacement
	updated := []Candle{
		{
			Symbol:   "BTCUSDT",
			Interval: "15m",
			OpenTime: baseTime,
			Open:     100.0,
			High:     105.0,
			Low:      98.0,
			Close:    999.0, // updated close
			Volume:   50.0,
		},
	}
	if err := db.UpsertCandles(updated); err != nil {
		t.Fatalf("failed to update candle: %v", err)
	}
	afterUpdate, err := db.GetCandles("BTCUSDT", "15m", baseTime, baseTime)
	if err != nil || len(afterUpdate) != 1 || afterUpdate[0].Close != 999.0 {
		t.Errorf("expected close 999.0 after replacement, got %+v", afterUpdate)
	}
}

func TestPositions_Lifecycle(t *testing.T) {
	db := newTestDB(t)

	// Verify empty state
	count, err := db.CountPositions()
	if err != nil || count != 0 {
		t.Fatalf("expected 0 positions, got %d, err=%v", count, err)
	}

	has, err := db.HasPosition("BTCUSDT")
	if err != nil || has {
		t.Fatalf("expected HasPosition to be false, got has=%v, err=%v", has, err)
	}

	now := time.Now().Truncate(time.Millisecond)
	pos := StoredPosition{
		Symbol:        "BTCUSDT",
		Side:          "BUY",
		EntryPrice:    50000.0,
		Quantity:      0.002,
		QuoteSpent:    100.0,
		OrderID:       123456789,
		OpenedAt:      now,
		EntryRSI:      25.5,
		EntryBBWidth:  0.02,
		EntryScore:    0.8,
		EntryVolRatio: 1.2,
	}

	// 1. Upsert
	if err := db.UpsertPosition(pos); err != nil {
		t.Fatalf("failed to upsert position: %v", err)
	}

	// 2. HasPosition & Count
	has, err = db.HasPosition("BTCUSDT")
	if err != nil || !has {
		t.Errorf("expected HasPosition to be true, got %v", has)
	}
	count, err = db.CountPositions()
	if err != nil || count != 1 {
		t.Errorf("expected count 1, got %d", count)
	}

	// 3. GetPosition
	p, err := db.GetPosition("BTCUSDT")
	if err != nil || p == nil {
		t.Fatalf("failed to get position: %v", err)
	}
	if p.Symbol != "BTCUSDT" || p.EntryPrice != 50000.0 || p.EntryRSI != 25.5 || p.EntryScore != 0.8 {
		t.Errorf("position field mismatch: %+v", p)
	}

	// 4. GetAllPositions
	all, err := db.GetAllPositions()
	if err != nil || len(all) != 1 {
		t.Errorf("expected 1 position from GetAllPositions, got %d", len(all))
	}

	// 5. DeletePosition
	if err := db.DeletePosition("BTCUSDT"); err != nil {
		t.Fatalf("failed to delete position: %v", err)
	}
	hasAfter, err := db.HasPosition("BTCUSDT")
	if err != nil || hasAfter {
		t.Errorf("expected HasPosition to be false after deletion, got %v", hasAfter)
	}
	pAfter, err := db.GetPosition("BTCUSDT")
	if err != nil || pAfter != nil {
		t.Errorf("expected nil position after deletion, got %v", pAfter)
	}
}

func TestTrades_InsertAndQuery(t *testing.T) {
	db := newTestDB(t)

	now := time.Now().Truncate(time.Millisecond)
	trade1 := Trade{
		OrderID:         111,
		Symbol:          "BTCUSDT",
		Side:            "BUY",
		EntryPrice:      50000.0,
		ExitPrice:       52000.0,
		Quantity:        0.002,
		QuoteSpent:      100.0,
		PnL:             3.8,
		PnLPct:          3.8,
		ExitReason:      "TAKE_PROFIT",
		EntryRSI:        25.0,
		EntryBBWidth:    0.02,
		EntryScore:      0.8,
		EntryVolRatio:   1.2,
		HoldDurationSec: 3600,
		OpenedAt:        now.Add(-2 * time.Hour),
		ClosedAt:        now.Add(-1 * time.Hour),
	}

	trade2 := Trade{
		OrderID:         222,
		Symbol:          "ETHUSDT",
		Side:            "BUY",
		EntryPrice:      3000.0,
		ExitPrice:       2950.0,
		Quantity:        0.02,
		QuoteSpent:      60.0,
		PnL:             -1.2,
		PnLPct:          -2.0,
		ExitReason:      "STOP_LOSS",
		HoldDurationSec: 1800,
		OpenedAt:        now.Add(-40 * time.Minute),
		ClosedAt:        now.Add(-10 * time.Minute),
	}

	if err := db.InsertTrade(trade1); err != nil {
		t.Fatalf("failed to insert trade 1: %v", err)
	}
	if err := db.InsertTrade(trade2); err != nil {
		t.Fatalf("failed to insert trade 2: %v", err)
	}

	// 1. GetAllTrades
	all, err := db.GetAllTrades()
	if err != nil || len(all) != 2 {
		t.Fatalf("expected 2 trades, got %d, err=%v", len(all), err)
	}
	// Ordered ascending by closed_at -> trade1 then trade2
	if all[0].Symbol != "BTCUSDT" || all[1].Symbol != "ETHUSDT" {
		t.Errorf("trades not in ascending order: %+v", all)
	}

	// 2. GetTrades limit (recent descending)
	recent, err := db.GetTrades(1)
	if err != nil || len(recent) != 1 {
		t.Fatalf("expected 1 recent trade, got %d", len(recent))
	}
	if recent[0].Symbol != "ETHUSDT" {
		t.Errorf("expected most recent trade to be ETHUSDT, got %s", recent[0].Symbol)
	}

	// 3. GetDailyPnL
	// Since 3 hours ago -> includes trade1 (3.8) and trade2 (-1.2) = 2.6
	totalPnL, err := db.GetDailyPnL(now.Add(-3 * time.Hour))
	if err != nil {
		t.Fatalf("failed to calculate daily PnL: %v", err)
	}
	if math.Abs(totalPnL-2.6) > 1e-6 {
		t.Errorf("expected total PnL 2.6, got %f", totalPnL)
	}

	// Since 30 minutes ago -> includes only trade2 (-1.2)
	recentPnL, err := db.GetDailyPnL(now.Add(-30 * time.Minute))
	if err != nil {
		t.Fatalf("failed to calculate recent PnL: %v", err)
	}
	if math.Abs(recentPnL - -1.2) > 1e-6 {
		t.Errorf("expected recent PnL -1.2, got %f", recentPnL)
	}
}

