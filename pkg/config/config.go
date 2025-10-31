package config

type Category int8
type Url uint8

const (
	Status Category = iota
	ExchangeInfo
	DailyTicker
	CandleStick
	Order
)

const (
	DataUrl Url = iota
	MarketUrl1
	MarketUrl2
	MarketUrl3
	MarketUrl4
	MarketUrl5
)

func (c Category) String() string {
	/*
		Returns the corresponding Binance API endpoint URL for the category.
		If the category is unknown, it returns "unknown".
		String returns the string representation of the given category.
	*/
	switch c {
	case Status:
		return "/api/v3/ping"
	case ExchangeInfo:
		return "/api/v3/exchangeInfo?permissions=SPOT"
	case DailyTicker:
		return "/api/v3/ticker/24hr"
	case CandleStick:
		return "/api/v3/klines"
	case Order:
		return "/api/v3/order/test"
	}
	return "unknown"
}

func (u Url) String() string {
	/*
		Returns the corresponding Binance URL.
		If the category is unknown, it returns "unknown".
		String returns the string representation of the given category.
	*/
	switch u {
	case DataUrl:
		return "https://data-api.binance.vision"
	case MarketUrl1:
		return "https://api.binance.com"
	case MarketUrl2:
		return "https://api1.binance.com"
	case MarketUrl3:
		return "https://api2.binance.com"
	case MarketUrl4:
		return "https://api3.binance.com"
	case MarketUrl5:
		return "https://api4.binance.com"
	}
	return "unknown"
}
