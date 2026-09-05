package dating

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/0FL01/tg-dating-agent/internal/llm"
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
	PhotoPaths       []string
	PhotoIdentifiers []string
	ProfileText      string
	Content          llm.MultimodalContent
	Prompt           string
	Decision         llm.Decision
}

// ProfileJob represents a job to process a profile from the queue
type ProfileJob struct {
	Type             string               // "message" or "album"
	Message          *telegram.NewMessage // nil for album jobs
	Album            *telegram.Album      // nil for message jobs
	ProfileMessageID int32                // anchor message ID for stale/duplicate suppression
}

type StateMachine struct {
	mu                   sync.RWMutex
	state                State
	stuckEscalationLevel int
	pendingMessage       string
	retryCount           int
	profileData          *ProfileData
	pausedUntil          time.Time
	ownProfileSkip       ownProfileSkipContext
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
	profileLLMCache       map[string]llm.Decision
	profileLLMCacheOrder  []string
	profileLLMCacheMax    int
	reciprocalLikeContext []RecentReciprocalLikeContext
	reciprocalLikeMax     int
	visibleProfileCard    RecentVisibleProfileCard
}

const ownProfileSkipTTL = 45 * time.Second
const ownProfileSkipMaxMessageGap int32 = 3
const groupedCaptionTTL = 2 * time.Minute
const startupOwnProfileSkipTTL = 90 * time.Second
const defaultProfileLLMCacheMaxEntries = 1000
const reciprocalLikeContextTTL = 30 * time.Minute
const defaultReciprocalLikeContextMaxEntries = 64
const visibleProfileCardTTL = 30 * time.Minute

type RecentReciprocalLikeContext struct {
	ProfileText string
	OpenerText  string
	MBTI        string
	Fingerprint string
	CapturedAt  time.Time
}

type RecentVisibleProfileCard struct {
	ProfileText string
	MessageID   int32
	CapturedAt  time.Time
	MediaSource RecentVisibleProfileMediaSource
}

type RecentVisibleProfileMediaSource struct {
	Message       *telegram.NewMessage
	AlbumMessages []*telegram.NewMessage
}

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
		groupedCaptions:    make(map[int64]groupedCaptionContext),
		profileLLMCache:    make(map[string]llm.Decision),
		profileLLMCacheMax: defaultProfileLLMCacheMaxEntries,
		reciprocalLikeMax:  defaultReciprocalLikeContextMaxEntries,
	}
}

func (sm *StateMachine) AddRecentReciprocalLikeContext(entry RecentReciprocalLikeContext) {
	if entry.CapturedAt.IsZero() {
		entry.CapturedAt = time.Now()
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.pruneReciprocalLikeContextLocked(entry.CapturedAt)
	sm.reciprocalLikeContext = append(sm.reciprocalLikeContext, entry)

	if len(sm.reciprocalLikeContext) <= sm.reciprocalLikeMax {
		return
	}

	overflow := len(sm.reciprocalLikeContext) - sm.reciprocalLikeMax
	sm.reciprocalLikeContext = append([]RecentReciprocalLikeContext(nil), sm.reciprocalLikeContext[overflow:]...)
}

func (sm *StateMachine) GetLatestReciprocalLikeContext(now time.Time) (RecentReciprocalLikeContext, bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.pruneReciprocalLikeContextLocked(now)
	if len(sm.reciprocalLikeContext) == 0 {
		return RecentReciprocalLikeContext{}, false
	}

	latest := sm.reciprocalLikeContext[len(sm.reciprocalLikeContext)-1]
	return latest, true
}

func (sm *StateMachine) ListRecentReciprocalLikeContexts(now time.Time, limit int) []RecentReciprocalLikeContext {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.pruneReciprocalLikeContextLocked(now)
	if len(sm.reciprocalLikeContext) == 0 || limit == 0 {
		return nil
	}

	size := len(sm.reciprocalLikeContext)
	if limit > 0 && limit < size {
		size = limit
	}

	start := len(sm.reciprocalLikeContext) - size
	out := make([]RecentReciprocalLikeContext, size)
	copy(out, sm.reciprocalLikeContext[start:])
	return out
}

func (sm *StateMachine) ClearRecentReciprocalLikeContexts() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.reciprocalLikeContext = nil
}

func (sm *StateMachine) pruneReciprocalLikeContextLocked(now time.Time) {
	if now.IsZero() {
		now = time.Now()
	}

	pruned := sm.reciprocalLikeContext[:0]
	for _, entry := range sm.reciprocalLikeContext {
		if now.Sub(entry.CapturedAt) > reciprocalLikeContextTTL {
			continue
		}
		pruned = append(pruned, entry)
	}
	sm.reciprocalLikeContext = pruned
}

func (sm *StateMachine) GetProfileLLMCache(key string) (llm.Decision, bool) {
	if key == "" {
		return llm.Decision{}, false
	}

	sm.mu.RLock()
	defer sm.mu.RUnlock()

	entry, ok := sm.profileLLMCache[key]
	if !ok {
		return llm.Decision{}, false
	}

	return entry, true
}

func (sm *StateMachine) SetProfileLLMCache(key string, decision llm.Decision) {
	if key == "" || decision.Validate() != nil {
		return
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	if _, exists := sm.profileLLMCache[key]; !exists {
		sm.profileLLMCacheOrder = append(sm.profileLLMCacheOrder, key)
	}

	sm.profileLLMCache[key] = decision

	if len(sm.profileLLMCache) <= sm.profileLLMCacheMax {
		return
	}

	oldestKey := sm.profileLLMCacheOrder[0]
	sm.profileLLMCacheOrder = sm.profileLLMCacheOrder[1:]
	delete(sm.profileLLMCache, oldestKey)
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
		sm.stuckEscalationLevel = 0
		sm.visibleProfileCard = RecentVisibleProfileCard{}
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
		sm.stuckEscalationLevel = 0
		sm.visibleProfileCard = RecentVisibleProfileCard{}
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
	if sm.state == StateStopped {
		return
	}
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
	sm.profileData = nil
	sm.pendingMessage = ""
	sm.retryCount = 0
	sm.ownProfileSkip = ownProfileSkipContext{}
	sm.groupedCaptions = make(map[int64]groupedCaptionContext)
	sm.startupOwnProfileSkip = startupOwnProfileSkipContext{}
	sm.latestProfileJobID = 0
	sm.lastProcessedJobID = 0
	sm.stuckEscalationLevel = 0
	sm.visibleProfileCard = RecentVisibleProfileCard{}
}

func (sm *StateMachine) RememberVisibleProfileCard(profileText string, messageID int32, now time.Time) {
	sm.rememberVisibleProfileCard(profileText, messageID, RecentVisibleProfileMediaSource{}, now)
}

func (sm *StateMachine) RememberVisibleProfileMessage(profileText string, messageID int32, message *telegram.NewMessage, now time.Time) {
	sm.rememberVisibleProfileCard(profileText, messageID, RecentVisibleProfileMediaSource{Message: message}, now)
}

func (sm *StateMachine) RememberVisibleProfileAlbum(profileText string, messageID int32, messages []*telegram.NewMessage, now time.Time) {
	albumMessages := append([]*telegram.NewMessage(nil), messages...)
	sm.rememberVisibleProfileCard(profileText, messageID, RecentVisibleProfileMediaSource{AlbumMessages: albumMessages}, now)
}

func (sm *StateMachine) rememberVisibleProfileCard(profileText string, messageID int32, source RecentVisibleProfileMediaSource, now time.Time) {
	trimmedProfileText := strings.TrimSpace(profileText)
	if trimmedProfileText == "" {
		return
	}

	if now.IsZero() {
		now = time.Now()
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.pruneVisibleProfileCardLocked(now)
	sm.visibleProfileCard = RecentVisibleProfileCard{
		ProfileText: trimmedProfileText,
		MessageID:   messageID,
		CapturedAt:  now,
		MediaSource: source,
	}
}

func (sm *StateMachine) GetLatestVisibleProfileCardBefore(messageID int32, now time.Time) (RecentVisibleProfileCard, bool) {
	if now.IsZero() {
		now = time.Now()
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.pruneVisibleProfileCardLocked(now)

	if strings.TrimSpace(sm.visibleProfileCard.ProfileText) == "" {
		return RecentVisibleProfileCard{}, false
	}

	if messageID > 0 && sm.visibleProfileCard.MessageID > 0 && sm.visibleProfileCard.MessageID >= messageID {
		return RecentVisibleProfileCard{}, false
	}

	entry := sm.visibleProfileCard
	entry.MediaSource.AlbumMessages = append([]*telegram.NewMessage(nil), entry.MediaSource.AlbumMessages...)
	return entry, true
}

func (sm *StateMachine) pruneVisibleProfileCardLocked(now time.Time) {
	if sm.visibleProfileCard.CapturedAt.IsZero() {
		return
	}

	if now.Sub(sm.visibleProfileCard.CapturedAt) > visibleProfileCardTTL {
		sm.visibleProfileCard = RecentVisibleProfileCard{}
	}
}

func (sm *StateMachine) NextStuckRecoveryEscalation() int {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	switch sm.stuckEscalationLevel {
	case 0:
		sm.stuckEscalationLevel = 1
		return 1
	case 1:
		sm.stuckEscalationLevel = 2
		return 2
	default:
		sm.stuckEscalationLevel = 0
		return 3
	}
}

func (sm *StateMachine) ResetStuckRecoveryEscalation() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.stuckEscalationLevel = 0
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
	if sm.state == StateStopped {
		return
	}
	if data == nil {
		sm.profileData = nil
		return
	}
	copy := *data
	sm.profileData = &copy
}

func (sm *StateMachine) GetProfileData() *ProfileData {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if sm.profileData == nil {
		return nil
	}
	copy := *sm.profileData
	return &copy
}

func (sm *StateMachine) ClearProfileData() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.profileData = nil
}

func (sm *StateMachine) FinalizeSendState(expectedCurrent State) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.captureRecentReciprocalLikeContextLocked(time.Now())
	if sm.state == StateIdle || sm.state == StateStopped {
		sm.pendingMessage = ""
		sm.profileData = nil
		sm.retryCount = 0
		return false
	}

	// API delivery is not bot acceptance. Retain immutable retry context until
	// the next profile or explicit reset confirms completion.

	if sm.state == expectedCurrent {
		sm.state = StateViewingProfiles
		return true
	}

	return false
}

func (sm *StateMachine) captureRecentReciprocalLikeContextLocked(now time.Time) {
	if sm.profileData == nil {
		return
	}

	if sm.profileData.ProfileText == "" && sm.pendingMessage == "" {
		return
	}

	entry := RecentReciprocalLikeContext{
		ProfileText: sm.profileData.ProfileText,
		OpenerText:  sm.pendingMessage,
		CapturedAt:  now,
	}

	fingerprintSource := sm.profileData.ProfileText + "\n" + sm.pendingMessage
	entry.Fingerprint = buildProfileLLMCacheKey(fingerprintSource, sm.profileData.PhotoIdentifiers)

	sm.pruneReciprocalLikeContextLocked(now)
	sm.reciprocalLikeContext = append(sm.reciprocalLikeContext, entry)

	if len(sm.reciprocalLikeContext) <= sm.reciprocalLikeMax {
		return
	}

	overflow := len(sm.reciprocalLikeContext) - sm.reciprocalLikeMax
	sm.reciprocalLikeContext = append([]RecentReciprocalLikeContext(nil), sm.reciprocalLikeContext[overflow:]...)
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

// HasPendingFresherProfileJob reports whether a newer profile job is queued but not yet processed.
func (sm *StateMachine) HasPendingFresherProfileJob() (hasPending bool, latest int32, lastProcessed int32) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	latest = sm.latestProfileJobID
	lastProcessed = sm.lastProcessedJobID
	return latest > lastProcessed, latest, lastProcessed
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
