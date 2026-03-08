package llm

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0FL01/tg-dating-agent/internal/tghelper"
	"github.com/revrost/go-openrouter"
)

var mimeTypes = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".mp3":  "audio/mpeg",
}

type Client struct {
	client               *openrouter.Client
	createChatCompletion func(context.Context, openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error)
	onMediaPartStart     func()
}

var _ MultimodalSummarizer = (*Client)(nil)

func NewClient(apiKey string) *Client {
	openRouterClient := openrouter.NewClient(apiKey)
	return &Client{
		client:               openRouterClient,
		createChatCompletion: openRouterClient.CreateChatCompletion,
	}
}

func (c *Client) SummarizeMultimodal(ctx context.Context, model, systemPrompt string, content MultimodalContent, temperature float64) (string, error) {
	parts, err := c.buildMultimodalParts(ctx, content, systemPrompt)
	if err != nil {
		return "", err
	}

	if len(parts) == 0 {
		return "", fmt.Errorf("no content provided for summarization")
	}

	messages := []openrouter.ChatCompletionMessage{
		openrouter.SystemMessage(systemPrompt),
		{
			Role: openrouter.ChatMessageRoleUser,
			Content: openrouter.Content{
				Multi: parts,
			},
		},
	}

	return c.createCompletion(ctx, model, messages, temperature)
}

func (c *Client) createCompletion(ctx context.Context, model string, messages []openrouter.ChatCompletionMessage, temperature float64) (string, error) {
	createChatCompletion := c.createChatCompletion
	if createChatCompletion == nil {
		if c.client == nil {
			return "", errors.New("openrouter client is not configured")
		}
		createChatCompletion = c.client.CreateChatCompletion
	}

	opts := tghelper.DefaultRetryOptions()
	opts.OnRetry = func(attempt int, err error, delay time.Duration) {
		log.Printf("[openrouter] retry %d: %v (sleep %s)", attempt, err, delay)
	}

	return tghelper.DoRetry(ctx, func(ctx context.Context) (string, error) {
		resp, err := createChatCompletion(ctx, openrouter.ChatCompletionRequest{
			Model:       model,
			Messages:    messages,
			Temperature: float32(temperature),
		})
		if err != nil {
			return "", c.wrapRetryableError(err)
		}
		if len(resp.Choices) == 0 {
			return "", tghelper.MarkRetryable(errors.New("no completion choices returned"))
		}
		return resp.Choices[0].Message.Content.Text, nil
	}, nil, opts)
}

func (c *Client) wrapRetryableError(err error) error {
	var apiErr *openrouter.APIError
	if errors.As(err, &apiErr) {
		if apiErr.HTTPStatusCode == 429 || apiErr.HTTPStatusCode >= 500 {
			return tghelper.MarkRetryable(err)
		}
	}
	return err
}

func fileToBase64DataURL(ctx context.Context, filePath string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	ext := strings.ToLower(filepath.Ext(filePath))
	mime, ok := mimeTypes[ext]
	if !ok {
		return "", fmt.Errorf("unsupported file type: %s", ext)
	}
	return fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(data)), nil
}

func (c *Client) buildMultimodalParts(ctx context.Context, content MultimodalContent, systemPrompt string) ([]openrouter.ChatMessagePart, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var parts []openrouter.ChatMessagePart

	if content.Text != "" {
		parts = append(parts, openrouter.ChatMessagePart{
			Type: openrouter.ChatMessagePartTypeText,
			Text: content.Text,
		})
	}

	for _, imgPath := range content.ImagePaths {
		if c.onMediaPartStart != nil {
			c.onMediaPartStart()
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		dataURL, err := fileToBase64DataURL(ctx, imgPath)
		if err != nil {
			return nil, fmt.Errorf("image encoding failed: %w", err)
		}
		parts = append(parts, openrouter.ChatMessagePart{
			Type:     openrouter.ChatMessagePartTypeImageURL,
			ImageURL: &openrouter.ChatMessageImageURL{URL: dataURL},
		})
	}

	if content.AudioPath != "" {
		if c.onMediaPartStart != nil {
			c.onMediaPartStart()
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		audioData, err := os.ReadFile(content.AudioPath)
		if err != nil {
			return nil, fmt.Errorf("audio read failed: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		parts = append(parts, openrouter.ChatMessagePart{
			Type: openrouter.ChatMessagePartTypeInputAudio,
			InputAudio: &openrouter.ChatMessageInputAudio{
				Data:   base64.StdEncoding.EncodeToString(audioData),
				Format: openrouter.AudioFormatMp3,
			},
		})
	}

	// Note: VideoPath is tracked for media presence detection but not processed here.
	// Video files require pre-processing (e.g., frame extraction via ffmpeg) before
	// being sent to the LLM. The caller is responsible for video handling.
	hasMedia := content.AudioPath != "" || content.VideoPath != "" || len(content.ImagePaths) > 0
	hasText := content.Text != ""
	if hasMedia && !hasText {
		parts = append(parts, openrouter.ChatMessagePart{
			Type: openrouter.ChatMessagePartTypeText,
			Text: systemPrompt,
		})
	}

	return parts, nil
}
