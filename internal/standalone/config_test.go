package standalone

import (
	"reflect"
	"testing"
	"time"
)

// setRequiredEnv sets required environment variables for testing.
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TG_APP_ID", "12345")
	t.Setenv("TG_APP_HASH", "testhash")
	t.Setenv("OPENROUTER_API_KEY", "testkey")
}

func TestLoadRequiredFields(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.TGAppID != 12345 {
		t.Errorf("TGAppID = %d, want 12345", cfg.TGAppID)
	}
	if cfg.TGAppHash != "testhash" {
		t.Errorf("TGAppHash = %s, want testhash", cfg.TGAppHash)
	}
	if cfg.OpenRouterAPIKey != "testkey" {
		t.Errorf("OpenRouterAPIKey = %s, want testkey", cfg.OpenRouterAPIKey)
	}
}

func TestLoadMissingTGAppID(t *testing.T) {
	t.Setenv("TG_APP_HASH", "testhash")
	t.Setenv("OPENROUTER_API_KEY", "testkey")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing TG_APP_ID")
	}
	if !contains(err.Error(), "TG_APP_ID") {
		t.Errorf("error message should mention TG_APP_ID: %v", err)
	}
}

func TestLoadMissingTGAppHash(t *testing.T) {
	t.Setenv("TG_APP_ID", "12345")
	t.Setenv("OPENROUTER_API_KEY", "testkey")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing TG_APP_HASH")
	}
}

func TestLoadMissingAPIKey(t *testing.T) {
	t.Setenv("TG_APP_ID", "12345")
	t.Setenv("TG_APP_HASH", "testhash")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing OPENROUTER_API_KEY")
	}
}

func TestLoadSessionPrecedence(t *testing.T) {
	setRequiredEnv(t)

	// Only SESSION_PATH set
	t.Setenv("SESSION_PATH", "/tmp/session.dat")
	t.Setenv("TG_STRING_SESSION", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.SessionPath != "/tmp/session.dat" {
		t.Errorf("SessionPath = %s, want /tmp/session.dat", cfg.SessionPath)
	}
	if cfg.StringSession != "" {
		t.Errorf("StringSession should be empty, got %s", cfg.StringSession)
	}
	if cfg.SessionSource() != "file" {
		t.Errorf("SessionSource() = %s, want file", cfg.SessionSource())
	}
}

func TestLoadStringSessionPrecedence(t *testing.T) {
	setRequiredEnv(t)

	// Both set - StringSession should take precedence
	t.Setenv("SESSION_PATH", "/tmp/session.dat")
	t.Setenv("TG_STRING_SESSION", "base64sessionstring")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.StringSession != "base64sessionstring" {
		t.Errorf("StringSession = %s, want base64sessionstring", cfg.StringSession)
	}
	if cfg.SessionSource() != "string" {
		t.Errorf("SessionSource() = %s, want string", cfg.SessionSource())
	}
}

func TestLoadDefaultSessionPath(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("SESSION_PATH", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.SessionPath != DefaultSessionPath {
		t.Errorf("SessionPath = %s, want default %s", cfg.SessionPath, DefaultSessionPath)
	}
}

func TestLoadDatingDefaults(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.DatingBotChatID != DefaultDatingBotChatID {
		t.Errorf("DatingBotChatID = %d, want %d", cfg.DatingBotChatID, DefaultDatingBotChatID)
	}
	if cfg.DatingModel != DefaultDatingModel {
		t.Errorf("DatingModel = %s, want %s", cfg.DatingModel, DefaultDatingModel)
	}
	if cfg.DatingPrompt != DefaultDatingPrompt {
		t.Errorf("DatingPrompt doesn't match default")
	}
	if cfg.DatingMBTIPrompt != DefaultDatingMBTIPrompt {
		t.Errorf("DatingMBTIPrompt doesn't match default")
	}
	if cfg.DatingActionDelay != DefaultDatingActionDelay {
		t.Errorf("DatingActionDelay = %v, want %v", cfg.DatingActionDelay, DefaultDatingActionDelay)
	}
	if cfg.DatingTemperature != DefaultDatingTemperature {
		t.Errorf("DatingTemperature = %f, want %f", cfg.DatingTemperature, DefaultDatingTemperature)
	}
	if cfg.DatingMinBioLength != DefaultDatingMinBioLength {
		t.Errorf("DatingMinBioLength = %d, want %d", cfg.DatingMinBioLength, DefaultDatingMinBioLength)
	}
	if cfg.DatingSkipLowQuality != false {
		t.Errorf("DatingSkipLowQuality = %v, want false", cfg.DatingSkipLowQuality)
	}
	if !reflect.DeepEqual(cfg.DatingMBTIAllowlist, defaultDatingMBTIAllowlist) {
		t.Errorf("DatingMBTIAllowlist = %v, want %v", cfg.DatingMBTIAllowlist, defaultDatingMBTIAllowlist)
	}
}

func TestLoadDatingCustomValues(t *testing.T) {
	setRequiredEnv(t)

	t.Setenv("DATING_MODEL", "custom/model")
	t.Setenv("DATING_PROMPT", "custom prompt")
	t.Setenv("DATING_MBTI_PROMPT", "custom mbti prompt")
	t.Setenv("DATING_MBTI_ALLOWLIST", "INTP,ENTP")
	t.Setenv("DATING_ACTION_DELAY", "5s")
	t.Setenv("DATING_TEMPERATURE", "0.9")
	t.Setenv("DATING_SKIP_LOW_QUALITY", "true")
	t.Setenv("DATING_MIN_BIO_LENGTH", "100")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// DatingBotChatID is hardcoded, not configurable via env
	if cfg.DatingBotChatID != DefaultDatingBotChatID {
		t.Errorf("DatingBotChatID = %d, want %d (hardcoded default)", cfg.DatingBotChatID, DefaultDatingBotChatID)
	}
	if cfg.DatingModel != "custom/model" {
		t.Errorf("DatingModel = %s, want custom/model", cfg.DatingModel)
	}
	if cfg.DatingPrompt != "custom prompt" {
		t.Errorf("DatingPrompt = %s, want 'custom prompt'", cfg.DatingPrompt)
	}
	if cfg.DatingMBTIPrompt != "custom mbti prompt" {
		t.Errorf("DatingMBTIPrompt = %s, want 'custom mbti prompt'", cfg.DatingMBTIPrompt)
	}

	expectedAllowlist := []string{"INTP", "ENTP"}
	if !reflect.DeepEqual(cfg.DatingMBTIAllowlist, expectedAllowlist) {
		t.Errorf("DatingMBTIAllowlist = %v, want %v", cfg.DatingMBTIAllowlist, expectedAllowlist)
	}

	if cfg.DatingActionDelay != 5*time.Second {
		t.Errorf("DatingActionDelay = %v, want 5s", cfg.DatingActionDelay)
	}
	if cfg.DatingTemperature != 0.9 {
		t.Errorf("DatingTemperature = %f, want 0.9", cfg.DatingTemperature)
	}
	if cfg.DatingSkipLowQuality != true {
		t.Errorf("DatingSkipLowQuality = %v, want true", cfg.DatingSkipLowQuality)
	}
	if cfg.DatingMinBioLength != 100 {
		t.Errorf("DatingMinBioLength = %d, want 100", cfg.DatingMinBioLength)
	}
}

func TestLoadDatingMBTIAllowlistNormalization(t *testing.T) {
	setRequiredEnv(t)

	// Test normalization of MBTI allowlist (lowercase -> uppercase, trimming)
	t.Setenv("DATING_MBTI_ALLOWLIST", " intj, enfp , , IsFj ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	expected := []string{"INTJ", "ENFP", "ISFJ"}
	if !reflect.DeepEqual(cfg.DatingMBTIAllowlist, expected) {
		t.Errorf("DatingMBTIAllowlist = %v, want %v", cfg.DatingMBTIAllowlist, expected)
	}
}

func TestLoadDatingMBTIAllowlistFallbackOnEmpty(t *testing.T) {
	setRequiredEnv(t)

	// Test fallback to defaults when only whitespace/commas provided
	t.Setenv("DATING_MBTI_ALLOWLIST", " ,   ,\t")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if !reflect.DeepEqual(cfg.DatingMBTIAllowlist, defaultDatingMBTIAllowlist) {
		t.Errorf("DatingMBTIAllowlist = %v, want default %v", cfg.DatingMBTIAllowlist, defaultDatingMBTIAllowlist)
	}
}

func TestLoadOpenRouterModelDefault(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.OpenRouterModel != DefaultOpenRouterModel {
		t.Errorf("OpenRouterModel = %s, want %s", cfg.OpenRouterModel, DefaultOpenRouterModel)
	}
}

func TestLoadOpenRouterModelCustom(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("OPENROUTER_MODEL", "openai/gpt-4")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.OpenRouterModel != "openai/gpt-4" {
		t.Errorf("OpenRouterModel = %s, want openai/gpt-4", cfg.OpenRouterModel)
	}
}

func TestLoadInvalidAppID(t *testing.T) {
	t.Setenv("TG_APP_ID", "not-a-number")
	t.Setenv("TG_APP_HASH", "testhash")
	t.Setenv("OPENROUTER_API_KEY", "testkey")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid TG_APP_ID")
	}
	if !contains(err.Error(), "TG_APP_ID") {
		t.Errorf("error should mention TG_APP_ID: %v", err)
	}
}

func TestLoadBackwardCompatibleAppHash(t *testing.T) {
	// Test that TG_APP_APP_HASH (old name) also works
	t.Setenv("TG_APP_ID", "12345")
	t.Setenv("TG_APP_APP_HASH", "oldstylehash")
	t.Setenv("OPENROUTER_API_KEY", "testkey")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.TGAppHash != "oldstylehash" {
		t.Errorf("TGAppHash = %s, want oldstylehash", cfg.TGAppHash)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
