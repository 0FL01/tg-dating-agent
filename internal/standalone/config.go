// Package standalone provides config loading and auth/bootstrap helpers for the
// standalone Dating Agent application. It isolates standalone-specific wiring
// from the existing multi-skill runtime.
package standalone

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all configuration needed for the standalone Dating Agent.
// It includes Telegram auth/session, OpenRouter, and full Dating settings.
type Config struct {
	// Telegram credentials and session
	TGAppID       int32
	TGAppHash     string
	SessionPath   string // Path to session file (fallback if StringSession not set)
	StringSession string // Base64-encoded session string (preferred for Docker)

	// OpenAI-compatible API. OpenRouterAPIKey is retained for legacy callers.
	LLMAPIKey        string
	LLMBaseURL       string
	OpenRouterAPIKey string
	OpenRouterModel  string // Model for LLM requests

	// Dating bot settings (full set as used by internal/skills/dating)
	DatingBotChatID           int64
	DatingBotUsername         string
	DatingModel               string
	DatingPrompt              string
	DatingMBTIPrompt          string
	DatingMBTIAllowlist       []string
	DatingActionDelay         time.Duration
	DatingJitterDelay         time.Duration
	DatingTemperature         float64
	DatingSkipLowQuality      bool
	DatingMinBioLength        int
	DatingReplyAuditLogPath   string
	DatingMatchWebhookURL     string
	DatingMatchWebhookToken   string
	DatingMatchWebhookTimeout time.Duration
	DatingInstanceName        string
	DatingProfileDedupTTL     time.Duration
	DatingR2Enabled           bool
	DatingR2Bucket            string
	DatingR2Endpoint          string
	DatingR2Region            string
	DatingR2AccessKeyID       string
	DatingR2SecretAccessKey   string
}

// Default values for Dating configuration.
const (
	DefaultLLMBaseURL                      = "https://openrouter.ai/api/v1"
	DefaultDatingBotChatID           int64 = 1234060895
	DefaultDatingBotUsername               = "leomatchbot"
	DefaultDatingModel                     = "google/gemini-2.5-flash-lite-preview-06-2025"
	DefaultDatingActionDelay               = 15 * time.Second
	DefaultDatingJitterMax                 = 5 * time.Second
	DefaultDatingTemperature               = 0.7
	DefaultDatingMinBioLength              = 50
	DefaultDatingReplyAuditLogPath         = "/app/logs/replies.jsonl"
	DefaultDatingMatchWebhookTimeout       = 5 * time.Second
	DefaultDatingProfileDedupTTL           = 72 * time.Hour
	DefaultDatingR2Region                  = "auto"
	DefaultOpenRouterModel                 = "google/gemini-2.5-flash"
	DefaultSessionPath                     = "session.dat"
)

// DefaultDatingPrompt is the system prompt for generating dating messages.
const DefaultDatingPrompt = `Ты - помощник для знакомств. Проанализируй анкету (фото и описание) и напиши короткое, дружелюбное первое сообщение для знакомства.

ПРАВИЛА:
1. Сообщение на русском языке, 1-3 предложения
2. Обращай внимание на детали из анкеты (хобби, интересы, возраст)
3. Задай открытый вопрос или прокомментируй что-то конкретное из анкеты
4. Тон: дружелюбный, с юмором, не навязчивый
5. НЕ используй шаблонные фразы типа "Привет, как дела?"
6. НЕ комментируй внешность напрямую

Ответь ТОЛЬКО текстом сообщения, без пояснений.`

// DefaultDatingMBTIPrompt is the system prompt for MBTI analysis from profile data.
const DefaultDatingMBTIPrompt = `Ты - аналитик психотипов MBTI. На основе фото и текста анкеты оцени наиболее вероятный MBTI тип.

ПРАВИЛА:
1. Используй только 16 валидных MBTI типов (например, INTJ, ENFP)
2. Если уверенность низкая, все равно выбери наиболее вероятный тип
3. Не добавляй пояснений, markdown или лишнего текста

Ответь ТОЛЬКО MBTI типом в формате из 4 заглавных букв.`

// defaultDatingMBTIAllowlist is the default list of allowed MBTI types.
var defaultDatingMBTIAllowlist = []string{"INTJ", "INFJ", "ENTJ", "ENFJ"}

// Load reads configuration from environment variables and returns a standalone Config.
// Required: TG_APP_ID, TG_APP_HASH, LLM_API_KEY (or legacy OPENROUTER_API_KEY).
// Session: TG_STRING_SESSION takes precedence over SESSION_PATH.
func Load() (*Config, error) {
	// Required: Telegram App ID
	appIDStr := os.Getenv("TG_APP_ID")
	if appIDStr == "" {
		return nil, fmt.Errorf("TG_APP_ID is required")
	}
	appID, err := strconv.ParseInt(appIDStr, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("TG_APP_ID must be a valid integer: %w", err)
	}

	// Required: Telegram App Hash
	appHash := os.Getenv("TG_APP_APP_HASH")
	if appHash == "" {
		appHash = os.Getenv("TG_APP_HASH")
	}
	if appHash == "" {
		return nil, fmt.Errorf("TG_APP_HASH is required")
	}

	// Session: StringSession preferred over SessionPath
	sessionPath := os.Getenv("SESSION_PATH")
	if sessionPath == "" {
		sessionPath = DefaultSessionPath
	}
	stringSession := os.Getenv("TG_STRING_SESSION")

	baseURL := strings.TrimSpace(os.Getenv("LLM_BASE_URL"))
	apiKey := strings.TrimSpace(os.Getenv("LLM_API_KEY"))
	if baseURL == "" {
		baseURL = DefaultLLMBaseURL
		if apiKey == "" {
			apiKey = os.Getenv("OPENROUTER_API_KEY")
		}
	}
	if apiKey == "" {
		return nil, fmt.Errorf("LLM_API_KEY is required (OPENROUTER_API_KEY is supported only without LLM_BASE_URL)")
	}
	parsedURL, err := url.Parse(baseURL)
	if err != nil || parsedURL.Hostname() == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.ForceQuery || parsedURL.Fragment != "" || strings.Contains(baseURL, "#") {
		return nil, fmt.Errorf("LLM_BASE_URL must be an absolute HTTP(S) URL without credentials, query or fragment")
	}
	baseURL = strings.TrimRight(baseURL, "/")

	// Optional: OpenRouter Model
	model := os.Getenv("OPENROUTER_MODEL")
	if model == "" {
		model = DefaultOpenRouterModel
	}

	// Dating bot settings are hardcoded for Leomatch (@leomatchbot)
	// Chat ID: 1234060895, Username: leomatchbot

	// Dating: Model
	datingModel := os.Getenv("DATING_MODEL")
	if datingModel == "" {
		datingModel = DefaultDatingModel
	}

	// Dating: Prompt
	datingPrompt := os.Getenv("DATING_PROMPT")
	if datingPrompt == "" {
		datingPrompt = DefaultDatingPrompt
	}

	// Dating: MBTI Prompt
	datingMBTIPrompt := os.Getenv("DATING_MBTI_PROMPT")
	if datingMBTIPrompt == "" {
		datingMBTIPrompt = DefaultDatingMBTIPrompt
	}

	// Dating: MBTI Allowlist
	datingMBTIAllowlist := append([]string(nil), defaultDatingMBTIAllowlist...)
	if v := os.Getenv("DATING_MBTI_ALLOWLIST"); v != "" {
		parsed := make([]string, 0)
		for _, item := range strings.Split(v, ",") {
			normalized := strings.ToUpper(strings.TrimSpace(item))
			if normalized != "" {
				parsed = append(parsed, normalized)
			}
		}
		if len(parsed) > 0 {
			datingMBTIAllowlist = parsed
		}
	}

	// Dating: Action Delay
	datingActionDelay := DefaultDatingActionDelay
	if v := os.Getenv("DATING_ACTION_DELAY"); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil {
			datingActionDelay = parsed
		}
	}

	// Dating: Jitter Delay
	datingJitterDelay := DefaultDatingJitterMax
	if v := os.Getenv("DATING_JITTER_DELAY"); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil {
			datingJitterDelay = parsed
		}
	}

	// Dating: Temperature
	datingTemperature := DefaultDatingTemperature
	if v := os.Getenv("DATING_TEMPERATURE"); v != "" {
		if parsed, err := strconv.ParseFloat(v, 64); err == nil {
			datingTemperature = parsed
		}
	}

	// Dating: Skip Low Quality
	datingSkipLowQuality := strings.ToLower(os.Getenv("DATING_SKIP_LOW_QUALITY")) == "true"

	// Dating: Min Bio Length
	datingMinBioLength := DefaultDatingMinBioLength
	if v := os.Getenv("DATING_MIN_BIO_LENGTH"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			datingMinBioLength = parsed
		}
	}

	// Dating: Reply Audit Log Path
	datingReplyAuditLogPath, ok := os.LookupEnv("DATING_REPLY_AUDIT_LOG_PATH")
	if !ok {
		datingReplyAuditLogPath = DefaultDatingReplyAuditLogPath
	}

	// Dating: Reciprocal-like webhook integration
	datingMatchWebhookURL, _ := os.LookupEnv("DATING_MATCH_WEBHOOK_URL")
	datingMatchWebhookToken, _ := os.LookupEnv("DATING_MATCH_WEBHOOK_TOKEN")
	datingInstanceName := strings.TrimSpace(os.Getenv("DATING_INSTANCE_NAME"))

	datingMatchWebhookTimeout := DefaultDatingMatchWebhookTimeout
	if v := os.Getenv("DATING_MATCH_WEBHOOK_TIMEOUT"); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil {
			datingMatchWebhookTimeout = parsed
		}
	}

	datingProfileDedupTTL := DefaultDatingProfileDedupTTL
	if v := strings.TrimSpace(os.Getenv("DATING_PROFILE_DEDUP_TTL")); v != "" {
		parsed, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("DATING_PROFILE_DEDUP_TTL must be a valid duration: %w", err)
		}
		if parsed <= 0 {
			return nil, fmt.Errorf("DATING_PROFILE_DEDUP_TTL must be positive")
		}
		datingProfileDedupTTL = parsed
	}

	datingR2Bucket := strings.TrimSpace(os.Getenv("DATING_R2_BUCKET"))
	datingR2Endpoint := strings.TrimSpace(os.Getenv("DATING_R2_ENDPOINT"))
	datingR2Region := strings.TrimSpace(os.Getenv("DATING_R2_REGION"))
	if datingR2Region == "" {
		datingR2Region = DefaultDatingR2Region
	}
	datingR2AccessKeyID := strings.TrimSpace(os.Getenv("DATING_R2_ACCESS_KEY_ID"))
	datingR2SecretAccessKey := strings.TrimSpace(os.Getenv("DATING_R2_SECRET_ACCESS_KEY"))

	datingR2Enabled := datingR2Bucket != "" || datingR2Endpoint != "" || datingR2AccessKeyID != "" || datingR2SecretAccessKey != ""
	if datingR2Enabled {
		if datingR2Bucket == "" {
			return nil, fmt.Errorf("DATING_R2_BUCKET is required when R2 config is enabled")
		}
		if datingR2Endpoint == "" {
			return nil, fmt.Errorf("DATING_R2_ENDPOINT is required when R2 config is enabled")
		}
		if datingR2AccessKeyID == "" {
			return nil, fmt.Errorf("DATING_R2_ACCESS_KEY_ID is required when R2 config is enabled")
		}
		if datingR2SecretAccessKey == "" {
			return nil, fmt.Errorf("DATING_R2_SECRET_ACCESS_KEY is required when R2 config is enabled")
		}
		if datingInstanceName == "" {
			return nil, fmt.Errorf("DATING_INSTANCE_NAME is required when R2 config is enabled")
		}
	}

	return &Config{
		TGAppID:                   int32(appID),
		TGAppHash:                 appHash,
		SessionPath:               sessionPath,
		StringSession:             stringSession,
		OpenRouterAPIKey:          apiKey,
		LLMAPIKey:                 apiKey,
		LLMBaseURL:                baseURL,
		OpenRouterModel:           model,
		DatingBotChatID:           DefaultDatingBotChatID,
		DatingBotUsername:         DefaultDatingBotUsername,
		DatingModel:               datingModel,
		DatingPrompt:              datingPrompt,
		DatingMBTIPrompt:          datingMBTIPrompt,
		DatingMBTIAllowlist:       datingMBTIAllowlist,
		DatingActionDelay:         datingActionDelay,
		DatingJitterDelay:         datingJitterDelay,
		DatingTemperature:         datingTemperature,
		DatingSkipLowQuality:      datingSkipLowQuality,
		DatingMinBioLength:        datingMinBioLength,
		DatingReplyAuditLogPath:   datingReplyAuditLogPath,
		DatingMatchWebhookURL:     datingMatchWebhookURL,
		DatingMatchWebhookToken:   datingMatchWebhookToken,
		DatingMatchWebhookTimeout: datingMatchWebhookTimeout,
		DatingInstanceName:        datingInstanceName,
		DatingProfileDedupTTL:     datingProfileDedupTTL,
		DatingR2Enabled:           datingR2Enabled,
		DatingR2Bucket:            datingR2Bucket,
		DatingR2Endpoint:          datingR2Endpoint,
		DatingR2Region:            datingR2Region,
		DatingR2AccessKeyID:       datingR2AccessKeyID,
		DatingR2SecretAccessKey:   datingR2SecretAccessKey,
	}, nil
}

// SessionSource returns which session source is active: "string" or "file".
// StringSession takes precedence over SessionPath per the existing behavior.
func (c *Config) SessionSource() string {
	if c.StringSession != "" {
		return "string"
	}
	return "file"
}
