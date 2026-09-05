package dating

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0FL01/tg-dating-agent/internal/standalone"
	"github.com/amarnathcjd/gogram/telegram"
)

func verificationExample() *telegram.NewMessage {
	return &telegram.NewMessage{ID: 100, Message: &telegram.MessageObj{
		PeerID:      &telegram.PeerUser{UserID: 123},
		Message:     "To verify your profile, send a circle video \u2013 say Leomatchbot on camera and show this gesture \u2013 \U0001f44e\n\n\u26a0\ufe0f Faking verification leads to instant ban.",
		Media:       &telegram.MessageMediaDocument{},
		ReplyMarkup: &telegram.ReplyKeyboardMarkup{Rows: []*telegram.KeyboardButtonRow{{Buttons: []telegram.KeyboardButton{&telegram.KeyboardButtonObj{Text: "Skip"}}}}},
	}}
}

func TestVerificationExampleBlocksAllAutomation(t *testing.T) {
	for _, album := range []bool{false, true} {
		t.Run(map[bool]string{false: "message", true: "album"}[album], func(t *testing.T) {
			client := &scriptedSummarizer{}
			h := &Handler{chatID: 123, state: NewStateMachine(), client: client}
			var sends atomic.Int32
			h.clickButtonFn = func(context.Context, string) error { sends.Add(1); return nil }
			h.sendMessageFn = func(context.Context, telegram.InputPeer, string) error { sends.Add(1); return nil }
			h.state.SetPendingMessage("old opener")
			h.state.SetProfileData(&ProfileData{ProfileText: "old profile"})
			h.state.Enqueue(ProfileJob{Type: "menu_recovery"})
			ctx := h.lifecycleContext()
			if album {
				_ = h.HandleAlbum(&telegram.Album{Messages: []*telegram.NewMessage{verificationExample()}})
			} else {
				_ = h.Handle(verificationExample())
			}
			if ctx.Err() == nil || h.state.GetState() != StateWaitingVerification {
				t.Fatal("verification must cancel lifecycle and enter distinct waiting state")
			}
			if h.state.GetPendingMessage() != "" || h.state.GetProfileData() != nil {
				t.Fatal("old profile context retained")
			}
			mustQueueEmpty(t, h.state)
			for _, text := range []string{"Skip", PatternViewProfiles, PatternWriteMessage, "Start chatting", "profile text"} {
				m := verificationExample()
				m.Message.Message = text
				_ = h.Handle(m)
				m.Message.Out = true
				_ = h.Handle(m)
			}
			h.Start()
			h.StartWorker()
			_ = h.Bootstrap()
			_ = h.processJob(context.Background(), ProfileJob{Type: "menu_recovery"})
			_ = h.clickButton("Skip")
			_ = h.sendDatingMessage(context.Background(), nil, "old opener")
			if h.state.GetState() != StateWaitingVerification || h.state.Enqueue(ProfileJob{Type: "menu_recovery"}) {
				t.Fatal("wait was bypassed")
			}
			h.Stop()
			if sends.Load() != 0 || client.snapshotCallCount() != 0 {
				t.Fatal("verification example triggered sending or LLM")
			}
		})
	}
}

func TestVerificationCancelsInFlightWorker(t *testing.T) {
	client := &blockingSummarizer{started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	h := &Handler{chatID: 123, state: NewStateMachine(), config: &standalone.Config{}, client: client}
	defer h.Shutdown()
	var sends atomic.Int32
	h.clickButtonFn = func(context.Context, string) error { sends.Add(1); return nil }
	h.StartWorker()
	h.state.Enqueue(ProfileJob{Type: "album", Album: &telegram.Album{Messages: []*telegram.NewMessage{{ID: 1, Message: &telegram.MessageObj{Message: "profile text"}}}}})
	mustReceiveSignal(t, client.started, "LLM start")
	h.state.Enqueue(ProfileJob{Type: "menu_recovery"})
	_ = h.Handle(verificationExample())
	mustReceiveSignal(t, client.canceled, "verification LLM cancellation")
	h.WaitWorkerStop()
	mustQueueEmpty(t, h.state)
	if sends.Load() != 0 || h.state.GetState() != StateWaitingVerification {
		t.Fatal("in-flight work sent or overwrote verification wait")
	}
}

func TestVerificationCancelsSendDelay(t *testing.T) {
	h := &Handler{chatID: 123, state: NewStateMachine(), actionDelay: time.Hour}
	ctx := h.lifecycleContext()
	var sends atomic.Int32
	h.setBotPeer(&telegram.InputPeerUser{UserID: 123})
	h.sendMessageFn = func(context.Context, telegram.InputPeer, string) error { sends.Add(1); return nil }
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := h.sendValidatedMessage(ctx, "old opener"); err != nil {
			t.Errorf("send cancellation: %v", err)
		}
	}()
	_ = h.Handle(verificationExample())
	mustReceiveSignal(t, done, "send delay cancellation")
	if sends.Load() != 0 {
		t.Fatal("opener sent after verification pause")
	}
}

func TestVerificationStartupHistoryBeforeBootstrap(t *testing.T) {
	for _, failed := range []bool{false, true} {
		h := &Handler{chatID: 123, state: NewStateMachine()}
		calls := 0
		h.getVerificationHistoryFn = func(limit int) ([]telegram.NewMessage, error) {
			calls++
			if limit != 50 {
				t.Fatalf("history bound = %d", limit)
			}
			if failed {
				return nil, errors.New("history unavailable")
			}
			outgoing := verificationExample()
			outgoing.Message.Out = true
			outgoing.Message.Message = "Skip"
			return []telegram.NewMessage{*outgoing, *verificationExample()}, nil
		}
		err := h.Bootstrap() // A send would access the nil Telegram client.
		if (err != nil) != failed || calls != 1 || !h.state.IsWaitingVerification() {
			t.Fatalf("history gate: error=%v calls=%d state=%v", err, calls, h.state.GetState())
		}
		_ = h.CheckVerificationHistory()
		if calls != 1 {
			t.Fatal("history check repeated")
		}
	}
}

func TestVerificationIgnoresOutgoingAndUnrelatedChat(t *testing.T) {
	h := &Handler{chatID: 123, state: NewStateMachine()}
	m := verificationExample()
	m.Message.Out = true
	if h.observeVerification(m) {
		t.Fatal("outgoing text is not bot authority")
	}
	m.Message.Out = false
	m.Message.PeerID = &telegram.PeerUser{UserID: 456}
	if h.observeVerification(m) {
		t.Fatal("unrelated chat is not bot authority")
	}
}
