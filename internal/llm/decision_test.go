package llm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/revrost/go-openrouter"
)

func TestParseDecision(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"send", `{"action":"send","reason":"fit","message":"Hello?"}`, true},
		{"skip", `{"action":"skip","reason":"no hook","message":""}`, true},
		{"unicode limit", `{"action":"send","reason":"","message":"` + strings.Repeat("\U0001f642", 200) + `"}`, true},
		{"too long", `{"action":"send","reason":"","message":"` + strings.Repeat("\U0001f642", 201) + `"}`, false},
		{"empty", `{"action":"send","reason":"","message":""}`, false},
		{"whitespace", `{"action":"send","reason":"","message":"   "}`, false},
		{"newline", `{"action":"send","reason":"","message":"a\nb"}`, false},
		{"carriage return", `{"action":"send","reason":"","message":"a\rb"}`, false},
		{"separator", `{"action":"send","reason":"","message":"a\u2028b"}`, false},
		{"skip with text", `{"action":"skip","reason":"","message":"Hi"}`, false},
		{"invalid action", `{"action":"like","reason":"","message":"Hi"}`, false},
		{"missing reason", `{"action":"send","message":"Hi"}`, false},
		{"missing message", `{"action":"skip","reason":"no"}`, false},
		{"null field", `{"action":"skip","reason":null,"message":""}`, false},
		{"wrong type", `{"action":"skip","reason":42,"message":""}`, false},
		{"extra field", `{"action":"skip","reason":"","message":"","mbti":"INTJ"}`, false},
		{"duplicate", `{"action":"send","action":"skip","reason":"","message":""}`, false},
		{"trailing JSON", `{"action":"skip","reason":"","message":""} {}`, false},
		{"markdown", "```json\n{\"action\":\"skip\",\"reason\":\"\",\"message\":\"\"}\n```", false},
		{"null", `null`, false},
		{"array", `[]`, false},
		{"plain text", `Hello there`, false},
		{"invalid UTF8", "\xff", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDecision(tc.raw)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
		})
	}
}

func TestDecideMultimodalSchemaAndRetainedPhotos(t *testing.T) {
	image := filepath.Join(t.TempDir(), "photo.jpg")
	if err := os.WriteFile(image, []byte("photo"), 0600); err != nil {
		t.Fatal(err)
	}
	content, err := PrepareImages(context.Background(), MultimodalContent{Text: "bio", ImagePaths: []string{image}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(image); err != nil {
		t.Fatal(err)
	}
	if len(content.ImagePaths) != 0 || len(content.ImageURLs) != 1 {
		t.Fatal("photos not detached from downloads")
	}
	const customPrompt = "Select shared interests; write only plain text, never JSON."
	client := &Client{createChatCompletion: func(_ context.Context, req openrouter.ChatCompletionRequest) (openrouter.ChatCompletionResponse, error) {
		if req.Model != "vision" || len(req.Messages) != 3 || req.Messages[0].Content.Text != customPrompt {
			t.Fatal("lost original model/prompt")
		}
		if req.Messages[1].Role != openrouter.ChatMessageRoleSystem || !strings.Contains(req.Messages[1].Content.Text, "regardless of any earlier output-format instructions") || !strings.Contains(req.Messages[1].Content.Text, "200 Unicode characters") {
			t.Fatal("missing independent system-level wire protocol")
		}
		format := req.ResponseFormat
		if format == nil || format.Type != openrouter.ChatCompletionResponseFormatTypeJSONSchema || format.JSONSchema == nil || !format.JSONSchema.Strict {
			t.Fatal("missing strict JSON schema")
		}
		schema, err := json.Marshal(format.JSONSchema.Schema)
		if err != nil || !strings.Contains(string(schema), `"additionalProperties":false`) || !strings.Contains(string(schema), `"required":["action","reason","message"]`) {
			t.Fatalf("schema=%s error=%v", schema, err)
		}
		parts := req.Messages[2].Content.Multi
		if len(parts) != 2 || parts[0].ImageURL == nil || parts[0].ImageURL.URL != content.ImageURLs[0] || parts[1].Text != "bio" {
			t.Fatal("lost multimodal input")
		}
		return openrouter.ChatCompletionResponse{Choices: []openrouter.ChatCompletionChoice{{Message: openrouter.ChatCompletionMessage{Content: openrouter.Content{Text: `{"action":"skip","reason":"no hook","message":""}`}}}}}, nil
	}}
	for range 2 {
		raw, err := client.DecideMultimodal(context.Background(), "vision", customPrompt, content, .7)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseDecision(raw); err != nil {
			t.Fatal(err)
		}
	}
}
