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

	"adnan/binance-bot/pkg/backtest"
	"adnan/binance-bot/pkg/config"
	"adnan/binance-bot/pkg/store"
	"adnan/binance-bot/pkg/utils"
)

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
//
// If screening produces zero results (bad market, API error, etc.) it falls
// back to the provided fallback slice and logs a warning.
func Run(cfg *config.BotConfig, db *store.DB, fallback []string) []string {
	slog.Info("screener: starting",
		"top_n", cfg.ScreenTopN,
		"min_win_rate_pct", cfg.ScreenMinWinRate,
		"min_pnl_usdt", cfg.ScreenMinPnL,
		"min_trades", cfg.ScreenMinTrades,
	)

	// --- Step 1: fetch top N symbols by 24h USDT volume ---
	symbols, err := getTopSymbols(cfg.ScreenTopN)
	if err != nil {
		slog.Error("screener: failed to fetch symbols, using fallback", "error", err)
		return fallback
	}
	slog.Info("screener: fetched candidate symbols", "count", len(symbols))

	// --- Step 2: backfill candles (upsert — fast on repeat runs) ---
	backfillSymbols(symbols, cfg.CandleInterval, db)

	// --- Step 3: run backtest on each ---
	runner := backtest.NewRunnerV2(db, cfg.MaxPositionUSDT).
		WithRisk(cfg.StopLossPct, cfg.TakeProfitPct, 192).
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

	// --- Step 4: filter by thresholds ---
	var passed []Result
	for _, r := range results {
		if r.Trades >= cfg.ScreenMinTrades &&
			r.WinRate >= cfg.ScreenMinWinRate &&
			r.PnLUSDT >= cfg.ScreenMinPnL {
			passed = append(passed, r)
		}
	}

	// --- Step 5: sort by PnL descending, cap at MaxWatchlistSize ---
	sort.Slice(passed, func(i, j int) bool {
		return passed[i].PnLUSDT > passed[j].PnLUSDT
	})
	if cfg.MaxWatchlistSize > 0 && len(passed) > cfg.MaxWatchlistSize {
		passed = passed[:cfg.MaxWatchlistSize]
	}

	if len(passed) == 0 {
		slog.Warn("screener: no symbols passed filter — using fallback watchlist",
			"screened", len(results),
			"min_win_rate", cfg.ScreenMinWinRate,
			"min_pnl", cfg.ScreenMinPnL,
		)
		return fallback
	}

	watchlist := make([]string, len(passed))
	for i, r := range passed {
		watchlist[i] = r.Symbol
		slog.Info("screener: selected symbol",
			"rank", i+1,
			"symbol", r.Symbol,
			"trades", r.Trades,
			"win_rate_pct", fmt.Sprintf("%.1f%%", r.WinRate),
			"pnl_usdt", fmt.Sprintf("$%.4f", r.PnLUSDT),
			"max_drawdown", fmt.Sprintf("$%.4f", r.Drawdown),
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

// getTopSymbols calls the Binance 24hr ticker and returns the top n USDT spot
// pairs sorted by quoteVolume descending. Leveraged tokens (UP/DOWN/BULL/BEAR)
// are excluded. Symbols with < $1M 24h volume are skipped.
func getTopSymbols(n int) ([]string, error) {
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

	type candidate struct {
		symbol string
		vol    float64
	}
	var candidates []candidate

	for _, t := range tickers {
		if !strings.HasSuffix(t.Symbol, "USDT") {
			continue
		}
		base := strings.TrimSuffix(t.Symbol, "USDT")
		if strings.HasSuffix(base, "UP") || strings.HasSuffix(base, "DOWN") ||
			strings.HasSuffix(base, "BULL") || strings.HasSuffix(base, "BEAR") {
			continue
		}
		vol, _ := strconv.ParseFloat(t.QuoteVolume, 64)
		if vol < 1_000_000 {
			continue
		}
		candidates = append(candidates, candidate{symbol: t.Symbol, vol: vol})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].vol > candidates[j].vol
	})
	if n > 0 && len(candidates) > n {
		candidates = candidates[:n]
	}

	out := make([]string, len(candidates))
	for i, c := range candidates {
		out[i] = c.symbol
	}
	return out, nil
}
