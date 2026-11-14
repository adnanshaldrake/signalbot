package metrics

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adnanshaldrake/signalbot/pkg/store"
)

func TestBuildPublicTelemetry_EquityAndDrawdown(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

	trades := []store.Trade{
		{
			Symbol:          "BTCUSDT",
			Side:            "BUY",
			EntryPrice:      50000,
			ExitPrice:       55000,
			PnL:             100,
			PnLPct:          10.0,
			ExitReason:      "TAKE_PROFIT",
			HoldDurationSec: 3600,
			OpenedAt:        now.Add(-5 * time.Hour),
			ClosedAt:        now.Add(-4 * time.Hour),
		},
		{
			Symbol:          "ETHUSDT",
			Side:            "BUY",
			EntryPrice:      3000,
			ExitPrice:       2835,
			PnL:             -55,
			PnLPct:          -5.5,
			ExitReason:      "STOP_LOSS",
			HoldDurationSec: 1800,
			OpenedAt:        now.Add(-3 * time.Hour),
			ClosedAt:        now.Add(-2 * time.Hour),
		},
		{
			Symbol:          "SOLUSDT",
			Side:            "BUY",
			EntryPrice:      150,
			ExitPrice:       173.25,
			PnL:             155,
			PnLPct:          15.5,
			ExitReason:      "TAKE_PROFIT",
			HoldDurationSec: 2400,
			OpenedAt:        now.Add(-90 * time.Minute),
			ClosedAt:        now.Add(-30 * time.Minute),
		},
	}

	positions := []store.StoredPosition{
		{
			Symbol:     "NEARUSDT",
			Side:       "BUY",
			EntryPrice: 5.5,
			OpenedAt:   now.Add(-15 * time.Minute),
		},
	}

	tel := BuildPublicTelemetry(trades, positions, TelemetryOptions{
		StartingBalanceUSDT: 1000.0,
		Mode:                "DEMONET",
		Now:                 now,
	})

	p := tel.Performance
	if p.Mode != "DEMONET" {
		t.Errorf("expected Mode DEMONET, got %s", p.Mode)
	}
	if !p.GeneratedAt.Equal(now) {
		t.Errorf("expected GeneratedAt %v, got %v", now, p.GeneratedAt)
	}
	if math.Abs(p.CurrentEquityUSDT-1200.0) > 1e-6 {
		t.Errorf("expected CurrentEquityUSDT 1200, got %f", p.CurrentEquityUSDT)
	}
	if math.Abs(p.TotalPnLUSDT-200.0) > 1e-6 {
		t.Errorf("expected TotalPnLUSDT 200, got %f", p.TotalPnLUSDT)
	}
	if math.Abs(p.TotalReturnPct-20.0) > 1e-6 {
		t.Errorf("expected TotalReturnPct 20.0, got %f", p.TotalReturnPct)
	}
	// Peak was 1100, trough was 1045 -> drawdown = 55 USDT (5.0%)
	if math.Abs(p.MaxDrawdownUSDT-55.0) > 1e-6 {
		t.Errorf("expected MaxDrawdownUSDT 55, got %f", p.MaxDrawdownUSDT)
	}
	if math.Abs(p.MaxDrawdownPct-5.0) > 1e-6 {
		t.Errorf("expected MaxDrawdownPct 5.0, got %f", p.MaxDrawdownPct)
	}
	if len(p.EquityCurve) != 4 {
		t.Fatalf("expected 4 equity curve points (anchor + 3 trades), got %d", len(p.EquityCurve))
	}
	if p.EquityCurve[0].EquityUSDT != 1000.0 || p.EquityCurve[2].EquityUSDT != 1045.0 {
		t.Errorf("unexpected equity curve values: %+v", p.EquityCurve)
	}
	if len(p.OpenPositions) != 1 || p.OpenPositions[0].Symbol != "NEARUSDT" {
		t.Errorf("expected 1 open position NEARUSDT, got %+v", p.OpenPositions)
	}

	// Trades feed should be reverse chronological (SOLUSDT first)
	if tel.Trades.TotalTrades != 3 || tel.Trades.Trades[0].Symbol != "SOLUSDT" {
		t.Errorf("expected trades ordered newest-first with SOLUSDT at index 0, got %+v", tel.Trades.Trades)
	}
}

func TestBuildPublicTelemetry_ScrubsSensitiveFields(t *testing.T) {
	now := time.Now().UTC()
	trades := []store.Trade{
		{
			ID:            42,
			OrderID:       999888777,
			Symbol:        "BTCUSDT",
			Side:          "BUY",
			EntryPrice:    60000,
			ExitPrice:     61200,
			Quantity:      0.5,
			QuoteSpent:    30000,
			PnL:           600,
			PnLPct:        2.0,
			ExitReason:    "TAKE_PROFIT",
			EntryRSI:      21.45,
			EntryBBWidth:  0.0312,
			EntryScore:    0.9412,
			EntryVolRatio: 3.1415,
			OpenedAt:      now.Add(-time.Hour),
			ClosedAt:      now,
		},
	}
	positions := []store.StoredPosition{
		{
			Symbol:        "ETHUSDT",
			Side:          "BUY",
			EntryPrice:    3200,
			Quantity:      2.5,
			QuoteSpent:    8000,
			OrderID:       555444333,
			EntryRSI:      19.1,
			EntryScore:    0.88,
			EntryVolRatio: 2.7,
			OpenedAt:      now,
		},
	}

	tel := BuildPublicTelemetry(trades, positions, TelemetryOptions{Now: now})
	raw, err := json.Marshal(tel)
	if err != nil {
		t.Fatalf("marshal telemetry failed: %v", err)
	}
	s := string(raw)

	forbidden := []string{
		"order_id",
		"quote_spent",
		"quantity",
		"entry_rsi",
		"entry_bb_width",
		"entry_score",
		"entry_vol_ratio",
		"999888777",
		"555444333",
		"0.9412",
		"3.1415",
	}
	for _, token := range forbidden {
		if strings.Contains(s, token) {
			t.Errorf("scrubbed telemetry leaked forbidden field/value %q in JSON: %s", token, s)
		}
	}
}

func TestBuildPublicTelemetry_EmptyStateNonNilSlices(t *testing.T) {
	tel := BuildPublicTelemetry(nil, nil, TelemetryOptions{})
	raw, err := json.Marshal(tel)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	s := string(raw)
	if strings.Contains(s, ":null") {
		t.Errorf("expected empty JSON arrays instead of null, got: %s", s)
	}
	if tel.Performance.CurrentEquityUSDT != defaultStartingBalanceUSDT {
		t.Errorf("expected default equity %f, got %f", defaultStartingBalanceUSDT, tel.Performance.CurrentEquityUSDT)
	}
}

func TestExportAuditTelemetryAndWriteFiles(t *testing.T) {
	tmpDir := t.TempDir()
	db, err := store.Open(filepath.Join(tmpDir, "telemetry.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := db.InsertTrade(store.Trade{
		OrderID:    1001,
		Symbol:     "BTCUSDT",
		Side:       "BUY",
		EntryPrice: 50000,
		ExitPrice:  51000,
		Quantity:   0.01,
		QuoteSpent: 500,
		PnL:        10.0,
		PnLPct:     2.0,
		ExitReason: "TAKE_PROFIT",
		OpenedAt:   now.Add(-time.Hour),
		ClosedAt:   now,
	}); err != nil {
		t.Fatalf("insert trade: %v", err)
	}

	var buf bytes.Buffer
	opts := TelemetryOptions{StartingBalanceUSDT: 2000, Mode: "PAPER", Now: now}
	if err := ExportAuditTelemetry(&buf, db, opts); err != nil {
		t.Fatalf("ExportAuditTelemetry failed: %v", err)
	}

	var decoded PublicTelemetry
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal exported telemetry: %v", err)
	}
	if decoded.Performance.TotalTrades != 1 || decoded.Performance.CurrentEquityUSDT != 2010.0 {
		t.Errorf("unexpected decoded performance: %+v", decoded.Performance)
	}

	outDir := filepath.Join(tmpDir, "dist", "data")
	if _, err := WriteTelemetryFiles(outDir, db, opts); err != nil {
		t.Fatalf("WriteTelemetryFiles failed: %v", err)
	}

	for _, name := range []string{"performance.json", "trades.json"} {
		data, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("missing generated file %s: %v", name, err)
		}
		if len(data) == 0 {
			t.Errorf("generated file %s is empty", name)
		}
	}
}

