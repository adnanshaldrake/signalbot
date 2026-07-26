package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// BotConfig holds all runtime configuration loaded from environment variables.
// Secrets (API keys, etc.) are never hardcoded — they must be provided via env vars
// or a .env file sourced before starting the bot.
type BotConfig struct {
	// Binance API credentials
	BinanceAPIKey    string // BINANCE_API_KEY — required for order placement
	BinanceAPISecret string // BINANCE_API_SECRET — required for signed requests

	// Trading mode
	PaperTrading bool   // PAPER_TRADING — if true, simulate trades; default true
	UseTestnet   bool   // USE_TESTNET — if true, point at testnet.binance.vision; default true

	// Strategy parameters
	CandleInterval string        // CANDLE_INTERVAL — e.g. "15m", "1h"; default "15m"
	CandleLimit    int           // CANDLE_LIMIT — number of candles to fetch; default 100
	LoopInterval   time.Duration // derived from CandleInterval

	// Watchlist — comma-separated symbols; if empty, all USDT pairs are used
	Watchlist []string // WATCHLIST e.g. "BTCUSDT,ETHUSDT,SOLUSDT"

	// Risk management
	MaxPositions    int     // MAX_POSITIONS — max simultaneous open trades; default 3
	MaxPositionUSDT float64 // MAX_POSITION_USDT — max USDT per trade; default 50.0
	StopLossPct     float64 // STOP_LOSS_PCT — stop-loss percentage e.g. 2.0 = 2%; default 2.0
	TakeProfitPct   float64 // TAKE_PROFIT_PCT — take-profit percentage; default 4.0
	MaxDailyLossPct float64 // MAX_DAILY_LOSS_PCT — kill-switch threshold; default 5.0

	// Redis
	RedisURL      string // REDIS_URL — default "localhost:6379"
	RedisUsername string // REDIS_USERNAME — default ""
	RedisPassword string // REDIS_PASSWORD — default ""

	// SQLite (backtesting / historical data)
	SQLiteDBPath string // SQLITE_DB_PATH — default "data/signalbot.db"

	// Notifications
	TelegramBotToken string // TELEGRAM_BOT_TOKEN — optional; alerts disabled if empty
	TelegramChatID   string // TELEGRAM_CHAT_ID — your personal or group chat ID
}

// Load reads environment variables and returns a populated BotConfig.
// It returns an error if any required variable is missing when not in paper/testnet mode.
func Load() (*BotConfig, error) {
	cfg := &BotConfig{
		BinanceAPIKey:    os.Getenv("BINANCE_API_KEY"),
		BinanceAPISecret: os.Getenv("BINANCE_API_SECRET"),

		PaperTrading: getBoolEnv("PAPER_TRADING", true),
		UseTestnet:   getBoolEnv("USE_TESTNET", true),

		CandleInterval: getStringEnv("CANDLE_INTERVAL", "15m"),
		CandleLimit:    getIntEnv("CANDLE_LIMIT", 100),

		MaxPositions:    getIntEnv("MAX_POSITIONS", 3),
		MaxPositionUSDT: getFloat64Env("MAX_POSITION_USDT", 50.0),
		StopLossPct:     getFloat64Env("STOP_LOSS_PCT", 2.0),
		TakeProfitPct:   getFloat64Env("TAKE_PROFIT_PCT", 4.0),
		MaxDailyLossPct: getFloat64Env("MAX_DAILY_LOSS_PCT", 5.0),

		RedisURL:      getStringEnv("REDIS_URL", "localhost:6379"),
		RedisUsername: getStringEnv("REDIS_USERNAME", ""),
		RedisPassword: getStringEnv("REDIS_PASSWORD", ""),

		SQLiteDBPath: getStringEnv("SQLITE_DB_PATH", "data/signalbot.db"),

		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:   os.Getenv("TELEGRAM_CHAT_ID"),
	}

	// Parse watchlist
	if wl := os.Getenv("WATCHLIST"); wl != "" {
		cfg.Watchlist = strings.Split(wl, ",")
		for i := range cfg.Watchlist {
			cfg.Watchlist[i] = strings.TrimSpace(cfg.Watchlist[i])
		}
	}

	// Derive loop interval from candle interval string
	d, err := parseCandleInterval(cfg.CandleInterval)
	if err != nil {
		return nil, fmt.Errorf("invalid CANDLE_INTERVAL %q: %w", cfg.CandleInterval, err)
	}
	cfg.LoopInterval = d

	// Validate: real trading requires API credentials
	if !cfg.PaperTrading {
		if cfg.BinanceAPIKey == "" {
			return nil, fmt.Errorf("BINANCE_API_KEY must be set when PAPER_TRADING=false")
		}
		if cfg.BinanceAPISecret == "" {
			return nil, fmt.Errorf("BINANCE_API_SECRET must be set when PAPER_TRADING=false")
		}
	}

	return cfg, nil
}

// parseCandleInterval converts Binance interval strings (e.g. "15m", "1h") to time.Duration.
func parseCandleInterval(interval string) (time.Duration, error) {
	mapping := map[string]time.Duration{
		"1m":  1 * time.Minute,
		"3m":  3 * time.Minute,
		"5m":  5 * time.Minute,
		"15m": 15 * time.Minute,
		"30m": 30 * time.Minute,
		"1h":  1 * time.Hour,
		"2h":  2 * time.Hour,
		"4h":  4 * time.Hour,
		"6h":  6 * time.Hour,
		"8h":  8 * time.Hour,
		"12h": 12 * time.Hour,
		"1d":  24 * time.Hour,
	}
	d, ok := mapping[interval]
	if !ok {
		return 0, fmt.Errorf("unsupported interval %q — use one of: 1m 3m 5m 15m 30m 1h 2h 4h 6h 8h 12h 1d", interval)
	}
	return d, nil
}

// --- helpers ---

func getStringEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getBoolEnv(key string, defaultVal bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return defaultVal
	}
	return b
}

func getIntEnv(key string, defaultVal int) int {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}
	return i
}

func getFloat64Env(key string, defaultVal float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return defaultVal
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return defaultVal
	}
	return f
}
