package app

import (
	"fmt"

	"github.com/sdcoffey/techan"
)

// =========================================================================
// INDICATOR HELPERS
// =========================================================================

// getBolBands calculates the Bollinger Bands indicator.
func getBolBands(series *techan.TimeSeries) (techan.Indicator, techan.Indicator, error) {
	if series == nil || len(series.Candles) == 0 {
		return nil, nil, fmt.Errorf("input series is nil or empty")
	}

	// Get the Closing price for the series
	closePrices := techan.NewClosePriceIndicator(series)

	// Generate the Upper Bollinger Band (21 period, 2.0 std dev)
	bolHigh := techan.NewBollingerUpperBandIndicator(closePrices, 21, 2.0)

	// Generate the Lower Bollinger Band (21 period, 2.0 std dev)
	bolLow := techan.NewBollingerLowerBandIndicator(closePrices, 21, 2.0)

	return bolHigh, bolLow, nil
}

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

// =========================================================================
// HIERARCHICAL STRATEGY (MAC Trend Filter + BBANDS/RSI Reversion)
// =========================================================================

func HierarchicalStrategyExample(series *techan.TimeSeries) (techan.RuleStrategy, error) {
	if series == nil || len(series.Candles) == 0 {
		return techan.RuleStrategy{}, fmt.Errorf("empty or nil time series")
	}

	// --- Parent Strategy: Trend Filter (using MA Crossover logic from Strategy2) ---
	shortMATrendPeriod := 10
	longMATrendPeriod := 30
	closePrices := techan.NewClosePriceIndicator(series)
	shortMA := techan.NewSimpleMovingAverage(closePrices, shortMATrendPeriod)
	longMA := techan.NewSimpleMovingAverage(closePrices, longMATrendPeriod)

	// Use techan struct rules for comparison:
	isUptrendRule := techan.OverIndicatorRule{First: shortMA, Second: longMA}
	isDowntrendRule := techan.UnderIndicatorRule{First: shortMA, Second: longMA}

	// --- Child Strategy: BBANDS + RSI (Mean Reversion logic from Strategy1) ---
	highPrices := techan.NewHighPriceIndicator(series)
	lowPrices := techan.NewLowPriceIndicator(series)

	bolHigh, bolLow, err := getBolBands(series) // Uses a 21-period SMA for BB
	if err != nil {
		return techan.RuleStrategy{}, fmt.Errorf("failed to get Bollinger Bands %v", err)
	}

	rsi := techan.NewRelativeStrengthIndexIndicator(closePrices, 14)
	rsiOversold := techan.NewConstantIndicator(30)
	rsiUndersold := techan.NewConstantIndicator(70)

	// --- Combining Parent and Child Rules ---
	// Entry Rule: (Uptrend AND Child Strategy's Buy Signal AND No existing position)
	entryRule := techan.And(
		techan.And(
			isDowntrendRule,
			techan.And( // 1st level combines the crossover rules
				techan.NewCrossDownIndicatorRule(lowPrices, bolLow), // Price below lower Bollinger Band
				techan.NewCrossUpIndicatorRule(rsi, rsiOversold),    // RSI crosses below 70
			),
		),
		techan.PositionNewRule{}, // 2nd level combines the result with the position filter
	)

	// Exit Rule: (Downtrend AND Child Strategy's Sell Signal AND Existing position)
	exitRule := techan.And(
		techan.And(
			isUptrendRule,
			techan.And(
				techan.NewCrossUpIndicatorRule(highPrices, bolHigh), // Price above upper Bollinger Band
				techan.NewCrossDownIndicatorRule(rsi, rsiUndersold), // RSI crosses above 30
			),
		),
		techan.PositionOpenRule{},
	)

	// Max unstable period is 30 (from longMATrendPeriod)
	maxUnstablePeriod := 30

	strategy := techan.RuleStrategy{
		UnstablePeriod: maxUnstablePeriod,
		EntryRule:      entryRule,
		ExitRule:       exitRule,
	}
	return strategy, nil
}
