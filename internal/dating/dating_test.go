package dating

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/amarnathcjd/gogram/telegram"
)

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

	job2 := mustDequeueJob(t, h.state)
	if job2.Type != "menu_recovery" {
		t.Fatalf("second queued job type = %q, want %q", job2.Type, "menu_recovery")
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
			name:  "viewing state with text but non-empty markup",
			state: StateViewingProfiles,
			message: &telegram.NewMessage{Message: &telegram.MessageObj{
				Message:     "This is a generic informational notice.",
				ReplyMarkup: &telegram.ReplyInlineMarkup{},
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
}

func TestSendTruncatedMessageCacheMissPreservesState(t *testing.T) {
	h := &Handler{chatID: 123456789, state: NewStateMachine()}
	h.state.SetState(StateWaitingPrompt)
	h.state.SetPendingMessage("truncated")
	h.state.SetProfileData(&ProfileData{ProfileText: "bio", PhotoPaths: []string{"/tmp/photo.jpg"}})
	h.state.IncrementRetry()

	err := h.sendTruncatedMessage("truncated")
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
