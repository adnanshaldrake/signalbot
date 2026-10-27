// Package state provides position tracking backends.
package state

import (
	"context"
	"fmt"
	"time"

	"adnan/binance-bot/pkg/store"
)

// SQLiteManager manages active positions and closed trades in SQLite.
// Open positions and daily PnL survive bot restarts.
type SQLiteManager struct {
	db *store.DB
}

// NewSQLiteManager creates a SQLite-backed StateManager.
func NewSQLiteManager(db *store.DB) *SQLiteManager {
	return &SQLiteManager{db: db}
}

// Close is a no-op (the database connection lifecycle is managed by main/store).
func (m *SQLiteManager) Close() error { return nil }

// OpenPosition stores a new position in SQLite.
func (m *SQLiteManager) OpenPosition(_ context.Context, pos Position) error {
	sp := store.StoredPosition{
		Symbol:     pos.Symbol,
		Side:       pos.Side,
		EntryPrice: pos.EntryPrice,
		Quantity:   pos.Quantity,
		QuoteSpent: pos.QuoteSpent,
		OrderID:    pos.OrderID,
		OpenedAt:   pos.OpenedAt,
	}
	return m.db.UpsertPosition(sp)
}

// ClosePosition removes the position and records a closed trade with realised PnL.
func (m *SQLiteManager) ClosePosition(ctx context.Context, symbol string, realisedPnL float64) error {
	return m.ClosePositionWithDetails(ctx, symbol, 0, realisedPnL, "")
}

// ClosePositionWithDetails removes the position and records the trade with exit price and reason.
func (m *SQLiteManager) ClosePositionWithDetails(_ context.Context, symbol string, exitPrice float64, realisedPnL float64, exitReason string) error {
	pos, err := m.db.GetPosition(symbol)
	if err != nil {
		return fmt.Errorf("lookup position %s: %w", symbol, err)
	}

	if pos != nil {
		now := time.Now()
		calcExitPrice := exitPrice
		if calcExitPrice <= 0 && pos.Quantity > 0 && pos.QuoteSpent > 0 {
			calcExitPrice = pos.EntryPrice * (1 + realisedPnL/pos.QuoteSpent)
		}
		pnlPct := 0.0
		if pos.QuoteSpent > 0 {
			pnlPct = (realisedPnL / pos.QuoteSpent) * 100
		}
		reason := exitReason
		if reason == "" {
			if realisedPnL > 0 {
				reason = "TAKE_PROFIT"
			} else if realisedPnL < 0 {
				reason = "STOP_LOSS"
			} else {
				reason = "EXIT"
			}
		}

		trade := store.Trade{
			OrderID:    pos.OrderID,
			Symbol:     pos.Symbol,
			Side:       pos.Side,
			EntryPrice: pos.EntryPrice,
			ExitPrice:  calcExitPrice,
			Quantity:   pos.Quantity,
			QuoteSpent: pos.QuoteSpent,
			PnL:        realisedPnL,
			PnLPct:     pnlPct,
			ExitReason: reason,
			OpenedAt:   pos.OpenedAt,
			ClosedAt:   now,
		}
		_ = m.db.InsertTrade(trade)
	}

	return m.db.DeletePosition(symbol)
}

// GetPosition returns the open position for a symbol, or nil if none exists.
func (m *SQLiteManager) GetPosition(_ context.Context, symbol string) (*Position, error) {
	sp, err := m.db.GetPosition(symbol)
	if err != nil {
		return nil, err
	}
	if sp == nil {
		return nil, nil
	}
	return &Position{
		Symbol:     sp.Symbol,
		Side:       sp.Side,
		EntryPrice: sp.EntryPrice,
		Quantity:   sp.Quantity,
		QuoteSpent: sp.QuoteSpent,
		OrderID:    sp.OrderID,
		OpenedAt:   sp.OpenedAt,
	}, nil
}

// GetAllPositions returns all currently open positions.
func (m *SQLiteManager) GetAllPositions(_ context.Context) ([]Position, error) {
	sps, err := m.db.GetAllPositions()
	if err != nil {
		return nil, err
	}
	positions := make([]Position, 0, len(sps))
	for _, sp := range sps {
		positions = append(positions, Position{
			Symbol:     sp.Symbol,
			Side:       sp.Side,
			EntryPrice: sp.EntryPrice,
			Quantity:   sp.Quantity,
			QuoteSpent: sp.QuoteSpent,
			OrderID:    sp.OrderID,
			OpenedAt:   sp.OpenedAt,
		})
	}
	return positions, nil
}

// CountOpenPositions returns the number of open positions.
func (m *SQLiteManager) CountOpenPositions(_ context.Context) (int, error) {
	return m.db.CountPositions()
}

// HasOpenPosition reports whether an open position exists for the given symbol.
func (m *SQLiteManager) HasOpenPosition(_ context.Context, symbol string) (bool, error) {
	return m.db.HasPosition(symbol)
}

// GetDailyPnL returns today's accumulated realised P&L from SQLite trades.
func (m *SQLiteManager) GetDailyPnL(_ context.Context) (float64, error) {
	now := time.Now().UTC()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return m.db.GetDailyPnL(startOfDay)
}
