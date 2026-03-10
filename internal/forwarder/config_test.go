package forwarder

import (
	"strings"
	"testing"
	"time"
)

func setRequiredForwarderEnv(t *testing.T) {
	t.Helper()
	t.Setenv("FORWARDER_BOT_TOKEN", "123456:token")
	t.Setenv("FORWARDER_TARGET_CHAT_ID", "12345")
	t.Setenv("FORWARDER_WEBHOOK_AUTH_TOKEN", "shared-secret")
}

func TestLoadConfigSuccessDefaults(t *testing.T) {
	setRequiredForwarderEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.BotToken != "123456:token" {
		t.Fatalf("BotToken = %q, want %q", cfg.BotToken, "123456:token")
	}
	if cfg.TargetChatID != 12345 {
		t.Fatalf("TargetChatID = %d, want %d", cfg.TargetChatID, 12345)
	}
	if cfg.TelegramAPIBaseURL != DefaultTelegramAPIBaseURL {
		t.Fatalf("TelegramAPIBaseURL = %q, want %q", cfg.TelegramAPIBaseURL, DefaultTelegramAPIBaseURL)
	}
	if cfg.HTTPTimeout != DefaultHTTPTimeout {
		t.Fatalf("HTTPTimeout = %v, want %v", cfg.HTTPTimeout, DefaultHTTPTimeout)
	}
	if cfg.BindAddress != DefaultBindAddress {
		t.Fatalf("BindAddress = %q, want %q", cfg.BindAddress, DefaultBindAddress)
	}
	if cfg.WebhookPath != DefaultWebhookPath {
		t.Fatalf("WebhookPath = %q, want %q", cfg.WebhookPath, DefaultWebhookPath)
	}
	if cfg.WebhookAuthToken != "shared-secret" {
		t.Fatalf("WebhookAuthToken = %q, want %q", cfg.WebhookAuthToken, "shared-secret")
	}
}

func TestLoadConfigCustomOptionalValues(t *testing.T) {
	setRequiredForwarderEnv(t)
	t.Setenv("FORWARDER_TELEGRAM_API_BASE_URL", "https://example.local/api")
	t.Setenv("FORWARDER_HTTP_TIMEOUT", "3s")
	t.Setenv("FORWARDER_BIND_ADDRESS", "127.0.0.1:9090")
	t.Setenv("FORWARDER_WEBHOOK_PATH", "/hooks/forward")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.TelegramAPIBaseURL != "https://example.local/api" {
		t.Fatalf("TelegramAPIBaseURL = %q, want %q", cfg.TelegramAPIBaseURL, "https://example.local/api")
	}
	if cfg.HTTPTimeout != 3*time.Second {
		t.Fatalf("HTTPTimeout = %v, want %v", cfg.HTTPTimeout, 3*time.Second)
	}
	if cfg.BindAddress != "127.0.0.1:9090" {
		t.Fatalf("BindAddress = %q, want %q", cfg.BindAddress, "127.0.0.1:9090")
	}
	if cfg.WebhookPath != "/hooks/forward" {
		t.Fatalf("WebhookPath = %q, want %q", cfg.WebhookPath, "/hooks/forward")
	}
}

func TestLoadConfigMissingRequired(t *testing.T) {
	t.Setenv("FORWARDER_BOT_TOKEN", "")
	t.Setenv("FORWARDER_TARGET_CHAT_ID", "")
	t.Setenv("FORWARDER_WEBHOOK_AUTH_TOKEN", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "FORWARDER_BOT_TOKEN") {
		t.Fatalf("Load() error = %v, want FORWARDER_BOT_TOKEN mention", err)
	}
}

func TestLoadConfigInvalidChatID(t *testing.T) {
	t.Setenv("FORWARDER_BOT_TOKEN", "123456:token")
	t.Setenv("FORWARDER_TARGET_CHAT_ID", "abc")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "FORWARDER_TARGET_CHAT_ID") {
		t.Fatalf("Load() error = %v, want FORWARDER_TARGET_CHAT_ID mention", err)
	}
}

func TestLoadConfigInvalidBaseURL(t *testing.T) {
	setRequiredForwarderEnv(t)
	t.Setenv("FORWARDER_TELEGRAM_API_BASE_URL", "://bad-url")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
}

func TestLoadConfigInvalidTimeout(t *testing.T) {
	setRequiredForwarderEnv(t)
	t.Setenv("FORWARDER_HTTP_TIMEOUT", "not-a-duration")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "FORWARDER_HTTP_TIMEOUT") {
		t.Fatalf("Load() error = %v, want FORWARDER_HTTP_TIMEOUT mention", err)
	}
}

func TestConfigValidateInvalid(t *testing.T) {
	err := (&Config{
		BotToken:           "token",
		TargetChatID:       0,
		TelegramAPIBaseURL: DefaultTelegramAPIBaseURL,
		HTTPTimeout:        time.Second,
		BindAddress:        DefaultBindAddress,
		WebhookPath:        DefaultWebhookPath,
		WebhookAuthToken:   "token",
	}).Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want error")
	}
}

func TestLoadConfigMissingWebhookToken(t *testing.T) {
	setRequiredForwarderEnv(t)
	t.Setenv("FORWARDER_WEBHOOK_AUTH_TOKEN", "")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "FORWARDER_WEBHOOK_AUTH_TOKEN") {
		t.Fatalf("Load() error = %v, want FORWARDER_WEBHOOK_AUTH_TOKEN mention", err)
	}
}

func TestConfigValidateInvalidWebhookPath(t *testing.T) {
	err := (&Config{
		BotToken:           "token",
		TargetChatID:       123,
		TelegramAPIBaseURL: DefaultTelegramAPIBaseURL,
		HTTPTimeout:        time.Second,
		BindAddress:        DefaultBindAddress,
		WebhookPath:        "hooks/no-leading-slash",
		WebhookAuthToken:   "token",
	}).Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want error")
	}
}
