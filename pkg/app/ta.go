package app

import (
	"fmt"

	"github.com/sdcoffey/techan"
)

// GetRSI returns the RSI(14) value at the given candle index for a series.
// Returns 0 and an error if the series is nil or empty.
func GetRSI(series *techan.TimeSeries, idx int) (float64, error) {
	if series == nil || len(series.Candles) == 0 {
		return 0, fmt.Errorf("empty or nil time series")
	}
	closePrices := techan.NewClosePriceIndicator(series)
	rsi := techan.NewRelativeStrengthIndexIndicator(closePrices, 14)
	return rsi.Calculate(idx).Float(), nil
}

// GetUpperBB returns the upper Bollinger Band (21, 2.0) value at the given index.
// Returns 0 and an error if the series is nil or empty.
func GetUpperBB(series *techan.TimeSeries, idx int) (float64, error) {
	if series == nil || len(series.Candles) == 0 {
		return 0, fmt.Errorf("empty or nil time series")
	}
	bolHigh, _, err := getBolBands(series)
	if err != nil {
		return 0, err
	}
	return bolHigh.Calculate(idx).Float(), nil
}

// GetMiddleBB returns the middle Bollinger Band (21-period SMA) at the given index.
func GetMiddleBB(series *techan.TimeSeries, idx int) (float64, error) {
	if series == nil || len(series.Candles) == 0 {
		return 0, fmt.Errorf("empty or nil time series")
	}
	closePrices := techan.NewClosePriceIndicator(series)
	sma := techan.NewSimpleMovingAverage(closePrices, 21)
	return sma.Calculate(idx).Float(), nil
}

// GetLowerBB returns the lower Bollinger Band (21, 2.0) value at the given index.
func GetLowerBB(series *techan.TimeSeries, idx int) (float64, error) {
	if series == nil || len(series.Candles) == 0 {
		return 0, fmt.Errorf("empty or nil time series")
	}
	_, bolLow, err := getBolBands(series)
	if err != nil {
		return 0, err
	}
	return bolLow.Calculate(idx).Float(), nil
}

// GetEMA returns an EMA of the given period at the given candle index.
func GetEMA(series *techan.TimeSeries, period, idx int) (float64, error) {
	if series == nil || len(series.Candles) == 0 {
		return 0, fmt.Errorf("empty or nil time series")
	}
	closePrices := techan.NewClosePriceIndicator(series)
	ema := techan.NewEMAIndicator(closePrices, period)
	return ema.Calculate(idx).Float(), nil
}

func getBolBands(series *techan.TimeSeries) (techan.Indicator, techan.Indicator, error) {
	/*
		Calculates the Bollinger Bands indicator for a given time series.
		The upper band and lower band indicators are returned.

		Parameters:
		- series: a pointer to a techan.TimeSeries object for which to calculate the Bollinger Bands.

		Returns:
		- A techan.Indicator representing the upper band of the Bollinger Bands.
		- A techan.Indicator representing the lower band of the Bollinger Bands.
	*/
	if series == nil || series == techan.NewTimeSeries() {
		return nil, nil, fmt.Errorf("input series is nil or empty")
	}

	// Get the Closing price for the series
	closePrices := techan.NewClosePriceIndicator(series)

	// Generate the Upper Bollinger Band
	bolHigh := techan.NewBollingerUpperBandIndicator(closePrices, 21, 2.0)

	// Generate the Lower Bollinger Band
	bolLow := techan.NewBollingerLowerBandIndicator(closePrices, 21, 2.0)

	return bolHigh, bolLow, nil
}

func Strategy1(series *techan.TimeSeries) (techan.RuleStrategy, error) {
	/*
		Returns a RuleStrategy that uses Bollinger Bands and cross
		indicator rules to generate buy and sell signals.

		Parameters:
		- series: A pointer to a TimeSeries containing the data to be analyzed.

		Returns:
		- A RuleStrategy struct containing the entry and exit rules for the strategy.
		- An error if there was an issue generating the strategy.
	*/
	if series == nil || len(series.Candles) == 0 {
		return techan.RuleStrategy{}, fmt.Errorf("empty or nil time series")
	}

	// Get the High, Low, and Close prices from the series
	highPrices := techan.NewHighPriceIndicator(series)
	lowPrices := techan.NewLowPriceIndicator(series)
	closePrices := techan.NewClosePriceIndicator(series)

	// Get the Upper and Lower Bollinger Bands
	bolHigh, bolLow, err := getBolBands(series)
	if err != nil {
		return techan.RuleStrategy{}, fmt.Errorf("failed to get Bollinger Bands %v\n", err)
	}

	// Calculate the RSI indicator
	rsi := techan.NewRelativeStrengthIndexIndicator(closePrices, 14)
	rsiOversold := techan.NewConstantIndicator(30)
	rsiUndersold := techan.NewConstantIndicator(70)

	// Create the entry rule with Bollinger Bands and RSI filter conditions
	entryRule := techan.And(
		techan.And(
			techan.NewCrossDownIndicatorRule(lowPrices, bolLow), // Price below lower Bollinger Band
			techan.NewCrossUpIndicatorRule(rsi, rsiOversold),    // RSI crosses above 30 (oversold condition)
		),
		techan.PositionNewRule{}, // No existing position
	)

	// Create the exit rule with Bollinger Bands and RSI filter conditions
	exitRule := techan.And(
		techan.And(
			techan.NewCrossUpIndicatorRule(highPrices, bolHigh), // Price above upper Bollinger Band
			techan.NewCrossDownIndicatorRule(rsi, rsiUndersold), // RSI crosses below 70 (overbought condition)
		),
		techan.PositionOpenRule{}, // Existing position open
	)

	// Create the strategy with the above entry and exit rules
	strategy := techan.RuleStrategy{
		UnstablePeriod: 0,
		EntryRule:      entryRule,
		ExitRule:       exitRule,
	}
	return strategy, nil
}

// Strategy2 extends Strategy1 with a trend filter: EMA(50) must be above EMA(200)
// at signal time, preventing mean-reversion entries during sustained downtrends.
// This is the recommended strategy for production use on alt coins.
//
// Additional entry conditions vs Strategy1:
//  1. EMA(50) > EMA(200) — long-term trend must be bullish
//
// Exit rules are identical to Strategy1 (SL/TP from the risk manager are the
// primary exits in the live loop; the strategy exit rule fires in backtest).
//
// NOTE: requires at least 200 candles of warm-up data. Use MinCandlesRequired >= 200
// when routing through this strategy.
func Strategy2(series *techan.TimeSeries) (techan.RuleStrategy, error) {
	if series == nil || len(series.Candles) == 0 {
		return techan.RuleStrategy{}, fmt.Errorf("empty or nil time series")
	}

	highPrices := techan.NewHighPriceIndicator(series)
	lowPrices := techan.NewLowPriceIndicator(series)
	closePrices := techan.NewClosePriceIndicator(series)

	bolHigh, bolLow, err := getBolBands(series)
	if err != nil {
		return techan.RuleStrategy{}, fmt.Errorf("Strategy2: get Bollinger Bands: %v", err)
	}

	rsi := techan.NewRelativeStrengthIndexIndicator(closePrices, 14)
	rsiOversold := techan.NewConstantIndicator(30)
	rsiUndersold := techan.NewConstantIndicator(70)

	// EMA trend filter: EMA(50) must be above EMA(200).
	// trendAbove is true on every candle where EMA50 > EMA200 (sustained bullish trend).
	// trendBullish is true on the candle where EMA50 first crosses above EMA200.
	ema50 := techan.NewEMAIndicator(closePrices, 50)
	ema200 := techan.NewEMAIndicator(closePrices, 200)
	trendBullish := techan.NewCrossUpIndicatorRule(ema50, ema200)
	trendAbove := techan.OverIndicatorRule{First: ema50, Second: ema200}

	entryRule := techan.And(
		techan.And(
			techan.And(
				techan.NewCrossDownIndicatorRule(lowPrices, bolLow), // Price pierces lower BB
				techan.NewCrossUpIndicatorRule(rsi, rsiOversold),    // RSI crosses above 30
			),
			// Trend gate: EMA50 just crossed above EMA200, or is already above it
			techan.Or(trendBullish, trendAbove),
		),
		techan.PositionNewRule{},
	)

	exitRule := techan.And(
		techan.And(
			techan.NewCrossUpIndicatorRule(highPrices, bolHigh),
			techan.NewCrossDownIndicatorRule(rsi, rsiUndersold),
		),
		techan.PositionOpenRule{},
	)

	return techan.RuleStrategy{
		UnstablePeriod: 0,
		EntryRule:      entryRule,
		ExitRule:       exitRule,
	}, nil
}
