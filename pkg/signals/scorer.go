// Package signals provides scoring and ranking for buy signal candidates.
// Each candidate is evaluated on three dimensions:
//
//  1. Upside potential  — distance from current price to the upper Bollinger Band
//     (the strategy's exit target), expressed as a percentage.
//  2. RSI depth         — how far RSI has fallen below the 30 oversold threshold.
//  3. Volume            — normalised 24 h USDT volume among the current candidate set.
//
// The three sub-scores are combined into a single [0, 1] composite score using
// configurable weights. Position size is then set to:
//
//	base_usdt + score * extra_pool_usdt
package signals

import (
	"math"
	"sort"
)

// ScoreWeights defines the relative importance of each scoring dimension.
// Upside + RSIDepth + Volume should sum to 1.0.
type ScoreWeights struct {
	Upside   float64 // weight for BB upside potential (recommended: 0.50)
	RSIDepth float64 // weight for RSI depth below 30  (recommended: 0.30)
	Volume   float64 // weight for 24h USDT volume      (recommended: 0.20)
}

// Candidate holds all the raw values needed to score a single buy signal.
type Candidate struct {
	Symbol        string
	CurrentPrice  float64
	UpperBB       float64 // upper Bollinger Band value at signal candle
	RSI           float64 // RSI value at signal candle
	Volume24hUSDT float64 // estimated 24 h USDT volume (base volume * close price)
	BBBandwidth   float64 // Bollinger Band bandwidth (volatility snapshot)
	VolRatio      float64 // volume ratio relative to average

	// Populated by Score()
	Score          float64 // composite [0,1]
	AllocationUSDT float64 // USDT to spend on this trade
}

// Score computes a composite score for each candidate, sorts the slice in
// descending order of score, and fills AllocationUSDT for each entry.
//
// baseUSDT    — minimum allocation per trade (e.g. MaxPositionUSDT)
// extraUSDT   — additional budget distributed proportionally by score
func Score(candidates []Candidate, w ScoreWeights, baseUSDT, extraUSDT float64) []Candidate {
	if len(candidates) == 0 {
		return candidates
	}

	// --- Compute raw sub-scores ---
	upsideScores := make([]float64, len(candidates))
	rsiScores := make([]float64, len(candidates))
	volumeRaw := make([]float64, len(candidates))

	for i, c := range candidates {
		// 1. Upside: (upperBB - price) / price, capped at 50% and clamped to [0, 1]
		//    The 50% cap prevents a delisted/stale coin with a blown-out BB from dominating.
		const maxUpsidePct = 0.50
		if c.UpperBB > c.CurrentPrice {
			raw := (c.UpperBB - c.CurrentPrice) / c.CurrentPrice
			if raw > maxUpsidePct {
				raw = maxUpsidePct
			}
			upsideScores[i] = raw
		}

		// 2. RSI depth: how far below 30 (deeper = stronger oversold signal)
		//    Normalise against the maximum possible depth (RSI of 0 → depth of 30).
		const oversoldThreshold = 30.0
		if c.RSI < oversoldThreshold {
			rsiScores[i] = (oversoldThreshold - c.RSI) / oversoldThreshold
		}
		// RSI exactly at 30 (the threshold) gives 0; anything above gives 0 too.

		volumeRaw[i] = c.Volume24hUSDT
	}

	// --- Normalise upside and volume to [0, 1] across the candidate set ---
	upsideScores = normalise(upsideScores)
	volumeScores := normalise(volumeRaw)

	// --- Composite score ---
	for i := range candidates {
		candidates[i].Score = w.Upside*upsideScores[i] +
			w.RSIDepth*rsiScores[i] +
			w.Volume*volumeScores[i]
	}

	// --- Sort descending ---
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	// --- Allocation ---
	for i := range candidates {
		candidates[i].AllocationUSDT = baseUSDT + candidates[i].Score*extraUSDT
	}

	return candidates
}

// normalise maps a slice of non-negative floats to [0, 1] using min-max scaling.
// If all values are equal (including all-zero), every element is set to 1.0 so
// that a single-candidate cycle still receives full allocation.
func normalise(values []float64) []float64 {
	if len(values) == 0 {
		return values
	}

	minV, maxV := math.MaxFloat64, -math.MaxFloat64
	for _, v := range values {
		if v < minV {
			minV = v
		}
		if v > maxV {
			maxV = v
		}
	}

	out := make([]float64, len(values))
	rng := maxV - minV
	if rng == 0 {
		// All values identical — give everyone a score of 1.0
		for i := range out {
			out[i] = 1.0
		}
		return out
	}

	for i, v := range values {
		out[i] = (v - minV) / rng
	}
	return out
}
