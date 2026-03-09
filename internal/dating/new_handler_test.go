package dating

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/0FL01/tg-dating-agent/internal/standalone"
)

func TestNewHandlerReplyAuditEnabledWithConfiguredPath(t *testing.T) {
	cfg := &standalone.Config{
		DatingBotChatID:         standalone.DefaultDatingBotChatID,
		DatingBotUsername:       standalone.DefaultDatingBotUsername,
		DatingModel:             standalone.DefaultDatingModel,
		DatingPrompt:            standalone.DefaultDatingPrompt,
		DatingActionDelay:       standalone.DefaultDatingActionDelay,
		DatingJitterDelay:       standalone.DefaultDatingJitterMax,
		DatingTemperature:       standalone.DefaultDatingTemperature,
		DatingReplyAuditLogPath: filepath.Join(t.TempDir(), "logs", "replies.jsonl"),
	}

	h := NewHandler(cfg, nil, nil)

	if h.replyAudit == nil {
		t.Fatal("replyAudit = nil, want initialized logger")
	}
}

func TestNewHandlerReplyAuditDisabledWhenPathEmpty(t *testing.T) {
	cfg := &standalone.Config{
		DatingBotChatID:   standalone.DefaultDatingBotChatID,
		DatingBotUsername: standalone.DefaultDatingBotUsername,
		DatingModel:       standalone.DefaultDatingModel,
		DatingPrompt:      standalone.DefaultDatingPrompt,
		DatingActionDelay: standalone.DefaultDatingActionDelay,
		DatingJitterDelay: standalone.DefaultDatingJitterMax,
		DatingTemperature: standalone.DefaultDatingTemperature,
	}

	h := NewHandler(cfg, nil, nil)

	if h.replyAudit != nil {
		t.Fatal("replyAudit != nil, want nil when path is empty")
	}
}

func TestNewHandlerReplyAuditDisabledOnInvalidPath(t *testing.T) {
	cfg := &standalone.Config{
		DatingBotChatID:         standalone.DefaultDatingBotChatID,
		DatingBotUsername:       standalone.DefaultDatingBotUsername,
		DatingModel:             standalone.DefaultDatingModel,
		DatingPrompt:            standalone.DefaultDatingPrompt,
		DatingActionDelay:       standalone.DefaultDatingActionDelay,
		DatingJitterDelay:       standalone.DefaultDatingJitterMax,
		DatingTemperature:       standalone.DefaultDatingTemperature,
		DatingReplyAuditLogPath: t.TempDir(),
	}

	h := NewHandler(cfg, nil, nil)

	if h.replyAudit != nil {
		t.Fatal("replyAudit != nil, want nil when path points to directory")
	}
}

func TestNewHandlerKeepsConfiguredTimingValues(t *testing.T) {
	cfg := &standalone.Config{
		DatingBotChatID:         99,
		DatingBotUsername:       "bot",
		DatingModel:             "model",
		DatingPrompt:            "prompt",
		DatingActionDelay:       2 * time.Second,
		DatingJitterDelay:       500 * time.Millisecond,
		DatingTemperature:       0.8,
		DatingReplyAuditLogPath: "",
	}

	h := NewHandler(cfg, nil, nil)

	if h.actionDelay != 2*time.Second {
		t.Fatalf("actionDelay = %v, want 2s", h.actionDelay)
	}
	if h.jitterDelay != 500*time.Millisecond {
		t.Fatalf("jitterDelay = %v, want 500ms", h.jitterDelay)
	}
}
