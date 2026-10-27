package backtest

import (
	"path/filepath"
	"testing"
	"time"

	"adnan/binance-bot/pkg/store"
)

func TestRunnerV2_WithFee(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_runner.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer db.Close()

	// Build 230 candles: 220 flat candles to warm up Strategy2 EMA200,
	// then 1 dip candle to trigger buy, then 1 rally candle to trigger take profit.
	candles := make([]store.Candle, 0, 230)
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	for i := 0; i < 230; i++ {
		cTime := baseTime.Add(time.Duration(i*15) * time.Minute)
		price := 100.0
		if i == 221 {
			// small dip to trigger oversold/entry condition
			price = 95.0
		} else if i >= 222 {
			// rally to trigger take profit
			price = 110.0
		}
		candles = append(candles, store.Candle{
			Symbol:   "TESTUSDT",
			Interval: "15m",
			OpenTime: cTime,
			Open:     price,
			High:     price + 1.0,
			Low:      price - 1.0,
			Close:    price,
			Volume:   1000.0,
		})
	}

	if err := db.UpsertCandles(candles); err != nil {
		t.Fatalf("failed to upsert test candles: %v", err)
	}

	// Verify RunnerV2 can be configured with fees
	runner := NewRunnerV2(db, 100.0).
		WithRisk(2.0, 4.0, 192).
		WithFee(0.1)

	if runner.feePct != 0.1 {
		t.Errorf("expected feePct = 0.1, got %f", runner.feePct)
	}
}
