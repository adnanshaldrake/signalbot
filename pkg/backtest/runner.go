// Package backtest replays stored candle data through a strategy and reports P&L.
package backtest

import (
	"fmt"
	"log/slog"
	"time"

	"adnan/binance-bot/pkg/app"
	"adnan/binance-bot/pkg/filter"
	"adnan/binance-bot/pkg/store"

	"github.com/sdcoffey/big"
	"github.com/sdcoffey/techan"
)

// TradeRecord captures a single simulated trade during backtesting.
type TradeRecord struct {
	Symbol     string
	Side       string
	Price      float64
	Qty        float64
	QuoteValue float64
	Time       time.Time
	PnLUSDT    float64 // filled on SELL
	PnLPct     float64 // filled on SELL
	ExitReason string  // "strategy", "stop_loss", "take_profit", "max_hold", "rug_exit"
}

// Result summarises backtest performance for a single symbol.
type Result struct {
	Symbol           string
	TotalTrades      int
	WinTrades        int
	LossTrades       int
	TotalPnLUSDT     float64
	MaxDrawdown      float64
	WinRate          float64
	Trades           []TradeRecord
	FilterRejections map[string]int // reason -> count of candles where entry was filter-blocked
}

// FilterConfig mirrors the subset of filter.Config used inside the backtest walk.
// Pass zero values to disable individual checks.
type FilterConfig struct {
	MinRollingVolUSDT float64 // 0 = disabled
	RugCandleBodyPct  float64 // 0 = disabled
	MaxPriceChangePct float64 // 0 = disabled
	MinBBBandwidth    float64 // 0 = disabled
	RequireEMATrend   bool    // if true, EMA50 must be > EMA200
	// VolumeWindow is how many candles to sum for the rolling volume check.
	// Defaults to 96 (24h on 15m bars) if zero.
	VolumeWindow int
}

// Runner executes a backtest over stored historical data.
type Runner struct {
	db             *store.DB
	positionUSDT   float64
	stopLossPct    float64 // e.g. 2.0 = exit if price drops 2% below entry
	takeProfitPct  float64 // e.g. 4.0 = exit if price rises 4% above entry
	maxHoldCandles int     // e.g. 192 = 48h on 15m bars; 0 = no limit
	filterCfg      *FilterConfig
	useStrategy2   bool
}

// NewRunner creates a new backtest Runner with default risk parameters.
// By default it uses Strategy2 (with EMA trend filter) when filters are enabled.
func NewRunner(db *store.DB, positionUSDT float64) *Runner {
	return &Runner{
		db:             db,
		positionUSDT:   positionUSDT,
		stopLossPct:    2.0,
		takeProfitPct:  4.0,
		maxHoldCandles: 192, // 48 hours at 15m intervals
		useStrategy2:   false,
	}
}

// WithRisk overrides the default risk parameters.
func (r *Runner) WithRisk(stopLossPct, takeProfitPct float64, maxHoldCandles int) *Runner {
	r.stopLossPct = stopLossPct
	r.takeProfitPct = takeProfitPct
	r.maxHoldCandles = maxHoldCandles
	return r
}

// WithFilters enables per-candle eligibility checks during the backtest walk.
// This makes the backtest faithfully mirror what the live bot would do, preventing
// survivorship bias from pre-filtering on today's data.
func (r *Runner) WithFilters(cfg FilterConfig) *Runner {
	r.filterCfg = &cfg
	r.useStrategy2 = true // Strategy2 includes EMA trend filter in its rules
	return r
}

// minCandles returns the minimum warm-up required based on whether Strategy2 is active.
func (r *Runner) minCandles() int {
	if r.useStrategy2 {
		return 210 // EMA200 needs 200 bars + a few for stability
	}
	return 50
}

// buildRollingWindow extracts the last N candles up to (but not including) idx.
func buildRollingWindow(candles []store.Candle, idx, windowSize int) filter.RollingWindow {
	start := idx - windowSize
	if start < 0 {
		start = 0
	}
	window := make(filter.RollingWindow, idx-start)
	for i, c := range candles[start:idx] {
		window[i] = filter.Candle{
			Open:   c.Open,
			High:   c.High,
			Low:    c.Low,
			Close:  c.Close,
			Volume: c.Volume,
		}
	}
	return window
}

// parseIntervalDuration maps a Binance interval string to a time.Duration.
func parseIntervalDuration(interval string) time.Duration {
	switch interval {
	case "1m":
		return time.Minute
	case "3m":
		return 3 * time.Minute
	case "5m":
		return 5 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "30m":
		return 30 * time.Minute
	case "1h":
		return time.Hour
	case "2h":
		return 2 * time.Hour
	case "4h":
		return 4 * time.Hour
	case "6h":
		return 6 * time.Hour
	case "8h":
		return 8 * time.Hour
	case "12h":
		return 12 * time.Hour
	case "1d":
		return 24 * time.Hour
	case "3d":
		return 3 * 24 * time.Hour
	case "1w":
		return 7 * 24 * time.Hour
	default:
		return 15 * time.Minute
	}
}

// RunSymbol backtests over all stored candles for a given symbol and interval,
// processing data in 30-day chunks to bound memory usage regardless of how
// much history is stored. When WithFilters() has been called, per-candle
// eligibility checks are applied exactly as the live bot would.
func (r *Runner) RunSymbol(symbol, interval string) (*Result, error) {
	startRange, endRange, err := r.db.GetCandleRange(symbol, interval)
	if err != nil {
		return nil, fmt.Errorf("backtest: get candle range for %s: %w", symbol, err)
	}

	result := &Result{
		Symbol:           symbol,
		FilterRejections: make(map[string]int),
	}

	// Build filter.Config from FilterConfig if filters are active.
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
			volWindow = 96 // 24h on 15m bars
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
	intervalDuration := parseIntervalDuration(interval)
	// Warmup: enough candles for the slowest indicator + volume window + buffer
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
			return nil, fmt.Errorf("backtest: load candles for %s: %w", symbol, err)
		}

		if len(candles) < minC {
			if currentStart == startRange {
				return nil, fmt.Errorf("backtest: not enough candles for %s (got %d, need >=%d)", symbol, len(candles), minC)
			}
			continue
		}

		// Build techan TimeSeries for this chunk
		series := techan.NewTimeSeries()
		for _, c := range candles {
			period := techan.NewTimePeriod(c.OpenTime, intervalDuration)
			candle := techan.NewCandle(period)
			candle.OpenPrice = big.NewFromString(fmt.Sprintf("%.8f", c.Open))
			candle.MaxPrice = big.NewFromString(fmt.Sprintf("%.8f", c.High))
			candle.MinPrice = big.NewFromString(fmt.Sprintf("%.8f", c.Low))
			candle.ClosePrice = big.NewFromString(fmt.Sprintf("%.8f", c.Close))
			candle.Volume = big.NewFromString(fmt.Sprintf("%.8f", c.Volume))
			series.AddCandle(candle)
		}

		var strategy techan.RuleStrategy
		if r.useStrategy2 {
			strategy, err = app.Strategy2(series)
		} else {
			strategy, err = app.Strategy1(series)
		}
		if err != nil {
			return nil, fmt.Errorf("backtest: build strategy for %s: %w", symbol, err)
		}
		record := techan.NewTradingRecord()

		for i := minC; i < len(candles); i++ {
			// Skip warmup candles that are before the current chunk window
			if candles[i].OpenTime.Before(currentStart) {
				continue
			}

			currentPrice := candles[i].Close

			// Emergency exit on rug-pull candle (open positions only)
			if openTrade != nil && r.filterCfg != nil && r.filterCfg.RugCandleBodyPct > 0 {
				recentWindow := buildRollingWindow(candles, i+1, 3)
				if filter.ShouldEmergencyExit(recentWindow, fCfg) {
					closeTrade(currentPrice, candles[i].OpenTime, "rug_exit")
					result.FilterRejections["rug_exit"]++
					continue
				}
			}

			if openTrade == nil && strategy.ShouldEnter(i, record) {
				// Per-candle eligibility checks before accepting entry
				if r.filterCfg != nil {
					upperBB, _ := app.GetUpperBB(series, i)
					lowerBB, _ := app.GetLowerBB(series, i)
					middleBB, _ := app.GetMiddleBB(series, i)
					window := buildRollingWindow(candles, i+1, volWindow)

					eligibility := filter.IsEligibleForEntry(
						window, currentPrice,
						upperBB, lowerBB, middleBB,
						0, // tradeCount not available in backtest
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
				case strategy.ShouldExit(i, record):
					closeTrade(currentPrice, candles[i].OpenTime, "strategy")
				}
			}
		}
	}

	if result.TotalTrades > 0 && result.WinTrades > 0 {
		completed := result.WinTrades + result.LossTrades
		if completed > 0 {
			result.WinRate = float64(result.WinTrades) / float64(completed) * 100
		}
	}

	return result, nil
}

// PrintReport logs a formatted summary of a backtest result.
func PrintReport(res *Result) {
	slog.Info("=== Backtest Report ===",
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
