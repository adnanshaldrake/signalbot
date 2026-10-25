// Package app provides the core trading application logic.
package app

import (
	"adnan/binance-bot/pkg/config"
	"adnan/binance-bot/pkg/models"
	"adnan/binance-bot/pkg/utils"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/sdcoffey/big"
	"github.com/sdcoffey/techan"
)

// TickerCache is the interface for storing and retrieving raw candlestick JSON.
// There are two implementations: InMemoryCache (no dependencies) and RedisCache.
type TickerCache interface {
	Set(symbol, data string) error
	Get(symbol string) (string, error)
}

// --- In-memory cache (default, no Redis required) ---

// InMemoryCache stores ticker JSON in a plain Go map with a read/write mutex.
type InMemoryCache struct {
	mu   sync.RWMutex
	data map[string]string
}

// NewInMemoryCache creates a new InMemoryCache.
func NewInMemoryCache() *InMemoryCache {
	return &InMemoryCache{data: make(map[string]string)}
}

func (c *InMemoryCache) Set(symbol, data string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[symbol] = data
	return nil
}

func (c *InMemoryCache) Get(symbol string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.data[symbol]
	if !ok {
		return "", fmt.Errorf("symbol %s not found in cache", symbol)
	}
	return v, nil
}

// --- Module-level cache instance ---
// Initialised once via InitCache; defaults to in-memory if Redis is not configured.
var globalCache TickerCache

// InitCache sets up the ticker cache based on configuration.
// Call this once at startup before using LoadTickerData / GetTimeSeries.
func InitCache(cfg *config.BotConfig) {
	if cfg.RedisURL != "" && cfg.RedisURL != "localhost:6379" {
		// Redis URL explicitly configured — use Redis
		globalCache = newRedisCache(cfg)
	} else {
		// No Redis configured (or default placeholder) — use in-memory
		globalCache = NewInMemoryCache()
	}
}

// SetCache allows injecting a custom cache (useful for testing).
func SetCache(c TickerCache) { globalCache = c }

func getCache() TickerCache {
	if globalCache == nil {
		// Safety: auto-init to in-memory if InitCache was never called
		globalCache = NewInMemoryCache()
	}
	return globalCache
}

// GetSymbolList returns all USDT spot trading pairs from Binance.
func GetSymbolList() ([]string, error) {
	var symbolList []string
	var client utils.HTTPClient = &http.Client{}

	resBody, err := utils.GetData(client, config.ExchangeInfo.String())
	if err != nil {
		return nil, fmt.Errorf("failed to get exchange info: %s\n", err)
	}

	var parsed models.ExchangeInfoResponse
	if err := json.Unmarshal(resBody, &parsed); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %s", err)
	}

	for _, symbol := range parsed.Symbols {
		if symbol.QuoteAsset == "USDT" &&
			symbol.Status == "TRADING" &&
			symbol.IsSpotTradingAllowed {
			symbolList = append(symbolList, symbol.Symbol)
		}
	}
	return symbolList, nil
}

// LoadTickerData fetches candlestick data for each symbol concurrently and stores
// it in the configured cache (in-memory or Redis).
func LoadTickerData(symbolList []string, limit int) error {
	return LoadTickerDataWithInterval(symbolList, limit, "15m")
}

// LoadTickerDataWithInterval is the same as LoadTickerData but lets the caller
// specify the candle interval explicitly.
func LoadTickerDataWithInterval(symbolList []string, limit int, interval string) error {
	var client utils.HTTPClient = &http.Client{}
	cache := getCache()

	var wg sync.WaitGroup
	errChan := make(chan error, len(symbolList))

	for _, symbol := range symbolList {
		wg.Add(1)
		go func(symbol string) {
			defer wg.Done()

			endpointUrl := fmt.Sprintf(
				"%s?interval=%s&limit=%s&symbol=%s",
				config.CandleStick.String(), interval, strconv.Itoa(limit), symbol,
			)
			body, err := utils.GetData(client, endpointUrl)
			if err != nil {
				errChan <- fmt.Errorf("failed to get data for %s: %v", symbol, err)
				return
			}
			if err := cache.Set(symbol, string(body)); err != nil {
				errChan <- fmt.Errorf("failed to cache data for %s: %v", symbol, err)
			}
		}(symbol)
	}

	go func() {
		wg.Wait()
		close(errChan)
	}()

	for err := range errChan {
		if err != nil {
			return err
		}
	}
	return nil
}

// GetTimeSeries builds a techan.TimeSeries for a symbol from the cache.
func GetTimeSeries(symbol string) (*techan.TimeSeries, error) {
	cache := getCache()
	result, err := cache.Get(symbol)
	if err != nil {
		return nil, fmt.Errorf("cache miss for %s: %v", symbol, err)
	}

	var ticker [][]interface{}
	if err := json.Unmarshal([]byte(result), &ticker); err != nil {
		return nil, fmt.Errorf("unmarshal ticker for %s: %v", symbol, err)
	}

	series := techan.NewTimeSeries()
	for idx := range ticker {
		start := ticker[idx][0].(float64)
		period := techan.NewTimePeriod(time.Unix(int64(start)/1000, 0), time.Minute*15)
		candle := techan.NewCandle(period)
		candle.OpenPrice = big.NewFromString(ticker[idx][1].(string))
		candle.MaxPrice = big.NewFromString(ticker[idx][2].(string))
		candle.MinPrice = big.NewFromString(ticker[idx][3].(string))
		candle.ClosePrice = big.NewFromString(ticker[idx][4].(string))
		candle.Volume = big.NewFromString(ticker[idx][5].(string))
		series.AddCandle(candle)
	}
	return series, nil
}

// ticker24h is the minimal subset of the Binance /api/v3/ticker/24hr response
// that we need for auto-discovering active USDT pairs.
type ticker24h struct {
	Symbol             string `json:"symbol"`
	QuoteVolume        string `json:"quoteVolume"`        // 24h volume in quote asset (USDT)
	Count              int    `json:"count"`              // number of trades in 24h
	PriceChangePercent string `json:"priceChangePercent"` // e.g. "3.25" or "-1.80"
}

// GetActiveSymbols calls the Binance 24hr ticker and returns up to maxSymbols
// USDT spot pairs that meet the minimum volume and trade count thresholds,
// sorted by 24h USDT volume descending (most liquid first).
// Pass minTradeCount=0 to skip the trade-count filter.
func GetActiveSymbols(minVolUSDT float64, minTradeCount, maxSymbols int) ([]string, error) {
	var client utils.HTTPClient = &http.Client{Timeout: 10 * time.Second}

	body, err := utils.GetData(client, config.DailyTicker.String())
	if err != nil {
		return nil, fmt.Errorf("GetActiveSymbols: fetch 24hr ticker: %w", err)
	}

	var tickers []ticker24h
	if err := json.Unmarshal(body, &tickers); err != nil {
		return nil, fmt.Errorf("GetActiveSymbols: unmarshal: %w", err)
	}

	type candidate struct {
		symbol  string
		volUSDT float64
	}
	var candidates []candidate

	for _, t := range tickers {
		// Only USDT quote pairs
		if len(t.Symbol) < 5 || t.Symbol[len(t.Symbol)-4:] != "USDT" {
			continue
		}
		vol, err := strconv.ParseFloat(t.QuoteVolume, 64)
		if err != nil || vol < minVolUSDT {
			continue
		}
		if minTradeCount > 0 && t.Count < minTradeCount {
			continue
		}
		candidates = append(candidates, candidate{symbol: t.Symbol, volUSDT: vol})
	}

	// Sort by volume descending (highest liquidity first)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].volUSDT > candidates[j].volUSDT
	})

	if maxSymbols > 0 && len(candidates) > maxSymbols {
		candidates = candidates[:maxSymbols]
	}

	symbols := make([]string, len(candidates))
	for i, c := range candidates {
		symbols[i] = c.symbol
	}
	return symbols, nil
}
