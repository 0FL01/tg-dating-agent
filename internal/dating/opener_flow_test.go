package dating

import (
	"context"
	"testing"
	"time"

	"github.com/amarnathcjd/gogram/telegram"
)

func openerKeyboard(label string) telegram.ReplyMarkup {
	return &telegram.ReplyKeyboardMarkup{Rows: []*telegram.KeyboardButtonRow{{Buttons: []telegram.KeyboardButton{
		&telegram.KeyboardButtonObj{Text: label},
		&telegram.KeyboardButtonObj{Text: ButtonDislike},
	}}}}
}

func TestObservedAlbumOpenerFlow(t *testing.T) {
	const label = "\U0001f48c \U0001f4f9 \U0001f3a4"
	for _, confirmBefore := range []bool{false, true} {
		t.Run(map[bool]string{false: "after text", true: "before text"}[confirmBefore], func(t *testing.T) {
			h := &Handler{state: NewStateMachine(), client: &scriptedSummarizer{responses: []string{`{"action":"send","reason":"fit","message":"Favorite trail?"}`}}}
			h.setBotPeer(&telegram.InputPeerUser{UserID: 1})
			var buttons, texts []string
			h.clickButtonFn = func(_ context.Context, text string) error { buttons = append(buttons, text); return nil }
			h.sendMessageFn = func(_ context.Context, _ telegram.InputPeer, text string) error {
				texts = append(texts, text)
				return nil
			}
			individual := &telegram.NewMessage{ID: 100, Message: &telegram.MessageObj{GroupedID: 42, Message: "Profile", ReplyMarkup: openerKeyboard(label)}}
			if err := h.Handle(individual); err != nil {
				t.Fatal(err)
			}
			if len(h.state.GetQueue()) != 0 {
				t.Fatal("individual album callback queued a profile")
			}
			album := &telegram.Album{Messages: []*telegram.NewMessage{{ID: 100, Message: &telegram.MessageObj{GroupedID: 42, Message: "Profile"}}}}
			if err := h.HandleAlbum(album); err != nil {
				t.Fatal(err)
			}
			if err := h.processJob(context.Background(), <-h.state.GetQueue()); err != nil {
				t.Fatal(err)
			}
			if len(buttons) != 1 || buttons[0] != label {
				t.Fatalf("buttons=%q", buttons)
			}
			if !confirmBefore {
				// Old prompts cannot authorize the current profile's opener.
				if err := h.Handle(&telegram.NewMessage{ID: 99, Message: &telegram.MessageObj{Message: PatternWriteMessage}}); err != nil {
					t.Fatal(err)
				}
				if len(texts) != 0 {
					t.Fatal("stale prompt sent text")
				}
				for range 2 {
					if err := h.Handle(&telegram.NewMessage{ID: 101, Message: &telegram.MessageObj{Message: PatternWriteMessage}}); err != nil {
						t.Fatal(err)
					}
				}
				if len(texts) != 1 || texts[0] != "Favorite trail?" {
					t.Fatalf("texts=%q", texts)
				}
			}
			for range 2 {
				if err := h.Handle(&telegram.NewMessage{ID: 102, Message: &telegram.MessageObj{Message: PatternSendMessage, ReplyMarkup: openerKeyboard("\U0001f48c")}}); err != nil {
					t.Fatal(err)
				}
			}
			if !h.IsStopped() || len(buttons) != 1 || (confirmBefore && len(texts) != 0) {
				t.Fatalf("unsafe confirmation: buttons=%q texts=%q", buttons, texts)
			}
		})
	}
}

func TestProfileMessageKeyboardSafety(t *testing.T) {
	for _, label := range []string{"\U0001f48c \U0001f4f9 \U0001f3a4", "\U0001f48c \U0001f4f7 \U0001f3a4", ButtonLikeMessage} {
		m := &telegram.NewMessage{Message: &telegram.MessageObj{ReplyMarkup: openerKeyboard(label)}}
		if got, ok := profileMessageButtonText(m); !ok || got != label {
			t.Fatalf("label=%q got=%q ok=%v", label, got, ok)
		}
		h := &Handler{state: NewStateMachine()}
		data, cleanup := h.downloadProfileData(context.Background(), m)
		cleanup()
		if data.MessageButton != label {
			t.Fatalf("single profile lost label: %q", data.MessageButton)
		}
	}
	for _, markup := range []telegram.ReplyMarkup{nil, openerKeyboard("unknown"), openerKeyboard("\U0001f48c buy premium"), &telegram.ReplyInlineMarkup{Rows: []*telegram.KeyboardButtonRow{{Buttons: []telegram.KeyboardButton{&telegram.KeyboardButtonCallback{Text: "\U0001f48c"}}}}}} {
		h := &Handler{state: NewStateMachine(), client: &scriptedSummarizer{responses: []string{`{"action":"send","reason":"fit","message":"Favorite trail?"}`}}}
		h.clickButtonFn = func(context.Context, string) error { t.Fatal("sent command with unusable keyboard"); return nil }
		m := &telegram.NewMessage{ID: 10, Message: &telegram.MessageObj{ReplyMarkup: markup}}
		data, cleanup := h.downloadProfileData(context.Background(), m)
		cleanup()
		if err := h.generateAndSendLike(context.Background(), data); err != nil {
			t.Fatal(err)
		}
		if !h.IsStopped() {
			t.Fatal("missing keyboard did not stop")
		}
	}
}

func TestGroupedButtonCorrelation(t *testing.T) {
	sm := NewStateMachine()
	now := time.Now()
	sm.RememberGroupedButton(42, "label", 100, now)
	if got := sm.ConsumeGroupedButton(43, 100, now); got != "" {
		t.Fatal("cross-album label")
	}
	if got := sm.ConsumeGroupedButton(42, 99, now); got != "" {
		t.Fatal("future label")
	}
	sm.RememberGroupedButton(42, "label", 100, now)
	if got := sm.ConsumeGroupedButton(42, 100, now.Add(groupedCaptionTTL+time.Second)); got != "" {
		t.Fatal("expired label")
	}
}
