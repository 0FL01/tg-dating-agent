package dating

import (
	"sync"
	"time"
)

type State int

const (
	StateIdle State = iota
	StateViewingProfiles
	StateWaitingPrompt
	StateStopped
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateViewingProfiles:
		return "viewing_profiles"
	case StateWaitingPrompt:
		return "waiting_prompt"
	case StateStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

type ProfileData struct {
	PhotoPaths  []string
	ProfileText string
}

type StateMachine struct {
	mu             sync.RWMutex
	state          State
	pendingMessage string
	processing     bool
	retryCount     int
	profileData    *ProfileData
	pausedUntil    time.Time
}

func NewStateMachine() *StateMachine {
	return &StateMachine{
		state: StateIdle,
	}
}

func (sm *StateMachine) GetState() State {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state
}

func (sm *StateMachine) SetState(state State) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.state = state
}

func (sm *StateMachine) GetPendingMessage() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.pendingMessage
}

func (sm *StateMachine) SetPendingMessage(msg string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.pendingMessage = msg
}

func (sm *StateMachine) ClearPendingMessage() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.pendingMessage = ""
}

func (sm *StateMachine) IsStopped() bool {
	return sm.GetState() == StateStopped
}

func (sm *StateMachine) TryStartProcessing() bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.processing {
		return false
	}
	sm.processing = true
	return true
}

func (sm *StateMachine) StopProcessing() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.processing = false
}

func (sm *StateMachine) IsProcessing() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.processing
}

func (sm *StateMachine) IncrementRetry() int {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.retryCount++
	return sm.retryCount
}

func (sm *StateMachine) GetRetryCount() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.retryCount
}

func (sm *StateMachine) ResetRetry() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.retryCount = 0
}

func (sm *StateMachine) SetProfileData(data *ProfileData) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.profileData = data
}

func (sm *StateMachine) GetProfileData() *ProfileData {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.profileData
}

func (sm *StateMachine) ClearProfileData() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.profileData = nil
}

func (sm *StateMachine) FinalizeSendState(expectedCurrent State) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.pendingMessage = ""
	sm.profileData = nil
	sm.retryCount = 0

	if sm.state == expectedCurrent {
		sm.state = StateViewingProfiles
		return true
	}

	return false
}

func (sm *StateMachine) PauseFor(duration time.Duration) time.Time {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.pausedUntil = time.Now().Add(duration)
	return sm.pausedUntil
}

func (sm *StateMachine) CheckPause(now time.Time) (paused bool, resumed bool, until time.Time) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.pausedUntil.IsZero() {
		return false, false, time.Time{}
	}

	if now.Before(sm.pausedUntil) {
		return true, false, sm.pausedUntil
	}

	sm.pausedUntil = time.Time{}
	return false, true, time.Time{}
}
