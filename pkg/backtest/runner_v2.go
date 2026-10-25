// runner_v2.go — chunked backtest runner backed by pkg/strategy (native float64).
// This file lives alongside runner.go in the same package. It exposes RunnerV2,
// which has the same API as Runner but uses zero techan/big dependencies.
//
// To switch the backtest command to V2, change cmd/main.go to call:
//
//	backtest.NewRunnerV2(db, positionUSDT)
//
// instead of backtest.NewRunner(db, positionUSDT). Everything else stays the same.
package backtest

import (
	"fmt"
	"log/slog"
	"time"

	"adnan/binance-bot/pkg/filter"
	"adnan/binance-bot/pkg/store"
	"adnan/binance-bot/pkg/strategy"
)

// RunnerV2 is a drop-in replacement for Runner that uses pkg/strategy instead
// of techan. It has an identical public API (NewRunnerV2 / WithRisk / WithFilters
// / RunSymbolV2 / PrintReport — PrintReport is shared with Runner).
type RunnerV2 struct {
	db             *store.DB
	positionUSDT   float64
	stopLossPct    float64
	takeProfitPct  float64
	maxHoldCandles int
	filterCfg      *FilterConfig // reuses the same FilterConfig type from runner.go
	useStrategy2   bool
}

// NewRunnerV2 creates a RunnerV2 with the same defaults as NewRunner.
func NewRunnerV2(db *store.DB, positionUSDT float64) *RunnerV2 {
	return &RunnerV2{
		db:             db,
		positionUSDT:   positionUSDT,
		stopLossPct:    2.0,
		takeProfitPct:  4.0,
		maxHoldCandles: 192,
		useStrategy2:   false,
	}
}

// WithRisk overrides the default risk parameters.
func (r *RunnerV2) WithRisk(stopLossPct, takeProfitPct float64, maxHoldCandles int) *RunnerV2 {
	r.stopLossPct = stopLossPct
	r.takeProfitPct = takeProfitPct
	r.maxHoldCandles = maxHoldCandles
	return r
}

// WithFilters enables per-candle eligibility checks (mirrors Runner.WithFilters).
func (r *RunnerV2) WithFilters(cfg FilterConfig) *RunnerV2 {
	r.filterCfg = &cfg
	r.useStrategy2 = true
	return r
}

func (r *RunnerV2) minCandles() int {
	if r.useStrategy2 {
		return 210
	}
	return 50
}

// candlesToSeries converts a []store.Candle slice to a *strategy.Series.
func candlesToSeries(candles []store.Candle) *strategy.Series {
	s := &strategy.Series{Candles: make([]strategy.Candle, len(candles))}
	for i, c := range candles {
		s.Candles[i] = strategy.Candle{
			OpenTime: c.OpenTime,
			Open:     c.Open,
			High:     c.High,
			Low:      c.Low,
			Close:    c.Close,
			Volume:   c.Volume,
		}
	}
	return s
}

// RunSymbolV2 is the float64-native equivalent of Runner.RunSymbol.
// It processes data in 30-day chunks (identical windowing logic) but builds
// a pkg/strategy.Strategy instead of a techan.TimeSeries per chunk.
func (r *RunnerV2) RunSymbolV2(symbol, interval string) (*Result, error) {
	startRange, endRange, err := r.db.GetCandleRange(symbol, interval)
	if err != nil {
		return nil, fmt.Errorf("backtestV2: get candle range for %s: %w", symbol, err)
	}

	result := &Result{
		Symbol:           symbol,
		FilterRejections: make(map[string]int),
	}

	var fCfg filter.Config
	var volWindow int
	if r.filterCfg != nil {
		fCfg = filter.Config{
			MinRollingVolUSDT: r.filterCfg.MinRollingVolUSDT,
			RugCandleBodyPct:  r.filterCfg.RugCandleBodyPct,
			MaxPriceChangePct: r.filterCfg.MaxPriceChangePct,
			MinBBBandwidth:    r.filterCfg.MinBBBandwidth,
		}
		volWindow = r.filterCfg.VolumeWindow
		if volWindow == 0 {
			volWindow = 96
		}
	}

	var openTrade *TradeRecord
	var openTradeHeldCandles int
	peakPnL := 0.0
	runningPnL := 0.0

	closeTrade := func(exitPrice float64, exitTime time.Time, reason string) {
		pnl := (exitPrice - openTrade.Price) / openTrade.Price * openTrade.QuoteValue
		pnlPct := (exitPrice - openTrade.Price) / openTrade.Price * 100
		result.Trades = append(result.Trades, TradeRecord{
			Symbol:     symbol,
			Side:       "SELL",
			Price:      exitPrice,
			Qty:        openTrade.Qty,
			QuoteValue: openTrade.QuoteValue + pnl,
			Time:       exitTime,
			PnLUSDT:    pnl,
			PnLPct:     pnlPct,
			ExitReason: reason,
		})
		result.TotalTrades++
		result.TotalPnLUSDT += pnl
		if pnl > 0 {
			result.WinTrades++
		} else {
			result.LossTrades++
		}
		runningPnL += pnl
		if runningPnL > peakPnL {
			peakPnL = runningPnL
		}
		if dd := peakPnL - runningPnL; dd > result.MaxDrawdown {
			result.MaxDrawdown = dd
		}
		openTrade = nil
	}

	minC := r.minCandles()
	chunkDuration := 30 * 24 * time.Hour
	intervalDuration := parseIntervalDuration(interval) // reuse from runner.go
	warmupDuration := time.Duration(minC+volWindow+10) * intervalDuration

	for currentStart := startRange; !currentStart.After(endRange); currentStart = currentStart.Add(chunkDuration) {
		currentEnd := currentStart.Add(chunkDuration)
		if currentEnd.After(endRange) {
			currentEnd = endRange
		}

		fetchStart := currentStart.Add(-warmupDuration)
		if fetchStart.Before(startRange) {
			fetchStart = startRange
		}

		candles, err := r.db.GetCandles(symbol, interval, fetchStart, currentEnd)
		if err != nil {
			return nil, fmt.Errorf("backtestV2: load candles for %s: %w", symbol, err)
		}
		if len(candles) < minC {
			if currentStart == startRange {
				return nil, fmt.Errorf("backtestV2: not enough candles for %s (got %d, need >=%d)", symbol, len(candles), minC)
			}
			continue
		}

		// Build float64 series and precompute all indicators in one pass
		ser := candlesToSeries(candles)
		var strat *strategy.Strategy
		if r.useStrategy2 {
			strat, err = strategy.Strategy2(ser)
		} else {
			strat, err = strategy.Strategy1(ser)
		}
		if err != nil {
			return nil, fmt.Errorf("backtestV2: build strategy for %s: %w", symbol, err)
		}

		for i := minC; i < len(candles); i++ {
			if candles[i].OpenTime.Before(currentStart) {
				continue
			}
			currentPrice := candles[i].Close

			// Emergency rug-pull exit
			if openTrade != nil && r.filterCfg != nil && r.filterCfg.RugCandleBodyPct > 0 {
				recentWindow := buildRollingWindow(candles, i+1, 3)
				if filter.ShouldEmergencyExit(recentWindow, fCfg) {
					closeTrade(currentPrice, candles[i].OpenTime, "rug_exit")
					result.FilterRejections["rug_exit"]++
					continue
				}
			}

			if openTrade == nil && strat.ShouldEnter(i) {
				if r.filterCfg != nil {
					window := buildRollingWindow(candles, i+1, volWindow)
					eligibility := filter.IsEligibleForEntry(
						window, currentPrice,
						strat.UpperBB(i), strat.LowerBB(i), strat.MidBB(i),
						0,
						fCfg,
					)
					if !eligibility.Eligible {
						result.FilterRejections[eligibility.Reason]++
						continue
					}
				}
				qty := r.positionUSDT / currentPrice
				openTrade = &TradeRecord{
					Symbol:     symbol,
					Side:       "BUY",
					Price:      currentPrice,
					Qty:        qty,
					QuoteValue: r.positionUSDT,
					Time:       candles[i].OpenTime,
				}
				openTradeHeldCandles = 0
				result.Trades = append(result.Trades, *openTrade)
				result.TotalTrades++

			} else if openTrade != nil {
				openTradeHeldCandles++
				stopPrice := openTrade.Price * (1 - r.stopLossPct/100)
				targetPrice := openTrade.Price * (1 + r.takeProfitPct/100)
				switch {
				case currentPrice <= stopPrice:
					closeTrade(currentPrice, candles[i].OpenTime, "stop_loss")
				case currentPrice >= targetPrice:
					closeTrade(currentPrice, candles[i].OpenTime, "take_profit")
				case r.maxHoldCandles > 0 && openTradeHeldCandles >= r.maxHoldCandles:
					closeTrade(currentPrice, candles[i].OpenTime, "max_hold")
				case strat.ShouldExit(i):
					closeTrade(currentPrice, candles[i].OpenTime, "strategy")
				}
			}
		}
	}

	if result.WinTrades+result.LossTrades > 0 {
		result.WinRate = float64(result.WinTrades) / float64(result.WinTrades+result.LossTrades) * 100
	}
	return result, nil
}

// PrintReportV2 is an alias for PrintReport — both use the same Result type.
func PrintReportV2(res *Result) {
	slog.Info("=== Backtest Report (V2) ===",
		"symbol", res.Symbol,
		"total_trades", res.TotalTrades,
		"wins", res.WinTrades,
		"losses", res.LossTrades,
		"win_rate_pct", fmt.Sprintf("%.1f%%", res.WinRate),
		"total_pnl_usdt", fmt.Sprintf("$%.4f", res.TotalPnLUSDT),
		"max_drawdown_usdt", fmt.Sprintf("$%.4f", res.MaxDrawdown),
	)
	if len(res.FilterRejections) > 0 {
		for reason, count := range res.FilterRejections {
			slog.Info("filter_blocked", "symbol", res.Symbol, "reason", reason, "count", count)
		}
	}
	for _, t := range res.Trades {
		if t.Side == "SELL" {
			slog.Info("trade",
				"symbol", t.Symbol,
				"time", t.Time.Format("2006-01-02 15:04"),
				"exit_reason", t.ExitReason,
				"price", fmt.Sprintf("%.6f", t.Price),
				"pnl_usdt", fmt.Sprintf("$%.4f", t.PnLUSDT),
				"pnl_pct", fmt.Sprintf("%.2f%%", t.PnLPct),
			)
		}
	}
}
