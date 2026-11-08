// Package engine orchestrates the 24/7 trading lifecycle for signalbot.
// It supports pluggable strategy evaluators and symbol screeners.
package engine

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/adnanshaldrake/signalbot/pkg/app"
	"github.com/adnanshaldrake/signalbot/pkg/binance"
	"github.com/adnanshaldrake/signalbot/pkg/config"
	"github.com/adnanshaldrake/signalbot/pkg/filter"
	"github.com/adnanshaldrake/signalbot/pkg/indicators"
	"github.com/adnanshaldrake/signalbot/pkg/metrics"
	"github.com/adnanshaldrake/signalbot/pkg/notify"
	"github.com/adnanshaldrake/signalbot/pkg/paper"
	"github.com/adnanshaldrake/signalbot/pkg/risk"
	"github.com/adnanshaldrake/signalbot/pkg/screener"
	"github.com/adnanshaldrake/signalbot/pkg/signals"
	"github.com/adnanshaldrake/signalbot/pkg/state"
	"github.com/adnanshaldrake/signalbot/pkg/store"
	"github.com/adnanshaldrake/signalbot/pkg/strategy"
)

// Engine manages the bot's execution loop, market feeds, and order routing.
type Engine struct {
	cfg             *config.BotConfig
	db              *store.DB
	strategyFactory strategy.Factory
	customScreener  screener.Screener
	stateMgr        state.StateManager
	notifier        *notify.Notifier
	riskMgr         *risk.Manager
	paperTrader     *paper.Trader
	binanceClient   *binance.Client
	watchlist       []string
}

// New creates a new Engine configured with the provided bot config and store.
func New(cfg *config.BotConfig, db *store.DB) *Engine {
	return &Engine{
		cfg: cfg,
		db:  db,
	}
}

// WithStrategy configures a custom Strategy Evaluator factory.
func (e *Engine) WithStrategy(factory strategy.Factory) *Engine {
	e.strategyFactory = factory
	return e
}

// WithScreener configures a custom Screener for watchlist selection.
func (e *Engine) WithScreener(scr screener.Screener) *Engine {
	e.customScreener = scr
	return e
}

// Init initializes state managers, clients, and notifiers.
func (e *Engine) Init() error {
	app.InitCache(e.cfg)

	stateMgr, err := state.NewStateManager(e.db)
	if err != nil {
		return fmt.Errorf("engine: failed to init state manager: %w", err)
	}
	e.stateMgr = stateMgr

	e.notifier = notify.NewNotifier(e.cfg)
	e.riskMgr = risk.NewManager(e.cfg, e.stateMgr)
	e.paperTrader = paper.NewTrader(e.cfg, e.stateMgr)
	e.binanceClient = binance.NewClient(e.cfg)

	if err := e.binanceClient.SyncTime(); err != nil {
		slog.Error("engine: failed to sync time with Binance", "error", err)
	}

	if err := e.binanceClient.LoadExchangeInfo(); err != nil {
		slog.Error("engine: failed to load exchange info", "error", err)
	}

	watchlist, err := e.ResolveWatchlist()
	if err != nil {
		return fmt.Errorf("engine: resolve watchlist: %w", err)
	}

	// Filter active symbols
	var activeWatchlist []string
	for _, sym := range watchlist {
		if e.binanceClient.IsMarketOpen(sym) {
			activeWatchlist = append(activeWatchlist, sym)
		} else {
			slog.Warn("excluding symbol from watchlist — market closed or not trading", "symbol", sym)
		}
	}
	e.watchlist = activeWatchlist

	if len(e.watchlist) == 0 {
		slog.Warn("active watchlist is empty — bot will wait for active symbols")
	} else {
		slog.Info("watchlist resolved", "count", len(e.watchlist), "symbols", e.watchlist)
	}

	mode := e.TradingMode()
	e.notifier.BotStarted(mode, e.cfg.CandleInterval, len(e.watchlist))
	return nil
}

// TradingMode returns a human-readable mode string ("PAPER", "TESTNET", or "LIVE").
func (e *Engine) TradingMode() string {
	if e.cfg.PaperTrading {
		return "PAPER"
	}
	if e.cfg.UseTestnet {
		return "TESTNET"
	}
	return "LIVE"
}

// ResolveWatchlist selects the trading universe using custom screener, startup screener, auto-discovery, or static config.
func (e *Engine) ResolveWatchlist() ([]string, error) {
	if e.customScreener != nil {
		slog.Info("running custom screener to discover active watchlist")
		screened, err := e.customScreener.Screen()
		if err == nil && len(screened) > 0 {
			slog.Info("custom screener selected symbols", "count", len(screened), "symbols", screened)
			return screened, nil
		}
		if err != nil {
			slog.Warn("custom screener failed", "error", err)
		}
	}

	if e.cfg.ScreenOnStartup {
		slog.Info("running startup screener to discover active watchlist",
			"top_n", e.cfg.ScreenTopN,
			"min_win_rate", e.cfg.ScreenMinWinRate,
			"min_pnl", e.cfg.ScreenMinPnL,
			"min_trades", e.cfg.ScreenMinTrades,
		)
		scr := screener.NewVolumeBacktestScreener(e.cfg, e.db)
		screened, _ := scr.Screen()
		if len(screened) > 0 {
			slog.Info("screener selected symbols for trading", "count", len(screened), "symbols", screened)
			return screened, nil
		}
		slog.Warn("screener yielded 0 symbols — checking auto-discovery or fallback watchlist")
	}

	if e.cfg.AutoDiscoverSymbols {
		slog.Info("auto-discovering active USDT pairs from Binance",
			"min_volume_usdt", e.cfg.MinVolume24hUSDT,
			"min_trade_count", e.cfg.MinTradeCount24h,
			"max_symbols", e.cfg.MaxWatchlistSize,
		)
		discovered, err := app.GetActiveSymbols(e.cfg.MinVolume24hUSDT, e.cfg.MinTradeCount24h, e.cfg.MaxWatchlistSize)
		if err != nil {
			slog.Warn("auto-discovery failed — falling back to user watchlist", "error", err)
		} else if len(discovered) > 0 {
			slog.Info("auto-discovered active symbols", "count", len(discovered), "symbols", discovered)
			return discovered, nil
		} else {
			slog.Warn("auto-discovery yielded 0 symbols — falling back to user watchlist")
		}
	}

	if len(e.cfg.Watchlist) > 0 {
		slog.Info("using user-configured watchlist as fallback", "count", len(e.cfg.Watchlist), "symbols", e.cfg.Watchlist)
		return e.cfg.Watchlist, nil
	}

	slog.Warn("no screened symbols, no auto-discovered symbols, and no user watchlist configured")
	return nil, nil
}

// Run executes the main trading loop until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) error {
	if err := e.Init(); err != nil {
		return err
	}
	defer e.stateMgr.Close()

	ticker := time.NewTicker(e.cfg.LoopInterval)
	defer ticker.Stop()

	slog.Info("engine loop started", "interval", e.cfg.LoopInterval, "mode", e.TradingMode())

	// Run initial cycle immediately
	if err := e.RunCycle(ctx); err != nil {
		slog.Error("error during initial trading cycle", "error", err)
	}

	for {
		select {
		case <-ticker.C:
			if err := e.RunCycle(ctx); err != nil {
				slog.Error("error during trading cycle", "error", err)
			}
		case <-ctx.Done():
			slog.Info("engine shutdown signal received, stopping loop")
			return nil
		}
	}
}

// RunCycle executes one full cycle: fetch candles -> evaluate strategy -> check exits -> score entries -> place orders.
func (e *Engine) RunCycle(ctx context.Context) error {
	if len(e.watchlist) == 0 {
		slog.Info("cycle start — watchlist is empty, skipping cycle", "time", time.Now().UTC().Format(time.RFC3339))
		return nil
	}
	slog.Info("cycle start", "symbols", len(e.watchlist), "time", time.Now().UTC().Format(time.RFC3339))

	if err := app.LoadTickerDataWithInterval(e.watchlist, e.cfg.CandleLimit, e.cfg.CandleInterval); err != nil {
		slog.Error("LoadTickerData failed", "error", err)
		e.notifier.Error("LoadTickerData", err.Error())
		return err
	}

	fCfg := filter.Config{
		MinRollingVolUSDT: e.cfg.MinVolume24hUSDT,
		MinTradeCount:     e.cfg.MinTradeCount24h,
		MaxPriceChangePct: e.cfg.MaxPriceChange24h,
		RugCandleBodyPct:  e.cfg.RugCandleBodyPct,
		MinBBBandwidth:    0.01,
	}

	minCandles := e.cfg.MinCandlesRequired
	if minCandles <= 0 {
		minCandles = 50
	}

	var candidates []signals.Candidate

	for _, symbol := range e.watchlist {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		if !e.binanceClient.IsMarketOpen(symbol) {
			slog.Debug("market closed or not trading, skipping symbol", "symbol", symbol)
			continue
		}

		series, err := app.GetStrategySeries(symbol)
		if err != nil {
			slog.Warn("GetStrategySeries failed", "symbol", symbol, "error", err)
			continue
		}

		if len(series.Candles) < minCandles {
			slog.Warn("insufficient candles for symbol", "symbol", symbol, "count", len(series.Candles), "required", minCandles)
			continue
		}

		var eval strategy.Evaluator
		if e.strategyFactory != nil {
			eval, err = e.strategyFactory(series)
		} else {
			eval, err = strategy.NewStrategy2(series)
		}
		if err != nil {
			slog.Warn("evaluator creation failed", "symbol", symbol, "error", err)
			continue
		}

		lastIdx := len(series.Candles) - 1
		currentPrice := series.Candles[lastIdx].Close

		// Check exits on open positions
		pos, err := e.stateMgr.GetPosition(ctx, symbol)
		if err != nil {
			slog.Warn("GetPosition failed", "symbol", symbol, "error", err)
			continue
		}

		if pos != nil {
			nCandles := len(series.Candles)
			winStart := nCandles - 3
			if winStart < 0 {
				winStart = 0
			}
			var rugWindow filter.RollingWindow
			for _, sc := range series.Candles[winStart:] {
				rugWindow = append(rugWindow, filter.Candle{Open: sc.Open, Close: sc.Close, Volume: sc.Volume})
			}

			if filter.ShouldEmergencyExit(rugWindow, fCfg) {
				slog.Warn("rug_pull_detected — emergency exit", "symbol", symbol)
				pnl := risk.CalcPnL(pos, currentPrice, e.cfg.TakerFeePct)
				pnlPct := pnl / pos.QuoteSpent * 100
				if e.cfg.PaperTrading {
					e.paperTrader.PlaceMarketOrderWithReason(ctx, symbol, binance.Sell, currentPrice, pos.QuoteSpent, "EMERGENCY_RUG") //nolint:errcheck
				} else {
					e.binanceClient.PlaceMarketOrder(symbol, binance.Sell, pos.Quantity) //nolint:errcheck
					_ = e.stateMgr.ClosePositionWithDetails(ctx, symbol, currentPrice, pnl, "EMERGENCY_RUG")
				}
				metrics.LogTradeClosed(e.db, symbol, pnl)
				e.notifier.TradeClosed(symbol, e.TradingMode(), pos.EntryPrice, currentPrice, pnl, pnlPct)
				continue
			}

			shouldExit := e.riskMgr.CheckStopLoss(pos, currentPrice) ||
				e.riskMgr.CheckTakeProfit(pos, currentPrice) ||
				eval.ShouldExit(lastIdx)

			if shouldExit {
				pnl := risk.CalcPnL(pos, currentPrice, e.cfg.TakerFeePct)
				pnlPct := pnl / pos.QuoteSpent * 100

				exitReason := "STOP_LOSS"
				if e.riskMgr.CheckTakeProfit(pos, currentPrice) {
					exitReason = "TAKE_PROFIT"
				} else if eval.ShouldExit(lastIdx) {
					exitReason = "STRATEGY_EXIT"
				}

				if e.cfg.PaperTrading {
					if _, err := e.paperTrader.PlaceMarketOrderWithReason(ctx, symbol, binance.Sell, currentPrice, pos.QuoteSpent, exitReason); err != nil {
						slog.Error("paper sell failed", "symbol", symbol, "error", err)
						continue
					}
				} else {
					if _, err := e.binanceClient.PlaceMarketOrder(symbol, binance.Sell, pos.Quantity); err != nil {
						slog.Error("live sell failed", "symbol", symbol, "error", err)
						e.notifier.Error("PlaceMarketOrder SELL", err.Error())
						continue
					}
					if err := e.stateMgr.ClosePositionWithDetails(ctx, symbol, currentPrice, pnl, exitReason); err != nil {
						slog.Error("ClosePosition failed", "symbol", symbol, "error", err)
					}
				}
				metrics.LogTradeClosed(e.db, symbol, pnl)
				e.notifier.TradeClosed(symbol, e.TradingMode(), pos.EntryPrice, currentPrice, pnl, pnlPct)
			}
			continue
		}

		// Evaluate entry
		if !eval.ShouldEnter(lastIdx) {
			continue
		}

		upperBB, midBB, lowerBB, rsiVal := 0.0, 0.0, 0.0, 0.0
		if accessor, ok := eval.(strategy.IndicatorAccessor); ok {
			upperBB = accessor.UpperBB(lastIdx)
			midBB = accessor.MidBB(lastIdx)
			lowerBB = accessor.LowerBB(lastIdx)
			rsiVal = accessor.RSIAt(lastIdx)
		} else {
			closes := series.Closes()
			upper, mid, lower := indicators.BollingerBands(closes, 20, 2.0)
			rsis := indicators.RSI(closes, 14)
			upperBB = upper[lastIdx]
			midBB = mid[lastIdx]
			lowerBB = lower[lastIdx]
			rsiVal = rsis[lastIdx]
		}

		nCandles := len(series.Candles)
		winStart := nCandles - 97
		if winStart < 0 {
			winStart = 0
		}
		var rollingWindow filter.RollingWindow
		for _, sc := range series.Candles[winStart:lastIdx] {
			rollingWindow = append(rollingWindow, filter.Candle{Open: sc.Open, Close: sc.Close, Volume: sc.Volume})
		}

		volumeBase := series.Candles[lastIdx].Volume
		volume24hUSDT := rollingWindow.VolumeUSDT() + volumeBase*currentPrice

		eligibility := filter.IsEligibleForEntry(rollingWindow, currentPrice, upperBB, lowerBB, midBB, 0, fCfg)
		if !eligibility.Eligible {
			slog.Debug("entry_filter_blocked", "symbol", symbol, "reason", eligibility.Reason)
			continue
		}

		bbBandwidth := 0.0
		if midBB > 0 {
			bbBandwidth = (upperBB - lowerBB) / midBB
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
		return nil
	}

	weights := signals.ScoreWeights{
		Upside:   e.cfg.SignalUpsideWeight,
		RSIDepth: e.cfg.SignalRSIWeight,
		Volume:   e.cfg.SignalVolumeWeight,
	}
	ranked := signals.Score(candidates, weights, e.cfg.MaxPositionUSDT, e.cfg.SignalExtraBudgetUSDT)
	slog.Info("entry signals scored", "count", len(ranked))

	for _, candidate := range ranked {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		decision := e.riskMgr.CheckEntry(ctx, candidate.Symbol, candidate.CurrentPrice, candidate.AllocationUSDT)
		if !decision.Allowed {
			slog.Debug("entry blocked by risk", "symbol", candidate.Symbol, "reason", decision.Reason)
			continue
		}

		if e.cfg.PaperTrading {
			if _, err := e.paperTrader.PlaceBuyOrderWithIndicators(
				ctx, candidate.Symbol, candidate.CurrentPrice, decision.Quantity,
				candidate.RSI, candidate.BBBandwidth, candidate.Score, candidate.VolRatio,
			); err != nil {
				slog.Error("paper buy failed", "symbol", candidate.Symbol, "error", err)
				continue
			}
		} else {
			resp, err := e.binanceClient.PlaceMarketOrder(candidate.Symbol, binance.Buy, decision.Quantity)
			if err != nil {
				slog.Error("live buy failed", "symbol", candidate.Symbol, "error", err)
				e.notifier.Error("PlaceMarketOrder BUY", err.Error())
				continue
			}
			filledQty, _ := strconv.ParseFloat(resp.ExecutedQty, 64)
			var baseAssetCommission float64
			for _, fill := range resp.Fills {
				if fill.CommissionAsset != "USDT" && fill.CommissionAsset != "BNB" {
					comm, _ := strconv.ParseFloat(fill.Commission, 64)
					baseAssetCommission += comm
				}
			}
			netQuantity := filledQty - baseAssetCommission
			entryPrice, quoteSpent, ok := resp.FillSummary()
			if !ok {
				slog.Warn("no fill data in order response — falling back to ticker price", "symbol", candidate.Symbol)
				entryPrice, quoteSpent = candidate.CurrentPrice, decision.Quantity
			}

			pos := state.Position{
				Symbol:        candidate.Symbol,
				Side:          "BUY",
				EntryPrice:    entryPrice,
				Quantity:      netQuantity,
				QuoteSpent:    quoteSpent,
				OrderID:       resp.OrderID,
				OpenedAt:      time.Now(),
				EntryRSI:      candidate.RSI,
				EntryBBWidth:  candidate.BBBandwidth,
				EntryScore:    candidate.Score,
				EntryVolRatio: candidate.VolRatio,
			}
			if err := e.stateMgr.OpenPosition(ctx, pos); err != nil {
				slog.Error("OpenPosition failed", "symbol", candidate.Symbol, "error", err)
			}
		}
		slog.Info("signal ranked and executed",
			"symbol", candidate.Symbol,
			"score", fmt.Sprintf("%.3f", candidate.Score),
			"allocation_usdt", fmt.Sprintf("%.2f", decision.Quantity),
			"rsi", fmt.Sprintf("%.2f", candidate.RSI),
		)
		e.notifier.TradeOpened(candidate.Symbol, e.TradingMode(), candidate.CurrentPrice, decision.Quantity/candidate.CurrentPrice, decision.Quantity)
	}

	slog.Info("cycle complete", "time", time.Now().UTC().Format(time.RFC3339))
	return nil
}
