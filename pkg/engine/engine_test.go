package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/adnanshaldrake/signalbot/pkg/config"
	"github.com/adnanshaldrake/signalbot/pkg/screener"
	"github.com/adnanshaldrake/signalbot/pkg/store"
	"github.com/adnanshaldrake/signalbot/pkg/strategy"
)

type dummyStrategy struct{}

func (d *dummyStrategy) ShouldEnter(index int) bool { return false }
func (d *dummyStrategy) ShouldExit(index int) bool  { return false }

func TestEngine_WithStrategyAndScreener(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test store: %v", err)
	}
	defer db.Close()

	cfg := &config.BotConfig{
		PaperTrading:     true,
		UseTestnet:       true,
		LoopInterval:     10 * time.Millisecond,
		CandleInterval:   "15m",
		CandleLimit:      50,
		Watchlist:        []string{"BTCUSDT", "ETHUSDT"},
		ScreenOnStartup:  false,
		MinCandlesRequired: 50,
	}

	customScreener := screener.NewStaticScreener([]string{"SOLUSDT", "ADAUSDT"})
	strategyCalled := false

	eng := New(cfg, db).
		WithScreener(customScreener).
		WithStrategy(func(series *strategy.Series) (strategy.Evaluator, error) {
			strategyCalled = true
			return &dummyStrategy{}, nil
		})

	watchlist, err := eng.ResolveWatchlist()
	if err != nil {
		t.Fatalf("ResolveWatchlist failed: %v", err)
	}
	if len(watchlist) != 2 || watchlist[0] != "SOLUSDT" {
		t.Errorf("expected custom screener watchlist, got %v", watchlist)
	}

	// Verify strategy evaluator can be invoked
	eval, err := eng.strategyFactory(&strategy.Series{})
	if err != nil || !strategyCalled || eval == nil {
		t.Errorf("expected custom strategy factory to be wired")
	}

	// Verify context cancellation shuts down gracefully
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	_ = eng.Run(ctx)
}
