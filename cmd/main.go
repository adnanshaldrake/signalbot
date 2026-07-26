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
	"strconv"
	"syscall"
	"time"

	"adnan/binance-bot/pkg/app"
	"adnan/binance-bot/pkg/backtest"
	"adnan/binance-bot/pkg/binance"
	"adnan/binance-bot/pkg/config"
	"adnan/binance-bot/pkg/notify"
	"adnan/binance-bot/pkg/paper"
	"adnan/binance-bot/pkg/risk"
	"adnan/binance-bot/pkg/state"
	"adnan/binance-bot/pkg/store"
	"adnan/binance-bot/pkg/utils"

	"github.com/sdcoffey/big"
	"github.com/sdcoffey/techan"
)

func main() {
	// --- CLI flags ---
	backfillFlag := flag.Bool("backfill", false, "Download and store historical candles, then exit")
	backtestFlag := flag.Bool("backtest", false, "Run backtest over stored candles, then exit")
	flag.Parse()

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
	// State manager (Redis)
	stateMgr, err := state.NewManager(cfg)
	if err != nil {
		slog.Error("failed to connect to Redis", "error", err)
		os.Exit(1)
	}
	defer stateMgr.Close()

	// Notifier
	notifier := notify.NewNotifier(cfg)

	// Risk manager
	riskMgr := risk.NewManager(cfg, stateMgr)

	// Paper trader (used when PaperTrading=true)
	paperTrader := paper.NewTrader(cfg, stateMgr)

	// Real Binance client (used when PaperTrading=false)
	binanceClient := binance.NewClient(cfg)

	// Resolve watchlist
	watchlist, err := resolveWatchlist(cfg)
	if err != nil {
		slog.Error("failed to resolve watchlist", "error", err)
		os.Exit(1)
	}
	slog.Info("watchlist resolved", "count", len(watchlist))

	mode := tradingMode(cfg)
	notifier.BotStarted(mode, cfg.CandleInterval, len(watchlist))

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ticker := time.NewTicker(cfg.LoopInterval)
	defer ticker.Stop()

	slog.Info("bot loop started", "interval", cfg.LoopInterval)

	// Run immediately on startup, then on every tick
	runCycle(ctx, cfg, watchlist, stateMgr, riskMgr, notifier, paperTrader, binanceClient)

	for {
		select {
		case <-ticker.C:
			runCycle(ctx, cfg, watchlist, stateMgr, riskMgr, notifier, paperTrader, binanceClient)
		case <-ctx.Done():
			slog.Info("shutdown signal received, stopping bot")
			return
		}
	}
}

// runCycle executes one full iteration: fetch data → analyse → check exits → check entries → trade.
func runCycle(
	ctx context.Context,
	cfg *config.BotConfig,
	watchlist []string,
	stateMgr *state.Manager,
	riskMgr *risk.Manager,
	notifier *notify.Notifier,
	paperTrader *paper.Trader,
	binanceClient *binance.Client,
) {
	slog.Info("cycle start", "symbols", len(watchlist), "time", time.Now().UTC().Format(time.RFC3339))

	// Load fresh ticker data into Redis
	if err := app.LoadTickerData(watchlist, cfg.CandleLimit); err != nil {
		slog.Error("LoadTickerData failed", "error", err)
		notifier.Error("LoadTickerData", err.Error())
		return
	}

	for _, symbol := range watchlist {
		select {
		case <-ctx.Done():
			return
		default:
		}

		series, err := app.GetTimeSeries(symbol)
		if err != nil {
			slog.Warn("GetTimeSeries failed", "symbol", symbol, "error", err)
			continue
		}
		if len(series.Candles) < 25 {
			continue // not enough data
		}

		strategy, err := app.Strategy1(series)
		if err != nil {
			slog.Warn("Strategy1 failed", "symbol", symbol, "error", err)
			continue
		}

		lastIdx := len(series.Candles) - 1
		currentPrice, _ := strconv.ParseFloat(series.Candles[lastIdx].ClosePrice.String(), 64)

		// --- Check exits on open positions ---
		pos, err := stateMgr.GetPosition(ctx, symbol)
		if err != nil {
			slog.Warn("GetPosition failed", "symbol", symbol, "error", err)
			continue
		}

		if pos != nil {
			shouldExit := strategy.ShouldExit(lastIdx, techan.NewTradingRecord()) ||
				riskMgr.CheckStopLoss(pos, currentPrice) ||
				riskMgr.CheckTakeProfit(pos, currentPrice)

			if shouldExit {
				pnl := risk.CalcPnL(pos, currentPrice)
				pnlPct := pnl / pos.QuoteSpent * 100

				if cfg.PaperTrading {
					if _, err := paperTrader.PlaceMarketOrder(ctx, symbol, binance.Sell, currentPrice, pos.QuoteSpent); err != nil {
						slog.Error("paper sell failed", "symbol", symbol, "error", err)
						continue
					}
				} else {
					if _, err := binanceClient.PlaceMarketOrder(symbol, binance.Sell, pos.Quantity); err != nil {
						slog.Error("live sell failed", "symbol", symbol, "error", err)
						notifier.Error("PlaceMarketOrder SELL", err.Error())
						continue
					}
					if err := stateMgr.ClosePosition(ctx, symbol, pnl); err != nil {
						slog.Error("ClosePosition failed", "symbol", symbol, "error", err)
					}
				}
				notifier.TradeClosed(symbol, tradingMode(cfg), pos.EntryPrice, currentPrice, pnl, pnlPct)
			}
			continue // don't check entry if we already have a position
		}

		// --- Check entries for new positions ---
		record := techan.NewTradingRecord()
		if !strategy.ShouldEnter(lastIdx, record) {
			continue
		}

		decision := riskMgr.CheckEntry(ctx, symbol, currentPrice)
		if !decision.Allowed {
			slog.Debug("entry blocked by risk", "symbol", symbol, "reason", decision.Reason)
			continue
		}

		if cfg.PaperTrading {
			if _, err := paperTrader.PlaceMarketOrder(ctx, symbol, binance.Buy, currentPrice, decision.Quantity); err != nil {
				slog.Error("paper buy failed", "symbol", symbol, "error", err)
				continue
			}
		} else {
			resp, err := binanceClient.PlaceMarketOrder(symbol, binance.Buy, decision.Quantity)
			if err != nil {
				slog.Error("live buy failed", "symbol", symbol, "error", err)
				notifier.Error("PlaceMarketOrder BUY", err.Error())
				continue
			}
			// Record position from real fill data
			filledQty, _ := strconv.ParseFloat(resp.ExecutedQty, 64)
			pos := state.Position{
				Symbol:     symbol,
				Side:       "BUY",
				EntryPrice: currentPrice,
				Quantity:   filledQty,
				QuoteSpent: decision.Quantity,
				OrderID:    resp.OrderID,
				OpenedAt:   time.Now(),
			}
			if err := stateMgr.OpenPosition(ctx, pos); err != nil {
				slog.Error("OpenPosition failed", "symbol", symbol, "error", err)
			}
		}
		notifier.TradeOpened(symbol, tradingMode(cfg), currentPrice, decision.Quantity/currentPrice, decision.Quantity)
	}

	slog.Info("cycle complete", "time", time.Now().UTC().Format(time.RFC3339))
}

// runBackfill downloads and stores historical candles for all watchlist symbols.
func runBackfill(cfg *config.BotConfig, db *store.DB) {
	slog.Info("backfill starting")
	watchlist, err := resolveWatchlist(cfg)
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

// runBacktest replays stored candles through Strategy1 and prints results.
func runBacktest(cfg *config.BotConfig, db *store.DB) {
	slog.Info("backtest starting")
	symbols, err := db.ListSymbols()
	if err != nil || len(symbols) == 0 {
		slog.Error("backtest: no symbols in DB — run --backfill first")
		os.Exit(1)
	}

	runner := backtest.NewRunner(db, cfg.MaxPositionUSDT)
	totalPnL := 0.0

	for _, symbol := range symbols {
		res, err := runner.RunSymbol(symbol, cfg.CandleInterval)
		if err != nil {
			slog.Warn("backtest: skip", "symbol", symbol, "error", err)
			continue
		}
		backtest.PrintReport(res)
		totalPnL += res.TotalPnLUSDT
	}

	slog.Info("backtest complete", "total_pnl_usdt", fmt.Sprintf("$%.4f", totalPnL))
}

// resolveWatchlist returns the configured watchlist, or fetches all USDT pairs if empty.
func resolveWatchlist(cfg *config.BotConfig) ([]string, error) {
	if len(cfg.Watchlist) > 0 {
		return cfg.Watchlist, nil
	}
	slog.Info("no watchlist configured, fetching all USDT pairs from Binance")
	return app.GetSymbolList()
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
