package dating

import (
	"context"
	"errors"
	"sync"
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

func verificationIncomingForTest(id int32, text string) *telegram.NewMessage {
	return &telegram.NewMessage{ID: id, Message: &telegram.MessageObj{
		PeerID:  &telegram.PeerUser{UserID: 123},
		Message: text,
	}}
}

func verificationOutgoingForTest(id int32, text string) *telegram.NewMessage {
	return &telegram.NewMessage{ID: id, Message: &telegram.MessageObj{
		PeerID:  &telegram.PeerUser{UserID: 123},
		Message: text,
		Out:     true,
	}}
}

func mustBeWaiting(t *testing.T, h *Handler) {
	t.Helper()
	if !h.state.IsWaitingVerification() {
		t.Fatal("expected waiting_verification, got not waiting")
	}
	if h.state.GetState() != StateWaitingVerification {
		t.Fatalf("state = %v, want waiting_verification", h.state.GetState())
	}
	if h.state.Enqueue(ProfileJob{Type: "menu_recovery"}) {
		t.Fatal("Enqueue succeeded while waiting, want blocked")
	}
}

func mustBeResumed(t *testing.T, h *Handler) {
	t.Helper()
	if h.state.IsWaitingVerification() {
		t.Fatal("expected resumed (not waiting), still waiting_verification")
	}
	if h.state.GetState() == StateWaitingVerification {
		t.Fatal("state still waiting_verification after resume")
	}
	// Stop worker so queue assertions are deterministic (worker would race to consume).
	h.StopWorker()
	h.WaitWorkerStop()
	if !h.state.Enqueue(ProfileJob{Type: "message"}) {
		t.Fatal("Enqueue failed after resume, want success")
	}
	mustDequeueJob(t, h.state)
	if err := h.lifecycleContext().Err(); err != nil {
		t.Fatalf("lifecycle after resume = %v, want active", err)
	}
}

func TestVerificationOldRequestThenSuccessResumes(t *testing.T) {
	for _, successText := range []string{"Verification passed, thank you!", "✅"} {
		t.Run(successText, func(t *testing.T) {
			h := &Handler{chatID: 123, state: NewStateMachine()}
			defer h.Shutdown()
			_ = h.Handle(verificationExample())
			mustBeWaiting(t, h)
			processing := verificationIncomingForTest(101, "The video is being processed, please wait...")
			_ = h.Handle(processing)
			if !h.state.IsWaitingVerification() {
				t.Fatal("processing notice resumed, want still waiting")
			}
			success := verificationIncomingForTest(102, successText)
			_ = h.Handle(success)
			mustBeResumed(t, h)
		})
	}
}

func TestVerificationRequestWithoutSuccessRemainsWaiting(t *testing.T) {
	h := &Handler{chatID: 123, state: NewStateMachine()}
	defer h.Shutdown()
	_ = h.Handle(verificationExample())
	mustBeWaiting(t, h)
	for _, text := range []string{"profile text", "View profiles", "Skip", "1", "➡️"} {
		m := verificationIncomingForTest(101, text)
		_ = h.Handle(m)
		mustBeWaiting(t, h)
	}
}

func TestVerificationProcessingOnlyStaysWait(t *testing.T) {
	h := &Handler{chatID: 123, state: NewStateMachine()}
	defer h.Shutdown()
	_ = h.Handle(verificationExample())
	mustBeWaiting(t, h)
	_ = h.Handle(verificationIncomingForTest(101, "The video is being processed, please wait..."))
	mustBeWaiting(t, h)
	_ = h.Handle(verificationIncomingForTest(102, "The video is being processed, please wait..."))
	mustBeWaiting(t, h)
}

func TestVerificationDuplicateSuccessSafe(t *testing.T) {
	h := &Handler{chatID: 123, state: NewStateMachine()}
	defer h.Shutdown()
	_ = h.Handle(verificationExample())
	mustBeWaiting(t, h)
	_ = h.Handle(verificationIncomingForTest(102, "Verification passed, thank you!"))
	mustBeResumed(t, h)
	_ = h.Handle(verificationIncomingForTest(103, "Verification passed, thank you!"))
	mustBeResumed(t, h)
	_ = h.Handle(verificationIncomingForTest(104, "✅"))
	mustBeResumed(t, h)
}

func TestVerificationDoesNotResumeOutgoingOrKeyboard(t *testing.T) {
	h := &Handler{chatID: 123, state: NewStateMachine()}
	defer h.Shutdown()
	_ = h.Handle(verificationExample())
	mustBeWaiting(t, h)
	for _, text := range []string{"1", "➡️", "Skip", "Verification passed, thank you!", "✅"} {
		m := verificationOutgoingForTest(200, text)
		_ = h.Handle(m)
		mustBeWaiting(t, h)
	}
	// Incoming keyboard-like texts that are not exact success must not resume.
	for _, text := range []string{"1", "➡️", "Skip"} {
		m := verificationIncomingForTest(201, text)
		_ = h.Handle(m)
		mustBeWaiting(t, h)
	}
}

func TestVerificationMediaBlockedWhileWaitPreserved(t *testing.T) {
	h := &Handler{chatID: 123, state: NewStateMachine()}
	defer h.Shutdown()
	_ = h.Handle(verificationExample())
	mustBeWaiting(t, h)
	media := &telegram.NewMessage{ID: 150, Message: &telegram.MessageObj{
		Message: "profile photo",
		Media:   &telegram.MessageMediaPhoto{},
		PeerID:  &telegram.PeerUser{UserID: 123},
	}}
	_ = h.Handle(media)
	mustQueueEmpty(t, h.state)
	_ = h.HandleAlbum(&telegram.Album{Messages: []*telegram.NewMessage{media}})
	mustQueueEmpty(t, h.state)
	_ = h.Handle(verificationIncomingForTest(160, "✅"))
	if h.state.IsWaitingVerification() {
		t.Fatal("expected resume after ✅")
	}
	h.StopWorker()
	h.WaitWorkerStop()
	media2 := &telegram.NewMessage{ID: 170, Message: &telegram.MessageObj{
		Message: "profile photo after resume",
		Media:   &telegram.MessageMediaPhoto{},
		PeerID:  &telegram.PeerUser{UserID: 123},
	}}
	_ = h.Handle(media2)
	job := mustDequeueJob(t, h.state)
	if job.Type != "message" || job.Message == nil || job.Message.ID != 170 {
		t.Fatalf("after resume queued job = %+v, want message id 170", job)
	}
}

func TestVerificationHistoryOldRequestThenSuccessResumes(t *testing.T) {
	for _, successText := range []string{"Verification passed, thank you!", "✅"} {
		t.Run(successText, func(t *testing.T) {
			for _, newestFirst := range []bool{true, false} {
				h := &Handler{chatID: 123, state: NewStateMachine()}
				request := *verificationExample()
				request.ID = 100
				processing := *verificationIncomingForTest(101, "The video is being processed, please wait...")
				success := *verificationIncomingForTest(102, successText)
				var history []telegram.NewMessage
				if newestFirst {
					history = []telegram.NewMessage{success, processing, request}
				} else {
					history = []telegram.NewMessage{request, processing, success}
				}
				calls := 0
				h.getVerificationHistoryFn = func(limit int) ([]telegram.NewMessage, error) {
					calls++
					if limit != 50 {
						t.Fatalf("history bound = %d, want 50", limit)
					}
					return history, nil
				}
				if err := h.CheckVerificationHistory(); err != nil {
					t.Fatalf("CheckVerificationHistory() error = %v", err)
				}
				if calls != 1 {
					t.Fatalf("history calls = %d, want 1", calls)
				}
				if h.state.IsWaitingVerification() {
					t.Fatalf("old request with later success (newestFirst=%v) stayed waiting, want resumed", newestFirst)
				}
			}
		})
	}
}

func TestVerificationHistoryRequestWithoutSuccessRemainsWaiting(t *testing.T) {
	h := &Handler{chatID: 123, state: NewStateMachine()}
	request := *verificationExample()
	request.ID = 100
	h.getVerificationHistoryFn = func(int) ([]telegram.NewMessage, error) {
		return []telegram.NewMessage{request}, nil
	}
	if err := h.CheckVerificationHistory(); err != nil {
		t.Fatalf("CheckVerificationHistory() error = %v", err)
	}
	mustBeWaiting(t, h)
}

func TestVerificationHistoryInvertedSuccessBeforeRequestStaysWaiting(t *testing.T) {
	h := &Handler{chatID: 123, state: NewStateMachine()}
	success := *verificationIncomingForTest(100, "Verification passed, thank you!")
	request := *verificationExample()
	request.ID = 102
	h.getVerificationHistoryFn = func(int) ([]telegram.NewMessage, error) {
		return []telegram.NewMessage{request, success}, nil
	}
	if err := h.CheckVerificationHistory(); err != nil {
		t.Fatalf("CheckVerificationHistory() error = %v", err)
	}
	mustBeWaiting(t, h)
}

func TestVerificationHistoryProcessingOnlyWithRequestStaysWait(t *testing.T) {
	h := &Handler{chatID: 123, state: NewStateMachine()}
	request := *verificationExample()
	request.ID = 100
	processing := *verificationIncomingForTest(101, "The video is being processed, please wait...")
	h.getVerificationHistoryFn = func(int) ([]telegram.NewMessage, error) {
		return []telegram.NewMessage{processing, request}, nil
	}
	if err := h.CheckVerificationHistory(); err != nil {
		t.Fatalf("CheckVerificationHistory() error = %v", err)
	}
	mustBeWaiting(t, h)
}

func TestVerificationHistorySuccessAloneDoesNotWait(t *testing.T) {
	h := &Handler{chatID: 123, state: NewStateMachine()}
	success := *verificationIncomingForTest(100, "✅")
	h.getVerificationHistoryFn = func(int) ([]telegram.NewMessage, error) {
		return []telegram.NewMessage{success}, nil
	}
	if err := h.CheckVerificationHistory(); err != nil {
		t.Fatalf("CheckVerificationHistory() error = %v", err)
	}
	if h.state.IsWaitingVerification() {
		t.Fatal("success alone triggered wait, want not waiting")
	}
}

func TestVerificationConcurrentDuplicateSuccessSafe(t *testing.T) {
	h := &Handler{chatID: 123, state: NewStateMachine()}
	defer h.Shutdown()
	_ = h.Handle(verificationExample())
	mustBeWaiting(t, h)
	_ = h.Handle(verificationIncomingForTest(102, "✅"))
	mustBeResumed(t, h)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(id int32) {
			defer wg.Done()
			_ = h.Handle(verificationIncomingForTest(id, "Verification passed, thank you!"))
			_ = h.lifecycleContext().Err()
			_ = h.state.IsWaitingVerification()
			_ = h.state.Enqueue(ProfileJob{Type: "menu_recovery"})
			select {
			case job := <-h.state.GetQueue():
				_ = job
			default:
			}
		}(int32(200 + i))
	}
	waitGroupWithTimeout(t, &wg, "concurrent duplicate success")
	if h.state.IsWaitingVerification() {
		t.Fatal("concurrent duplicate success re-entered wait")
	}
}
