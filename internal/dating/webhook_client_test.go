package dating

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/0FL01/tg-dating-agent/internal/standalone"
)

func TestNewReciprocalLikeFinalWebhookClientDisabledWhenURLIsEmpty(t *testing.T) {
	client, err := NewReciprocalLikeFinalWebhookClient(&standalone.Config{
		DatingMatchWebhookURL: "   ",
	})
	if err != nil {
		t.Fatalf("NewReciprocalLikeFinalWebhookClient() error = %v, want nil", err)
	}
	if client != nil {
		t.Fatal("NewReciprocalLikeFinalWebhookClient() client != nil, want nil when URL is empty")
	}
}

func TestNewReciprocalLikeFinalWebhookClientInvalidURL(t *testing.T) {
	_, err := NewReciprocalLikeFinalWebhookClient(&standalone.Config{DatingMatchWebhookURL: "://bad-url"})
	if err == nil {
		t.Fatal("NewReciprocalLikeFinalWebhookClient() error = nil, want error")
	}
}

func TestReciprocalLikeFinalWebhookClientDeliverSuccess(t *testing.T) {
	type requestCapture struct {
		authHeader     string
		instanceHeader string
		contentType    string
		payload        map[string]any
	}

	var captured requestCapture
	handlerErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.authHeader = r.Header.Get("Authorization")
		captured.instanceHeader = r.Header.Get(datingInstanceHeader)
		captured.contentType = r.Header.Get("Content-Type")

		if r.Method != http.MethodPost {
			select {
			case handlerErrCh <- fmt.Errorf("request method = %s, want POST", r.Method):
			default:
			}
		}

		if err := json.NewDecoder(r.Body).Decode(&captured.payload); err != nil {
			select {
			case handlerErrCh <- fmt.Errorf("decode request body: %w", err):
			default:
			}
		}

		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client, err := NewReciprocalLikeFinalWebhookClient(&standalone.Config{
		DatingMatchWebhookURL:     server.URL,
		DatingMatchWebhookToken:   "token-123",
		DatingMatchWebhookTimeout: 2 * time.Second,
		DatingInstanceName:        "instance-a",
	})
	if err != nil {
		t.Fatalf("NewReciprocalLikeFinalWebhookClient() error = %v", err)
	}

	payload := ReciprocalLikeFinalPayload{
		EventType:       reciprocalLikeFinalEventType,
		RawContactURL:   "https://t.me/test_user",
		ContactUsername: "test_user",
		EventTimestamp:  time.Unix(1710000000, 0).UTC(),
	}

	if err := client.DeliverReciprocalLikeFinal(context.Background(), payload); err != nil {
		t.Fatalf("DeliverReciprocalLikeFinal() error = %v, want nil", err)
	}
	select {
	case handlerErr := <-handlerErrCh:
		t.Fatal(handlerErr)
	default:
	}

	if captured.authHeader != "Bearer token-123" {
		t.Fatalf("Authorization header = %q, want %q", captured.authHeader, "Bearer token-123")
	}
	if captured.instanceHeader != "instance-a" {
		t.Fatalf("%s header = %q, want %q", datingInstanceHeader, captured.instanceHeader, "instance-a")
	}
	if captured.contentType != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", captured.contentType, "application/json")
	}
	if captured.payload["contact_username"] != "test_user" {
		t.Fatalf("payload contact_username = %v, want %q", captured.payload["contact_username"], "test_user")
	}
}

func TestReciprocalLikeFinalWebhookClientDeliverNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("unauthorized"))
	}))
	defer server.Close()

	client, err := NewReciprocalLikeFinalWebhookClient(&standalone.Config{
		DatingMatchWebhookURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewReciprocalLikeFinalWebhookClient() error = %v", err)
	}

	err = client.DeliverReciprocalLikeFinal(context.Background(), ReciprocalLikeFinalPayload{
		EventType:       reciprocalLikeFinalEventType,
		RawContactURL:   "https://t.me/test_user",
		ContactUsername: "test_user",
		EventTimestamp:  time.Unix(1710000000, 0).UTC(),
	})
	if err == nil {
		t.Fatal("DeliverReciprocalLikeFinal() error = nil, want non-2xx error")
	}
	if !strings.Contains(err.Error(), "status 401") {
		t.Fatalf("DeliverReciprocalLikeFinal() error = %v, want status code in error", err)
	}
	if !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("DeliverReciprocalLikeFinal() error = %v, want response body in error", err)
	}
}

func TestNewReciprocalLikeFinalWebhookClientUsesDefaultTimeout(t *testing.T) {
	client, err := NewReciprocalLikeFinalWebhookClient(&standalone.Config{DatingMatchWebhookURL: "https://example.com/hook"})
	if err != nil {
		t.Fatalf("NewReciprocalLikeFinalWebhookClient() error = %v", err)
	}

	if client.httpClient.Timeout != standalone.DefaultDatingMatchWebhookTimeout {
		t.Fatalf("http timeout = %v, want %v", client.httpClient.Timeout, standalone.DefaultDatingMatchWebhookTimeout)
	}
}
