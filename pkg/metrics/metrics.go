package metrics

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"time"

	"adnan/binance-bot/pkg/store"
)

// DailySummary holds trade aggregations for a single day.
type DailySummary struct {
	Date          string  `json:"date"` // YYYY-MM-DD
	Trades        int     `json:"trades"`
	Wins          int     `json:"wins"`
	Losses        int     `json:"losses"`
	DailyPnL      float64 `json:"daily_pnl"`
	CumulativePnL float64 `json:"cumulative_pnl"`
}

// TradeMetrics contains overall performance, recent trends, daily breakdown, and active positions.
type TradeMetrics struct {
	TotalTrades    int                    `json:"total_trades"`
	Wins           int                    `json:"wins"`
	Losses         int                    `json:"losses"`
	WinRate        float64                `json:"win_rate"` // percentage, e.g. 62.5
	TotalPnL       float64                `json:"total_pnl"`
	GrossProfit    float64                `json:"gross_profit"`
	GrossLoss      float64                `json:"gross_loss"`
	AvgWin         float64                `json:"avg_win"`
	AvgLoss        float64                `json:"avg_loss"`
	ProfitFactor   float64                `json:"profit_factor"`
	TodayTrades    int                    `json:"today_trades"`
	TodayWins      int                    `json:"today_wins"`
	TodayLosses    int                    `json:"today_losses"`
	TodayPnL       float64                `json:"today_pnl"`
	RecentTrades   int                    `json:"recent_trades"` // count in recent window (up to 10)
	RecentWins     int                    `json:"recent_wins"`
	RecentLosses   int                    `json:"recent_losses"`
	RecentWinRate  float64                `json:"recent_win_rate"` // percentage, e.g. 70.0
	RecentPnL      float64                `json:"recent_pnl"`
	PriorWindowPnL float64                `json:"prior_window_pnl"`
	HasPriorWindow bool                   `json:"has_prior_window"`
	RecentSells    []store.Trade          `json:"recent_sells"`
	DailySummaries []DailySummary         `json:"daily_summaries"`
	OpenPositions  []store.StoredPosition `json:"open_positions"`
}

// Calculate computes performance and trend metrics from a list of trades and open positions.
func Calculate(trades []store.Trade, positions []store.StoredPosition, now time.Time) TradeMetrics {
	m := TradeMetrics{
		TotalTrades:   len(trades),
		OpenPositions: positions,
	}

	startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	type dayBucket struct {
		trades int
		wins   int
		losses int
		pnl    float64
	}
	dailyMap := make(map[string]*dayBucket)

	for _, t := range trades {
		m.TotalPnL += t.PnL
		if t.PnL > 0 {
			m.Wins++
			m.GrossProfit += t.PnL
		} else {
			m.Losses++
			m.GrossLoss += math.Abs(t.PnL)
		}

		// Today's stats
		if !t.ClosedAt.Before(startOfToday) {
			m.TodayTrades++
			m.TodayPnL += t.PnL
			if t.PnL > 0 {
				m.TodayWins++
			} else {
				m.TodayLosses++
			}
		}

		// Daily aggregation
		dayKey := t.ClosedAt.In(now.Location()).Format("2006-01-02")
		b, ok := dailyMap[dayKey]
		if !ok {
			b = &dayBucket{}
			dailyMap[dayKey] = b
		}
		b.trades++
		b.pnl += t.PnL
		if t.PnL > 0 {
			b.wins++
		} else {
			b.losses++
		}
	}

	if m.TotalTrades > 0 {
		m.WinRate = float64(m.Wins) / float64(m.TotalTrades) * 100.0
	}
	if m.Wins > 0 {
		m.AvgWin = m.GrossProfit / float64(m.Wins)
	}
	if m.Losses > 0 {
		m.AvgLoss = m.GrossLoss / float64(m.Losses)
	}
	if m.GrossLoss > 0 {
		m.ProfitFactor = m.GrossProfit / m.GrossLoss
	} else if m.GrossProfit > 0 {
		m.ProfitFactor = m.GrossProfit // no losses
	}

	// Recent 10 window
	n := len(trades)
	recentCount := 10
	if n < recentCount {
		recentCount = n
	}
	if recentCount > 0 {
		recentSlice := trades[n-recentCount:]
		m.RecentTrades = recentCount
		for _, t := range recentSlice {
			m.RecentPnL += t.PnL
			if t.PnL > 0 {
				m.RecentWins++
			} else {
				m.RecentLosses++
			}
		}
		m.RecentWinRate = float64(m.RecentWins) / float64(recentCount) * 100.0

		// Prior window comparison (up to 10 trades before the recent window)
		if n > recentCount {
			priorStart := n - (recentCount * 2)
			if priorStart < 0 {
				priorStart = 0
			}
			priorSlice := trades[priorStart : n-recentCount]
			for _, t := range priorSlice {
				m.PriorWindowPnL += t.PnL
			}
			m.HasPriorWindow = true
		}

		// Collect recent sells in reverse chronological order (latest sell first)
		for i := n - 1; i >= n-recentCount; i-- {
			m.RecentSells = append(m.RecentSells, trades[i])
		}
	}

	// Build sorted daily summaries
	var dates []string
	for d := range dailyMap {
		dates = append(dates, d)
	}
	sort.Strings(dates)

	var cumulative float64
	for _, d := range dates {
		b := dailyMap[d]
		cumulative += b.pnl
		m.DailySummaries = append(m.DailySummaries, DailySummary{
			Date:          d,
			Trades:        b.trades,
			Wins:          b.wins,
			Losses:        b.losses,
			DailyPnL:      b.pnl,
			CumulativePnL: cumulative,
		})
	}

	return m
}

// Collect reads all trades and open positions from SQLite and returns aggregated TradeMetrics.
func Collect(db *store.DB) (*TradeMetrics, error) {
	trades, err := db.GetAllTrades()
	if err != nil {
		return nil, fmt.Errorf("metrics: get trades: %w", err)
	}

	positions, err := db.GetAllPositions()
	if err != nil {
		return nil, fmt.Errorf("metrics: get positions: %w", err)
	}

	m := Calculate(trades, positions, time.Now())
	return &m, nil
}

// LogTradeClosed logs cumulative metrics when a position exits.
func LogTradeClosed(db *store.DB, symbol string, tradePnL float64) {
	if db == nil {
		slog.Info("trade closed", "symbol", symbol, "pnl", tradePnL)
		return
	}
	m, err := Collect(db)
	if err != nil {
		slog.Info("trade closed", "symbol", symbol, "pnl", tradePnL)
		return
	}
	slog.Info("trade closed",
		"symbol", symbol,
		"pnl", tradePnL,
		"total_pnl", m.TotalPnL,
		"win_rate", fmt.Sprintf("%.1f%%", m.WinRate),
		"recent_10_wr", fmt.Sprintf("%.1f%%", m.RecentWinRate),
	)
}

// PrintDashboard writes a clean, formatted terminal summary to w.
func PrintDashboard(w io.Writer, db *store.DB) error {
	m, err := Collect(db)
	if err != nil {
		return err
	}
	RenderDashboard(w, m)
	return nil
}

// RenderDashboard formats TradeMetrics into a clear terminal report.
func RenderDashboard(w io.Writer, m *TradeMetrics) {
	fmt.Fprintln(w, "================================================================================")
	fmt.Fprintln(w, "                          SIGNALBOT PERFORMANCE SUMMARY                         ")
	fmt.Fprintln(w, "================================================================================")
	fmt.Fprintf(w, "Total Trades:      %d (%d Wins / %d Losses)\n", m.TotalTrades, m.Wins, m.Losses)
	fmt.Fprintf(w, "Overall Win Rate:  %.1f%%\n", m.WinRate)
	fmt.Fprintf(w, "Total Realized:    %s USDT\n", formatUSDT(m.TotalPnL))
	if m.Wins > 0 || m.Losses > 0 {
		pfStr := fmt.Sprintf("%.2f", m.ProfitFactor)
		if m.GrossLoss == 0 && m.GrossProfit > 0 {
			pfStr = "∞ (No Losses)"
		}
		fmt.Fprintf(w, "Avg Win / Loss:    +$%.2f / -$%.2f (Profit Factor: %s)\n", m.AvgWin, m.AvgLoss, pfStr)
	}
	fmt.Fprintf(w, "Today's Trades:    %d (%d Wins / %d Losses)\n", m.TodayTrades, m.TodayWins, m.TodayLosses)
	fmt.Fprintf(w, "Today's Realized:  %s USDT\n", formatUSDT(m.TodayPnL))

	var lockedCapital float64
	for _, pos := range m.OpenPositions {
		lockedCapital += pos.QuoteSpent
	}
	fmt.Fprintf(w, "Active Positions:  %d ($%.2f USDT locked)\n", len(m.OpenPositions), lockedCapital)

	fmt.Fprintln(w, "\n----------------------------- ARE WE IMPROVING? --------------------------------")
	if m.TotalTrades == 0 {
		fmt.Fprintln(w, "No completed trades recorded yet.")
	} else {
		fmt.Fprintf(w, "Recent Window:     Last %d trades\n", m.RecentTrades)

		wrDiff := m.RecentWinRate - m.WinRate
		var wrTrend string
		if wrDiff > 0 {
			wrTrend = fmt.Sprintf("+%.1f%% 📈 Improving", wrDiff)
		} else if wrDiff < 0 {
			wrTrend = fmt.Sprintf("%.1f%% 📉 Declining", wrDiff)
		} else {
			wrTrend = "0.0% ➖ Neutral"
		}
		fmt.Fprintf(w, "Recent Win Rate:   %.1f%% vs Baseline: %.1f%% (%s)\n", m.RecentWinRate, m.WinRate, wrTrend)

		if m.HasPriorWindow {
			pnlDiff := m.RecentPnL - m.PriorWindowPnL
			var pnlTrend string
			if pnlDiff > 0 {
				pnlTrend = fmt.Sprintf("%s 📈", formatUSDT(pnlDiff))
			} else if pnlDiff < 0 {
				pnlTrend = fmt.Sprintf("%s 📉", formatUSDT(pnlDiff))
			} else {
				pnlTrend = "$0.00 ➖ Neutral"
			}
			fmt.Fprintf(w, "Recent Realized:   %s USDT (Prior Window: %s USDT -> %s)\n",
				formatUSDT(m.RecentPnL), formatUSDT(m.PriorWindowPnL), pnlTrend)
		} else {
			fmt.Fprintf(w, "Recent Realized:   %s USDT\n", formatUSDT(m.RecentPnL))
		}
	}

	fmt.Fprintln(w, "\n--------------------------- RECENT SELLS (LAST 10) -----------------------------")
	if len(m.RecentSells) == 0 {
		fmt.Fprintln(w, "No completed sells recorded yet.")
	} else {
		fmt.Fprintf(w, "%-10s  %-12s  %10s  %10s  %11s  %8s  %9s  %-19s\n",
			"SYMBOL", "REASON", "BUY PRICE", "SELL PRICE", "PNL (USDT)", "PNL (%)", "HOLD TIME", "CLOSED AT")
		for _, t := range m.RecentSells {
			closedStr := t.ClosedAt.Local().Format("2006-01-02 15:04:05")
			reason := t.ExitReason
			if reason == "" {
				reason = "EXIT"
			}
			pnlPct := t.PnLPct
			if pnlPct == 0 && t.QuoteSpent > 0 && t.PnL != 0 {
				pnlPct = (t.PnL / t.QuoteSpent) * 100
			}
			pnlPctStr := fmt.Sprintf("%+.2f%%", pnlPct)

			holdSec := t.HoldDurationSec
			if holdSec == 0 && !t.ClosedAt.IsZero() && !t.OpenedAt.IsZero() {
				holdSec = (t.ClosedAt.UnixMilli() - t.OpenedAt.UnixMilli()) / 1000
			}

			fmt.Fprintf(w, "%-10s  %-12s  %10.4f  %10.4f  %11s  %8s  %9s  %-19s\n",
				t.Symbol, reason, t.EntryPrice, t.ExitPrice, formatUSDT(t.PnL), pnlPctStr, formatDuration(holdSec), closedStr)
		}
	}

	fmt.Fprintln(w, "\n--------------------------- DAILY PERFORMANCE ----------------------------------")
	if len(m.DailySummaries) == 0 {
		fmt.Fprintln(w, "No daily performance history available.")
	} else {
		// Show up to the last 10 days
		summaries := m.DailySummaries
		if len(summaries) > 10 {
			summaries = summaries[len(summaries)-10:]
		}
		fmt.Fprintf(w, "%-12s  %6s  %7s  %14s  %16s\n", "DATE", "TRADES", "W/L", "DAILY PNL", "CUMULATIVE PNL")
		for _, d := range summaries {
			wl := fmt.Sprintf("%d/%d", d.Wins, d.Losses)
			fmt.Fprintf(w, "%-12s  %6d  %7s  %14s  %16s\n",
				d.Date, d.Trades, wl, formatUSDT(d.DailyPnL), formatUSDT(d.CumulativePnL))
		}
	}

	fmt.Fprintln(w, "\n------------------------------- ACTIVE POSITIONS -------------------------------")
	if len(m.OpenPositions) == 0 {
		fmt.Fprintln(w, "None (0 open positions)")
	} else {
		fmt.Fprintf(w, "%-10s  %-4s  %12s  %14s  %12s  %-19s\n",
			"SYMBOL", "SIDE", "ENTRY PRICE", "QUANTITY", "SPENT USDT", "OPENED AT")
		for _, pos := range m.OpenPositions {
			openedStr := pos.OpenedAt.Local().Format("2006-01-02 15:04:05")
			fmt.Fprintf(w, "%-10s  %-4s  %12.4f  %14.6f  %11.2f$  %-19s\n",
				pos.Symbol, pos.Side, pos.EntryPrice, pos.Quantity, pos.QuoteSpent, openedStr)
		}
	}
	fmt.Fprintln(w, "================================================================================")
}

// ExportTradesJSON writes all completed trades formatted as indented JSON to w.
func ExportTradesJSON(w io.Writer, db *store.DB) error {
	trades, err := db.GetAllTrades()
	if err != nil {
		return fmt.Errorf("export trades json: %w", err)
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(trades)
}

// ExportTradesCSV writes all completed trades formatted as CSV to w.
func ExportTradesCSV(w io.Writer, db *store.DB) error {
	trades, err := db.GetAllTrades()
	if err != nil {
		return fmt.Errorf("export trades csv: %w", err)
	}

	writer := csv.NewWriter(w)
	defer writer.Flush()

	header := []string{
		"id", "order_id", "symbol", "side",
		"entry_price", "exit_price", "quantity", "quote_spent",
		"pnl", "pnl_pct", "exit_reason",
		"entry_rsi", "entry_bb_width", "entry_score", "entry_vol_ratio",
		"hold_duration_sec", "opened_at", "closed_at",
	}
	if err := writer.Write(header); err != nil {
		return err
	}

	for _, t := range trades {
		row := []string{
			strconv.FormatInt(t.ID, 10),
			strconv.FormatInt(t.OrderID, 10),
			t.Symbol,
			t.Side,
			strconv.FormatFloat(t.EntryPrice, 'f', 8, 64),
			strconv.FormatFloat(t.ExitPrice, 'f', 8, 64),
			strconv.FormatFloat(t.Quantity, 'f', 8, 64),
			strconv.FormatFloat(t.QuoteSpent, 'f', 4, 64),
			strconv.FormatFloat(t.PnL, 'f', 4, 64),
			strconv.FormatFloat(t.PnLPct, 'f', 4, 64),
			t.ExitReason,
			strconv.FormatFloat(t.EntryRSI, 'f', 4, 64),
			strconv.FormatFloat(t.EntryBBWidth, 'f', 6, 64),
			strconv.FormatFloat(t.EntryScore, 'f', 4, 64),
			strconv.FormatFloat(t.EntryVolRatio, 'f', 4, 64),
			strconv.FormatInt(t.HoldDurationSec, 10),
			t.OpenedAt.UTC().Format(time.RFC3339),
			t.ClosedAt.UTC().Format(time.RFC3339),
		}
		if err := writer.Write(row); err != nil {
			return err
		}
	}
	return nil
}

func formatUSDT(amount float64) string {
	if amount > 0 {
		return fmt.Sprintf("+$%.2f", amount)
	} else if amount < 0 {
		return fmt.Sprintf("-$%.2f", math.Abs(amount))
	}
	return "$0.00"
}

func formatDuration(sec int64) string {
	if sec <= 0 {
		return "< 1m"
	}
	d := time.Duration(sec) * time.Second
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if hours > 0 {
		return fmt.Sprintf("%dh %02dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}
