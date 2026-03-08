package dating

import (
	"context"
	"sync"
	"time"

	"github.com/amarnathcjd/gogram/telegram"
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

// ProfileJob represents a job to process a profile from the queue
type ProfileJob struct {
	Type             string               // "message" or "album"
	Message          *telegram.NewMessage // nil for album jobs
	Album            *telegram.Album      // nil for message jobs
	ProfileMessageID int32                // anchor message ID for stale/duplicate suppression
}

type StateMachine struct {
	mu             sync.RWMutex
	state          State
	pendingMessage string
	retryCount     int
	profileData    *ProfileData
	pausedUntil    time.Time
	ownProfileSkip ownProfileSkipContext
	// New fields for worker pool pattern:
	profileQueue          chan ProfileJob // buffered channel for profile queue
	quitChan              chan struct{}   // for graceful shutdown
	workerDone            chan struct{}
	workerActive          bool
	acceptingWork         bool
	workerCtx             context.Context
	workerCancel          context.CancelFunc
	recoveryQueued        map[string]bool
	groupedCaptions       map[int64]groupedCaptionContext
	startupOwnProfileSkip startupOwnProfileSkipContext
	latestProfileJobID    int32
	lastProcessedJobID    int32
}

const ownProfileSkipTTL = 45 * time.Second
const ownProfileSkipMaxMessageGap int32 = 3
const groupedCaptionTTL = 2 * time.Minute
const startupOwnProfileSkipTTL = 90 * time.Second

type ownProfileSkipContext struct {
	markerMessageID int32
	setAt           time.Time
	active          bool
}

type groupedCaptionContext struct {
	text      string
	messageID int32
	setAt     time.Time
}

type startupOwnProfileSkipContext struct {
	setAt  time.Time
	active bool
}

func NewStateMachine() *StateMachine {
	workerDone := make(chan struct{})
	close(workerDone)

	return &StateMachine{
		state:         StateIdle,
		profileQueue:  make(chan ProfileJob, 50), // buffer for 50 profiles
		quitChan:      make(chan struct{}),
		workerDone:    workerDone,
		acceptingWork: true,
		recoveryQueued: map[string]bool{
			"menu_recovery":  false,
			"stuck_recovery": false,
		},
		groupedCaptions: make(map[int64]groupedCaptionContext),
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

	// Reset skip flag when stopping or going idle
	// (profiles won't be processed in these states anyway)
	if state == StateStopped || state == StateIdle {
		sm.ownProfileSkip = ownProfileSkipContext{}
		sm.startupOwnProfileSkip = startupOwnProfileSkipContext{}
		sm.latestProfileJobID = 0
		sm.lastProcessedJobID = 0
	}
}

func (sm *StateMachine) SetStateIfNotStopped(state State) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.state == StateStopped {
		return false
	}

	sm.state = state

	if state == StateIdle {
		sm.ownProfileSkip = ownProfileSkipContext{}
		sm.startupOwnProfileSkip = startupOwnProfileSkipContext{}
		sm.latestProfileJobID = 0
		sm.lastProcessedJobID = 0
	}

	return true
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

// Enqueue adds a job to the profile queue.
// Returns true if job was added, false if queue is full or shutdown has started.
func (sm *StateMachine) Enqueue(job ProfileJob) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.state == StateStopped || !sm.acceptingWork {
		return false
	}

	shouldDeduplicate := isRecoveryJobType(job.Type)
	if shouldDeduplicate && sm.recoveryQueued[job.Type] {
		return false
	}

	if shouldDeduplicate {
		sm.recoveryQueued[job.Type] = true
	}

	updatedLatestProfileID := false
	previousLatestProfileID := sm.latestProfileJobID
	if isProfileJobType(job.Type) && job.ProfileMessageID > sm.latestProfileJobID {
		sm.latestProfileJobID = job.ProfileMessageID
		updatedLatestProfileID = true
	}

	select {
	case sm.profileQueue <- job:
		return true
	default:
		if shouldDeduplicate {
			sm.recoveryQueued[job.Type] = false
		}
		if updatedLatestProfileID {
			sm.latestProfileJobID = previousLatestProfileID
		}
		return false // queue is full
	}
}

func isRecoveryJobType(jobType string) bool {
	return jobType == "menu_recovery" || jobType == "stuck_recovery"
}

func isProfileJobType(jobType string) bool {
	return jobType == "message" || jobType == "album"
}

func (sm *StateMachine) OnJobDequeued(jobType string) {
	if !isRecoveryJobType(jobType) {
		return
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.recoveryQueued[jobType] = false
}

func (sm *StateMachine) StopAcceptingWork() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.acceptingWork = false
}

// BeginShutdown performs the shutdown state transition atomically.
func (sm *StateMachine) BeginShutdown() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.state = StateStopped
	sm.acceptingWork = false
	sm.ownProfileSkip = ownProfileSkipContext{}
	sm.groupedCaptions = make(map[int64]groupedCaptionContext)
	sm.startupOwnProfileSkip = startupOwnProfileSkipContext{}
	sm.latestProfileJobID = 0
	sm.lastProcessedJobID = 0
}

// GetQueue returns the receive-only channel for the profile queue
func (sm *StateMachine) GetQueue() <-chan ProfileJob {
	return sm.profileQueue
}

// StopWorker signals the worker goroutine to stop
func (sm *StateMachine) StopWorker() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	select {
	case <-sm.quitChan:
		return
	default:
		close(sm.quitChan)
	}
}

func (sm *StateMachine) MarkWorkerStarted() bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.workerActive {
		return false
	}

	if !sm.acceptingWork {
		return false
	}

	select {
	case <-sm.quitChan:
		return false
	default:
	}

	sm.workerDone = make(chan struct{})
	sm.workerActive = true
	sm.workerCtx, sm.workerCancel = context.WithCancel(context.Background())
	return true
}

func (sm *StateMachine) MarkWorkerStopped() {
	sm.mu.Lock()
	if !sm.workerActive {
		sm.mu.Unlock()
		return
	}
	sm.workerActive = false
	workerDone := sm.workerDone
	cancel := sm.workerCancel
	sm.workerCtx = nil
	sm.workerCancel = nil
	sm.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	close(workerDone)
}

func (sm *StateMachine) WaitWorkerStop() {
	sm.mu.RLock()
	workerDone := sm.workerDone
	sm.mu.RUnlock()

	<-workerDone
}

func (sm *StateMachine) WorkerContext() context.Context {
	sm.mu.RLock()
	ctx := sm.workerCtx
	sm.mu.RUnlock()

	if ctx == nil {
		return context.Background()
	}

	return ctx
}

func (sm *StateMachine) CancelWorkerContext() {
	sm.mu.RLock()
	cancel := sm.workerCancel
	sm.mu.RUnlock()

	if cancel != nil {
		cancel()
	}
}

// ShouldQuit returns the channel that signals worker to stop
func (sm *StateMachine) ShouldQuit() <-chan struct{} {
	return sm.quitChan
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

func (sm *StateMachine) MarkOwnProfileSkip(markerMessageID int32, now time.Time) {
	if markerMessageID <= 0 {
		return
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.ownProfileSkip = ownProfileSkipContext{
		markerMessageID: markerMessageID,
		setAt:           now,
		active:          true,
	}
}

func (sm *StateMachine) ArmStartupOwnProfileSkip(now time.Time) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.startupOwnProfileSkip = startupOwnProfileSkipContext{
		setAt:  now,
		active: true,
	}
}

func (sm *StateMachine) ConsumeStartupOwnProfileSkip(now time.Time) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if !sm.startupOwnProfileSkip.active {
		return false
	}
	if now.Sub(sm.startupOwnProfileSkip.setAt) > startupOwnProfileSkipTTL {
		sm.startupOwnProfileSkip = startupOwnProfileSkipContext{}
		return false
	}

	sm.startupOwnProfileSkip = startupOwnProfileSkipContext{}
	return true
}

func (sm *StateMachine) ClearStartupOwnProfileSkip() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.startupOwnProfileSkip = startupOwnProfileSkipContext{}
}

// TryMarkProfileJobProcessing returns false if a profile job is stale or duplicate.
// It accepts only the latest enqueued profile job ID and strictly monotonic progression.
func (sm *StateMachine) TryMarkProfileJobProcessing(profileMessageID int32) (accepted bool, latest int32, lastProcessed int32) {
	if profileMessageID <= 0 {
		sm.mu.RLock()
		latest = sm.latestProfileJobID
		lastProcessed = sm.lastProcessedJobID
		sm.mu.RUnlock()
		return true, latest, lastProcessed
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	latest = sm.latestProfileJobID
	lastProcessed = sm.lastProcessedJobID

	if latest > 0 && profileMessageID < latest {
		return false, latest, lastProcessed
	}
	if lastProcessed > 0 && profileMessageID <= lastProcessed {
		return false, latest, lastProcessed
	}

	sm.lastProcessedJobID = profileMessageID
	return true, latest, sm.lastProcessedJobID
}

func (sm *StateMachine) ConsumeOwnProfileSkip(candidateMessageID int32, now time.Time) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if !sm.ownProfileSkip.active {
		return false
	}

	if now.Sub(sm.ownProfileSkip.setAt) > ownProfileSkipTTL {
		sm.ownProfileSkip = ownProfileSkipContext{}
		return false
	}

	if candidateMessageID <= 0 {
		return false
	}

	gap := candidateMessageID - sm.ownProfileSkip.markerMessageID
	if gap <= 0 || gap > ownProfileSkipMaxMessageGap {
		return false
	}

	sm.ownProfileSkip = ownProfileSkipContext{}
	return true
}

func (sm *StateMachine) RememberGroupedCaption(groupedID int64, text string, messageID int32, now time.Time) {
	if groupedID == 0 {
		return
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.pruneGroupedCaptionsLocked(now)

	sm.groupedCaptions[groupedID] = groupedCaptionContext{
		text:      text,
		messageID: messageID,
		setAt:     now,
	}
}

func (sm *StateMachine) ConsumeGroupedCaption(groupedID int64, now time.Time) (string, bool) {
	if groupedID == 0 {
		return "", false
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.pruneGroupedCaptionsLocked(now)

	entry, ok := sm.groupedCaptions[groupedID]
	if !ok {
		return "", false
	}
	delete(sm.groupedCaptions, groupedID)

	if now.Sub(entry.setAt) > groupedCaptionTTL {
		return "", false
	}

	return entry.text, true
}

func (sm *StateMachine) pruneGroupedCaptionsLocked(now time.Time) {
	for groupedID, entry := range sm.groupedCaptions {
		if now.Sub(entry.setAt) > groupedCaptionTTL {
			delete(sm.groupedCaptions, groupedID)
		}
	}
}
