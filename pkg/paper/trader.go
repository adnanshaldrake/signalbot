// Package paper provides a simulated trading executor that mirrors the real Binance
// client interface but writes no real orders. Virtual positions and balances are
// tracked in Redis so paper trading state also survives restarts.
package paper

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"adnan/binance-bot/pkg/binance"
	"adnan/binance-bot/pkg/config"
	"adnan/binance-bot/pkg/risk"
	"adnan/binance-bot/pkg/state"
)

const startingBalanceUSDT = 1000.0 // Virtual starting balance for paper trading

// Trader simulates order execution without touching real funds.
type Trader struct {
	cfg      *config.BotConfig
	stateMgr state.StateManager
}

// NewTrader creates a new paper Trader.
func NewTrader(cfg *config.BotConfig, stateMgr state.StateManager) *Trader {
	return &Trader{cfg: cfg, stateMgr: stateMgr}
}

// PlaceMarketOrder simulates a market buy or sell and records the position in Redis.
// It returns a fake OrderResponse consistent with the real Binance client's return type.
func (t *Trader) PlaceMarketOrder(
	ctx context.Context,
	symbol string,
	side binance.OrderSide,
	currentPrice float64,
	quoteQty float64,
) (*binance.OrderResponse, error) {
	now := time.Now()

	// Estimate quantity of base asset from quote amount and current price
	baseQty := quoteQty / currentPrice

	switch side {
	case binance.Buy:
		// Buy fee is taken from the base asset, so the held quantity is net of the fee
		netQty := baseQty * (1 - t.cfg.TakerFeePct/100)
		pos := state.Position{
			Symbol:     symbol,
			Side:       "BUY",
			EntryPrice: currentPrice,
			Quantity:   netQty,
			QuoteSpent: quoteQty,
			OrderID:    now.UnixNano(), // fake order ID
			OpenedAt:   now,
		}
		if err := t.stateMgr.OpenPosition(ctx, pos); err != nil {
			return nil, fmt.Errorf("paper buy: record position: %w", err)
		}
		slog.Info("[PAPER] BUY executed",
			"symbol", symbol,
			"price", currentPrice,
			"usdt_spent", quoteQty,
			"qty", baseQty,
		)

	case binance.Sell:
		pos, err := t.stateMgr.GetPosition(ctx, symbol)
		if err != nil {
			return nil, fmt.Errorf("paper sell: lookup position: %w", err)
		}
		if pos == nil {
			return nil, fmt.Errorf("paper sell: no open position for %s", symbol)
		}

		pnl := risk.CalcPnL(pos, currentPrice, t.cfg.TakerFeePct)
		if err := t.stateMgr.ClosePosition(ctx, symbol, pnl); err != nil {
			return nil, fmt.Errorf("paper sell: close position: %w", err)
		}
		slog.Info("[PAPER] SELL executed",
			"symbol", symbol,
			"entry_price", pos.EntryPrice,
			"exit_price", currentPrice,
			"pnl_usdt", fmt.Sprintf("%.4f", pnl),
			"pnl_pct", fmt.Sprintf("%.2f%%", pnl/pos.QuoteSpent*100),
		)
	}

	// Return a fake response that looks like the real Binance API response
	return &binance.OrderResponse{
		Symbol:              symbol,
		OrderID:             now.UnixNano(),
		TransactTime:        now.UnixMilli(),
		OrigQty:             fmt.Sprintf("%.8f", baseQty),
		ExecutedQty:         fmt.Sprintf("%.8f", baseQty),
		CummulativeQuoteQty: fmt.Sprintf("%.2f", quoteQty),
		Status:              "FILLED",
		Type:                "MARKET",
		Side:                string(side),
	}, nil
}

// GetVirtualBalance returns the estimated remaining virtual USDT balance.
// It starts from startingBalanceUSDT and subtracts the quote value of open positions
// then adjusts for daily P&L.
func (t *Trader) GetVirtualBalance(ctx context.Context) (float64, error) {
	positions, err := t.stateMgr.GetAllPositions(ctx)
	if err != nil {
		return 0, err
	}

	lockedUSDT := 0.0
	for _, pos := range positions {
		lockedUSDT += pos.QuoteSpent
	}

	dailyPnL, err := t.stateMgr.GetDailyPnL(ctx)
	if err != nil {
		return 0, err
	}

	return startingBalanceUSDT + dailyPnL - lockedUSDT, nil
}
