package risk

import (
	"math"
	"testing"

	"github.com/adnanshaldrake/signalbot/pkg/state"
)

func TestCalcPnL(t *testing.T) {
	// Bought 100 USDT at 10.0 → 10 units; 0.1% fee taken in base → net 9.99 held.
	pos := &state.Position{EntryPrice: 10, Quantity: 9.99, QuoteSpent: 100}

	tests := []struct {
		name  string
		exit  float64
		fee   float64
		wantP float64
	}{
		// flat price still loses buy + sell fees
		{"flat price loses fees", 10, 0.1, 9.99*10*0.999 - 100},
		{"take profit 4%", 10.4, 0.1, 9.99*10.4*0.999 - 100},
		{"no fee, net qty only", 10, 0, 9.99*10 - 100},
	}
	for _, tc := range tests {
		got := CalcPnL(pos, tc.exit, tc.fee)
		if math.Abs(got-tc.wantP) > 1e-9 {
			t.Errorf("%s: got %.6f want %.6f", tc.name, got, tc.wantP)
		}
	}

	if got := CalcPnL(pos, 10, 0.1); got >= 0 {
		t.Errorf("flat exit should be a net loss, got %.6f", got)
	}
}
