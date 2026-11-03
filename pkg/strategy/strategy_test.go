package strategy_test

import (
	"testing"
	"time"

	"github.com/adnanshaldrake/signalbot/pkg/strategy"
)

// mockStrategy implements strategy.Evaluator for testing pluggability.
type mockStrategy struct {
	enterIndex int
	exitIndex  int
}

func (m *mockStrategy) ShouldEnter(index int) bool {
	return index == m.enterIndex
}

func (m *mockStrategy) ShouldExit(index int) bool {
	return index == m.exitIndex
}

func TestEvaluatorInterfaceCompliance(t *testing.T) {
	// 1. Verify custom strategy satisfies Evaluator
	var _ strategy.Evaluator = &mockStrategy{}

	// 2. Build sample series
	series := &strategy.Series{
		Candles: make([]strategy.Candle, 210),
	}
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range series.Candles {
		series.Candles[i] = strategy.Candle{
			OpenTime: baseTime.Add(time.Duration(i*15) * time.Minute),
			Open:     100.0,
			High:     101.0,
			Low:      99.0,
			Close:    100.0,
			Volume:   1000.0,
		}
	}

	// 3. Verify concrete Strategy satisfies Evaluator and IndicatorAccessor
	strat, err := strategy.Strategy1(series)
	if err != nil {
		t.Fatalf("Strategy1 failed: %v", err)
	}

	var _ strategy.Evaluator = strat
	var _ strategy.IndicatorAccessor = strat

	// 4. Verify Factory constructors NewStrategy1 and NewStrategy2
	eval1, err := strategy.NewStrategy1(series)
	if err != nil {
		t.Fatalf("NewStrategy1 failed: %v", err)
	}
	if eval1 == nil {
		t.Fatalf("expected non-nil Evaluator from NewStrategy1")
	}

	eval2, err := strategy.NewStrategy2(series)
	if err != nil {
		t.Fatalf("NewStrategy2 failed: %v", err)
	}
	if eval2 == nil {
		t.Fatalf("expected non-nil Evaluator from NewStrategy2")
	}

	// 5. Test mock strategy evaluation
	mock := &mockStrategy{enterIndex: 10, exitIndex: 15}
	if !mock.ShouldEnter(10) {
		t.Errorf("expected ShouldEnter(10) to be true")
	}
	if mock.ShouldEnter(11) {
		t.Errorf("expected ShouldEnter(11) to be false")
	}
	if !mock.ShouldExit(15) {
		t.Errorf("expected ShouldExit(15) to be true")
	}
	if mock.ShouldExit(16) {
		t.Errorf("expected ShouldExit(16) to be false")
	}
}

func TestStrategyIndicatorAccessor(t *testing.T) {
	series := &strategy.Series{
		Candles: make([]strategy.Candle, 50),
	}
	baseTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range series.Candles {
		series.Candles[i] = strategy.Candle{
			OpenTime: baseTime.Add(time.Duration(i*15) * time.Minute),
			Open:     100.0,
			High:     105.0,
			Low:      95.0,
			Close:    100.0,
			Volume:   500.0,
		}
	}

	strat, err := strategy.Strategy1(series)
	if err != nil {
		t.Fatalf("Strategy1 failed: %v", err)
	}

	lastIdx := len(series.Candles) - 1
	if strat.UpperBB(lastIdx) <= 0 {
		t.Errorf("expected positive UpperBB, got %f", strat.UpperBB(lastIdx))
	}
	if strat.LowerBB(lastIdx) <= 0 {
		t.Errorf("expected positive LowerBB, got %f", strat.LowerBB(lastIdx))
	}
	if strat.MidBB(lastIdx) <= 0 {
		t.Errorf("expected positive MidBB, got %f", strat.MidBB(lastIdx))
	}
	if strat.RSIAt(lastIdx) <= 0 {
		t.Errorf("expected positive RSI, got %f", strat.RSIAt(lastIdx))
	}

	// Out of bounds checks
	if got := strat.UpperBB(-1); got != 0 {
		t.Errorf("expected 0 for negative index, got %f", got)
	}
	if got := strat.UpperBB(100); got != 0 {
		t.Errorf("expected 0 for out-of-range index, got %f", got)
	}
}

