package filter

import (
	"testing"
)

func TestRollingWindow_VolumeUSDT(t *testing.T) {
	window := RollingWindow{
		{Volume: 10, Close: 100},  // 1000 USDT
		{Volume: 5, Close: 200},   // 1000 USDT
		{Volume: 20, Close: 50},   // 1000 USDT
	}

	got := window.VolumeUSDT()
	want := 3000.0
	if got != want {
		t.Errorf("VolumeUSDT() = %f; want %f", got, want)
	}
}

func TestCheckVolume(t *testing.T) {
	cfg := Config{MinRollingVolUSDT: 500_000}

	// 1. Below threshold
	lowVolWindow := RollingWindow{
		{Volume: 1000, Close: 100}, // 100,000 USDT
	}
	resLow := CheckVolume(lowVolWindow, cfg)
	if resLow.Eligible || resLow.Reason != "low_volume" {
		t.Errorf("expected fail with low_volume, got %+v", resLow)
	}

	// 2. Meets threshold
	goodVolWindow := RollingWindow{
		{Volume: 6000, Close: 100}, // 600,000 USDT
	}
	resGood := CheckVolume(goodVolWindow, cfg)
	if !resGood.Eligible {
		t.Errorf("expected pass, got %+v", resGood)
	}

	// 3. Disabled filter (<= 0)
	cfgDisabled := Config{MinRollingVolUSDT: 0}
	resDisabled := CheckVolume(lowVolWindow, cfgDisabled)
	if !resDisabled.Eligible {
		t.Errorf("expected pass when MinRollingVolUSDT <= 0")
	}
}

func TestRugCandleDetection(t *testing.T) {
	cfg := Config{RugCandleBodyPct: 15.0}

	// Normal candle: open 100, close 90 (10% drop, < 15%)
	normalWindow := RollingWindow{
		{Open: 100, Close: 90},
		{Open: 90, Close: 95},
	}
	if normalWindow.HasRugCandle(cfg.RugCandleBodyPct) {
		t.Errorf("did not expect rug candle in normal window")
	}
	if ShouldEmergencyExit(normalWindow, cfg) {
		t.Errorf("did not expect emergency exit for normal window")
	}
	resNormal := CheckRugCandle(normalWindow, cfg)
	if !resNormal.Eligible {
		t.Errorf("expected normal window to be eligible, got %+v", resNormal)
	}

	// Rug candle: open 100, close 80 (20% drop, >= 15%)
	rugWindow := RollingWindow{
		{Open: 100, Close: 90},
		{Open: 100, Close: 80}, // rug!
	}
	if !rugWindow.HasRugCandle(cfg.RugCandleBodyPct) {
		t.Errorf("expected rug candle to be detected")
	}
	if !ShouldEmergencyExit(rugWindow, cfg) {
		t.Errorf("expected ShouldEmergencyExit to return true")
	}
	resRug := CheckRugCandle(rugWindow, cfg)
	if resRug.Eligible || resRug.Reason != "rug_candle_detected" {
		t.Errorf("expected rug_candle_detected fail, got %+v", resRug)
	}
}

func TestCheckTradeCount(t *testing.T) {
	cfg := Config{MinTradeCount: 5000}

	// Below threshold
	resLow := CheckTradeCount(4999, cfg)
	if resLow.Eligible || resLow.Reason != "low_trade_count" {
		t.Errorf("expected low_trade_count fail, got %+v", resLow)
	}

	// Meets threshold
	resGood := CheckTradeCount(5000, cfg)
	if !resGood.Eligible {
		t.Errorf("expected pass, got %+v", resGood)
	}

	// 0 count (skipped for backtests)
	resZero := CheckTradeCount(0, cfg)
	if !resZero.Eligible {
		t.Errorf("expected tradeCount=0 to skip check and pass")
	}
}

func TestCheckPriceChange(t *testing.T) {
	cfg := Config{MaxPriceChangePct: 40.0}

	// 30% upward pump -> Allowed
	resPumpOk := CheckPriceChange(100.0, 130.0, cfg)
	if !resPumpOk.Eligible {
		t.Errorf("expected 30%% change to pass, got %+v", resPumpOk)
	}

	// 45% upward pump -> Denied
	resPumpExtreme := CheckPriceChange(100.0, 145.0, cfg)
	if resPumpExtreme.Eligible || resPumpExtreme.Reason != "price_change_too_large" {
		t.Errorf("expected price_change_too_large fail, got %+v", resPumpExtreme)
	}

	// 45% downward dump -> Denied
	resDumpExtreme := CheckPriceChange(100.0, 55.0, cfg)
	if resDumpExtreme.Eligible || resDumpExtreme.Reason != "price_change_too_large" {
		t.Errorf("expected price_change_too_large fail on dump, got %+v", resDumpExtreme)
	}
}

func TestCheckBBBandwidth(t *testing.T) {
	cfg := Config{MinBBBandwidth: 0.02} // 2% minimum width

	// Flat market: middle = 100, upper = 100.5, lower = 99.5 -> width = 1.0 / 100 = 0.01 (< 0.02)
	resNarrow := CheckBBBandwidth(100.5, 99.5, 100.0, cfg)
	if resNarrow.Eligible || resNarrow.Reason != "bb_bandwidth_too_narrow" {
		t.Errorf("expected bb_bandwidth_too_narrow fail, got %+v", resNarrow)
	}

	// Healthy volatility: middle = 100, upper = 102.0, lower = 98.0 -> width = 4.0 / 100 = 0.04 (>= 0.02)
	resGood := CheckBBBandwidth(102.0, 98.0, 100.0, cfg)
	if !resGood.Eligible {
		t.Errorf("expected pass, got %+v", resGood)
	}
}

func TestIsEligibleForEntry(t *testing.T) {
	cfg := DefaultConfig()
	// Default: MinRollingVolUSDT=500_000, MinTradeCount=5000, MaxPriceChangePct=40, RugCandleBodyPct=15, MinBBBandwidth=0.01

	goodWindow := RollingWindow{
		{Open: 100, Close: 100, Volume: 6000}, // 600,000 USDT volume
	}

	// All passing
	res := IsEligibleForEntry(goodWindow, 100.0, 102.0, 98.0, 100.0, 6000, cfg)
	if !res.Eligible {
		t.Fatalf("expected all checks to pass, got %+v", res)
	}

	// Volume failure
	lowVolWindow := RollingWindow{
		{Open: 100, Close: 100, Volume: 1000},
	}
	resFail := IsEligibleForEntry(lowVolWindow, 100.0, 102.0, 98.0, 100.0, 6000, cfg)
	if resFail.Eligible || resFail.Reason != "low_volume" {
		t.Errorf("expected low_volume failure, got %+v", resFail)
	}
}

