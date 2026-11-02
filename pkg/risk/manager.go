// Package risk enforces trading limits and safety rules before any order is placed.
package risk

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"adnan/binance-bot/pkg/config"
	"adnan/binance-bot/pkg/state"
)

// Decision is the outcome of a risk evaluation.
type Decision struct {
	Allowed  bool
	Reason   string
	Quantity float64 // USDT amount approved for this trade
}

// Manager checks all risk rules before a trade is allowed.
type Manager struct {
	cfg           *config.BotConfig
	stateMgr      state.StateManager
	dailyStartPnL float64
	startTime     time.Time
}

// NewManager creates a new risk Manager.
func NewManager(cfg *config.BotConfig, stateMgr state.StateManager) *Manager {
	return &Manager{
		cfg:       cfg,
		stateMgr:  stateMgr,
		startTime: time.Now(),
	}
}

// CheckEntry evaluates whether it is safe to open a new position for the given symbol.
// overrideQty, if > 0, replaces the configured MaxPositionUSDT as the trade size.
// It returns a Decision indicating whether the trade is allowed and at what size.
func (m *Manager) CheckEntry(ctx context.Context, symbol string, currentPrice float64, overrideQty float64) Decision {
	// 1. Max simultaneous positions
	count, err := m.stateMgr.CountOpenPositions(ctx)
	if err != nil {
		return deny(fmt.Sprintf("failed to count positions: %v", err))
	}
	if count >= m.cfg.MaxPositions {
		return deny(fmt.Sprintf("max positions reached (%d/%d)", count, m.cfg.MaxPositions))
	}

	// 2. No duplicate position for the same symbol
	has, err := m.stateMgr.HasOpenPosition(ctx, symbol)
	if err != nil {
		return deny(fmt.Sprintf("position lookup failed: %v", err))
	}
	if has {
		return deny(fmt.Sprintf("already have open position for %s", symbol))
	}

	// 3. Daily loss kill-switch
	if blocked, reason := m.dailyLossKillSwitch(ctx); blocked {
		return deny(reason)
	}

	qty := m.cfg.MaxPositionUSDT
	if overrideQty > 0 {
		qty = overrideQty
	}

	return Decision{
		Allowed:  true,
		Reason:   "all risk checks passed",
		Quantity: qty,
	}
}

// CheckStopLoss returns true if the current price has hit the stop-loss for an open position.
func (m *Manager) CheckStopLoss(pos *state.Position, currentPrice float64) bool {
	if pos.Side != "BUY" {
		return false
	}
	stopPrice := pos.EntryPrice * (1 - m.cfg.StopLossPct/100)
	hit := currentPrice <= stopPrice
	if hit {
		slog.Warn("stop-loss triggered",
			"symbol", pos.Symbol,
			"entry", pos.EntryPrice,
			"current", currentPrice,
			"stop_price", stopPrice,
		)
	}
	return hit
}

// CheckTakeProfit returns true if the current price has hit the take-profit for an open position.
func (m *Manager) CheckTakeProfit(pos *state.Position, currentPrice float64) bool {
	if pos.Side != "BUY" {
		return false
	}
	targetPrice := pos.EntryPrice * (1 + m.cfg.TakeProfitPct/100)
	hit := currentPrice >= targetPrice
	if hit {
		slog.Info("take-profit triggered",
			"symbol", pos.Symbol,
			"entry", pos.EntryPrice,
			"current", currentPrice,
			"target_price", targetPrice,
		)
	}
	return hit
}

// CheckMaxHold returns true if the position has been open longer than cfg.MaxHoldHours.
// If cfg.MaxHoldHours <= 0 or pos.OpenedAt is zero, returns false (disabled).
func (m *Manager) CheckMaxHold(pos *state.Position, now time.Time) bool {
	if m.cfg.MaxHoldHours <= 0 || pos.OpenedAt.IsZero() {
		return false
	}
	maxDuration := time.Duration(m.cfg.MaxHoldHours * float64(time.Hour))
	hit := now.Sub(pos.OpenedAt) >= maxDuration
	if hit {
		slog.Info("max-hold duration exceeded",
			"symbol", pos.Symbol,
			"opened_at", pos.OpenedAt,
			"held_hours", now.Sub(pos.OpenedAt).Hours(),
			"max_hold_hours", m.cfg.MaxHoldHours,
		)
	}
	return hit
}

// CalcPnL calculates realised net P&L in USDT when closing a BUY position.
// proceeds = netQuantity * exitPrice, minus the estimated sell fee (sellFeePct, e.g. 0.1 = 0.1%).
// pos.Quantity is already net of any buy-side base-asset commission, and
// pos.QuoteSpent is the total USDT paid on entry (buy fee in USDT is included in it).
func CalcPnL(pos *state.Position, exitPrice, sellFeePct float64) float64 {
	proceeds := pos.Quantity * exitPrice * (1 - sellFeePct/100)
	return proceeds - pos.QuoteSpent
}

// dailyLossKillSwitch checks if daily loss exceeds the configured threshold.
func (m *Manager) dailyLossKillSwitch(ctx context.Context) (blocked bool, reason string) {
	dailyPnL, err := m.stateMgr.GetDailyPnL(ctx)
	if err != nil {
		return false, "" // don't block on lookup error
	}

	// Only check losses (negative P&L)
	if dailyPnL >= 0 {
		return false, ""
	}

	// Calculate loss as % of total position capacity as proxy for account size
	capacity := float64(m.cfg.MaxPositions) * m.cfg.MaxPositionUSDT
	lossPct := (-dailyPnL / capacity) * 100

	if lossPct >= m.cfg.MaxDailyLossPct {
		reason := fmt.Sprintf("daily loss kill-switch: %.2f%% loss >= %.2f%% threshold (P&L: $%.2f)",
			lossPct, m.cfg.MaxDailyLossPct, dailyPnL)
		slog.Error("kill-switch activated", "daily_pnl_usdt", dailyPnL, "loss_pct", lossPct)
		return true, reason
	}
	return false, ""
}

func deny(reason string) Decision {
	return Decision{Allowed: false, Reason: reason}
}
