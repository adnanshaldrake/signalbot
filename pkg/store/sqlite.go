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
	if _, err := conn.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL;`); err != nil {
		return nil, fmt.Errorf("store: sqlite pragma: %w", err)
	}

	if _, err := conn.Exec(schema); err != nil {
		return nil, fmt.Errorf("store: create schema: %w", err)
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
