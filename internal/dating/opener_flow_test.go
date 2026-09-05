package dating

import (
	"context"
	"sync"
	"testing"

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
		data, cleanup := h.downloadProfileData(context.Background(), m, h.state.ObserveProfileKeyboard(m))
		cleanup()
		if data.MessageButton != label {
			t.Fatalf("single profile lost label: %q", data.MessageButton)
		}
	}
	for _, markup := range []telegram.ReplyMarkup{nil, openerKeyboard("unknown"), openerKeyboard("\U0001f48c buy premium"), &telegram.ReplyInlineMarkup{Rows: []*telegram.KeyboardButtonRow{{Buttons: []telegram.KeyboardButton{&telegram.KeyboardButtonCallback{Text: "\U0001f48c"}}}}}} {
		h := &Handler{state: NewStateMachine(), client: &scriptedSummarizer{responses: []string{`{"action":"send","reason":"fit","message":"Favorite trail?"}`}}}
		h.clickButtonFn = func(context.Context, string) error { t.Fatal("sent command with unusable keyboard"); return nil }
		m := &telegram.NewMessage{ID: 10, Message: &telegram.MessageObj{ReplyMarkup: markup}}
		data, cleanup := h.downloadProfileData(context.Background(), m, h.state.ObserveProfileKeyboard(m))
		cleanup()
		if err := h.generateAndSendLike(context.Background(), data); err != nil {
			t.Fatal(err)
		}
		if !h.IsStopped() {
			t.Fatal("missing keyboard did not stop")
		}
	}
}

func TestPersistentProfileKeyboard(t *testing.T) {
	const label = "\U0001f48c \U0001f4f9 \U0001f3a4"
	for _, tc := range []struct {
		name   string
		markup telegram.ReplyMarkup
		want   string
	}{
		{"absent", nil, label},
		{"inline", &telegram.ReplyInlineMarkup{}, label},
		{"force reply", &telegram.ReplyKeyboardForceReply{}, label},
		{"hide", &telegram.ReplyKeyboardHide{}, ""},
		{"menu", openerKeyboard("unrelated"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sm := NewStateMachine()
			observe := func(id int32, markup telegram.ReplyMarkup) string {
				return sm.ObserveProfileKeyboard(&telegram.NewMessage{ID: id, Message: &telegram.MessageObj{ReplyMarkup: markup}})
			}
			if got := observe(100, openerKeyboard(label)); got != label {
				t.Fatal(got)
			}
			if got := observe(101, tc.markup); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
			if got := observe(900000, nil); got != tc.want {
				t.Fatalf("persistent snapshot = %q", got)
			}
			if got := observe(99, openerKeyboard(label)); got != "" {
				t.Fatal("future keyboard applied to stale card")
			}
			if got := observe(900001, nil); got != tc.want {
				t.Fatal("stale keyboard replaced current state")
			}
			sm.BeginShutdown()
			if got := observe(900002, openerKeyboard(label)); got != "" {
				t.Fatal("shutdown retained keyboard")
			}
		})
	}
}

func TestPersistentKeyboardSequentialCards(t *testing.T) {
	const label = "\U0001f48c \U0001f4f9 \U0001f3a4"
	h := &Handler{state: NewStateMachine(), client: &scriptedSummarizer{responses: []string{
		`{"action":"skip","reason":"not fit","message":""}`,
		`{"action":"send","reason":"fit","message":"Favorite trail?"}`,
		`{"action":"send","reason":"fit","message":"Favorite book?"}`,
	}}}
	var buttons []string
	h.clickButtonFn = func(_ context.Context, text string) error { buttons = append(buttons, text); return nil }
	first := &telegram.NewMessage{ID: 100, Message: &telegram.MessageObj{Message: "First card", ReplyMarkup: openerKeyboard(label)}}
	if err := h.Handle(first); err != nil {
		t.Fatal(err)
	}
	if err := h.processJob(context.Background(), mustDequeueJob(t, h.state)); err != nil {
		t.Fatal(err)
	}
	second := &telegram.NewMessage{ID: 110, Message: &telegram.MessageObj{Media: &telegram.MessageMediaPhoto{}}}
	if err := h.Handle(second); err != nil {
		t.Fatal(err)
	}
	job := mustDequeueJob(t, h.state)
	// A newer menu must invalidate future cards, not mutate an already queued snapshot.
	if err := h.Handle(&telegram.NewMessage{ID: 111, Message: &telegram.MessageObj{ReplyMarkup: openerKeyboard("unrelated")}}); err != nil {
		t.Fatal(err)
	}
	if err := h.processJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if len(buttons) != 2 || buttons[0] != ButtonDislike || buttons[1] != label {
		t.Fatalf("buttons = %q", buttons)
	}
	third := &telegram.NewMessage{ID: 112, Message: &telegram.MessageObj{Message: "Third card", Media: &telegram.MessageMediaPhoto{}}}
	if err := h.Handle(third); err != nil {
		t.Fatal(err)
	}
	job = mustDequeueJob(t, h.state)
	if job.MessageButton != "" {
		t.Fatalf("menu failed to invalidate: %q", job.MessageButton)
	}
	if err := h.processJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if !h.IsStopped() || len(buttons) != 2 {
		t.Fatalf("invalid keyboard used: stopped=%v buttons=%q", h.IsStopped(), buttons)
	}
}

func TestAlbumKeyboardReceptionSnapshot(t *testing.T) {
	const label = "\U0001f48c \U0001f4f9 \U0001f3a4"
	h := &Handler{state: NewStateMachine()}
	h.state.ObserveProfileKeyboard(&telegram.NewMessage{ID: 90, Message: &telegram.MessageObj{ReplyMarkup: openerKeyboard(label)}})
	part := &telegram.NewMessage{ID: 100, Message: &telegram.MessageObj{GroupedID: 42, Message: "Album card"}}
	if err := h.Handle(part); err != nil {
		t.Fatal(err)
	}
	h.state.ObserveProfileKeyboard(&telegram.NewMessage{ID: 110, Message: &telegram.MessageObj{ReplyMarkup: openerKeyboard("unrelated")}})
	if err := h.HandleAlbum(&telegram.Album{Messages: []*telegram.NewMessage{part}}); err != nil {
		t.Fatal(err)
	}
	job := mustDequeueJob(t, h.state)
	if job.MessageButton != label {
		t.Fatalf("delayed album lost reception snapshot: %q", job.MessageButton)
	}
	data, cleanup := h.downloadAlbumData(context.Background(), job.Album, "Album card", job.MessageButton)
	defer cleanup()
	if data.MessageButton != label {
		t.Fatalf("prepared album snapshot: %q", data.MessageButton)
	}
	stale := &telegram.NewMessage{ID: 99, Message: &telegram.MessageObj{GroupedID: 43}}
	if got := h.state.ObserveProfileKeyboard(stale); got != "" {
		t.Fatalf("future/cross-album keyboard: %q", got)
	}
}

func TestPersistentKeyboardConcurrentUpdates(t *testing.T) {
	sm := NewStateMachine()
	var wg sync.WaitGroup
	for id := int32(1); id <= 64; id++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			markup := openerKeyboard(ButtonLikeMessage)
			if id == 64 {
				markup = &telegram.ReplyKeyboardHide{}
			}
			sm.ObserveProfileKeyboard(&telegram.NewMessage{ID: id, Message: &telegram.MessageObj{ReplyMarkup: markup}})
		}()
	}
	wg.Wait()
	if got := sm.ObserveProfileKeyboard(&telegram.NewMessage{ID: 65, Message: &telegram.MessageObj{}}); got != "" {
		t.Fatalf("older concurrent keyboard replaced latest hide: %q", got)
	}
}
