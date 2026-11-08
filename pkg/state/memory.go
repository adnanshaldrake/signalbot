// Package state provides state managers for trading positions and P&L.
package state

import (
	"context"
	"log/slog"
	"sync"

	"github.com/adnanshaldrake/signalbot/pkg/store"
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
	return m.ClosePositionWithDetails(context.Background(), symbol, 0, realisedPnL, "")
}

// ClosePositionWithDetails removes the position and accumulates P&L.
func (m *MemoryManager) ClosePositionWithDetails(_ context.Context, symbol string, _ float64, realisedPnL float64, _ string) error {
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
// SQLiteManager and MemoryManager implement this interface.
// The rest of the app uses this interface so the backend is swappable.

// StateManager is the interface satisfied by SQLite and in-memory managers.
type StateManager interface {
	Close() error
	OpenPosition(ctx context.Context, pos Position) error
	ClosePosition(ctx context.Context, symbol string, realisedPnL float64) error
	ClosePositionWithDetails(ctx context.Context, symbol string, exitPrice float64, realisedPnL float64, exitReason string) error
	GetPosition(ctx context.Context, symbol string) (*Position, error)
	GetAllPositions(ctx context.Context) ([]Position, error)
	CountOpenPositions(ctx context.Context) (int, error)
	HasOpenPosition(ctx context.Context, symbol string) (bool, error)
	GetDailyPnL(ctx context.Context) (float64, error)
}

// NewStateManager returns a persistent SQLite manager if db is provided,
// otherwise falls back to an in-memory manager if db is nil.
func NewStateManager(db *store.DB) (StateManager, error) {
	if db != nil {
		slog.Info("using persistent SQLite state manager (positions survive restart)")
		return NewSQLiteManager(db), nil
	}
	slog.Info("no DB provided — using in-memory state (positions lost on restart)")
	return NewMemoryManager(), nil
}
