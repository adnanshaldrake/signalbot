package binance

import (
	"math"
	"testing"
)

func TestFillSummary(t *testing.T) {
	t.Run("vwap across multiple fills", func(t *testing.T) {
		r := &OrderResponse{
			ExecutedQty:         "3",
			CummulativeQuoteQty: "10.5",
			Fills: []Fill{
				{Price: "3.0", Quantity: "1"},
				{Price: "3.75", Quantity: "2"},
			},
		}
		price, spent, ok := r.FillSummary()
		if !ok || math.Abs(price-3.5) > 1e-9 || math.Abs(spent-10.5) > 1e-9 {
			t.Errorf("got price=%v spent=%v ok=%v, want 3.5 10.5 true", price, spent, ok)
		}
	})

	t.Run("fallback without fills", func(t *testing.T) {
		r := &OrderResponse{ExecutedQty: "4", CummulativeQuoteQty: "20"}
		price, spent, ok := r.FillSummary()
		if !ok || price != 5 || spent != 20 {
			t.Errorf("got price=%v spent=%v ok=%v, want 5 20 true", price, spent, ok)
		}
	})

	t.Run("no data", func(t *testing.T) {
		if _, _, ok := (&OrderResponse{}).FillSummary(); ok {
			t.Error("expected ok=false for empty response")
		}
	})
}
