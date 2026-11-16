package metrics

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/adnanshaldrake/signalbot/pkg/store"
)

const defaultStartingBalanceUSDT = 1000.0

// TelemetryOptions configures public telemetry generation.
type TelemetryOptions struct {
	StartingBalanceUSDT float64
	Mode                string
	Now                 time.Time
}

// PublicTrade is a zero-PII, strategy-scrubbed record of a completed trade.
// Internal order IDs, account balances, and indicator/scoring values are omitted.
type PublicTrade struct {
	Symbol          string    `json:"symbol"`
	Side            string    `json:"side"`
	EntryPrice      float64   `json:"entry_price"`
	ExitPrice       float64   `json:"exit_price"`
	PnLUSDT         float64   `json:"pnl_usdt"`
	PnLPct          float64   `json:"pnl_pct"`
	ExitReason      string    `json:"exit_reason"`
	HoldDurationSec int64     `json:"hold_duration_sec"`
	OpenedAt        time.Time `json:"opened_at"`
	ClosedAt        time.Time `json:"closed_at"`
}

// PublicPosition is a zero-PII snapshot of an active open position.
type PublicPosition struct {
	Symbol     string    `json:"symbol"`
	Side       string    `json:"side"`
	EntryPrice float64   `json:"entry_price"`
	OpenedAt   time.Time `json:"opened_at"`
}

// EquityPoint represents a single point on the portfolio equity curve.
type EquityPoint struct {
	Timestamp     time.Time `json:"timestamp"`
	EquityUSDT    float64   `json:"equity_usdt"`
	CumulativePnL float64   `json:"cumulative_pnl"`
	ReturnPct     float64   `json:"return_pct"`
}

// PublicPerformance is the sanitized performance payload served at performance.json.
type PublicPerformance struct {
	GeneratedAt         time.Time        `json:"generated_at"`
	Mode                string           `json:"mode"`
	StartingBalanceUSDT float64          `json:"starting_balance_usdt"`
	CurrentEquityUSDT   float64          `json:"current_equity_usdt"`
	TotalPnLUSDT        float64          `json:"total_pnl_usdt"`
	TotalReturnPct      float64          `json:"total_return_pct"`
	TotalTrades         int              `json:"total_trades"`
	Wins                int              `json:"wins"`
	Losses              int              `json:"losses"`
	WinRate             float64          `json:"win_rate"`
	ProfitFactor        float64          `json:"profit_factor"`
	AvgWinUSDT          float64          `json:"avg_win_usdt"`
	AvgLossUSDT         float64          `json:"avg_loss_usdt"`
	MaxDrawdownUSDT     float64          `json:"max_drawdown_usdt"`
	MaxDrawdownPct      float64          `json:"max_drawdown_pct"`
	TodayTrades         int              `json:"today_trades"`
	TodayPnLUSDT        float64          `json:"today_pnl_usdt"`
	DailySummaries      []DailySummary   `json:"daily_summaries"`
	EquityCurve         []EquityPoint    `json:"equity_curve"`
	OpenPositions       []PublicPosition `json:"open_positions"`
}

// PublicTradesFeed is the sanitized closed-trade feed served at trades.json.
type PublicTradesFeed struct {
	GeneratedAt time.Time     `json:"generated_at"`
	TotalTrades int           `json:"total_trades"`
	Trades      []PublicTrade `json:"trades"`
}

// PublicTelemetry bundles both public feeds for combined export or webhook publishing.
type PublicTelemetry struct {
	Performance PublicPerformance `json:"performance"`
	Trades      PublicTradesFeed  `json:"trades"`
}

// BuildPublicTelemetry computes scrubbed public performance metrics, equity curve,
// max drawdown, and trade audit records from raw store slices.
func BuildPublicTelemetry(trades []store.Trade, positions []store.StoredPosition, opts TelemetryOptions) PublicTelemetry {
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	startingBalance := opts.StartingBalanceUSDT
	if startingBalance <= 0 {
		startingBalance = defaultStartingBalanceUSDT
	}

	mode := opts.Mode
	if mode == "" {
		mode = "PAPER"
	}

	// Sort a local copy chronologically ascending by ClosedAt for equity curve & drawdown math.
	chronTrades := make([]store.Trade, len(trades))
	copy(chronTrades, trades)
	sort.Slice(chronTrades, func(i, j int) bool {
		return chronTrades[i].ClosedAt.Before(chronTrades[j].ClosedAt)
	})

	base := Calculate(chronTrades, positions, now)

	daily := base.DailySummaries
	if daily == nil {
		daily = make([]DailySummary, 0)
	}

	equityCurve := make([]EquityPoint, 0, len(chronTrades)+1)
	anchorTime := now
	if len(chronTrades) > 0 && !chronTrades[0].OpenedAt.IsZero() {
		anchorTime = chronTrades[0].OpenedAt.UTC()
	}
	equityCurve = append(equityCurve, EquityPoint{
		Timestamp:     anchorTime,
		EquityUSDT:    startingBalance,
		CumulativePnL: 0,
		ReturnPct:     0,
	})

	cumPnL := 0.0
	peakEquity := startingBalance
	maxDDUSDT := 0.0
	maxDDPct := 0.0

	for _, t := range chronTrades {
		cumPnL += t.PnL
		equity := startingBalance + cumPnL
		if equity > peakEquity {
			peakEquity = equity
		}
		dd := peakEquity - equity
		if dd > maxDDUSDT {
			maxDDUSDT = dd
		}
		if peakEquity > 0 {
			ddPct := (dd / peakEquity) * 100.0
			if ddPct > maxDDPct {
				maxDDPct = ddPct
			}
		}

		equityCurve = append(equityCurve, EquityPoint{
			Timestamp:     t.ClosedAt.UTC(),
			EquityUSDT:    equity,
			CumulativePnL: cumPnL,
			ReturnPct:     (cumPnL / startingBalance) * 100.0,
		})
	}

	pubPositions := make([]PublicPosition, 0, len(positions))
	for _, p := range positions {
		pubPositions = append(pubPositions, PublicPosition{
			Symbol:     p.Symbol,
			Side:       p.Side,
			EntryPrice: p.EntryPrice,
			OpenedAt:   p.OpenedAt.UTC(),
		})
	}

	// Public trades list ordered most-recent first for UI table display.
	pubTrades := make([]PublicTrade, 0, len(chronTrades))
	for i := len(chronTrades) - 1; i >= 0; i-- {
		t := chronTrades[i]
		pubTrades = append(pubTrades, PublicTrade{
			Symbol:          t.Symbol,
			Side:            t.Side,
			EntryPrice:      t.EntryPrice,
			ExitPrice:       t.ExitPrice,
			PnLUSDT:         t.PnL,
			PnLPct:          t.PnLPct,
			ExitReason:      t.ExitReason,
			HoldDurationSec: t.HoldDurationSec,
			OpenedAt:        t.OpenedAt.UTC(),
			ClosedAt:        t.ClosedAt.UTC(),
		})
	}

	currentEquity := startingBalance + base.TotalPnL
	totalReturnPct := (base.TotalPnL / startingBalance) * 100.0

	return PublicTelemetry{
		Performance: PublicPerformance{
			GeneratedAt:         now,
			Mode:                mode,
			StartingBalanceUSDT: startingBalance,
			CurrentEquityUSDT:   currentEquity,
			TotalPnLUSDT:        base.TotalPnL,
			TotalReturnPct:      totalReturnPct,
			TotalTrades:         base.TotalTrades,
			Wins:                base.Wins,
			Losses:              base.Losses,
			WinRate:             base.WinRate,
			ProfitFactor:        base.ProfitFactor,
			AvgWinUSDT:          base.AvgWin,
			AvgLossUSDT:         base.AvgLoss,
			MaxDrawdownUSDT:     maxDDUSDT,
			MaxDrawdownPct:      maxDDPct,
			TodayTrades:         base.TodayTrades,
			TodayPnLUSDT:        base.TodayPnL,
			DailySummaries:      daily,
			EquityCurve:         equityCurve,
			OpenPositions:       pubPositions,
		},
		Trades: PublicTradesFeed{
			GeneratedAt: now,
			TotalTrades: len(pubTrades),
			Trades:      pubTrades,
		},
	}
}

// BuildPublicTelemetryFromDB loads trades and open positions from db and constructs PublicTelemetry.
func BuildPublicTelemetryFromDB(db *store.DB, opts TelemetryOptions) (PublicTelemetry, error) {
	completedTrades, err := db.GetAllTrades()
	if err != nil {
		return PublicTelemetry{}, fmt.Errorf("failed to get all trades: %w", err)
	}

	allOpenPositions, err := db.GetAllPositions()
	if err != nil {
		return PublicTelemetry{}, fmt.Errorf("failed to get all open positions: %w", err)
	}

	return BuildPublicTelemetry(completedTrades, allOpenPositions, opts), nil
}

// ExportAuditTelemetry writes the scrubbed PublicTelemetry JSON bundle to w.
func ExportAuditTelemetry(w io.Writer, db *store.DB, opts TelemetryOptions) error {
	payload, err := BuildPublicTelemetryFromDB(db, opts)
	if err != nil {
		return fmt.Errorf("failed to export audit telemetry: %w", err)
	}
	return json.MarshalWrite(w, payload, jsontext.WithIndent(" "))
}

// WriteTelemetryFiles writes sanitized performance.json and trades.json atomically into dir.
func WriteTelemetryFiles(dir string, db *store.DB, opts TelemetryOptions) (PublicTelemetry, error) {
	payload, err := BuildPublicTelemetryFromDB(db, opts)
	if err != nil {
		return PublicTelemetry{}, fmt.Errorf("failed to build public telemetry: %w", err)
	}
	err = os.MkdirAll(dir, 0o755)
	if err != nil {
		return PublicTelemetry{}, fmt.Errorf("failed to create directory: %w", err)
	}
	err = writeJSONAtomic(filepath.Join(dir, "performance.json"), payload.Performance)
	if err != nil {
		return PublicTelemetry{}, fmt.Errorf("failed to write performance.json: %w", err)
	}
	err = writeJSONAtomic(filepath.Join(dir, "trades.json"), payload.Trades)
	if err != nil {
		return PublicTelemetry{}, fmt.Errorf("failed to write trades.json: %w", err)
	}
	return payload, nil
}

// writeJSONAtomic marshals v as indented JSON and writes it to path using a temp file + rename
// so readers never observe a partially written JSON file.
func writeJSONAtomic(path string, v any) error {
	tmpPath := path + ".tmp"
	data, err := json.Marshal(v, jsontext.WithIndent(" "))
	if err != nil {
		return fmt.Errorf("failed to write data. marshalling data failed: %w", err)
	}
	err = os.WriteFile(tmpPath, data, 0o644)
	if err != nil {
		return fmt.Errorf("failed to write data. writing to file failed: %w", err)
	}
	err = os.Rename(tmpPath, path)
	if err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to rename file. Removing tmpFile: %w", err)
	}
	return nil
}
