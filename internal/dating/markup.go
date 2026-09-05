package dating

import (
	"strings"

	"github.com/amarnathcjd/gogram/telegram"
)

func hasReplyMarkup(m *telegram.NewMessage) bool {
	if m == nil || m.Message == nil {
		return false
	}

	return m.Message.ReplyMarkup != nil
}

func hasReplyKeyboardButtonText(m *telegram.NewMessage, buttonText string) bool {
	if buttonText == "" {
		return false
	}

	_, ok := findReplyKeyboardButtonText(m, func(text string) bool {
		return text == buttonText
	})

	return ok
}

func findReplyKeyboardButtonText(m *telegram.NewMessage, match func(string) bool) (string, bool) {
	if m == nil || m.Message == nil {
		return "", false
	}
	if match == nil {
		return "", false
	}

	markup, ok := m.Message.ReplyMarkup.(*telegram.ReplyKeyboardMarkup)
	if !ok || markup == nil {
		return "", false
	}

	for _, row := range markup.Rows {
		if row == nil {
			continue
		}

		for _, button := range row.Buttons {
			obj, ok := button.(*telegram.KeyboardButtonObj)
			if !ok || obj == nil {
				continue
			}

			if match(obj.Text) {
				return obj.Text, true
			}
		}
	}

	return "", false
}

func reciprocalOpenButtonText(m *telegram.NewMessage) (string, bool) {
	return findReplyKeyboardButtonText(m, func(text string) bool {
		trimmed := strings.TrimSpace(text)
		if trimmed == "" {
			return false
		}

		if trimmed == ButtonViewProfiles {
			return true
		}

		return strings.EqualFold(trimmed, "show") || strings.HasPrefix(strings.ToLower(trimmed), "show ")
	})
}

func hasProfileActionKeyboard(m *telegram.NewMessage) bool {
	if !hasReplyKeyboardButtonText(m, ButtonDislike) {
		return false
	}

	if hasReplyKeyboardButtonText(m, ButtonLike) {
		return true
	}

	_, ok := profileMessageButtonText(m)
	return ok
}

func profileMessageButtonText(m *telegram.NewMessage) (string, bool) {
	if !hasReplyKeyboardButtonText(m, ButtonDislike) {
		return "", false
	}
	return findReplyKeyboardButtonText(m, func(text string) bool {
		if !strings.Contains(text, "💌") {
			return false
		}
		for _, r := range text {
			switch r {
			case '💌', '📹', '📷', '🎥', '🎤', ' ', '/', '\ufe0f':
			default:
				return false
			}
		}
		return true
	})
}
