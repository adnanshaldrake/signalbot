// Package state manages open trade positions persisted in Redis.
// Positions survive bot restarts — the bot reads its current state from Redis
// on startup rather than assuming a clean slate.
package state

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"adnan/binance-bot/pkg/config"

	"github.com/redis/go-redis/v9"
)

const (
	positionsKey   = "signalbot:positions"  // Redis hash: field=symbol, value=JSON Position
	dailyPnLKey    = "signalbot:daily_pnl"  // Redis string: today's realised P&L in USDT
	dailyPnLExpiry = 25 * time.Hour         // Expires slightly after a day to cover timezone drift
)

// Position represents a single open trade.
type Position struct {
	Symbol     string    `json:"symbol"`
	Side       string    `json:"side"`        // "BUY" or "SELL"
	EntryPrice float64   `json:"entry_price"` // Average fill price
	Quantity   float64   `json:"quantity"`    // Base asset quantity held
	QuoteSpent float64   `json:"quote_spent"` // USDT spent to open the position
	OrderID    int64     `json:"order_id"`
	OpenedAt   time.Time `json:"opened_at"`
}

// Manager handles reading and writing position state from/to Redis.
type Manager struct {
	rdb *redis.Client
}

// NewManager creates a new state Manager using the provided BotConfig.
func NewManager(cfg *config.BotConfig) (*Manager, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisURL,
		Username: cfg.RedisUsername,
		Password: cfg.RedisPassword,
		DB:       0,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("state: redis ping failed: %w", err)
	}
	return &Manager{rdb: rdb}, nil
}

// Close releases the underlying Redis connection.
func (m *Manager) Close() error {
	return m.rdb.Close()
}

// OpenPosition stores a new position in Redis.
func (m *Manager) OpenPosition(ctx context.Context, pos Position) error {
	data, err := json.Marshal(pos)
	if err != nil {
		return fmt.Errorf("OpenPosition: marshal: %w", err)
	}
	return m.rdb.HSet(ctx, positionsKey, pos.Symbol, string(data)).Err()
}

// ClosePosition removes the position for a symbol and records the realised P&L.
func (m *Manager) ClosePosition(ctx context.Context, symbol string, realisedPnL float64) error {
	pipe := m.rdb.Pipeline()

	// Remove from open positions
	pipe.HDel(ctx, positionsKey, symbol)

	// Accumulate daily P&L (stored as a float string)
	pipe.IncrByFloat(ctx, dailyPnLKey, realisedPnL)
	pipe.Expire(ctx, dailyPnLKey, dailyPnLExpiry)

	_, err := pipe.Exec(ctx)
	return err
}

// GetPosition returns the open position for a symbol, or nil if none exists.
func (m *Manager) GetPosition(ctx context.Context, symbol string) (*Position, error) {
	val, err := m.rdb.HGet(ctx, positionsKey, symbol).Result()
	if err == redis.Nil {
		return nil, nil // no open position
	}
	if err != nil {
		return nil, fmt.Errorf("GetPosition %s: %w", symbol, err)
	}

	var pos Position
	if err := json.Unmarshal([]byte(val), &pos); err != nil {
		return nil, fmt.Errorf("GetPosition %s: unmarshal: %w", symbol, err)
	}
	return &pos, nil
}

// GetAllPositions returns all currently open positions.
func (m *Manager) GetAllPositions(ctx context.Context) ([]Position, error) {
	vals, err := m.rdb.HGetAll(ctx, positionsKey).Result()
	if err != nil {
		return nil, fmt.Errorf("GetAllPositions: %w", err)
	}

	positions := make([]Position, 0, len(vals))
	for symbol, raw := range vals {
		var pos Position
		if err := json.Unmarshal([]byte(raw), &pos); err != nil {
			return nil, fmt.Errorf("GetAllPositions: unmarshal %s: %w", symbol, err)
		}
		positions = append(positions, pos)
	}
	return positions, nil
}

// CountOpenPositions returns the number of currently open positions.
func (m *Manager) CountOpenPositions(ctx context.Context) (int, error) {
	count, err := m.rdb.HLen(ctx, positionsKey).Result()
	return int(count), err
}

// HasOpenPosition reports whether there is an open position for the given symbol.
func (m *Manager) HasOpenPosition(ctx context.Context, symbol string) (bool, error) {
	exists, err := m.rdb.HExists(ctx, positionsKey, symbol).Result()
	return exists, err
}

// GetDailyPnL returns today's accumulated realised P&L in USDT.
func (m *Manager) GetDailyPnL(ctx context.Context) (float64, error) {
	val, err := m.rdb.Get(ctx, dailyPnLKey).Float64()
	if err == redis.Nil {
		return 0, nil
	}
	return val, err
}
