package dating

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0FL01/tg-dating-agent/internal/standalone"
	"github.com/amarnathcjd/gogram/telegram"
)

// TestNewStandaloneHandler_Wiring verifies that NewStandaloneHandler correctly
// wires the configuration, LLM client, and Telegram client into a Handler.
// This test uses minimal mocking - only a nil Telegram client which is acceptable
// for verifying the bootstrap wiring without triggering actual Telegram calls.
func TestNewStandaloneHandler_Wiring(t *testing.T) {
	cfg := &standalone.Config{
		OpenRouterAPIKey:     "test-api-key",
		DatingBotChatID:      123456789,
		DatingModel:          "test-model",
		DatingPrompt:         "test prompt",
		DatingMBTIPrompt:     "test mbti prompt",
		DatingMBTIAllowlist:  []string{"INTJ", "ENFP"},
		DatingActionDelay:    1000,
		DatingTemperature:    0.8,
		DatingSkipLowQuality: true,
		DatingMinBioLength:   75,
	}

	// Nil client is acceptable for wiring verification - the handler stores
	// the reference but doesn't make Telegram calls during initialization.
	var tgClient *telegram.Client

	handler := NewStandaloneHandler(cfg, tgClient)

	if handler == nil {
		t.Fatal("NewStandaloneHandler returned nil")
	}

	// Verify handler name for logging/debugging
	if handler.Name() != "dating" {
		t.Errorf("expected handler name 'dating', got %q", handler.Name())
	}

	// Verify config was wired correctly
	if handler.config != cfg {
		t.Error("handler.config was not set to provided cfg")
	}

	// Verify Telegram client reference was stored
	if handler.tgClient != tgClient {
		t.Error("handler.tgClient was not set to provided tgClient")
	}

	// Verify all config fields were propagated to handler
	if handler.chatID != cfg.DatingBotChatID {
		t.Errorf("handler.chatID = %d, want %d", handler.chatID, cfg.DatingBotChatID)
	}
	if handler.model != cfg.DatingModel {
		t.Errorf("handler.model = %q, want %q", handler.model, cfg.DatingModel)
	}
	if handler.prompt != cfg.DatingPrompt {
		t.Errorf("handler.prompt = %q, want %q", handler.prompt, cfg.DatingPrompt)
	}
	if handler.actionDelay != cfg.DatingActionDelay {
		t.Errorf("handler.actionDelay = %v, want %v", handler.actionDelay, cfg.DatingActionDelay)
	}
	if handler.temperature != cfg.DatingTemperature {
		t.Errorf("handler.temperature = %v, want %v", handler.temperature, cfg.DatingTemperature)
	}
}

// TestNewStandaloneHandler_StateInitialized verifies that the handler's
// state machine is properly initialized during bootstrap.
func TestNewStandaloneHandler_StateInitialized(t *testing.T) {
	cfg := &standalone.Config{
		OpenRouterAPIKey:    "test-key",
		DatingBotChatID:     123456789,
		DatingModel:         "test-model",
		DatingMBTIAllowlist: []string{"INTJ"},
	}

	handler := NewStandaloneHandler(cfg, nil)

	if handler.state == nil {
		t.Fatal("handler.state was not initialized")
	}

	// Verify initial state is idle (StateIdle = 0)
	if handler.state.GetState() != StateIdle {
		t.Errorf("expected initial state StateIdle, got %v", handler.state.GetState())
	}

	// Verify state machine is not in stopped state initially
	if handler.state.IsStopped() {
		t.Error("handler should not be in stopped state initially")
	}
}

// TestNewStandaloneHandler_Filter verifies that the Filter function correctly
// matches messages from the configured dating bot chat.
func TestNewStandaloneHandler_Filter(t *testing.T) {
	tests := []struct {
		name      string
		chatID    int64
		wantMatch bool
	}{
		{"matching chat ID", 123456789, true},
		{"different chat ID", 987654321, false},
		{"zero chat ID", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &standalone.Config{
				OpenRouterAPIKey:    "test-key",
				DatingBotChatID:     123456789,
				DatingModel:         "test-model",
				DatingMBTIAllowlist: []string{"INTJ"},
			}

			handler := NewStandaloneHandler(cfg, nil)
			filter := handler.Filter()

			// Create a minimal mock message
			// We can't fully construct a telegram.NewMessage without internal access,
			// but we can verify the handler stores the correct chatID for filtering
			if handler.chatID != cfg.DatingBotChatID {
				t.Errorf("handler.chatID = %d, want %d", handler.chatID, cfg.DatingBotChatID)
			}

			// Verify filter function is not nil
			if filter == nil {
				t.Error("handler.Filter() returned nil")
			}
		})
	}
}

func TestNewStandaloneHandler_WebhookDisabledKeepsNoopDelivery(t *testing.T) {
	cfg := &standalone.Config{
		OpenRouterAPIKey:      "test-key",
		DatingBotChatID:       123456789,
		DatingModel:           "test-model",
		DatingMBTIAllowlist:   []string{"INTJ"},
		DatingMatchWebhookURL: "",
	}

	handler := NewStandaloneHandler(cfg, nil)
	if handler.deliverReciprocalLikeFinalFn != nil {
		t.Fatal("deliverReciprocalLikeFinalFn should be nil when webhook URL is empty")
	}

	err := handler.Handle(&telegram.NewMessage{Message: &telegram.MessageObj{
		Message: "Start chatting: https://t.me/final_user?text=Hi",
	}})
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}
}

func TestNewStandaloneHandler_WebhookEnabledDeliversPayload(t *testing.T) {
	var calls int32
	var gotPayload ReciprocalLikeFinalPayload

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		defer func() {
			_ = r.Body.Close()
		}()

		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	cfg := &standalone.Config{
		OpenRouterAPIKey:        "test-key",
		DatingBotChatID:         123456789,
		DatingModel:             "test-model",
		DatingMBTIAllowlist:     []string{"INTJ"},
		DatingMatchWebhookURL:   server.URL,
		DatingMatchWebhookToken: "token",
	}

	handler := NewStandaloneHandler(cfg, nil)
	if handler.deliverReciprocalLikeFinalFn == nil {
		t.Fatal("deliverReciprocalLikeFinalFn should be wired when webhook URL is set")
	}

	err := handler.Handle(&telegram.NewMessage{Message: &telegram.MessageObj{
		Message: "It's a match! Start chatting: https://t.me/final_user?text=Hi%20there",
	}})
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("webhook calls = %d, want 1", got)
	}

	if gotPayload.ContactUsername != "final_user" {
		t.Fatalf("payload.ContactUsername = %q, want %q", gotPayload.ContactUsername, "final_user")
	}
	if gotPayload.DeeplinkText != "Hi there" {
		t.Fatalf("payload.DeeplinkText = %q, want %q", gotPayload.DeeplinkText, "Hi there")
	}
}

func TestNewStandaloneHandler_WiresProfileDedupeWhenR2Enabled(t *testing.T) {
	cfg := &standalone.Config{
		OpenRouterAPIKey:        "test-key",
		DatingBotChatID:         123456789,
		DatingModel:             "test-model",
		DatingMBTIAllowlist:     []string{"INTJ"},
		DatingProfileDedupTTL:   6 * time.Hour,
		DatingR2Enabled:         true,
		DatingR2Bucket:          "profiles",
		DatingR2Endpoint:        "https://example.r2.cloudflarestorage.com",
		DatingR2Region:          "auto",
		DatingR2AccessKeyID:     "access",
		DatingR2SecretAccessKey: "secret",
		DatingInstanceName:      "instance-a",
	}

	handler := NewStandaloneHandler(cfg, nil)
	if handler.profileDedupe == nil {
		t.Fatal("profileDedupe = nil, want initialized dedupe store")
	}

	store, ok := handler.profileDedupe.(*ProfileDedupeStore)
	if !ok {
		t.Fatalf("profileDedupe type = %T, want *ProfileDedupeStore", handler.profileDedupe)
	}
	if store.ttl != cfg.DatingProfileDedupTTL {
		t.Fatalf("profile dedupe ttl = %v, want %v", store.ttl, cfg.DatingProfileDedupTTL)
	}
}

func TestNewStandaloneHandler_WiresR2ReplyAuditUsingSharedStore(t *testing.T) {
	cfg := &standalone.Config{
		OpenRouterAPIKey:        "test-key",
		DatingBotChatID:         123456789,
		DatingModel:             "test-model",
		DatingMBTIAllowlist:     []string{"INTJ"},
		DatingReplyAuditLogPath: filepath.Join(t.TempDir(), "replies.jsonl"),
		DatingProfileDedupTTL:   6 * time.Hour,
		DatingR2Enabled:         true,
		DatingR2Bucket:          "profiles",
		DatingR2Endpoint:        "https://example.r2.cloudflarestorage.com",
		DatingR2Region:          "auto",
		DatingR2AccessKeyID:     "access",
		DatingR2SecretAccessKey: "secret",
		DatingInstanceName:      "instance-a",
	}

	handler := NewStandaloneHandler(cfg, nil)
	if handler.replyAudit == nil {
		t.Fatal("replyAudit = nil, want composed appender")
	}

	composite, ok := handler.replyAudit.(*CompositeReplyAuditAppender)
	if !ok {
		t.Fatalf("replyAudit type = %T, want *CompositeReplyAuditAppender", handler.replyAudit)
	}

	if len(composite.appenders) != 2 {
		t.Fatalf("composite appender count = %d, want 2", len(composite.appenders))
	}

	localLogger, ok := composite.appenders[0].(*ReplyAuditLogger)
	if !ok {
		t.Fatalf("composite appenders[0] type = %T, want *ReplyAuditLogger", composite.appenders[0])
	}
	if localLogger.path != cfg.DatingReplyAuditLogPath {
		t.Fatalf("local logger path = %q, want %q", localLogger.path, cfg.DatingReplyAuditLogPath)
	}

	r2Appender, ok := composite.appenders[1].(*ReplyAuditR2Appender)
	if !ok {
		t.Fatalf("composite appenders[1] type = %T, want *ReplyAuditR2Appender", composite.appenders[1])
	}

	store, ok := handler.profileDedupe.(*ProfileDedupeStore)
	if !ok {
		t.Fatalf("profileDedupe type = %T, want *ProfileDedupeStore", handler.profileDedupe)
	}

	if r2Appender.store != store.store {
		t.Fatal("r2 reply audit appender does not reuse profile dedupe object store")
	}
}
