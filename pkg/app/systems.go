package app

import (
	"fmt"

	"github.com/sdcoffey/techan"
)

// =========================================================================
// HIERARCHICAL STRATEGY (MAC Trend Filter + BBANDS/RSI Reversion)
// =========================================================================

func ShortTermMeanReversion(series *techan.TimeSeries) (techan.RuleStrategy, error) {
	if series == nil || len(series.Candles) == 0 {
		return techan.RuleStrategy{}, fmt.Errorf("empty or nil time series")
	}

	// --- Parent Strategy: Trend Filter (using MA Crossover logic from Strategy2) ---
	shortMATrendPeriod := 10
	longMATrendPeriod := 30
	closePrices := techan.NewClosePriceIndicator(series)
	shortMA := techan.NewSimpleMovingAverage(closePrices, shortMATrendPeriod)
	longMA := techan.NewSimpleMovingAverage(closePrices, longMATrendPeriod)

	isUptrendRule := techan.OverIndicatorRule{First: shortMA, Second: longMA}
	isDowntrendRule := techan.UnderIndicatorRule{First: shortMA, Second: longMA}

	// --- Child Strategy: BBANDS + RSI (Mean Reversion logic from Strategy1) ---
	highPrices := techan.NewHighPriceIndicator(series)
	lowPrices := techan.NewLowPriceIndicator(series)

	bolHigh, bolLow, err := getBolBands(series)
	if err != nil {
		return techan.RuleStrategy{}, fmt.Errorf("failed to get Bollinger Bands %v", err)
	}

	rsi := techan.NewRelativeStrengthIndexIndicator(closePrices, 14)
	rsiOversold := techan.NewConstantIndicator(30)
	rsiUndersold := techan.NewConstantIndicator(70)

	// --- Combining Parent and Child Rules ---
	// Entry Rule: (Downtrend AND Child Strategy's Buy Signal AND No existing position)
	entryRule := techan.And(
		techan.And(
			isDowntrendRule, // Requirement for Short-Term Buy: Downtrend is the environment
			techan.And(
				techan.NewCrossDownIndicatorRule(lowPrices, bolLow), // Price below lower Bollinger Band
				techan.NewCrossUpIndicatorRule(rsi, rsiOversold),    // RSI crosses above 30 (oversold bounce)
			),
		),
		techan.PositionNewRule{},
	)

	// Exit Rule: (Uptrend AND Child Strategy's Sell Signal AND Existing position)
	exitRule := techan.And(
		techan.And(
			isUptrendRule, // Use uptrend as a safety exit/confirmation
			techan.And(
				techan.NewCrossUpIndicatorRule(highPrices, bolHigh), // Price above upper Bollinger Band
				techan.NewCrossDownIndicatorRule(rsi, rsiUndersold), // RSI crosses below 70 (overbought exit)
			),
		),
		techan.PositionOpenRule{},
	)

	maxUnstablePeriod := 30

	strategy := techan.RuleStrategy{
		UnstablePeriod: maxUnstablePeriod,
		EntryRule:      entryRule,
		ExitRule:       exitRule,
	}
	return strategy, nil
}

func LongTermTrendFollowing(series *techan.TimeSeries) (string, techan.RuleStrategy, error) {
	strategyName := "Long Term Trend Following"
	if series == nil || len(series.Candles) == 0 {
		return strategyName, techan.RuleStrategy{}, fmt.Errorf("empty or nil time series")
	}
	// --- Trend Indicators ---
	shortMATrendPeriod := 10
	longMATrendPeriod := 30
	rsiPeriod := 14

	closePrices := techan.NewClosePriceIndicator(series)
	shortMA := techan.NewSimpleMovingAverage(closePrices, shortMATrendPeriod)
	longMA := techan.NewSimpleMovingAverage(closePrices, longMATrendPeriod)

	// --- Momentum Indicator ---
	rsi := techan.NewRelativeStrengthIndexIndicator(closePrices, rsiPeriod)
	rsiMidPoint := techan.NewConstantIndicator(50)

	// --- Trend Rules ---
	// 1. MUST be in an established Uptrend: Short MA is ABOVE Long MA
	isUptrendRule := techan.OverIndicatorRule{First: shortMA, Second: longMA}

	// 2. Momentum Confirmation: Close Price is above the Long MA (confirming price strength)
	priceAboveTrendRule := techan.OverIndicatorRule{First: closePrices, Second: longMA}

	// --- Entry Rule: (Uptrend AND Price Confirmation AND Momentum Acceleration AND New Position) ---
	entryRule := techan.And(
		techan.And(
			isUptrendRule,
			techan.And(
				priceAboveTrendRule,
				// RSI crosses ABOVE the 50 mark
				techan.NewCrossUpIndicatorRule(rsi, rsiMidPoint),
			),
		),
		techan.PositionNewRule{},
	)

	// --- Exit Rule: (Trend Reversal or Momentum Loss) ---
	exitRule := techan.And(
		techan.Or(
			// Option 1: Short MA crosses BELOW Long MA (Trend Reversal)
			techan.NewCrossDownIndicatorRule(shortMA, longMA),
			// OR Option 2: RSI drops below 50 (Loss of Positive Momentum)
			techan.NewCrossDownIndicatorRule(rsi, rsiMidPoint),
		),
		techan.PositionOpenRule{},
	)

	finalExitRule := techan.And(
		exitRule,
		techan.PositionOpenRule{},
	)

	// Max unstable period is 30 (from longMATrendPeriod)
	maxUnstablePeriod := 30

	strategy := techan.RuleStrategy{
		UnstablePeriod: maxUnstablePeriod,
		EntryRule:      entryRule,
		ExitRule:       finalExitRule,
	}
	return strategyName, strategy, nil
}
