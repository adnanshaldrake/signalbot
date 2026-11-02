package config

import (
	"os"
	"testing"
)

func TestConfig15mStrategyDefaults(t *testing.T) {
	// Temporarily clear environment variables to test defaults
	keys := []string{
		"BINANCE_API_KEY", "BINANCE_API_SECRET", "PAPER_TRADING",
		"MAX_HOLD_HOURS", "MIN_VOL_RATIO", "MIN_SIGNAL_SCORE",
	}
	for _, k := range keys {
		old := os.Getenv(k)
		defer os.Setenv(k, old)
		os.Unsetenv(k)
	}

	os.Setenv("BINANCE_API_KEY", "test_key")
	os.Setenv("BINANCE_API_SECRET", "test_secret")
	os.Setenv("PAPER_TRADING", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.MaxHoldHours != 20.0 {
		t.Errorf("expected default MaxHoldHours=20.0, got %f", cfg.MaxHoldHours)
	}
	if cfg.MinVolRatio != 1.0 {
		t.Errorf("expected default MinVolRatio=1.0, got %f", cfg.MinVolRatio)
	}
	if cfg.MinSignalScore != 0.50 {
		t.Errorf("expected default MinSignalScore=0.50, got %f", cfg.MinSignalScore)
	}
}

func TestConfig15mStrategyOverrides(t *testing.T) {
	keys := []string{
		"BINANCE_API_KEY", "BINANCE_API_SECRET", "PAPER_TRADING",
		"MAX_HOLD_HOURS", "MIN_VOL_RATIO", "MIN_SIGNAL_SCORE",
	}
	for _, k := range keys {
		old := os.Getenv(k)
		defer os.Setenv(k, old)
		os.Unsetenv(k)
	}

	os.Setenv("BINANCE_API_KEY", "test_key")
	os.Setenv("BINANCE_API_SECRET", "test_secret")
	os.Setenv("PAPER_TRADING", "true")
	os.Setenv("MAX_HOLD_HOURS", "12.5")
	os.Setenv("MIN_VOL_RATIO", "1.5")
	os.Setenv("MIN_SIGNAL_SCORE", "0.65")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.MaxHoldHours != 12.5 {
		t.Errorf("expected MaxHoldHours=12.5, got %f", cfg.MaxHoldHours)
	}
	if cfg.MinVolRatio != 1.5 {
		t.Errorf("expected MinVolRatio=1.5, got %f", cfg.MinVolRatio)
	}
	if cfg.MinSignalScore != 0.65 {
		t.Errorf("expected MinSignalScore=0.65, got %f", cfg.MinSignalScore)
	}
}

