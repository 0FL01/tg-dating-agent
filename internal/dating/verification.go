package dating

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/amarnathcjd/gogram/telegram"
)

const verificationHistoryLimit = 50

// Authoritative incoming confirmations observed from the dating bot.
// Resume happens ONLY on these exact texts (trimmed, case-sensitive).
// The processing notice alone can never resume.
const (
	verificationProcessingText = "The video is being processed, please wait..."
	verificationPassedText     = "Verification passed, thank you!"
	verificationPassedEmoji    = "✅"
)

func isVerificationRequest(text string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	return strings.HasPrefix(text, "to verify your profile, send a circle video") &&
		strings.Contains(text, "say leomatchbot on camera") &&
		strings.Contains(text, "show this gesture")
}

func isVerificationProcessing(text string) bool {
	return strings.TrimSpace(text) == verificationProcessingText
}

func isVerificationSuccess(text string) bool {
	trimmed := strings.TrimSpace(text)
	return trimmed == verificationPassedText || trimmed == verificationPassedEmoji
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
func (h *Handler) waitForVerification() {
	h.state.waitForVerification()
	h.clearPauseWakeup()
	_ = h.lifecycleContext()
	h.cancelLifecycleContext()
	h.state.CancelWorkerContext()
	log.Printf("[%s] Waiting for human verification; automation disabled", h.Name())
}

func (h *Handler) observeVerification(m *telegram.NewMessage) bool {
	if m != nil && m.Message != nil && !m.Message.Out && m.ChatID() == h.chatID {
		text := m.Text()
		if isVerificationRequest(text) {
			h.waitForVerification()
			return true
		}
		// Explicit safe resume: ONLY exact authoritative incoming success
		// resumes, and only when a request was seen before (currently waiting).
		// Processing notice, outgoing "1"/"➡️", keyboards and Skip never resume.
		if isVerificationProcessing(text) {
			return h.state.IsWaitingVerification()
		}
		if isVerificationSuccess(text) {
			if h.state.IsWaitingVerification() {
				h.resumeFromVerification()
			}
			return h.state.IsWaitingVerification()
		}
	}
	return h.state.IsWaitingVerification()
}

// CheckVerificationHistory is read-only and must run before handlers/workers
// are registered. It replays the bounded 50-history window oldest-first so an
// OLD request coexisting with a later authoritative success resumes instead of
// staying blocked. Success only after request resumes; inverted order
// (success before request), processing-only, outgoing, keyboard and Skip input
// never resume.
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
		ordered := make([]telegram.NewMessage, len(messages))
		copy(ordered, messages)
		sort.SliceStable(ordered, func(i, j int) bool {
			di, dj := ordered[i].Date(), ordered[j].Date()
			if di != dj {
				return di < dj
			}
			return ordered[i].ID < ordered[j].ID
		})
		pendingRequest := false
		for i := range ordered {
			m := &ordered[i]
			if m.Message == nil || m.Message.Out {
				continue
			}
			if m.ChatID() != h.chatID {
				continue
			}
			text := m.Text()
			if isVerificationRequest(text) {
				pendingRequest = true
				continue
			}
			if isVerificationProcessing(text) {
				continue
			}
			if isVerificationSuccess(text) {
				if pendingRequest {
					pendingRequest = false
				}
				continue
			}
		}
		if pendingRequest {
			h.waitForVerification()
		}
	})
	return h.verificationHistoryErr
}
