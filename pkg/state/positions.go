// Package state manages open trade positions.
package state

import (
	"time"
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
