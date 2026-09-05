package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func metadataResponse(body string, status int) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestResolveDirectCatalog(t *testing.T) {
	for _, tc := range []struct{ name, provider, npm, override, want string }{
		{"provider default", "opencode", "", "", "chat_completions"},
		{"responses", "opencode", "@ai-sdk/openai", "auto", "responses"},
		{"messages", "opencode-go", "@ai-sdk/anthropic", "", "anthropic"},
		{"explicit matching", "opencode-go", "@ai-sdk/openai-compatible", "chat_completions", "chat_completions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := "https://opencode.ai/zen/v1"
			if tc.provider == "opencode-go" {
				base = "https://opencode.ai/zen/go/v1"
			}
			calls := 0
			httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "GET" {
					t.Fatal("inference during resolution")
				}
				if r.URL.String() == base+"/models" {
					if r.Header.Get("Authorization") != "Bearer secret" {
						t.Fatal("live model auth missing")
					}
					return metadataResponse(`{"data":[{"id":"arbitrary-model"}]}`, 200), nil
				}
				if r.URL.String() != catalogURL || r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "" {
					t.Fatal("catalog credentials or endpoint changed")
				}
				return metadataResponse(fmt.Sprintf(`{%q:{"npm":"@ai-sdk/openai-compatible","api":"https://do-not-use.invalid/v1","models":{"arbitrary-model":{"id":"arbitrary-model","provider":{"npm":%q,"api":"https://also-do-not-use.invalid"},"modalities":{"input":["text","image"]},"temperature":false}}}}`, tc.provider, tc.npm), 200), nil
			})}
			c, model, err := resolveClient(context.Background(), NewClient("secret", base), tc.provider+"/arbitrary-model", tc.override, catalogURL, httpClient)
			if err != nil {
				t.Fatal(err)
			}
			if c.mode != tc.want || c.baseURL != base || model != "arbitrary-model" || !c.omitTemperature || calls != 2 {
				t.Fatalf("wrong routing: mode=%s base=%s model=%s calls=%d", c.mode, c.baseURL, model, calls)
			}
			conflicting := "responses"
			if tc.want == conflicting {
				conflicting = "anthropic"
			}
			if _, _, err := resolveClient(context.Background(), NewClient("secret", base), model, conflicting, catalogURL, httpClient); err == nil || !strings.Contains(err.Error(), "conflicts") {
				t.Fatalf("direct catalog protocol overridden: %v", err)
			}
		})
	}
}

func TestResolveStartupFailures(t *testing.T) {
	for _, tc := range []struct {
		name, live, catalog, model, want string
		status                           int
	}{
		{name: "missing live id", live: `{"data":[]}`, want: "not listed"},
		{name: "live HTTP error", status: 503, want: "HTTP 503"},
		{name: "malformed live", live: `{`, want: "model validation"},
		{name: "missing catalog id", catalog: `{}`, want: "missing"},
		{name: "malformed catalog", catalog: `{`, want: "protocol catalog"},
		{name: "unsupported protocol", catalog: `{"opencode":{"npm":"@ai-sdk/google","models":{"m":{"id":"m","modalities":{"input":["image"]}}}}}`, want: "unsupported catalog protocol"},
		{name: "no vision", catalog: `{"opencode":{"npm":"@ai-sdk/openai-compatible","models":{"m":{"id":"m","modalities":{"input":["text"]}}}}}`, want: "image input"},
		{name: "no schema", catalog: `{"opencode":{"npm":"@ai-sdk/openai-compatible","models":{"m":{"id":"m","modalities":{"input":["image"]},"structured_output":false}}}}`, want: "structured output"},
		{name: "wrong prefix", model: "opencode-go/m", want: "prefix does not match"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/models") {
					status, body := tc.status, tc.live
					if status == 0 {
						status = 200
					}
					if body == "" {
						body = `{"data":[{"id":"m"}]}`
					}
					return metadataResponse(body, status), nil
				}
				return metadataResponse(tc.catalog, 200), nil
			})}
			model := tc.model
			if model == "" {
				model = "m"
			}
			c, _, err := resolveClient(context.Background(), NewClient("secret", "https://opencode.ai/zen/v1"), model, "", catalogURL, httpClient)
			if err == nil || !strings.Contains(err.Error(), tc.want) || c != nil {
				t.Fatalf("want startup failure %q, got %v", tc.want, err)
			}
		})
	}
}

func TestResolveCompatibleDoesNotFetch(t *testing.T) {
	for _, base := range []string{"", "https://openrouter.ai/api/v1", "http://localhost:20128/v1", "https://custom.example/v1"} {
		for _, requestedModel := range []string{"provider/model", "opencode/model", "opencode-go/model"} {
			c, model, err := resolveClient(context.Background(), NewClient("key", base), requestedModel, "", catalogURL, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected metadata request"); return nil, nil })})
			if err != nil || c.mode != "" || model != requestedModel {
				t.Fatalf("compatible passthrough changed for %q at %q: model=%q error=%v", requestedModel, base, model, err)
			}
		}
	}
}

func TestMetadataBounds(t *testing.T) {
	t.Run("size", func(t *testing.T) {
		h := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return metadataResponse(strings.Repeat(" ", metadataLimit+1), 200), nil
		})}
		_, _, err := resolveClient(context.Background(), NewClient("key", "https://opencode.ai/zen/v1"), "m", "", catalogURL, h)
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("size bound: %v", err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		h := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })}
		_, _, err := resolveClient(ctx, NewClient("key", "https://opencode.ai/zen/v1"), "m", "", catalogURL, h)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("cancellation lost: %v", err)
		}
	})
}
