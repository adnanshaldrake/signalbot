// Package state provides an in-memory state manager for local/no-Redis runs.
// It implements the same interface as the Redis Manager so the rest of the
// codebase doesn't need to know which backend is in use.
package state

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// MemoryManager implements position tracking entirely in-process.
// Data is lost on restart — suitable for local development and paper trading.
type MemoryManager struct {
	mu        sync.RWMutex
	positions map[string]Position
	dailyPnL  float64
}

// NewMemoryManager creates a new in-memory state manager.
func NewMemoryManager() *MemoryManager {
	return &MemoryManager{
		positions: make(map[string]Position),
	}
}

// Close is a no-op for the memory manager (satisfies the Manager interface pattern).
func (m *MemoryManager) Close() error { return nil }

// OpenPosition stores a new position.
func (m *MemoryManager) OpenPosition(_ context.Context, pos Position) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.positions[pos.Symbol] = pos
	return nil
}

// ClosePosition removes the position and accumulates P&L.
func (m *MemoryManager) ClosePosition(_ context.Context, symbol string, realisedPnL float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.positions, symbol)
	m.dailyPnL += realisedPnL
	return nil
}

// GetPosition returns the open position for a symbol, or nil if none exists.
func (m *MemoryManager) GetPosition(_ context.Context, symbol string) (*Position, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pos, ok := m.positions[symbol]
	if !ok {
		return nil, nil
	}
	return &pos, nil
}

// GetAllPositions returns all currently open positions.
func (m *MemoryManager) GetAllPositions(_ context.Context) ([]Position, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Position, 0, len(m.positions))
	for _, pos := range m.positions {
		out = append(out, pos)
	}
	return out, nil
}

// CountOpenPositions returns the number of open positions.
func (m *MemoryManager) CountOpenPositions(_ context.Context) (int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.positions), nil
}

// HasOpenPosition reports whether there is an open position for the given symbol.
func (m *MemoryManager) HasOpenPosition(_ context.Context, symbol string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.positions[symbol]
	return ok, nil
}

// GetDailyPnL returns today's accumulated realised P&L.
func (m *MemoryManager) GetDailyPnL(_ context.Context) (float64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.dailyPnL, nil
}

// --- StateManager interface ---
// Both Manager (Redis) and MemoryManager implement this interface.
// The rest of the app uses this interface so the backend is swappable.

// StateManager is the interface satisfied by both Redis and in-memory managers.
type StateManager interface {
	Close() error
	OpenPosition(ctx context.Context, pos Position) error
	ClosePosition(ctx context.Context, symbol string, realisedPnL float64) error
	GetPosition(ctx context.Context, symbol string) (*Position, error)
	GetAllPositions(ctx context.Context) ([]Position, error)
	CountOpenPositions(ctx context.Context) (int, error)
	HasOpenPosition(ctx context.Context, symbol string) (bool, error)
	GetDailyPnL(ctx context.Context) (float64, error)
}

// NewStateManager returns a Redis-backed manager if REDIS_URL is set,
// otherwise falls back to an in-memory manager.
func NewStateManager(redisURL, redisUser, redisPass string) (StateManager, error) {
	if redisURL == "" {
		slog.Info("REDIS_URL not set — using in-memory state (positions lost on restart)")
		return NewMemoryManager(), nil
	}
	// Redis URL configured — connect and verify
	redisMgr, err := NewManager2(redisURL, redisUser, redisPass)
	if err != nil {
		return nil, fmt.Errorf("redis connection failed: %w", err)
	}
	slog.Info("connected to Redis", "url", redisURL)
	return redisMgr, nil
}

// Position (re-exported for use through the interface — defined in positions.go)
// Dummy usage to avoid "declared but not used" — the real struct is in positions.go
var _ = Position{OpenedAt: time.Time{}}
