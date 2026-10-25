// count.go — quick inspection tool: prints how many candles are stored in SQLite per symbol.
// Run with: go run ./cmd/tools/count.go
package main

import (
	"fmt"
	"log"
	"time"

	"adnan/binance-bot/pkg/store"
)

func main() {
	db, err := store.Open("data/signalbot.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	symbols, _ := db.ListSymbols()
	for _, sym := range symbols {
		candles, _ := db.GetCandles(sym, "15m", time.Time{}, time.Time{})
		fmt.Printf("Symbol: %s, Candles: %d\n", sym, len(candles))
	}
}
