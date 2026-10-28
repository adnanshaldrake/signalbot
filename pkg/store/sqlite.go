// Package store provides a SQLite-backed OHLCV candle store for historical data
// and backtesting. It uses modernc.org/sqlite (pure Go, no CGo required).
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // register the sqlite driver
)

const schema = `
CREATE TABLE IF NOT EXISTS candles (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    symbol      TEXT    NOT NULL,
    interval    TEXT    NOT NULL,
    open_time   INTEGER NOT NULL,  -- Unix milliseconds
    open        REAL    NOT NULL,
    high        REAL    NOT NULL,
    low         REAL    NOT NULL,
    close       REAL    NOT NULL,
    volume      REAL    NOT NULL,
    UNIQUE(symbol, interval, open_time)
);
CREATE INDEX IF NOT EXISTS idx_candles_lookup ON candles(symbol, interval, open_time);

CREATE TABLE IF NOT EXISTS positions (
    symbol          TEXT    PRIMARY KEY,
    side            TEXT    NOT NULL,
    entry_price     REAL    NOT NULL,
    quantity        REAL    NOT NULL,
    quote_spent     REAL    NOT NULL,
    order_id        INTEGER NOT NULL,
    opened_at       INTEGER NOT NULL,  -- Unix milliseconds
    entry_rsi       REAL    DEFAULT 0,
    entry_bb_width  REAL    DEFAULT 0,
    entry_score     REAL    DEFAULT 0,
    entry_vol_ratio REAL    DEFAULT 0
);

CREATE TABLE IF NOT EXISTS trades (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id          INTEGER NOT NULL,
    symbol            TEXT    NOT NULL,
    side              TEXT    NOT NULL,
    entry_price       REAL    NOT NULL,
    exit_price        REAL    NOT NULL,
    quantity          REAL    NOT NULL,
    quote_spent       REAL    NOT NULL,
    pnl               REAL    NOT NULL,
    pnl_pct           REAL    NOT NULL,
    exit_reason       TEXT    NOT NULL,  -- "TAKE_PROFIT", "STOP_LOSS", "EMERGENCY_RUG", "MANUAL"
    entry_rsi         REAL    DEFAULT 0,
    entry_bb_width    REAL    DEFAULT 0,
    entry_score       REAL    DEFAULT 0,
    entry_vol_ratio   REAL    DEFAULT 0,
    hold_duration_sec INTEGER DEFAULT 0,
    opened_at         INTEGER NOT NULL,  -- Unix milliseconds
    closed_at         INTEGER NOT NULL   -- Unix milliseconds
);
CREATE INDEX IF NOT EXISTS idx_trades_symbol ON trades(symbol);
CREATE INDEX IF NOT EXISTS idx_trades_closed_at ON trades(closed_at);
`

// Candle represents a single OHLCV bar.
type Candle struct {
	Symbol   string
	Interval string
	OpenTime time.Time
	Open     float64
	High     float64
	Low      float64
	Close    float64
	Volume   float64
}

// StoredPosition represents an active open position stored in SQLite.
type StoredPosition struct {
	Symbol        string    `json:"symbol"`
	Side          string    `json:"side"`
	EntryPrice    float64   `json:"entry_price"`
	Quantity      float64   `json:"quantity"`
	QuoteSpent    float64   `json:"quote_spent"`
	OrderID       int64     `json:"order_id"`
	OpenedAt      time.Time `json:"opened_at"`
	EntryRSI      float64   `json:"entry_rsi"`
	EntryBBWidth  float64   `json:"entry_bb_width"`
	EntryScore    float64   `json:"entry_score"`
	EntryVolRatio float64   `json:"entry_vol_ratio"`
}

// Trade represents a completed round-trip trade (entry to exit).
type Trade struct {
	ID              int64     `json:"id"`
	OrderID         int64     `json:"order_id"`
	Symbol          string    `json:"symbol"`
	Side            string    `json:"side"`
	EntryPrice      float64   `json:"entry_price"`
	ExitPrice       float64   `json:"exit_price"`
	Quantity        float64   `json:"quantity"`
	QuoteSpent      float64   `json:"quote_spent"`
	PnL             float64   `json:"pnl"`
	PnLPct          float64   `json:"pnl_pct"`
	ExitReason      string    `json:"exit_reason"`
	EntryRSI        float64   `json:"entry_rsi"`
	EntryBBWidth    float64   `json:"entry_bb_width"`
	EntryScore      float64   `json:"entry_score"`
	EntryVolRatio   float64   `json:"entry_vol_ratio"`
	HoldDurationSec int64     `json:"hold_duration_sec"`
	OpenedAt        time.Time `json:"opened_at"`
	ClosedAt        time.Time `json:"closed_at"`
}

// DB wraps a SQLite database connection for candle storage.
type DB struct {
	conn *sql.DB
}

// Open opens (or creates) the SQLite database at the given path.
func Open(dbPath string) (*DB, error) {
	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("store: create data dir: %w", err)
	}

	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite: %w", err)
	}

	// Tune for write throughput (backtesting inserts many rows)
	conn.SetMaxOpenConns(1) // SQLite does not support concurrent writers
	if _, err := conn.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; PRAGMA busy_timeout=5000;`); err != nil {
		return nil, fmt.Errorf("store: sqlite pragma: %w", err)
	}

	if _, err := conn.Exec(schema); err != nil {
		return nil, fmt.Errorf("store: create schema: %w", err)
	}

	migrations := []string{
		`ALTER TABLE positions ADD COLUMN entry_rsi REAL DEFAULT 0;`,
		`ALTER TABLE positions ADD COLUMN entry_bb_width REAL DEFAULT 0;`,
		`ALTER TABLE positions ADD COLUMN entry_score REAL DEFAULT 0;`,
		`ALTER TABLE positions ADD COLUMN entry_vol_ratio REAL DEFAULT 0;`,
		`ALTER TABLE trades ADD COLUMN entry_rsi REAL DEFAULT 0;`,
		`ALTER TABLE trades ADD COLUMN entry_bb_width REAL DEFAULT 0;`,
		`ALTER TABLE trades ADD COLUMN entry_score REAL DEFAULT 0;`,
		`ALTER TABLE trades ADD COLUMN entry_vol_ratio REAL DEFAULT 0;`,
		`ALTER TABLE trades ADD COLUMN hold_duration_sec INTEGER DEFAULT 0;`,
		`UPDATE trades SET hold_duration_sec = (closed_at - opened_at) / 1000 WHERE (hold_duration_sec = 0 OR hold_duration_sec IS NULL) AND closed_at > opened_at;`,
	}
	for _, stmt := range migrations {
		_, _ = conn.Exec(stmt) // ignore duplicate column error on already-migrated DBs
	}

	return &DB{conn: conn}, nil
}

// Close releases the database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}

// UpsertCandles inserts or replaces a batch of candles (UNIQUE constraint on symbol+interval+open_time).
func (db *DB) UpsertCandles(candles []Candle) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	stmt, err := tx.Prepare(`
		INSERT OR REPLACE INTO candles
			(symbol, interval, open_time, open, high, low, close, volume)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("store: prepare stmt: %w", err)
	}
	defer stmt.Close()

	for _, c := range candles {
		if _, err := stmt.Exec(
			c.Symbol, c.Interval, c.OpenTime.UnixMilli(),
			c.Open, c.High, c.Low, c.Close, c.Volume,
		); err != nil {
			return fmt.Errorf("store: insert candle %s: %w", c.Symbol, err)
		}
	}
	return tx.Commit()
}

// GetCandles retrieves historical candles for a symbol and interval, ordered ascending by time.
// Pass a zero time.Time for from/to to get all available candles.
func (db *DB) GetCandles(symbol, interval string, from, to time.Time) ([]Candle, error) {
	query := `
		SELECT symbol, interval, open_time, open, high, low, close, volume
		FROM candles
		WHERE symbol = ? AND interval = ?
	`
	args := []any{symbol, interval}

	if !from.IsZero() {
		query += " AND open_time >= ?"
		args = append(args, from.UnixMilli())
	}
	if !to.IsZero() {
		query += " AND open_time <= ?"
		args = append(args, to.UnixMilli())
	}
	query += " ORDER BY open_time ASC"

	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: query candles: %w", err)
	}
	defer rows.Close()

	var candles []Candle
	for rows.Next() {
		var c Candle
		var openTimeMs int64
		if err := rows.Scan(&c.Symbol, &c.Interval, &openTimeMs,
			&c.Open, &c.High, &c.Low, &c.Close, &c.Volume); err != nil {
			return nil, fmt.Errorf("store: scan candle: %w", err)
		}
		c.OpenTime = time.UnixMilli(openTimeMs)
		candles = append(candles, c)
	}
	return candles, rows.Err()
}

// GetCandleRange returns the earliest and latest open_time for a given symbol and interval.
func (db *DB) GetCandleRange(symbol, interval string) (time.Time, time.Time, error) {
	var minMs, maxMs sql.NullInt64
	query := `SELECT MIN(open_time), MAX(open_time) FROM candles WHERE symbol = ? AND interval = ?`
	if err := db.conn.QueryRow(query, symbol, interval).Scan(&minMs, &maxMs); err != nil {
		return time.Time{}, time.Time{}, err
	}
	if !minMs.Valid || !maxMs.Valid {
		return time.Time{}, time.Time{}, fmt.Errorf("store: no data for %s %s", symbol, interval)
	}
	return time.UnixMilli(minMs.Int64), time.UnixMilli(maxMs.Int64), nil
}

// CountCandles returns how many candles are stored for a given symbol and interval.
func (db *DB) CountCandles(symbol, interval string) (int, error) {
	var count int
	err := db.conn.QueryRow(
		`SELECT COUNT(*) FROM candles WHERE symbol = ? AND interval = ?`,
		symbol, interval,
	).Scan(&count)
	return count, err
}

// ListSymbols returns all distinct symbols stored in the database.
func (db *DB) ListSymbols() ([]string, error) {
	rows, err := db.conn.Query(`SELECT DISTINCT symbol FROM candles ORDER BY symbol`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var symbols []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		symbols = append(symbols, s)
	}
	return symbols, rows.Err()
}

// UpsertPosition inserts or replaces an active open position.
func (db *DB) UpsertPosition(pos StoredPosition) error {
	query := `
		INSERT OR REPLACE INTO positions
			(symbol, side, entry_price, quantity, quote_spent, order_id, opened_at, entry_rsi, entry_bb_width, entry_score, entry_vol_ratio)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := db.conn.Exec(query,
		pos.Symbol, pos.Side, pos.EntryPrice, pos.Quantity, pos.QuoteSpent,
		pos.OrderID, pos.OpenedAt.UnixMilli(),
		pos.EntryRSI, pos.EntryBBWidth, pos.EntryScore, pos.EntryVolRatio,
	)
	if err != nil {
		return fmt.Errorf("store: upsert position %s: %w", pos.Symbol, err)
	}
	return nil
}

// DeletePosition deletes the open position for a given symbol.
func (db *DB) DeletePosition(symbol string) error {
	_, err := db.conn.Exec(`DELETE FROM positions WHERE symbol = ?`, symbol)
	if err != nil {
		return fmt.Errorf("store: delete position %s: %w", symbol, err)
	}
	return nil
}

// GetPosition retrieves an open position by symbol, or nil if none exists.
func (db *DB) GetPosition(symbol string) (*StoredPosition, error) {
	query := `SELECT symbol, side, entry_price, quantity, quote_spent, order_id, opened_at, entry_rsi, entry_bb_width, entry_score, entry_vol_ratio FROM positions WHERE symbol = ?`
	row := db.conn.QueryRow(query, symbol)

	var p StoredPosition
	var openedAtMs int64
	if err := row.Scan(&p.Symbol, &p.Side, &p.EntryPrice, &p.Quantity, &p.QuoteSpent, &p.OrderID, &openedAtMs, &p.EntryRSI, &p.EntryBBWidth, &p.EntryScore, &p.EntryVolRatio); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("store: get position %s: %w", symbol, err)
	}
	p.OpenedAt = time.UnixMilli(openedAtMs)
	return &p, nil
}

// GetAllPositions returns all currently open positions ordered by opened_at ascending.
func (db *DB) GetAllPositions() ([]StoredPosition, error) {
	query := `SELECT symbol, side, entry_price, quantity, quote_spent, order_id, opened_at, entry_rsi, entry_bb_width, entry_score, entry_vol_ratio FROM positions ORDER BY opened_at ASC`
	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, fmt.Errorf("store: get all positions: %w", err)
	}
	defer rows.Close()

	var positions []StoredPosition
	for rows.Next() {
		var p StoredPosition
		var openedAtMs int64
		if err := rows.Scan(&p.Symbol, &p.Side, &p.EntryPrice, &p.Quantity, &p.QuoteSpent, &p.OrderID, &openedAtMs, &p.EntryRSI, &p.EntryBBWidth, &p.EntryScore, &p.EntryVolRatio); err != nil {
			return nil, fmt.Errorf("store: scan position: %w", err)
		}
		p.OpenedAt = time.UnixMilli(openedAtMs)
		positions = append(positions, p)
	}
	return positions, rows.Err()
}

// CountPositions returns the count of open positions.
func (db *DB) CountPositions() (int, error) {
	var count int
	err := db.conn.QueryRow(`SELECT COUNT(*) FROM positions`).Scan(&count)
	return count, err
}

// HasPosition checks whether an open position exists for the given symbol.
func (db *DB) HasPosition(symbol string) (bool, error) {
	var exists int
	err := db.conn.QueryRow(`SELECT 1 FROM positions WHERE symbol = ? LIMIT 1`, symbol).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return exists == 1, err
}

// InsertTrade records a closed trade.
func (db *DB) InsertTrade(t Trade) error {
	query := `
		INSERT INTO trades
			(order_id, symbol, side, entry_price, exit_price, quantity, quote_spent, pnl, pnl_pct, exit_reason, entry_rsi, entry_bb_width, entry_score, entry_vol_ratio, hold_duration_sec, opened_at, closed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	openedAtMs := t.OpenedAt.UnixMilli()
	closedAtMs := t.ClosedAt.UnixMilli()
	if closedAtMs == 0 {
		closedAtMs = time.Now().UnixMilli()
	}
	holdSec := t.HoldDurationSec
	if holdSec == 0 && closedAtMs > openedAtMs {
		holdSec = (closedAtMs - openedAtMs) / 1000
	}
	_, err := db.conn.Exec(query,
		t.OrderID, t.Symbol, t.Side, t.EntryPrice, t.ExitPrice, t.Quantity,
		t.QuoteSpent, t.PnL, t.PnLPct, t.ExitReason,
		t.EntryRSI, t.EntryBBWidth, t.EntryScore, t.EntryVolRatio,
		holdSec, openedAtMs, closedAtMs,
	)
	if err != nil {
		return fmt.Errorf("store: insert trade: %w", err)
	}
	return nil
}

// GetAllTrades returns all completed trades ordered ascending by closed_at.
func (db *DB) GetAllTrades() ([]Trade, error) {
	return db.getTradesWithQuery(`SELECT id, order_id, symbol, side, entry_price, exit_price, quantity, quote_spent, pnl, pnl_pct, exit_reason, entry_rsi, entry_bb_width, entry_score, entry_vol_ratio, hold_duration_sec, opened_at, closed_at FROM trades ORDER BY closed_at ASC`)
}

// GetTrades returns recent completed trades up to limit, ordered descending by closed_at.
func (db *DB) GetTrades(limit int) ([]Trade, error) {
	q := `SELECT id, order_id, symbol, side, entry_price, exit_price, quantity, quote_spent, pnl, pnl_pct, exit_reason, entry_rsi, entry_bb_width, entry_score, entry_vol_ratio, hold_duration_sec, opened_at, closed_at FROM trades ORDER BY closed_at DESC`
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	return db.getTradesWithQuery(q)
}

func (db *DB) getTradesWithQuery(query string) ([]Trade, error) {
	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, fmt.Errorf("store: query trades: %w", err)
	}
	defer rows.Close()

	var trades []Trade
	for rows.Next() {
		var t Trade
		var openedAtMs, closedAtMs int64
		if err := rows.Scan(
			&t.ID, &t.OrderID, &t.Symbol, &t.Side, &t.EntryPrice, &t.ExitPrice,
			&t.Quantity, &t.QuoteSpent, &t.PnL, &t.PnLPct, &t.ExitReason,
			&t.EntryRSI, &t.EntryBBWidth, &t.EntryScore, &t.EntryVolRatio,
			&t.HoldDurationSec, &openedAtMs, &closedAtMs,
		); err != nil {
			return nil, fmt.Errorf("store: scan trade: %w", err)
		}
		t.OpenedAt = time.UnixMilli(openedAtMs)
		t.ClosedAt = time.UnixMilli(closedAtMs)
		if t.HoldDurationSec == 0 && closedAtMs > openedAtMs {
			t.HoldDurationSec = (closedAtMs - openedAtMs) / 1000
		}
		trades = append(trades, t)
	}
	return trades, rows.Err()
}

// GetDailyPnL returns the sum of realised PnL for trades closed since the given time.
func (db *DB) GetDailyPnL(since time.Time) (float64, error) {
	var total sql.NullFloat64
	query := `SELECT SUM(pnl) FROM trades WHERE closed_at >= ?`
	err := db.conn.QueryRow(query, since.UnixMilli()).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("store: daily pnl: %w", err)
	}
	if !total.Valid {
		return 0, nil
	}
	return total.Float64, nil
}

