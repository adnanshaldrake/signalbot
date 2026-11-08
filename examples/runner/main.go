// Package main demonstrates how developers can embed signalbot's core engine
// and plug in a custom strategy evaluator and asset screener.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/adnanshaldrake/signalbot/docs/blueprints"
	"github.com/adnanshaldrake/signalbot/pkg/backtest"
	"github.com/adnanshaldrake/signalbot/pkg/config"
	"github.com/adnanshaldrake/signalbot/pkg/engine"
	"github.com/adnanshaldrake/signalbot/pkg/screener"
	"github.com/adnanshaldrake/signalbot/pkg/store"
	"github.com/adnanshaldrake/signalbot/pkg/strategy"
)

func main() {
	backtestFlag := flag.Bool("backtest", false, "Run backtest with custom strategy instead of live trading bot")
	flag.Parse()

	// 1. Load configuration (reads environment variables or .env)
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load configuration", "error", err)
		os.Exit(1)
	}

	// 2. Open SQLite database for state management and candle history
	db, err := store.Open(cfg.SQLiteDBPath)
	if err != nil {
		slog.Error("failed to open database store", "path", cfg.SQLiteDBPath, "error", err)
		os.Exit(1)
	}
	defer db.Close()

	// 3. Define custom strategy factory (Momentum Breakout Golden Cross)
	customStrategyFactory := func(series *strategy.Series) (strategy.Evaluator, error) {
		return blueprints.NewMomentumBreakout(series, 12, 26, 14)
	}

	// 4. Optionally define custom symbol screener
	customScreener := screener.NewStaticScreener([]string{"BTCUSDT", "ETHUSDT", "SOLUSDT"})

	// Mode A: Backtest custom strategy
	if *backtestFlag {
		slog.Info("running backtest with custom MomentumBreakoutStrategy")
		runner := backtest.NewRunnerV2(db, cfg.MaxPositionUSDT).
			WithFee(cfg.TakerFeePct).
			WithStrategy(customStrategyFactory)

		symbols, _ := customScreener.Screen()
		for _, sym := range symbols {
			res, err := runner.RunSymbolV2(sym, cfg.CandleInterval)
			if err != nil {
				slog.Error("backtest failed", "symbol", sym, "error", err)
				continue
			}
			fmt.Printf("\n=== Custom Strategy Backtest [%s] ===\n", sym)
			fmt.Printf("Total Trades : %d (Wins: %d / Losses: %d)\n", res.TotalTrades, res.WinTrades, res.LossTrades)
			fmt.Printf("Win Rate     : %.2f%%\n", res.WinRate)
			fmt.Printf("Net PnL      : $%.2f USDT\n", res.TotalPnLUSDT)
			fmt.Printf("Max Drawdown : $%.2f USDT\n", res.MaxDrawdown)
		}
		return
	}

	// Mode B: 24/7 Trading Bot execution loop
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	slog.Info("starting custom bot runner powered by MomentumBreakoutStrategy",
		"paper_trading", cfg.PaperTrading,
		"interval", cfg.CandleInterval,
	)

	eng := engine.New(cfg, db).
		WithStrategy(customStrategyFactory).
		WithScreener(customScreener)

	if err := eng.Run(ctx); err != nil {
		slog.Error("bot stopped with error", "error", err)
		os.Exit(1)
	}
}

