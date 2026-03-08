package dating

import (
	"sync"
	"testing"
	"time"
)

func TestStateMachinePauseLifecycle(t *testing.T) {
	sm := NewStateMachine()

	paused, resumed, until := sm.CheckPause(time.Now())
	if paused || resumed || !until.IsZero() {
		t.Fatalf("initial CheckPause() = (%v, %v, %v), want (false, false, zero)", paused, resumed, until)
	}

	pausedUntil := sm.PauseFor(2 * time.Minute)
	if !sm.pausedUntil.Equal(pausedUntil) {
		t.Fatalf("pausedUntil field = %v, want %v", sm.pausedUntil, pausedUntil)
	}

	paused, resumed, until = sm.CheckPause(pausedUntil.Add(-time.Nanosecond))
	if !paused || resumed || !until.Equal(pausedUntil) {
		t.Fatalf("CheckPause(before) = (%v, %v, %v), want (true, false, %v)", paused, resumed, until, pausedUntil)
	}

	paused, resumed, until = sm.CheckPause(pausedUntil)
	if paused || !resumed || !until.IsZero() {
		t.Fatalf("CheckPause(at deadline) = (%v, %v, %v), want (false, true, zero)", paused, resumed, until)
	}

	paused, resumed, until = sm.CheckPause(pausedUntil.Add(time.Nanosecond))
	if paused || resumed || !until.IsZero() {
		t.Fatalf("CheckPause(after clear) = (%v, %v, %v), want (false, false, zero)", paused, resumed, until)
	}
}

func TestStateMachinePauseForOverwritesExistingPause(t *testing.T) {
	sm := NewStateMachine()

	first := sm.PauseFor(1 * time.Second)
	second := sm.PauseFor(1 * time.Hour)

	if !sm.pausedUntil.Equal(second) {
		t.Fatalf("pausedUntil field = %v, want %v", sm.pausedUntil, second)
	}

	if !second.After(first) {
		t.Fatalf("second pause deadline = %v, want after first = %v", second, first)
	}
}

func TestStateMachineOwnProfileSkipExpiresByTTL(t *testing.T) {
	sm := NewStateMachine()
	now := time.Unix(1000, 0)

	sm.MarkOwnProfileSkip(100, now)

	if got := sm.ConsumeOwnProfileSkip(101, now.Add(ownProfileSkipTTL+time.Nanosecond)); got {
		t.Fatal("ConsumeOwnProfileSkip() = true after TTL expiry, want false")
	}

	if got := sm.ConsumeOwnProfileSkip(101, now.Add(ownProfileSkipTTL+2*time.Nanosecond)); got {
		t.Fatal("ConsumeOwnProfileSkip() = true after context expired and cleared, want false")
	}
}

func TestStateMachineOwnProfileSkipInterleavingWrongFirstMedia(t *testing.T) {
	sm := NewStateMachine()
	now := time.Unix(2000, 0)

	sm.MarkOwnProfileSkip(100, now)

	if got := sm.ConsumeOwnProfileSkip(110, now.Add(time.Second)); got {
		t.Fatal("ConsumeOwnProfileSkip() = true for non-correlated media, want false")
	}

	if got := sm.ConsumeOwnProfileSkip(101, now.Add(2*time.Second)); !got {
		t.Fatal("ConsumeOwnProfileSkip() = false for correlated own profile media, want true")
	}

	if got := sm.ConsumeOwnProfileSkip(102, now.Add(3*time.Second)); got {
		t.Fatal("ConsumeOwnProfileSkip() = true after successful consume, want false")
	}
}

func TestStateMachineEnqueueRejectsAfterStopAcceptingWork(t *testing.T) {
	sm := NewStateMachine()

	if ok := sm.Enqueue(ProfileJob{Type: "message"}); !ok {
		t.Fatal("initial Enqueue() = false, want true")
	}

	sm.StopAcceptingWork()

	if ok := sm.Enqueue(ProfileJob{Type: "message"}); ok {
		t.Fatal("Enqueue() after StopAcceptingWork = true, want false")
	}
}

func TestStateMachineEnqueueRejectsAfterStopAcceptingWorkConcurrently(t *testing.T) {
	sm := NewStateMachine()
	sm.StopAcceptingWork()

	const workers = 32
	var wg sync.WaitGroup
	successes := make(chan struct{}, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if sm.Enqueue(ProfileJob{Type: "message"}) {
				successes <- struct{}{}
			}
		}()
	}

	wg.Wait()
	close(successes)

	if len(successes) != 0 {
		t.Fatalf("successful Enqueue() count = %d, want 0", len(successes))
	}
}

func TestStateMachineEnqueueDeduplicatesRecoveryJobsUntilDequeued(t *testing.T) {
	sm := NewStateMachine()

	if ok := sm.Enqueue(ProfileJob{Type: "menu_recovery"}); !ok {
		t.Fatal("first Enqueue(menu_recovery) = false, want true")
	}

	if ok := sm.Enqueue(ProfileJob{Type: "menu_recovery"}); ok {
		t.Fatal("second Enqueue(menu_recovery) = true, want false")
	}

	sm.OnJobDequeued("menu_recovery")

	if ok := sm.Enqueue(ProfileJob{Type: "menu_recovery"}); !ok {
		t.Fatal("Enqueue(menu_recovery) after dequeue = false, want true")
	}
}

func TestStateMachineBeginShutdownAtomicallyStopsAndRejectsEnqueue(t *testing.T) {
	sm := NewStateMachine()
	sm.MarkOwnProfileSkip(123, time.Now())

	sm.BeginShutdown()

	if got := sm.GetState(); got != StateStopped {
		t.Fatalf("state after BeginShutdown() = %v, want %v", got, StateStopped)
	}

	if sm.acceptingWork {
		t.Fatal("acceptingWork after BeginShutdown() = true, want false")
	}

	if got := sm.ConsumeOwnProfileSkip(124, time.Now()); got {
		t.Fatal("own-profile skip context still active after BeginShutdown()")
	}

	if ok := sm.Enqueue(ProfileJob{Type: "message"}); ok {
		t.Fatal("Enqueue() after BeginShutdown() = true, want false")
	}
}

func TestStateMachineGroupedCaptionConsumeLifecycle(t *testing.T) {
	sm := NewStateMachine()
	now := time.Unix(3000, 0)

	sm.RememberGroupedCaption(777, "caption text", 10, now)

	got, ok := sm.ConsumeGroupedCaption(777, now.Add(time.Second))
	if !ok {
		t.Fatal("ConsumeGroupedCaption() ok = false, want true")
	}
	if got != "caption text" {
		t.Fatalf("ConsumeGroupedCaption() text = %q, want %q", got, "caption text")
	}

	if _, ok := sm.ConsumeGroupedCaption(777, now.Add(2*time.Second)); ok {
		t.Fatal("ConsumeGroupedCaption() ok = true after consume, want false")
	}
}

func TestStateMachineGroupedCaptionExpiresByTTL(t *testing.T) {
	sm := NewStateMachine()
	now := time.Unix(4000, 0)

	sm.RememberGroupedCaption(888, "stale caption", 11, now)

	if got, ok := sm.ConsumeGroupedCaption(888, now.Add(groupedCaptionTTL+time.Nanosecond)); ok || got != "" {
		t.Fatalf("ConsumeGroupedCaption() = (%q, %v), want (\"\", false)", got, ok)
	}
}

func TestStateMachineStartupOwnProfileSkipConsumeLifecycle(t *testing.T) {
	sm := NewStateMachine()
	now := time.Unix(5000, 0)

	sm.ArmStartupOwnProfileSkip(now)

	if got := sm.ConsumeStartupOwnProfileSkip(now.Add(time.Second)); !got {
		t.Fatal("ConsumeStartupOwnProfileSkip() = false, want true")
	}

	if got := sm.ConsumeStartupOwnProfileSkip(now.Add(2 * time.Second)); got {
		t.Fatal("ConsumeStartupOwnProfileSkip() = true after consume, want false")
	}
}

func TestStateMachineStartupOwnProfileSkipExpiresByTTL(t *testing.T) {
	sm := NewStateMachine()
	now := time.Unix(6000, 0)

	sm.ArmStartupOwnProfileSkip(now)

	if got := sm.ConsumeStartupOwnProfileSkip(now.Add(startupOwnProfileSkipTTL + time.Nanosecond)); got {
		t.Fatal("ConsumeStartupOwnProfileSkip() = true after TTL, want false")
	}
}

func TestStateMachineStartupOwnProfileSkipClearedExplicitly(t *testing.T) {
	sm := NewStateMachine()

	sm.ArmStartupOwnProfileSkip(time.Now())
	sm.ClearStartupOwnProfileSkip()

	if got := sm.ConsumeStartupOwnProfileSkip(time.Now()); got {
		t.Fatal("ConsumeStartupOwnProfileSkip() = true after Clear, want false")
	}
}
