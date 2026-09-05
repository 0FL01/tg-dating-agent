package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/0FL01/tg-dating-agent/internal/tghelper"
	"github.com/revrost/go-openrouter"
)

// The existing SDK only supports chat completions. These non-streaming adapters
// use final text blocks only, never reasoning/thinking or tool arguments.
func (c *Client) createNativeCompletion(ctx context.Context, model string, messages []openrouter.ChatCompletionMessage, temperature float64, format *openrouter.ChatCompletionResponseFormat) (string, error) {
	var system []string
	var input []any
	for _, message := range messages {
		if message.Role == openrouter.ChatMessageRoleSystem {
			system = append(system, message.Content.Text)
			continue
		}
		parts := make([]any, 0, len(message.Content.Multi))
		for _, part := range message.Content.Multi {
			switch part.Type {
			case openrouter.ChatMessagePartTypeText:
				typ := "input_text"
				if c.mode == "anthropic" {
					typ = "text"
				}
				parts = append(parts, map[string]any{"type": typ, "text": part.Text})
			case openrouter.ChatMessagePartTypeImageURL:
				if part.ImageURL == nil {
					return "", fmt.Errorf("missing image URL")
				}
				image := part.ImageURL.URL
				if c.mode == "responses" {
					parts = append(parts, map[string]any{"type": "input_image", "image_url": image, "detail": "auto"})
				} else {
					source := map[string]any{"type": "url", "url": image}
					if strings.HasPrefix(image, "data:") {
						header, data, ok := strings.Cut(strings.TrimPrefix(image, "data:"), ";base64,")
						if !ok || !strings.HasPrefix(header, "image/") || data == "" {
							return "", fmt.Errorf("invalid image data URL")
						}
						source = map[string]any{"type": "base64", "media_type": header, "data": data}
					}
					parts = append(parts, map[string]any{"type": "image", "source": source})
				}
			default:
				return "", fmt.Errorf("media type %q is unsupported by %s transport", part.Type, c.mode)
			}
		}
		input = append(input, map[string]any{"role": message.Role, "content": parts})
	}
	payload := map[string]any{"model": model, "stream": false}
	if !c.omitTemperature {
		payload["temperature"] = temperature
	}
	path := "/responses"
	if c.mode == "responses" {
		payload["instructions"] = strings.Join(system, "\n\n")
		payload["input"] = input
		payload["store"] = false
		if format != nil {
			payload["text"] = map[string]any{"format": map[string]any{"type": "json_schema", "name": format.JSONSchema.Name, "strict": true, "schema": format.JSONSchema.Schema}}
		}
	} else {
		path = "/messages"
		payload["system"] = strings.Join(system, "\n\n")
		payload["messages"] = input
		payload["max_tokens"] = 4096
		if format != nil {
			// Anthropic does not support string length constraints in JSON schemas.
			// Retain the limit in the prompt and enforce it in ParseDecision.
			encoded, err := json.Marshal(format.JSONSchema.Schema)
			if err != nil {
				return "", err
			}
			var schema map[string]any
			if err := json.Unmarshal(encoded, &schema); err != nil {
				return "", err
			}
			if properties, ok := schema["properties"].(map[string]any); ok {
				if message, ok := properties["message"].(map[string]any); ok {
					delete(message, "maxLength")
				}
			}
			payload["output_config"] = map[string]any{"format": map[string]any{"type": "json_schema", "schema": schema}}
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	opts := tghelper.DefaultRetryOptions()
	opts.OnRetry = func(attempt int, err error, delay time.Duration) {
		log.Printf("[llm] retry %d: %v (sleep %s)", attempt, err, delay)
	}
	return tghelper.DoRetry(ctx, func(ctx context.Context) (string, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		if c.mode == "anthropic" {
			req.Header.Set("x-api-key", c.apiKey)
			req.Header.Set("anthropic-version", "2023-06-01")
		} else {
			req.Header.Set("Authorization", "Bearer "+c.apiKey)
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			err := fmt.Errorf("%s API returned HTTP %d (structured output required; no fallback)", c.mode, resp.StatusCode)
			if resp.StatusCode == 429 || resp.StatusCode >= 500 {
				return "", tghelper.MarkRetryable(err)
			}
			return "", err
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
		if err != nil {
			return "", err
		}
		if len(data) > 4<<20 {
			return "", fmt.Errorf("LLM response too large")
		}
		return parseNativeOutput(c.mode, data)
	}, nil, opts)
}

func parseNativeOutput(mode string, data []byte) (string, error) {
	type block struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	var response struct {
		Type       string          `json:"type"`
		Role       string          `json:"role"`
		Status     string          `json:"status"`
		StopReason string          `json:"stop_reason"`
		Error      json.RawMessage `json:"error"`
		Content    []block         `json:"content"`
		Output     []struct {
			Type    string  `json:"type"`
			Role    string  `json:"role"`
			Status  string  `json:"status"`
			Content []block `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return "", fmt.Errorf("invalid %s response: %w", mode, err)
	}
	if len(response.Error) != 0 && string(response.Error) != "null" {
		return "", fmt.Errorf("%s response contains an API error", mode)
	}
	var text strings.Builder
	if mode == "responses" {
		if response.Status != "completed" {
			return "", fmt.Errorf("responses output is not completed: %s", response.Status)
		}
		for _, item := range response.Output {
			if item.Type != "message" || item.Role != "assistant" {
				continue
			}
			if item.Status != "completed" {
				return "", fmt.Errorf("incomplete response message")
			}
			for _, part := range item.Content {
				if part.Type == "refusal" {
					return "", fmt.Errorf("model refused structured output")
				}
				if part.Type == "output_text" {
					text.WriteString(part.Text)
				}
			}
		}
	} else {
		if response.Type != "message" || response.Role != "assistant" || response.StopReason != "end_turn" {
			return "", fmt.Errorf("anthropic output is not a completed assistant message: %s", response.StopReason)
		}
		for _, part := range response.Content {
			if part.Type == "text" {
				text.WriteString(part.Text)
			}
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		return "", fmt.Errorf("no final text in %s response", mode)
	}
	return text.String(), nil
}
