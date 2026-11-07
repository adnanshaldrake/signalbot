package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/adnanshaldrake/signalbot/pkg/app"
	"github.com/adnanshaldrake/signalbot/pkg/backtest"
	"github.com/adnanshaldrake/signalbot/pkg/config"
	"github.com/adnanshaldrake/signalbot/pkg/engine"
	"github.com/adnanshaldrake/signalbot/pkg/metrics"
	"github.com/adnanshaldrake/signalbot/pkg/screener"
	"github.com/adnanshaldrake/signalbot/pkg/store"
	"github.com/adnanshaldrake/signalbot/pkg/utils"

	"github.com/sdcoffey/big"
)

func main() {
	// --- CLI flags ---
	backfillFlag := flag.Bool("backfill", false, "Download and store historical candles, then exit")
	backtestFlag := flag.Bool("backtest", false, "Run backtest over stored candles, then exit")
	metricsFlag := flag.Bool("metrics", false, "Display trade performance metrics and exit")
	exportTradesFlag := flag.Bool("export-trades", false, "Export completed trade history to stdout, then exit")
	formatFlag := flag.String("format", "json", "Output format for -export-trades: 'json' or 'csv'")
	cpuProfile := flag.String("cpuprofile", "", "Write cpu profile to file")
	memProfile := flag.String("memprofile", "", "Write memory profile to file")
	flag.Parse()

	if *cpuProfile != "" {
		f, err := os.Create(*cpuProfile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "could not create CPU profile: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			fmt.Fprintf(os.Stderr, "could not start CPU profile: %v\n", err)
			os.Exit(1)
		}
		defer pprof.StopCPUProfile()
	}

	if *memProfile != "" {
		defer func() {
			f, err := os.Create(*memProfile)
			if err != nil {
				fmt.Fprintf(os.Stderr, "could not create memory profile: %v\n", err)
				return
			}
			defer f.Close()
			runtime.GC()
			if err := pprof.WriteHeapProfile(f); err != nil {
				fmt.Fprintf(os.Stderr, "could not write memory profile: %v\n", err)
			}
		}()
	}

	// --- Quick metrics inspection mode ---
	if *metricsFlag {
		dbPath := "data/signalbot.db"
		if cfg, err := config.Load(); err == nil && cfg.SQLiteDBPath != "" {
			dbPath = cfg.SQLiteDBPath
		}
		db, err := store.Open(dbPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to open database at %s: %v\n", dbPath, err)
			os.Exit(1)
		}
		defer db.Close()

		if err := metrics.PrintDashboard(os.Stdout, db); err != nil {
			fmt.Fprintf(os.Stderr, "failed to print metrics: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// --- Export trades dataset mode (for model feeding & offline analysis) ---
	if *exportTradesFlag {
		dbPath := "data/signalbot.db"
		if cfg, err := config.Load(); err == nil && cfg.SQLiteDBPath != "" {
			dbPath = cfg.SQLiteDBPath
		}
		db, err := store.Open(dbPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to open database at %s: %v\n", dbPath, err)
			os.Exit(1)
		}
		defer db.Close()

		if strings.ToLower(*formatFlag) == "csv" {
			if err := metrics.ExportTradesCSV(os.Stdout, db); err != nil {
				fmt.Fprintf(os.Stderr, "failed to export trades as CSV: %v\n", err)
				os.Exit(1)
			}
		} else {
			if err := metrics.ExportTradesJSON(os.Stdout, db); err != nil {
				fmt.Fprintf(os.Stderr, "failed to export trades as JSON: %v\n", err)
				os.Exit(1)
			}
		}
		return
	}

	// --- Structured JSON logging ---
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// --- Load config ---
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config error", "error", err)
		os.Exit(1)
	}

	slog.Info("signalbot starting",
		"mode", tradingMode(cfg),
		"interval", cfg.CandleInterval,
		"paper_trading", cfg.PaperTrading,
		"testnet", cfg.UseTestnet,
	)

	// --- Open SQLite for historical data ---
	db, err := store.Open(cfg.SQLiteDBPath)
	if err != nil {
		slog.Error("failed to open sqlite", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	// --- Backfill mode: download candles and exit ---
	if *backfillFlag {
		runBackfill(cfg, db)
		return
	}

	// --- Backtest mode: replay candles and exit ---
	if *backtestFlag {
		runBacktest(cfg, db)
		return
	}

	// --- Live / Paper trading ---
	runBot(cfg, db)
}

// runBot is the main trading loop.
func runBot(cfg *config.BotConfig, db *store.DB) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	eng := engine.New(cfg, db)
	if err := eng.Run(ctx); err != nil {
		slog.Error("engine stopped with error", "error", err)
		os.Exit(1)
	}
}

// runBackfill downloads and stores historical candles for all watchlist symbols.
func runBackfill(cfg *config.BotConfig, db *store.DB) {
	slog.Info("backfill starting")
	watchlist, err := resolveWatchlist(cfg, db)
	if err != nil {
		slog.Error("backfill: resolve watchlist", "error", err)
		os.Exit(1)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	httpClient := utils.HTTPClient(client)

	for _, symbol := range watchlist {
		endpointURL := fmt.Sprintf(
			"%s?interval=%s&limit=1000&symbol=%s",
			config.CandleStick.String(), cfg.CandleInterval, symbol,
		)
		body, err := utils.GetData(httpClient, endpointURL)
		if err != nil {
			slog.Warn("backfill: fetch failed", "symbol", symbol, "error", err)
			continue
		}

		var raw [][]interface{}
		if err := json.Unmarshal(body, &raw); err != nil {
			slog.Warn("backfill: unmarshal failed", "symbol", symbol, "error", err)
			continue
		}

		candles := make([]store.Candle, 0, len(raw))
		for _, bar := range raw {
			openTimeMs := int64(bar[0].(float64))
			open, _ := strconv.ParseFloat(bar[1].(string), 64)
			high, _ := strconv.ParseFloat(bar[2].(string), 64)
			low, _ := strconv.ParseFloat(bar[3].(string), 64)
			close_, _ := strconv.ParseFloat(bar[4].(string), 64)
			volume, _ := strconv.ParseFloat(bar[5].(string), 64)
			candles = append(candles, store.Candle{
				Symbol:   symbol,
				Interval: cfg.CandleInterval,
				OpenTime: time.UnixMilli(openTimeMs),
				Open:     open,
				High:     high,
				Low:      low,
				Close:    close_,
				Volume:   volume,
			})
		}

		if err := db.UpsertCandles(candles); err != nil {
			slog.Warn("backfill: upsert failed", "symbol", symbol, "error", err)
			continue
		}
		slog.Info("backfill: stored", "symbol", symbol, "candles", len(candles))
	}
	slog.Info("backfill complete")
}

// runBacktest replays stored candles through Strategy2 with filters and prints results.
// Uses RunnerV2 (native float64 indicators via pkg/strategy — zero techan/big).
func runBacktest(cfg *config.BotConfig, db *store.DB) {
	slog.Info("backtest starting")
	symbols, err := db.ListSymbols()
	if err != nil || len(symbols) == 0 {
		slog.Error("backtest: no symbols in DB — run --backfill first")
		os.Exit(1)
	}

	runner := backtest.NewRunnerV2(db, cfg.MaxPositionUSDT).
		WithRisk(cfg.StopLossPct, cfg.TakeProfitPct, 192).
		WithFee(cfg.TakerFeePct).
		WithFilters(backtest.FilterConfig{
			MinRollingVolUSDT: cfg.MinVolume24hUSDT / 2, // relax volume
			RugCandleBodyPct:  cfg.RugCandleBodyPct,
			MaxPriceChangePct: cfg.MaxPriceChange24h,
			MinBBBandwidth:    0.0,   // disable BB bandwidth filter
			RequireEMATrend:   false, // disable trend gate
		})
	totalPnL := 0.0

	for _, symbol := range symbols {
		res, err := runner.RunSymbolV2(symbol, cfg.CandleInterval)
		if err != nil {
			slog.Warn("backtest: skip", "symbol", symbol, "error", err)
			continue
		}
		backtest.PrintReportV2(res)
		totalPnL += res.TotalPnLUSDT
	}

	slog.Info("backtest complete", "total_pnl_usdt", fmt.Sprintf("$%.4f", totalPnL))
}

// resolveWatchlist returns the watchlist to trade.
// Priority order:
//  1. Screener: dynamically backtests candidates from Binance, keeps profitable pairs (primary)
//  2. Auto-Discovery: scans active Binance USDT pairs by volume & trade count if AUTO_DISCOVER_SYMBOLS=true
//  3. User WATCHLIST: fallback if screener/auto-discovery yield no symbols or if disabled
func resolveWatchlist(cfg *config.BotConfig, db *store.DB, customScreener ...screener.Screener) ([]string, error) {
	if len(customScreener) > 0 && customScreener[0] != nil {
		slog.Info("running custom screener to discover active watchlist")
		screened, err := customScreener[0].Screen()
		if err == nil && len(screened) > 0 {
			slog.Info("custom screener selected symbols for trading", "count", len(screened), "symbols", screened)
			return screened, nil
		}
		if err != nil {
			slog.Warn("custom screener failed", "error", err)
		}
	}

	if cfg.ScreenOnStartup {
		slog.Info("running startup screener to discover active watchlist",
			"top_n", cfg.ScreenTopN,
			"min_win_rate", cfg.ScreenMinWinRate,
			"min_pnl", cfg.ScreenMinPnL,
			"min_trades", cfg.ScreenMinTrades,
		)
		scr := screener.NewVolumeBacktestScreener(cfg, db)
		screened, _ := scr.Screen()
		if len(screened) > 0 {
			slog.Info("screener selected symbols for trading", "count", len(screened), "symbols", screened)
			return screened, nil
		}
		slog.Warn("screener yielded 0 symbols — checking auto-discovery or fallback watchlist")
	}

	if cfg.AutoDiscoverSymbols {
		slog.Info("auto-discovering active USDT pairs from Binance",
			"min_volume_usdt", cfg.MinVolume24hUSDT,
			"min_trade_count", cfg.MinTradeCount24h,
			"max_symbols", cfg.MaxWatchlistSize,
		)
		discovered, err := app.GetActiveSymbols(cfg.MinVolume24hUSDT, cfg.MinTradeCount24h, cfg.MaxWatchlistSize)
		if err != nil {
			slog.Warn("auto-discovery failed — falling back to user watchlist", "error", err)
		} else if len(discovered) > 0 {
			slog.Info("auto-discovered active symbols", "count", len(discovered), "symbols", discovered)
			return discovered, nil
		} else {
			slog.Warn("auto-discovery yielded 0 symbols — falling back to user watchlist")
		}
	}

	if len(cfg.Watchlist) > 0 {
		slog.Info("using user-configured watchlist as fallback", "count", len(cfg.Watchlist), "symbols", cfg.Watchlist)
		return cfg.Watchlist, nil
	}

	slog.Warn("no screened symbols, no auto-discovered symbols, and no user watchlist configured")
	return nil, nil
}

// tradingMode returns a human-readable mode string.
func tradingMode(cfg *config.BotConfig) string {
	if cfg.PaperTrading {
		return "PAPER"
	}
	if cfg.UseTestnet {
		return "TESTNET"
	}
	return "LIVE"
}

// Ensure big import is used (for candle building in backtest runner)
var _ = big.ZERO
