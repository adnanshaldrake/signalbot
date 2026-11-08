package screener_test

import (
	"errors"
	"testing"

	"github.com/adnanshaldrake/signalbot/pkg/config"
	"github.com/adnanshaldrake/signalbot/pkg/screener"
	"github.com/adnanshaldrake/signalbot/pkg/store"
)

// mockCustomScreener implements screener.Screener for testing.
type mockCustomScreener struct {
	symbols []string
	err     error
}

func (m *mockCustomScreener) Screen() ([]string, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.symbols, nil
}

func TestScreenerInterfaceCompliance(t *testing.T) {
	// 1. Verify StaticScreener satisfies Screener
	var _ screener.Screener = screener.NewStaticScreener([]string{"BTCUSDT", "ETHUSDT"})

	// 2. Verify VolumeBacktestScreener satisfies Screener
	var _ screener.Screener = screener.NewVolumeBacktestScreener(&config.BotConfig{}, &store.DB{})

	// 3. Test StaticScreener
	expected := []string{"BTCUSDT", "SOLUSDT"}
	staticScr := screener.NewStaticScreener(expected)
	got, err := staticScr.Screen()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != len(expected) {
		t.Fatalf("expected %d symbols, got %d", len(expected), len(got))
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("expected %s at index %d, got %s", expected[i], i, got[i])
		}
	}

	// 4. Test custom mock screener
	custom := &mockCustomScreener{symbols: []string{"NEARUSDT"}}
	symbols, err := custom.Screen()
	if err != nil || len(symbols) != 1 || symbols[0] != "NEARUSDT" {
		t.Errorf("custom screener failed, got symbols: %v, err: %v", symbols, err)
	}

	// 5. Test custom mock screener returning error
	customErr := &mockCustomScreener{err: errors.New("screener network failure")}
	_, err = customErr.Screen()
	if err == nil {
		t.Errorf("expected error from customErr, got nil")
	}
}
