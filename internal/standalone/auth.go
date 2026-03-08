package standalone

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/amarnathcjd/gogram/telegram"
)

// Environment variable to allow non-interactive execution.
// Set AUTH_ALLOW_NONINTERACTIVE=true only if you explicitly want to run
// in CI or automated environments. Use with caution.
const allowNonInteractiveEnv = "AUTH_ALLOW_NONINTERACTIVE"

// IsInteractiveEnvironment checks if we're in a safe interactive environment
// for authentication. Returns false for CI or non-TTY environments.
// This matches the hardened auth behavior from cmd/auth/main.go.
func IsInteractiveEnvironment() bool {
	// Explicit override for automated environments
	if os.Getenv(allowNonInteractiveEnv) == "true" {
		return true
	}

	// Check for common CI environment variables
	ciIndicators := []string{"CI", "CONTINUOUS_INTEGRATION", "GITHUB_ACTIONS", "GITLAB_CI", "JENKINS_URL", "BUILDKITE"}
	for _, indicator := range ciIndicators {
		if os.Getenv(indicator) != "" {
			return false
		}
	}

	// Check if stdin is a terminal
	stat, err := os.Stdin.Stat()
	if err != nil {
		return false
	}

	// If stdin is not a character device, we're likely piped or automated
	return (stat.Mode() & os.ModeCharDevice) == os.ModeCharDevice
}

// ClientConfig holds parameters for building a Telegram client config.
type ClientConfig struct {
	AppID         int32
	AppHash       string
	StringSession string // Preferred for Docker
	SessionPath   string // Fallback if StringSession not set
}

// BuildTelegramClientConfig creates a telegram.ClientConfig with session precedence
// and compatible settings (ParseMode, FloodHandler) matching cmd/bot/main.go.
func BuildTelegramClientConfig(cfg ClientConfig) telegram.ClientConfig {
	clientCfg := telegram.ClientConfig{
		AppID:     cfg.AppID,
		AppHash:   cfg.AppHash,
		ParseMode: "Markdown",
		// FloodHandler handles FLOOD_WAIT errors by sleeping the exact time Telegram requests
		FloodHandler: func(err error) bool {
			var seconds int
			if _, scanErr := fmt.Sscanf(err.Error(), "FLOOD_WAIT_%d", &seconds); scanErr == nil {
				log.Printf("[telegram] Flood wait: sleeping %d seconds", seconds)
				time.Sleep(time.Duration(seconds) * time.Second)
				return true // retry after waiting
			}
			log.Printf("[telegram] Unhandled flood error: %v", err)
			return false
		},
	}

	// TG_STRING_SESSION takes precedence over SESSION_PATH
	if cfg.StringSession != "" {
		clientCfg.StringSession = cfg.StringSession
	} else {
		clientCfg.Session = cfg.SessionPath
	}

	return clientCfg
}

// AuthResult represents the result of an authorization check.
type AuthResult struct {
	Authorized bool
	Me         *telegram.UserObj
}

// ErrNotAuthorized is returned when the client is not authorized.
var ErrNotAuthorized = errors.New("not authorized")

// CheckAuthorization connects the client and checks if it's authorized.
// It treats AUTH_KEY_UNREGISTERED as unauthorized (not fatal), matching
// the behavior in cmd/bot/main.go and cmd/auth/main.go.
// This function calls client.Conn() before checking auth, as required.
func CheckAuthorization(client *telegram.Client) (AuthResult, error) {
	// Connect first (required before auth check)
	if _, err := client.Conn(); err != nil {
		return AuthResult{}, fmt.Errorf("failed to connect: %w", err)
	}

	// Check authorization
	authorized, err := client.IsAuthorized()
	if err != nil {
		// AUTH_KEY_UNREGISTERED means session is empty/invalid - treat as not authorized
		if strings.Contains(err.Error(), "AUTH_KEY_UNREGISTERED") {
			return AuthResult{Authorized: false}, nil
		}
		return AuthResult{}, fmt.Errorf("failed to check authorization: %w", err)
	}

	if !authorized {
		return AuthResult{Authorized: false}, nil
	}

	me, err := client.GetMe()
	if err != nil {
		// Log but don't fail - we can still be authorized without GetMe working
		log.Printf("[auth] Warning: failed to get user info: %v", err)
		return AuthResult{Authorized: true, Me: nil}, nil
	}

	return AuthResult{Authorized: true, Me: me}, nil
}

// BootstrapResult holds the result of the bootstrap process.
type BootstrapResult struct {
	Client     *telegram.Client
	Authorized bool
	Me         *telegram.UserObj
	SessionStr string // Exported session string (useful for Docker deployment)
}

// BootstrapClient creates a Telegram client, connects, and checks authorization.
// It does NOT perform interactive auth - that's the caller's responsibility.
// This preserves non-interactive safety semantics from slice 1.
func BootstrapClient(cfg *Config) (*BootstrapResult, error) {
	clientCfg := BuildTelegramClientConfig(ClientConfig{
		AppID:         cfg.TGAppID,
		AppHash:       cfg.TGAppHash,
		StringSession: cfg.StringSession,
		SessionPath:   cfg.SessionPath,
	})

	client, err := telegram.NewClient(clientCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create telegram client: %w", err)
	}

	authResult, err := CheckAuthorization(client)
	if err != nil {
		return nil, err
	}

	result := &BootstrapResult{
		Client:     client,
		Authorized: authResult.Authorized,
		Me:         authResult.Me,
	}

	// If authorized, export the session string for convenience
	if authResult.Authorized {
		result.SessionStr = client.ExportSession()
	}

	return result, nil
}

// EnsureAuthorized wraps BootstrapClient and returns an error if not authorized.
// Use this when the application requires an authorized session to proceed.
func EnsureAuthorized(cfg *Config) (*BootstrapResult, error) {
	result, err := BootstrapClient(cfg)
	if err != nil {
		return nil, err
	}

	if !result.Authorized {
		return nil, ErrNotAuthorized
	}

	return result, nil
}

// ExportSession exports the session string from an authorized client.
// Returns empty string if the client is nil or session cannot be exported.
// Never returns secrets in error messages.
func ExportSession(client *telegram.Client) string {
	if client == nil {
		return ""
	}
	// ExportSession may panic in some edge cases, catch it
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[auth] Session export panicked (this is non-fatal)")
		}
	}()

	return client.ExportSession()
}
