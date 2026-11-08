// Package binance provides an authenticated HTTP client for the Binance REST API.
// It supports both the production API (api.binance.com) and the spot testnet
// (testnet.binance.vision). Private endpoints are signed with HMAC-SHA256.
package binance

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/adnanshaldrake/signalbot/pkg/config"
)

const (
	productionBaseURL = "https://api.binance.com"
	testnetBaseURL    = "https://testnet.binance.vision"
	demonetBaseURL    = "https://demo-api.binance.com"
)

type SymbolFilter struct {
	Status                     string
	IsSpotTradingAllowed       bool
	QuoteOrderQtyMarketAllowed bool
	MinNotional                float64
	StepSize                   float64
	MinQty                     float64
	Decimals                   int
}

// OrderSide represents BUY or SELL.
type OrderSide string

const (
	Buy  OrderSide = "BUY"
	Sell OrderSide = "SELL"
)

// OrderType represents the Binance order type.
type OrderType string

const (
	Market OrderType = "MARKET"
	Limit  OrderType = "LIMIT"
)

// OrderResponse is the parsed response from a successful order placement.
type OrderResponse struct {
	Symbol              string `json:"symbol"`
	OrderID             int64  `json:"orderId"`
	ClientOrderID       string `json:"clientOrderId"`
	TransactTime        int64  `json:"transactTime"`
	Price               string `json:"price"`
	OrigQty             string `json:"origQty"`
	ExecutedQty         string `json:"executedQty"`
	CummulativeQuoteQty string `json:"cummulativeQuoteQty"`
	Status              string `json:"status"`
	Type                string `json:"type"`
	Side                string `json:"side"`
	Fills               []Fill `json:"fills"`
}

type Fill struct {
	Price           string `json:"price"`
	Quantity        string `json:"qty"`
	Commission      string `json:"commission"`
	CommissionAsset string `json:"commissionAsset"`
	TradeId         int    `json:"tradeId"`
}

// FillSummary returns the real volume-weighted average fill price and the total
// quote amount (USDT) spent, taken from the order response rather than the
// pre-order ticker price. ok is false if the response has no usable fill data.
func (r *OrderResponse) FillSummary() (avgPrice, quoteSpent float64, ok bool) {
	quoteSpent, _ = strconv.ParseFloat(r.CummulativeQuoteQty, 64)
	executedQty, _ := strconv.ParseFloat(r.ExecutedQty, 64)

	// Primary: VWAP from fills
	var cost, qty float64
	for _, f := range r.Fills {
		p, _ := strconv.ParseFloat(f.Price, 64)
		q, _ := strconv.ParseFloat(f.Quantity, 64)
		cost += p * q
		qty += q
	}
	if qty > 0 {
		avgPrice = cost / qty
		if quoteSpent <= 0 {
			quoteSpent = cost
		}
		return avgPrice, quoteSpent, true
	}

	// Fallback: cumulative quote / executed qty
	if executedQty > 0 && quoteSpent > 0 {
		return quoteSpent / executedQty, quoteSpent, true
	}
	return 0, 0, false
}

// Balance holds the free and locked amounts of a single asset.
type Balance struct {
	Asset  string  `json:"asset"`
	Free   float64 `json:"free,string"`
	Locked float64 `json:"locked,string"`
}

// AccountInfo is a partial parse of /api/v3/account.
type AccountInfo struct {
	MakerCommission int       `json:"makerCommission"`
	TakerCommission int       `json:"takerCommission"`
	CanTrade        bool      `json:"canTrade"`
	CanWithdraw     bool      `json:"canWithdraw"`
	CanDeposit      bool      `json:"canDeposit"`
	Balances        []Balance `json:"balances"`
	AccountType     string    `json:"accountType"`
	UpdateTime      int64     `json:"updateTime"`
}

// Client is an authenticated Binance REST API client.
type Client struct {
	apiKey        string
	apiSecret     string
	baseURL       string
	httpClient    *http.Client
	timeOffset    int64
	exchangeRules map[string]SymbolFilter
}

type ExchangeInfoResponse struct {
	Symbols []struct {
		Symbol                     string                   `json:"symbol"`
		Status                     string                   `json:"status"`
		IsSpotTradingAllowed       bool                     `json:"isSpotTradingAllowed"`
		QuoteOrderQtyMarketAllowed bool                     `json:"quoteOrderQtyMarketAllowed"`
		Filters                    []map[string]interface{} `json:"filters"`
	} `json:"symbols"`
}

// NewClient creates a new Binance client using the provided BotConfig.
func NewClient(cfg *config.BotConfig) *Client {
	base := testnetBaseURL
	if cfg.UseDemonet && !cfg.UseTestnet {
		base = demonetBaseURL
	}
	if !cfg.UseTestnet && !cfg.UseDemonet {
		base = productionBaseURL
	}
	return &Client{
		apiKey:    cfg.BinanceAPIKey,
		apiSecret: cfg.BinanceAPISecret,
		baseURL:   base,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

type ServerTimeResponse struct {
	ServerTime int64 `json:"serverTime"`
}

func (c *Client) SyncTime() error {
	url := c.baseURL + "/api/v3/time"

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("SyncTime failed: %w", err)
	}
	defer resp.Body.Close()

	var data ServerTimeResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return fmt.Errorf("SyncTime unmarshal: %w", err)
	}

	// Calculate and store the WSL clock drift offset
	c.timeOffset = data.ServerTime - time.Now().UnixMilli()
	return nil
}

func (c *Client) LoadExchangeInfo() error {
	resp, err := http.Get(c.baseURL + "/api/v3/exchangeInfo")
	if err != nil {
		return fmt.Errorf("LoadExchangeInfo failed: %w", err)
	}
	defer resp.Body.Close()

	var data ExchangeInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return fmt.Errorf("LoadExchangeInfo unmarshal: %w", err)
	}

	c.exchangeRules = make(map[string]SymbolFilter, len(data.Symbols))

	for _, s := range data.Symbols {
		var stepSize float64
		var minQty float64
		var minNotional float64
		var decimals int

		for _, filter := range s.Filters {
			filterType, _ := filter["filterType"].(string)
			switch filterType {
			case "LOT_SIZE":
				stepSizeStr, _ := filter["stepSize"].(string)
				minQtyStr, _ := filter["minQty"].(string)
				stepSize, _ = strconv.ParseFloat(stepSizeStr, 64)
				minQty, _ = strconv.ParseFloat(minQtyStr, 64)
				decimals = getDecimals(stepSizeStr)
			case "NOTIONAL":
				minNotionalStr, _ := filter["minNotional"].(string)
				minNotional, _ = strconv.ParseFloat(minNotionalStr, 64)
			case "MIN_NOTIONAL":
				if minNotional == 0 {
					minNotionalStr, _ := filter["minNotional"].(string)
					minNotional, _ = strconv.ParseFloat(minNotionalStr, 64)
				}
			}
		}

		c.exchangeRules[s.Symbol] = SymbolFilter{
			Status:                     s.Status,
			IsSpotTradingAllowed:       s.IsSpotTradingAllowed,
			QuoteOrderQtyMarketAllowed: s.QuoteOrderQtyMarketAllowed,
			MinNotional:                minNotional,
			StepSize:                   stepSize,
			MinQty:                     minQty,
			Decimals:                   decimals,
		}
	}
	return nil
}

// IsMarketOpen returns true if the symbol exists in exchange info,
// its status is "TRADING", and spot trading is allowed.
// Returns true by default if exchange rules haven't been loaded.
func (c *Client) IsMarketOpen(symbol string) bool {
	if c == nil || len(c.exchangeRules) == 0 {
		return true
	}
	rule, exists := c.exchangeRules[symbol]
	if !exists {
		return false
	}
	return rule.Status == "TRADING" && rule.IsSpotTradingAllowed
}

// GetSymbolFilter returns the SymbolFilter for the given symbol if present.
func (c *Client) GetSymbolFilter(symbol string) (SymbolFilter, bool) {
	if c == nil || len(c.exchangeRules) == 0 {
		return SymbolFilter{}, false
	}
	rule, exists := c.exchangeRules[symbol]
	return rule, exists
}

// Helper to count decimal places from a scientific notation or float string
func getDecimals(str string) int {
	if !strings.Contains(str, ".") {
		return 0
	}
	parts := strings.Split(str, ".")
	if len(parts) < 2 {
		return 0
	}
	// Trim trailing zeros
	trimmed := strings.TrimRight(parts[1], "0")
	return len(trimmed)
}

func (c *Client) FormatQuantity(symbol string, qty float64) (string, error) {
	rule, exists := c.exchangeRules[symbol]
	if !exists {
		// Fallback if symbol not found
		return strconv.FormatFloat(qty, 'f', 4, 64), nil
	}

	if qty < rule.MinQty {
		return "", fmt.Errorf("quantity %f is below minimum allowed %f for %s", qty, rule.MinQty, symbol)
	}

	// Snap quantity to the nearest valid stepSize multiple (floored to avoid over-selling)
	precisionMultiplier := math.Round(1 / rule.StepSize)
	flooredQty := math.Floor(qty*precisionMultiplier) / precisionMultiplier

	// Format with the exact number of decimals required by Binance
	formatString := fmt.Sprintf("%%.%df", rule.Decimals)
	return fmt.Sprintf(formatString, flooredQty), nil
}

// GetAccountBalance returns USDT balance (and all balances) from /api/v3/account.
func (c *Client) GetAccountBalance() (*AccountInfo, error) {
	params := url.Values{}
	params.Set("timestamp", c.currentTimestamp())
	c.sign(params)

	body, err := c.doSignedRequest(http.MethodGet, "/api/v3/account", params, nil)
	if err != nil {
		return nil, fmt.Errorf("GetAccountBalance: %w", err)
	}

	var info AccountInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("GetAccountBalance: unmarshal: %w", err)
	}
	return &info, nil
}

// GetUSDTBalance is a convenience wrapper that returns just the free USDT balance.
func (c *Client) GetUSDTBalance() (float64, error) {
	info, err := c.GetAccountBalance()
	if err != nil {
		return 0, err
	}
	for _, b := range info.Balances {
		if b.Asset == "USDT" {
			return b.Free, nil
		}
	}
	return 0, nil
}

// PlaceMarketOrder places a MARKET order for the given symbol and side.
// quoteQty is the USDT amount to spend (for BUY) or the base asset quantity (for SELL).
func (c *Client) PlaceMarketOrder(symbol string, side OrderSide, amount float64) (*OrderResponse, error) {
	if len(c.exchangeRules) > 0 {
		rule, exists := c.exchangeRules[symbol]
		if !exists {
			return nil, fmt.Errorf("PlaceMarketOrder: symbol %s not found in exchange info", symbol)
		}
		if rule.Status != "TRADING" {
			return nil, fmt.Errorf("PlaceMarketOrder: market is closed for %s (status: %s)", symbol, rule.Status)
		}
		if !rule.IsSpotTradingAllowed {
			return nil, fmt.Errorf("PlaceMarketOrder: spot trading is not allowed for %s", symbol)
		}
		if side == Buy && !rule.QuoteOrderQtyMarketAllowed {
			return nil, fmt.Errorf("PlaceMarketOrder: quoteOrderQty market buy is not allowed for %s", symbol)
		}
		if side == Buy && rule.MinNotional > 0 && amount < rule.MinNotional {
			return nil, fmt.Errorf("PlaceMarketOrder: buy amount %.2f USDT is below min notional %.2f for %s", amount, rule.MinNotional, symbol)
		}
	}

	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("side", string(side))
	params.Set("type", string(Market))

	if side == Buy {
		// For BUY: 'amount' is your USDT (quote asset) budget to spend
		params.Set("quoteOrderQty", strconv.FormatFloat(amount, 'f', 2, 64))
	} else {
		// For SELL: 'amount' is the exact number of token units (base asset) to sell
		formattedQty, err := c.FormatQuantity(symbol, amount)
		if err != nil {
			return nil, fmt.Errorf("PlaceMarketOrder format quantity: %w", err)
		}
		params.Set("quantity", formattedQty)
	}

	params.Set("timestamp", c.currentTimestamp())
	params.Set("newOrderRespType", "FULL")
	params.Set("recvWindow", "5000")
	c.sign(params)
	body, err := c.doSignedRequest(http.MethodPost, "/api/v3/order", params, nil)
	if err != nil {
		if strings.Contains(err.Error(), "Market is closed") || strings.Contains(err.Error(), "-1013") {
			if rule, ok := c.exchangeRules[symbol]; ok {
				rule.Status = "BREAK"
				c.exchangeRules[symbol] = rule
			}
		}
		return nil, fmt.Errorf("PlaceMarketOrder %s %s: %w", side, symbol, err)
	}
	var resp OrderResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("PlaceMarketOrder: unmarshal: %w", err)
	}
	return &resp, nil
}

// CancelOrder cancels an open order by orderId.
func (c *Client) CancelOrder(symbol string, orderID int64) error {
	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("orderId", strconv.FormatInt(orderID, 10))
	params.Set("timestamp", c.currentTimestamp())
	c.sign(params)

	_, err := c.doSignedRequest(http.MethodDelete, "/api/v3/order", params, nil)
	return err
}

// Ping checks connectivity to the Binance API.
func (c *Client) Ping() error {
	_, err := c.doRequest(http.MethodGet, "/api/v3/ping", nil, nil)
	return err
}

// --- internal helpers ---

// sign appends the HMAC-SHA256 signature to params (modifies params in place).
func (c *Client) sign(params url.Values) {
	payload := params.Encode()
	mac := hmac.New(sha256.New, []byte(c.apiSecret))
	mac.Write([]byte(payload))
	params.Set("signature", hex.EncodeToString(mac.Sum(nil)))
}

// doSignedRequest sends a signed request (with X-MBX-APIKEY header).
func (c *Client) doSignedRequest(method, path string, params url.Values, reqBody io.Reader) ([]byte, error) {
	return c.doRequest(method, path, params, reqBody)
}

// doRequest is the low-level HTTP request dispatcher.
func (c *Client) doRequest(method, path string, params url.Values, reqBody io.Reader) ([]byte, error) {
	rawURL := c.baseURL + path
	if params != nil && (method == http.MethodGet || method == http.MethodDelete) {
		rawURL += "?" + params.Encode()
	}

	req, err := http.NewRequest(method, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	// Attach body for POST requests
	if params != nil && method == http.MethodPost {
		req.Body = io.NopCloser(strings.NewReader(params.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	if c.apiKey != "" {
		req.Header.Set("X-MBX-APIKEY", c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("binance API error %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// currentTimestamp returns the current Unix timestamp in milliseconds as a string.
func (c *Client) currentTimestamp() string {
	adjustedTime := time.Now().UnixMilli() + c.timeOffset
	return strconv.FormatInt(adjustedTime, 10)
}
