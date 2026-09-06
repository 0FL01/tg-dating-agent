package dating

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0FL01/tg-dating-agent/internal/llm"
	"github.com/0FL01/tg-dating-agent/internal/standalone"
)

func historyTestConfig(path string) *standalone.Config {
	return &standalone.Config{
		DatingBotChatID:         standalone.DefaultDatingBotChatID,
		DatingBotUsername:       standalone.DefaultDatingBotUsername,
		DatingModel:             standalone.DefaultDatingModel,
		DatingPrompt:            standalone.DefaultDatingPrompt,
		DatingActionDelay:       standalone.DefaultDatingActionDelay,
		DatingJitterDelay:       standalone.DefaultDatingJitterMax,
		DatingTemperature:       standalone.DefaultDatingTemperature,
		DatingReplyAuditLogPath: path,
	}
}

func TestProfileHistoryRoundTripAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replies.jsonl")
	first := NewHandler(historyTestConfig(path), nil, nil)
	decision := llm.Decision{Action: "send", Reason: "fit", Message: "привет"}
	first.appendReplyAudit("decision", decision, ProfileData{ProfileText: "Аня, 23", PhotoIdentifiers: []string{"10:20"}}, "")

	// The audit line must carry photo identifiers for key rebuilds.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &fields); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if fields["photo_identifiers"] == nil {
		t.Fatal("audit line misses photo_identifiers")
	}

	// A fresh handler simulates a restart with a new state machine.
	second := NewHandler(historyTestConfig(path), nil, nil)

	// Same content, different Telegram message IDs and text casing/spacing.
	key := buildProfileLLMCacheKey("  аня,   23 ", []string{"10:20"})
	got, ok := second.state.GetProfileLLMCache(key)
	if !ok {
		t.Fatal("restored decision not found for identical content")
	}
	if got.Message != "привет" || got.Action != "send" {
		t.Fatalf("restored decision = %+v, want original", got)
	}

	if _, ok := second.state.GetProfileLLMCache(buildProfileLLMCacheKey("Аня, 24", []string{"10:20"})); ok {
		t.Fatal("changed text must be a new profile")
	}
	if _, ok := second.state.GetProfileLLMCache(buildProfileLLMCacheKey("Аня, 23", []string{"30:40"})); ok {
		t.Fatal("changed photo must be a new profile")
	}
}

func TestProfileHistorySkipsUnusableLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "replies.jsonl")
	lines := []string{
		`not json`,
		`{"event":"error","action":"","reason":"","message":"","profile_text":"Аня, 23"}`,
		`{"event":"invalid_response","action":"","reason":"","message":"","profile_text":"Аня, 23"}`,
		`{"event":"decision","action":"send","reason":"x","message":"","profile_text":"Аня, 23"}`,
		`{"event":"decision","action":"skip","reason":"empty","message":"","profile_text":""}`,
		`{"event":"decision","action":"skip","reason":"no age","message":"","profile_text":"Бекка, 25","photo_identifiers":["1:2"]}`,
		"",
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	h := NewHandler(historyTestConfig(path), nil, nil)
	if _, ok := h.state.GetProfileLLMCache(buildProfileLLMCacheKey("Бекка, 25", []string{"1:2"})); !ok {
		t.Fatal("valid skip decision was not restored")
	}
	if _, ok := h.state.GetProfileLLMCache(buildProfileLLMCacheKey("Аня, 23", nil)); ok {
		t.Fatal("invalid decision must not be restored")
	}
	if _, ok := h.state.GetProfileLLMCache(buildProfileLLMCacheKey("", nil)); ok {
		t.Fatal("empty content must not be restored")
	}
}

func TestProfileHistoryMissingFileIsFreshStart(t *testing.T) {
	h := NewHandler(historyTestConfig(filepath.Join(t.TempDir(), "absent.jsonl")), nil, nil)
	if _, ok := h.state.GetProfileLLMCache(buildProfileLLMCacheKey("Аня, 23", []string{"1:2"})); ok {
		t.Fatal("missing audit file must restore nothing")
	}
}
