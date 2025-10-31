package app

import (
	"adnan/binance-bot/pkg/config"
	"adnan/binance-bot/pkg/models"
	"adnan/binance-bot/pkg/utils"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/sdcoffey/big"
	"github.com/sdcoffey/techan"
)

func GetSymbolList() ([]string, error) {
	/*
		Returns a list of trading symbols relevant to the 'USDT' quote asset.

		Returns:
		- []string: List of trading symbols relevant to the 'USDT' quote asset.
		- error: An error, if any occurred.
	*/
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

	// Get all symbols with the USDT quote asset
	for _, symbol := range parsed.Symbols {
		if symbol.QuoteAsset == "USDT" {
			symbolList = append(symbolList, symbol.Symbol)
		}
	}

	return symbolList, nil
}

func LoadTickerData(symbolList []string, limit int) error {
	/*
		LoadTickerData loads ticker data for each symbol in symbolList
		and stores it in Redis.
		It uses a goroutine for each symbol to fetch the data asynchronously,
		and uses a pipeline to efficiently store the data in Redis.

		Parameters:
		- limit: The maximum number of data points to retrieve per symbol.

		Returns:
		- error: An error if there was a problem getting the symbol list or storing data in Redis.
	*/
	var client utils.HTTPClient = &http.Client{}

	rdb, err := utils.GetRedisClient()
	if err != nil {
		return fmt.Errorf("failed to get Redis client %v\n", err)
	}

	ctx, cancel := utils.GetContextWithTimeout(10)
	defer cancel()
	// pipeline := rdb.Pipeline()

	var wg sync.WaitGroup
	errChan := make(chan error, len(symbolList))

	for _, symbol := range symbolList {
		wg.Add((1))
		go func(symbol string) {
			defer wg.Done()

			interval := "1h"
			endpointUrl := fmt.Sprintf(
				"%s?interval=%s&limit=%s&symbol=%s",
				config.CandleStick.String(), interval, strconv.Itoa(limit), symbol,
			)
			body, err := utils.GetData(client, endpointUrl)
			if err != nil {
				errChan <- fmt.Errorf("failed to get data for symbol %s: %v", symbol, err)
				return
			}
			if err := rdb.HSet(ctx, symbol, "ticker", string(body)).Err(); err != nil {
				errChan <- fmt.Errorf("failed to store data for symbol %s in Redis: %v", symbol, err)
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

// TODO Update to handle multiple symbols in goroutines
func GetTimeSeries(symbol string) (*techan.TimeSeries, error) {
	/*
		Generate and return a techan timeseries object when
		given a symbol using data stored in Redis in the
		<symbol> hash's 'ticker' field.
		symbol: Trading symbol to retrieve the timeseries for.
		return: A techan.Timeseries object, error
	*/

	ctx, cancel := utils.GetContextWithTimeout(10)
	defer cancel()

	rdb, err := utils.GetRedisClient()
	if err != nil {
		return nil, fmt.Errorf("failed to get Redis client %v\n", err)
	}
	defer rdb.Close()

	series := techan.NewTimeSeries()

	result, err := rdb.HGet(ctx, symbol, "ticker").Result()
	if err != nil {
		return nil, fmt.Errorf("error while doing HGET command in gredis : %v", err)
	}

	var ticker [][]interface{}
	if err := json.Unmarshal([]byte(result), &ticker); err != nil {
		return nil, fmt.Errorf("error while unmarshalling JSON data : %v", err)
	}

	for idx := range ticker {
		start := ticker[idx][0].(float64)
		period := techan.NewTimePeriod(time.Unix(int64(start)/1000, 0), time.Hour)

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

func ClearRedisDatabase() error {
	rdb, err := utils.GetRedisClient()
	if err != nil {
		return fmt.Errorf("failed to get Redis client: %w", err)
	}
	// Note: Do not defer rdb.Close() here if it's a shared connection pool.

	ctx, cancel := utils.GetContextWithTimeout(5) // Use a short timeout
	defer cancel()

	// Execute FLUSHDB: removes all keys from the current database.
	// Use FLUSHALL if you want to clear *all* databases, but FLUSHDB is safer.
	status := rdb.FlushDB(ctx)
	if status.Err() != nil {
		return fmt.Errorf("failed to execute FLUSHDB: %w", status.Err())
	}

	return nil
}
