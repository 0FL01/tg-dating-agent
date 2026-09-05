package dating

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0FL01/tg-dating-agent/internal/llm"
	"github.com/amarnathcjd/gogram/telegram"
)

func TestStructuredDecisionFlow(t *testing.T) {
	const send = `{"action":"send","reason":"fit","message":"A personal question?"}`
	const skip = `{"action":"skip","reason":"no hook","message":""}`
	for _, tc := range []struct {
		name      string
		responses []string
		button    string
		calls     int
		cached    bool
	}{
		{"send without bio gate", []string{send}, ButtonLikeMessage, 1, true},
		{"skip", []string{skip}, ButtonDislike, 1, true},
		{"corrected", []string{"not JSON", send}, ButtonLikeMessage, 2, true},
		{"malformed exhausted", []string{"bad", "bad", "bad"}, ButtonDislike, 3, false},
		{"failed call", nil, ButtonDislike, 1, false},
		{"no greeting fallback", []string{`{"action":"send","reason":"fit","message":""}`, "bad", "bad"}, ButtonDislike, 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &scriptedSummarizer{responses: tc.responses}
			audit := &stubReplyAuditLogger{}
			var buttons []string
			h := &Handler{state: NewStateMachine(), client: client, prompt: "my selection criteria", model: "vision", clickButtonFn: func(_ context.Context, button string) error { buttons = append(buttons, button); return nil }}
			h.replyAudit = audit
			if err := h.generateAndSendLike(context.Background(), ProfileData{ProfileText: "", MessageButton: ButtonLikeMessage}); err != nil {
				t.Fatal(err)
			}
			if len(buttons) != 1 || buttons[0] != tc.button || client.snapshotCallCount() != tc.calls {
				t.Fatalf("buttons=%v calls=%d", buttons, client.snapshotCallCount())
			}
			if _, ok := h.state.GetProfileLLMCache(buildProfileLLMCacheKey("", nil)); ok != tc.cached {
				t.Fatalf("cached=%v, want %v", ok, tc.cached)
			}
			if tc.button == ButtonDislike && h.state.GetPendingMessage() != "" {
				t.Fatal("unsafe pending message")
			}
			for _, prompt := range client.prompts {
				if prompt != h.prompt {
					t.Fatal("correction replaced original criteria")
				}
			}
			if tc.name == "corrected" && (!strings.Contains(client.contents[1].Text, "not JSON") || !strings.Contains(client.contents[1].Text, "Validation error:")) {
				t.Fatal("missing correction feedback")
			}
			calls := audit.snapshotCalls()
			if len(calls) != tc.calls {
				t.Fatalf("audit records=%d want %d", len(calls), tc.calls)
			}
			for i, call := range calls {
				wantEvent := "error"
				if i < len(tc.responses) {
					wantEvent = "invalid_response"
					if _, err := llm.ParseDecision(tc.responses[i]); err == nil {
						wantEvent = "decision"
					}
				}
				if call.event != wantEvent || call.model != h.model || call.prompt != h.prompt {
					t.Fatalf("audit=%+v want event %s", call, wantEvent)
				}
				if wantEvent != "decision" && (call.decision != (llm.Decision{}) || call.detail == "") {
					t.Fatalf("invalid output retained or missing error: %+v", call)
				}
			}
		})
	}
}

func TestTelegramRejectionRetainsPhotosAndStopsWithoutFallback(t *testing.T) {
	for _, terminal := range []string{"skip", "malformed", "failed", "timeout", "exhausted"} {
		t.Run(terminal, func(t *testing.T) {
			const first = `{"action":"send","reason":"fit","message":"Original question?"}`
			const corrected = `{"action":"send","reason":"fit","message":"Revised question?"}`
			responses := []string{first}
			switch terminal {
			case "skip":
				responses = append(responses, `{"action":"skip","reason":"no longer fits","message":""}`)
			case "malformed":
				responses = append(responses, "bad", "bad", "bad")
			case "exhausted":
				responses = append(responses, corrected, corrected)
			}
			client := &scriptedSummarizer{responses: responses}
			audit := &stubReplyAuditLogger{}
			if terminal == "timeout" {
				client.err = context.DeadlineExceeded
			}
			var sends, buttons []string
			h := &Handler{state: NewStateMachine(), client: client, prompt: "original selection criteria", model: "vision", clickButtonFn: func(_ context.Context, b string) error { buttons = append(buttons, b); return nil }, sendMessageFn: func(_ context.Context, _ telegram.InputPeer, msg string) error {
				sends = append(sends, msg)
				return nil
			}}
			h.setBotPeer(&telegram.InputPeerUser{UserID: 1})
			h.replyAudit = audit
			photo := filepath.Join(t.TempDir(), "profile.jpg")
			if err := os.WriteFile(photo, []byte("photo bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := h.generateAndSendLike(context.Background(), ProfileData{ProfileText: "bio", PhotoPaths: []string{photo}, MessageButton: ButtonLikeMessage}); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(photo); err != nil {
				t.Fatal(err)
			}
			if err := h.sendPendingMessage(nil); err != nil {
				t.Fatal(err)
			}
			data := h.state.GetProfileData()
			if data == nil || len(data.Content.ImageURLs) != 1 || len(data.Content.ImagePaths) != 0 {
				t.Fatal("API send cleared retry photos")
			}
			rejection := PatternTooLong + ": please use fewer characters"
			for range 3 {
				if err := h.Handle(&telegram.NewMessage{Message: &telegram.MessageObj{Message: rejection}}); err != nil {
					t.Fatal(err)
				}
			}
			if !h.IsStopped() || h.state.GetProfileData() != nil || h.state.GetPendingMessage() != "" {
				t.Fatal("unsafe terminal state or retained photos")
			}
			wantSends := 1
			if terminal == "exhausted" {
				wantSends = 3
			}
			if len(sends) != wantSends || len(buttons) != 1 || buttons[0] != ButtonLikeMessage {
				t.Fatalf("sends=%v buttons=%v", sends, buttons)
			}
			auditedSends := 0
			for _, call := range audit.snapshotCalls() {
				if call.event == "sent" {
					if auditedSends >= len(sends) || call.decision.Message != sends[auditedSends] || call.profileText != "bio" || call.prompt != h.prompt {
						t.Fatalf("sent audit mismatch: %+v", call)
					}
					auditedSends++
				}
			}
			if auditedSends != wantSends {
				t.Fatalf("sent audit count=%d want %d", auditedSends, wantSends)
			}
			for i, content := range client.contents {
				if client.prompts[i] != h.prompt || len(content.ImageURLs) != 1 || content.ImageURLs[0] != data.Content.ImageURLs[0] {
					t.Fatal("retry lost original prompt/photos")
				}
				if i > 0 && (!strings.Contains(content.Text, rejection) || !strings.Contains(content.Text, "Previous output:")) {
					t.Fatal("retry lost original output/rejection")
				}
			}
		})
	}
}

func TestDecisionContextReleasedOnReplacementAndMenu(t *testing.T) {
	client := &scriptedSummarizer{responses: []string{`{"action":"skip","reason":"no","message":""}`}}
	h := &Handler{state: NewStateMachine(), client: client, clickButtonFn: func(context.Context, string) error { return nil }}
	h.state.SetProfileData(&ProfileData{Content: llm.MultimodalContent{ImageURLs: []string{"old photo"}}})
	if err := h.generateAndSendLike(context.Background(), ProfileData{ProfileText: "new profile"}); err != nil {
		t.Fatal(err)
	}
	if h.state.GetProfileData() != nil {
		t.Fatal("replacement retained old photos")
	}
	h.state.SetProfileData(&ProfileData{Content: llm.MultimodalContent{ImageURLs: []string{"photo"}}})
	if err := h.Handle(&telegram.NewMessage{Message: &telegram.MessageObj{Message: PatternViewProfiles}}); err != nil {
		t.Fatal(err)
	}
	if h.state.GetProfileData() != nil {
		t.Fatal("menu completion retained photos")
	}
}

func TestSentAuditRequiresSuccessfulAPISend(t *testing.T) {
	for _, outcome := range []string{"failed", "canceled", "shutdown during success"} {
		t.Run(outcome, func(t *testing.T) {
			audit := &stubReplyAuditLogger{}
			h := &Handler{state: NewStateMachine(), replyAudit: audit, model: "model"}
			h.state.SetState(StateWaitingPrompt)
			h.state.SetProfileData(&ProfileData{ProfileText: "bio", Prompt: "original", Decision: llm.Decision{Action: "send", Reason: "fit", Message: "proposal"}})
			h.state.SetPendingMessage("actual message")
			h.setBotPeer(&telegram.InputPeerUser{UserID: 1})
			h.sendMessageFn = func(context.Context, telegram.InputPeer, string) error {
				if outcome == "failed" {
					return errors.New("API failure")
				}
				if outcome == "canceled" {
					t.Fatal("send after cancellation")
				}
				h.Shutdown()
				return nil
			}
			if outcome == "canceled" {
				h.lifecycleContext()
				h.cancelLifecycleContext()
			}
			err := h.sendPendingMessage(nil)
			if (err != nil) != (outcome == "failed") {
				t.Fatalf("send error=%v", err)
			}
			calls := audit.snapshotCalls()
			if outcome == "canceled" {
				if len(calls) != 0 {
					t.Fatal("canceled send audited as sent")
				}
				return
			}
			wantEvent := "sent"
			if outcome == "failed" {
				wantEvent = "error"
			}
			if len(calls) != 1 || calls[0].event != wantEvent || calls[0].decision.Message != "actual message" || calls[0].profileText != "bio" || calls[0].prompt != "original" {
				t.Fatalf("audit=%+v", calls)
			}
		})
	}
}
