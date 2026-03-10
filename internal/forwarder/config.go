package forwarder

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultTelegramAPIBaseURL = "https://api.telegram.org"
	DefaultHTTPTimeout        = 10 * time.Second
	DefaultBindAddress        = ":8080"
	DefaultWebhookPath        = "/webhook/reciprocal-like-final"
)

type Config struct {
	BotToken           string
	TargetChatID       int64
	TelegramAPIBaseURL string
	HTTPTimeout        time.Duration
	BindAddress        string
	WebhookPath        string
	WebhookAuthToken   string
}

func Load() (*Config, error) {
	botToken := strings.TrimSpace(os.Getenv("FORWARDER_BOT_TOKEN"))
	if botToken == "" {
		return nil, fmt.Errorf("FORWARDER_BOT_TOKEN is required")
	}

	targetChatIDRaw := strings.TrimSpace(os.Getenv("FORWARDER_TARGET_CHAT_ID"))
	if targetChatIDRaw == "" {
		return nil, fmt.Errorf("FORWARDER_TARGET_CHAT_ID is required")
	}

	targetChatID, err := strconv.ParseInt(targetChatIDRaw, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("FORWARDER_TARGET_CHAT_ID must be a valid integer: %w", err)
	}

	baseURL := strings.TrimSpace(os.Getenv("FORWARDER_TELEGRAM_API_BASE_URL"))
	if baseURL == "" {
		baseURL = DefaultTelegramAPIBaseURL
	}

	httpTimeout := DefaultHTTPTimeout
	if timeoutRaw := strings.TrimSpace(os.Getenv("FORWARDER_HTTP_TIMEOUT")); timeoutRaw != "" {
		parsedTimeout, parseErr := time.ParseDuration(timeoutRaw)
		if parseErr != nil {
			return nil, fmt.Errorf("FORWARDER_HTTP_TIMEOUT must be a valid duration: %w", parseErr)
		}
		httpTimeout = parsedTimeout
	}

	bindAddress := strings.TrimSpace(os.Getenv("FORWARDER_BIND_ADDRESS"))
	if bindAddress == "" {
		bindAddress = DefaultBindAddress
	}

	webhookPath := strings.TrimSpace(os.Getenv("FORWARDER_WEBHOOK_PATH"))
	if webhookPath == "" {
		webhookPath = DefaultWebhookPath
	}

	webhookAuthToken := strings.TrimSpace(os.Getenv("FORWARDER_WEBHOOK_AUTH_TOKEN"))
	if webhookAuthToken == "" {
		return nil, fmt.Errorf("FORWARDER_WEBHOOK_AUTH_TOKEN is required")
	}

	cfg := &Config{
		BotToken:           botToken,
		TargetChatID:       targetChatID,
		TelegramAPIBaseURL: baseURL,
		HTTPTimeout:        httpTimeout,
		BindAddress:        bindAddress,
		WebhookPath:        webhookPath,
		WebhookAuthToken:   webhookAuthToken,
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("forwarder config is nil")
	}

	if strings.TrimSpace(c.BotToken) == "" {
		return fmt.Errorf("bot token is required")
	}

	if c.TargetChatID == 0 {
		return fmt.Errorf("target chat ID must be non-zero")
	}

	parsedURL, err := url.ParseRequestURI(strings.TrimSpace(c.TelegramAPIBaseURL))
	if err != nil {
		return fmt.Errorf("invalid Telegram API base URL %q: %w", c.TelegramAPIBaseURL, err)
	}

	if parsedURL.Scheme == "" || parsedURL.Host == "" {
		return fmt.Errorf("invalid Telegram API base URL %q", c.TelegramAPIBaseURL)
	}

	if c.HTTPTimeout <= 0 {
		return fmt.Errorf("HTTP timeout must be positive")
	}

	if strings.TrimSpace(c.BindAddress) == "" {
		return fmt.Errorf("bind address is required")
	}

	path := strings.TrimSpace(c.WebhookPath)
	if path == "" {
		return fmt.Errorf("webhook path is required")
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("webhook path must start with '/'")
	}

	if strings.TrimSpace(c.WebhookAuthToken) == "" {
		return fmt.Errorf("webhook auth token is required")
	}

	return nil
}
