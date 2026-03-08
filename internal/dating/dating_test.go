package dating

import (
	"testing"
	"time"
	"unicode/utf8"

	"github.com/amarnathcjd/gogram/telegram"
)

func TestTruncateMessageASCII(t *testing.T) {
	tests := []struct {
		name   string
		msg    string
		maxLen int
		want   string
	}{
		{
			name:   "shorter than limit unchanged",
			msg:    "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "truncate by last space when far enough",
			msg:    "hello world from bot",
			maxLen: 12,
			want:   "hello world",
		},
		{
			name:   "fallback to hard truncation when last space too early",
			msg:    "hi therefriend",
			maxLen: 8,
			want:   "hi there",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateMessage(tt.msg, tt.maxLen); got != tt.want {
				t.Fatalf("truncateMessage(%q, %d) = %q, want %q", tt.msg, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestTruncateMessageUTF8(t *testing.T) {
	tests := []struct {
		name      string
		msg       string
		maxLen    int
		want      string
		wantRunes int
	}{
		{
			name:      "cyrillic with word boundary",
			msg:       "Привет мир как дела",
			maxLen:    12,
			want:      "Привет мир",
			wantRunes: 10,
		},
		{
			name:      "emoji and cyrillic without spaces",
			msg:       "Привет😊мир",
			maxLen:    7,
			want:      "Привет😊",
			wantRunes: 7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateMessage(tt.msg, tt.maxLen)
			if got != tt.want {
				t.Fatalf("truncateMessage(%q, %d) = %q, want %q", tt.msg, tt.maxLen, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("truncateMessage(%q, %d) returned invalid UTF-8: %q", tt.msg, tt.maxLen, got)
			}
			if gotRunes := utf8.RuneCountInString(got); gotRunes != tt.wantRunes {
				t.Fatalf("truncateMessage(%q, %d) rune count = %d, want %d", tt.msg, tt.maxLen, gotRunes, tt.wantRunes)
			}
		})
	}
}

func TestParseMBTI(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantMBTI string
		wantOK   bool
	}{
		{name: "exact token", input: "INTJ", wantMBTI: "INTJ", wantOK: true},
		{name: "lowercase token in sentence", input: "likely enfp type", wantMBTI: "ENFP", wantOK: true},
		{name: "punctuation wrapped", input: "Result: **infj**", wantMBTI: "INFJ", wantOK: true},
		{name: "first valid among many", input: "abc ENFJ/INFJ", wantMBTI: "ENFJ", wantOK: true},
		{name: "invalid token", input: "ABCD", wantMBTI: "", wantOK: false},
		{name: "empty string", input: "", wantMBTI: "", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMBTI, gotOK := parseMBTI(tt.input)
			if gotOK != tt.wantOK {
				t.Fatalf("parseMBTI(%q) ok = %v, want %v", tt.input, gotOK, tt.wantOK)
			}
			if gotMBTI != tt.wantMBTI {
				t.Fatalf("parseMBTI(%q) mbti = %q, want %q", tt.input, gotMBTI, tt.wantMBTI)
			}
		})
	}
}

func TestIsValidMBTI(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{name: "valid type", input: "INTJ", want: true},
		{name: "valid extrovert type", input: "ESFP", want: true},
		{name: "lowercase rejected", input: "intj", want: false},
		{name: "unknown rejected", input: "ABCD", want: false},
		{name: "short rejected", input: "INT", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isValidMBTI(tt.input); got != tt.want {
				t.Fatalf("isValidMBTI(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsMBTIAllowed(t *testing.T) {
	tests := []struct {
		name      string
		mbti      string
		allowlist []string
		want      bool
	}{
		{name: "allowed exact match", mbti: "INTJ", allowlist: []string{"INTJ", "ENFP"}, want: true},
		{name: "not present", mbti: "INFJ", allowlist: []string{"INTJ", "ENFP"}, want: false},
		{name: "empty allowlist", mbti: "INTJ", allowlist: nil, want: false},
		{name: "case sensitive input", mbti: "intj", allowlist: []string{"INTJ"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMBTIAllowed(tt.mbti, tt.allowlist); got != tt.want {
				t.Fatalf("isMBTIAllowed(%q, %v) = %v, want %v", tt.mbti, tt.allowlist, got, tt.want)
			}
		})
	}
}

func TestShouldRecoverFromStuck(t *testing.T) {
	tests := []struct {
		name    string
		state   State
		message *telegram.NewMessage
		want    bool
	}{
		{
			name:  "viewing state with view profiles button",
			state: StateViewingProfiles,
			message: &telegram.NewMessage{
				Message: &telegram.MessageObj{
					ReplyMarkup: &telegram.ReplyKeyboardMarkup{
						Rows: []*telegram.KeyboardButtonRow{
							{Buttons: []telegram.KeyboardButton{&telegram.KeyboardButtonObj{Text: ButtonViewProfiles}}},
						},
					},
				},
			},
			want: true,
		},
		{
			name:  "viewing state with wrong button",
			state: StateViewingProfiles,
			message: &telegram.NewMessage{
				Message: &telegram.MessageObj{
					ReplyMarkup: &telegram.ReplyKeyboardMarkup{
						Rows: []*telegram.KeyboardButtonRow{
							{Buttons: []telegram.KeyboardButton{&telegram.KeyboardButtonObj{Text: ButtonDislike}}},
						},
					},
				},
			},
			want: false,
		},
		{
			name:    "viewing state with no button",
			state:   StateViewingProfiles,
			message: &telegram.NewMessage{Message: &telegram.MessageObj{}},
			want:    false,
		},
		{
			name:  "viewing state with text-only subscribe interstitial",
			state: StateViewingProfiles,
			message: &telegram.NewMessage{Message: &telegram.MessageObj{
				Message: "Subscribe to my channel for more matches",
			}},
			want: true,
		},
		{
			name:  "viewing state with text-only internet safety interstitial",
			state: StateViewingProfiles,
			message: &telegram.NewMessage{Message: &telegram.MessageObj{
				Message: "Please note that people on the internet can pretend to be someone else.",
			}},
			want: true,
		},
		{
			name:  "viewing state with text-only tiktok promo interstitial",
			state: StateViewingProfiles,
			message: &telegram.NewMessage{Message: &telegram.MessageObj{
				Message: "Do you want more views on TikTok? #Leomatch",
			}},
			want: true,
		},
		{
			name:  "viewing state with unknown text-only notice",
			state: StateViewingProfiles,
			message: &telegram.NewMessage{Message: &telegram.MessageObj{
				Message: "This is a generic informational notice.",
			}},
			want: false,
		},
		{
			name:  "viewing state with known text but non-empty markup",
			state: StateViewingProfiles,
			message: &telegram.NewMessage{Message: &telegram.MessageObj{
				Message:     "Do you want more views on TikTok? #Leomatch",
				ReplyMarkup: &telegram.ReplyInlineMarkup{},
			}},
			want: false,
		},
		{
			name:  "non viewing state with view profiles button",
			state: StateIdle,
			message: &telegram.NewMessage{
				Message: &telegram.MessageObj{
					ReplyMarkup: &telegram.ReplyKeyboardMarkup{
						Rows: []*telegram.KeyboardButtonRow{
							{Buttons: []telegram.KeyboardButton{&telegram.KeyboardButtonObj{Text: ButtonViewProfiles}}},
						},
					},
				},
			},
			want: false,
		},
		{
			name:  "non viewing state with text-only interstitial",
			state: StateIdle,
			message: &telegram.NewMessage{Message: &telegram.MessageObj{
				Message: "Subscribe to my channel for more matches",
			}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{state: NewStateMachine()}
			h.state.SetState(tt.state)

			if got := h.shouldRecoverFromStuck(tt.message); got != tt.want {
				t.Fatalf("shouldRecoverFromStuck() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsDailyLimitMessage(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{
			name: "exact message",
			text: PatternDailyLimitExact,
			want: true,
		},
		{
			name: "case insensitive variation with emoji",
			text: "TOO MANY ❤️ TODAY. Invite your friends to get more hearts.",
			want: true,
		},
		{
			name: "minor wording changes keep signal",
			text: "Oops, too many ❤ for today. Invite friends and come back later.",
			want: true,
		},
		{
			name: "too many with today but no heart",
			text: "Too many likes today, try again tomorrow",
			want: false,
		},
		{
			name: "too many with heart but unrelated",
			text: "Too many thoughts ❤ about this profile",
			want: false,
		},
		{
			name: "empty text",
			text: "",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDailyLimitMessage(tt.text); got != tt.want {
				t.Fatalf("isDailyLimitMessage(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestHandleDailyLimitPausesAndResetsState(t *testing.T) {
	h := &Handler{state: NewStateMachine()}
	h.state.SetState(StateViewingProfiles)
	h.state.SetPendingMessage("draft")
	h.state.SetProfileData(&ProfileData{ProfileText: "bio", PhotoPaths: []string{"/tmp/photo.jpg"}})
	h.state.IncrementRetry()

	msg := &telegram.NewMessage{Message: &telegram.MessageObj{Message: PatternDailyLimitExact}}

	if err := h.Handle(msg); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if got := h.state.GetState(); got != StateIdle {
		t.Fatalf("state = %v, want %v", got, StateIdle)
	}

	if got := h.state.GetPendingMessage(); got != "" {
		t.Fatalf("pending message = %q, want empty", got)
	}

	if got := h.state.GetProfileData(); got != nil {
		t.Fatalf("profile data = %#v, want nil", got)
	}

	if got := h.state.GetRetryCount(); got != 0 {
		t.Fatalf("retry count = %d, want 0", got)
	}

	paused, resumed, until := h.state.CheckPause(time.Now())
	if !paused || resumed || until.IsZero() {
		t.Fatalf("CheckPause(now) = (%v, %v, %v), want (true, false, non-zero)", paused, resumed, until)
	}

	remaining := until.Sub(time.Now())
	if remaining > DailyLimitPauseDuration || remaining < DailyLimitPauseDuration-2*time.Second {
		t.Fatalf("pause remaining = %v, want close to %v", remaining, DailyLimitPauseDuration)
	}
}

func TestHandleGenericTooManyStopsWithoutPause(t *testing.T) {
	h := &Handler{state: NewStateMachine()}
	h.state.SetState(StateViewingProfiles)
	h.state.SetPendingMessage("draft")
	h.state.SetProfileData(&ProfileData{ProfileText: "bio", PhotoPaths: []string{"/tmp/photo.jpg"}})
	h.state.IncrementRetry()

	msg := &telegram.NewMessage{Message: &telegram.MessageObj{Message: "Too many likes today, try again tomorrow"}}

	if err := h.Handle(msg); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if got := h.state.GetState(); got != StateStopped {
		t.Fatalf("state = %v, want %v", got, StateStopped)
	}

	if got := h.state.GetPendingMessage(); got != "" {
		t.Fatalf("pending message = %q, want empty", got)
	}

	if got := h.state.GetProfileData(); got != nil {
		t.Fatalf("profile data = %#v, want nil", got)
	}

	if got := h.state.GetRetryCount(); got != 0 {
		t.Fatalf("retry count = %d, want 0", got)
	}

	paused, resumed, until := h.state.CheckPause(time.Now())
	if paused || resumed || !until.IsZero() {
		t.Fatalf("CheckPause(now) = (%v, %v, %v), want (false, false, zero)", paused, resumed, until)
	}
}
