// Package indicators provides fast, zero-dependency technical analysis
// indicators computed on plain []float64 slices. All functions return a slice
// of the same length as the input; indices in the warm-up period (insufficient
// history) are returned as 0.
package indicators

import "math"

// EMA returns the Exponential Moving Average for the given period.
// The first valid value is seeded with the SMA of the first period closes.
// alpha = 2 / (period + 1).
func EMA(closes []float64, period int) []float64 {
	result := make([]float64, len(closes))
	if len(closes) < period || period <= 0 {
		return result
	}
	k := 2.0 / float64(period+1)
	sum := 0.0
	for i := 0; i < period; i++ {
		sum += closes[i]
	}
	result[period-1] = sum / float64(period)
	for i := period; i < len(closes); i++ {
		result[i] = closes[i]*k + result[i-1]*(1-k)
	}
	return result
}

// RSI returns the Relative Strength Index for the given period using
// Wilders smoothing (equivalent to RMA with alpha = 1/period).
// Indices before period are returned as 0.
func RSI(closes []float64, period int) []float64 {
	result := make([]float64, len(closes))
	if len(closes) <= period || period <= 0 {
		return result
	}
	var avgGain, avgLoss float64
	for i := 1; i <= period; i++ {
		change := closes[i] - closes[i-1]
		if change > 0 {
			avgGain += change
		} else {
			avgLoss -= change
		}
	}
	avgGain /= float64(period)
	avgLoss /= float64(period)
	result[period] = rsiValue(avgGain, avgLoss)
	for i := period + 1; i < len(closes); i++ {
		change := closes[i] - closes[i-1]
		gain, loss := 0.0, 0.0
		if change > 0 {
			gain = change
		} else {
			loss = -change
		}
		avgGain = (avgGain*float64(period-1) + gain) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + loss) / float64(period)
		result[i] = rsiValue(avgGain, avgLoss)
	}
	return result
}

func rsiValue(avgGain, avgLoss float64) float64 {
	if avgLoss == 0 {
		return 100
	}
	return 100 - 100/(1+avgGain/avgLoss)
}

// BollingerBands returns the upper, middle, and lower Bollinger Bands.
// middle = SMA(period), upper = middle + mult*stddev, lower = middle - mult*stddev.
// Uses population standard deviation (divides by N).
// Indices before period-1 are returned as 0.
func BollingerBands(closes []float64, period int, mult float64) (upper, mid, lower []float64) {
	n := len(closes)
	upper = make([]float64, n)
	mid = make([]float64, n)
	lower = make([]float64, n)
	if n < period || period <= 0 {
		return
	}
	for i := period - 1; i < n; i++ {
		window := closes[i-period+1 : i+1]
		sma := mean(window)
		sd := stddev(window, sma)
		mid[i] = sma
		upper[i] = sma + mult*sd
		lower[i] = sma - mult*sd
	}
	return
}

// CrossUp returns true if a[i] crossed above b[i]:
// a[i] > b[i] and a[i-1] <= b[i-1]. Returns false when i < 1.
func CrossUp(a, b []float64, i int) bool {
	if i < 1 || i >= len(a) || i >= len(b) {
		return false
	}
	return a[i] > b[i] && a[i-1] <= b[i-1]
}

// CrossDown returns true if a[i] crossed below b[i]:
// a[i] < b[i] and a[i-1] >= b[i-1]. Returns false when i < 1.
func CrossDown(a, b []float64, i int) bool {
	if i < 1 || i >= len(a) || i >= len(b) {
		return false
	}
	return a[i] < b[i] && a[i-1] >= b[i-1]
}

// CrossUpConst returns true if a[i] crossed above a constant threshold.
func CrossUpConst(a []float64, threshold float64, i int) bool {
	if i < 1 || i >= len(a) {
		return false
	}
	return a[i] > threshold && a[i-1] <= threshold
}

// CrossDownConst returns true if a[i] crossed below a constant threshold.
func CrossDownConst(a []float64, threshold float64, i int) bool {
	if i < 1 || i >= len(a) {
		return false
	}
	return a[i] < threshold && a[i-1] >= threshold
}

func mean(v []float64) float64 {
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func stddev(v []float64, m float64) float64 {
	s := 0.0
	for _, x := range v {
		d := x - m
		s += d * d
	}
	return math.Sqrt(s / float64(len(v)))
}
