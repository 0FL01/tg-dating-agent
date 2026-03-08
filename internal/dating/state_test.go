package dating

import (
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
