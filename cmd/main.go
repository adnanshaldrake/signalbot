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
	"adnan/binance-bot/pkg/filter"
	"adnan/binance-bot/pkg/notify"
	"adnan/binance-bot/pkg/paper"
	"adnan/binance-bot/pkg/risk"
	"adnan/binance-bot/pkg/signals"
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
	// Initialise ticker cache (in-memory if no Redis, Redis otherwise)
	app.InitCache(cfg)

	// State manager — auto-selects Redis or in-memory based on REDIS_URL
	stateMgr, err := state.NewStateManager(cfg.RedisURL, cfg.RedisUsername, cfg.RedisPassword)
	if err != nil {
		slog.Error("failed to init state manager", "error", err)
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

// runCycle executes one full iteration: fetch data → analyse → check exits → collect entries → score → trade.
// Entry signals are gathered in phase 1 (no orders placed), then scored and executed in phase 2
// so that higher-conviction setups receive a larger capital allocation.
func runCycle(
	ctx context.Context,
	cfg *config.BotConfig,
	watchlist []string,
	stateMgr state.StateManager,
	riskMgr *risk.Manager,
	notifier *notify.Notifier,
	paperTrader *paper.Trader,
	binanceClient *binance.Client,
) {
	slog.Info("cycle start", "symbols", len(watchlist), "time", time.Now().UTC().Format(time.RFC3339))

	// Fetch fresh candlestick data into the cache (in-memory or Redis)
	if err := app.LoadTickerDataWithInterval(watchlist, cfg.CandleLimit, cfg.CandleInterval); err != nil {
		slog.Error("LoadTickerData failed", "error", err)
		notifier.Error("LoadTickerData", err.Error())
		return
	}

	// Build filter config from bot config
	fCfg := filter.Config{
		MinRollingVolUSDT: cfg.MinVolume24hUSDT,
		MinTradeCount:     cfg.MinTradeCount24h,
		MaxPriceChangePct: cfg.MaxPriceChange24h,
		RugCandleBodyPct:  cfg.RugCandleBodyPct,
		MinBBBandwidth:    0.01,
	}

	// --- Phase 1: exits + collect entry candidates ---
	var candidates []signals.Candidate

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
		// Require minimum candles for Strategy2 warm-up (EMA200 needs 200 bars)
		if len(series.Candles) < cfg.MinCandlesRequired {
			continue
		}

		strategy, err := app.Strategy2(series)
		if err != nil {
			slog.Warn("Strategy2 failed", "symbol", symbol, "error", err)
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
			// Build a rolling window from the last 3 candles for rug-pull detection
			nCandles := len(series.Candles)
			windowStart := nCandles - 3
			if windowStart < 0 {
				windowStart = 0
			}
			var rugWindow filter.RollingWindow
			for _, sc := range series.Candles[windowStart:] {
				o, _ := strconv.ParseFloat(sc.OpenPrice.String(), 64)
				c, _ := strconv.ParseFloat(sc.ClosePrice.String(), 64)
				v, _ := strconv.ParseFloat(sc.Volume.String(), 64)
				rugWindow = append(rugWindow, filter.Candle{Open: o, Close: c, Volume: v})
			}

			// Emergency exit on rug-pull candle
			if filter.ShouldEmergencyExit(rugWindow, fCfg) {
				slog.Warn("rug_pull_detected — emergency exit", "symbol", symbol)
				pnl := risk.CalcPnL(pos, currentPrice)
				pnlPct := pnl / pos.QuoteSpent * 100
				if cfg.PaperTrading {
					paperTrader.PlaceMarketOrder(ctx, symbol, binance.Sell, currentPrice, pos.QuoteSpent) //nolint:errcheck
				} else {
					binanceClient.PlaceMarketOrder(symbol, binance.Sell, pos.Quantity) //nolint:errcheck
					stateMgr.ClosePosition(ctx, symbol, pnl)                           //nolint:errcheck
				}
				notifier.TradeClosed(symbol, tradingMode(cfg), pos.EntryPrice, currentPrice, pnl, pnlPct)
				continue
			}

			// Normal exit: SL/TP check only (strategy.ShouldExit is not used in the live
			// loop — it requires a populated TradingRecord that we don't maintain per-symbol,
			// and PositionOpenRule{} would never fire with a fresh record).
			shouldExit := riskMgr.CheckStopLoss(pos, currentPrice) ||
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

		// --- Collect entry candidates (no order placed yet) ---
		record := techan.NewTradingRecord()
		if !strategy.ShouldEnter(lastIdx, record) {
			continue
		}

		// Gather indicator values for scoring and filter checks
		rsiVal, err := app.GetRSI(series, lastIdx)
		if err != nil {
			slog.Warn("GetRSI failed", "symbol", symbol, "error", err)
			continue
		}
		upperBB, err := app.GetUpperBB(series, lastIdx)
		if err != nil {
			slog.Warn("GetUpperBB failed", "symbol", symbol, "error", err)
			continue
		}
		lowerBB, _ := app.GetLowerBB(series, lastIdx)
		middleBB, _ := app.GetMiddleBB(series, lastIdx)

		// Build rolling 24h window for filter checks (96 candles on 15m)
		nCandles := len(series.Candles)
		winStart := nCandles - 97 // 96 candles + current
		if winStart < 0 {
			winStart = 0
		}
		var rollingWindow filter.RollingWindow
		for _, sc := range series.Candles[winStart:lastIdx] {
			o, _ := strconv.ParseFloat(sc.OpenPrice.String(), 64)
			c, _ := strconv.ParseFloat(sc.ClosePrice.String(), 64)
			v, _ := strconv.ParseFloat(sc.Volume.String(), 64)
			rollingWindow = append(rollingWindow, filter.Candle{Open: o, Close: c, Volume: v})
		}

		// Estimate 24h USDT volume and run eligibility checks
		volumeBase, _ := strconv.ParseFloat(series.Candles[lastIdx].Volume.String(), 64)
		volume24hUSDT := rollingWindow.VolumeUSDT() + volumeBase*currentPrice

		eligibility := filter.IsEligibleForEntry(
			rollingWindow, currentPrice,
			upperBB, lowerBB, middleBB,
			0, // tradeCount: TODO wire from 24hr ticker
			fCfg,
		)
		if !eligibility.Eligible {
			slog.Debug("entry_filter_blocked", "symbol", symbol, "reason", eligibility.Reason)
			continue
		}

		candidates = append(candidates, signals.Candidate{
			Symbol:        symbol,
			CurrentPrice:  currentPrice,
			UpperBB:       upperBB,
			RSI:           rsiVal,
			Volume24hUSDT: volume24hUSDT,
		})
	}

	if len(candidates) == 0 {
		slog.Info("cycle complete — no entry signals", "time", time.Now().UTC().Format(time.RFC3339))
		return
	}

	// --- Phase 2: score → sort → execute ---
	weights := signals.ScoreWeights{
		Upside:   cfg.SignalUpsideWeight,
		RSIDepth: cfg.SignalRSIWeight,
		Volume:   cfg.SignalVolumeWeight,
	}
	ranked := signals.Score(candidates, weights, cfg.MaxPositionUSDT, cfg.SignalExtraBudgetUSDT)

	slog.Info("entry signals scored", "count", len(ranked))

	for _, candidate := range ranked {
		select {
		case <-ctx.Done():
			return
		default:
		}

		decision := riskMgr.CheckEntry(ctx, candidate.Symbol, candidate.CurrentPrice, candidate.AllocationUSDT)
		if !decision.Allowed {
			slog.Debug("entry blocked by risk", "symbol", candidate.Symbol, "reason", decision.Reason)
			continue
		}

		if cfg.PaperTrading {
			if _, err := paperTrader.PlaceMarketOrder(ctx, candidate.Symbol, binance.Buy, candidate.CurrentPrice, decision.Quantity); err != nil {
				slog.Error("paper buy failed", "symbol", candidate.Symbol, "error", err)
				continue
			}
		} else {
			resp, err := binanceClient.PlaceMarketOrder(candidate.Symbol, binance.Buy, decision.Quantity)
			if err != nil {
				slog.Error("live buy failed", "symbol", candidate.Symbol, "error", err)
				notifier.Error("PlaceMarketOrder BUY", err.Error())
				continue
			}
			// Record position from real fill data
			filledQty, _ := strconv.ParseFloat(resp.ExecutedQty, 64)
			pos := state.Position{
				Symbol:     candidate.Symbol,
				Side:       "BUY",
				EntryPrice: candidate.CurrentPrice,
				Quantity:   filledQty,
				QuoteSpent: decision.Quantity,
				OrderID:    resp.OrderID,
				OpenedAt:   time.Now(),
			}
			if err := stateMgr.OpenPosition(ctx, pos); err != nil {
				slog.Error("OpenPosition failed", "symbol", candidate.Symbol, "error", err)
			}
		}
		slog.Info("signal ranked and executed",
			"symbol", candidate.Symbol,
			"score", fmt.Sprintf("%.3f", candidate.Score),
			"allocation_usdt", fmt.Sprintf("%.2f", decision.Quantity),
			"rsi", fmt.Sprintf("%.2f", candidate.RSI),
			"upside_pct", fmt.Sprintf("%.2f%%", (candidate.UpperBB-candidate.CurrentPrice)/candidate.CurrentPrice*100),
		)
		notifier.TradeOpened(candidate.Symbol, tradingMode(cfg), candidate.CurrentPrice, decision.Quantity/candidate.CurrentPrice, decision.Quantity)
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
		WithFilters(backtest.FilterConfig{
			MinRollingVolUSDT: cfg.MinVolume24hUSDT,
			RugCandleBodyPct:  cfg.RugCandleBodyPct,
			MaxPriceChangePct: cfg.MaxPriceChange24h,
			MinBBBandwidth:    0.01,
			RequireEMATrend:   true,
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

// resolveWatchlist returns the configured watchlist, auto-discovers active USDT pairs
// if AUTO_DISCOVER_SYMBOLS=true, or fetches all USDT pairs if WATCHLIST is empty.
func resolveWatchlist(cfg *config.BotConfig) ([]string, error) {
	if cfg.AutoDiscoverSymbols {
		slog.Info("auto-discovering active USDT pairs", "min_vol_usdt", cfg.MinVolume24hUSDT, "max", cfg.MaxWatchlistSize)
		return app.GetActiveSymbols(cfg.MinVolume24hUSDT, cfg.MinTradeCount24h, cfg.MaxWatchlistSize)
	}
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
