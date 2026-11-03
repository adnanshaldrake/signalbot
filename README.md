# signalbot

Binance altcoin trading bot. BB + RSI mean-reversion strategy with paper trading, backtesting, and Telegram alerts.

## Setup

```bash
cp .env.example .env
# fill in your values
```

## Run

```bash
# Paper trading (safe default)
go run ./cmd/main.go

# Backfill historical candles first
go run ./cmd/main.go --backfill

# Backtest
go run ./cmd/main.go --backtest
```

## Docker

```bash
docker compose up -d
```

## Deploy (Railway / Fly.io / any VPS)

1. Set your `.env` vars as environment variables on the platform
2. Push — the Dockerfile handles the rest (state persisted in SQLite at `data/signalbot.db`)

## Trading Modes

| `.env` setting | Effect |
|---|---|
| `PAPER_TRADING=true` | Simulated trades, no real orders |
| `USE_TESTNET=true` | Real API calls against Binance testnet (fake money) |
| Both false | **Live trading with real money** |

## Custom Strategies & Pluggability

Implement `strategy.Evaluator` to plug your own alpha logic into the fast float64 backtesting engine:

```go
type MyStrategy struct { ... }

func (s *MyStrategy) ShouldEnter(index int) bool { /* your entry rule */ return true }
func (s *MyStrategy) ShouldExit(index int) bool  { /* your exit rule */ return false }

// Inject into RunnerV2:
runner := backtest.NewRunnerV2(db, 100.0).
    WithStrategy(func(series *strategy.Series) (strategy.Evaluator, error) {
        return NewMyStrategy(series), nil
    })
```

See [`docs/blueprints/custom_strategy.go`](docs/blueprints/custom_strategy.go) for a full runnable blueprint.
