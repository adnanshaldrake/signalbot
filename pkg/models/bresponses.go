package models

type RateLimit struct {
	RateLimitType string `json:"rateLimitType"`
	Interval      string `json:"interval"`
	IntervalNum   int    `json:"intervalNum"`
	Limit         int    `json:"limit"`
}

type Symbol struct {
	Symbol                          string   `json:"symbol"`
	Status                          string   `json:"status"`
	BaseAsset                       string   `json:"baseAsset"`
	BaseAssetPrecision              int      `json:"baseAssetPrecision"`
	QuoteAsset                      string   `json:"quoteAsset"`
	QuotePrecision                  int      `json:"quotePrecision"`
	QuoteAssetPrecision             int      `json:"quoteAssetPrecision"`
	OrderTypes                      []string `json:"orderTypes"`
	IcebergAllowed                  bool     `json:"icebergAllowed"`
	OcoAllowed                      bool     `json:"ocoAllowed"`
	QuoteOrderQtyMarketAllowed      bool     `json:"quoteOrderQtyMarketAllowed"`
	AllowTrailingStop               bool     `json:"allowTrailingStop"`
	CancelReplaceAllowed            bool     `json:"cancelReplaceAllowed"`
	IsSpotTradingAllowed            bool     `json:"isSpotTradingAllowed"`
	IsMarginTradingAllowed          bool     `json:"isMarginTradingAllowed"`
	Filters                         []any    `json:"filters"`
	Permissions                     []string `json:"permissions"`
	DefaultSelfTradePreventionMode  string   `json:"defaultSelfTradePreventionMode"`
	AllowedSelfTradePreventionModes []string `json:"allowedSelfTradePreventionModes"`
}

type ExchangeInfoResponse struct {
	// Struct to parse the response from the Binance Market Data Exchange Info endpoint
	Timezone        string      `json:"timezone"`
	ServerTime      int64       `json:"serverTime"`
	RateLimits      []RateLimit `json:"rateLimits"`
	ExchangeFilters []any       `json:"exchangeFilters"`
	Symbols         []Symbol    `json:"symbols"`
}

// SignalResult defines the structure for our JSON output
type SignalResult struct {
	Symbol   string `json:"symbol"`
	Price    string `json:"price"`
	Time     string `json:"time"`
	Strategy string `json:"strategy"`
}

type Strategy int

const (
	LongTermTrendFollowing Strategy = iota
	ShortTermMeanReversion
)
