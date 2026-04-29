package app

import (
	"fmt"

	"github.com/sdcoffey/techan"
)

// =========================================================================
// STRATEGY 1: BBANDS + RSI (Mean Reversion)
// =========================================================================

func Strategy1(series *techan.TimeSeries) (techan.RuleStrategy, error) {
	if series == nil || len(series.Candles) == 0 {
		return techan.RuleStrategy{}, fmt.Errorf("empty or nil time series")
	}

	highPrices := techan.NewHighPriceIndicator(series)
	lowPrices := techan.NewLowPriceIndicator(series)
	closePrices := techan.NewClosePriceIndicator(series)

	bolHigh, bolLow, err := getBolBands(series)
	if err != nil {
		return techan.RuleStrategy{}, fmt.Errorf("failed to get Bollinger Bands %v", err)
	}

	// Calculate the RSI indicator
	rsi := techan.NewRelativeStrengthIndexIndicator(closePrices, 14)
	rsiOversold := techan.NewConstantIndicator(30)
	rsiUndersold := techan.NewConstantIndicator(70)

	// Create the entry rule: Price below lower BB AND RSI crosses above 30
	entryRule := techan.And(
		techan.And(
			techan.NewCrossDownIndicatorRule(lowPrices, bolLow), // Price below lower Bollinger Band
			techan.NewCrossUpIndicatorRule(rsi, rsiOversold),    // RSI crosses above 30 (oversold)
		),
		techan.PositionNewRule{}, // No existing position
	)

	// Create the exit rule: Price above upper BB AND RSI crosses below 70
	exitRule := techan.And(
		techan.And(
			techan.NewCrossUpIndicatorRule(highPrices, bolHigh), // Price above upper Bollinger Band
			techan.NewCrossDownIndicatorRule(rsi, rsiUndersold), // RSI crosses below 70 (overbought)
		),
		techan.PositionOpenRule{}, // Existing position open
	)

	strategy := techan.RuleStrategy{
		UnstablePeriod: 21, // Longest period is BBANDS (21)
		EntryRule:      entryRule,
		ExitRule:       exitRule,
	}
	return strategy, nil
}

// =========================================================================
// STRATEGY 2: Moving Average Crossover (MAC)
// =========================================================================

func Strategy2(series *techan.TimeSeries) (techan.RuleStrategy, error) {
	if series == nil || len(series.Candles) == 0 {
		return techan.RuleStrategy{}, fmt.Errorf("empty or nil time series")
	}

	closePrices := techan.NewClosePriceIndicator(series)

	// Define short and long period MAs
	shortPeriod := 10 // 10-period SMA
	longPeriod := 30  // 30-period SMA

	shortMA := techan.NewSimpleMovingAverage(closePrices, shortPeriod)
	longMA := techan.NewSimpleMovingAverage(closePrices, longPeriod)

	// Entry Rule: Short MA crosses above Long MA, and no existing position
	entryRule := techan.And(
		techan.NewCrossUpIndicatorRule(shortMA, longMA),
		techan.PositionNewRule{},
	)

	// Exit Rule: Short MA crosses below Long MA, and an existing position
	exitRule := techan.And(
		techan.NewCrossDownIndicatorRule(shortMA, longMA),
		techan.PositionOpenRule{},
	)

	strategy := techan.RuleStrategy{
		UnstablePeriod: longPeriod, // Longest period is 30
		EntryRule:      entryRule,
		ExitRule:       exitRule,
	}
	return strategy, nil
}

// =========================================================================
// STRATEGY 3: MACD Crossover with Signal Line
// =========================================================================

func Strategy3(series *techan.TimeSeries) (techan.RuleStrategy, error) {
	if series == nil || len(series.Candles) == 0 {
		return techan.RuleStrategy{}, fmt.Errorf("empty or nil time series")
	}

	closePrices := techan.NewClosePriceIndicator(series)

	// MACD parameters: (fastPeriod, slowPeriod, signalPeriod)
	macdLine := techan.NewMACDIndicator(closePrices, 12, 26)
	signalLine := techan.NewEMAIndicator(macdLine, 9)

	// Entry Rule: MACD line crosses above Signal line, and no existing position
	entryRule := techan.And(
		techan.NewCrossUpIndicatorRule(macdLine, signalLine),
		techan.PositionNewRule{},
	)

	// Exit Rule: MACD line crosses below Signal line, and an existing position
	exitRule := techan.And(
		techan.NewCrossDownIndicatorRule(macdLine, signalLine),
		techan.PositionOpenRule{},
	)

	strategy := techan.RuleStrategy{
		UnstablePeriod: 26, // Longest period is 26
		EntryRule:      entryRule,
		ExitRule:       exitRule,
	}
	return strategy, nil
}
