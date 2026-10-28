// Package notify sends trade alerts and error notifications via Telegram.
// If TELEGRAM_BOT_TOKEN is not set, all methods are no-ops — the bot runs fine without it.
package notify

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/adnanshaldrake/signalbot/pkg/config"
)

const telegramAPIBase = "https://api.telegram.org/bot"

// Notifier sends messages to a Telegram chat.
type Notifier struct {
	botToken string
	chatID   string
	client   *http.Client
	enabled  bool
}

// NewNotifier creates a Telegram notifier from config.
// If the token is empty, the notifier is disabled and all sends are silent no-ops.
func NewNotifier(cfg *config.BotConfig) *Notifier {
	enabled := cfg.TelegramBotToken != "" && cfg.TelegramChatID != ""
	if !enabled {
		slog.Info("telegram notifications disabled (TELEGRAM_BOT_TOKEN or TELEGRAM_CHAT_ID not set)")
	}
	return &Notifier{
		botToken: cfg.TelegramBotToken,
		chatID:   cfg.TelegramChatID,
		client:   &http.Client{Timeout: 5 * time.Second},
		enabled:  enabled,
	}
}

// TradeOpened sends a buy/entry notification.
func (n *Notifier) TradeOpened(symbol, mode string, price, qty, quoteSpent float64) {
	emoji := "🟢"
	if mode == "PAPER" {
		emoji = "📝"
	}
	msg := fmt.Sprintf(
		"%s *%s BUY* — `%s`\n"+
			"Price: `$%.6f`\n"+
			"Qty: `%.6f`\n"+
			"Spent: `$%.2f USDT`\n"+
			"Mode: `%s`\n"+
			"Time: `%s`",
		emoji, mode, symbol,
		price, qty, quoteSpent,
		mode,
		time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
	)
	n.send(msg)
}

// TradeClosed sends a sell/exit notification with P&L.
func (n *Notifier) TradeClosed(symbol, mode string, entryPrice, exitPrice, pnlUSDT, pnlPct float64) {
	emoji := "🔴"
	if pnlUSDT > 0 {
		emoji = "✅"
	}
	if mode == "PAPER" {
		emoji = "📝"
	}
	msg := fmt.Sprintf(
		"%s *%s SELL* — `%s`\n"+
			"Entry: `$%.6f` → Exit: `$%.6f`\n"+
			"P&L: `$%.4f USDT` (`%.2f%%`)\n"+
			"Mode: `%s`\n"+
			"Time: `%s`",
		emoji, mode, symbol,
		entryPrice, exitPrice,
		pnlUSDT, pnlPct,
		mode,
		time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
	)
	n.send(msg)
}

// KillSwitchTriggered sends a high-priority alert when the daily loss limit is hit.
func (n *Notifier) KillSwitchTriggered(dailyPnL float64) {
	msg := fmt.Sprintf(
		"🚨 *KILL SWITCH ACTIVATED*\n"+
			"Daily P&L: `$%.2f USDT`\n"+
			"All trading has been halted.\n"+
			"Time: `%s`",
		dailyPnL,
		time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
	)
	n.send(msg)
}

// Error sends an error alert.
func (n *Notifier) Error(component, message string) {
	msg := fmt.Sprintf(
		"⚠️ *Error in %s*\n`%s`\nTime: `%s`",
		component, message,
		time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
	)
	n.send(msg)
}

// BotStarted sends a startup notification.
func (n *Notifier) BotStarted(mode, interval string, watchlistLen int) {
	msg := fmt.Sprintf(
		"🤖 *Signalbot started*\n"+
			"Mode: `%s`\n"+
			"Interval: `%s`\n"+
			"Watching: `%d symbols`\n"+
			"Time: `%s`",
		mode, interval, watchlistLen,
		time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
	)
	n.send(msg)
}

// send dispatches a Markdown-formatted message to the configured Telegram chat.
func (n *Notifier) send(text string) {
	if !n.enabled {
		return
	}

	apiURL := fmt.Sprintf("%s%s/sendMessage", telegramAPIBase, n.botToken)

	params := url.Values{}
	params.Set("chat_id", n.chatID)
	params.Set("text", text)
	params.Set("parse_mode", "Markdown")

	resp, err := n.client.Post(apiURL, "application/x-www-form-urlencoded", strings.NewReader(params.Encode()))
	if err != nil {
		slog.Error("telegram send failed", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Error("telegram API error", "status", resp.StatusCode)
	}
}
