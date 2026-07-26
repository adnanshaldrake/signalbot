// Package backtest replays stored candle data through a strategy and reports P&L.
package backtest

import (
	"fmt"
	"log/slog"
	"time"

	"adnan/binance-bot/pkg/app"
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
}

// Result summarises backtest performance for a single symbol.
type Result struct {
	Symbol      string
	TotalTrades int
	WinTrades   int
	LossTrades  int
	TotalPnLUSDT float64
	MaxDrawdown  float64
	WinRate      float64
	Trades       []TradeRecord
}

// Runner executes a backtest over stored historical data.
type Runner struct {
	db           *store.DB
	positionUSDT float64
}

// NewRunner creates a new backtest Runner.
func NewRunner(db *store.DB, positionUSDT float64) *Runner {
	return &Runner{db: db, positionUSDT: positionUSDT}
}

// RunSymbol backtests Strategy1 over all stored candles for a given symbol and interval.
func (r *Runner) RunSymbol(symbol, interval string) (*Result, error) {
	candles, err := r.db.GetCandles(symbol, interval, time.Time{}, time.Time{})
	if err != nil {
		return nil, fmt.Errorf("backtest: load candles for %s: %w", symbol, err)
	}
	if len(candles) < 30 {
		return nil, fmt.Errorf("backtest: not enough candles for %s (got %d, need ≥30)", symbol, len(candles))
	}

	// Build techan TimeSeries from stored candles
	series := techan.NewTimeSeries()
	for _, c := range candles {
		period := techan.NewTimePeriod(c.OpenTime, time.Minute*15) // approximate period
		candle := techan.NewCandle(period)
		candle.OpenPrice = big.NewFromString(fmt.Sprintf("%.8f", c.Open))
		candle.MaxPrice = big.NewFromString(fmt.Sprintf("%.8f", c.High))
		candle.MinPrice = big.NewFromString(fmt.Sprintf("%.8f", c.Low))
		candle.ClosePrice = big.NewFromString(fmt.Sprintf("%.8f", c.Close))
		candle.Volume = big.NewFromString(fmt.Sprintf("%.8f", c.Volume))
		series.AddCandle(candle)
	}

	strategy, err := app.Strategy1(series)
	if err != nil {
		return nil, fmt.Errorf("backtest: build strategy for %s: %w", symbol, err)
	}

	result := &Result{Symbol: symbol}
	record := techan.NewTradingRecord()

	var openTrade *TradeRecord
	peakPnL := 0.0
	runningPnL := 0.0

	// Walk candles starting after the warm-up period (need 21 bars for BB)
	for i := 21; i < len(candles); i++ {
		currentPrice := candles[i].Close

		if openTrade == nil && strategy.ShouldEnter(i, record) {
			// Open a simulated long position
			qty := r.positionUSDT / currentPrice
			openTrade = &TradeRecord{
				Symbol:     symbol,
				Side:       "BUY",
				Price:      currentPrice,
				Qty:        qty,
				QuoteValue: r.positionUSDT,
				Time:       candles[i].OpenTime,
			}
			result.Trades = append(result.Trades, *openTrade)
			result.TotalTrades++

		} else if openTrade != nil && strategy.ShouldExit(i, record) {
			// Close the simulated position
			pnl := (currentPrice - openTrade.Price) / openTrade.Price * openTrade.QuoteValue
			pnlPct := (currentPrice - openTrade.Price) / openTrade.Price * 100

			result.Trades = append(result.Trades, TradeRecord{
				Symbol:     symbol,
				Side:       "SELL",
				Price:      currentPrice,
				Qty:        openTrade.Qty,
				QuoteValue: openTrade.QuoteValue + pnl,
				Time:       candles[i].OpenTime,
				PnLUSDT:    pnl,
				PnLPct:     pnlPct,
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
			drawdown := peakPnL - runningPnL
			if drawdown > result.MaxDrawdown {
				result.MaxDrawdown = drawdown
			}

			openTrade = nil
		}
	}

	if result.TotalTrades > 0 && result.WinTrades > 0 {
		completedTrades := result.WinTrades + result.LossTrades
		if completedTrades > 0 {
			result.WinRate = float64(result.WinTrades) / float64(completedTrades) * 100
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
	for _, t := range res.Trades {
		if t.Side == "SELL" {
			slog.Info("trade",
				"symbol", t.Symbol,
				"time", t.Time.Format("2006-01-02 15:04"),
				"side", t.Side,
				"price", fmt.Sprintf("%.6f", t.Price),
				"pnl_usdt", fmt.Sprintf("$%.4f", t.PnLUSDT),
				"pnl_pct", fmt.Sprintf("%.2f%%", t.PnLPct),
			)
		}
	}
}
