package metrics

import (
	"fmt"
	"io"
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/adnanshaldrake/signalbot/pkg/store"
)

// DailySummary holds trade aggregations for a single day.
type DailySummary struct {
	Date          string  // YYYY-MM-DD
	Trades        int
	Wins          int
	Losses        int
	DailyPnL      float64
	CumulativePnL float64
}

// TradeMetrics contains overall performance, recent trends, daily breakdown, and active positions.
type TradeMetrics struct {
	TotalTrades    int
	Wins           int
	Losses         int
	WinRate        float64 // percentage, e.g. 62.5
	TotalPnL       float64
	TodayTrades    int
	TodayWins      int
	TodayLosses    int
	TodayPnL       float64
	RecentTrades   int     // count in recent window (up to 10)
	RecentWins     int
	RecentLosses   int
	RecentWinRate  float64 // percentage, e.g. 70.0
	RecentPnL      float64
	PriorWindowPnL float64
	HasPriorWindow bool
	DailySummaries []DailySummary
	OpenPositions  []store.StoredPosition
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
		} else {
			m.Losses++
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
			openedStr := pos.OpenedAt.Format("2006-01-02 15:04:05")
			fmt.Fprintf(w, "%-10s  %-4s  %12.4f  %14.6f  %11.2f$  %-19s\n",
				pos.Symbol, pos.Side, pos.EntryPrice, pos.Quantity, pos.QuoteSpent, openedStr)
		}
	}
	fmt.Fprintln(w, "================================================================================")
}

func formatUSDT(amount float64) string {
	if amount > 0 {
		return fmt.Sprintf("+$%.2f", amount)
	} else if amount < 0 {
		return fmt.Sprintf("-$%.2f", math.Abs(amount))
	}
	return "$0.00"
}
