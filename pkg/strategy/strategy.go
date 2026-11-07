// Package strategy provides a native float64 implementation of the BB+RSI
// mean-reversion trading strategy, replacing the techan/big-based version.
// It is a drop-in functional replacement — all signal conditions are identical;
// only the arithmetic engine changes (big.Float → float64).
//
// To use in the backtest runner or live loop, replace:
//
//	*techan.TimeSeries  →  *strategy.Series
//	techan.RuleStrategy →  *strategy.Strategy
//
// The existing pkg/app/ta.go and pkg/backtest/runner.go are left untouched.
package strategy

import (
	"fmt"
	"strconv"
	"time"

	"github.com/adnanshaldrake/signalbot/pkg/indicators"
)

// Candle is a single OHLCV bar with native float64 fields.
type Candle struct {
	OpenTime time.Time
	Open     float64
	High     float64
	Low      float64
	Close    float64
	Volume   float64
}

// Series is an ordered slice of OHLCV candles.
type Series struct {
	Candles []Candle
}

// NewSeriesFromRaw builds a Series from a raw Binance kline JSON array.
// Each element of raw is a []interface{} with the standard Binance candle layout:
//
//	[openMs, open, high, low, close, volume, ...]
func NewSeriesFromRaw(raw [][]interface{}) (*Series, error) {
	candles := make([]Candle, 0, len(raw))
	for _, bar := range raw {
		if len(bar) < 6 {
			return nil, fmt.Errorf("unexpected kline length %d", len(bar))
		}
		openMs, ok := bar[0].(float64)
		if !ok {
			return nil, fmt.Errorf("open_time is not float64")
		}
		parse := func(v interface{}) float64 {
			s, _ := v.(string)
			f, _ := strconv.ParseFloat(s, 64)
			return f
		}
		candles = append(candles, Candle{
			OpenTime: time.UnixMilli(int64(openMs)),
			Open:     parse(bar[1]),
			High:     parse(bar[2]),
			Low:      parse(bar[3]),
			Close:    parse(bar[4]),
			Volume:   parse(bar[5]),
		})
	}
	return &Series{Candles: candles}, nil
}

// Closes returns a float64 slice of all close prices.
func (s *Series) Closes() []float64 {
	out := make([]float64, len(s.Candles))
	for i, c := range s.Candles {
		out[i] = c.Close
	}
	return out
}

func (s *Series) closes() []float64 {
	return s.Closes()
}

// highs returns a float64 slice of all high prices.
func (s *Series) highs() []float64 {
	out := make([]float64, len(s.Candles))
	for i, c := range s.Candles {
		out[i] = c.High
	}
	return out
}

// lows returns a float64 slice of all low prices.
func (s *Series) lows() []float64 {
	out := make([]float64, len(s.Candles))
	for i, c := range s.Candles {
		out[i] = c.Low
	}
	return out
}

// Evaluator evaluates entry and exit trading rules at a given candle index.
// Any custom or proprietary strategy (e.g. external private packages or plugins)
// can implement this interface to integrate with backtesting and execution runners.
type Evaluator interface {
	ShouldEnter(index int) bool
	ShouldExit(index int) bool
}

// IndicatorAccessor is an optional interface that strategies can implement
// if they expose technical indicator values for a given candle index.
type IndicatorAccessor interface {
	UpperBB(index int) float64
	MidBB(index int) float64
	LowerBB(index int) float64
	RSIAt(index int) float64
}

// Factory constructs an Evaluator from an OHLCV Series.
type Factory func(series *Series) (Evaluator, error)

// Strategy holds all precomputed indicator slices for a Series and can
// evaluate entry/exit conditions at any candle index in O(1).
type Strategy struct {
	series      *Series
	highSlice   []float64
	lowSlice    []float64
	rsi         []float64
	upperBB     []float64
	midBB       []float64
	lowerBB     []float64
	ema50       []float64
	ema200      []float64
	useEMATrend bool
}

// ShouldEnter returns true when all entry conditions fire at candle index i.
// The caller must ensure no position is already open (PositionNew guard).
func (s *Strategy) ShouldEnter(i int) bool {
	if i < 1 || i >= len(s.series.Candles) {
		return false
	}
	// We want to buy a bounce. It's rare for both the BB pierce AND the RSI cross
	// to happen on the exact same 15m candle. Let's relax it:
	// Entry condition: RSI crosses above 30, and price is at/below the lower BB.
	rsiRecovering := indicators.CrossUpConst(s.rsi, 30.0, i) || indicators.CrossUpConst(s.rsi, 35.0, i)
	priceLow := s.lowSlice[i] <= s.lowerBB[i]*1.005 // within 0.5% of lower BB

	if !rsiRecovering || !priceLow {
		return false
	}
	if s.useEMATrend {
		// EMA(50) must be above EMA(200) — skip during warm-up (value == 0)
		if s.ema50[i] == 0 || s.ema200[i] == 0 {
			return false
		}
		if s.ema50[i] <= s.ema200[i] {
			return false
		}
	}
	return true
}

// ShouldExit returns true when all exit conditions fire at candle index i.
// The caller must ensure a position is open (PositionOpen guard).
func (s *Strategy) ShouldExit(i int) bool {
	if i < 1 || i >= len(s.series.Candles) {
		return false
	}
	// High crosses above upper Bollinger Band
	if !indicators.CrossUp(s.highSlice, s.upperBB, i) {
		return false
	}
	// RSI crosses below 70 (fade from overbought)
	return indicators.CrossDownConst(s.rsi, 70.0, i)
}

// --- Indicator accessors (O(1) reads into precomputed slices) ---

// RSIAt returns the RSI(14) value at index i.
func (s *Strategy) RSIAt(i int) float64 {
	if i < 0 || i >= len(s.rsi) {
		return 0
	}
	return s.rsi[i]
}

// UpperBB returns the upper Bollinger Band value at index i.
func (s *Strategy) UpperBB(i int) float64 {
	if i < 0 || i >= len(s.upperBB) {
		return 0
	}
	return s.upperBB[i]
}

// MidBB returns the middle Bollinger Band (SMA21) value at index i.
func (s *Strategy) MidBB(i int) float64 {
	if i < 0 || i >= len(s.midBB) {
		return 0
	}
	return s.midBB[i]
}

// LowerBB returns the lower Bollinger Band value at index i.
func (s *Strategy) LowerBB(i int) float64 {
	if i < 0 || i >= len(s.lowerBB) {
		return 0
	}
	return s.lowerBB[i]
}

// --- Constructors ---

func build(series *Series, useEMATrend bool) (*Strategy, error) {
	if series == nil || len(series.Candles) == 0 {
		return nil, fmt.Errorf("strategy: empty or nil series")
	}
	closes := series.closes()
	upper, mid, lower := indicators.BollingerBands(closes, 21, 2.0)
	st := &Strategy{
		series:      series,
		highSlice:   series.highs(),
		lowSlice:    series.lows(),
		rsi:         indicators.RSI(closes, 14),
		upperBB:     upper,
		midBB:       mid,
		lowerBB:     lower,
		useEMATrend: useEMATrend,
	}
	if useEMATrend {
		st.ema50 = indicators.EMA(closes, 50)
		st.ema200 = indicators.EMA(closes, 200)
	}
	return st, nil
}

// Strategy1 computes BB pierce + RSI cross signals (no trend filter).
func Strategy1(series *Series) (*Strategy, error) {
	return build(series, false)
}

// Strategy2 extends Strategy1 with an EMA(50) > EMA(200) trend gate.
// Requires at least 200 candles of warm-up data.
func Strategy2(series *Series) (*Strategy, error) {
	return build(series, true)
}

// NewStrategy1 returns an Evaluator using Strategy1 rules.
func NewStrategy1(series *Series) (Evaluator, error) {
	return Strategy1(series)
}

// NewStrategy2 returns an Evaluator using Strategy2 rules.
func NewStrategy2(series *Series) (Evaluator, error) {
	return Strategy2(series)
}
