package standalone

import (
	"testing"
	"time"
)

// setRequiredEnv sets required environment variables for testing.
func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TG_APP_ID", "12345")
	t.Setenv("TG_APP_HASH", "testhash")
	t.Setenv("OPENROUTER_API_KEY", "testkey")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("LLM_BASE_URL", "")
}

func TestLoadLLMEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name, baseURL, key, wantURL, wantKey string
		wantErr                              bool
	}{
		{name: "legacy", wantURL: DefaultLLMBaseURL, wantKey: "testkey"},
		{name: "new key default endpoint", key: "new-key", wantURL: DefaultLLMBaseURL, wantKey: "new-key"},
		{name: "custom", baseURL: " http://localhost:20128/prefix/v1/ ", key: "gateway-key", wantURL: "http://localhost:20128/prefix/v1", wantKey: "gateway-key"},
		{name: "no legacy key leakage", baseURL: "https://example.com/v1", wantErr: true},
		{name: "relative", baseURL: "/v1", key: "key", wantErr: true},
		{name: "scheme", baseURL: "ftp://example.com/v1", key: "key", wantErr: true},
		{name: "credentials", baseURL: "https://user:pass@example.com/v1", key: "key", wantErr: true},
		{name: "query", baseURL: "https://example.com/v1?key=secret", key: "key", wantErr: true},
		{name: "fragment", baseURL: "https://example.com/v1#fragment", key: "key", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("LLM_BASE_URL", tc.baseURL)
			t.Setenv("LLM_API_KEY", tc.key)
			cfg, err := Load()
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected invalid LLM configuration")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.LLMBaseURL != tc.wantURL || cfg.LLMAPIKey != tc.wantKey {
				t.Fatal("unexpected resolved LLM configuration")
			}
		})
	}
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
	// Removed runtime gates must not affect the decision prompt or config.
	t.Setenv("DATING_MBTI_PROMPT", "obsolete")
	t.Setenv("DATING_MBTI_ALLOWLIST", "INTJ")
	t.Setenv("DATING_SKIP_LOW_QUALITY", "true")
	t.Setenv("DATING_MIN_BIO_LENGTH", "99999")

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
	if cfg.DatingActionDelay != DefaultDatingActionDelay {
		t.Errorf("DatingActionDelay = %v, want %v", cfg.DatingActionDelay, DefaultDatingActionDelay)
	}
	if cfg.DatingTemperature != DefaultDatingTemperature {
		t.Errorf("DatingTemperature = %f, want %f", cfg.DatingTemperature, DefaultDatingTemperature)
	}
	if cfg.DatingReplyAuditLogPath != DefaultDatingReplyAuditLogPath {
		t.Errorf("DatingReplyAuditLogPath = %q, want %q", cfg.DatingReplyAuditLogPath, DefaultDatingReplyAuditLogPath)
	}
	if cfg.DatingMatchWebhookURL != "" {
		t.Errorf("DatingMatchWebhookURL = %q, want empty", cfg.DatingMatchWebhookURL)
	}
	if cfg.DatingMatchWebhookToken != "" {
		t.Errorf("DatingMatchWebhookToken = %q, want empty", cfg.DatingMatchWebhookToken)
	}
	if cfg.DatingMatchWebhookTimeout != DefaultDatingMatchWebhookTimeout {
		t.Errorf("DatingMatchWebhookTimeout = %v, want %v", cfg.DatingMatchWebhookTimeout, DefaultDatingMatchWebhookTimeout)
	}
	if cfg.DatingInstanceName != "" {
		t.Errorf("DatingInstanceName = %q, want empty", cfg.DatingInstanceName)
	}
	if cfg.DatingProfileDedupTTL != DefaultDatingProfileDedupTTL {
		t.Errorf("DatingProfileDedupTTL = %v, want %v", cfg.DatingProfileDedupTTL, DefaultDatingProfileDedupTTL)
	}
	if cfg.DatingR2Enabled {
		t.Errorf("DatingR2Enabled = %v, want false", cfg.DatingR2Enabled)
	}
	if cfg.DatingR2Bucket != "" {
		t.Errorf("DatingR2Bucket = %q, want empty", cfg.DatingR2Bucket)
	}
	if cfg.DatingR2Endpoint != "" {
		t.Errorf("DatingR2Endpoint = %q, want empty", cfg.DatingR2Endpoint)
	}
	if cfg.DatingR2Region != DefaultDatingR2Region {
		t.Errorf("DatingR2Region = %q, want %q", cfg.DatingR2Region, DefaultDatingR2Region)
	}
	if cfg.DatingR2AccessKeyID != "" {
		t.Errorf("DatingR2AccessKeyID = %q, want empty", cfg.DatingR2AccessKeyID)
	}
	if cfg.DatingR2SecretAccessKey != "" {
		t.Errorf("DatingR2SecretAccessKey = %q, want empty", cfg.DatingR2SecretAccessKey)
	}
}

func TestLoadDatingCustomValues(t *testing.T) {
	setRequiredEnv(t)

	t.Setenv("DATING_MODEL", "custom/model")
	t.Setenv("DATING_PROMPT", "custom prompt")
	t.Setenv("DATING_ACTION_DELAY", "5s")
	t.Setenv("DATING_TEMPERATURE", "0.9")
	t.Setenv("DATING_REPLY_AUDIT_LOG_PATH", "/tmp/replies-custom.jsonl")
	t.Setenv("DATING_MATCH_WEBHOOK_URL", "https://example.com/hook")
	t.Setenv("DATING_MATCH_WEBHOOK_TOKEN", "secret-token")
	t.Setenv("DATING_MATCH_WEBHOOK_TIMEOUT", "2s")
	t.Setenv("DATING_INSTANCE_NAME", "prod-a")
	t.Setenv("DATING_PROFILE_DEDUP_TTL", "24h")
	t.Setenv("DATING_R2_BUCKET", "profiles-cache")
	t.Setenv("DATING_R2_ENDPOINT", "https://example.r2.cloudflarestorage.com")
	t.Setenv("DATING_R2_REGION", "eu")
	t.Setenv("DATING_R2_ACCESS_KEY_ID", "AKIAR2")
	t.Setenv("DATING_R2_SECRET_ACCESS_KEY", "secret-r2")

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

	if cfg.DatingActionDelay != 5*time.Second {
		t.Errorf("DatingActionDelay = %v, want 5s", cfg.DatingActionDelay)
	}
	if cfg.DatingTemperature != 0.9 {
		t.Errorf("DatingTemperature = %f, want 0.9", cfg.DatingTemperature)
	}
	if cfg.DatingReplyAuditLogPath != "/tmp/replies-custom.jsonl" {
		t.Errorf("DatingReplyAuditLogPath = %q, want %q", cfg.DatingReplyAuditLogPath, "/tmp/replies-custom.jsonl")
	}
	if cfg.DatingMatchWebhookURL != "https://example.com/hook" {
		t.Errorf("DatingMatchWebhookURL = %q, want %q", cfg.DatingMatchWebhookURL, "https://example.com/hook")
	}
	if cfg.DatingMatchWebhookToken != "secret-token" {
		t.Errorf("DatingMatchWebhookToken = %q, want %q", cfg.DatingMatchWebhookToken, "secret-token")
	}
	if cfg.DatingMatchWebhookTimeout != 2*time.Second {
		t.Errorf("DatingMatchWebhookTimeout = %v, want 2s", cfg.DatingMatchWebhookTimeout)
	}
	if cfg.DatingInstanceName != "prod-a" {
		t.Errorf("DatingInstanceName = %q, want %q", cfg.DatingInstanceName, "prod-a")
	}
	if cfg.DatingProfileDedupTTL != 24*time.Hour {
		t.Errorf("DatingProfileDedupTTL = %v, want 24h", cfg.DatingProfileDedupTTL)
	}
	if !cfg.DatingR2Enabled {
		t.Errorf("DatingR2Enabled = %v, want true", cfg.DatingR2Enabled)
	}
	if cfg.DatingR2Bucket != "profiles-cache" {
		t.Errorf("DatingR2Bucket = %q, want %q", cfg.DatingR2Bucket, "profiles-cache")
	}
	if cfg.DatingR2Endpoint != "https://example.r2.cloudflarestorage.com" {
		t.Errorf("DatingR2Endpoint = %q, want %q", cfg.DatingR2Endpoint, "https://example.r2.cloudflarestorage.com")
	}
	if cfg.DatingR2Region != "eu" {
		t.Errorf("DatingR2Region = %q, want %q", cfg.DatingR2Region, "eu")
	}
	if cfg.DatingR2AccessKeyID != "AKIAR2" {
		t.Errorf("DatingR2AccessKeyID = %q, want %q", cfg.DatingR2AccessKeyID, "AKIAR2")
	}
	if cfg.DatingR2SecretAccessKey != "secret-r2" {
		t.Errorf("DatingR2SecretAccessKey = %q, want %q", cfg.DatingR2SecretAccessKey, "secret-r2")
	}
}

func TestLoadDatingProfileDedupTTLInvalid(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DATING_PROFILE_DEDUP_TTL", "bad")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error for invalid DATING_PROFILE_DEDUP_TTL")
	}
	if !contains(err.Error(), "DATING_PROFILE_DEDUP_TTL") {
		t.Errorf("error should mention DATING_PROFILE_DEDUP_TTL: %v", err)
	}
}

func TestLoadDatingR2RequiresInstanceName(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DATING_R2_BUCKET", "profiles-cache")
	t.Setenv("DATING_R2_ENDPOINT", "https://example.r2.cloudflarestorage.com")
	t.Setenv("DATING_R2_ACCESS_KEY_ID", "AKIAR2")
	t.Setenv("DATING_R2_SECRET_ACCESS_KEY", "secret-r2")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when DATING_INSTANCE_NAME is missing and R2 is enabled")
	}
	if !contains(err.Error(), "DATING_INSTANCE_NAME") {
		t.Errorf("error should mention DATING_INSTANCE_NAME: %v", err)
	}
}

func TestLoadDatingR2RequiresAllFields(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DATING_INSTANCE_NAME", "prod-a")
	t.Setenv("DATING_R2_BUCKET", "profiles-cache")
	t.Setenv("DATING_R2_ACCESS_KEY_ID", "AKIAR2")
	t.Setenv("DATING_R2_SECRET_ACCESS_KEY", "secret-r2")

	_, err := Load()
	if err == nil {
		t.Fatal("expected error when DATING_R2_ENDPOINT is missing and R2 is enabled")
	}
	if !contains(err.Error(), "DATING_R2_ENDPOINT") {
		t.Errorf("error should mention DATING_R2_ENDPOINT: %v", err)
	}
}

func TestLoadDatingReplyAuditLogPathExplicitEmpty(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DATING_REPLY_AUDIT_LOG_PATH", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.DatingReplyAuditLogPath != "" {
		t.Errorf("DatingReplyAuditLogPath = %q, want empty string", cfg.DatingReplyAuditLogPath)
	}
}

func TestLoadDatingMatchWebhookExplicitEmptyURL(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DATING_MATCH_WEBHOOK_URL", "")
	t.Setenv("DATING_MATCH_WEBHOOK_TOKEN", "secret-token")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.DatingMatchWebhookURL != "" {
		t.Errorf("DatingMatchWebhookURL = %q, want empty string", cfg.DatingMatchWebhookURL)
	}
	if cfg.DatingMatchWebhookToken != "secret-token" {
		t.Errorf("DatingMatchWebhookToken = %q, want %q", cfg.DatingMatchWebhookToken, "secret-token")
	}
}

func TestLoadDatingMatchWebhookTimeoutInvalidFallsBackToDefault(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DATING_MATCH_WEBHOOK_TIMEOUT", "not-a-duration")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.DatingMatchWebhookTimeout != DefaultDatingMatchWebhookTimeout {
		t.Errorf("DatingMatchWebhookTimeout = %v, want %v", cfg.DatingMatchWebhookTimeout, DefaultDatingMatchWebhookTimeout)
	}
}

func TestLoadDatingJitterDelayDefault(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.DatingJitterDelay != DefaultDatingJitterMax {
		t.Errorf("DatingJitterDelay = %v, want %v", cfg.DatingJitterDelay, DefaultDatingJitterMax)
	}
}

func TestLoadDatingJitterDelayCustom(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("DATING_JITTER_DELAY", "3s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.DatingJitterDelay != 3*time.Second {
		t.Errorf("DatingJitterDelay = %v, want 3s", cfg.DatingJitterDelay)
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
