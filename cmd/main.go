package main

import (
	"adnan/binance-bot/pkg/app"
	"adnan/binance-bot/pkg/models"
	"encoding/json" // New import for JSON marshaling
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/sdcoffey/techan"
)

func init() {
	// Keep log flags set to zero for clean header/footer message
	log.SetFlags(0)
}

const dataLimit = 50

func loadData() []string {
	// --- NEW STEP: Clean DB ---
	log.Println("Clearing previous ticker data from Redis...")
	if err := app.ClearRedisDatabase(); err != nil {
		// Log the error but don't fail the program unless the cleanup is absolutely critical
		log.Printf("Warning: Failed to clear Redis database: %v. Proceeding with potentially stale data.", err)
	}

	// --- 1. Data Loading ---
	symbolList, err := app.GetSymbolList()
	if err != nil {
		log.Fatalf("Failed to get symbol list: %v\n", err)
	}

	symbolsToTest := symbolList
	// Adjust this count as needed, currently scanning all symbols from the list
	// if len(symbolList) > 5 {
	// 	symbolsToTest = symbolList[:5]
	// }

	if err := app.LoadTickerData(symbolsToTest, dataLimit); err != nil {
		log.Fatalf("Failed to load ticker data: %v", err)
	}
	return symbolsToTest
}

func computeBuySignals(symbolsToTest []string, strategyType models.Strategy) ([]string, []models.SignalResult) {
	now := time.Now().UTC()
	currentHourlyStart := now.Truncate(time.Hour)

	log.Printf("Current candle start time (UTC) being checked: %s", currentHourlyStart.Format(time.RFC3339))

	log.Println("--- Starting BUY Signal Scanner ---")

	var s app.StrategyFunc

	// --- 2. Strategy Execution & Result Collection ---
	var buySignals []models.SignalResult // Store results as a slice of structs
	var warnings []string                // Collect warnings for printing to Stderr

	switch strategyType {
	case models.LongTermTrendFollowing:
		s = app.LongTermTrendFollowing
	case models.ShortTermMeanReversion:
		s = app.ShortTermMeanReversion
	default:
		return []string{"Unknown"}, nil
	}

	for _, symbol := range symbolsToTest {
		series, err := app.GetTimeSeries(symbol)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Failed to get time series for %s: %v.", symbol, err))
			continue
		}

		strategyName, strategy, err := s(series)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Failed to init strategy for %s: %v.", symbol, err))
			continue
		}

		stableStart := strategy.UnstablePeriod
		latestIndex := len(series.Candles) - 1

		if latestIndex < 0 {
			warnings = append(warnings, fmt.Sprintf("%s: No candles found. Skipping.", symbol))
			continue
		}

		latestCandleTime := series.Candles[latestIndex].Period.Start.In(time.UTC)

		// NEW FILTERING CHECK
		if !latestCandleTime.Equal(currentHourlyStart) {
			// Log to Stderr (warnings) so it doesn't clutter the final JSON output
			warnings = append(warnings, fmt.Sprintf("%s: Latest data is stale. Time: %s. Skipping.",
				symbol, latestCandleTime.Format(time.RFC3339)))
			continue // Skip to the next symbol
		}

		if latestIndex < stableStart {
			warnings = append(warnings, fmt.Sprintf("%s: Data is unstable (Need %d).", symbol, stableStart))
			continue
		}

		record := techan.NewTradingRecord()

		if strategy.EntryRule.IsSatisfied(latestIndex, record) {
			price := series.Candles[latestIndex].ClosePrice
			candleTime := series.Candles[latestIndex].Period.Start.Format(time.RFC822)
			const priceDecimals = 8 // Increased precision for crypto prices

			// Append the structured result
			buySignals = append(buySignals, models.SignalResult{
				Symbol:   symbol,
				Price:    price.FormattedString(priceDecimals),
				Time:     candleTime,
				Strategy: strategyName,
			})
		}
	}
	return warnings, buySignals
}

func printOutputs(warnings []string, buySignals []models.SignalResult) {
	// --- 3. Final Output ---
	// Print Warnings to Standard Error (os.Stderr)
	if len(warnings) > 0 {
		fmt.Fprintln(os.Stderr, "\n--- Strategy Warnings ---")
		for _, w := range warnings {
			fmt.Fprintln(os.Stderr, w)
		}
		fmt.Fprintln(os.Stderr, "-------------------------")
	}

	// Marshal the results slice into JSON
	jsonData, err := json.MarshalIndent(buySignals, "", "    ")
	if err != nil {
		log.Fatalf("Failed to marshal JSON results: %v", err)
	}

	// Print status and the final JSON to Standard Output (os.Stdout)
	log.Printf("Found %d potential BUY signals.", len(buySignals))
	fmt.Println("--- JSON RESULTS START ---")

	// Print the JSON object (clean, single output)
	fmt.Println(string(jsonData))

	log.Println("--- Scanner Finished ---")
}

func getSymbols(c *gin.Context) {
	symbolsToTest := loadData()
	c.JSON(200, symbolsToTest)
}

func getBuySignals(c *gin.Context) {
	symbolsToTest := loadData()
	var strategyType models.Strategy

	strategyParam := c.Param("strategy")

	switch strategyParam {
	case "long":
		strategyType = models.LongTermTrendFollowing
	case "short":
		strategyType = models.ShortTermMeanReversion
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid strategy type"})
		return
	}

	warnings, buySignals := computeBuySignals(symbolsToTest, strategyType)
	if warnings != nil {

	}
	c.JSON(http.StatusOK, gin.H{
		"signals":  buySignals,
		"warnings": warnings,
	})
}

func main() {
	router := gin.Default()
	router.GET("/symbols", getSymbols)
	router.GET("/signals/:strategy", getBuySignals)

	router.Run("localhost:8080")
}
