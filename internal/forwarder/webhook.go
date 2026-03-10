package forwarder

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

const (
	DatingInstanceHeader = "X-Dating-Instance-Name"
	webhookTokenHeader   = "X-Forwarder-Webhook-Token"
	maxWebhookBodyBytes  = 64 << 10
)

type MessageSender interface {
	SendMessage(ctx context.Context, text string) error
}

type ReciprocalLikeFinalPayload struct {
	EventType         string    `json:"event_type"`
	RawContactURL     string    `json:"raw_contact_url"`
	ContactUsername   string    `json:"contact_username"`
	DeeplinkText      string    `json:"deeplink_text,omitempty"`
	ProfileText       string    `json:"profile_text,omitempty"`
	OpenerText        string    `json:"opener_text,omitempty"`
	MBTI              string    `json:"mbti,omitempty"`
	ContextCapturedAt time.Time `json:"context_captured_at,omitempty"`
	EventTimestamp    time.Time `json:"event_timestamp,omitempty"`
}

func (p ReciprocalLikeFinalPayload) Validate() error {
	if strings.TrimSpace(p.EventType) == "" {
		return fmt.Errorf("event_type is required")
	}

	if strings.TrimSpace(p.RawContactURL) == "" && strings.TrimSpace(p.ContactUsername) == "" {
		return fmt.Errorf("raw_contact_url or contact_username is required")
	}

	return nil
}

func NewWebhookHandler(cfg *Config, sender MessageSender) (http.Handler, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate forwarder config: %w", err)
	}
	if sender == nil {
		return nil, fmt.Errorf("message sender is nil")
	}

	mux := http.NewServeMux()
	mux.HandleFunc(cfg.WebhookPath, func(w http.ResponseWriter, r *http.Request) {
		handleWebhook(w, r, cfg, sender)
	})

	return mux, nil
}

func NewHTTPServer(cfg *Config, sender MessageSender) (*http.Server, error) {
	handler, err := NewWebhookHandler(cfg, sender)
	if err != nil {
		return nil, err
	}

	return &http.Server{
		Addr:              cfg.BindAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.HTTPTimeout,
		WriteTimeout:      cfg.HTTPTimeout,
	}, nil
}

func handleWebhook(w http.ResponseWriter, r *http.Request, cfg *Config, sender MessageSender) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !isAuthorized(r, cfg.WebhookAuthToken) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	contentType := strings.TrimSpace(r.Header.Get("Content-Type"))
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "application/json" {
		http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
		return
	}

	var payload ReciprocalLikeFinalPayload
	dec := json.NewDecoder(io.LimitReader(r.Body, maxWebhookBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&payload); err != nil {
		http.Error(w, "invalid JSON payload", http.StatusBadRequest)
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "invalid JSON payload", http.StatusBadRequest)
		return
	}
	if err := payload.Validate(); err != nil {
		http.Error(w, "invalid payload: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}

	text := formatForwardMessage(r.Header.Get(DatingInstanceHeader), payload)
	if err := sender.SendMessage(r.Context(), text); err != nil {
		http.Error(w, "failed to forward message", http.StatusBadGateway)
		return
	}

	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("forwarded"))
}

func isAuthorized(r *http.Request, expectedToken string) bool {
	if r == nil {
		return false
	}

	providedToken := extractBearerToken(r.Header.Get("Authorization"))
	if providedToken == "" {
		providedToken = strings.TrimSpace(r.Header.Get(webhookTokenHeader))
	}

	return secureEqual(strings.TrimSpace(expectedToken), providedToken)
}

func extractBearerToken(authHeader string) string {
	header := strings.TrimSpace(authHeader)
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}

func secureEqual(expected, actual string) bool {
	if expected == "" || actual == "" {
		return false
	}
	if len(expected) != len(actual) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}

func formatForwardMessage(instance string, payload ReciprocalLikeFinalPayload) string {
	contact := strings.TrimSpace(payload.RawContactURL)
	if contact == "" {
		username := strings.TrimPrefix(strings.TrimSpace(payload.ContactUsername), "@")
		if username != "" {
			contact = "https://t.me/" + username
		}
	}

	b := strings.Builder{}
	b.WriteString("New reciprocal like\n")
	b.WriteString("Source: ")
	b.WriteString(strings.TrimSpace(payload.EventType))
	b.WriteString("\n")
	b.WriteString("Instance: ")
	b.WriteString(nonEmptyOrDash(instance))
	b.WriteString("\n")
	b.WriteString("Contact: ")
	b.WriteString(nonEmptyOrDash(contact))
	b.WriteString("\n")
	b.WriteString("Profile: ")
	b.WriteString(nonEmptyOrDash(payload.ProfileText))
	b.WriteString("\n")
	b.WriteString("Opener: ")
	b.WriteString(nonEmptyOrDash(payload.OpenerText))

	if mbti := strings.TrimSpace(payload.MBTI); mbti != "" {
		b.WriteString("\n")
		b.WriteString("MBTI: ")
		b.WriteString(mbti)
	}

	return b.String()
}

func nonEmptyOrDash(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "-"
	}
	return trimmed
}
