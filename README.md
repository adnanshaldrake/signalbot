Step 1: Testnet (Fake money, real API)

Go to Binance Spot Testnet, log in with Github, and generate an API Key.
In your .env:
BINANCE_API_KEY=your_testnet_key
BINANCE_API_SECRET=your_testnet_secret
PAPER_TRADING=false
USE_TESTNET=true
Run the bot. It will place real HTTP requests, but against Binance's fake-money test environment.
Step 2: Mainnet (Real money, real API)

Generate an API key on your real Binance account (ensure it has "Spot Trading" enabled, but disable Withdrawals for safety).
In your .env:
BINANCE_API_KEY=your_real_key
BINANCE_API_SECRET=your_real_secret
PAPER_TRADING=false
USE_TESTNET=false
Run the bot. You are now live.
Give source .env && go run ./cmd/main.go a try right now to see the logs flow!