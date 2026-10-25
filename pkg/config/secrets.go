package config

import (
	"bufio"
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
	PaperTrading bool // PAPER_TRADING — if true, simulate trades; default true
	UseTestnet   bool // USE_TESTNET — if true, point at testnet.binance.vision; default true
	UseDemonet   bool // USE_DEMONET - if true, point at demo.binance.vision; default false

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

	// Signal scoring & dynamic position sizing
	SignalUpsideWeight    float64 // SIGNAL_UPSIDE_WEIGHT — BB upside weight; default 0.50
	SignalRSIWeight       float64 // SIGNAL_RSI_WEIGHT — RSI depth weight; default 0.30
	SignalVolumeWeight    float64 // SIGNAL_VOLUME_WEIGHT — volume weight; default 0.20
	SignalExtraBudgetUSDT float64 // SIGNAL_EXTRA_BUDGET_USDT — extra USDT distributed by score; default 40.0

	// Pre-filter: symbol eligibility
	MinVolume24hUSDT   float64 // MIN_VOLUME_24H_USDT — minimum 24h USDT volume; default 500000
	MinTradeCount24h   int     // MIN_TRADE_COUNT_24H — minimum 24h trade count; default 5000
	MaxPriceChange24h  float64 // MAX_PRICE_CHANGE_24H_PCT — block entries if |%change| exceeds this; default 40.0
	RugCandleBodyPct   float64 // RUG_CANDLE_BODY_PCT — emergency exit if candle body % drop exceeds this; default 15.0
	MinCandlesRequired int     // MIN_CANDLES_REQUIRED — warm-up minimum; default 50

	// Dynamic watchlist auto-discovery
	AutoDiscoverSymbols bool // AUTO_DISCOVER_SYMBOLS — if true, ignore WATCHLIST and scan all USDT pairs; default false
	MaxWatchlistSize    int  // MAX_WATCHLIST_SIZE — cap on auto-discovered symbols; default 50

	BackfillDays int // BACKFILL_DAYS — if >0, fetches multiple batches of candles to go back this many days. default 0 (single batch).

	// Startup symbol screener
	ScreenOnStartup  bool    // SCREEN_ON_STARTUP — if true, auto-screen symbols on startup; default false
	ScreenTopN       int     // SCREEN_TOP_N — top-N USDT symbols by volume to screen; default 40
	ScreenMinWinRate float64 // SCREEN_MIN_WIN_RATE — minimum win rate % to keep a symbol; default 35.0
	ScreenMinPnL     float64 // SCREEN_MIN_PNL — minimum total PnL USDT to keep a symbol; default 0.0
	ScreenMinTrades  int     // SCREEN_MIN_TRADES — minimum backtest trades to keep a symbol; default 3
}

func loadDotEnv() {
	file, err := os.Open(".env")
	if err != nil {
		file, err = os.Open(".env")
		if err != nil {
			return
		}
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			if os.Getenv(key) == "" { // only set if not already present in environment
				os.Setenv(key, val)
			}
		}
	}
}

// Load reads environment variables and returns a populated BotConfig.
// It returns an error if any required variable is missing when not in paper/testnet mode.
func Load() (*BotConfig, error) {
	// Automatically load .env if it exists in the current directory
	loadDotEnv()

	cfg := &BotConfig{
		BinanceAPIKey:    os.Getenv("BINANCE_API_KEY"),
		BinanceAPISecret: os.Getenv("BINANCE_API_SECRET"),

		PaperTrading: getBoolEnv("PAPER_TRADING", true),
		UseTestnet:   getBoolEnv("USE_TESTNET", true),
		UseDemonet:   getBoolEnv("USE_DEMONET", true),

		CandleInterval: getStringEnv("CANDLE_INTERVAL", "15m"),
		CandleLimit:    getIntEnv("CANDLE_LIMIT", 100),

		MaxPositions:    getIntEnv("MAX_POSITIONS", 3),
		MaxPositionUSDT: getFloat64Env("MAX_POSITION_USDT", 50.0),
		StopLossPct:     getFloat64Env("STOP_LOSS_PCT", 2.0),
		TakeProfitPct:   getFloat64Env("TAKE_PROFIT_PCT", 4.0),
		MaxDailyLossPct: getFloat64Env("MAX_DAILY_LOSS_PCT", 5.0),

		RedisURL:      getStringEnv("REDIS_URL", ""),
		RedisUsername: getStringEnv("REDIS_USERNAME", ""),
		RedisPassword: getStringEnv("REDIS_PASSWORD", ""),

		SQLiteDBPath: getStringEnv("SQLITE_DB_PATH", "data/signalbot.db"),

		TelegramBotToken: os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:   os.Getenv("TELEGRAM_CHAT_ID"),

		SignalUpsideWeight:    getFloat64Env("SIGNAL_UPSIDE_WEIGHT", 0.50),
		SignalRSIWeight:       getFloat64Env("SIGNAL_RSI_WEIGHT", 0.30),
		SignalVolumeWeight:    getFloat64Env("SIGNAL_VOLUME_WEIGHT", 0.20),
		SignalExtraBudgetUSDT: getFloat64Env("SIGNAL_EXTRA_BUDGET_USDT", 40.0),

		MinVolume24hUSDT:   getFloat64Env("MIN_VOLUME_24H_USDT", 500_000),
		MinTradeCount24h:   getIntEnv("MIN_TRADE_COUNT_24H", 5_000),
		MaxPriceChange24h:  getFloat64Env("MAX_PRICE_CHANGE_24H_PCT", 40.0),
		RugCandleBodyPct:   getFloat64Env("RUG_CANDLE_BODY_PCT", 15.0),
		MinCandlesRequired: getIntEnv("MIN_CANDLES_REQUIRED", 50),

		AutoDiscoverSymbols: getBoolEnv("AUTO_DISCOVER_SYMBOLS", false),
		MaxWatchlistSize:    getIntEnv("MAX_WATCHLIST_SIZE", 50),
		BackfillDays:        getIntEnv("BACKFILL_DAYS", 0),

		ScreenOnStartup:  getBoolEnv("SCREEN_ON_STARTUP", false),
		ScreenTopN:       getIntEnv("SCREEN_TOP_N", 40),
		ScreenMinWinRate: getFloat64Env("SCREEN_MIN_WIN_RATE", 35.0),
		ScreenMinPnL:     getFloat64Env("SCREEN_MIN_PNL", 0.0),
		ScreenMinTrades:  getIntEnv("SCREEN_MIN_TRADES", 3),
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
