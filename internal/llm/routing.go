package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const catalogURL = "https://models.opencode.ai/api.json"
const metadataLimit = 20 << 20

func ValidateAPIMode(mode string) error {
	switch mode {
	case "", "auto", "chat_completions", "responses", "anthropic":
		return nil
	default:
		return fmt.Errorf("LLM_API_MODE must be auto, chat_completions, responses or anthropic")
	}
}

func directProvider(baseURL string) string {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || u.Scheme != "https" || u.Host != "opencode.ai" {
		return ""
	}
	switch u.Path {
	case "/zen/v1":
		return "opencode"
	case "/zen/go/v1":
		return "opencode-go"
	}
	return ""
}

// NewResolvedClient resolves once, before Telegram startup. Metadata never changes
// the configured inference endpoint and the public catalog never receives credentials.
func NewResolvedClient(ctx context.Context, key, baseURL, model, mode string) (*Client, string, error) {
	c := NewClient(key, baseURL)
	return resolveClient(ctx, c, model, mode, catalogURL, &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	})
}

func resolveClient(ctx context.Context, c *Client, model, mode, catalog string, httpClient *http.Client) (*Client, string, error) {
	if err := ValidateAPIMode(mode); err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(model) == "" {
		return nil, "", fmt.Errorf("DATING_MODEL must not be empty")
	}
	provider := directProvider(c.baseURL)
	if provider == "" {
		c.mode = mode
		return c, model, nil
	}
	model = strings.TrimPrefix(model, provider+"/")
	if strings.HasPrefix(model, "opencode/") || strings.HasPrefix(model, "opencode-go/") {
		return nil, "", fmt.Errorf("DATING_MODEL prefix does not match %s; use its raw model ID", provider)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	fetch := func(endpoint, key string, target any) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("metadata returned HTTP %d", resp.StatusCode)
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, metadataLimit+1))
		if err != nil {
			return err
		}
		if len(data) > metadataLimit {
			return fmt.Errorf("metadata exceeds %d bytes", metadataLimit)
		}
		return json.Unmarshal(data, target)
	}
	var live struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := fetch(c.baseURL+"/models", c.apiKey, &live); err != nil {
		return nil, "", fmt.Errorf("OpenCode model validation: %w", err)
	}
	found := false
	for _, entry := range live.Data {
		if entry.ID == model {
			found = true
			break
		}
	}
	if !found {
		return nil, "", fmt.Errorf("DATING_MODEL %q is not listed by %s/models", model, c.baseURL)
	}
	var providers map[string]struct {
		NPM    string `json:"npm"`
		Models map[string]struct {
			ID       string `json:"id"`
			Provider struct {
				NPM string `json:"npm"`
			} `json:"provider"`
			Modalities struct {
				Input []string `json:"input"`
			} `json:"modalities"`
			StructuredOutput *bool `json:"structured_output"`
			Temperature      *bool `json:"temperature"`
		} `json:"models"`
	}
	if err := fetch(catalog, "", &providers); err != nil {
		return nil, "", fmt.Errorf("OpenCode protocol catalog: %w", err)
	}
	p := providers[provider]
	m, ok := p.Models[model]
	if !ok || m.ID != model {
		return nil, "", fmt.Errorf("model %q missing from %s catalog", model, provider)
	}
	if !slices.Contains(m.Modalities.Input, "image") {
		return nil, "", fmt.Errorf("model %q does not advertise image input required for profiles", model)
	}
	if m.StructuredOutput != nil && !*m.StructuredOutput {
		return nil, "", fmt.Errorf("model %q does not support structured output", model)
	}
	npm := m.Provider.NPM
	if npm == "" {
		npm = p.NPM
	}
	var resolvedMode string
	switch npm {
	case "@ai-sdk/openai-compatible":
		resolvedMode = "chat_completions"
	case "@ai-sdk/openai":
		resolvedMode = "responses"
	case "@ai-sdk/anthropic":
		resolvedMode = "anthropic"
	default:
		return nil, "", fmt.Errorf("model %q uses unsupported catalog protocol %q", model, npm)
	}
	if mode != "" && mode != "auto" && mode != resolvedMode {
		return nil, "", fmt.Errorf("LLM_API_MODE %q conflicts with direct %s model protocol %q; overrides are for custom endpoints", mode, provider, resolvedMode)
	}
	c.mode = resolvedMode
	c.omitTemperature = m.Temperature != nil && !*m.Temperature
	return c, model, nil
}
