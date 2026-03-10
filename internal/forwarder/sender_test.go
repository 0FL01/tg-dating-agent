package forwarder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
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

func TestTelegramSenderSendPhotosSuccess(t *testing.T) {
	photoCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot123456:token/sendPhoto" {
			t.Fatalf("path = %q, want %q", r.URL.Path, "/bot123456:token/sendPhoto")
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q, want %q", r.Method, http.MethodPost)
		}

		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("ParseMediaType() error = %v", err)
		}
		if mediaType != "multipart/form-data" {
			t.Fatalf("mediaType = %q, want %q", mediaType, "multipart/form-data")
		}

		mr := multipart.NewReader(r.Body, params["boundary"])
		chatIDSeen := false
		photoSeen := false
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("NextPart() error = %v", err)
			}

			partBytes, err := io.ReadAll(part)
			if err != nil {
				t.Fatalf("ReadAll(part) error = %v", err)
			}

			switch part.FormName() {
			case "chat_id":
				if string(partBytes) != "12345" {
					t.Fatalf("chat_id = %q, want %q", string(partBytes), "12345")
				}
				chatIDSeen = true
			case "photo":
				if !bytes.Equal(partBytes, []byte("image-bytes")) {
					t.Fatalf("photo bytes = %q, want %q", string(partBytes), "image-bytes")
				}
				photoSeen = true
			}
		}

		if !chatIDSeen {
			t.Fatal("chat_id part not found")
		}
		if !photoSeen {
			t.Fatal("photo part not found")
		}

		photoCalls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
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

	err = sender.SendPhotos(context.Background(), []Photo{
		{Filename: "photo1.jpg", ContentType: "image/jpeg", Data: []byte("image-bytes")},
		{Filename: "photo2.jpg", ContentType: "image/jpeg", Data: []byte("image-bytes")},
	})
	if err != nil {
		t.Fatalf("SendPhotos() error = %v", err)
	}

	if photoCalls != 2 {
		t.Fatalf("photoCalls = %d, want 2", photoCalls)
	}
}

func TestTelegramSenderSendPhotosNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("telegram down"))
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

	err = sender.SendPhotos(context.Background(), []Photo{{Filename: "photo.jpg", Data: []byte("image-bytes")}})
	if err == nil {
		t.Fatal("SendPhotos() error = nil, want non-2xx error")
	}
	if !strings.Contains(err.Error(), "sendPhoto returned status 502") {
		t.Fatalf("SendPhotos() error = %v, want status mention", err)
	}
	if !strings.Contains(err.Error(), "telegram down") {
		t.Fatalf("SendPhotos() error = %v, want body mention", err)
	}
}
