# signalbot

A modular Go algorithmic trading framework and execution engine built on the Binance REST APIs. Designed with zero unnecessary infrastructure—relying on pure Go and local SQLite (`modernc.org/sqlite`) for ultra-low operational overhead.

---

## Key Features

- **Modular Binance API Wrapper**: Spot order routing, exchange filter parsing, wallet balance tracking, and accurate fill accounting (VWAP + fee deduction).
- **Pluggable Strategies (`strategy.Evaluator`)**: Implement custom entry/exit logic and evaluate across historical or real-time data in O(1) per candle.
- **Dynamic Asset Screener (`screener.Screener`)**: Auto-discover candidates by 24h quote volume, run automated backtests, and trade only profitable pairs.
- **Fast Backtesting Engine (`RunnerV2`)**: Zero-allocation native float64 backtest walk simulating maker/taker fees, stop losses, take profits, and max hold durations in milliseconds.
- **Liquidity & Sanity Filters**: Pre-filters for minimum 24h volume, wash-trading trade counts, extreme pump volatility, and candle body rug-pull detection.
- **Flexible Trading Modes**: Local paper simulation, Binance Demonet, Binance Testnet, or real live production trading.
- **Telemetry & Notifications**: Telegram alerts for entries, exits, daily kill-switch activations, and SQLite trade history inspection.

---

## Setup

```bash
cp .env.example .env
# Edit .env with your configuration
```

---

## Trading Modes

Configure how `signalbot` executes orders via `.env`:

| Mode | Configuration | Description |
|:---|:---|:---|
| **Paper Trading** | `PAPER_TRADING=true` | Simulated orders tracked in SQLite. Safe default, no API keys needed. |
| **Binance Demonet** | `PAPER_TRADING=false`<br>`USE_DEMONET=true`<br>`USE_TESTNET=false` | Orders routed to Binance Demo environment (`https://demo-api.binance.com`). |
| **Binance Testnet** | `PAPER_TRADING=false`<br>`USE_TESTNET=true`<br>`USE_DEMONET=false` | Orders routed to Binance Spot Testnet (`https://testnet.binance.vision`). |
| **Live Production** | `PAPER_TRADING=false`<br>`USE_TESTNET=false`<br>`USE_DEMONET=false` | **Live trading with real money** on production Binance (`https://api.binance.com`). |

---

## How to Run

### 1. Build Standalone Binary (Recommended for Production)

```bash
# Build the binary
go build -o signalbot ./cmd/main.go

# Run 24/7 Trading Bot (Paper, Demonet, Testnet, or Live depending on .env)
./signalbot

# One-off operational commands:
./signalbot --backfill                 # Download & store historical candles in SQLite
./signalbot --backtest                 # Replay historical candles with Backtest Runner
./signalbot --metrics                  # View trade performance metrics dashboard
./signalbot --export-trades -format=csv # Export completed trades (CSV or JSON)
```

### 2. Direct Execution (Development & Quick Testing)

```bash
# Run 24/7 Trading Bot directly
go run ./cmd/main.go

# Run backtest directly
go run ./cmd/main.go --backtest

# Run standalone custom strategy runner example
go run ./examples/runner/main.go
```

---

## Pluggable Custom Strategies

`signalbot` decouples strategy logic from execution. Implement `strategy.Evaluator` to plug your own alpha models directly into the core engine:

```go
type MyCustomStrategy struct { ... }

func (s *MyCustomStrategy) ShouldEnter(index int) bool {
    // Return true to trigger entry signal
    return true
}

func (s *MyCustomStrategy) ShouldExit(index int) bool {
    // Return true to trigger exit signal
    return false
}

// 1. Inject into Backtest Runner:
runner := backtest.NewRunnerV2(db, 100.0).
    WithStrategy(func(series *strategy.Series) (strategy.Evaluator, error) {
        return NewMyCustomStrategy(series), nil
    })

// 2. Inject into 24/7 Live / Paper Trading Engine:
eng := engine.New(cfg, db).
    WithStrategy(func(series *strategy.Series) (strategy.Evaluator, error) {
        return NewMyCustomStrategy(series), nil
    })
eng.Run(ctx)
```

- See [`docs/blueprints/custom_strategy.go`](docs/blueprints/custom_strategy.go) for a runnable momentum golden-cross blueprint.
- See [`examples/runner/main.go`](examples/runner/main.go) for a standalone custom bot runner.

---

## Docker

Run `signalbot` locally with Docker Compose, or deploy the container to your preferred hosting provider (e.g. Railway, Fly.io, Hetzner, or any cloud VPS).

### Run Locally with Docker Compose

```bash
# 1. Start the bot in the background
docker compose up -d

# 2. Follow live execution logs
docker compose logs -f

# 3. Stop the bot gracefully
docker compose down
```

### Running Commands in Docker

You can run one-off tasks (like backtesting or checking metrics) inside the container:

```bash
# Run a backtest inside the container
docker compose run --rm signalbot --backtest

# View trade performance metrics
docker compose run --rm signalbot --metrics

# Backfill historical candles
docker compose run --rm signalbot --backfill
```

### Persistence & Configuration

- **Data Persistence**: The local `./data` directory is mapped to `/app/data` inside the container, preserving SQLite trade history and active positions across restarts.
- **Environment Variables**: Reads your local `.env` file automatically.
- **Cloud / VPS Hosting**: When deploying to a hosted platform, configure your `.env` keys as platform environment variables and mount a persistent volume at `/app/data`.

---

## Contributing

We welcome community contributions! Please read our [Contributing Guidelines](CONTRIBUTING.md) to get started, understand our code philosophy, and learn how to report issues or submit pull requests.

---

## Disclaimer

This software is for educational and research purposes only. Cryptocurrency trading involves substantial risk of financial loss and is not suitable for every investor. Do not trade with capital you cannot afford to lose. The authors and contributors accept no responsibility or liability for any financial losses or damages resulting from the use of this software.

---

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
