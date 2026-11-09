// Package screener backfills the top-N USDT symbols by 24h volume, runs a quick
// backtest on each, and returns a ranked watchlist of only profitable symbols.
// It is called on startup when SCREEN_ON_STARTUP=true and replaces the static
// WATCHLIST env var with a data-validated list.
package screener

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/adnanshaldrake/signalbot/pkg/app"
	"github.com/adnanshaldrake/signalbot/pkg/backtest"
	"github.com/adnanshaldrake/signalbot/pkg/config"
	"github.com/adnanshaldrake/signalbot/pkg/store"
	"github.com/adnanshaldrake/signalbot/pkg/utils"
)

// Screener defines an interface for selecting and ranking tradable symbols.
// Custom screeners (e.g. external proprietary packages or custom filters)
// can implement this interface to inject tailored asset selection into the bot.
type Screener interface {
	Screen() ([]string, error)
}

// VolumeBacktestScreener implements Screener using 24h volume candidates and backtest ranking.
type VolumeBacktestScreener struct {
	cfg *config.BotConfig
	db  *store.DB
}

// NewVolumeBacktestScreener creates a VolumeBacktestScreener.
func NewVolumeBacktestScreener(cfg *config.BotConfig, db *store.DB) *VolumeBacktestScreener {
	return &VolumeBacktestScreener{cfg: cfg, db: db}
}

// Screen executes the volume and backtest screening process.
func (s *VolumeBacktestScreener) Screen() ([]string, error) {
	return Run(s.cfg, s.db), nil
}

// StaticScreener implements Screener with a fixed list of symbols.
type StaticScreener struct {
	Symbols []string
}

// NewStaticScreener creates a StaticScreener.
func NewStaticScreener(symbols []string) *StaticScreener {
	return &StaticScreener{Symbols: symbols}
}

// Screen returns the static list of symbols.
func (s *StaticScreener) Screen() ([]string, error) {
	return s.Symbols, nil
}

// Result holds the backtest outcome for a single screened symbol.
type Result struct {
	Symbol   string
	Trades   int
	WinRate  float64
	PnLUSDT  float64
	Drawdown float64
}

// Run fetches the top ScreenTopN USDT symbols by 24h volume, backfills their
// candles into db, runs RunnerV2 on each, filters by the thresholds in cfg,
// and returns a ranked watchlist (best PnL first).
// Only backtested, screened symbols are returned — it never falls back to
// unscreened symbols.
func Run(cfg *config.BotConfig, db *store.DB) []string {
	slog.Info("screener: starting",
		"top_n", cfg.ScreenTopN,
		"min_volume_usdt", cfg.ScreenMinVolumeUSDT,
		"max_volume_usdt", cfg.ScreenMaxVolumeUSDT,
		"min_win_rate_pct", cfg.ScreenMinWinRate,
		"min_pnl_usdt", cfg.ScreenMinPnL,
		"min_trades", cfg.ScreenMinTrades,
	)

	// --- Step 1: fetch candidate symbols by 24h USDT volume range ---
	symbols, err := getTopSymbols(cfg.ScreenTopN, cfg.ScreenMinVolumeUSDT, cfg.ScreenMaxVolumeUSDT)
	if err != nil {
		slog.Error("screener: failed to fetch candidate symbols", "error", err)
		return nil
	}
	slog.Info("screener: fetched candidate symbols", "count", len(symbols))

	// --- Step 2: backfill candles (upsert — fast on repeat runs) ---
	backfillSymbols(symbols, cfg.CandleInterval, db)

	// --- Step 3: run backtest on each ---
	runner := backtest.NewRunnerV2(db, cfg.MaxPositionUSDT).
		WithRisk(cfg.StopLossPct, cfg.TakeProfitPct, 192).
		WithFee(cfg.TakerFeePct).
		WithFilters(backtest.FilterConfig{
			MinRollingVolUSDT: cfg.MinVolume24hUSDT / 4, // relaxed for screening
			RugCandleBodyPct:  cfg.RugCandleBodyPct,
			MaxPriceChangePct: cfg.MaxPriceChange24h,
			MinBBBandwidth:    0.0,
			RequireEMATrend:   false,
		})

	var results []Result
	for _, symbol := range symbols {
		res, err := runner.RunSymbolV2(symbol, cfg.CandleInterval)
		if err != nil {
			slog.Debug("screener: backtest skip", "symbol", symbol, "error", err)
			continue
		}
		results = append(results, Result{
			Symbol:   symbol,
			Trades:   res.TotalTrades,
			WinRate:  res.WinRate,
			PnLUSDT:  res.TotalPnLUSDT,
			Drawdown: res.MaxDrawdown,
		})
	}

	// --- Step 4 & 5: filter by thresholds and rank ---
	watchlist := FilterAndRankResults(results, cfg)
	if len(watchlist) == 0 {
		slog.Warn("screener: no symbols passed filter and none had positive PnL — returning empty watchlist",
			"screened", len(results),
			"min_win_rate", cfg.ScreenMinWinRate,
			"min_pnl", cfg.ScreenMinPnL,
		)
		return nil
	}

	for i, sym := range watchlist {
		slog.Info("screener: selected symbol",
			"rank", i+1,
			"symbol", sym,
		)
	}
	slog.Info("screener: complete", "selected", len(watchlist))
	return watchlist
}

// backfillSymbols fetches 1000-candle history for each symbol and upserts into db.
// Errors per-symbol are logged and skipped — a partial backfill is fine.
func backfillSymbols(symbols []string, interval string, db *store.DB) {
	client := &http.Client{Timeout: 10 * time.Second}
	httpClient := utils.HTTPClient(client)
	ok := 0
	for _, symbol := range symbols {
		endpointURL := fmt.Sprintf(
			"%s?interval=%s&limit=1000&symbol=%s",
			config.CandleStick.String(), interval, symbol,
		)
		body, err := utils.GetData(httpClient, endpointURL)
		if err != nil {
			slog.Debug("screener: backfill fetch failed", "symbol", symbol, "error", err)
			continue
		}
		var raw [][]interface{}
		if err := json.Unmarshal(body, &raw); err != nil {
			continue
		}
		candles := make([]store.Candle, 0, len(raw))
		for _, bar := range raw {
			if len(bar) < 6 {
				continue
			}
			openTimeMs := int64(bar[0].(float64))
			parse := func(v interface{}) float64 {
				s, _ := v.(string)
				f, _ := strconv.ParseFloat(s, 64)
				return f
			}
			candles = append(candles, store.Candle{
				Symbol:   symbol,
				Interval: interval,
				OpenTime: time.UnixMilli(openTimeMs),
				Open:     parse(bar[1]),
				High:     parse(bar[2]),
				Low:      parse(bar[3]),
				Close:    parse(bar[4]),
				Volume:   parse(bar[5]),
			})
		}
		if err := db.UpsertCandles(candles); err != nil {
			slog.Debug("screener: backfill upsert failed", "symbol", symbol, "error", err)
			continue
		}
		ok++
	}
	slog.Info("screener: backfill complete", "symbols", ok, "total", len(symbols))
}

// getTopSymbols calls the Binance 24hr ticker and returns up to n USDT spot
// pairs within the [minVol, maxVol] quoteVolume range, sorted by quoteVolume descending.
// If n <= 0, all matching symbols are returned.
// Leveraged tokens (UP/DOWN/BULL/BEAR) and inactive markets are excluded.
func getTopSymbols(n int, minVol, maxVol float64) ([]string, error) {
	type ticker struct {
		Symbol      string `json:"symbol"`
		QuoteVolume string `json:"quoteVolume"`
	}

	client := &http.Client{Timeout: 10 * time.Second}
	httpClient := utils.HTTPClient(client)
	body, err := utils.GetData(httpClient, config.DailyTicker.String())
	if err != nil {
		return nil, fmt.Errorf("getTopSymbols: %w", err)
	}

	var tickers []ticker
	if err := json.Unmarshal(body, &tickers); err != nil {
		return nil, fmt.Errorf("getTopSymbols: unmarshal: %w", err)
	}

	// Fetch active trading spot symbols to exclude BREAK/HALT/closed markets
	validSymbols, err := app.GetSymbolList()
	var validMap map[string]bool
	if err == nil && len(validSymbols) > 0 {
		validMap = make(map[string]bool, len(validSymbols))
		for _, s := range validSymbols {
			validMap[s] = true
		}
	} else if err != nil {
		slog.Warn("screener: could not fetch active symbol list from exchangeInfo", "error", err)
	}

	tickerCandidates := make([]TickerCandidate, len(tickers))
	for i, t := range tickers {
		vol, _ := strconv.ParseFloat(t.QuoteVolume, 64)
		tickerCandidates[i] = TickerCandidate{
			Symbol:      t.Symbol,
			QuoteVolume: vol,
		}
	}
	return FilterCandidateTickers(tickerCandidates, validMap, n, minVol, maxVol), nil
}

// TickerCandidate holds ticker data for candidate screening.
type TickerCandidate struct {
	Symbol      string
	QuoteVolume float64
}

// FilterCandidateTickers filters tickers by USDT suffix, valid status, non-leveraged tokens,
// and volume bounds [minVol, maxVol], returning top n symbols sorted by QuoteVolume descending.
func FilterCandidateTickers(tickers []TickerCandidate, validMap map[string]bool, n int, minVol, maxVol float64) []string {
	var candidates []TickerCandidate
	for _, t := range tickers {
		if validMap != nil && !validMap[t.Symbol] {
			continue
		}
		if !strings.HasSuffix(t.Symbol, "USDT") {
			continue
		}
		base := strings.TrimSuffix(t.Symbol, "USDT")
		if strings.HasSuffix(base, "UP") || strings.HasSuffix(base, "DOWN") ||
			strings.HasSuffix(base, "BULL") || strings.HasSuffix(base, "BEAR") {
			continue
		}
		if minVol > 0 && t.QuoteVolume < minVol {
			continue
		}
		if maxVol > 0 && t.QuoteVolume > maxVol {
			continue
		}
		candidates = append(candidates, t)
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].QuoteVolume > candidates[j].QuoteVolume
	})
	if n > 0 && len(candidates) > n {
		candidates = candidates[:n]
	}

	out := make([]string, len(candidates))
	for i, c := range candidates {
		out[i] = c.Symbol
	}
	return out
}

// FilterAndRankResults filters backtest results by config thresholds and returns the ranked watchlist.
func FilterAndRankResults(results []Result, cfg *config.BotConfig) []string {
	var passed []Result
	for _, r := range results {
		if r.Trades >= cfg.ScreenMinTrades &&
			r.WinRate >= cfg.ScreenMinWinRate &&
			r.PnLUSDT >= cfg.ScreenMinPnL {
			passed = append(passed, r)
		}
	}

	sort.Slice(passed, func(i, j int) bool {
		return passed[i].PnLUSDT > passed[j].PnLUSDT
	})
	if cfg.MaxWatchlistSize > 0 && len(passed) > cfg.MaxWatchlistSize {
		passed = passed[:cfg.MaxWatchlistSize]
	}

	if len(passed) == 0 {
		sort.Slice(results, func(i, j int) bool {
			return results[i].PnLUSDT > results[j].PnLUSDT
		})

		var bestPositive []Result
		for _, r := range results {
			if r.Trades > 0 && r.PnLUSDT > 0 {
				bestPositive = append(bestPositive, r)
				if len(bestPositive) >= 3 {
					break
				}
			}
		}

		if len(bestPositive) > 0 {
			slog.Warn("screener: no symbols met strict thresholds; selecting top performers with positive PnL",
				"count", len(bestPositive),
				"screened", len(results),
			)
			passed = bestPositive
		} else {
			return nil
		}
	}

	watchlist := make([]string, len(passed))
	for i, r := range passed {
		watchlist[i] = r.Symbol
	}
	return watchlist
}
