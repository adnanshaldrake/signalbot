package signals

import (
	"math"
	"testing"
)

func TestScore_Empty(t *testing.T) {
	w := ScoreWeights{Upside: 0.5, RSIDepth: 0.3, Volume: 0.2}
	got := Score(nil, w, 100, 50)
	if len(got) != 0 {
		t.Errorf("expected 0 candidates, got %d", len(got))
	}
}

func TestScore_SingleCandidate(t *testing.T) {
	w := ScoreWeights{Upside: 0.5, RSIDepth: 0.3, Volume: 0.2}
	candidates := []Candidate{
		{
			Symbol:        "BTCUSDT",
			CurrentPrice:  100.0,
			UpperBB:       110.0,
			RSI:           25.0,
			Volume24hUSDT: 1_000_000,
		},
	}

	res := Score(candidates, w, 100.0, 50.0)
	if len(res) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(res))
	}

	// When all candidates have the same score/single candidate, normalise gives 1.0
	// 0.5*1.0 + 0.3*(5/30) + 0.2*1.0 = 0.7 + 0.05 = 0.75
	if res[0].Score <= 0 {
		t.Errorf("expected positive score, got %f", res[0].Score)
	}
	wantAlloc := 100.0 + res[0].Score*50.0
	if math.Abs(res[0].AllocationUSDT-wantAlloc) > 1e-6 {
		t.Errorf("expected allocation %f, got %f", wantAlloc, res[0].AllocationUSDT)
	}
}

func TestScore_RankingAndWeights(t *testing.T) {
	w := ScoreWeights{Upside: 0.5, RSIDepth: 0.3, Volume: 0.2}

	candidates := []Candidate{
		{
			Symbol:        "WEAK",
			CurrentPrice:  100.0,
			UpperBB:       101.0, // small upside (1%)
			RSI:           45.0,  // RSI > 30 -> 0 depth score
			Volume24hUSDT: 100_000,
		},
		{
			Symbol:        "STRONG",
			CurrentPrice:  100.0,
			UpperBB:       120.0, // 20% upside
			RSI:           15.0,  // deeply oversold (15/30 = 0.5)
			Volume24hUSDT: 10_000_000,
		},
		{
			Symbol:        "MEDIUM",
			CurrentPrice:  100.0,
			UpperBB:       108.0, // 8% upside
			RSI:           25.0,  // oversold (5/30 = 0.166)
			Volume24hUSDT: 1_000_000,
		},
	}

	res := Score(candidates, w, 50.0, 50.0)

	// Check sorted order: STRONG > MEDIUM > WEAK
	if res[0].Symbol != "STRONG" {
		t.Errorf("expected rank 1 to be STRONG, got %s (score: %f)", res[0].Symbol, res[0].Score)
	}
	if res[1].Symbol != "MEDIUM" {
		t.Errorf("expected rank 2 to be MEDIUM, got %s (score: %f)", res[1].Symbol, res[1].Score)
	}
	if res[2].Symbol != "WEAK" {
		t.Errorf("expected rank 3 to be WEAK, got %s (score: %f)", res[2].Symbol, res[2].Score)
	}

	// Verify allocations follow the ranking
	if res[0].AllocationUSDT <= res[1].AllocationUSDT || res[1].AllocationUSDT <= res[2].AllocationUSDT {
		t.Errorf("allocations not monotonically decreasing: %f, %f, %f",
			res[0].AllocationUSDT, res[1].AllocationUSDT, res[2].AllocationUSDT)
	}
}

func TestScore_UpsideCappedAtFiftyPercent(t *testing.T) {
	w := ScoreWeights{Upside: 1.0, RSIDepth: 0.0, Volume: 0.0}

	candidates := []Candidate{
		{
			Symbol:       "OUTLIER",
			CurrentPrice: 100.0,
			UpperBB:      300.0, // 200% upside -> capped at 50%
		},
		{
			Symbol:       "EXACT_CAP",
			CurrentPrice: 100.0,
			UpperBB:      150.0, // exactly 50% upside
		},
	}

	res := Score(candidates, w, 100.0, 50.0)

	// Both hit the 50% max upside cap, so their raw upside is identical (0.50).
	// Normalisation gives both 1.0.
	if math.Abs(res[0].Score-res[1].Score) > 1e-6 {
		t.Errorf("expected equal scores due to 50%% upside cap, got %f and %f", res[0].Score, res[1].Score)
	}
}

