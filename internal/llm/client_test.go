package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/revrost/go-openrouter"
)

func TestClientCustomEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/zen/v1/chat/completions" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer custom-key" {
			t.Error("wrong authorization")
		}
		var req openrouter.ChatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if req.Model != "vision-model" || len(req.Messages) != 2 {
			t.Error("wrong model or messages")
			w.WriteHeader(400)
			return
		}
		parts := req.Messages[1].Content.Multi
		if len(parts) != 2 || parts[0].Text != "bio" || parts[1].ImageURL == nil || !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/jpeg;base64,") {
			t.Error("wrong multimodal content")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()
	image := filepath.Join(t.TempDir(), "photo.jpg")
	if err := os.WriteFile(image, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	client := NewClient("custom-key", server.URL+"/zen/v1/")
	text, err := client.SummarizeMultimodal(context.Background(), "vision-model", "system", MultimodalContent{Text: "bio", ImagePaths: []string{image}}, 0.7)
	if err != nil || text != "ok" {
		t.Fatalf("text = %q, error = %v", text, err)
	}
}

func TestGoHTTPClientHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "tg-dating-agent" || r.Header.Get("x-opencode-session") != "opaque-session" {
			t.Error("missing Go identification")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := &goHTTPClient{client: server.Client(), session: "opaque-session"}
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if req.Header.Get("x-opencode-session") != "" {
		t.Fatal("mutated caller headers")
	}
}

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
