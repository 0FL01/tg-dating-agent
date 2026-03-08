package llm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/revrost/go-openrouter"
)

func TestSummarizeMultimodal_ContextCanceledBeforeCall(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	client := &Client{
		createChatCompletion: func(ctx context.Context, req openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			calls.Add(1)
			return openrouter.ChatCompletionResponse{}, nil
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.SummarizeMultimodal(ctx, "model", "system", MultimodalContent{Text: "bio"}, 0.5)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled error, got %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("expected no completion calls when ctx canceled before call, got %d", calls.Load())
	}
}

func TestCreateCompletion_ContextValuePropagated(t *testing.T) {
	t.Parallel()

	type ctxKey string
	const key ctxKey = "trace"

	client := &Client{
		createChatCompletion: func(ctx context.Context, req openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			if got := ctx.Value(key); got != "value-123" {
				t.Fatalf("expected propagated context value, got %v", got)
			}
			return openrouter.ChatCompletionResponse{
				Choices: []openrouter.ChatCompletionChoice{
					{Message: openrouter.ChatCompletionMessage{Content: openrouter.Content{Text: "ok"}}},
				},
			}, nil
		},
	}

	ctx := context.WithValue(context.Background(), key, "value-123")
	text, err := client.createCompletion(ctx, "model", nil, 0.7)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if text != "ok" {
		t.Fatalf("expected text 'ok', got %q", text)
	}
}

func TestCreateCompletion_CancelStopsRetry(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &Client{
		createChatCompletion: func(ctx context.Context, req openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			if calls.Add(1) == 1 {
				cancel()
				return openrouter.ChatCompletionResponse{}, &openrouter.APIError{HTTPStatusCode: 500, Message: "temporary"}
			}
			t.Fatalf("unexpected retry after context cancellation")
			return openrouter.ChatCompletionResponse{}, nil
		},
	}

	_, err := client.createCompletion(ctx, "model", nil, 0.7)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected exactly one call before cancellation, got %d", calls.Load())
	}
}

func TestSummarizeMultimodal_ContextCanceledDuringMediaPreprocessing(t *testing.T) {
	t.Parallel()

	imgPath := filepath.Join(t.TempDir(), "avatar.jpg")
	if err := os.WriteFile(imgPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("write temp image: %v", err)
	}

	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client := &Client{
		onMediaPartStart: func() {
			cancel()
		},
		createChatCompletion: func(ctx context.Context, req openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
			calls.Add(1)
			return openrouter.ChatCompletionResponse{}, nil
		},
	}

	_, err := client.SummarizeMultimodal(ctx, "model", "system", MultimodalContent{ImagePaths: []string{imgPath}}, 0.5)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled error, got %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("expected no completion calls when canceled during preprocessing, got %d", calls.Load())
	}
}
