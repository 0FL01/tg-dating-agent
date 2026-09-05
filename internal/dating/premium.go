package dating

import (
	"context"
	"log"
	"strings"

	"github.com/amarnathcjd/gogram/telegram"
)

func isPremiumPurchaseMessage(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	return strings.HasPrefix(text, "activate premium and be at the top") &&
		strings.Contains(text, "choose your premium duration:") &&
		strings.Contains(text, "by paying for premium")
}

func (h *Handler) recoverPremiumPurchase(ctx context.Context, m *telegram.NewMessage) error {
	if h.shouldStopProcessing(ctx) || h.isPaused() || m == nil || !isPremiumPurchaseMessage(m.Text()) {
		return nil
	}
	if pending, latest, last := h.state.HasPendingFresherProfileJob(); pending || latest > m.ID || last > m.ID {
		return nil
	}
	state := h.state.GetState()
	if (state != StateIdle && state != StateViewingProfiles) || h.state.GetPendingMessage() != "" {
		log.Printf("[%s] Premium screen during pending letter flow; stopping locally", h.Name())
		return h.stopMessageEntry(ctx)
	}
	back, ok := findReplyKeyboardButtonText(m, func(text string) bool {
		return text == "\u2190 Back"
	})
	if !ok {
		log.Printf("[%s] Premium screen has no verified Back button; stopping locally", h.Name())
		return h.stopMessageEntry(ctx)
	}
	h.state.ResetStuckRecoveryEscalation()
	return h.clickButtonWithContext(ctx, back)
}
