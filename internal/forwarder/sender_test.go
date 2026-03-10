package forwarder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewTelegramSenderInvalidConfig(t *testing.T) {
	_, err := NewTelegramSender(&Config{
		BotToken:           "",
		TargetChatID:       100,
		TelegramAPIBaseURL: DefaultTelegramAPIBaseURL,
		HTTPTimeout:        time.Second,
		BindAddress:        DefaultBindAddress,
		WebhookPath:        DefaultWebhookPath,
		WebhookAuthToken:   "token",
	})
	if err == nil {
		t.Fatal("NewTelegramSender() error = nil, want error")
	}
}

func TestTelegramSenderSendMessageSuccess(t *testing.T) {
	type requestCapture struct {
		method      string
		path        string
		contentType string
		payload     map[string]any
	}

	var captured requestCapture
	handlerErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.method = r.Method
		captured.path = r.URL.Path
		captured.contentType = r.Header.Get("Content-Type")

		if err := json.NewDecoder(r.Body).Decode(&captured.payload); err != nil {
			select {
			case handlerErrCh <- fmt.Errorf("decode request body: %w", err):
			default:
			}
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer server.Close()

	sender, err := NewTelegramSender(&Config{
		BotToken:           "123456:token",
		TargetChatID:       12345,
		TelegramAPIBaseURL: server.URL,
		HTTPTimeout:        2 * time.Second,
		BindAddress:        DefaultBindAddress,
		WebhookPath:        DefaultWebhookPath,
		WebhookAuthToken:   "token",
	})
	if err != nil {
		t.Fatalf("NewTelegramSender() error = %v", err)
	}

	if err := sender.SendMessage(context.Background(), "hello from test"); err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}

	select {
	case handlerErr := <-handlerErrCh:
		t.Fatal(handlerErr)
	default:
	}

	if captured.method != http.MethodPost {
		t.Fatalf("method = %q, want %q", captured.method, http.MethodPost)
	}
	if captured.path != "/bot123456:token/sendMessage" {
		t.Fatalf("path = %q, want %q", captured.path, "/bot123456:token/sendMessage")
	}
	if captured.contentType != "application/json" {
		t.Fatalf("contentType = %q, want %q", captured.contentType, "application/json")
	}
	if captured.payload["chat_id"] != float64(12345) {
		t.Fatalf("chat_id = %v, want %d", captured.payload["chat_id"], 12345)
	}
	if captured.payload["text"] != "hello from test" {
		t.Fatalf("text = %v, want %q", captured.payload["text"], "hello from test")
	}
}

func TestTelegramSenderSendMessageNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("bad request"))
	}))
	defer server.Close()

	sender, err := NewTelegramSender(&Config{
		BotToken:           "123456:token",
		TargetChatID:       12345,
		TelegramAPIBaseURL: server.URL,
		HTTPTimeout:        time.Second,
		BindAddress:        DefaultBindAddress,
		WebhookPath:        DefaultWebhookPath,
		WebhookAuthToken:   "token",
	})
	if err != nil {
		t.Fatalf("NewTelegramSender() error = %v", err)
	}

	err = sender.SendMessage(context.Background(), "hello")
	if err == nil {
		t.Fatal("SendMessage() error = nil, want non-2xx error")
	}
	if !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("SendMessage() error = %v, want status mention", err)
	}
	if !strings.Contains(err.Error(), "bad request") {
		t.Fatalf("SendMessage() error = %v, want body mention", err)
	}
}
