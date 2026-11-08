package backtest

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/adnanshaldrake/signalbot/pkg/store"
	"github.com/adnanshaldrake/signalbot/pkg/strategy"
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

type customEvaluator struct {
	enterAt int
	exitAt  int
}

func (c *customEvaluator) ShouldEnter(i int) bool {
	return i == c.enterAt
}

func (c *customEvaluator) ShouldExit(i int) bool {
	return i == c.exitAt
}

func TestRunnerV2_WithCustomStrategy(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "custom_strat.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer db.Close()

	candles := make([]store.Candle, 250)
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 250; i++ {
		candles[i] = store.Candle{
			Symbol:   "CUSTOMUSDT",
			Interval: "15m",
			OpenTime: baseTime.Add(time.Duration(i*15) * time.Minute),
			Open:     100.0,
			High:     101.0,
			Low:      99.0,
			Close:    100.0,
			Volume:   1000.0,
		}
	}
	if err := db.UpsertCandles(candles); err != nil {
		t.Fatalf("upsert candles failed: %v", err)
	}

	runner := NewRunnerV2(db, 100.0).
		WithRisk(10.0, 10.0, 192).
		WithStrategy(func(series *strategy.Series) (strategy.Evaluator, error) {
			return &customEvaluator{enterAt: 210, exitAt: 215}, nil
		})

	res, err := runner.RunSymbolV2("CUSTOMUSDT", "15m")
	if err != nil {
		t.Fatalf("RunSymbolV2 failed with custom strategy: %v", err)
	}

	if len(res.Trades) != 2 {
		t.Fatalf("expected 2 trade records (1 BUY + 1 SELL), got %d", len(res.Trades))
	}
	if res.Trades[0].Side != "BUY" || res.Trades[1].Side != "SELL" {
		t.Errorf("expected BUY followed by SELL, got %s -> %s", res.Trades[0].Side, res.Trades[1].Side)
	}
	if res.Trades[1].ExitReason != "strategy" {
		t.Errorf("expected exit reason 'strategy', got %s", res.Trades[1].ExitReason)
	}
}
