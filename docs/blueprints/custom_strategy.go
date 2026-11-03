// Package blueprints demonstrates how developers can implement a custom
// trading strategy and plug it into the SignalBot backtesting engine.
package blueprints

import (
	"fmt"

	"github.com/adnanshaldrake/signalbot/pkg/indicators"
	"github.com/adnanshaldrake/signalbot/pkg/strategy"
)

// MomentumBreakoutStrategy demonstrates a custom trend-following strategy.
// It implements strategy.Evaluator (ShouldEnter and ShouldExit).
type MomentumBreakoutStrategy struct {
	series     *strategy.Series
	emaFast    []float64
	emaSlow    []float64
	rsi        []float64
	fastPeriod int
	slowPeriod int
	rsiPeriod  int
}

// NewMomentumBreakout creates and precomputes indicators for the strategy.
func NewMomentumBreakout(series *strategy.Series, fastPeriod, slowPeriod, rsiPeriod int) (*MomentumBreakoutStrategy, error) {
	if series == nil || len(series.Candles) < slowPeriod {
		return nil, fmt.Errorf("insufficient candles for strategy: need >= %d, got %d",
			slowPeriod, len(series.Candles))
	}

	closes := make([]float64, len(series.Candles))
	for i, c := range series.Candles {
		closes[i] = c.Close
	}

	return &MomentumBreakoutStrategy{
		series:     series,
		emaFast:    indicators.EMA(closes, fastPeriod),
		emaSlow:    indicators.EMA(closes, slowPeriod),
		rsi:        indicators.RSI(closes, rsiPeriod),
		fastPeriod: fastPeriod,
		slowPeriod: slowPeriod,
		rsiPeriod:  rsiPeriod,
	}, nil
}

// ShouldEnter returns true if EMA fast crosses above EMA slow and RSI > 50.
func (s *MomentumBreakoutStrategy) ShouldEnter(i int) bool {
	if i < s.slowPeriod || i >= len(s.series.Candles) {
		return false
	}

	// Golden Cross: fast EMA crosses above slow EMA
	crossedUp := indicators.CrossUp(s.emaFast, s.emaSlow, i)
	// Bullish momentum confirmation
	bullishRSI := s.rsi[i] > 50.0

	return crossedUp && bullishRSI
}

// ShouldExit returns true if EMA fast crosses below EMA slow or RSI drops below 40.
func (s *MomentumBreakoutStrategy) ShouldExit(i int) bool {
	if i < s.slowPeriod || i >= len(s.series.Candles) {
		return false
	}

	// Death Cross or momentum lost
	crossedDown := indicators.CrossDown(s.emaFast, s.emaSlow, i)
	momentumLost := s.rsi[i] < 40.0

	return crossedDown || momentumLost
}

// AsFactory returns a strategy.Factory closure ready to plug into RunnerV2.
func (s *MomentumBreakoutStrategy) AsFactory() strategy.Factory {
	return func(series *strategy.Series) (strategy.Evaluator, error) {
		return NewMomentumBreakout(series, s.fastPeriod, s.slowPeriod, s.rsiPeriod)
	}
}
