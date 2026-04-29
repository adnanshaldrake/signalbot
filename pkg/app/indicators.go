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
