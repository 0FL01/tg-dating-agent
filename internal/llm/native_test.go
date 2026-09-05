package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const finalDecision = `{"action":"send","reason":"shared hobby","message":"What are you reading?"}`

func nativeResponse(mode string) string {
	text, _ := json.Marshal(finalDecision)
	if mode == "responses" {
		return `{"status":"completed","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"private"}]},{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":` + string(text) + `}]}]}`
	}
	return `{"type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"private"},{"type":"text","text":` + string(text) + `}]}`
}

func TestNativeRetryKeepsInput(t *testing.T) {
	for _, mode := range []string{"responses", "anthropic"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			var calls int
			var first string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				body, _ := io.ReadAll(r.Body)
				if calls == 1 {
					first = string(body)
					status := 429
					if mode == "anthropic" {
						status = 503
					}
					w.WriteHeader(status)
					return
				}
				if string(body) != first {
					t.Error("retry changed model/prompt/schema/photos")
				}
				_, _ = w.Write([]byte(nativeResponse(mode)))
			}))
			defer server.Close()
			c, model, err := NewResolvedClient(context.Background(), "key", server.URL, "m", mode)
			if err != nil {
				t.Fatal(err)
			}
			text, err := c.DecideMultimodal(context.Background(), model, "original prompt", MultimodalContent{Text: "bio", ImageURLs: []string{"data:image/jpeg;base64,aW1hZ2U="}}, .7)
			if err != nil || text != finalDecision || calls != 2 {
				t.Fatalf("retry failed: calls=%d text=%q err=%v", calls, text, err)
			}
		})
	}
}

func TestNativeDecisionWireFormats(t *testing.T) {
	for _, mode := range []string{"responses", "anthropic"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				path := "/prefix/v1/responses"
				if mode == "anthropic" {
					path = "/prefix/v1/messages"
				}
				if r.Method != "POST" || r.URL.Path != path {
					t.Errorf("wrong inference endpoint: %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("x-opencode-session") != "stable-session" || r.Header.Get("User-Agent") != "tg-dating-agent" {
					t.Error("missing session/identification")
				}
				if mode == "anthropic" {
					if r.Header.Get("x-api-key") != "key" || r.Header.Get("anthropic-version") != "2023-06-01" {
						t.Error("missing anthropic headers")
					}
				} else if r.Header.Get("Authorization") != "Bearer key" {
					t.Error("missing bearer")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body["model"] != "opaque-model" || body["stream"] != false {
					t.Error("model or streaming changed")
				}
				if _, ok := body["temperature"]; ok {
					t.Error("unsupported temperature sent")
				}
				var input []any
				var schema map[string]any
				if mode == "responses" {
					if !strings.HasPrefix(body["instructions"].(string), "original selection criteria\n\nWire protocol:") || body["store"] != false {
						t.Error("prompt changed or server storage enabled")
					}
					input = body["input"].([]any)
					format := body["text"].(map[string]any)["format"].(map[string]any)
					if format["type"] != "json_schema" || format["strict"] != true || format["name"] != "dating_decision" {
						t.Error("missing strict schema")
					}
					schema = format["schema"].(map[string]any)
				} else {
					if !strings.HasPrefix(body["system"].(string), "original selection criteria\n\nWire protocol:") || body["max_tokens"] != float64(4096) {
						t.Error("prompt or token limit missing")
					}
					input = body["messages"].([]any)
					format := body["output_config"].(map[string]any)["format"].(map[string]any)
					if format["type"] != "json_schema" {
						t.Error("missing schema")
					}
					schema = format["schema"].(map[string]any)
					if _, ok := schema["properties"].(map[string]any)["message"].(map[string]any)["maxLength"]; ok {
						t.Error("unsupported string constraint sent")
					}
				}
				if schema["additionalProperties"] != false || len(schema["required"].([]any)) != 3 {
					t.Error("decision schema changed")
				}
				if len(input) != 1 {
					t.Error("wrong input count")
					return
				}
				user := input[0].(map[string]any)
				parts := user["content"].([]any)
				if user["role"] != "user" || len(parts) != 3 {
					t.Error("lost profile data")
					return
				}
				image := parts[0].(map[string]any)
				if mode == "responses" {
					if image["type"] != "input_image" || image["image_url"] != "data:image/jpeg;base64,aW1hZ2U=" {
						t.Error("lost encoded image")
					}
				} else {
					source := image["source"].(map[string]any)
					if image["type"] != "image" || source["type"] != "base64" || source["media_type"] != "image/jpeg" || source["data"] != "aW1hZ2U=" {
						t.Error("lost encoded image")
					}
					if parts[1].(map[string]any)["source"].(map[string]any)["url"] != "https://example.com/image.png" {
						t.Error("lost remote image")
					}
				}
				if parts[2].(map[string]any)["text"] != "bio" {
					t.Error("lost bio")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(nativeResponse(mode)))
			}))
			defer server.Close()
			c, model, err := NewResolvedClient(context.Background(), "key", server.URL+"/prefix/v1/", "opaque-model", mode)
			if err != nil {
				t.Fatal(err)
			}
			c.omitTemperature = true
			c.httpClient = &openCodeHTTPClient{client: server.Client(), session: "stable-session"}
			for range 2 {
				text, err := c.DecideMultimodal(context.Background(), model, "original selection criteria", MultimodalContent{Text: "bio", ImageURLs: []string{"data:image/jpeg;base64,aW1hZ2U=", "https://example.com/image.png"}}, .7)
				if err != nil || text != finalDecision {
					t.Fatalf("final text=%q error=%v", text, err)
				}
				if _, err := ParseDecision(text); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 2 {
				t.Fatalf("extra inference calls: %d", calls)
			}
		})
	}
}

func TestNativeFinalOutputOnly(t *testing.T) {
	for _, tc := range []struct{ mode, body string }{
		{"responses", `{"status":"completed","output":[{"type":"reasoning","content":[{"type":"output_text","text":"not final"}]}]}`},
		{"responses", `{"status":"incomplete","output":[]}`},
		{"responses", `{"status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"refusal"}]}]}`},
		{"responses", `{"status":"completed","error":{"message":"bad"}}`},
		{"anthropic", `{"type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"thinking","text":"not final"}]}`},
		{"anthropic", `{"type":"message","role":"assistant","stop_reason":"max_tokens","content":[{"type":"text","text":"partial"}]}`},
		{"anthropic", `{"type":"message","role":"assistant","stop_reason":"tool_use","content":[{"type":"tool_use","input":{}}]}`},
		{"anthropic", `{`},
	} {
		if text, err := parseNativeOutput(tc.mode, []byte(tc.body)); err == nil || text != "" {
			t.Errorf("accepted nonfinal %s output: %q %v", tc.mode, text, err)
		}
	}
}

func TestNativeNoSchemaFallbackAndCancellation(t *testing.T) {
	for _, mode := range []string{"responses", "anthropic"} {
		for _, status := range []int{400, 429, 503} {
			t.Run(mode+http.StatusText(status), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if status != 400 {
						cancel()
					}
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"error":{"message":"unsupported schema"}}`))
				}))
				defer server.Close()
				c, model, err := NewResolvedClient(ctx, "key", server.URL, "m", mode)
				if err != nil {
					t.Fatal(err)
				}
				_, err = c.DecideMultimodal(ctx, model, "prompt", MultimodalContent{Text: "bio"}, .7)
				if err == nil || calls.Load() != 1 {
					t.Fatalf("schema failure fell back or retried: calls=%d error=%v", calls.Load(), err)
				}
				if status != 400 && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			})
		}
	}
}
