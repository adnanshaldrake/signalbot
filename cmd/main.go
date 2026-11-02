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
	"github.com/adnanshaldrake/signalbot/pkg/binance"
	"github.com/adnanshaldrake/signalbot/pkg/config"
	"github.com/adnanshaldrake/signalbot/pkg/filter"
	"github.com/adnanshaldrake/signalbot/pkg/metrics"
	"github.com/adnanshaldrake/signalbot/pkg/notify"
	"github.com/adnanshaldrake/signalbot/pkg/paper"
	"github.com/adnanshaldrake/signalbot/pkg/risk"
	"github.com/adnanshaldrake/signalbot/pkg/screener"
	"github.com/adnanshaldrake/signalbot/pkg/signals"
	"github.com/adnanshaldrake/signalbot/pkg/state"
	"github.com/adnanshaldrake/signalbot/pkg/store"
	"github.com/adnanshaldrake/signalbot/pkg/utils"

	"github.com/sdcoffey/big"
	"github.com/sdcoffey/techan"
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
	// Initialise in-memory ticker cache
	app.InitCache(cfg)

	// State manager — persistent SQLite (or in-memory if no DB)
	stateMgr, err := state.NewStateManager(db)
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

	if err := binanceClient.SyncTime(); err != nil {
		slog.Error("Failed to sync time with Binance", "error", err)
	}

	if err := binanceClient.LoadExchangeInfo(); err != nil {
		slog.Error("Failed to load exchange info", "error", err)
	}

	// Resolve watchlist
	watchlist, err := resolveWatchlist(cfg, db)
	if err != nil {
		slog.Error("failed to resolve watchlist", "error", err)
		os.Exit(1)
	}

	// Filter out any symbol that is not currently in TRADING status or market is closed
	var activeWatchlist []string
	for _, sym := range watchlist {
		if binanceClient.IsMarketOpen(sym) {
			activeWatchlist = append(activeWatchlist, sym)
		} else {
			slog.Warn("excluding symbol from watchlist — market closed or not trading", "symbol", sym)
		}
	}
	watchlist = activeWatchlist

	if len(watchlist) == 0 {
		slog.Warn("active watchlist is empty — bot will not place orders until active symbols are available")
	} else {
		slog.Info("watchlist resolved", "count", len(watchlist), "symbols", watchlist)
	}

	mode := tradingMode(cfg)
	notifier.BotStarted(mode, cfg.CandleInterval, len(watchlist))

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ticker := time.NewTicker(cfg.LoopInterval)
	defer ticker.Stop()

	slog.Info("bot loop started", "interval", cfg.LoopInterval)

	// Run immediately on startup, then on every tick
	runCycle(ctx, cfg, watchlist, stateMgr, riskMgr, notifier, paperTrader, binanceClient, db)

	for {
		select {
		case <-ticker.C:
			runCycle(ctx, cfg, watchlist, stateMgr, riskMgr, notifier, paperTrader, binanceClient, db)
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
	db *store.DB,
) {
	if len(watchlist) == 0 {
		slog.Info("cycle start — watchlist is empty, skipping cycle", "time", time.Now().UTC().Format(time.RFC3339))
		return
	}
	slog.Info("cycle start", "symbols", len(watchlist), "time", time.Now().UTC().Format(time.RFC3339))

	// Fetch fresh candlestick data into the in-memory cache
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

		if !binanceClient.IsMarketOpen(symbol) {
			slog.Debug("market closed or not trading, skipping symbol", "symbol", symbol)
			continue
		}

		series, err := app.GetTimeSeries(symbol)
		if err != nil {
			slog.Warn("GetTimeSeries failed", "symbol", symbol, "error", err)
			continue
		}
		// Require minimum candles for Strategy2 warm-up (EMA200 needs 200 bars)
		if len(series.Candles) < cfg.MinCandlesRequired {
			slog.Warn("Minimun candles required not found", "symbol", symbol)
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
				pnl := risk.CalcPnL(pos, currentPrice, cfg.TakerFeePct)
				pnlPct := pnl / pos.QuoteSpent * 100
				if cfg.PaperTrading {
					paperTrader.PlaceMarketOrderWithReason(ctx, symbol, binance.Sell, currentPrice, pos.QuoteSpent, "EMERGENCY_RUG") //nolint:errcheck
				} else {
					binanceClient.PlaceMarketOrder(symbol, binance.Sell, pos.Quantity) //nolint:errcheck
					_ = stateMgr.ClosePositionWithDetails(ctx, symbol, currentPrice, pnl, "EMERGENCY_RUG")
				}
				metrics.LogTradeClosed(db, symbol, pnl)
				notifier.TradeClosed(symbol, tradingMode(cfg), pos.EntryPrice, currentPrice, pnl, pnlPct)
				continue
			}

			// Normal exit: SL/TP check only (strategy.ShouldExit is not used in the live
			// loop — it requires a populated TradingRecord that we don't maintain per-symbol,
			// and PositionOpenRule{} would never fire with a fresh record).
			shouldExit := riskMgr.CheckStopLoss(pos, currentPrice) ||
				riskMgr.CheckTakeProfit(pos, currentPrice)

			if shouldExit {
				pnl := risk.CalcPnL(pos, currentPrice, cfg.TakerFeePct)
				pnlPct := pnl / pos.QuoteSpent * 100

				exitReason := "STOP_LOSS"
				if riskMgr.CheckTakeProfit(pos, currentPrice) {
					exitReason = "TAKE_PROFIT"
				}

				if cfg.PaperTrading {
					if _, err := paperTrader.PlaceMarketOrderWithReason(ctx, symbol, binance.Sell, currentPrice, pos.QuoteSpent, exitReason); err != nil {
						slog.Error("paper sell failed", "symbol", symbol, "error", err)
						continue
					}
				} else {
					if _, err := binanceClient.PlaceMarketOrder(symbol, binance.Sell, pos.Quantity); err != nil {
						slog.Error("live sell failed", "symbol", symbol, "error", err)
						notifier.Error("PlaceMarketOrder SELL", err.Error())
						continue
					}
					if err := stateMgr.ClosePositionWithDetails(ctx, symbol, currentPrice, pnl, exitReason); err != nil {
						slog.Error("ClosePosition failed", "symbol", symbol, "error", err)
					}
				}
				metrics.LogTradeClosed(db, symbol, pnl)
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
		bbBandwidth := 0.0
		if middleBB > 0 {
			bbBandwidth = (upperBB - lowerBB) / middleBB
		}
		volRatio := 1.0
		avgVol := rollingWindow.VolumeUSDT() / 96.0
		if avgVol > 0 {
			volRatio = (volumeBase * currentPrice) / avgVol
		}

		candidates = append(candidates, signals.Candidate{
			Symbol:        symbol,
			CurrentPrice:  currentPrice,
			UpperBB:       upperBB,
			RSI:           rsiVal,
			Volume24hUSDT: volume24hUSDT,
			BBBandwidth:   bbBandwidth,
			VolRatio:      volRatio,
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
			if _, err := paperTrader.PlaceBuyOrderWithIndicators(
				ctx, candidate.Symbol, candidate.CurrentPrice, decision.Quantity,
				candidate.RSI, candidate.BBBandwidth, candidate.Score, candidate.VolRatio,
			); err != nil {
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

			// Calculate any fees deducted directly from the base asset
			var baseAssetCommission float64
			for _, fill := range resp.Fills {
				if fill.CommissionAsset != "USDT" && fill.CommissionAsset != "BNB" {
					comm, _ := strconv.ParseFloat(fill.Commission, 64)
					baseAssetCommission += comm
				}
			}

			// Compute the actual net quantity available in your wallet
			netQuantity := filledQty - baseAssetCommission

			// Use real fill data (VWAP + cumulative quote) rather than the pre-order ticker price
			entryPrice, quoteSpent, ok := resp.FillSummary()
			if !ok {
				slog.Warn("no fill data in order response — falling back to ticker price", "symbol", candidate.Symbol)
				entryPrice, quoteSpent = candidate.CurrentPrice, decision.Quantity
			}

			// Record position using the accurate net quantity and entry indicator snapshot
			pos := state.Position{
				Symbol:        candidate.Symbol,
				Side:          "BUY",
				EntryPrice:    entryPrice,
				Quantity:      netQuantity, // Use net quantity after fees
				QuoteSpent:    quoteSpent,
				OrderID:       resp.OrderID,
				OpenedAt:      time.Now(),
				EntryRSI:      candidate.RSI,
				EntryBBWidth:  candidate.BBBandwidth,
				EntryScore:    candidate.Score,
				EntryVolRatio: candidate.VolRatio,
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
func resolveWatchlist(cfg *config.BotConfig, db *store.DB) ([]string, error) {
	if cfg.ScreenOnStartup {
		slog.Info("running startup screener to discover active watchlist",
			"top_n", cfg.ScreenTopN,
			"min_win_rate", cfg.ScreenMinWinRate,
			"min_pnl", cfg.ScreenMinPnL,
			"min_trades", cfg.ScreenMinTrades,
		)
		screened := screener.Run(cfg, db)
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
