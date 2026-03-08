// Package standalone provides config loading and auth/bootstrap helpers for the
// standalone Dating Agent application. It isolates standalone-specific wiring
// from the existing multi-skill runtime.
package standalone

import (
	"fmt"
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

	// OpenRouter API
	OpenRouterAPIKey string
	OpenRouterModel  string // Model for LLM requests

	// Dating bot settings (full set as used by internal/skills/dating)
	DatingBotChatID      int64
	DatingBotUsername    string
	DatingModel          string
	DatingPrompt         string
	DatingMBTIPrompt     string
	DatingMBTIAllowlist  []string
	DatingActionDelay    time.Duration
	DatingTemperature    float64
	DatingSkipLowQuality bool
	DatingMinBioLength   int
}

// Default values for Dating configuration.
const (
	DefaultDatingBotChatID    int64 = 1234060895
	DefaultDatingBotUsername        = "leomatchbot"
	DefaultDatingModel              = "google/gemini-2.5-flash-lite-preview-06-2025"
	DefaultDatingActionDelay        = 3 * time.Second
	DefaultDatingTemperature        = 0.7
	DefaultDatingMinBioLength       = 50
	DefaultOpenRouterModel          = "google/gemini-2.5-flash"
	DefaultSessionPath              = "session.dat"
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
// Required: TG_APP_ID, TG_APP_HASH, OPENROUTER_API_KEY.
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

	// Required: OpenRouter API Key
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("OPENROUTER_API_KEY is required")
	}

	// Optional: OpenRouter Model
	model := os.Getenv("OPENROUTER_MODEL")
	if model == "" {
		model = DefaultOpenRouterModel
	}

	// Dating: Bot Chat ID
	datingBotChatID := DefaultDatingBotChatID
	if v := os.Getenv("DATING_BOT_CHAT_ID"); v != "" {
		if parsed, err := strconv.ParseInt(v, 10, 64); err == nil {
			datingBotChatID = parsed
		}
	}

	// Dating: Bot Username
	datingBotUsername := DefaultDatingBotUsername
	if v := os.Getenv("DATING_BOT_USERNAME"); v != "" {
		datingBotUsername = v
	}

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

	return &Config{
		TGAppID:              int32(appID),
		TGAppHash:            appHash,
		SessionPath:          sessionPath,
		StringSession:        stringSession,
		OpenRouterAPIKey:     apiKey,
		OpenRouterModel:      model,
		DatingBotChatID:      datingBotChatID,
		DatingBotUsername:    datingBotUsername,
		DatingModel:          datingModel,
		DatingPrompt:         datingPrompt,
		DatingMBTIPrompt:     datingMBTIPrompt,
		DatingMBTIAllowlist:  datingMBTIAllowlist,
		DatingActionDelay:    datingActionDelay,
		DatingTemperature:    datingTemperature,
		DatingSkipLowQuality: datingSkipLowQuality,
		DatingMinBioLength:   datingMinBioLength,
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
