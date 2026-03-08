package dating

import (
	"testing"

	"github.com/amarnathcjd/gogram/telegram"
)

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
