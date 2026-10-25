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

	"adnan/binance-bot/pkg/config"
)

const (
	productionBaseURL = "https://api.binance.com"
	testnetBaseURL    = "https://testnet.binance.vision"
	demonetBaseURL    = "https://demo-api.binance.com"
)

type SymbolFilter struct {
	StepSize float64
	MinQty   float64
	Decimals int
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
		Symbol  string                   `json:"symbol"`
		Status  string                   `json:"status"`
		Filters []map[string]interface{} `json:"filters"`
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

	c.exchangeRules = make(map[string]SymbolFilter)

	for _, s := range data.Symbols {
		if s.Status != "TRADING" {
			continue
		}
		for _, filter := range s.Filters {
			if filter["filterType"] == "LOT_SIZE" {
				stepSizeStr, _ := filter["stepSize"].(string)
				minQtyStr, _ := filter["minQty"].(string)

				stepSize, _ := strconv.ParseFloat(stepSizeStr, 64)
				minQty, _ := strconv.ParseFloat(minQtyStr, 64)

				// Calculate decimal places dynamically from stepSize string
				decimals := getDecimals(stepSizeStr)

				c.exchangeRules[s.Symbol] = SymbolFilter{
					StepSize: stepSize,
					MinQty:   minQty,
					Decimals: decimals,
				}
			}
		}
	}
	return nil
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
