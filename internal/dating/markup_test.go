package dating

import (
	"testing"

	"github.com/amarnathcjd/gogram/telegram"
)

func TestHasReplyKeyboardButtonText(t *testing.T) {
	tests := []struct {
		name       string
		message    *telegram.NewMessage
		buttonText string
		want       bool
	}{
		{
			name:       "nil message",
			message:    nil,
			buttonText: ButtonViewProfiles,
			want:       false,
		},
		{
			name:       "nil message object",
			message:    &telegram.NewMessage{},
			buttonText: ButtonViewProfiles,
			want:       false,
		},
		{
			name: "no markup",
			message: &telegram.NewMessage{
				Message: &telegram.MessageObj{},
			},
			buttonText: ButtonViewProfiles,
			want:       false,
		},
		{
			name: "matching button in reply keyboard",
			message: &telegram.NewMessage{
				Message: &telegram.MessageObj{
					ReplyMarkup: &telegram.ReplyKeyboardMarkup{
						Rows: []*telegram.KeyboardButtonRow{
							{
								Buttons: []telegram.KeyboardButton{
									&telegram.KeyboardButtonObj{Text: ButtonViewProfiles},
								},
							},
						},
					},
				},
			},
			buttonText: ButtonViewProfiles,
			want:       true,
		},
		{
			name: "non matching button in reply keyboard",
			message: &telegram.NewMessage{
				Message: &telegram.MessageObj{
					ReplyMarkup: &telegram.ReplyKeyboardMarkup{
						Rows: []*telegram.KeyboardButtonRow{
							{
								Buttons: []telegram.KeyboardButton{
									&telegram.KeyboardButtonObj{Text: ButtonDislike},
								},
							},
						},
					},
				},
			},
			buttonText: ButtonViewProfiles,
			want:       false,
		},
		{
			name: "non reply keyboard markup",
			message: &telegram.NewMessage{
				Message: &telegram.MessageObj{
					ReplyMarkup: &telegram.ReplyInlineMarkup{},
				},
			},
			buttonText: ButtonViewProfiles,
			want:       false,
		},
		{
			name: "non object keyboard button type",
			message: &telegram.NewMessage{
				Message: &telegram.MessageObj{
					ReplyMarkup: &telegram.ReplyKeyboardMarkup{
						Rows: []*telegram.KeyboardButtonRow{
							{
								Buttons: []telegram.KeyboardButton{
									&telegram.KeyboardButtonURL{Text: ButtonViewProfiles, URL: "https://example.com"},
								},
							},
						},
					},
				},
			},
			buttonText: ButtonViewProfiles,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasReplyKeyboardButtonText(tt.message, tt.buttonText); got != tt.want {
				t.Fatalf("hasReplyKeyboardButtonText() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasProfileActionKeyboard(t *testing.T) {
	tests := []struct {
		name    string
		message *telegram.NewMessage
		want    bool
	}{
		{
			name:    "nil message",
			message: nil,
			want:    false,
		},
		{
			name: "like and dislike buttons",
			message: &telegram.NewMessage{Message: &telegram.MessageObj{ReplyMarkup: &telegram.ReplyKeyboardMarkup{Rows: []*telegram.KeyboardButtonRow{{Buttons: []telegram.KeyboardButton{
				&telegram.KeyboardButtonObj{Text: ButtonLike},
				&telegram.KeyboardButtonObj{Text: ButtonDislike},
			}}}}}},
			want: true,
		},
		{
			name: "like message and dislike buttons",
			message: &telegram.NewMessage{Message: &telegram.MessageObj{ReplyMarkup: &telegram.ReplyKeyboardMarkup{Rows: []*telegram.KeyboardButtonRow{{Buttons: []telegram.KeyboardButton{
				&telegram.KeyboardButtonObj{Text: ButtonLikeMessage},
				&telegram.KeyboardButtonObj{Text: ButtonDislike},
			}}}}}},
			want: true,
		},
		{
			name: "missing dislike button",
			message: &telegram.NewMessage{Message: &telegram.MessageObj{ReplyMarkup: &telegram.ReplyKeyboardMarkup{Rows: []*telegram.KeyboardButtonRow{{Buttons: []telegram.KeyboardButton{
				&telegram.KeyboardButtonObj{Text: ButtonLike},
			}}}}}},
			want: false,
		},
		{
			name: "unrelated keyboard",
			message: &telegram.NewMessage{Message: &telegram.MessageObj{ReplyMarkup: &telegram.ReplyKeyboardMarkup{Rows: []*telegram.KeyboardButtonRow{{Buttons: []telegram.KeyboardButton{
				&telegram.KeyboardButtonObj{Text: ButtonViewProfiles},
				&telegram.KeyboardButtonObj{Text: ButtonMyProfile},
			}}}}}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasProfileActionKeyboard(tt.message); got != tt.want {
				t.Fatalf("hasProfileActionKeyboard() = %v, want %v", got, tt.want)
			}
		})
	}
}
