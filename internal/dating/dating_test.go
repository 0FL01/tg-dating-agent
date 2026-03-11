package dating

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/0FL01/tg-dating-agent/internal/llm"
	"github.com/0FL01/tg-dating-agent/internal/standalone"
	"github.com/amarnathcjd/gogram/telegram"
)

const testSyncTimeout = 2 * time.Second

type blockingSummarizer struct {
	startedOnce  sync.Once
	canceledOnce sync.Once
	started      chan struct{}
	canceled     chan struct{}
	release      chan struct{}
}

type scriptedSummarizer struct {
	mu           sync.Mutex
	responses    []string
	prompts      []string
	callCount    int
	cancelOnCall int
	cancel       context.CancelFunc
}

func (s *scriptedSummarizer) snapshotCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callCount
}

func (s *scriptedSummarizer) SummarizeMultimodal(_ context.Context, _ string, prompt string, _ llm.MultimodalContent, _ float64) (string, error) {
	s.mu.Lock()
	call := s.callCount + 1
	s.callCount = call
	s.prompts = append(s.prompts, prompt)
	if call > len(s.responses) {
		s.mu.Unlock()
		return "", errors.New("unexpected summarize call")
	}
	response := s.responses[call-1]
	shouldCancel := s.cancel != nil && s.cancelOnCall == call
	s.mu.Unlock()

	if shouldCancel {
		s.cancel()
	}

	return response, nil
}

type auditCall struct {
	mbti        string
	profileText string
	prompt      string
	response    string
}

type stubReplyAuditLogger struct {
	mu    sync.Mutex
	calls []auditCall
	err   error
}

type stubProfileDedupeStore struct {
	mu          sync.Mutex
	isActive    bool
	isActiveErr error
	markErr     error
	activeCalls []string
	markCalls   []string
}

func (s *stubReplyAuditLogger) Append(mbti, profileText, prompt, response string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls = append(s.calls, auditCall{mbti: mbti, profileText: profileText, prompt: prompt, response: response})
	return s.err
}

func (s *stubReplyAuditLogger) snapshotCalls() []auditCall {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]auditCall, len(s.calls))
	copy(out, s.calls)
	return out
}

func (s *stubProfileDedupeStore) IsActive(_ context.Context, profileHash string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.activeCalls = append(s.activeCalls, profileHash)
	if s.isActiveErr != nil {
		return false, s.isActiveErr
	}

	return s.isActive, nil
}

func (s *stubProfileDedupeStore) MarkProcessed(_ context.Context, profileHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.markCalls = append(s.markCalls, profileHash)
	return s.markErr
}

func (s *stubProfileDedupeStore) snapshotActiveCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]string, len(s.activeCalls))
	copy(out, s.activeCalls)
	return out
}

func (s *stubProfileDedupeStore) snapshotMarkCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]string, len(s.markCalls))
	copy(out, s.markCalls)
	return out
}

func (s *blockingSummarizer) SummarizeMultimodal(ctx context.Context, _ string, _ string, _ llm.MultimodalContent, _ float64) (string, error) {
	s.startedOnce.Do(func() {
		close(s.started)
	})

	select {
	case <-ctx.Done():
		s.canceledOnce.Do(func() {
			close(s.canceled)
		})
		return "", ctx.Err()
	case <-s.release:
		return "INTJ", nil
	}
}

func TestTruncateMessageASCII(t *testing.T) {
	tests := []struct {
		name   string
		msg    string
		maxLen int
		want   string
	}{
		{
			name:   "shorter than limit unchanged",
			msg:    "hello",
			maxLen: 10,
			want:   "hello",
		},
		{
			name:   "truncate by last space when far enough",
			msg:    "hello world from bot",
			maxLen: 12,
			want:   "hello world",
		},
		{
			name:   "fallback to hard truncation when last space too early",
			msg:    "hi therefriend",
			maxLen: 8,
			want:   "hi there",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncateMessage(tt.msg, tt.maxLen); got != tt.want {
				t.Fatalf("truncateMessage(%q, %d) = %q, want %q", tt.msg, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestGetBotPeerEmptyCache(t *testing.T) {
	h := &Handler{chatID: 123456789}

	peer, ok := h.getBotPeer()
	if ok {
		t.Fatal("getBotPeer() ok = true, want false")
	}

	if peer != nil {
		t.Fatalf("getBotPeer() peer = %#v, want nil", peer)
	}
}

func TestCacheBotPeerStoresResolvedPeerForTargetChat(t *testing.T) {
	h := &Handler{chatID: 123456789}
	resolved := &telegram.InputPeerUser{UserID: 42, AccessHash: 77}

	h.cacheBotPeer(&telegram.NewMessage{
		Peer: resolved,
		Message: &telegram.MessageObj{
			PeerID: &telegram.PeerUser{UserID: h.chatID},
		},
	})

	peer, ok := h.getBotPeer()
	if !ok {
		t.Fatal("getBotPeer() ok = false, want true")
	}

	userPeer, typeOK := peer.(*telegram.InputPeerUser)
	if !typeOK {
		t.Fatalf("getBotPeer() type = %T, want *telegram.InputPeerUser", peer)
	}

	if userPeer.UserID != resolved.UserID || userPeer.AccessHash != resolved.AccessHash {
		t.Fatalf("cached peer = %#v, want %#v", userPeer, resolved)
	}
}

func TestCacheBotPeerIgnoresMismatchedOrMissingPeer(t *testing.T) {
	h := &Handler{chatID: 123456789}
	initial := &telegram.InputPeerUser{UserID: 1, AccessHash: 2}

	h.cacheBotPeer(&telegram.NewMessage{
		Peer: initial,
		Message: &telegram.MessageObj{
			PeerID: &telegram.PeerUser{UserID: h.chatID},
		},
	})

	h.cacheBotPeer(&telegram.NewMessage{
		Peer: &telegram.InputPeerUser{UserID: 999, AccessHash: 999},
		Message: &telegram.MessageObj{
			PeerID: &telegram.PeerUser{UserID: 555},
		},
	})

	h.cacheBotPeer(&telegram.NewMessage{
		Peer: nil,
		Message: &telegram.MessageObj{
			PeerID: &telegram.PeerUser{UserID: h.chatID},
		},
	})

	peer, ok := h.getBotPeer()
	if !ok {
		t.Fatal("getBotPeer() ok = false, want true")
	}

	userPeer, typeOK := peer.(*telegram.InputPeerUser)
	if !typeOK {
		t.Fatalf("getBotPeer() type = %T, want *telegram.InputPeerUser", peer)
	}

	if userPeer.UserID != initial.UserID || userPeer.AccessHash != initial.AccessHash {
		t.Fatalf("cached peer = %#v, want %#v", userPeer, initial)
	}
}

func TestHandleAlbumCachesBotPeerBeforeEarlyReturn(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}
	h.state.SetState(StateStopped)
	resolved := &telegram.InputPeerUser{UserID: 42, AccessHash: 77}

	err := h.HandleAlbum(&telegram.Album{Messages: []*telegram.NewMessage{
		{
			Peer: resolved,
			Message: &telegram.MessageObj{
				PeerID: &telegram.PeerUser{UserID: h.chatID},
			},
		},
	}})
	if err != nil {
		t.Fatalf("HandleAlbum() error = %v", err)
	}

	peer, ok := h.getBotPeer()
	if !ok {
		t.Fatal("getBotPeer() ok = false, want true")
	}

	userPeer, typeOK := peer.(*telegram.InputPeerUser)
	if !typeOK {
		t.Fatalf("getBotPeer() type = %T, want *telegram.InputPeerUser", peer)
	}

	if userPeer.UserID != resolved.UserID || userPeer.AccessHash != resolved.AccessHash {
		t.Fatalf("cached peer = %#v, want %#v", userPeer, resolved)
	}
}

func TestHandleOwnProfileSkipKeepsContextAcrossWrongFirstMedia(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}

	marker := &telegram.NewMessage{ID: 100, Message: &telegram.MessageObj{
		Message: "Your profile",
		PeerID:  &telegram.PeerUser{UserID: h.chatID},
	}}
	if err := h.Handle(marker); err != nil {
		t.Fatalf("Handle(marker) error = %v", err)
	}

	wrongFirstMedia := &telegram.NewMessage{ID: 110, Message: &telegram.MessageObj{
		Media:  &telegram.MessageMediaPhoto{},
		PeerID: &telegram.PeerUser{UserID: h.chatID},
	}}
	if err := h.Handle(wrongFirstMedia); err != nil {
		t.Fatalf("Handle(wrongFirstMedia) error = %v", err)
	}

	ownProfileMedia := &telegram.NewMessage{ID: 101, Message: &telegram.MessageObj{
		Media:  &telegram.MessageMediaPhoto{},
		PeerID: &telegram.PeerUser{UserID: h.chatID},
	}}
	if err := h.Handle(ownProfileMedia); err != nil {
		t.Fatalf("Handle(ownProfileMedia) error = %v", err)
	}

	job1 := mustDequeueJob(t, h.state)
	if job1.Type != "message" || job1.Message == nil || job1.Message.ID != 110 {
		t.Fatalf("first queued job = %+v, want media message id 110", job1)
	}

	job2 := mustDequeueJob(t, h.state)
	if job2.Type != "menu_recovery" {
		t.Fatalf("second queued job type = %q, want %q", job2.Type, "menu_recovery")
	}
}

func TestHandleAlbumOwnProfileSkipUsesSameCorrelationRule(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}
	h.state.MarkOwnProfileSkip(300, time.Now())

	wrongFirstAlbum := &telegram.Album{Messages: []*telegram.NewMessage{{
		ID: 310,
		Message: &telegram.MessageObj{
			Media:  &telegram.MessageMediaPhoto{},
			PeerID: &telegram.PeerUser{UserID: h.chatID},
		},
	}}}
	if err := h.HandleAlbum(wrongFirstAlbum); err != nil {
		t.Fatalf("HandleAlbum(wrongFirstAlbum) error = %v", err)
	}

	ownProfileAlbum := &telegram.Album{Messages: []*telegram.NewMessage{{
		ID: 301,
		Message: &telegram.MessageObj{
			Media:  &telegram.MessageMediaPhoto{},
			PeerID: &telegram.PeerUser{UserID: h.chatID},
		},
	}}}
	if err := h.HandleAlbum(ownProfileAlbum); err != nil {
		t.Fatalf("HandleAlbum(ownProfileAlbum) error = %v", err)
	}

	job1 := mustDequeueJob(t, h.state)
	if job1.Type != "album" || job1.Album == nil || len(job1.Album.Messages) == 0 || job1.Album.Messages[0].ID != 310 {
		t.Fatalf("first queued job = %+v, want album with id 310", job1)
	}
	if job1.ProfileMessageID != 310 {
		t.Fatalf("album job ProfileMessageID = %d, want 310", job1.ProfileMessageID)
	}

	job2 := mustDequeueJob(t, h.state)
	if job2.Type != "menu_recovery" {
		t.Fatalf("second queued job type = %q, want %q", job2.Type, "menu_recovery")
	}
}

func TestHandleGroupedMediaSkipsMessageEnqueue(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}

	msg := &telegram.NewMessage{ID: 401, Message: &telegram.MessageObj{
		Media:     &telegram.MessageMediaPhoto{},
		GroupedID: 777,
		PeerID:    &telegram.PeerUser{UserID: h.chatID},
	}}

	if err := h.Handle(msg); err != nil {
		t.Fatalf("Handle(grouped media) error = %v", err)
	}

	mustQueueEmpty(t, h.state)
}

func TestHandleGroupedMediaStoresCaptionForAlbumFallback(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}

	msg := &telegram.NewMessage{ID: 403, Message: &telegram.MessageObj{
		Message:   "grouped caption",
		Media:     &telegram.MessageMediaPhoto{},
		GroupedID: 778,
		PeerID:    &telegram.PeerUser{UserID: h.chatID},
	}}

	if err := h.Handle(msg); err != nil {
		t.Fatalf("Handle(grouped media with caption) error = %v", err)
	}

	mustQueueEmpty(t, h.state)

	got, ok := h.state.ConsumeGroupedCaption(778, time.Now())
	if !ok {
		t.Fatal("ConsumeGroupedCaption() ok = false, want true")
	}
	if got != "grouped caption" {
		t.Fatalf("ConsumeGroupedCaption() = %q, want %q", got, "grouped caption")
	}
}

func TestHandleNonGroupedMediaStillEnqueuesMessageJob(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}

	msg := &telegram.NewMessage{ID: 402, Message: &telegram.MessageObj{
		Media:  &telegram.MessageMediaPhoto{},
		PeerID: &telegram.PeerUser{UserID: h.chatID},
	}}

	if err := h.Handle(msg); err != nil {
		t.Fatalf("Handle(non-grouped media) error = %v", err)
	}

	job := mustDequeueJob(t, h.state)
	if job.Type != "message" || job.Message == nil || job.Message.ID != 402 {
		t.Fatalf("queued job = %+v, want message job id 402", job)
	}
	if job.ProfileMessageID != 402 {
		t.Fatalf("message job ProfileMessageID = %d, want 402", job.ProfileMessageID)
	}
}

func TestHandleTextOnlyProfileActionKeyboardEnqueuesMessageJob(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}

	msg := &telegram.NewMessage{ID: 406, Message: &telegram.MessageObj{
		Message: "Андрей, subscribe to my channel ...",
		ReplyMarkup: &telegram.ReplyKeyboardMarkup{Rows: []*telegram.KeyboardButtonRow{{Buttons: []telegram.KeyboardButton{
			&telegram.KeyboardButtonObj{Text: ButtonLike},
			&telegram.KeyboardButtonObj{Text: ButtonLikeMessage},
			&telegram.KeyboardButtonObj{Text: ButtonDislike},
		}}}},
		PeerID: &telegram.PeerUser{UserID: h.chatID},
	}}

	if err := h.Handle(msg); err != nil {
		t.Fatalf("Handle(text-only profile) error = %v", err)
	}

	job := mustDequeueJob(t, h.state)
	if job.Type != "message" || job.Message == nil || job.Message.ID != 406 {
		t.Fatalf("queued job = %+v, want message job id 406", job)
	}
	if job.ProfileMessageID != 406 {
		t.Fatalf("message job ProfileMessageID = %d, want 406", job.ProfileMessageID)
	}
}

func TestHandleTextOnlyUnrelatedKeyboardDoesNotEnqueueProfileJob(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}

	msg := &telegram.NewMessage{ID: 407, Message: &telegram.MessageObj{
		Message: "Андрей, subscribe to my channel ...",
		ReplyMarkup: &telegram.ReplyKeyboardMarkup{Rows: []*telegram.KeyboardButtonRow{{Buttons: []telegram.KeyboardButton{
			&telegram.KeyboardButtonObj{Text: ButtonViewProfiles},
			&telegram.KeyboardButtonObj{Text: ButtonMyProfile},
		}}}},
		PeerID: &telegram.PeerUser{UserID: h.chatID},
	}}

	if err := h.Handle(msg); err != nil {
		t.Fatalf("Handle(text-only unrelated keyboard) error = %v", err)
	}

	mustQueueEmpty(t, h.state)
}

func TestHandleViewingProfilesInterstitialWithUnrelatedMarkupEnqueuesStuckRecovery(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}
	h.state.SetState(StateViewingProfiles)

	msg := &telegram.NewMessage{ID: 408, Message: &telegram.MessageObj{
		Message: "Андрей, subscribe to my channel 👉 @Leomatchglobal",
		ReplyMarkup: &telegram.ReplyKeyboardMarkup{Rows: []*telegram.KeyboardButtonRow{{Buttons: []telegram.KeyboardButton{
			&telegram.KeyboardButtonObj{Text: ButtonMyProfile},
			&telegram.KeyboardButtonObj{Text: "Promo"},
		}}}},
		PeerID: &telegram.PeerUser{UserID: h.chatID},
	}}

	if err := h.Handle(msg); err != nil {
		t.Fatalf("Handle(interstitial) error = %v", err)
	}

	job := mustDequeueJob(t, h.state)
	if job.Type != "stuck_recovery" || job.Message == nil || job.Message.ID != 408 {
		t.Fatalf("queued job = %+v, want stuck_recovery job id 408", job)
	}

	if got := h.state.GetState(); got != StateViewingProfiles {
		t.Fatalf("state after Handle(interstitial) = %v, want %v", got, StateViewingProfiles)
	}

	select {
	case <-h.state.ShouldQuit():
		t.Fatal("quit channel closed for interstitial recovery")
	default:
	}
}

func TestHandleViewingProfilesProfileActionKeyboardStillEnqueuesProfileMessage(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}
	h.state.SetState(StateViewingProfiles)

	msg := &telegram.NewMessage{ID: 409, Message: &telegram.MessageObj{
		Message: "Profile text",
		ReplyMarkup: &telegram.ReplyKeyboardMarkup{Rows: []*telegram.KeyboardButtonRow{{Buttons: []telegram.KeyboardButton{
			&telegram.KeyboardButtonObj{Text: ButtonLike},
			&telegram.KeyboardButtonObj{Text: ButtonLikeMessage},
			&telegram.KeyboardButtonObj{Text: ButtonDislike},
		}}}},
		PeerID: &telegram.PeerUser{UserID: h.chatID},
	}}

	if err := h.Handle(msg); err != nil {
		t.Fatalf("Handle(profile-action text) error = %v", err)
	}

	job := mustDequeueJob(t, h.state)
	if job.Type != "message" || job.Message == nil || job.Message.ID != 409 {
		t.Fatalf("queued job = %+v, want message job id 409", job)
	}
	if job.ProfileMessageID != 409 {
		t.Fatalf("message job ProfileMessageID = %d, want 409", job.ProfileMessageID)
	}
}

func TestHandleViewingProfilesPlainTextWithoutMarkupRemembersVisibleProfileForReciprocalFinal(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}
	h.state.SetState(StateViewingProfiles)

	profileText := "Anna, 27 - Loves books and coffee"
	plainProfile := &telegram.NewMessage{ID: 410, Message: &telegram.MessageObj{
		Message: profileText,
		PeerID:  &telegram.PeerUser{UserID: h.chatID},
	}}

	if err := h.Handle(plainProfile); err != nil {
		t.Fatalf("Handle(plain profile text) error = %v", err)
	}

	job := mustDequeueJob(t, h.state)
	if job.Type != "stuck_recovery" || job.Message == nil || job.Message.ID != 410 {
		t.Fatalf("queued job = %+v, want stuck_recovery job id 410", job)
	}

	var gotPayload ReciprocalLikeFinalPayload
	deliverCalls := 0
	h.deliverReciprocalLikeFinalFn = func(_ context.Context, payload ReciprocalLikeFinalPayload, _ []ReciprocalLikePhoto) error {
		deliverCalls++
		gotPayload = payload
		return nil
	}

	startChatting := &telegram.NewMessage{ID: 411, Message: &telegram.MessageObj{Message: "Start chatting: https://t.me/final_user"}}
	if err := h.Handle(startChatting); err != nil {
		t.Fatalf("Handle(start chatting) error = %v", err)
	}

	if deliverCalls != 1 {
		t.Fatalf("deliverReciprocalLikeFinalFn calls = %d, want 1", deliverCalls)
	}
	if gotPayload.ProfileText != profileText {
		t.Fatalf("payload.ProfileText = %q, want %q", gotPayload.ProfileText, profileText)
	}
}

func TestHandleViewingProfilesPlainTextFallbackPreservesExistingVisibleMediaSource(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}
	h.state.SetState(StateViewingProfiles)

	mediaMessage := &telegram.NewMessage{ID: 500, Message: &telegram.MessageObj{
		Message: "Old profile text",
		Media:   &telegram.MessageMediaPhoto{},
		PeerID:  &telegram.PeerUser{UserID: h.chatID},
	}}
	h.rememberVisibleProfileMessage("Old profile text", mediaMessage.ID, mediaMessage)

	plainProfile := &telegram.NewMessage{ID: 501, Message: &telegram.MessageObj{
		Message: "Updated profile text without keyboard",
		PeerID:  &telegram.PeerUser{UserID: h.chatID},
	}}

	if err := h.Handle(plainProfile); err != nil {
		t.Fatalf("Handle(plain profile fallback) error = %v", err)
	}

	entry, ok := h.state.GetLatestVisibleProfileCardBefore(999, time.Now())
	if !ok {
		t.Fatal("GetLatestVisibleProfileCardBefore() ok = false, want true")
	}
	if entry.ProfileText != plainProfile.Text() {
		t.Fatalf("visible profile text = %q, want %q", entry.ProfileText, plainProfile.Text())
	}
	if entry.MediaSource.Message != mediaMessage {
		t.Fatal("visible profile media source message was not preserved")
	}
}

func TestProcessJobStuckRecoveryEscalationSequenceReachesStartFallback(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}
	h.state.SetState(StateViewingProfiles)
	h.setBotPeer(&telegram.InputPeerUser{UserID: h.chatID, AccessHash: 1})

	actions := make([]string, 0, 4)
	h.clickButtonFn = func(_ context.Context, buttonText string) error {
		actions = append(actions, "button:"+buttonText)
		return nil
	}
	h.sendMessageFn = func(_ context.Context, _ telegram.InputPeer, msg string) error {
		actions = append(actions, "msg:"+msg)
		return nil
	}

	for i := 0; i < 3; i++ {
		if err := h.processJob(context.Background(), ProfileJob{Type: "stuck_recovery"}); err != nil {
			t.Fatalf("processJob(stuck_recovery #%d) error = %v", i+1, err)
		}
	}

	want := []string{"button:" + ButtonViewProfiles, "button:" + ButtonViewProfiles, "msg:/start"}
	if len(actions) != len(want) {
		t.Fatalf("actions len = %d, want %d (%v)", len(actions), len(want), want)
	}
	for i := range want {
		if actions[i] != want[i] {
			t.Fatalf("actions[%d] = %q, want %q", i, actions[i], want[i])
		}
	}

	if got := h.state.GetState(); got != StateIdle {
		t.Fatalf("state after escalation fallback = %v, want %v", got, StateIdle)
	}
}

func TestStuckRecoveryEscalationResetsAfterProgressPath(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}
	h.state.SetState(StateViewingProfiles)
	h.setBotPeer(&telegram.InputPeerUser{UserID: h.chatID, AccessHash: 1})

	actions := make([]string, 0, 4)
	h.clickButtonFn = func(_ context.Context, buttonText string) error {
		actions = append(actions, "button:"+buttonText)
		return nil
	}
	h.sendMessageFn = func(_ context.Context, _ telegram.InputPeer, msg string) error {
		actions = append(actions, "msg:"+msg)
		return nil
	}

	if err := h.processJob(context.Background(), ProfileJob{Type: "stuck_recovery"}); err != nil {
		t.Fatalf("processJob(stuck_recovery first) error = %v", err)
	}

	profileActionMsg := &telegram.NewMessage{ID: 490, Message: &telegram.MessageObj{
		Message: "Profile text",
		ReplyMarkup: &telegram.ReplyKeyboardMarkup{Rows: []*telegram.KeyboardButtonRow{{Buttons: []telegram.KeyboardButton{
			&telegram.KeyboardButtonObj{Text: ButtonLike},
			&telegram.KeyboardButtonObj{Text: ButtonLikeMessage},
			&telegram.KeyboardButtonObj{Text: ButtonDislike},
		}}}},
		PeerID: &telegram.PeerUser{UserID: h.chatID},
	}}

	if err := h.Handle(profileActionMsg); err != nil {
		t.Fatalf("Handle(profileActionMsg) error = %v", err)
	}

	job := mustDequeueJob(t, h.state)
	if job.Type != "message" {
		t.Fatalf("queued job type = %q, want %q", job.Type, "message")
	}

	if err := h.processJob(context.Background(), ProfileJob{Type: "stuck_recovery"}); err != nil {
		t.Fatalf("processJob(stuck_recovery second after progress) error = %v", err)
	}
	if err := h.processJob(context.Background(), ProfileJob{Type: "stuck_recovery"}); err != nil {
		t.Fatalf("processJob(stuck_recovery third after progress) error = %v", err)
	}

	for _, action := range actions {
		if action == "msg:/start" {
			t.Fatalf("unexpected /start fallback after progress reset, actions=%v", actions)
		}
	}
}

func TestProcessJobStuckRecoverySkipsActionsWhenPaused(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}
	h.state.SetState(StateViewingProfiles)
	h.state.PauseFor(time.Hour)
	h.setBotPeer(&telegram.InputPeerUser{UserID: h.chatID, AccessHash: 1})

	actionCalls := 0
	h.clickButtonFn = func(_ context.Context, _ string) error {
		actionCalls++
		return nil
	}
	h.sendMessageFn = func(_ context.Context, _ telegram.InputPeer, _ string) error {
		actionCalls++
		return nil
	}

	if err := h.processJob(context.Background(), ProfileJob{Type: "stuck_recovery"}); err != nil {
		t.Fatalf("processJob(stuck_recovery) error = %v", err)
	}

	if actionCalls != 0 {
		t.Fatalf("stuck recovery actions while paused = %d, want 0", actionCalls)
	}
}

func TestProcessJobStuckRecoverySkipsWhenFresherProfilePending(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}
	h.state.SetState(StateViewingProfiles)
	h.setBotPeer(&telegram.InputPeerUser{UserID: h.chatID, AccessHash: 1})

	// Put escalation at level 2 to verify skip path resets it.
	h.state.NextStuckRecoveryEscalation()
	h.state.NextStuckRecoveryEscalation()

	if ok := h.state.Enqueue(ProfileJob{Type: "message", ProfileMessageID: 700}); !ok {
		t.Fatal("Enqueue(profile id=700) = false, want true")
	}

	actionCalls := 0
	h.clickButtonFn = func(_ context.Context, _ string) error {
		actionCalls++
		return nil
	}
	h.sendMessageFn = func(_ context.Context, _ telegram.InputPeer, _ string) error {
		actionCalls++
		return nil
	}

	if err := h.processJob(context.Background(), ProfileJob{Type: "stuck_recovery"}); err != nil {
		t.Fatalf("processJob(stuck_recovery) error = %v", err)
	}

	if actionCalls != 0 {
		t.Fatalf("stuck recovery actions with pending profile = %d, want 0", actionCalls)
	}

	if got := h.state.NextStuckRecoveryEscalation(); got != 1 {
		t.Fatalf("NextStuckRecoveryEscalation() after pending-profile skip = %d, want 1", got)
	}
}

func TestHandleProfileActionKeyboardRoutingUnaffectedByStuckEscalationState(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}
	h.state.SetState(StateViewingProfiles)
	h.state.NextStuckRecoveryEscalation()
	h.state.NextStuckRecoveryEscalation()

	msg := &telegram.NewMessage{ID: 491, Message: &telegram.MessageObj{
		Message: "Profile text",
		ReplyMarkup: &telegram.ReplyKeyboardMarkup{Rows: []*telegram.KeyboardButtonRow{{Buttons: []telegram.KeyboardButton{
			&telegram.KeyboardButtonObj{Text: ButtonLike},
			&telegram.KeyboardButtonObj{Text: ButtonLikeMessage},
			&telegram.KeyboardButtonObj{Text: ButtonDislike},
		}}}},
		PeerID: &telegram.PeerUser{UserID: h.chatID},
	}}

	if err := h.Handle(msg); err != nil {
		t.Fatalf("Handle(profile-action text) error = %v", err)
	}

	job := mustDequeueJob(t, h.state)
	if job.Type != "message" || job.Message == nil || job.Message.ID != 491 {
		t.Fatalf("queued job = %+v, want message job id 491", job)
	}
}

func TestHandleSkipsStartupOwnProfileWithoutMarkerThenRecoversFromMenu(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}
	h.state.ArmStartupOwnProfileSkip(time.Now())

	ownProfileMedia := &telegram.NewMessage{ID: 500, Message: &telegram.MessageObj{
		Message: "self profile text",
		Media:   &telegram.MessageMediaPhoto{},
		PeerID:  &telegram.PeerUser{UserID: h.chatID},
	}}
	if err := h.Handle(ownProfileMedia); err != nil {
		t.Fatalf("Handle(ownProfileMedia) error = %v", err)
	}
	mustQueueEmpty(t, h.state)

	mainMenu := &telegram.NewMessage{ID: 501, Message: &telegram.MessageObj{
		Message: "1. View profiles.\n2. Edit my profile.",
		PeerID:  &telegram.PeerUser{UserID: h.chatID},
	}}
	if err := h.Handle(mainMenu); err != nil {
		t.Fatalf("Handle(mainMenu) error = %v", err)
	}

	job := mustDequeueJob(t, h.state)
	if job.Type != "menu_recovery" {
		t.Fatalf("queued job type = %q, want %q", job.Type, "menu_recovery")
	}
}

func TestHandleAlbumSkipsStartupOwnProfileWithoutMarker(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}
	h.state.ArmStartupOwnProfileSkip(time.Now())

	album := &telegram.Album{
		Messages: []*telegram.NewMessage{
			{
				ID: 600,
				Message: &telegram.MessageObj{
					Media:  &telegram.MessageMediaPhoto{},
					PeerID: &telegram.PeerUser{UserID: h.chatID},
				},
			},
		},
	}

	if err := h.HandleAlbum(album); err != nil {
		t.Fatalf("HandleAlbum() error = %v", err)
	}

	mustQueueEmpty(t, h.state)
}

func TestResolveAlbumProfileTextFallsBackToGroupedCaption(t *testing.T) {
	h := &Handler{state: NewStateMachine()}
	h.state.RememberGroupedCaption(991, "caption from grouped message", 41, time.Now())

	album := &telegram.Album{
		Messages: []*telegram.NewMessage{
			{
				ID: 42,
				Message: &telegram.MessageObj{
					Media:     &telegram.MessageMediaPhoto{},
					GroupedID: 991,
				},
			},
		},
	}

	got := h.resolveAlbumProfileText(album)
	if got != "caption from grouped message" {
		t.Fatalf("resolveAlbumProfileText() = %q, want %q", got, "caption from grouped message")
	}

	if got := h.resolveAlbumProfileText(album); got != "" {
		t.Fatalf("resolveAlbumProfileText() second call = %q, want empty after consume", got)
	}
}

func TestFirstAlbumGroupedIDUsesLowestMessageID(t *testing.T) {
	album := &telegram.Album{
		Messages: []*telegram.NewMessage{
			{ID: 300, Message: &telegram.MessageObj{GroupedID: 3}},
			{ID: 250, Message: &telegram.MessageObj{GroupedID: 2}},
		},
	}

	if got := firstAlbumGroupedID(album); got != 2 {
		t.Fatalf("firstAlbumGroupedID() = %d, want 2", got)
	}
}

func TestMaxAlbumMessageIDUsesHighestMessageID(t *testing.T) {
	album := &telegram.Album{
		Messages: []*telegram.NewMessage{
			{ID: 250, Message: &telegram.MessageObj{}},
			{ID: 300, Message: &telegram.MessageObj{}},
			{ID: 275, Message: &telegram.MessageObj{}},
		},
	}

	if got := maxAlbumMessageID(album); got != 300 {
		t.Fatalf("maxAlbumMessageID() = %d, want 300", got)
	}
}

func TestBootstrapWithActionsSequencingWhileHandlersMutateOwnProfileState(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}

	startEntered := make(chan struct{})
	allowStart := make(chan struct{})
	searchCalled := make(chan struct{}, 1)
	done := make(chan error, 1)

	go func() {
		done <- h.bootstrapWithActions(func() error {
			close(startEntered)
			<-allowStart
			return nil
		}, func() error {
			searchCalled <- struct{}{}
			return nil
		})
	}()

	mustReceiveSignal(t, startEntered, "bootstrap sendStart entry")

	marker := &telegram.NewMessage{ID: 100, Message: &telegram.MessageObj{
		Message: "Your profile",
		PeerID:  &telegram.PeerUser{UserID: h.chatID},
	}}
	if err := h.Handle(marker); err != nil {
		t.Fatalf("Handle(marker) error = %v", err)
	}

	ownProfileMedia := &telegram.NewMessage{ID: 101, Message: &telegram.MessageObj{
		Media:  &telegram.MessageMediaPhoto{},
		PeerID: &telegram.PeerUser{UserID: h.chatID},
	}}
	if err := h.Handle(ownProfileMedia); err != nil {
		t.Fatalf("Handle(ownProfileMedia) error = %v", err)
	}

	profileMedia := &telegram.NewMessage{ID: 105, Message: &telegram.MessageObj{
		Media:  &telegram.MessageMediaPhoto{},
		PeerID: &telegram.PeerUser{UserID: h.chatID},
	}}
	if err := h.Handle(profileMedia); err != nil {
		t.Fatalf("Handle(profileMedia) error = %v", err)
	}

	select {
	case <-searchCalled:
		t.Fatal("startSearch called before sendStart unblocked")
	default:
	}

	close(allowStart)

	if err := mustReceiveError(t, done, "bootstrap completion"); err != nil {
		t.Fatalf("bootstrapWithActions() error = %v, want nil", err)
	}

	select {
	case <-searchCalled:
	default:
		t.Fatal("startSearch was not called")
	}

	job1 := mustDequeueJob(t, h.state)
	if job1.Type != "menu_recovery" {
		t.Fatalf("first queued job type = %q, want %q", job1.Type, "menu_recovery")
	}

	job2 := mustDequeueJob(t, h.state)
	if job2.Type != "message" || job2.Message == nil || job2.Message.ID != 105 {
		t.Fatalf("second queued job = %+v, want media message id 105", job2)
	}
}

func TestHandleOwnProfileSkipConcurrentInterleavingQueuesExpectedOutcomes(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}

	marker := &telegram.NewMessage{ID: 100, Message: &telegram.MessageObj{
		Message: "Your profile",
		PeerID:  &telegram.PeerUser{UserID: h.chatID},
	}}
	if err := h.Handle(marker); err != nil {
		t.Fatalf("Handle(marker) error = %v", err)
	}

	wrongFirstMedia := &telegram.NewMessage{ID: 110, Message: &telegram.MessageObj{
		Media:  &telegram.MessageMediaPhoto{},
		PeerID: &telegram.PeerUser{UserID: h.chatID},
	}}
	ownProfileMedia := &telegram.NewMessage{ID: 101, Message: &telegram.MessageObj{
		Media:  &telegram.MessageMediaPhoto{},
		PeerID: &telegram.PeerUser{UserID: h.chatID},
	}}

	start := make(chan struct{})
	errCh := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		<-start
		errCh <- h.Handle(wrongFirstMedia)
	}()

	go func() {
		defer wg.Done()
		<-start
		errCh <- h.Handle(ownProfileMedia)
	}()

	close(start)
	waitGroupWithTimeout(t, &wg, "concurrent own-profile handlers")
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("Handle() error = %v, want nil", err)
		}
	}

	jobs := []ProfileJob{mustDequeueJob(t, h.state), mustDequeueJob(t, h.state)}
	seenMedia := false
	seenMenuRecovery := false
	for _, job := range jobs {
		switch job.Type {
		case "message":
			if job.Message == nil || job.Message.ID != 110 {
				t.Fatalf("queued message job = %+v, want media message id 110", job)
			}
			if seenMedia {
				t.Fatalf("duplicate message job detected: %+v", jobs)
			}
			seenMedia = true
		case "menu_recovery":
			if seenMenuRecovery {
				t.Fatalf("duplicate menu_recovery job detected: %+v", jobs)
			}
			seenMenuRecovery = true
		default:
			t.Fatalf("unexpected queued job type %q in %+v", job.Type, jobs)
		}
	}

	if !seenMedia || !seenMenuRecovery {
		t.Fatalf("queued jobs = %+v, want one message(id=110) and one menu_recovery", jobs)
	}
}

func TestBotPeerCacheConcurrentReadWrite(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}

	makeMsg := func(userID int64, accessHash int64) *telegram.NewMessage {
		return &telegram.NewMessage{
			Peer: &telegram.InputPeerUser{UserID: userID, AccessHash: accessHash},
			Message: &telegram.MessageObj{
				PeerID: &telegram.PeerUser{UserID: h.chatID},
			},
		}
	}

	h.cacheBotPeer(makeMsg(1, 1))

	const writers = 8
	const readers = 8
	const iterations = 200

	errCh := make(chan error, readers)
	var wg sync.WaitGroup

	for w := range writers {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range iterations {
				userID := int64(w*iterations + i + 2)
				h.cacheBotPeer(makeMsg(userID, userID+1000))
			}
		}()
	}

	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				peer, ok := h.getBotPeer()
				if !ok || peer == nil {
					errCh <- errors.New("getBotPeer() returned empty cache during concurrent access")
					return
				}
				if _, typeOK := peer.(*telegram.InputPeerUser); !typeOK {
					errCh <- errors.New("getBotPeer() returned unexpected peer type")
					return
				}
			}
		}()
	}

	waitGroupWithTimeout(t, &wg, "bot peer cache concurrent read/write workers")
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}

	final := makeMsg(4242, 7777)
	h.cacheBotPeer(final)

	peer, ok := h.getBotPeer()
	if !ok {
		t.Fatal("getBotPeer() ok = false, want true")
	}

	userPeer, typeOK := peer.(*telegram.InputPeerUser)
	if !typeOK {
		t.Fatalf("getBotPeer() type = %T, want *telegram.InputPeerUser", peer)
	}

	if userPeer.UserID != 4242 || userPeer.AccessHash != 7777 {
		t.Fatalf("cached peer = %#v, want UserID=4242 AccessHash=7777", userPeer)
	}
}

func TestSelectAlbumTextSourceMessagePrefersPhotoCaptionOwnership(t *testing.T) {
	messages := []*telegram.NewMessage{
		{ID: 1, Message: &telegram.MessageObj{Message: "text-only intro"}},
		{ID: 10, Message: &telegram.MessageObj{Message: "photo caption", Media: &telegram.MessageMediaPhoto{}}},
		{ID: 12, Message: &telegram.MessageObj{Message: "later caption", Media: &telegram.MessageMediaPhoto{}}},
	}

	got := profileTextFromAlbumMessages(messages)
	if got != "photo caption" {
		t.Fatalf("profileTextFromAlbumMessages() = %q, want %q", got, "photo caption")
	}
}

func TestSelectAlbumTextSourceMessageUsesMessageIDOrdering(t *testing.T) {
	messages := []*telegram.NewMessage{
		{ID: 200, Message: &telegram.MessageObj{Message: "second caption", Media: &telegram.MessageMediaPhoto{}}},
		{ID: 150, Message: &telegram.MessageObj{Message: "first caption", Media: &telegram.MessageMediaPhoto{}}},
	}

	got := profileTextFromAlbumMessages(messages)
	if got != "first caption" {
		t.Fatalf("profileTextFromAlbumMessages() = %q, want %q", got, "first caption")
	}
}

func TestSelectAlbumTextSourceMessageFallsBackToTextOnly(t *testing.T) {
	messages := []*telegram.NewMessage{
		{ID: 99, Message: &telegram.MessageObj{Message: "later text-only"}},
		{ID: 80, Message: &telegram.MessageObj{Message: "earlier text-only"}},
	}

	got := profileTextFromAlbumMessages(messages)
	if got != "earlier text-only" {
		t.Fatalf("profileTextFromAlbumMessages() = %q, want %q", got, "earlier text-only")
	}
}

func mustDequeueJob(t *testing.T, sm *StateMachine) ProfileJob {
	t.Helper()

	select {
	case job := <-sm.GetQueue():
		return job
	default:
		t.Fatal("expected queued job, queue is empty")
	}

	return ProfileJob{}
}

func mustQueueEmpty(t *testing.T, sm *StateMachine) {
	t.Helper()

	select {
	case job := <-sm.GetQueue():
		t.Fatalf("expected empty queue, got job %+v", job)
	default:
	}
}

func mustReceiveSignal(t *testing.T, ch <-chan struct{}, waitFor string) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(testSyncTimeout):
		t.Fatalf("timeout waiting for %s", waitFor)
	}
}

func mustReceiveError(t *testing.T, ch <-chan error, waitFor string) error {
	t.Helper()

	select {
	case err := <-ch:
		return err
	case <-time.After(testSyncTimeout):
		t.Fatalf("timeout waiting for %s", waitFor)
	}

	return nil
}

func waitGroupWithTimeout(t *testing.T, wg *sync.WaitGroup, waitFor string) {
	t.Helper()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	mustReceiveSignal(t, done, waitFor)
}

func TestTruncateMessageUTF8(t *testing.T) {
	tests := []struct {
		name      string
		msg       string
		maxLen    int
		want      string
		wantRunes int
	}{
		{
			name:      "cyrillic with word boundary",
			msg:       "Привет мир как дела",
			maxLen:    12,
			want:      "Привет мир",
			wantRunes: 10,
		},
		{
			name:      "emoji and cyrillic without spaces",
			msg:       "Привет😊мир",
			maxLen:    7,
			want:      "Привет😊",
			wantRunes: 7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateMessage(tt.msg, tt.maxLen)
			if got != tt.want {
				t.Fatalf("truncateMessage(%q, %d) = %q, want %q", tt.msg, tt.maxLen, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("truncateMessage(%q, %d) returned invalid UTF-8: %q", tt.msg, tt.maxLen, got)
			}
			if gotRunes := utf8.RuneCountInString(got); gotRunes != tt.wantRunes {
				t.Fatalf("truncateMessage(%q, %d) rune count = %d, want %d", tt.msg, tt.maxLen, gotRunes, tt.wantRunes)
			}
		})
	}
}

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

func TestGenerateAndSendLikeAppendsReplyAudit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	summarizer := &scriptedSummarizer{
		responses:    []string{"INTJ", "generated"},
		cancelOnCall: 2,
		cancel:       cancel,
	}
	audit := &stubReplyAuditLogger{}

	h := &Handler{
		state: NewStateMachine(),
		config: &standalone.Config{
			DatingMBTIPrompt:    "mbti prompt",
			DatingMBTIAllowlist: []string{"INTJ"},
		},
		client:      summarizer,
		model:       "model",
		prompt:      "reply prompt",
		temperature: 0.2,
		replyAudit:  audit,
	}

	err := h.generateAndSendLike(ctx, ProfileData{ProfileText: "bio"})
	if err != nil {
		t.Fatalf("generateAndSendLike() error = %v, want nil", err)
	}

	calls := audit.snapshotCalls()
	if len(calls) != 1 {
		t.Fatalf("reply audit call count = %d, want 1", len(calls))
	}

	if calls[0].mbti != "INTJ" || calls[0].profileText != "bio" || calls[0].prompt != "reply prompt" || calls[0].response != "generated" {
		t.Fatalf("reply audit call = %+v, want mbti=%q profile_text=%q prompt=%q response=%q", calls[0], "INTJ", "bio", "reply prompt", "generated")
	}
}

func TestGenerateAndSendLikeReplyAuditErrorDoesNotStopFlow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	summarizer := &scriptedSummarizer{
		responses:    []string{"INTJ", "generated"},
		cancelOnCall: 2,
		cancel:       cancel,
	}
	audit := &stubReplyAuditLogger{err: errors.New("append failed")}

	h := &Handler{
		state: NewStateMachine(),
		config: &standalone.Config{
			DatingMBTIPrompt:    "mbti prompt",
			DatingMBTIAllowlist: []string{"INTJ"},
		},
		client:      summarizer,
		model:       "model",
		prompt:      "reply prompt",
		temperature: 0.2,
		replyAudit:  audit,
	}

	err := h.generateAndSendLike(ctx, ProfileData{ProfileText: "bio"})
	if err != nil {
		t.Fatalf("generateAndSendLike() error = %v, want nil", err)
	}

	if len(audit.snapshotCalls()) != 1 {
		t.Fatalf("reply audit call count = %d, want 1", len(audit.snapshotCalls()))
	}
}

func TestGenerateAndSendLikeUsesProfileLLMCacheForSinglePhoto(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	summarizer := &scriptedSummarizer{
		responses:    []string{"INTJ", "cached opener"},
		cancelOnCall: 2,
		cancel:       cancel,
	}

	h := &Handler{
		state: NewStateMachine(),
		config: &standalone.Config{
			DatingMBTIPrompt:    "mbti prompt",
			DatingMBTIAllowlist: []string{"INTJ"},
		},
		client:      summarizer,
		model:       "model",
		prompt:      "reply prompt",
		temperature: 0.2,
	}

	first := ProfileData{
		ProfileText:      "  Alice   \n  bio  ",
		PhotoPaths:       []string{"/tmp/photo-first.jpg"},
		PhotoIdentifiers: []string{"100:200"},
	}
	if err := h.generateAndSendLike(ctx, first); err != nil {
		t.Fatalf("generateAndSendLike(first) error = %v, want nil", err)
	}

	second := ProfileData{
		ProfileText:      "alice bio",
		PhotoPaths:       []string{"/tmp/photo-second.jpg"},
		PhotoIdentifiers: []string{"100:200"},
	}
	if err := h.generateAndSendLike(ctx, second); err != nil {
		t.Fatalf("generateAndSendLike(second) error = %v, want nil", err)
	}

	if got := summarizer.snapshotCallCount(); got != 2 {
		t.Fatalf("SummarizeMultimodal calls = %d, want 2 (single MBTI+opener pass, cached second pass)", got)
	}
}

func TestGenerateAndSendLikeUsesProfileLLMCacheForAlbumWithStablePhotoOrdering(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	summarizer := &scriptedSummarizer{
		responses:    []string{"INTJ", "album opener"},
		cancelOnCall: 2,
		cancel:       cancel,
	}

	h := &Handler{
		state: NewStateMachine(),
		config: &standalone.Config{
			DatingMBTIPrompt:    "mbti prompt",
			DatingMBTIAllowlist: []string{"INTJ"},
		},
		client:      summarizer,
		model:       "model",
		prompt:      "reply prompt",
		temperature: 0.2,
	}

	albumOne := &telegram.Album{Messages: []*telegram.NewMessage{
		{
			ID: 200,
			Message: &telegram.MessageObj{
				Media: &telegram.MessageMediaPhoto{Photo: &telegram.PhotoObj{ID: 2, AccessHash: 22}},
			},
		},
		{
			ID: 100,
			Message: &telegram.MessageObj{
				Media: &telegram.MessageMediaPhoto{Photo: &telegram.PhotoObj{ID: 1, AccessHash: 11}},
			},
		},
	}}
	albumTwo := &telegram.Album{Messages: []*telegram.NewMessage{
		{
			ID: 100,
			Message: &telegram.MessageObj{
				Media: &telegram.MessageMediaPhoto{Photo: &telegram.PhotoObj{ID: 1, AccessHash: 11}},
			},
		},
		{
			ID: 200,
			Message: &telegram.MessageObj{
				Media: &telegram.MessageMediaPhoto{Photo: &telegram.PhotoObj{ID: 2, AccessHash: 22}},
			},
		},
	}}

	firstIDs := photoIdentifiersFromAlbum(albumOne)
	secondIDs := photoIdentifiersFromAlbum(albumTwo)

	firstKey := buildProfileLLMCacheKey("Album profile", firstIDs)
	secondKey := buildProfileLLMCacheKey("  album   profile ", secondIDs)
	if firstKey != secondKey {
		t.Fatalf("buildProfileLLMCacheKey() mismatch for equivalent albums: %q != %q", firstKey, secondKey)
	}

	if err := h.generateAndSendLike(ctx, ProfileData{ProfileText: "Album profile", PhotoPaths: []string{"/tmp/a.jpg"}, PhotoIdentifiers: firstIDs}); err != nil {
		t.Fatalf("generateAndSendLike(first album) error = %v, want nil", err)
	}
	if err := h.generateAndSendLike(ctx, ProfileData{ProfileText: "  album   profile ", PhotoPaths: []string{"/tmp/b.jpg"}, PhotoIdentifiers: secondIDs}); err != nil {
		t.Fatalf("generateAndSendLike(second album) error = %v, want nil", err)
	}

	if got := summarizer.snapshotCallCount(); got != 2 {
		t.Fatalf("SummarizeMultimodal calls = %d, want 2 (single MBTI+opener pass, cached second pass)", got)
	}
}

func TestGenerateAndSendLikeDuplicateSkipsBeforeLLM(t *testing.T) {
	ctx := context.Background()
	dedupe := &stubProfileDedupeStore{isActive: true}
	clicked := ""

	h := &Handler{
		state: NewStateMachine(),
		config: &standalone.Config{
			DatingMBTIPrompt:    "mbti prompt",
			DatingMBTIAllowlist: []string{"INTJ"},
		},
		client:        &scriptedSummarizer{responses: []string{"INTJ", "generated"}},
		profileDedupe: dedupe,
		clickButtonFn: func(_ context.Context, button string) error {
			clicked = button
			return nil
		},
	}

	profile := ProfileData{ProfileText: "duplicate profile", PhotoIdentifiers: []string{"10:20"}}
	if err := h.generateAndSendLike(ctx, profile); err != nil {
		t.Fatalf("generateAndSendLike() error = %v, want nil", err)
	}

	if clicked != ButtonDislike {
		t.Fatalf("clicked button = %q, want %q", clicked, ButtonDislike)
	}

	if got := len(dedupe.snapshotActiveCalls()); got != 1 {
		t.Fatalf("IsActive calls = %d, want 1", got)
	}
	if got := len(dedupe.snapshotMarkCalls()); got != 1 {
		t.Fatalf("MarkProcessed calls = %d, want 1", got)
	}

	summarizer := h.client.(*scriptedSummarizer)
	if got := summarizer.snapshotCallCount(); got != 0 {
		t.Fatalf("SummarizeMultimodal calls = %d, want 0 on duplicate", got)
	}
}

func TestGenerateAndSendLikeMarkProcessedBestEffortOnLikePath(t *testing.T) {
	ctx := context.Background()
	dedupe := &stubProfileDedupeStore{markErr: errors.New("mark failed")}
	clicked := ""

	h := &Handler{
		state: NewStateMachine(),
		config: &standalone.Config{
			DatingMBTIPrompt:    "mbti prompt",
			DatingMBTIAllowlist: []string{"INTJ"},
		},
		client: &scriptedSummarizer{
			responses: []string{"INTJ", "generated"},
		},
		model:         "model",
		prompt:        "prompt",
		temperature:   0.2,
		profileDedupe: dedupe,
		clickButtonFn: func(_ context.Context, button string) error {
			clicked = button
			return nil
		},
	}

	if err := h.generateAndSendLike(ctx, ProfileData{ProfileText: "fresh profile"}); err != nil {
		t.Fatalf("generateAndSendLike() error = %v, want nil", err)
	}

	if clicked != ButtonLikeMessage {
		t.Fatalf("clicked button = %q, want %q", clicked, ButtonLikeMessage)
	}
	if got := len(dedupe.snapshotMarkCalls()); got != 1 {
		t.Fatalf("MarkProcessed calls = %d, want 1", got)
	}
}

func TestProcessProfileLowQualityMarksProcessedAfterDislike(t *testing.T) {
	ctx := context.Background()
	dedupe := &stubProfileDedupeStore{}
	clicked := ""

	h := &Handler{
		state: NewStateMachine(),
		config: &standalone.Config{
			DatingSkipLowQuality: true,
			DatingMinBioLength:   100,
		},
		profileDedupe: dedupe,
		clickButtonFn: func(_ context.Context, button string) error {
			clicked = button
			return nil
		},
	}

	msg := &telegram.NewMessage{Message: &telegram.MessageObj{Message: "Name - short bio"}}
	if err := h.processProfile(ctx, msg); err != nil {
		t.Fatalf("processProfile() error = %v, want nil", err)
	}

	if clicked != ButtonDislike {
		t.Fatalf("clicked button = %q, want %q", clicked, ButtonDislike)
	}
	if got := len(dedupe.snapshotMarkCalls()); got != 1 {
		t.Fatalf("MarkProcessed calls = %d, want 1", got)
	}
}

func TestRetryGenerateMessageAppendsReplyAudit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	summarizer := &scriptedSummarizer{
		responses:    []string{"retry generated"},
		cancelOnCall: 1,
		cancel:       cancel,
	}
	audit := &stubReplyAuditLogger{}

	h := &Handler{
		state:       NewStateMachine(),
		client:      summarizer,
		model:       "model",
		temperature: 0.2,
		replyAudit:  audit,
	}
	h.state.SetPendingMessage("draft")
	h.state.SetProfileData(&ProfileData{ProfileText: "bio", MBTI: "INFJ"})

	err := h.retryGenerateMessage(ctx, RetryTooShort)
	if err != nil {
		t.Fatalf("retryGenerateMessage() error = %v, want nil", err)
	}

	calls := audit.snapshotCalls()
	if len(calls) != 1 {
		t.Fatalf("reply audit call count = %d, want 1", len(calls))
	}

	if calls[0].mbti != "INFJ" || calls[0].profileText != "bio" || calls[0].prompt != TooShortRetryPrompt || calls[0].response != "retry generated" {
		t.Fatalf("reply audit call = %+v, want mbti=%q profile_text=%q prompt=%q response=%q", calls[0], "INFJ", "bio", TooShortRetryPrompt, "retry generated")
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
			name:  "viewing state with unknown text-only notice",
			state: StateViewingProfiles,
			message: &telegram.NewMessage{Message: &telegram.MessageObj{
				Message: "This is a generic informational notice.",
			}},
			want: true,
		},
		{
			name:  "viewing state with whitespace-only text",
			state: StateViewingProfiles,
			message: &telegram.NewMessage{Message: &telegram.MessageObj{
				Message: "   \n\t",
			}},
			want: false,
		},
		{
			name:  "viewing state with text and unrelated markup",
			state: StateViewingProfiles,
			message: &telegram.NewMessage{Message: &telegram.MessageObj{
				Message:     "This is a generic informational notice.",
				ReplyMarkup: &telegram.ReplyInlineMarkup{},
			}},
			want: true,
		},
		{
			name:  "viewing state with profile action keyboard",
			state: StateViewingProfiles,
			message: &telegram.NewMessage{Message: &telegram.MessageObj{
				Message: "Profile text",
				ReplyMarkup: &telegram.ReplyKeyboardMarkup{Rows: []*telegram.KeyboardButtonRow{{Buttons: []telegram.KeyboardButton{
					&telegram.KeyboardButtonObj{Text: ButtonLike},
					&telegram.KeyboardButtonObj{Text: ButtonLikeMessage},
					&telegram.KeyboardButtonObj{Text: ButtonDislike},
				}}}},
			}},
			want: false,
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
		{
			name:  "non viewing state with text-only notice",
			state: StateIdle,
			message: &telegram.NewMessage{Message: &telegram.MessageObj{
				Message: "This is a generic informational notice.",
			}},
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

func TestIsDailyLimitMessage(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{
			name: "exact message",
			text: PatternDailyLimitExact,
			want: true,
		},
		{
			name: "case insensitive variation with emoji",
			text: "TOO MANY ❤️ TODAY. Invite your friends to get more hearts.",
			want: true,
		},
		{
			name: "minor wording changes keep signal",
			text: "Oops, too many ❤ for today. Invite friends and come back later.",
			want: true,
		},
		{
			name: "too many with today but no heart",
			text: "Too many likes today, try again tomorrow",
			want: true,
		},
		{
			name: "too many with invite cues but no heart",
			text: "Too many today. Invite friends to get more tomorrow.",
			want: true,
		},
		{
			name: "too many with heart but unrelated",
			text: "Too many thoughts ❤ about this profile",
			want: false,
		},
		{
			name: "empty text",
			text: "",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDailyLimitMessage(tt.text); got != tt.want {
				t.Fatalf("isDailyLimitMessage(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestHandleDailyLimitPausesAndResetsState(t *testing.T) {
	h := &Handler{state: NewStateMachine()}
	h.state.SetState(StateViewingProfiles)
	h.state.NextStuckRecoveryEscalation()
	h.state.NextStuckRecoveryEscalation()
	h.state.SetPendingMessage("draft")
	h.state.SetProfileData(&ProfileData{ProfileText: "bio", PhotoPaths: []string{"/tmp/photo.jpg"}})
	h.state.IncrementRetry()

	msg := &telegram.NewMessage{Message: &telegram.MessageObj{Message: PatternDailyLimitExact}}

	if err := h.Handle(msg); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if got := h.state.GetState(); got != StateIdle {
		t.Fatalf("state = %v, want %v", got, StateIdle)
	}

	if got := h.state.GetPendingMessage(); got != "" {
		t.Fatalf("pending message = %q, want empty", got)
	}

	if got := h.state.GetProfileData(); got != nil {
		t.Fatalf("profile data = %#v, want nil", got)
	}

	if got := h.state.GetRetryCount(); got != 0 {
		t.Fatalf("retry count = %d, want 0", got)
	}

	paused, resumed, until := h.state.CheckPause(time.Now())
	if !paused || resumed || until.IsZero() {
		t.Fatalf("CheckPause(now) = (%v, %v, %v), want (true, false, non-zero)", paused, resumed, until)
	}

	remaining := until.Sub(time.Now())
	if remaining > DailyLimitPauseDuration || remaining < DailyLimitPauseDuration-2*time.Second {
		t.Fatalf("pause remaining = %v, want close to %v", remaining, DailyLimitPauseDuration)
	}

	if got := h.state.NextStuckRecoveryEscalation(); got != 1 {
		t.Fatalf("stuck escalation after daily limit reset = %d, want 1", got)
	}
}

func TestHandleGenericTooManyLikesPausesBeforeRecovery(t *testing.T) {
	h := &Handler{state: NewStateMachine()}
	h.state.SetState(StateViewingProfiles)
	h.state.SetPendingMessage("draft")
	h.state.SetProfileData(&ProfileData{ProfileText: "bio", PhotoPaths: []string{"/tmp/photo.jpg"}})
	h.state.IncrementRetry()

	msg := &telegram.NewMessage{Message: &telegram.MessageObj{Message: "Too many likes today, try again tomorrow"}}

	if err := h.Handle(msg); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	if got := h.state.GetState(); got != StateIdle {
		t.Fatalf("state = %v, want %v", got, StateIdle)
	}

	if got := h.state.GetPendingMessage(); got != "" {
		t.Fatalf("pending message = %q, want empty", got)
	}

	if got := h.state.GetProfileData(); got != nil {
		t.Fatalf("profile data = %#v, want nil", got)
	}

	if got := h.state.GetRetryCount(); got != 0 {
		t.Fatalf("retry count = %d, want 0", got)
	}

	paused, resumed, until := h.state.CheckPause(time.Now())
	if !paused || resumed || until.IsZero() {
		t.Fatalf("CheckPause(now) = (%v, %v, %v), want (true, false, non-zero)", paused, resumed, until)
	}
}

func TestHandlePausedSkipsGenericRecovery(t *testing.T) {
	h := &Handler{state: NewStateMachine()}
	h.state.SetState(StateViewingProfiles)
	h.state.PauseFor(time.Hour)

	err := h.Handle(&telegram.NewMessage{Message: &telegram.MessageObj{Message: "Interstitial text"}})
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}

	if got := h.state.GetState(); got != StateViewingProfiles {
		t.Fatalf("state = %v, want %v", got, StateViewingProfiles)
	}
}

func TestHandlePausedStillProcessesStartChattingFinal(t *testing.T) {
	h := &Handler{state: NewStateMachine()}
	h.state.SetState(StateViewingProfiles)
	h.state.PauseFor(time.Hour)

	deliverCalls := 0
	h.deliverReciprocalLikeFinalFn = func(_ context.Context, _ ReciprocalLikeFinalPayload, _ []ReciprocalLikePhoto) error {
		deliverCalls++
		return nil
	}

	err := h.Handle(&telegram.NewMessage{Message: &telegram.MessageObj{Message: "Start chatting: https://t.me/final_user"}})
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}

	if deliverCalls != 1 {
		t.Fatalf("deliverReciprocalLikeFinalFn calls = %d, want 1", deliverCalls)
	}

	if got := h.state.GetState(); got != StateViewingProfiles {
		t.Fatalf("state = %v, want %v", got, StateViewingProfiles)
	}

	paused, resumed, until := h.state.CheckPause(time.Now())
	if !paused || resumed || until.IsZero() {
		t.Fatalf("CheckPause(now) = (%v, %v, %v), want (true, false, non-zero)", paused, resumed, until)
	}
}

func TestHandleDailyLimitTakesPrecedenceOverViewProfilesAndRecovery(t *testing.T) {
	h := &Handler{state: NewStateMachine()}
	h.state.SetState(StateViewingProfiles)
	h.state.SetPendingMessage("draft")
	h.state.SetProfileData(&ProfileData{ProfileText: "bio", PhotoPaths: []string{"/tmp/photo.jpg"}})
	h.state.IncrementRetry()

	msg := &telegram.NewMessage{Message: &telegram.MessageObj{
		Message: "Too many likes today. Invite your friends for more hearts. " + PatternViewProfiles,
		ReplyMarkup: &telegram.ReplyKeyboardMarkup{
			Rows: []*telegram.KeyboardButtonRow{
				{Buttons: []telegram.KeyboardButton{&telegram.KeyboardButtonObj{Text: ButtonViewProfiles}}},
			},
		},
	}}

	err := h.Handle(msg)
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}

	if got := h.state.GetState(); got != StateIdle {
		t.Fatalf("state = %v, want %v", got, StateIdle)
	}

	if got := h.state.GetPendingMessage(); got != "" {
		t.Fatalf("pending message = %q, want empty", got)
	}

	if got := h.state.GetProfileData(); got != nil {
		t.Fatalf("profile data = %#v, want nil", got)
	}

	if got := h.state.GetRetryCount(); got != 0 {
		t.Fatalf("retry count = %d, want 0", got)
	}

	paused, resumed, until := h.state.CheckPause(time.Now())
	if !paused || resumed || until.IsZero() {
		t.Fatalf("CheckPause(now) = (%v, %v, %v), want (true, false, non-zero)", paused, resumed, until)
	}
}

func TestFinalizeSendStateResetsConversationInvariant(t *testing.T) {
	h := &Handler{state: NewStateMachine()}
	h.state.SetState(StateWaitingPrompt)
	h.state.SetPendingMessage("draft")
	h.state.SetProfileData(&ProfileData{
		ProfileText:      "bio",
		PhotoPaths:       []string{"/tmp/photo.jpg"},
		PhotoIdentifiers: []string{"123:456"},
		MBTI:             "INTJ",
	})
	h.state.IncrementRetry()

	h.finalizeSendState()

	contexts := h.state.ListRecentReciprocalLikeContexts(time.Now(), -1)
	if len(contexts) != 1 {
		t.Fatalf("ListRecentReciprocalLikeContexts() len=%d, want 1", len(contexts))
	}

	captured := contexts[0]
	if captured.ProfileText != "bio" || captured.OpenerText != "draft" || captured.MBTI != "INTJ" {
		t.Fatalf("captured context = %+v, want bio/draft/INTJ", captured)
	}

	wantFingerprint := buildProfileLLMCacheKey("bio\ndraft\nINTJ", []string{"123:456"})
	if captured.Fingerprint != wantFingerprint {
		t.Fatalf("captured fingerprint = %q, want %q", captured.Fingerprint, wantFingerprint)
	}
	if captured.CapturedAt.IsZero() {
		t.Fatal("captured timestamp is zero, want non-zero")
	}

	if got := h.state.GetPendingMessage(); got != "" {
		t.Fatalf("pending message = %q, want empty", got)
	}

	if got := h.state.GetProfileData(); got != nil {
		t.Fatalf("profile data = %#v, want nil", got)
	}

	if got := h.state.GetRetryCount(); got != 0 {
		t.Fatalf("retry count = %d, want 0", got)
	}

	if got := h.state.GetState(); got != StateViewingProfiles {
		t.Fatalf("state = %v, want %v", got, StateViewingProfiles)
	}
}

func TestSendPendingMessageCacheMissPreservesState(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}
	h.state.SetState(StateWaitingPrompt)
	h.state.SetPendingMessage("draft")
	h.state.SetProfileData(&ProfileData{ProfileText: "bio", PhotoPaths: []string{"/tmp/photo.jpg"}})
	h.state.IncrementRetry()

	err := h.sendPendingMessage(&telegram.NewMessage{Message: &telegram.MessageObj{}})
	if err == nil {
		t.Fatal("sendPendingMessage() error = nil, want non-nil")
	}

	if !strings.Contains(err.Error(), "dating peer is not cached yet") {
		t.Fatalf("sendPendingMessage() error = %v, want cache miss error", err)
	}

	if got := h.state.GetPendingMessage(); got != "draft" {
		t.Fatalf("pending message = %q, want %q", got, "draft")
	}

	if got := h.state.GetProfileData(); got == nil || got.ProfileText != "bio" {
		t.Fatalf("profile data = %#v, want non-nil bio", got)
	}

	if got := h.state.GetRetryCount(); got != 1 {
		t.Fatalf("retry count = %d, want 1", got)
	}

	if got := h.state.GetState(); got != StateWaitingPrompt {
		t.Fatalf("state = %v, want %v", got, StateWaitingPrompt)
	}

	if got := h.state.ListRecentReciprocalLikeContexts(time.Now(), -1); len(got) != 0 {
		t.Fatalf("ListRecentReciprocalLikeContexts() len=%d, want 0", len(got))
	}
}

func TestSendTruncatedMessageCacheMissPreservesState(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}
	h.state.SetState(StateWaitingPrompt)
	h.state.SetPendingMessage("truncated")
	h.state.SetProfileData(&ProfileData{ProfileText: "bio", PhotoPaths: []string{"/tmp/photo.jpg"}})
	h.state.IncrementRetry()

	err := h.sendTruncatedMessage(context.Background(), "truncated")
	if err == nil {
		t.Fatal("sendTruncatedMessage() error = nil, want non-nil")
	}

	if !strings.Contains(err.Error(), "dating peer is not cached yet") {
		t.Fatalf("sendTruncatedMessage() error = %v, want cache miss error", err)
	}

	if got := h.state.GetPendingMessage(); got != "truncated" {
		t.Fatalf("pending message = %q, want %q", got, "truncated")
	}

	if got := h.state.GetProfileData(); got == nil || got.ProfileText != "bio" {
		t.Fatalf("profile data = %#v, want non-nil bio", got)
	}

	if got := h.state.GetRetryCount(); got != 1 {
		t.Fatalf("retry count = %d, want 1", got)
	}

	if got := h.state.GetState(); got != StateWaitingPrompt {
		t.Fatalf("state = %v, want %v", got, StateWaitingPrompt)
	}
}

func TestSendPendingMessageSuccessfulSendFollowedByShutdownFinalizesState(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}
	h.state.SetState(StateWaitingPrompt)
	h.state.SetPendingMessage("draft")
	h.state.SetProfileData(&ProfileData{ProfileText: "bio", PhotoPaths: []string{"/tmp/photo.jpg"}})
	h.state.IncrementRetry()
	h.setBotPeer(&telegram.InputPeerUser{UserID: h.chatID, AccessHash: 1})

	h.sendMessageFn = func(_ context.Context, _ telegram.InputPeer, _ string) error {
		h.Shutdown()
		return nil
	}

	err := h.sendPendingMessage(&telegram.NewMessage{Message: &telegram.MessageObj{}})
	if err != nil {
		t.Fatalf("sendPendingMessage() error = %v, want nil", err)
	}

	if got := h.state.GetPendingMessage(); got != "" {
		t.Fatalf("pending message = %q, want empty", got)
	}

	if got := h.state.GetProfileData(); got != nil {
		t.Fatalf("profile data = %#v, want nil", got)
	}

	if got := h.state.GetRetryCount(); got != 0 {
		t.Fatalf("retry count = %d, want 0", got)
	}

	if got := h.state.GetState(); got != StateStopped {
		t.Fatalf("state = %v, want %v", got, StateStopped)
	}
}

func TestSendTruncatedMessageSuccessfulSendFollowedByShutdownFinalizesState(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}
	h.state.SetState(StateWaitingPrompt)
	h.state.SetPendingMessage("truncated")
	h.state.SetProfileData(&ProfileData{ProfileText: "bio", PhotoPaths: []string{"/tmp/photo.jpg"}})
	h.state.IncrementRetry()
	h.setBotPeer(&telegram.InputPeerUser{UserID: h.chatID, AccessHash: 1})

	h.sendMessageFn = func(_ context.Context, _ telegram.InputPeer, _ string) error {
		h.Shutdown()
		return nil
	}

	err := h.sendTruncatedMessage(context.Background(), "truncated")
	if err != nil {
		t.Fatalf("sendTruncatedMessage() error = %v, want nil", err)
	}

	if got := h.state.GetPendingMessage(); got != "" {
		t.Fatalf("pending message = %q, want empty", got)
	}

	if got := h.state.GetProfileData(); got != nil {
		t.Fatalf("profile data = %#v, want nil", got)
	}

	if got := h.state.GetRetryCount(); got != 0 {
		t.Fatalf("retry count = %d, want 0", got)
	}

	if got := h.state.GetState(); got != StateStopped {
		t.Fatalf("state = %v, want %v", got, StateStopped)
	}
}

func TestFinalizeSendStateDoesNotOverrideTerminalStates(t *testing.T) {
	tests := []struct {
		name         string
		initialState State
	}{
		{name: "idle", initialState: StateIdle},
		{name: "stopped", initialState: StateStopped},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{state: NewStateMachine()}
			h.state.SetState(tt.initialState)
			h.state.SetPendingMessage("draft")
			h.state.SetProfileData(&ProfileData{ProfileText: "bio", PhotoPaths: []string{"/tmp/photo.jpg"}})
			h.state.IncrementRetry()

			h.finalizeSendState()

			if got := h.state.GetPendingMessage(); got != "" {
				t.Fatalf("pending message = %q, want empty", got)
			}

			if got := h.state.GetProfileData(); got != nil {
				t.Fatalf("profile data = %#v, want nil", got)
			}

			if got := h.state.GetRetryCount(); got != 0 {
				t.Fatalf("retry count = %d, want 0", got)
			}

			if got := h.state.GetState(); got != tt.initialState {
				t.Fatalf("state = %v, want %v", got, tt.initialState)
			}
		})
	}
}

func TestShutdownSetsStoppedAndStopsWorker(t *testing.T) {
	h := &Handler{state: NewStateMachine()}

	h.Shutdown()

	if got := h.state.GetState(); got != StateStopped {
		t.Fatalf("state after Shutdown() = %v, want %v", got, StateStopped)
	}

	select {
	case <-h.state.ShouldQuit():
		// expected
	default:
		t.Fatal("quit channel was not closed by Shutdown()")
	}
}

func TestShutdownCancelsWorkerContext(t *testing.T) {
	h := &Handler{state: NewStateMachine()}
	h.StartWorker()

	ctx := h.state.WorkerContext()

	h.Shutdown()

	select {
	case <-ctx.Done():
		// expected
	case <-time.After(testSyncTimeout):
		t.Fatal("worker context was not canceled by Shutdown()")
	}
}

func TestStopSendsInternalSleepCommandOnce(t *testing.T) {
	h := &Handler{state: NewStateMachine()}
	var calls atomic.Int32

	h.sendSleepFn = func(context.Context) error {
		calls.Add(1)
		return nil
	}

	const stopCalls = 32
	var wg sync.WaitGroup
	for i := 0; i < stopCalls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.Stop()
		}()
	}

	waitGroupWithTimeout(t, &wg, "concurrent Stop calls")

	if got := calls.Load(); got != 1 {
		t.Fatalf("internal sleep command calls = %d, want 1", got)
	}

	if got := h.state.GetState(); got != StateStopped {
		t.Fatalf("state after Stop() = %v, want %v", got, StateStopped)
	}
}

func TestHandleConcurrentWithShutdownRejectsLateEnqueue(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}
	msg := &telegram.NewMessage{ID: 1, Message: &telegram.MessageObj{
		Media:  &telegram.MessageMediaPhoto{},
		PeerID: &telegram.PeerUser{UserID: h.chatID},
	}}

	shutdownDone := make(chan struct{})
	go func() {
		h.Shutdown()
		close(shutdownDone)
	}()
	mustReceiveSignal(t, shutdownDone, "Shutdown completion")

	if ok := h.state.Enqueue(ProfileJob{Type: "message", Message: msg}); ok {
		t.Fatal("Enqueue() after Shutdown() = true, want false")
	}

	before := len(h.state.profileQueue)

	const postShutdownCalls = 32
	var wg sync.WaitGroup
	errCh := make(chan error, postShutdownCalls)
	for i := 0; i < postShutdownCalls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- h.Handle(msg)
		}()
	}

	waitGroupWithTimeout(t, &wg, "post-shutdown Handle calls")
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("Handle() after Shutdown() error = %v", err)
		}
	}

	after := len(h.state.profileQueue)
	if after != before {
		t.Fatalf("queue length changed after Handle() post-shutdown: before=%d after=%d", before, after)
	}
}

func TestStartWorkerQuitPrioritySkipsQueuedJobsAfterStopSignal(t *testing.T) {
	h := &Handler{state: NewStateMachine()}

	if ok := h.state.Enqueue(ProfileJob{Type: "message"}); !ok {
		t.Fatal("Enqueue() = false, want true")
	}

	h.StopWorker()
	h.StartWorker()
	h.WaitWorkerStop()

	if got := len(h.state.profileQueue); got != 1 {
		t.Fatalf("queue length after quit-priority start = %d, want 1", got)
	}
}

func TestShutdownWaitsForWorkerStopAndInterruptsDelay(t *testing.T) {
	h := &Handler{
		state:       NewStateMachine(),
		actionDelay: 500 * time.Millisecond,
	}

	h.StartWorker()
	if ok := h.state.Enqueue(ProfileJob{Type: "message"}); !ok {
		t.Fatal("Enqueue() = false, want true")
	}

	deadline := time.Now().Add(testSyncTimeout)
	for len(h.state.profileQueue) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("worker did not dequeue job before shutdown")
		}
		time.Sleep(10 * time.Millisecond)
	}

	startedAt := time.Now()
	h.Shutdown()
	elapsed := time.Since(startedAt)

	if elapsed >= h.actionDelay/2 {
		t.Fatalf("Shutdown() took %v with pending delay %v, want interruptible stop faster than %v", elapsed, h.actionDelay, h.actionDelay/2)
	}
}

func TestShutdownCancelsInFlightWorkerJobAndKeepsStoppedState(t *testing.T) {
	summarizer := &blockingSummarizer{
		started:  make(chan struct{}),
		canceled: make(chan struct{}),
		release:  make(chan struct{}),
	}

	h := &Handler{
		state: NewStateMachine(),
		config: &standalone.Config{
			DatingMBTIPrompt:     "mbti",
			DatingMBTIAllowlist:  []string{"INTJ"},
			DatingSkipLowQuality: false,
		},
		client:      summarizer,
		model:       "model",
		prompt:      "prompt",
		temperature: 0.3,
	}

	h.StartWorker()
	if ok := h.state.Enqueue(ProfileJob{Type: "album", Album: &telegram.Album{Messages: []*telegram.NewMessage{{
		ID: 1,
		Message: &telegram.MessageObj{
			Message: "profile text",
		},
	}}}}); !ok {
		t.Fatal("Enqueue() = false, want true")
	}

	mustReceiveSignal(t, summarizer.started, "blocking summarizer start")

	shutdownDone := make(chan struct{})
	go func() {
		h.Shutdown()
		close(shutdownDone)
	}()

	mustReceiveSignal(t, summarizer.canceled, "blocking summarizer cancellation")
	mustReceiveSignal(t, shutdownDone, "Shutdown completion")

	if got := h.state.GetState(); got != StateStopped {
		t.Fatalf("state after Shutdown() = %v, want %v", got, StateStopped)
	}
}

func TestIsReciprocalLikePrompt(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "person liked you", text: "A person liked you", want: true},
		{name: "woman liked you", text: "A woman liked you", want: true},
		{name: "man liked you", text: "A man liked you", want: true},
		{name: "have a look prompt", text: "Someone liked you. Have a look?", want: true},
		{name: "unrelated", text: "view profiles", want: false},
		{name: "empty", text: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isReciprocalLikePrompt(tt.text); got != tt.want {
				t.Fatalf("isReciprocalLikePrompt(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestHandleReciprocalLikePromptDoesNotShutdown(t *testing.T) {
	h := &Handler{state: NewStateMachine()}
	h.state.SetState(StateViewingProfiles)
	lifecycleCtx := h.lifecycleContext()

	err := h.Handle(&telegram.NewMessage{Message: &telegram.MessageObj{Message: "A person liked you"}})
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}

	if got := h.state.GetState(); got != StateViewingProfiles {
		t.Fatalf("state after Handle(reciprocal) = %v, want %v", got, StateViewingProfiles)
	}

	select {
	case <-h.state.ShouldQuit():
		t.Fatal("quit channel closed for reciprocal-like prompt")
	default:
	}

	select {
	case <-lifecycleCtx.Done():
		t.Fatal("lifecycle context canceled for reciprocal-like prompt")
	default:
	}
}

func TestHandleReciprocalLikePromptWithShowButtonStaysNonTerminal(t *testing.T) {
	h := &Handler{state: NewStateMachine()}
	h.state.SetState(StateViewingProfiles)

	err := h.Handle(&telegram.NewMessage{Message: &telegram.MessageObj{
		Message: "Someone liked you. Have a look?",
		ReplyMarkup: &telegram.ReplyKeyboardMarkup{Rows: []*telegram.KeyboardButtonRow{{Buttons: []telegram.KeyboardButton{
			&telegram.KeyboardButtonObj{Text: ButtonViewProfiles},
		}}}},
	}})
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}

	if got := h.state.GetState(); got != StateViewingProfiles {
		t.Fatalf("state after Handle(reciprocal with button) = %v, want %v", got, StateViewingProfiles)
	}

	select {
	case <-h.state.ShouldQuit():
		t.Fatal("quit channel closed for reciprocal-like prompt with button")
	default:
	}
}

func TestHandleStartChattingAssemblesReciprocalFinalPayloadNonTerminal(t *testing.T) {
	h := &Handler{state: NewStateMachine()}
	h.state.SetState(StateViewingProfiles)

	contextCapturedAt := time.Now()
	h.state.AddRecentReciprocalLikeContext(RecentReciprocalLikeContext{
		ProfileText: "profile bio",
		OpenerText:  "hello opener",
		MBTI:        "INTJ",
		CapturedAt:  contextCapturedAt,
	})

	var (
		deliverCalls int
		gotPayload   ReciprocalLikeFinalPayload
		gotPhotos    []ReciprocalLikePhoto
	)
	h.deliverReciprocalLikeFinalFn = func(_ context.Context, payload ReciprocalLikeFinalPayload, photos []ReciprocalLikePhoto) error {
		deliverCalls++
		gotPayload = payload
		gotPhotos = photos
		return nil
	}

	err := h.Handle(&telegram.NewMessage{Message: &telegram.MessageObj{
		Date:    1710001100,
		Message: "It's a match! Start chatting: https://t.me/final_user?text=Hi%20there",
	}})
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}

	if deliverCalls != 1 {
		t.Fatalf("deliverReciprocalLikeFinalFn calls = %d, want 1", deliverCalls)
	}

	if gotPayload.ContactUsername != "final_user" {
		t.Fatalf("payload.ContactUsername = %q, want %q", gotPayload.ContactUsername, "final_user")
	}
	if gotPayload.RawContactURL != "https://t.me/final_user?text=Hi%20there" {
		t.Fatalf("payload.RawContactURL = %q, want %q", gotPayload.RawContactURL, "https://t.me/final_user?text=Hi%20there")
	}
	if gotPayload.DeeplinkText != "Hi there" {
		t.Fatalf("payload.DeeplinkText = %q, want %q", gotPayload.DeeplinkText, "Hi there")
	}
	if gotPayload.ProfileText != "profile bio" || gotPayload.OpenerText != "hello opener" || gotPayload.MBTI != "INTJ" {
		t.Fatalf("payload context fields = [%q, %q, %q], want [profile bio, hello opener, INTJ]", gotPayload.ProfileText, gotPayload.OpenerText, gotPayload.MBTI)
	}
	if !gotPayload.ContextCapturedAt.Equal(contextCapturedAt) {
		t.Fatalf("payload.ContextCapturedAt = %v, want %v", gotPayload.ContextCapturedAt, contextCapturedAt)
	}
	if !gotPayload.EventTimestamp.Equal(time.Unix(1710001100, 0)) {
		t.Fatalf("payload.EventTimestamp = %v, want %v", gotPayload.EventTimestamp, time.Unix(1710001100, 0))
	}
	if len(gotPhotos) != 0 {
		t.Fatalf("delivered photos len = %d, want 0 without visible media source", len(gotPhotos))
	}

	if got := h.state.GetState(); got != StateViewingProfiles {
		t.Fatalf("state after Handle(start chatting) = %v, want %v", got, StateViewingProfiles)
	}

	select {
	case <-h.state.ShouldQuit():
		t.Fatal("quit channel closed for start chatting message")
	default:
	}
}

func TestHandleStartChattingWithoutURLFailsGracefullyAndStaysNonTerminal(t *testing.T) {
	h := &Handler{state: NewStateMachine()}
	h.state.SetState(StateViewingProfiles)

	deliverCalled := false
	h.deliverReciprocalLikeFinalFn = func(_ context.Context, _ ReciprocalLikeFinalPayload, _ []ReciprocalLikePhoto) error {
		deliverCalled = true
		return nil
	}

	err := h.Handle(&telegram.NewMessage{Message: &telegram.MessageObj{Message: "Start chatting now"}})
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}

	if deliverCalled {
		t.Fatal("deliverReciprocalLikeFinalFn called for start chatting message without URL")
	}

	if got := h.state.GetState(); got != StateViewingProfiles {
		t.Fatalf("state after Handle(start chatting without URL) = %v, want %v", got, StateViewingProfiles)
	}

	select {
	case <-h.state.ShouldQuit():
		t.Fatal("quit channel closed for start chatting message without URL")
	default:
	}
}

func TestHandleStartChattingDeliveryErrorIsSwallowedAndNonTerminal(t *testing.T) {
	h := &Handler{state: NewStateMachine()}
	h.state.SetState(StateViewingProfiles)

	deliverCalls := 0
	h.deliverReciprocalLikeFinalFn = func(_ context.Context, _ ReciprocalLikeFinalPayload, _ []ReciprocalLikePhoto) error {
		deliverCalls++
		return errors.New("webhook unavailable")
	}

	err := h.Handle(&telegram.NewMessage{Message: &telegram.MessageObj{
		Message: "Start chatting: https://t.me/final_user?text=Hi",
	}})
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}

	if deliverCalls != 1 {
		t.Fatalf("deliverReciprocalLikeFinalFn calls = %d, want 1", deliverCalls)
	}

	if got := h.state.GetState(); got != StateViewingProfiles {
		t.Fatalf("state after Handle(start chatting with delivery error) = %v, want %v", got, StateViewingProfiles)
	}

	select {
	case <-h.state.ShouldQuit():
		t.Fatal("quit channel closed for start chatting delivery error")
	default:
	}
}

func TestReciprocalOpenButtonText(t *testing.T) {
	tests := []struct {
		name       string
		message    *telegram.NewMessage
		wantButton string
		wantOK     bool
	}{
		{
			name: "numeric view button",
			message: &telegram.NewMessage{Message: &telegram.MessageObj{ReplyMarkup: &telegram.ReplyKeyboardMarkup{Rows: []*telegram.KeyboardButtonRow{{Buttons: []telegram.KeyboardButton{
				&telegram.KeyboardButtonObj{Text: ButtonViewProfiles},
			}}}}}},
			wantButton: ButtonViewProfiles,
			wantOK:     true,
		},
		{
			name: "show text button",
			message: &telegram.NewMessage{Message: &telegram.MessageObj{ReplyMarkup: &telegram.ReplyKeyboardMarkup{Rows: []*telegram.KeyboardButtonRow{{Buttons: []telegram.KeyboardButton{
				&telegram.KeyboardButtonObj{Text: "Show profile"},
			}}}}}},
			wantButton: "Show profile",
			wantOK:     true,
		},
		{
			name: "no matching button",
			message: &telegram.NewMessage{Message: &telegram.MessageObj{ReplyMarkup: &telegram.ReplyKeyboardMarkup{Rows: []*telegram.KeyboardButtonRow{{Buttons: []telegram.KeyboardButton{
				&telegram.KeyboardButtonObj{Text: ButtonMyProfile},
			}}}}}},
			wantButton: "",
			wantOK:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotButton, gotOK := reciprocalOpenButtonText(tt.message)
			if gotOK != tt.wantOK {
				t.Fatalf("reciprocalOpenButtonText() ok = %v, want %v", gotOK, tt.wantOK)
			}
			if gotButton != tt.wantButton {
				t.Fatalf("reciprocalOpenButtonText() button = %q, want %q", gotButton, tt.wantButton)
			}
		})
	}
}

func TestStopTriggersFullShutdownAndCancelsContexts(t *testing.T) {
	h := &Handler{state: NewStateMachine(), chatID: 123456789}
	h.StartWorker()

	lifecycleCtx := h.lifecycleContext()
	workerCtx := h.state.WorkerContext()

	h.Stop()

	if got := h.state.GetState(); got != StateStopped {
		t.Fatalf("state after Stop() = %v, want %v", got, StateStopped)
	}

	select {
	case <-h.state.ShouldQuit():
		// expected
	default:
		t.Fatal("quit channel was not closed by Stop()")
	}

	select {
	case <-lifecycleCtx.Done():
		// expected
	case <-time.After(testSyncTimeout):
		t.Fatal("lifecycle context was not canceled by Stop()")
	}

	select {
	case <-workerCtx.Done():
		// expected
	case <-time.After(testSyncTimeout):
		t.Fatal("worker context was not canceled by Stop()")
	}
}

func TestHandleRetryMessageShutdownCancelsLifecycleAndPreventsPostStopSend(t *testing.T) {
	tests := []struct {
		name        string
		incomingMsg string
	}{
		{name: "too long", incomingMsg: PatternTooLong},
		{name: "too short", incomingMsg: PatternTooShort},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summarizer := &blockingSummarizer{
				started:  make(chan struct{}),
				canceled: make(chan struct{}),
				release:  make(chan struct{}),
			}

			h := &Handler{
				state: NewStateMachine(),
				config: &standalone.Config{
					DatingMBTIPrompt:     "mbti",
					DatingMBTIAllowlist:  []string{"INTJ"},
					DatingSkipLowQuality: false,
				},
				client:      summarizer,
				model:       "model",
				temperature: 0.3,
			}
			h.state.SetState(StateWaitingPrompt)
			h.state.SetPendingMessage("draft")
			h.state.SetProfileData(&ProfileData{ProfileText: "bio"})

			handleDone := make(chan error, 1)
			go func() {
				handleDone <- h.Handle(&telegram.NewMessage{Message: &telegram.MessageObj{Message: tt.incomingMsg}})
			}()

			mustReceiveSignal(t, summarizer.started, "retry summarizer start")

			h.Shutdown()

			mustReceiveSignal(t, summarizer.canceled, "retry summarizer cancel")

			select {
			case err := <-handleDone:
				if err != nil {
					t.Fatalf("Handle() error = %v, want nil", err)
				}
			case <-time.After(testSyncTimeout):
				t.Fatal("Handle() did not return after Shutdown()")
			}

			if got := h.state.GetState(); got != StateStopped {
				t.Fatalf("state after Shutdown() = %v, want %v", got, StateStopped)
			}

			if got := h.state.GetPendingMessage(); got != "draft" {
				t.Fatalf("pending message after Shutdown() = %q, want unchanged %q", got, "draft")
			}
		})
	}
}

func TestBootstrapWithActionsSkipsWhenPaused(t *testing.T) {
	h := &Handler{state: NewStateMachine()}
	h.state.PauseFor(time.Hour)

	called := 0
	err := h.bootstrapWithActions(func() error {
		called++
		return nil
	}, func() error {
		called++
		return nil
	})

	if err != nil {
		t.Fatalf("bootstrapWithActions() error = %v, want nil", err)
	}

	if called != 0 {
		t.Fatalf("bootstrap actions called %d times, want 0", called)
	}
}

func TestBootstrapSkipsActionsWhenPaused(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}
	h.state.PauseFor(time.Hour)

	err := h.Bootstrap()
	if err != nil {
		t.Fatalf("Bootstrap() error = %v, want nil", err)
	}
}

func TestBootstrapWithActionsSequencing(t *testing.T) {
	h := &Handler{state: NewStateMachine()}

	steps := make([]string, 0, 2)
	err := h.bootstrapWithActions(func() error {
		steps = append(steps, "start")
		return nil
	}, func() error {
		steps = append(steps, "search")
		return nil
	})

	if err != nil {
		t.Fatalf("bootstrapWithActions() error = %v, want nil", err)
	}

	if len(steps) != 2 {
		t.Fatalf("steps len = %d, want 2", len(steps))
	}

	if steps[0] != "start" || steps[1] != "search" {
		t.Fatalf("steps = %v, want [start search]", steps)
	}
}

func TestBootstrapWithActionsStillRunsSearchWhenStartFails(t *testing.T) {
	h := &Handler{state: NewStateMachine()}

	searchCalled := false
	err := h.bootstrapWithActions(func() error {
		return errors.New("start failed")
	}, func() error {
		searchCalled = true
		return nil
	})

	if err == nil {
		t.Fatal("bootstrapWithActions() error = nil, want non-nil")
	}

	if !searchCalled {
		t.Fatal("search action was not called after start failure")
	}

	if !strings.Contains(err.Error(), "send /start") {
		t.Fatalf("bootstrapWithActions() error = %v, want send /start details", err)
	}
}

func TestBootstrapWithActionsAllowsNilSearch(t *testing.T) {
	h := &Handler{state: NewStateMachine()}

	steps := make([]string, 0, 1)
	err := h.bootstrapWithActions(func() error {
		steps = append(steps, "start")
		return nil
	}, nil)
	if err != nil {
		t.Fatalf("bootstrapWithActions() error = %v, want nil", err)
	}

	if len(steps) != 1 || steps[0] != "start" {
		t.Fatalf("steps = %v, want [start]", steps)
	}
}

func TestExtractBioText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "only spaces", in: "   ", want: ""},
		{name: "profile with en dash", in: "Ксюша, 23, Нижний Новгород – Исключительно общение", want: "Исключительно общение"},
		{name: "profile with hyphen", in: "Ксюша, 23, Нижний Новгород - Исключительно общение", want: "Исключительно общение"},
		{name: "bio only", in: "Ищу серьезные отношения", want: "Ищу серьезные отношения"},
		{name: "separator no bio", in: "Ксюша, 23, Нижний Новгород –   ", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractBioText(tt.in); got != tt.want {
				t.Fatalf("extractBioText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsLowQualityUsesBioOnly(t *testing.T) {
	h := &Handler{config: &standalone.Config{DatingSkipLowQuality: true, DatingMinBioLength: 50}}

	shortBio := "Ксюша, 23, Нижний Новгород – Коротко о себе"
	if !h.isLowQuality(shortBio) {
		t.Fatalf("isLowQuality(%q) = false, want true", shortBio)
	}

	longBio := "Ксюша, 23, Нижний Новгород – " + strings.Repeat("а", 60)
	if h.isLowQuality(longBio) {
		t.Fatalf("isLowQuality(%q) = true, want false", longBio)
	}
}

func TestIsLowQualityEmptyTextWhenEnabled(t *testing.T) {
	h := &Handler{config: &standalone.Config{DatingSkipLowQuality: true, DatingMinBioLength: 50}}

	if !h.isLowQuality("") {
		t.Fatal("isLowQuality(\"\") = false, want true")
	}
}

func TestIsLowQualityDisabled(t *testing.T) {
	h := &Handler{config: &standalone.Config{DatingSkipLowQuality: false, DatingMinBioLength: 50}}

	if h.isLowQuality("") {
		t.Fatal("isLowQuality disabled should return false for empty text")
	}

	if h.isLowQuality("коротко") {
		t.Fatal("isLowQuality disabled should return false for short text")
	}
}
