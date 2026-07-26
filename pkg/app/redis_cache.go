package app

import (
	"context"
	"fmt"
	"time"

	"adnan/binance-bot/pkg/config"

	"github.com/redis/go-redis/v9"
)

// redisCache implements TickerCache using Redis HSet/HGet.
type redisCache struct {
	rdb *redis.Client
}

func newRedisCache(cfg *config.BotConfig) TickerCache {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisURL,
		Username: cfg.RedisUsername,
		Password: cfg.RedisPassword,
		DB:       0,
	})
	return &redisCache{rdb: rdb}
}

func (r *redisCache) Set(symbol, data string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return r.rdb.HSet(ctx, symbol, "ticker", data).Err()
}

func (r *redisCache) Get(symbol string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	val, err := r.rdb.HGet(ctx, symbol, "ticker").Result()
	if err == redis.Nil {
		return "", fmt.Errorf("symbol %s not found in Redis", symbol)
	}
	return val, err
}
