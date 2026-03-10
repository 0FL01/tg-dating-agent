package dating

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

	if err := client.DeliverReciprocalLikeFinal(context.Background(), payload, nil); err != nil {
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
	}, nil)
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

func TestReciprocalLikeFinalWebhookClientDeliverMultipartWithPhotos(t *testing.T) {
	type requestCapture struct {
		authHeader     string
		instanceHeader string
		contentType    string
		payload        ReciprocalLikeFinalPayload
		fileCount      int
		fileNames      []string
	}

	var captured requestCapture
	handlerErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.authHeader = r.Header.Get("Authorization")
		captured.instanceHeader = r.Header.Get(datingInstanceHeader)
		captured.contentType = r.Header.Get("Content-Type")

		if err := r.ParseMultipartForm(2 << 20); err != nil {
			select {
			case handlerErrCh <- fmt.Errorf("ParseMultipartForm: %w", err):
			default:
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		payloadField := strings.TrimSpace(r.FormValue("payload"))
		if payloadField == "" {
			select {
			case handlerErrCh <- fmt.Errorf("missing payload form field"):
			default:
			}
		}

		if err := json.Unmarshal([]byte(payloadField), &captured.payload); err != nil {
			select {
			case handlerErrCh <- fmt.Errorf("payload JSON decode: %w", err):
			default:
			}
		}

		files := r.MultipartForm.File["photos"]
		captured.fileCount = len(files)
		captured.fileNames = make([]string, 0, len(files))
		for _, fh := range files {
			captured.fileNames = append(captured.fileNames, fh.Filename)
			f, err := fh.Open()
			if err != nil {
				select {
				case handlerErrCh <- fmt.Errorf("open multipart file: %w", err):
				default:
				}
				continue
			}
			if _, err := io.ReadAll(f); err != nil {
				select {
				case handlerErrCh <- fmt.Errorf("read multipart file: %w", err):
				default:
				}
			}
			_ = f.Close()
		}

		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	client, err := NewReciprocalLikeFinalWebhookClient(&standalone.Config{
		DatingMatchWebhookURL:     server.URL,
		DatingMatchWebhookToken:   "token-photos",
		DatingMatchWebhookTimeout: 2 * time.Second,
		DatingInstanceName:        "instance-media",
	})
	if err != nil {
		t.Fatalf("NewReciprocalLikeFinalWebhookClient() error = %v", err)
	}

	payload := ReciprocalLikeFinalPayload{
		EventType:       reciprocalLikeFinalEventType,
		RawContactURL:   "https://t.me/photo_user",
		ContactUsername: "photo_user",
		EventTimestamp:  time.Unix(1710000001, 0).UTC(),
	}
	photos := []ReciprocalLikePhoto{
		{FileName: "photo1.jpg", ContentType: "image/jpeg", Data: []byte{0x01, 0x02, 0x03}},
		{FileName: "photo2.jpg", ContentType: "image/jpeg", Data: []byte{0x04, 0x05, 0x06}},
	}

	if err := client.DeliverReciprocalLikeFinal(context.Background(), payload, photos); err != nil {
		t.Fatalf("DeliverReciprocalLikeFinal() error = %v, want nil", err)
	}

	select {
	case handlerErr := <-handlerErrCh:
		t.Fatal(handlerErr)
	default:
	}

	if !strings.HasPrefix(captured.contentType, "multipart/form-data;") {
		t.Fatalf("Content-Type = %q, want multipart/form-data", captured.contentType)
	}
	if captured.authHeader != "Bearer token-photos" {
		t.Fatalf("Authorization header = %q, want %q", captured.authHeader, "Bearer token-photos")
	}
	if captured.instanceHeader != "instance-media" {
		t.Fatalf("%s header = %q, want %q", datingInstanceHeader, captured.instanceHeader, "instance-media")
	}
	if captured.payload.ContactUsername != "photo_user" {
		t.Fatalf("payload.ContactUsername = %q, want %q", captured.payload.ContactUsername, "photo_user")
	}
	if captured.fileCount != 2 {
		t.Fatalf("multipart photo files = %d, want 2", captured.fileCount)
	}
}
