package dating

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/amarnathcjd/gogram/telegram"
)

const verificationHistoryLimit = 50

func isVerificationRequest(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	return strings.HasPrefix(text, "to verify your profile, send a circle video") &&
		strings.Contains(text, "say leomatchbot on camera") &&
		strings.Contains(text, "show this gesture")
}

func (sm *StateMachine) IsWaitingVerification() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.verificationWaiting
}

func (sm *StateMachine) waitForVerification() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.verificationWaiting = true
	if sm.state != StateStopped {
		sm.state = StateWaitingVerification
	}
	sm.acceptingWork = false
	sm.pendingMessage = ""
	sm.profileData = nil
	sm.retryCount = 0
	sm.pausedUntil = time.Time{}
	sm.ownProfileSkip = ownProfileSkipContext{}
	sm.startupOwnProfileSkip = startupOwnProfileSkipContext{}
	clear(sm.groupedCaptions)
	clear(sm.groupedButtons)
	clear(sm.recoveryQueued)
	sm.activeMessageButton = ""
	sm.activeKeyboardID = 0
	sm.latestProfileJobID = 0
	sm.lastProcessedJobID = 0
	sm.stuckEscalationLevel = 0
	sm.visibleProfileCard = RecentVisibleProfileCard{}
	sm.reciprocalLikeContext = nil
	for {
		select {
		case <-sm.profileQueue:
		default:
			return
		}
	}
}

// Cancellation must precede any wait on decisionMu: the LLM owns that lock.
// There is deliberately no automatic resume without known success evidence.
func (h *Handler) waitForVerification() {
	h.state.waitForVerification()
	h.clearPauseWakeup()
	_ = h.lifecycleContext()
	h.cancelLifecycleContext()
	h.state.CancelWorkerContext()
	log.Printf("[%s] Waiting for human verification; automation disabled, no automatic resume", h.Name())
}

func (h *Handler) observeVerification(m *telegram.NewMessage) bool {
	if m != nil && m.Message != nil && !m.Message.Out && m.ChatID() == h.chatID && isVerificationRequest(m.Text()) {
		h.waitForVerification()
		return true
	}
	return h.state.IsWaitingVerification()
}

// CheckVerificationHistory is read-only and must run before handlers/workers
// are registered. Any challenge in this bounded window is treated as unresolved;
// outgoing videos, Skip, menus and later media are not proof of verification.
func (h *Handler) CheckVerificationHistory() error {
	h.verificationHistoryOnce.Do(func() {
		getHistory := h.getVerificationHistoryFn
		if getHistory == nil {
			getHistory = func(limit int) ([]telegram.NewMessage, error) {
				if h.tgClient == nil {
					return nil, fmt.Errorf("Telegram client unavailable")
				}
				peer, err := h.ensureBotPeer(h.lifecycleContext())
				if err != nil {
					return nil, err
				}
				return h.tgClient.GetHistory(peer, &telegram.HistoryOption{Limit: int32(limit)})
			}
		}
		messages, err := getHistory(verificationHistoryLimit)
		if err != nil {
			h.waitForVerification()
			h.verificationHistoryErr = fmt.Errorf("verification history check failed (automation disabled): %w", err)
			return
		}
		for i := range messages {
			if h.observeVerification(&messages[i]) {
				break
			}
		}
	})
	return h.verificationHistoryErr
}
