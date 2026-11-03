package blueprints_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/adnanshaldrake/signalbot/docs/blueprints"
	"github.com/adnanshaldrake/signalbot/pkg/backtest"
	"github.com/adnanshaldrake/signalbot/pkg/store"
	"github.com/adnanshaldrake/signalbot/pkg/strategy"
)

func TestCustomStrategyBlueprint(t *testing.T) {
	// 1. Verify interface compliance
	var _ strategy.Evaluator = (*blueprints.MomentumBreakoutStrategy)(nil)

	// 2. Setup mock candle database
	dbPath := filepath.Join(t.TempDir(), "blueprint_test.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer db.Close()

	// 250 candles to allow 50 EMA warm-up
	candles := make([]store.Candle, 250)
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 250; i++ {
		price := 100.0
		if i > 210 {
			price = 100.0 + float64(i-210)*1.5 // rising trend to trigger breakout
		}
		candles[i] = store.Candle{
			Symbol:   "BTCUSDT",
			Interval: "15m",
			OpenTime: baseTime.Add(time.Duration(i*15) * time.Minute),
			Open:     price,
			High:     price + 2.0,
			Low:      price - 1.0,
			Close:    price + 1.0,
			Volume:   1000.0,
		}
	}
	if err := db.UpsertCandles(candles); err != nil {
		t.Fatalf("failed to upsert candles: %v", err)
	}

	// 3. Plug blueprint into RunnerV2
	stratFactory := func(series *strategy.Series) (strategy.Evaluator, error) {
		return blueprints.NewMomentumBreakout(series, 12, 26, 14)
	}

	runner := backtest.NewRunnerV2(db, 100.0).
		WithRisk(3.0, 6.0, 192).
		WithStrategy(stratFactory)

	result, err := runner.RunSymbolV2("BTCUSDT", "15m")
	if err != nil {
		t.Fatalf("RunSymbolV2 failed: %v", err)
	}

	if result.Symbol != "BTCUSDT" {
		t.Errorf("expected symbol BTCUSDT, got %s", result.Symbol)
	}
}
