package dating

import (
	"testing"
	"time"

	"github.com/amarnathcjd/gogram/telegram"
)

func TestIsStartChattingMessage(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "exact", text: "Start chatting", want: true},
		{name: "mixed case with spaces", text: "  START CHATTING with her  ", want: true},
		{name: "other text", text: "Write a message", want: false},
		{name: "empty", text: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isStartChattingMessage(tt.text); got != tt.want {
				t.Fatalf("isStartChattingMessage(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestExtractAndParseTelegramContactURL(t *testing.T) {
	msg := &telegram.NewMessage{Message: &telegram.MessageObj{Message: "Great! Start chatting: https://t.me/alice?text=Hello%20there."}}

	rawURL, ok := extractTelegramContactURL(msg)
	if !ok {
		t.Fatal("extractTelegramContactURL() ok = false, want true")
	}

	if rawURL != "https://t.me/alice?text=Hello%20there" {
		t.Fatalf("extractTelegramContactURL() url = %q, want %q", rawURL, "https://t.me/alice?text=Hello%20there")
	}

	parsed, ok := parseTelegramContactURL(rawURL)
	if !ok {
		t.Fatal("parseTelegramContactURL() ok = false, want true")
	}

	if got := firstPathSegment(parsed.Path); got != "alice" {
		t.Fatalf("firstPathSegment() = %q, want %q", got, "alice")
	}

	if got := parsed.Query().Get("text"); got != "Hello there" {
		t.Fatalf("parsed.Query().Get(text) = %q, want %q", got, "Hello there")
	}
}

func TestExtractTelegramContactURLFromTextURLEntity(t *testing.T) {
	msg := &telegram.NewMessage{Message: &telegram.MessageObj{
		Message:  "Start chatting",
		Entities: []telegram.MessageEntity{&telegram.MessageEntityTextURL{URL: "https://t.me/entity_user?text=Entity%20path"}},
	}}

	rawURL, ok := extractTelegramContactURL(msg)
	if !ok {
		t.Fatal("extractTelegramContactURL() ok = false, want true")
	}

	if rawURL != "https://t.me/entity_user?text=Entity%20path" {
		t.Fatalf("extractTelegramContactURL() url = %q, want %q", rawURL, "https://t.me/entity_user?text=Entity%20path")
	}
}

func TestBuildReciprocalLikeFinalPayloadWithContext(t *testing.T) {
	eventNow := time.Unix(1710000100, 0)
	msg := &telegram.NewMessage{Message: &telegram.MessageObj{
		Date:    1710000000,
		Message: "It's a match. Start chatting now: t.me/example_user?text=Hello%20from%20bot",
	}}

	ctx := RecentReciprocalLikeContext{
		ProfileText: "bio",
		OpenerText:  "opener",
		CapturedAt:  time.Unix(1710000050, 0),
	}

	payload, ok := buildReciprocalLikeFinalPayload(msg, RecentVisibleProfileCard{}, false, ctx, true, eventNow)
	if !ok {
		t.Fatal("buildReciprocalLikeFinalPayload() ok = false, want true")
	}

	if payload.EventType != reciprocalLikeFinalEventType {
		t.Fatalf("EventType = %q, want %q", payload.EventType, reciprocalLikeFinalEventType)
	}
	if payload.RawContactURL != "t.me/example_user?text=Hello%20from%20bot" {
		t.Fatalf("RawContactURL = %q, want %q", payload.RawContactURL, "t.me/example_user?text=Hello%20from%20bot")
	}
	if payload.ContactUsername != "example_user" {
		t.Fatalf("ContactUsername = %q, want %q", payload.ContactUsername, "example_user")
	}
	if payload.DeeplinkText != "Hello from bot" {
		t.Fatalf("DeeplinkText = %q, want %q", payload.DeeplinkText, "Hello from bot")
	}
	if payload.ProfileText != "bio" || payload.OpenerText != "opener" {
		t.Fatalf("context fields = [%q, %q], want [bio, opener]", payload.ProfileText, payload.OpenerText)
	}
	if !payload.ContextCapturedAt.Equal(ctx.CapturedAt) {
		t.Fatalf("ContextCapturedAt = %v, want %v", payload.ContextCapturedAt, ctx.CapturedAt)
	}
	if !payload.EventTimestamp.Equal(time.Unix(1710000000, 0)) {
		t.Fatalf("EventTimestamp = %v, want %v", payload.EventTimestamp, time.Unix(1710000000, 0))
	}
}

func TestBuildReciprocalLikeFinalPayloadWithoutContext(t *testing.T) {
	eventNow := time.Unix(1710000100, 0)
	msg := &telegram.NewMessage{Message: &telegram.MessageObj{
		Message: "Start chatting here https://t.me/no_context",
	}}

	payload, ok := buildReciprocalLikeFinalPayload(msg, RecentVisibleProfileCard{}, false, RecentReciprocalLikeContext{}, false, eventNow)
	if !ok {
		t.Fatal("buildReciprocalLikeFinalPayload() ok = false, want true")
	}

	if payload.RawContactURL != "https://t.me/no_context" {
		t.Fatalf("RawContactURL = %q, want %q", payload.RawContactURL, "https://t.me/no_context")
	}
	if payload.ContactUsername != "no_context" {
		t.Fatalf("ContactUsername = %q, want %q", payload.ContactUsername, "no_context")
	}
	if payload.DeeplinkText != "" {
		t.Fatalf("DeeplinkText = %q, want empty", payload.DeeplinkText)
	}
	if payload.ProfileText != "" || payload.OpenerText != "" {
		t.Fatalf("context fields = [%q, %q], want all empty", payload.ProfileText, payload.OpenerText)
	}
	if !payload.ContextCapturedAt.IsZero() {
		t.Fatalf("ContextCapturedAt = %v, want zero", payload.ContextCapturedAt)
	}
	if !payload.EventTimestamp.Equal(eventNow) {
		t.Fatalf("EventTimestamp = %v, want %v", payload.EventTimestamp, eventNow)
	}
}

func TestBuildReciprocalLikeFinalPayloadPrefersVisibleProfileAndDropsStaleOpener(t *testing.T) {
	h := &Handler{state: NewStateMachine()}
	now := time.Unix(1711000100, 0)

	h.state.AddRecentReciprocalLikeContext(RecentReciprocalLikeContext{
		ProfileText: "Alice, 28 - Loves hiking",
		OpenerText:  "Hi Alice!",
		CapturedAt:  now.Add(-2 * time.Minute),
	})
	h.state.RememberVisibleProfileCard("Bella, 27 - Coffee and books", 200, now.Add(-time.Minute))

	msg := &telegram.NewMessage{ID: 201, Message: &telegram.MessageObj{Message: "Excellent! Start chatting 👉 Bella https://t.me/bella_user?text=Hey"}}

	payload, ok := h.BuildReciprocalLikeFinalPayload(msg, now)
	if !ok {
		t.Fatal("BuildReciprocalLikeFinalPayload() ok = false, want true")
	}

	if payload.ProfileText != "Bella, 27 - Coffee and books" {
		t.Fatalf("ProfileText = %q, want %q", payload.ProfileText, "Bella, 27 - Coffee and books")
	}
	if payload.OpenerText != "" {
		t.Fatalf("OpenerText = %q, want empty", payload.OpenerText)
	}
	if !payload.ContextCapturedAt.IsZero() {
		t.Fatalf("ContextCapturedAt = %v, want zero", payload.ContextCapturedAt)
	}
}
