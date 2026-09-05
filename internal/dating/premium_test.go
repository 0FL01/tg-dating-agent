package dating

import (
	"context"
	"testing"
	"time"

	"github.com/amarnathcjd/gogram/telegram"
)

const premiumScreenText = "Activate Premium and be at the top \u2728\n\nChoose your Premium duration:\n\nBy paying for Premium you agree to the terms."

func premiumScreenMessage(buttons ...string) *telegram.NewMessage {
	row := &telegram.KeyboardButtonRow{}
	for _, text := range buttons {
		row.Buttons = append(row.Buttons, &telegram.KeyboardButtonObj{Text: text})
	}
	return &telegram.NewMessage{ID: 500, Message: &telegram.MessageObj{
		Message:     premiumScreenText,
		PeerID:      &telegram.PeerUser{UserID: 123456789},
		ReplyMarkup: &telegram.ReplyKeyboardMarkup{Rows: []*telegram.KeyboardButtonRow{row}},
	}}
}

func TestPremiumPurchaseRecoveryUsesOnlyActualBack(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}
	h.state.SetState(StateViewingProfiles)
	h.state.NextStuckRecoveryEscalation()
	h.state.NextStuckRecoveryEscalation()
	var actions []string
	h.clickButtonFn = func(_ context.Context, button string) error {
		actions = append(actions, button)
		return nil
	}
	h.sendMessageFn = func(_ context.Context, _ telegram.InputPeer, text string) error {
		t.Fatalf("unexpected message: %q", text)
		return nil
	}
	m := premiumScreenMessage("30 days \u2022 \u2b50 750", "90 days \u2022 \u2b50 1500", "\u2190 Back")
	for i := 0; i < 2; i++ {
		if err := h.Handle(m); err != nil {
			t.Fatal(err)
		}
	}
	job := mustDequeueJob(t, h.state)
	if job.Type != "premium_recovery" || job.Message != m {
		t.Fatalf("job = %+v, want premium recovery for actual message", job)
	}
	if len(h.state.GetQueue()) != 0 {
		t.Fatal("premium screen queued duplicate or generic recovery")
	}
	if err := h.processJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0] != "\u2190 Back" {
		t.Fatalf("actions = %q, want only actual Back button", actions)
	}
	if h.IsStopped() {
		t.Fatal("safe Back recovery stopped handler")
	}
	if got := h.state.NextStuckRecoveryEscalation(); got != 1 {
		t.Fatalf("escalation = %d, want reset", got)
	}
}

func TestPremiumPurchaseRecoveryFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*Handler, *telegram.NewMessage)
	}{
		{"missing back", func(_ *Handler, m *telegram.NewMessage) {
			m.Message.ReplyMarkup = premiumScreenMessage("30 days \u2022 \u2b50 750", "1", "Back", "\u2190 Back and pay").Message.ReplyMarkup
		}},
		{"no markup", func(_ *Handler, m *telegram.NewMessage) { m.Message.ReplyMarkup = nil }},
		{"inline back", func(_ *Handler, m *telegram.NewMessage) {
			m.Message.ReplyMarkup = &telegram.ReplyInlineMarkup{Rows: []*telegram.KeyboardButtonRow{{Buttons: []telegram.KeyboardButton{
				&telegram.KeyboardButtonCallback{Text: "\u2190 Back", Data: []byte("pay")},
			}}}}
		}},
		{"waiting prompt", func(h *Handler, _ *telegram.NewMessage) { h.state.SetState(StateWaitingPrompt) }},
		{"pending letter", func(h *Handler, _ *telegram.NewMessage) { h.state.SetPendingMessage("draft") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{chatID: 123456789, state: NewStateMachine()}
			h.state.SetState(StateViewingProfiles)
			h.clickButtonFn = func(_ context.Context, button string) error {
				t.Fatalf("unsafe button sent: %q", button)
				return nil
			}
			h.sendSleepFn = func(context.Context) error { t.Fatal("sleep sent"); return nil }
			m := premiumScreenMessage("30 days \u2022 \u2b50 750", "\u2190 Back")
			if err := h.Handle(m); err != nil {
				t.Fatal(err)
			}
			// The worker must recheck letter state and markup, not trust enqueue-time conditions.
			tc.prepare(h, m)
			if err := h.processJob(context.Background(), mustDequeueJob(t, h.state)); err != nil {
				t.Fatal(err)
			}
			if !h.IsStopped() {
				t.Fatal("unsafe premium recovery did not stop locally")
			}
		})
	}
}

func TestPremiumPurchaseRecoverySkipsCancelledPausedAndStaleJobs(t *testing.T) {
	for _, name := range []string{"cancelled", "paused", "stopped", "fresher pending", "fresher processed"} {
		t.Run(name, func(t *testing.T) {
			h := &Handler{state: NewStateMachine()}
			h.state.SetState(StateViewingProfiles)
			h.clickButtonFn = func(_ context.Context, button string) error {
				t.Fatalf("unexpected button: %q", button)
				return nil
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch name {
			case "cancelled":
				cancel()
			case "paused":
				h.state.PauseFor(time.Hour)
			case "stopped":
				h.state.BeginShutdown()
			case "fresher pending", "fresher processed":
				h.state.Enqueue(ProfileJob{Type: "message", ProfileMessageID: 501})
				if name == "fresher processed" {
					h.state.TryMarkProfileJobProcessing(501)
				}
			}
			if err := h.processJob(ctx, ProfileJob{Type: "premium_recovery", Message: premiumScreenMessage("\u2190 Back")}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPremiumPurchaseDetectionRequiresOfficialScreenMarkers(t *testing.T) {
	for _, text := range []string{"I use Premium", "Choose your Premium duration:", "Activate Premium and be at the top", "My profile: " + premiumScreenText} {
		if isPremiumPurchaseMessage(text) {
			t.Fatalf("ordinary text classified as premium purchase: %q", text)
		}
	}
	if !isPremiumPurchaseMessage(premiumScreenText) {
		t.Fatal("official premium screen not detected")
	}
}
