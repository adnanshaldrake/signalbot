# Contributing to signalbot

Thank you for your interest in contributing to `signalbot`! We welcome contributions from developers, quantitative analysts, and algorithmic traders.

---

## Core Development Principles

Before contributing, please keep our core development philosophy in mind:

1. **Simplicity First**:
   - Favor straightforward, readable Go idioms over complex abstractions or unnecessary layers.
   - Keep pull requests minimal and focused directly on the issue requirements.
2. **Zero Unnecessary Infrastructure**:
   - Avoid introducing external servers, daemons, or cloud dependencies (e.g. Redis, message queues, external databases).
   - Rely on pure Go solutions and local SQLite (`modernc.org/sqlite`) for zero operational overhead.
3. **Performance & Reliability**:
   - Keep indicator calculations, candle walks, and queries fast (<10ms).
   - Ensure bot state and telemetry operations never block trading execution.
4. **Test Verification**:
   - Every feature or bug fix must include focused unit tests.
   - All tests must pass cleanly (`go test ./...`) before opening a pull request.

---

## How to Report Issues

To keep discussions actionable and structured, please select the appropriate template below:

### 1. Bug Reports

For bugs, unexpected errors, or trading execution failures:

```markdown
### Summary
A clear, 1-2 sentence description of the bug.

### Environment
- Go version (`go version`)
- OS / Platform (Linux, macOS, Docker)
- Trading Mode (`PAPER_TRADING`, `USE_DEMONET`, `USE_TESTNET`, or `LIVE`)

### Steps to Reproduce
1. Command run (e.g. `go run ./cmd/main.go --backtest` or Docker Compose)
2. Relevant `.env` settings (candle interval, pair, risk thresholds)

### Expected vs. Actual Behavior
- Expected: What should have happened.
- Actual: What actually occurred.

### Relevant Logs
```text
Paste log snippets or error output here (ALWAYS ensure API keys and secrets are redacted!).
```
```

### 2. Feature Requests & Enhancements

For new indicators, screeners, execution features, or refactors:

```markdown
### Summary
1-2 sentences clearly stating the requested capability or enhancement.

### Motivation / Use Case
Why is this feature needed, and who will benefit from it?

### Proposed Change
2-4 concise bullet points outlining:
- The architectural or code changes.
- Any API, interface (`strategy.Evaluator`, `screener.Screener`), or configuration impact.
- Required unit tests.
```

> **Security Vulnerabilities**: If you discover a security vulnerability (such as API key exposure or order routing safety risks), please do **NOT** open a public issue. Review our [Security Policy](SECURITY.md) and report it privately.

---

## Development Workflow

### 1. Prerequisites
- **Go**: Version 1.27.3 or higher.
- **Git**

### 2. Fork and Clone
```bash
git clone https://github.com/<your-username>/signalbot.git
cd signalbot
```

### 3. Branch Naming
Create a dedicated branch for your work following conventional naming:
- `feat/issue-<number>-<short-description>`
- `fix/issue-<number>-<short-description>`
- `refactor/issue-<number>-<short-description>`
- `docs/issue-<number>-<short-description>`

### 4. Running Tests
Run all unit tests across the workspace:
```bash
go test ./...
```
To test specific components:
```bash
# Test engine
go test -v ./pkg/engine/...

# Test backtester and custom strategies
go test -v ./pkg/backtest/... ./docs/blueprints/...

# Test indicators
go test -v ./pkg/indicators/...
```

### 5. Running the Bot Locally
You can test the bot in safe simulated mode without placing real orders:
```bash
cp .env.example .env
# Set PAPER_TRADING=true in .env
go run ./cmd/main.go
```

To run a backtest over local historical data:
```bash
go run ./cmd/main.go --backtest
```

### 6. Submitting a Pull Request
1. Ensure `go test ./...` passes.
2. Keep commit messages clear and conventional: `feat(...)`, `fix(...)`, `refactor(...)`, `docs(...)`.
3. Reference the relevant issue number in the PR description (e.g. `Fixes #25`).
4. Once reviewed and approved, PRs will be squashed or rebased into `master`.

