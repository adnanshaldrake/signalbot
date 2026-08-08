// Package filter provides symbol-level eligibility checks that run before any
// technical analysis. All checks operate on data that is available per-candle,
// so the same logic can be applied both in the live trading loop and inside the
// backtest candle walk — ensuring the backtest faithfully mirrors live behaviour.
package filter

import (
"math"
)

// Config holds all tunable thresholds for the eligibility checks.
type Config struct {
// MinRollingVolUSDT is the minimum rolling 24h USDT volume required to
// consider a symbol eligible for entry. Computed from the last 96 candles
// on a 15m interval (96 x 15m = 24h). Default: 500_000.
MinRollingVolUSDT float64

// MinTradeCount is the minimum number of trades recorded in the rolling
// window. Used to detect wash-trading (high volume, few actual trades).
// NOTE: only available in the live loop via the 24hr ticker; in the
// backtest it is skipped (candles don't carry trade count). Default: 5000.
MinTradeCount int

// MaxPriceChangePct blocks new entries when the 24h price change exceeds
// this percentage in either direction. Catches hyper-volatile / suspicious
// pumps. Default: 40.0.
MaxPriceChangePct float64

// RugCandleBodyPct triggers an emergency-exit flag when any candle's body
// (open - close) / open drops by more than this percentage downward.
// Used both to protect open positions and to block new entries on the
// same symbol. Default: 15.0.
RugCandleBodyPct float64

// MinBBBandwidth is the minimum relative Bollinger Band width
// (upperBB - lowerBB) / middleBB required to allow entry. Prevents
// signals in flat/choppy, zero-volatility markets. Default: 0.01.
MinBBBandwidth float64

// CandleIntervalMinutes is used for context; not currently enforced in the
// filter logic itself (staleness is checked by the caller). Default: 15.
CandleIntervalMinutes int
}

// DefaultConfig returns sensible production defaults.
func DefaultConfig() Config {
return Config{
MinRollingVolUSDT:     500_000,
MinTradeCount:         5_000,
MaxPriceChangePct:     40.0,
RugCandleBodyPct:      15.0,
MinBBBandwidth:        0.01,
CandleIntervalMinutes: 15,
}
}

// Candle is a lightweight view of a single OHLCV bar used by the filter.
// Decoupled from store.Candle and techan types to avoid import cycles.
type Candle struct {
Open   float64
High   float64
Low    float64
Close  float64
Volume float64 // base asset volume
}

// RollingWindow holds the most recent N candles for a symbol. Pass the last
// 96 candles (24h on 15m) to the volume and rug checks.
type RollingWindow []Candle

// VolumeUSDT returns the total USDT volume across all candles in the window
// by summing (base_volume x close_price) for each bar.
func (w RollingWindow) VolumeUSDT() float64 {
total := 0.0
for _, c := range w {
total += c.Volume * c.Close
}
return total
}

// HasRugCandle returns true if any candle in the window has a downward body
// greater than pct percent (i.e. open dropped by pct% to close).
func (w RollingWindow) HasRugCandle(pct float64) bool {
for _, c := range w {
if c.Open <= 0 {
continue
}
bodyDropPct := (c.Open - c.Close) / c.Open * 100
if bodyDropPct >= pct {
return true
}
}
return false
}

// Result carries the outcome of an eligibility check with a human-readable reason.
type Result struct {
Eligible bool
Reason   string
}

func pass() Result              { return Result{Eligible: true, Reason: "ok"} }
func fail(reason string) Result { return Result{Eligible: false, Reason: reason} }

// CheckVolume returns whether the rolling 24h USDT volume meets the minimum.
func CheckVolume(window RollingWindow, cfg Config) Result {
if cfg.MinRollingVolUSDT <= 0 {
return pass()
}
if window.VolumeUSDT() < cfg.MinRollingVolUSDT {
return fail("low_volume")
}
return pass()
}

// CheckRugCandle returns whether any recent candle looks like a rug-pull.
func CheckRugCandle(window RollingWindow, cfg Config) Result {
if cfg.RugCandleBodyPct <= 0 {
return pass()
}
if window.HasRugCandle(cfg.RugCandleBodyPct) {
return fail("rug_candle_detected")
}
return pass()
}

// CheckPriceChange blocks entry if the 24h price change is too extreme.
// openPrice24h is the price at the start of the rolling window; currentPrice is now.
func CheckPriceChange(openPrice24h, currentPrice float64, cfg Config) Result {
if cfg.MaxPriceChangePct <= 0 || openPrice24h <= 0 {
return pass()
}
changePct := math.Abs((currentPrice-openPrice24h)/openPrice24h) * 100
if changePct > cfg.MaxPriceChangePct {
return fail("price_change_too_large")
}
return pass()
}

// CheckBBBandwidth blocks entry when the Bollinger Bands are too narrow.
func CheckBBBandwidth(upperBB, lowerBB, middleBB float64, cfg Config) Result {
if cfg.MinBBBandwidth <= 0 || middleBB <= 0 {
return pass()
}
bandwidth := (upperBB - lowerBB) / middleBB
if bandwidth < cfg.MinBBBandwidth {
return fail("bb_bandwidth_too_narrow")
}
return pass()
}

// CheckTradeCount blocks entry when the 24h trade count is too low.
// Pass tradeCount=0 to skip (e.g. inside backtest where count is unavailable).
func CheckTradeCount(tradeCount int, cfg Config) Result {
if cfg.MinTradeCount <= 0 || tradeCount == 0 {
return pass()
}
if tradeCount < cfg.MinTradeCount {
return fail("low_trade_count")
}
return pass()
}

// IsEligibleForEntry runs all entry-gate checks and returns the first failure,
// or a passing Result if all checks pass.
//
//   - window       - last 96 candles (or all available if fewer)
//   - currentPrice - latest close price
//   - upperBB      - current upper Bollinger Band value
//   - lowerBB      - current lower Bollinger Band value
//   - middleBB     - current middle Bollinger Band (SMA) value
//   - tradeCount   - 24h trade count from exchange ticker; 0 = skip check
func IsEligibleForEntry(
window RollingWindow,
currentPrice float64,
upperBB, lowerBB, middleBB float64,
tradeCount int,
cfg Config,
) Result {
if r := CheckVolume(window, cfg); !r.Eligible {
return r
}
if r := CheckTradeCount(tradeCount, cfg); !r.Eligible {
return r
}
if len(window) > 0 {
if r := CheckPriceChange(window[0].Open, currentPrice, cfg); !r.Eligible {
return r
}
}
if r := CheckRugCandle(window, cfg); !r.Eligible {
return r
}
if r := CheckBBBandwidth(upperBB, lowerBB, middleBB, cfg); !r.Eligible {
return r
}
return pass()
}

// ShouldEmergencyExit checks only the rug-pull condition on open positions.
// Returns true if an emergency exit should be triggered immediately.
func ShouldEmergencyExit(window RollingWindow, cfg Config) bool {
return window.HasRugCandle(cfg.RugCandleBodyPct)
}
