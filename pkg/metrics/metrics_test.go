package metrics

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"adnan/binance-bot/pkg/store"
)

func TestCalculate_Empty(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	m := Calculate(nil, nil, now)

	if m.TotalTrades != 0 {
		t.Fatalf("expected 0 total trades, got %d", m.TotalTrades)
	}
	if m.WinRate != 0 {
		t.Fatalf("expected 0 win rate, got %f", m.WinRate)
	}
	if m.TotalPnL != 0 {
		t.Fatalf("expected 0 total pnl, got %f", m.TotalPnL)
	}
	if len(m.DailySummaries) != 0 {
		t.Fatalf("expected 0 daily summaries, got %d", len(m.DailySummaries))
	}

	var buf bytes.Buffer
	RenderDashboard(&buf, &m)
	out := buf.String()
	if !strings.Contains(out, "No completed trades recorded yet.") {
		t.Errorf("expected empty trade notice in output: %s", out)
	}
	if !strings.Contains(out, "None (0 open positions)") {
		t.Errorf("expected 0 open positions notice: %s", out)
	}
}

func TestCalculate_TradesAndTrend(t *testing.T) {
	now := time.Date(2026, 10, 27, 15, 0, 0, 0, time.UTC)
	day1 := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	day3 := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)

	trades := []store.Trade{
		// Day 1: 2 trades (1 win: +5, 1 loss: -2) -> daily: +3
		{Symbol: "BTCUSDT", PnL: 5.0, ClosedAt: day1},
		{Symbol: "ETHUSDT", PnL: -2.0, ClosedAt: day1},

		// Day 2: 2 trades (1 win: +8, 1 loss: -3) -> daily: +5, cum: +8
		{Symbol: "SOLUSDT", PnL: 8.0, ClosedAt: day2},
		{Symbol: "ADAUSDT", PnL: -3.0, ClosedAt: day2},

		// Day 3 (today): 3 trades (3 wins: +4, +6, +5) -> daily: +15, cum: +23
		{Symbol: "BNBUSDT", PnL: 4.0, ClosedAt: day3},
		{Symbol: "DOGEUSDT", PnL: 6.0, ClosedAt: day3},
		{Symbol: "XRPUSDT", PnL: 5.0, ClosedAt: day3},
	}

	positions := []store.StoredPosition{
		{
			Symbol:     "DOTUSDT",
			Side:       "BUY",
			EntryPrice: 5.50,
			Quantity:   10.0,
			QuoteSpent: 55.0,
			OpenedAt:   day3,
		},
	}

	m := Calculate(trades, positions, now)

	// Overall verification
	if m.TotalTrades != 7 {
		t.Errorf("expected 7 total trades, got %d", m.TotalTrades)
	}
	if m.Wins != 5 || m.Losses != 2 {
		t.Errorf("expected 5 wins and 2 losses, got %d wins, %d losses", m.Wins, m.Losses)
	}
	expectedWR := (5.0 / 7.0) * 100.0
	if m.WinRate != expectedWR {
		t.Errorf("expected win rate %f, got %f", expectedWR, m.WinRate)
	}
	if m.TotalPnL != 23.0 {
		t.Errorf("expected total pnl 23.0, got %f", m.TotalPnL)
	}

	// Today's stats
	if m.TodayTrades != 3 {
		t.Errorf("expected 3 today trades, got %d", m.TodayTrades)
	}
	if m.TodayWins != 3 || m.TodayLosses != 0 {
		t.Errorf("expected 3 today wins and 0 losses, got %d/%d", m.TodayWins, m.TodayLosses)
	}
	if m.TodayPnL != 15.0 {
		t.Errorf("expected today pnl 15.0, got %f", m.TodayPnL)
	}

	// Recent window (all 7 trades since len < 10)
	if m.RecentTrades != 7 {
		t.Errorf("expected 7 recent trades, got %d", m.RecentTrades)
	}
	if m.RecentWinRate != expectedWR {
		t.Errorf("expected recent win rate %f, got %f", expectedWR, m.RecentWinRate)
	}
	if m.HasPriorWindow {
		t.Errorf("expected no prior window when len < 10")
	}

	// Daily summaries
	if len(m.DailySummaries) != 3 {
		t.Fatalf("expected 3 daily summaries, got %d", len(m.DailySummaries))
	}
	if m.DailySummaries[0].Date != "2026-10-25" || m.DailySummaries[0].DailyPnL != 3.0 || m.DailySummaries[0].CumulativePnL != 3.0 {
		t.Errorf("day 1 summary mismatch: %+v", m.DailySummaries[0])
	}
	if m.DailySummaries[1].Date != "2026-10-26" || m.DailySummaries[1].DailyPnL != 5.0 || m.DailySummaries[1].CumulativePnL != 8.0 {
		t.Errorf("day 2 summary mismatch: %+v", m.DailySummaries[1])
	}
	if m.DailySummaries[2].Date != "2026-10-27" || m.DailySummaries[2].DailyPnL != 15.0 || m.DailySummaries[2].CumulativePnL != 23.0 {
		t.Errorf("day 3 summary mismatch: %+v", m.DailySummaries[2])
	}

	// Check dashboard output
	var buf bytes.Buffer
	RenderDashboard(&buf, &m)
	out := buf.String()
	if !strings.Contains(out, "DOTUSDT") {
		t.Errorf("expected DOTUSDT in active positions: %s", out)
	}
	if !strings.Contains(out, "2026-10-27") {
		t.Errorf("expected 2026-10-27 in daily table: %s", out)
	}
	if !strings.Contains(out, "+$23.00 USDT") {
		t.Errorf("expected +$23.00 USDT total realized: %s", out)
	}
}

func TestCalculate_PriorWindowComparison(t *testing.T) {
	now := time.Date(2026, 10, 27, 18, 0, 0, 0, time.UTC)
	var trades []store.Trade

	// 10 trades in prior window: 5 wins (+10 each), 5 losses (-5 each) -> PnL: +25
	for i := 0; i < 10; i++ {
		pnl := 10.0
		if i%2 == 1 {
			pnl = -5.0
		}
		trades = append(trades, store.Trade{
			Symbol:   "BTCUSDT",
			PnL:      pnl,
			ClosedAt: now.Add(-time.Duration(20-i) * time.Hour),
		})
	}

	// 10 trades in recent window: 8 wins (+10 each), 2 losses (-5 each) -> PnL: +70
	for i := 0; i < 10; i++ {
		pnl := 10.0
		if i < 2 {
			pnl = -5.0
		}
		trades = append(trades, store.Trade{
			Symbol:   "ETHUSDT",
			PnL:      pnl,
			ClosedAt: now.Add(-time.Duration(10-i) * time.Hour),
		})
	}

	m := Calculate(trades, nil, now)

	if !m.HasPriorWindow {
		t.Fatalf("expected HasPriorWindow to be true")
	}
	if m.PriorWindowPnL != 25.0 {
		t.Errorf("expected PriorWindowPnL 25.0, got %f", m.PriorWindowPnL)
	}
	if m.RecentPnL != 70.0 {
		t.Errorf("expected RecentPnL 70.0, got %f", m.RecentPnL)
	}
	if m.RecentWinRate != 80.0 {
		t.Errorf("expected RecentWinRate 80%%, got %f", m.RecentWinRate)
	}
	if m.WinRate != 65.0 { // (5 + 8) / 20 = 13/20 = 65%
		t.Errorf("expected overall WinRate 65%%, got %f", m.WinRate)
	}

	var buf bytes.Buffer
	RenderDashboard(&buf, &m)
	out := buf.String()
	if !strings.Contains(out, "Improving") {
		t.Errorf("expected 'Improving' trend in output: %s", out)
	}
}
