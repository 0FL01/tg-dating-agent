package forwarder

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeMessageSender struct {
	sendErr       error
	sendPhotosErr error
	messages      []string
	photos        []Photo
}

func (f *fakeMessageSender) SendMessage(_ context.Context, text string) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.messages = append(f.messages, text)
	return nil
}

func (f *fakeMessageSender) SendPhotos(_ context.Context, photos []Photo) error {
	if f.sendPhotosErr != nil {
		return f.sendPhotosErr
	}
	f.photos = append(f.photos, photos...)
	return nil
}

func newTestForwarderConfig() *Config {
	return &Config{
		BotToken:           "123456:token",
		TargetChatID:       123,
		TelegramAPIBaseURL: DefaultTelegramAPIBaseURL,
		HTTPTimeout:        2 * time.Second,
		BindAddress:        ":8080",
		WebhookPath:        "/webhook",
		WebhookAuthToken:   "token-123",
	}
}

func TestWebhookHandlerAuthSuccess(t *testing.T) {
	cfg := newTestForwarderConfig()
	sender := &fakeMessageSender{}
	h, err := NewWebhookHandler(cfg, sender)
	if err != nil {
		t.Fatalf("NewWebhookHandler() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, cfg.WebhookPath, strings.NewReader(`{"event_type":"reciprocal_like_final","raw_contact_url":"https://t.me/user1"}`))
	req.Header.Set("Authorization", "Bearer token-123")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("forwarded messages = %d, want 1", len(sender.messages))
	}
}

func TestWebhookHandlerMultipartSuccessWithPhotos(t *testing.T) {
	cfg := newTestForwarderConfig()
	sender := &fakeMessageSender{}
	h, err := NewWebhookHandler(cfg, sender)
	if err != nil {
		t.Fatalf("NewWebhookHandler() error = %v", err)
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("payload", `{"event_type":"reciprocal_like_final","raw_contact_url":"https://t.me/user1"}`); err != nil {
		t.Fatalf("WriteField(payload) error = %v", err)
	}

	for range 12 {
		part, err := writer.CreateFormFile("photos", "photo.jpg")
		if err != nil {
			t.Fatalf("CreateFormFile() error = %v", err)
		}
		if _, err := part.Write([]byte("jpeg-bytes")); err != nil {
			t.Fatalf("part.Write() error = %v", err)
		}
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, cfg.WebhookPath, body)
	req.Header.Set("Authorization", "Bearer token-123")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("forwarded messages = %d, want 1", len(sender.messages))
	}
	if len(sender.photos) != maxPhotoCount {
		t.Fatalf("forwarded photos = %d, want %d", len(sender.photos), maxPhotoCount)
	}
}

func TestWebhookHandlerAuthFailure(t *testing.T) {
	cfg := newTestForwarderConfig()
	sender := &fakeMessageSender{}
	h, err := NewWebhookHandler(cfg, sender)
	if err != nil {
		t.Fatalf("NewWebhookHandler() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, cfg.WebhookPath, strings.NewReader(`{"event_type":"reciprocal_like_final","raw_contact_url":"https://t.me/user1"}`))
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
	if len(sender.messages) != 0 {
		t.Fatalf("forwarded messages = %d, want 0", len(sender.messages))
	}
}

func TestWebhookHandlerPayloadValidationFailure(t *testing.T) {
	cfg := newTestForwarderConfig()
	sender := &fakeMessageSender{}
	h, err := NewWebhookHandler(cfg, sender)
	if err != nil {
		t.Fatalf("NewWebhookHandler() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, cfg.WebhookPath, strings.NewReader(`{"event_type":"","raw_contact_url":""}`))
	req.Header.Set("Authorization", "Bearer token-123")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnprocessableEntity)
	}
	if len(sender.messages) != 0 {
		t.Fatalf("forwarded messages = %d, want 0", len(sender.messages))
	}
}

func TestWebhookHandlerFormattingAndForwarding(t *testing.T) {
	cfg := newTestForwarderConfig()
	sender := &fakeMessageSender{}
	h, err := NewWebhookHandler(cfg, sender)
	if err != nil {
		t.Fatalf("NewWebhookHandler() error = %v", err)
	}

	body := `{"event_type":"reciprocal_like_final","raw_contact_url":"https://t.me/test_user?text=hi","contact_username":"test_user","profile_text":"Profile bio","opener_text":"Hello there","mbti":"INTJ"}`
	req := httptest.NewRequest(http.MethodPost, cfg.WebhookPath, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer token-123")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(DatingInstanceHeader, "instance-a")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("forwarded messages = %d, want 1", len(sender.messages))
	}
	msg := sender.messages[0]
	if !strings.Contains(msg, "Source: reciprocal_like_final") {
		t.Fatalf("message = %q, want source line", msg)
	}
	if !strings.Contains(msg, "Instance: instance-a") {
		t.Fatalf("message = %q, want instance line", msg)
	}
	if !strings.Contains(msg, "Contact: https://t.me/test_user?text=hi") {
		t.Fatalf("message = %q, want contact line", msg)
	}
	if !strings.Contains(msg, "Profile: Profile bio") {
		t.Fatalf("message = %q, want profile line", msg)
	}
	if !strings.Contains(msg, "Opener: Hello there") {
		t.Fatalf("message = %q, want opener line", msg)
	}
	if !strings.Contains(msg, "MBTI: INTJ") {
		t.Fatalf("message = %q, want mbti line", msg)
	}
}

func TestWebhookHandlerAcceptsDatingAgentPayloadSchema(t *testing.T) {
	cfg := newTestForwarderConfig()
	sender := &fakeMessageSender{}
	h, err := NewWebhookHandler(cfg, sender)
	if err != nil {
		t.Fatalf("NewWebhookHandler() error = %v", err)
	}

	body := `{"event_type":"reciprocal_like_final","raw_contact_url":"https://t.me/test_user?text=hello","contact_username":"test_user","deeplink_text":"hello","profile_text":"Profile bio","opener_text":"Hi","mbti":"INFJ","context_captured_at":"2026-03-10T12:30:00Z","event_timestamp":"2026-03-10T12:30:45Z"}`
	req := httptest.NewRequest(http.MethodPost, cfg.WebhookPath, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer token-123")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusAccepted)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("forwarded messages = %d, want 1", len(sender.messages))
	}
}

func TestWebhookHandlerMethodAndPathHandling(t *testing.T) {
	cfg := newTestForwarderConfig()
	sender := &fakeMessageSender{}
	h, err := NewWebhookHandler(cfg, sender)
	if err != nil {
		t.Fatalf("NewWebhookHandler() error = %v", err)
	}

	wrongMethodReq := httptest.NewRequest(http.MethodGet, cfg.WebhookPath, nil)
	wrongMethodW := httptest.NewRecorder()
	h.ServeHTTP(wrongMethodW, wrongMethodReq)
	if wrongMethodW.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want %d", wrongMethodW.Code, http.StatusMethodNotAllowed)
	}

	wrongPathReq := httptest.NewRequest(http.MethodPost, "/other", nil)
	wrongPathW := httptest.NewRecorder()
	h.ServeHTTP(wrongPathW, wrongPathReq)
	if wrongPathW.Code != http.StatusNotFound {
		t.Fatalf("wrong path status = %d, want %d", wrongPathW.Code, http.StatusNotFound)
	}
}

func TestWebhookHandlerSendFailure(t *testing.T) {
	cfg := newTestForwarderConfig()
	sender := &fakeMessageSender{sendErr: errors.New("telegram unavailable")}
	h, err := NewWebhookHandler(cfg, sender)
	if err != nil {
		t.Fatalf("NewWebhookHandler() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, cfg.WebhookPath, strings.NewReader(`{"event_type":"reciprocal_like_final","raw_contact_url":"https://t.me/user1"}`))
	req.Header.Set("Authorization", "Bearer token-123")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}
}

func TestWebhookHandlerSendPhotosFailure(t *testing.T) {
	cfg := newTestForwarderConfig()
	sender := &fakeMessageSender{sendPhotosErr: errors.New("telegram unavailable")}
	h, err := NewWebhookHandler(cfg, sender)
	if err != nil {
		t.Fatalf("NewWebhookHandler() error = %v", err)
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("payload", `{"event_type":"reciprocal_like_final","raw_contact_url":"https://t.me/user1"}`); err != nil {
		t.Fatalf("WriteField(payload) error = %v", err)
	}
	part, err := writer.CreateFormFile("photos", "photo.jpg")
	if err != nil {
		t.Fatalf("CreateFormFile() error = %v", err)
	}
	if _, err := part.Write([]byte("jpeg-bytes")); err != nil {
		t.Fatalf("part.Write() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, cfg.WebhookPath, body)
	req.Header.Set("Authorization", "Bearer token-123")
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("forwarded messages = %d, want 1", len(sender.messages))
	}
}

func TestWebhookHandlerMissingContentType(t *testing.T) {
	cfg := newTestForwarderConfig()
	sender := &fakeMessageSender{}
	h, err := NewWebhookHandler(cfg, sender)
	if err != nil {
		t.Fatalf("NewWebhookHandler() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, cfg.WebhookPath, strings.NewReader(`{"event_type":"reciprocal_like_final","raw_contact_url":"https://t.me/user1"}`))
	req.Header.Set("Authorization", "Bearer token-123")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnsupportedMediaType)
	}
	if len(sender.messages) != 0 {
		t.Fatalf("forwarded messages = %d, want 0", len(sender.messages))
	}
}
