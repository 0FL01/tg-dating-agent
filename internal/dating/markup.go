package dating

import "github.com/amarnathcjd/gogram/telegram"

func hasReplyMarkup(m *telegram.NewMessage) bool {
	if m == nil || m.Message == nil {
		return false
	}

	return m.Message.ReplyMarkup != nil
}

func hasReplyKeyboardButtonText(m *telegram.NewMessage, buttonText string) bool {
	if m == nil || m.Message == nil || buttonText == "" {
		return false
	}

	markup, ok := m.Message.ReplyMarkup.(*telegram.ReplyKeyboardMarkup)
	if !ok || markup == nil {
		return false
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

			if obj.Text == buttonText {
				return true
			}
		}
	}

	return false
}

func hasProfileActionKeyboard(m *telegram.NewMessage) bool {
	if !hasReplyKeyboardButtonText(m, ButtonDislike) {
		return false
	}

	if hasReplyKeyboardButtonText(m, ButtonLike) {
		return true
	}

	return hasReplyKeyboardButtonText(m, ButtonLikeMessage)
}
