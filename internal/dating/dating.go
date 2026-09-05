package dating

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/0FL01/tg-dating-agent/internal/llm"
	"github.com/0FL01/tg-dating-agent/internal/standalone"
	"github.com/0FL01/tg-dating-agent/internal/tghelper"
	"github.com/0FL01/tg-dating-agent/internal/utils"
	"github.com/amarnathcjd/gogram/telegram"
)

const DailyLimitPauseDuration = 2 * time.Hour
const stopCommandTimeout = 10 * time.Second

// jitterRand is a thread-safe local random generator for jitter calculations
var jitterRand = rand.New(rand.NewSource(time.Now().UnixNano()))
var jitterRandMu sync.Mutex

// Handler handles the dating bot automation
type Handler struct {
	config                       *standalone.Config
	client                       llm.MultimodalDecider
	replyAudit                   replyAuditAppender
	profileDedupe                profileDedupeChecker
	tgClient                     *telegram.Client
	state                        *StateMachine
	chatID                       int64
	botUsername                  string
	model                        string
	prompt                       string
	actionDelay                  time.Duration
	jitterDelay                  time.Duration
	temperature                  float64
	clickButtonFn                func(context.Context, string) error
	sendMessageFn                func(context.Context, telegram.InputPeer, string) error
	sendSleepFn                  func(context.Context) error
	deliverReciprocalLikeFinalFn func(context.Context, ReciprocalLikeFinalPayload, []ReciprocalLikePhoto) error
	botPeerMu                    sync.RWMutex
	botPeer                      telegram.InputPeer
	lifecycleMu                  sync.Mutex
	lifecycleCtx                 context.Context
	lifecycleCancel              context.CancelFunc
	stopSleepOnce                sync.Once
	decisionMu                   sync.Mutex
	pauseWakeMu                  sync.Mutex
	pauseWakeTimer               *time.Timer
	pauseWakeDeadline            time.Time
	dailyLimitPauseFn            func() time.Duration
}

type replyAuditAppender interface {
	Append(replyAuditRecord) error
}

type profileDedupeChecker interface {
	IsActive(context.Context, string) (bool, error)
	MarkProcessed(context.Context, string) error
}

// NewHandler creates a new dating handler
func NewHandler(cfg *standalone.Config, client llm.MultimodalDecider, tgClient *telegram.Client) *Handler {
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	var replyAudit replyAuditAppender
	if cfg != nil {
		replyAudit = newReplyAuditAppender(cfg.DatingReplyAuditLogPath)
	}

	return &Handler{
		config:          cfg,
		client:          client,
		replyAudit:      replyAudit,
		tgClient:        tgClient,
		state:           NewStateMachine(),
		chatID:          cfg.DatingBotChatID,
		botUsername:     cfg.DatingBotUsername,
		model:           cfg.DatingModel,
		prompt:          cfg.DatingPrompt,
		actionDelay:     cfg.DatingActionDelay,
		jitterDelay:     cfg.DatingJitterDelay,
		temperature:     cfg.DatingTemperature,
		lifecycleCtx:    lifecycleCtx,
		lifecycleCancel: lifecycleCancel,
	}
}

func newReplyAuditAppender(path string) replyAuditAppender {
	if strings.TrimSpace(path) == "" {
		return nil
	}

	logger := NewReplyAuditLogger(path)
	if err := logger.ensureDir(); err != nil {
		log.Printf("[dating] Reply audit disabled: %v", err)
		return nil
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("[dating] Reply audit disabled: open log path %q: %v", path, err)
		return nil
	}
	if err := f.Close(); err != nil {
		log.Printf("[dating] Reply audit disabled: close log path %q: %v", path, err)
		return nil
	}

	return logger
}

// Name returns the handler name for logging
func (h *Handler) Name() string {
	return "dating"
}

// getProcessingDelay returns delay with jitter for rate limiting
// Minimum actionDelay + random 0 to jitterDelay (default: 15s + 0-5s = 15-20s)
func (h *Handler) getProcessingDelay() time.Duration {
	return h.actionDelay + randomJitterDuration(h.jitterDelay)
}

func (h *Handler) getDailyLimitPauseDuration() time.Duration {
	if h.dailyLimitPauseFn != nil {
		return h.dailyLimitPauseFn()
	}

	return DailyLimitPauseDuration + randomJitterDuration(h.jitterDelay)
}

func randomJitterDuration(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}

	jitterRandMu.Lock()
	defer jitterRandMu.Unlock()
	return time.Duration(jitterRand.Int63n(int64(max)))
}

// Filter returns a filter function for incoming messages from dating bot
func (h *Handler) Filter() func(*telegram.NewMessage) bool {
	return func(m *telegram.NewMessage) bool {
		h.cacheBotPeer(m)
		log.Printf("[dating] Filter check: ChatID=%d, SenderID=%d, expected=%d",
			m.ChatID(), m.SenderID(), h.chatID)
		return m.ChatID() == h.chatID
	}
}

// Handle processes incoming messages from the dating bot
func (h *Handler) Handle(m *telegram.NewMessage) error {
	h.cacheBotPeer(m)

	if h.state.IsStopped() {
		return nil
	}

	text := m.Text()
	if isStartChattingMessage(text) {
		return h.handleReciprocalLikeFinalMessage(m)
	}

	if h.isPaused() {
		return nil
	}

	h.rememberGroupedCaption(m, text)
	log.Printf("[%s] Received message: %s...", h.Name(), utils.Truncate(text, 50))

	if isReciprocalLikePrompt(text) {
		return h.handleReciprocalLikePrompt(m)
	}

	if isDailyLimitMessage(text) {
		pauseDuration := h.getDailyLimitPauseDuration()
		pausedUntil := h.state.PauseFor(pauseDuration)
		h.schedulePauseWakeup(pausedUntil)
		h.state.ResetStuckRecoveryEscalation()
		h.state.ClearPendingMessage()
		h.state.ClearProfileData()
		h.state.ResetRetry()
		h.state.SetState(StateIdle)
		log.Printf("[%s] Daily limit message received, pausing for %v until %s", h.Name(), pauseDuration, pausedUntil.Format(time.RFC3339))
		return nil
	}

	if strings.Contains(strings.ToLower(text), PatternTooLong) {
		log.Printf("[%s] Message was too long, retrying...", h.Name())
		return h.retryGenerateMessage(h.lifecycleContext(), text)
	}

	if strings.Contains(strings.ToLower(text), PatternTooShort) {
		log.Printf("[%s] Message was too short, retrying with rejection feedback...", h.Name())
		return h.retryGenerateMessage(h.lifecycleContext(), text)
	}

	if strings.Contains(text, PatternWriteMessage) || text == PatternSendMessage {
		return h.sendPendingMessage(m)
	}

	if strings.Contains(text, PatternViewProfiles) {
		h.state.ClearProfileData()
		h.state.ClearPendingMessage()
		h.state.ResetRetry()
		h.state.ClearStartupOwnProfileSkip()
		log.Printf("[%s] Detected main menu, enqueuing profile viewing", h.Name())
		if !h.state.Enqueue(ProfileJob{Type: "menu_recovery", Message: m}) {
			log.Printf("[%s] Queue full, skipping main menu recovery", h.Name())
		}
		return nil
	}

	// Detect "Your profile" message - next photo will be own profile, skip it
	lowerText := strings.ToLower(text)
	if strings.Contains(lowerText, PatternYourProfile) || strings.Contains(lowerText, PatternYourProfileRU) {
		log.Printf("[%s] Detected 'Your profile' marker (msg_id=%d), arming skip context", h.Name(), m.ID)
		h.state.MarkOwnProfileSkip(m.ID, time.Now())
		return nil
	}

	if m.Photo() != nil || m.IsMedia() {
		if m.Message != nil && m.Message.GroupedID != 0 {
			return nil
		}

		if h.state.ConsumeStartupOwnProfileSkip(time.Now()) {
			log.Printf("[%s] Skipping startup own profile photo (markerless fallback)", h.Name())
			return nil
		}

		// Check if this is our own profile that should be skipped
		if h.shouldSkipOwnProfileByMessageID(m.ID) {
			log.Printf("[%s] Skipping own profile photo", h.Name())
			// Continue to actual profiles by clicking "View profiles"
			if !h.state.Enqueue(ProfileJob{Type: "menu_recovery", Message: m}) {
				log.Printf("[%s] Queue full, skipping view profiles after own profile", h.Name())
			}
			return nil
		}
		h.rememberVisibleProfileMessage(m.Text(), m.ID, m)
		if !h.state.Enqueue(ProfileJob{Type: "message", Message: m, ProfileMessageID: m.ID}) {
			log.Printf("[%s] Queue full, skipping profile", h.Name())
		} else {
			h.state.ResetStuckRecoveryEscalation()
		}
		return nil
	}

	if hasProfileActionKeyboard(m) && isNonEmptyTextOnlyMessage(text) {
		if isMineMessage(text) {
			log.Printf("[%s] Detected mine/interstitial message, enqueuing mine recovery", h.Name())
			if !h.state.Enqueue(ProfileJob{Type: "mine_recovery", Message: m}) {
				log.Printf("[%s] Queue full, skipping mine recovery", h.Name())
			}
			return nil
		}

		h.rememberVisibleProfileCard(text, m.ID)
		if !h.state.Enqueue(ProfileJob{Type: "message", Message: m, ProfileMessageID: m.ID}) {
			log.Printf("[%s] Queue full, skipping text-only profile", h.Name())
		} else {
			h.state.ResetStuckRecoveryEscalation()
		}
		return nil
	}

	if h.state.GetState() == StateViewingProfiles && shouldRememberVisibleProfileTextFallback(m, text) {
		h.rememberVisibleProfileTextFallback(text, m.ID)
	}

	if h.shouldRecoverFromStuck(m) {
		log.Printf("[%s] Recovering viewing flow from interstitial message", h.Name())
		if !h.state.Enqueue(ProfileJob{Type: "stuck_recovery", Message: m}) {
			log.Printf("[%s] Queue full, skipping recovery", h.Name())
		}
		return nil
	}

	return nil
}

func (h *Handler) shouldRecoverFromStuck(m *telegram.NewMessage) bool {
	if h.state.GetState() != StateViewingProfiles || m == nil {
		return false
	}

	if hasReplyKeyboardButtonText(m, ButtonViewProfiles) {
		return true
	}

	if hasProfileActionKeyboard(m) {
		return false
	}

	return isNonEmptyTextOnlyMessage(m.Text())
}

func isNonEmptyTextOnlyMessage(text string) bool {
	return strings.TrimSpace(text) != ""
}

func shouldRememberVisibleProfileTextFallback(m *telegram.NewMessage, text string) bool {
	if !isNonEmptyTextOnlyMessage(text) {
		return false
	}

	if hasReplyMarkup(m) {
		return false
	}

	if isMineMessage(text) || isReciprocalLikePrompt(text) || isDailyLimitMessage(text) {
		return false
	}

	if strings.Contains(text, PatternViewProfiles) || strings.Contains(text, PatternWriteMessage) {
		return false
	}

	return true
}

func isMineMessage(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return false
	}

	return strings.Contains(normalized, PatternMineKeywords)
}

func isReciprocalLikePrompt(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return false
	}

	if strings.Contains(normalized, PatternLikedYouPrompt) {
		return true
	}

	return strings.Contains(normalized, PatternLikedYouPerson) ||
		strings.Contains(normalized, PatternLikedYouWoman) ||
		strings.Contains(normalized, PatternLikedYouMan)
}

func (h *Handler) handleReciprocalLikePrompt(m *telegram.NewMessage) error {
	log.Printf("[%s] Received reciprocal-like prompt", h.Name())

	buttonText, ok := reciprocalOpenButtonText(m)
	if !ok {
		log.Printf("[%s] Reciprocal-like prompt has no obvious show button, skipping", h.Name())
		return nil
	}

	if err := h.clickButtonWithContext(h.lifecycleContext(), buttonText); err != nil {
		log.Printf("[%s] Failed to open reciprocal-like prompt with button %q: %v", h.Name(), buttonText, err)
	}

	return nil
}

func (h *Handler) handleReciprocalLikeFinalMessage(m *telegram.NewMessage) error {
	now := time.Now()
	latest, hasContext := h.state.GetLatestReciprocalLikeContext(now)
	visibleProfile, hasVisibleProfile := h.state.GetLatestVisibleProfileCardBefore(messageIDFromMessage(m), now)
	payload, ok := buildReciprocalLikeFinalPayload(m, visibleProfile, hasVisibleProfile, latest, hasContext, now)
	if !ok {
		log.Printf("[%s] Start chatting message detected, but payload assembly failed (no valid Telegram contact URL)", h.Name())
		return nil
	}

	photos := h.collectReciprocalLikePhotos(h.lifecycleContext(), visibleProfile, hasVisibleProfile)

	if err := h.deliverReciprocalLikeFinalPayload(h.lifecycleContext(), payload, photos); err != nil {
		log.Printf("[%s] Reciprocal-like final payload handling failed: %v", h.Name(), err)
		return nil
	}

	return nil
}

func (h *Handler) deliverReciprocalLikeFinalPayload(ctx context.Context, payload ReciprocalLikeFinalPayload, photos []ReciprocalLikePhoto) error {
	if h.deliverReciprocalLikeFinalFn != nil {
		return h.deliverReciprocalLikeFinalFn(ctx, payload, photos)
	}

	log.Printf("[%s] Reciprocal-like final payload assembled: username=%q url=%q context=%t photos=%d",
		h.Name(), payload.ContactUsername, payload.RawContactURL, !payload.ContextCapturedAt.IsZero(), len(photos))
	return nil
}

func isDailyLimitMessage(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return false
	}

	if strings.TrimSpace(text) == PatternDailyLimitExact {
		return true
	}

	if !strings.Contains(normalized, PatternTooManyLikes) {
		return false
	}

	hasLikeCue := strings.Contains(normalized, "like")
	hasHeartCue := strings.Contains(text, "❤️") || strings.Contains(text, "❤") || strings.Contains(normalized, "heart")
	hasTodayCue := strings.Contains(normalized, "today")
	hasInviteCue := strings.Contains(normalized, "invite") || strings.Contains(normalized, "share") || strings.Contains(normalized, "friend") || strings.Contains(normalized, "personal link")

	if hasLikeCue || (hasHeartCue && (hasTodayCue || hasInviteCue)) {
		return true
	}

	return hasTodayCue && hasInviteCue
}

func (h *Handler) processProfile(ctx context.Context, m *telegram.NewMessage) error {
	if !h.state.SetStateIfNotStopped(StateViewingProfiles) {
		return nil
	}

	data, cleanup := h.downloadProfileData(ctx, m)
	defer cleanup()

	return h.generateAndSendLike(ctx, data)
}

func (h *Handler) downloadProfileData(ctx context.Context, m *telegram.NewMessage) (ProfileData, func()) {
	var data ProfileData
	var photoPaths []string

	photoPath, err := tghelper.RetryTelegram(ctx, "download_photo", func() (string, error) {
		return m.Download(&telegram.DownloadOptions{FileName: writableTempDownloadDir()})
	})
	if err != nil {
		log.Printf("[%s] Failed to download photo: %v", h.Name(), err)
	} else if photoPath != "" {
		photoPaths = append(photoPaths, photoPath)
	}

	data.PhotoPaths = photoPaths
	data.PhotoIdentifiers = photoIdentifiersFromMessage(m)
	data.ProfileText = m.Text()

	log.Printf("[%s] Profile text: %s", h.Name(), utils.Truncate(data.ProfileText, 100))

	cleanup := func() {
		for _, path := range photoPaths {
			tghelper.CleanupFile(path, nil, h.Name())
		}
	}

	return data, cleanup
}

func (h *Handler) generateAndSendLike(ctx context.Context, data ProfileData) error {
	h.decisionMu.Lock()
	defer h.decisionMu.Unlock()
	h.state.ClearProfileData()
	h.state.ClearPendingMessage()
	h.state.ResetRetry()
	if h.shouldStopProcessing(ctx) {
		return nil
	}
	cacheKey := buildProfileLLMCacheKey(data.ProfileText, data.PhotoIdentifiers)
	if h.isDuplicateProfileActive(ctx, cacheKey) {
		log.Printf("[%s] Profile dedupe hit (key=%s), skipping before LLM", h.Name(), utils.Truncate(cacheKey, 12))
		return h.clickButtonAndMarkProcessed(ctx, ButtonDislike, cacheKey)
	}

	content, err := llm.PrepareImages(ctx, llm.MultimodalContent{Text: data.ProfileText, ImagePaths: data.PhotoPaths})
	if err != nil {
		if h.shouldStopProcessing(ctx) {
			return nil
		}
		log.Printf("[%s] Cannot prepare profile photos: %v", h.Name(), err)
		return h.clickButtonAndMarkProcessed(ctx, ButtonDislike, cacheKey)
	}
	data.Content = content
	data.PhotoPaths = nil
	data.Prompt = h.prompt
	decision, ok := h.state.GetProfileLLMCache(cacheKey)
	if !ok {
		decision, err = h.generateDecision(ctx, data, "")
		if err == nil {
			h.state.SetProfileLLMCache(cacheKey, decision)
		}
	} else {
		h.appendReplyAudit("decision", decision, data, "")
	}
	if h.shouldStopProcessing(ctx) {
		return nil
	}
	if err != nil || decision.Validate() != nil || decision.Action == "skip" {
		if err != nil {
			log.Printf("[%s] Decision failed, skipping profile: %v", h.Name(), err)
		}
		return h.clickButtonAndMarkProcessed(ctx, ButtonDislike, cacheKey)
	}
	data.Decision = decision
	h.state.SetProfileData(&data)
	h.state.ResetRetry()
	h.state.SetPendingMessage(decision.Message)
	if !h.state.SetStateIfNotStopped(StateWaitingPrompt) {
		return nil
	}

	return h.clickButtonAndMarkProcessed(ctx, ButtonLikeMessage, cacheKey)
}

func (h *Handler) generateDecision(ctx context.Context, data ProfileData, feedback string) (llm.Decision, error) {
	var decision llm.Decision
	var err error
	if h.client == nil {
		h.appendReplyAudit("error", llm.Decision{}, data, "Decision client is not configured")
		return decision, errors.New("decision client is not configured")
	}
	for attempt := 0; attempt <= MaxRetries; attempt++ {
		if h.shouldStopProcessing(ctx) {
			return decision, context.Canceled
		}
		content := data.Content
		content.Text = "Profile:\n" + data.ProfileText
		if feedback != "" {
			content.Text += "\n\nCorrection feedback:\n" + feedback
		}
		raw, callErr := h.client.DecideMultimodal(ctx, h.model, data.Prompt, content, h.temperature)
		if callErr != nil {
			// Provider errors may contain response bodies; do not persist them.
			h.appendReplyAudit("error", llm.Decision{}, data, "LLM request failed")
			return decision, callErr
		}
		decision, err = llm.ParseDecision(raw)
		if err == nil {
			h.appendReplyAudit("decision", decision, data, "")
			return decision, nil
		}
		h.appendReplyAudit("invalid_response", llm.Decision{}, data, "Response failed decision validation")
		feedback += fmt.Sprintf("\nPrevious output: %q\nValidation error: %s\nCorrect the JSON without changing the original selection criteria.", raw, err)
	}
	return llm.Decision{}, err
}

func (h *Handler) sendPendingMessage(m *telegram.NewMessage) error {
	h.decisionMu.Lock()
	defer h.decisionMu.Unlock()
	if h.state.GetState() != StateWaitingPrompt {
		return nil
	}
	msg := h.state.GetPendingMessage()
	if err := (llm.Decision{Action: "send", Message: msg}).Validate(); err != nil {
		return h.stopMessageEntry(h.lifecycleContext())
	}

	ctx := h.lifecycleContext()
	if h.shouldStopProcessing(ctx) {
		return nil
	}

	log.Printf("[%s] Sending message: %s (delay: %v)", h.Name(), utils.Truncate(msg, 50), h.actionDelay)

	if !waitForDelayWithContext(h.actionDelay, ctx) {
		return nil
	}

	if h.shouldStopProcessing(ctx) {
		return nil
	}

	peer, ok := h.getBotPeer()
	if !ok {
		err := fmt.Errorf("dating peer is not cached yet for chat %d", h.chatID)
		log.Printf("[%s] %v", h.Name(), err)
		return err
	}
	err := h.sendOpener(ctx, peer, msg)
	if err == nil {
		h.finalizeSendState()
	}

	if h.shouldStopProcessing(ctx) {
		return nil
	}

	if err != nil {
		log.Printf("[%s] Failed to send message: %v", h.Name(), err)
		return err
	}

	log.Printf("[%s] Message sent successfully", h.Name())
	return nil
}

func (h *Handler) cacheBotPeer(m *telegram.NewMessage) {
	if m == nil || m.ChatID() != h.chatID || m.Peer == nil {
		return
	}

	h.botPeerMu.Lock()
	h.botPeer = m.Peer
	h.botPeerMu.Unlock()
}

func (h *Handler) getBotPeer() (telegram.InputPeer, bool) {
	h.botPeerMu.RLock()
	peer := h.botPeer
	h.botPeerMu.RUnlock()
	return peer, peer != nil
}

func (h *Handler) setBotPeer(peer telegram.InputPeer) {
	h.botPeerMu.Lock()
	h.botPeer = peer
	h.botPeerMu.Unlock()
}

// resolveBotByUsername resolves bot username to InputPeer via Telegram API
func (h *Handler) resolveBotByUsername(ctx context.Context) (telegram.InputPeer, error) {
	if h.botUsername == "" {
		return nil, fmt.Errorf("bot username is empty")
	}

	result, err := tghelper.RetryTelegram(ctx, "resolve_username", func() (*telegram.ContactsResolvedPeer, error) {
		return h.tgClient.ContactsResolveUsername(h.botUsername, "")
	})
	if err != nil {
		return nil, fmt.Errorf("resolve username %q: %w", h.botUsername, err)
	}

	if result.Peer == nil {
		return nil, fmt.Errorf("resolve username %q: peer is nil in response", h.botUsername)
	}

	// Convert Peer to InputPeer
	switch p := result.Peer.(type) {
	case *telegram.PeerUser:
		if len(result.Users) == 0 {
			return nil, fmt.Errorf("resolve username %q: no users in response", h.botUsername)
		}
		user, ok := result.Users[0].(*telegram.UserObj)
		if !ok {
			return nil, fmt.Errorf("resolve username %q: unexpected user type %T", h.botUsername, result.Users[0])
		}
		return &telegram.InputPeerUser{
			UserID:     user.ID,
			AccessHash: user.AccessHash,
		}, nil
	case *telegram.PeerChannel:
		if len(result.Chats) == 0 {
			return nil, fmt.Errorf("resolve username %q: no chats in response", h.botUsername)
		}
		channel, ok := result.Chats[0].(*telegram.Channel)
		if !ok {
			return nil, fmt.Errorf("resolve username %q: unexpected chat type %T", h.botUsername, result.Chats[0])
		}
		return &telegram.InputPeerChannel{
			ChannelID:  channel.ID,
			AccessHash: channel.AccessHash,
		}, nil
	default:
		return nil, fmt.Errorf("resolve username %q: unexpected peer type %T", h.botUsername, p)
	}
}

func (h *Handler) finalizeSendState() {
	h.state.FinalizeSendState(StateWaitingPrompt)
}

func (h *Handler) sendDatingMessage(ctx context.Context, peer telegram.InputPeer, msg string) error {
	if h.sendMessageFn != nil {
		return h.sendMessageFn(ctx, peer, msg)
	}

	_, err := tghelper.RetryTelegram(ctx, "send_dating_message", func() (*telegram.NewMessage, error) {
		return h.tgClient.SendMessage(peer, msg)
	})

	return err
}

func (h *Handler) sendOpener(ctx context.Context, peer telegram.InputPeer, msg string) error {
	// Snapshot before sending: shutdown may clear state while the API is in flight.
	data := ProfileData{Prompt: h.prompt}
	if profile := h.state.GetProfileData(); profile != nil {
		data = *profile
	}
	decision := llm.Decision{Action: "send", Reason: data.Decision.Reason, Message: msg}
	if err := h.sendDatingMessage(ctx, peer, msg); err != nil {
		h.appendReplyAudit("error", decision, data, "Telegram send failed")
		return err
	}
	h.appendReplyAudit("sent", decision, data, "")
	return nil
}

func (h *Handler) retryGenerateMessage(ctx context.Context, rejection string) error {
	h.decisionMu.Lock()
	defer h.decisionMu.Unlock()
	if h.shouldStopProcessing(ctx) {
		return nil
	}

	retryCount := h.state.IncrementRetry()
	pendingMsg := h.state.GetPendingMessage()

	log.Printf("[%s] Retry attempt %d/%d after bot rejection", h.Name(), retryCount, MaxRetries)

	if retryCount > MaxRetries {
		log.Printf("[%s] Max retries reached, stopping locally", h.Name())
		return h.stopMessageEntry(ctx)
	}

	profileData := h.state.GetProfileData()
	if profileData == nil {
		log.Printf("[%s] No profile data for retry, stopping locally", h.Name())
		return h.stopMessageEntry(ctx)
	}

	previous, _ := json.Marshal(profileData.Decision)
	decision, err := h.generateDecision(ctx, *profileData, fmt.Sprintf("Previous output: %s\nPrevious message: %q\nTelegram rejection: %s", previous, pendingMsg, rejection))
	if err != nil {
		if h.shouldStopProcessing(ctx) {
			return nil
		}
		log.Printf("[%s] Failed to regenerate message: %v, stopping locally", h.Name(), err)
		return h.stopMessageEntry(ctx)
	}
	if h.shouldStopProcessing(ctx) {
		return nil
	}

	if decision.Action == "skip" {
		return h.stopMessageEntry(ctx)
	}
	profileData.Decision = decision
	h.state.SetProfileData(profileData)
	h.state.SetProfileLLMCache(buildProfileLLMCacheKey(profileData.ProfileText, profileData.PhotoIdentifiers), decision)
	h.state.SetPendingMessage(decision.Message)
	return h.sendValidatedMessage(ctx, decision.Message)
}

func (h *Handler) stopMessageEntry(ctx context.Context) error {
	if h.shouldStopProcessing(ctx) {
		return nil
	}

	// No proven cancel button exists in message entry. Stop locally, never send
	// sleep/dislike commands that the bot could interpret as an opener.
	h.state.BeginShutdown()
	h.clearPauseWakeup()
	h.cancelLifecycleContext()
	h.state.CancelWorkerContext()
	h.state.StopWorker()
	return nil
}

func (h *Handler) sendValidatedMessage(ctx context.Context, msg string) error {
	if err := (llm.Decision{Action: "send", Message: msg}).Validate(); err != nil {
		return h.stopMessageEntry(ctx)
	}
	if h.shouldStopProcessing(ctx) {
		return nil
	}

	log.Printf("[%s] Sending message directly: %s (delay: %v)", h.Name(), utils.Truncate(msg, 50), h.actionDelay)

	if !waitForDelayWithContext(h.actionDelay, ctx) {
		return nil
	}

	if h.shouldStopProcessing(ctx) {
		return nil
	}

	peer, ok := h.getBotPeer()
	if !ok {
		err := fmt.Errorf("dating peer is not cached yet for chat %d", h.chatID)
		log.Printf("[%s] %v", h.Name(), err)
		return err
	}
	err := h.sendOpener(ctx, peer, msg)
	if err == nil {
		h.finalizeSendState()
	}

	if h.shouldStopProcessing(ctx) {
		return nil
	}

	if err != nil {
		log.Printf("[%s] Failed to send message: %v", h.Name(), err)
		return err
	}

	return nil
}

func (h *Handler) appendReplyAudit(event string, decision llm.Decision, data ProfileData, detail string) {
	if h.replyAudit == nil {
		return
	}

	if err := h.replyAudit.Append(replyAuditRecord{Event: event, Decision: decision, Model: h.model, ProfileText: data.ProfileText, Prompt: data.Prompt, Error: detail}); err != nil {
		log.Printf("[%s] Reply audit append failed: %v", h.Name(), err)
	}
}

func (h *Handler) clickButton(buttonText string) error {
	return h.clickButtonWithContext(h.lifecycleContext(), buttonText)
}

func (h *Handler) clickButtonWithContext(ctx context.Context, buttonText string) error {
	log.Printf("[%s] Clicking button: %s", h.Name(), buttonText)
	if h.clickButtonFn != nil {
		return h.clickButtonFn(ctx, buttonText)
	}

	// Note: Rate limiting is now handled in worker loop via getProcessingDelay()
	// This method no longer includes time.Sleep to avoid double delays

	peer, err := h.ensureBotPeer(ctx)
	if err != nil {
		log.Printf("[%s] %v", h.Name(), err)
		return err
	}
	_, err = tghelper.RetryTelegram(ctx, "click_button", func() (*telegram.NewMessage, error) {
		return h.tgClient.SendMessage(peer, buttonText)
	})

	if err != nil {
		log.Printf("[%s] Failed to click button: %v", h.Name(), err)
	}
	return err
}

func (h *Handler) isDuplicateProfileActive(ctx context.Context, profileHash string) bool {
	if h.profileDedupe == nil || strings.TrimSpace(profileHash) == "" {
		return false
	}

	active, err := h.profileDedupe.IsActive(ctx, profileHash)
	if err != nil {
		log.Printf("[%s] Profile dedupe check failed for key=%s: %v", h.Name(), utils.Truncate(profileHash, 12), err)
		return false
	}

	return active
}

func (h *Handler) markProfileProcessedBestEffort(ctx context.Context, profileHash string) {
	if h.profileDedupe == nil || strings.TrimSpace(profileHash) == "" {
		return
	}

	if err := h.profileDedupe.MarkProcessed(ctx, profileHash); err != nil {
		log.Printf("[%s] Profile dedupe mark failed for key=%s: %v", h.Name(), utils.Truncate(profileHash, 12), err)
	}
}

func (h *Handler) clickButtonAndMarkProcessed(ctx context.Context, buttonText, profileHash string) error {
	err := h.clickButtonWithContext(ctx, buttonText)
	if err != nil {
		return err
	}

	h.markProfileProcessedBestEffort(ctx, profileHash)
	return nil
}

func (h *Handler) Stop() {
	log.Printf("[%s] Stopping...", h.Name())
	h.Shutdown()
	h.stopSleepOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), stopCommandTimeout)
		defer cancel()

		if h.sendSleepFn != nil {
			_ = h.sendSleepFn(ctx)
			return
		}

		_ = h.clickButtonWithContext(ctx, ButtonSleep)
	})
}

func (h *Handler) IsStopped() bool {
	return h.state.IsStopped()
}

func (h *Handler) Start() {
	log.Printf("[%s] Starting...", h.Name())
	h.state.SetState(StateIdle)
}

func (h *Handler) Bootstrap() error {
	return h.bootstrapWithActions(func() error {
		return h.sendBootstrapCommand("/start")
	}, nil)
}

func (h *Handler) bootstrapWithActions(sendStart func() error, startSearch func() error) error {
	if h.isPaused() {
		log.Printf("[%s] Startup bootstrap skipped: pause is active", h.Name())
		return nil
	}

	if startSearch != nil {
		log.Printf("[%s] Startup bootstrap: sending /start then %s", h.Name(), ButtonViewProfiles)
	} else {
		log.Printf("[%s] Startup bootstrap: sending /start and waiting for menu recovery", h.Name())
	}

	var startErr error
	if sendStart != nil {
		startErr = sendStart()
		if startErr != nil {
			log.Printf("[%s] Startup bootstrap: /start failed: %v", h.Name(), startErr)
		}
	}

	var searchErr error
	if startSearch != nil {
		searchErr = startSearch()
		if searchErr != nil {
			log.Printf("[%s] Startup bootstrap: %s failed: %v", h.Name(), ButtonViewProfiles, searchErr)
		}
	}

	if startSearch == nil {
		if startErr != nil {
			return fmt.Errorf("startup bootstrap errors: send /start: %v", startErr)
		}
		log.Printf("[%s] Startup bootstrap completed", h.Name())
		return nil
	}

	if startErr != nil || searchErr != nil {
		return fmt.Errorf("startup bootstrap errors: send /start: %v; start search: %v", startErr, searchErr)
	}

	log.Printf("[%s] Startup bootstrap completed", h.Name())
	return nil
}

func (h *Handler) sendBootstrapCommand(command string) error {
	log.Printf("[%s] Sending bootstrap command: %s (delay: %v)", h.Name(), command, h.actionDelay)
	ctx := h.lifecycleContext()
	if h.shouldStopProcessing(ctx) {
		return nil
	}

	if !waitForDelayWithContext(h.actionDelay, ctx) {
		return nil
	}

	peer, err := h.ensureBotPeer(ctx)
	if err != nil {
		return err
	}

	_, err = tghelper.RetryTelegram(ctx, "bootstrap_send_start", func() (*telegram.NewMessage, error) {
		return h.tgClient.SendMessage(peer, command)
	})
	if err != nil {
		return fmt.Errorf("failed to send bootstrap command %q: %w", command, err)
	}
	if command == "/start" {
		h.state.ArmStartupOwnProfileSkip(time.Now())
	}

	return nil
}

func (h *Handler) sendStartCommand(ctx context.Context) error {
	if h.shouldStopProcessing(ctx) {
		return nil
	}

	peer, err := h.ensureBotPeer(ctx)
	if err != nil {
		return err
	}

	err = h.sendDatingMessage(ctx, peer, "/start")
	if err != nil {
		return fmt.Errorf("failed to send /start for mine recovery: %w", err)
	}

	h.state.ArmStartupOwnProfileSkip(time.Now())
	return nil
}

func (h *Handler) HandleAlbum(a *telegram.Album) error {
	h.cacheBotPeerFromAlbum(a)

	if h.state.IsStopped() {
		return nil
	}

	if h.isPaused() {
		return nil
	}

	if h.state.ConsumeStartupOwnProfileSkip(time.Now()) {
		log.Printf("[%s] Skipping startup own profile album (markerless fallback)", h.Name())
		return nil
	}

	// Check if this is our own profile that should be skipped
	if h.shouldSkipOwnProfileByMessageID(firstAlbumMessageID(a)) {
		log.Printf("[%s] Skipping own profile album", h.Name())
		// Continue to actual profiles by clicking "View profiles"
		if !h.state.Enqueue(ProfileJob{Type: "menu_recovery", Message: nil}) {
			log.Printf("[%s] Queue full, skipping view profiles after own profile album", h.Name())
		}
		return nil
	}

	if profileText, messageID, ok := visibleProfileCardFromAlbum(a); ok {
		h.rememberVisibleProfileAlbum(profileText, messageID, a.Messages)
	}

	// Add to queue instead of direct processing
	if !h.state.Enqueue(ProfileJob{Type: "album", Album: a, ProfileMessageID: maxAlbumMessageID(a)}) {
		log.Printf("[%s] Queue full, skipping album", h.Name())
	} else {
		h.state.ResetStuckRecoveryEscalation()
	}
	return nil
}

// handleAlbumJob performs actual album processing (inside worker)
func (h *Handler) handleAlbumJob(ctx context.Context, a *telegram.Album) error {
	h.cacheBotPeerFromAlbum(a)

	profileText := h.resolveAlbumProfileText(a)
	if !h.state.SetStateIfNotStopped(StateViewingProfiles) {
		return nil
	}

	data, cleanup := h.downloadAlbumData(ctx, a, profileText)
	defer cleanup()

	log.Printf("[%s] Album: %d photos, text: %s", h.Name(), len(data.PhotoPaths), utils.Truncate(data.ProfileText, 100))

	return h.generateAndSendLike(ctx, data)
}

// processJob processes jobs from the queue sequentially
func (h *Handler) processJob(ctx context.Context, job ProfileJob) error {
	switch job.Type {
	case "message":
		if job.Message == nil {
			return nil
		}
		if accepted, latest, last := h.state.TryMarkProfileJobProcessing(job.ProfileMessageID); !accepted {
			log.Printf("[%s] Skipping stale/duplicate message job (id=%d latest=%d last=%d)",
				h.Name(), job.ProfileMessageID, latest, last)
			return nil
		}
		h.state.ResetStuckRecoveryEscalation()
		return h.processProfile(ctx, job.Message)
	case "album":
		if job.Album == nil {
			return nil
		}
		if accepted, latest, last := h.state.TryMarkProfileJobProcessing(job.ProfileMessageID); !accepted {
			log.Printf("[%s] Skipping stale/duplicate album job (id=%d latest=%d last=%d)",
				h.Name(), job.ProfileMessageID, latest, last)
			return nil
		}
		h.state.ResetStuckRecoveryEscalation()
		return h.handleAlbumJob(ctx, job.Album)
	case "menu_recovery":
		log.Printf("[%s] Processing menu recovery job", h.Name())
		return h.clickButtonWithContext(ctx, ButtonViewProfiles)
	case "stuck_recovery":
		log.Printf("[%s] Processing stuck recovery job", h.Name())
		if h.isPaused() {
			log.Printf("[%s] Skipping stuck recovery while pause is active", h.Name())
			return nil
		}

		if hasPending, latest, last := h.state.HasPendingFresherProfileJob(); hasPending {
			log.Printf("[%s] Skipping stuck recovery due to pending fresher profile job (latest=%d last=%d)", h.Name(), latest, last)
			h.state.ResetStuckRecoveryEscalation()
			return nil
		}

		escalation := h.state.NextStuckRecoveryEscalation()
		switch escalation {
		case 1:
			log.Printf("[%s] Stuck recovery escalation level 1: click %s", h.Name(), ButtonViewProfiles)
			return h.clickButtonWithContext(ctx, ButtonViewProfiles)
		case 2:
			log.Printf("[%s] Stuck recovery escalation level 2: repeat click %s", h.Name(), ButtonViewProfiles)
			return h.clickButtonWithContext(ctx, ButtonViewProfiles)
		default:
			log.Printf("[%s] Stuck recovery escalation level 3: fallback /start and reset flow", h.Name())
			if err := h.sendStartCommand(ctx); err != nil {
				return err
			}
			h.state.SetStateIfNotStopped(StateIdle)
			return nil
		}
	case "mine_recovery":
		log.Printf("[%s] Processing mine recovery job", h.Name())
		return h.sendStartCommand(ctx)
	default:
		log.Printf("[%s] Unknown job type: %s", h.Name(), job.Type)
		return nil
	}
}

// StartWorker starts a goroutine to process the profile queue
func (h *Handler) StartWorker() {
	if !h.state.MarkWorkerStarted() {
		return
	}

	go func() {
		log.Printf("[%s] Worker started", h.Name())
		defer func() {
			h.state.MarkWorkerStopped()
			log.Printf("[%s] Worker stopped", h.Name())
		}()

		quit := h.state.ShouldQuit()
		queue := h.state.GetQueue()
		workerCtx := h.state.WorkerContext()

		for {
			select {
			case <-quit:
				return
			case <-workerCtx.Done():
				return
			case job := <-queue:
				h.state.OnJobDequeued(job.Type)
				if shouldStopWorker(workerCtx, quit) || h.state.IsStopped() {
					return
				}

				log.Printf("[%s] Processing job type: %s", h.Name(), job.Type)
				if err := h.processJob(workerCtx, job); err != nil {
					log.Printf("[%s] Job processing error: %v", h.Name(), err)
				}

				delay := h.getProcessingDelay()
				log.Printf("[%s] Rate limiting: waiting %v before processing", h.Name(), delay)
				if !waitForDelayOrStop(delay, workerCtx, quit) {
					return
				}
			}
		}
	}()
}

func waitForDelayOrStop(delay time.Duration, ctx context.Context, quit <-chan struct{}) bool {
	if delay <= 0 {
		return !shouldStopWorker(ctx, quit)
	}

	timer := time.NewTimer(delay)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()

	select {
	case <-ctx.Done():
		return false
	case <-quit:
		return false
	case <-timer.C:
		return true
	}
}

func (h *Handler) rememberGroupedCaption(m *telegram.NewMessage, text string) {
	if m == nil || m.Message == nil || m.Message.GroupedID == 0 {
		return
	}

	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return
	}

	h.state.RememberGroupedCaption(m.Message.GroupedID, trimmed, m.ID, time.Now())
}

func (h *Handler) rememberVisibleProfileCard(profileText string, messageID int32) {
	h.state.RememberVisibleProfileCard(profileText, messageID, time.Now())
}

func (h *Handler) rememberVisibleProfileTextFallback(profileText string, messageID int32) {
	now := time.Now()
	if previous, ok := h.state.GetLatestVisibleProfileCardBefore(messageID, now); ok {
		if len(previous.MediaSource.AlbumMessages) > 0 {
			h.state.RememberVisibleProfileAlbum(profileText, messageID, previous.MediaSource.AlbumMessages, now)
			return
		}
		if previous.MediaSource.Message != nil {
			h.state.RememberVisibleProfileMessage(profileText, messageID, previous.MediaSource.Message, now)
			return
		}
	}

	h.state.RememberVisibleProfileCard(profileText, messageID, now)
}

func (h *Handler) rememberVisibleProfileMessage(profileText string, messageID int32, m *telegram.NewMessage) {
	h.state.RememberVisibleProfileMessage(profileText, messageID, m, time.Now())
}

func (h *Handler) rememberVisibleProfileAlbum(profileText string, messageID int32, messages []*telegram.NewMessage) {
	h.state.RememberVisibleProfileAlbum(profileText, messageID, messages, time.Now())
}

func (h *Handler) collectReciprocalLikePhotos(ctx context.Context, visibleProfile RecentVisibleProfileCard, hasVisibleProfile bool) []ReciprocalLikePhoto {
	if !hasVisibleProfile {
		return nil
	}

	sourceMessages := reciprocalLikePhotoSourceMessages(visibleProfile.MediaSource)
	if len(sourceMessages) == 0 {
		return nil
	}

	photos := make([]ReciprocalLikePhoto, 0, minInt(len(sourceMessages), maxReciprocalLikePhotos))
	for sourceIndex, msg := range sourceMessages {
		if len(photos) >= maxReciprocalLikePhotos {
			break
		}
		if msg == nil || msg.Photo() == nil {
			continue
		}

		photoPath, err := tghelper.RetryTelegram(ctx, "download_reciprocal_photo", func() (string, error) {
			return msg.Download(&telegram.DownloadOptions{FileName: writableTempDownloadDir()})
		})
		if err != nil || strings.TrimSpace(photoPath) == "" {
			log.Printf("[%s] Failed to download reciprocal-like photo attachment: %v", h.Name(), err)
			continue
		}

		data, readErr := os.ReadFile(photoPath)
		tghelper.CleanupFile(photoPath, nil, h.Name())
		if readErr != nil {
			log.Printf("[%s] Failed to read reciprocal-like photo attachment %q: %v", h.Name(), photoPath, readErr)
			continue
		}
		if len(data) == 0 {
			continue
		}

		photos = append(photos, ReciprocalLikePhoto{
			FileName:    reciprocalLikePhotoFilename(photoPath, len(photos), sourceIndex),
			ContentType: http.DetectContentType(data),
			Data:        data,
		})
	}

	return photos
}

func reciprocalLikePhotoSourceMessages(source RecentVisibleProfileMediaSource) []*telegram.NewMessage {
	if len(source.AlbumMessages) > 0 {
		return append([]*telegram.NewMessage(nil), source.AlbumMessages...)
	}
	if source.Message == nil {
		return nil
	}
	return []*telegram.NewMessage{source.Message}
}

func reciprocalLikePhotoFilename(path string, photoIndex int, sourceIndex int) string {
	base := strings.TrimSpace(filepath.Base(path))
	if base != "" && base != "." && base != string(filepath.Separator) {
		return base
	}

	return fmt.Sprintf("profile_photo_%02d_%02d.jpg", photoIndex+1, sourceIndex+1)
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}

	return right
}

func shouldStopWorker(ctx context.Context, quit <-chan struct{}) bool {
	select {
	case <-ctx.Done():
		return true
	case <-quit:
		return true
	default:
		return false
	}
}

// StopWorker stops the worker goroutine
func (h *Handler) StopWorker() {
	h.state.StopWorker()
}

func (h *Handler) WaitWorkerStop() {
	h.state.WaitWorkerStop()
}

// Shutdown stops accepting new work and then stops worker processing.
func (h *Handler) Shutdown() {
	h.state.BeginShutdown()
	h.clearPauseWakeup()
	h.cancelLifecycleContext()
	h.state.CancelWorkerContext()
	h.StopWorker()
	h.WaitWorkerStop()
}

func (h *Handler) lifecycleContext() context.Context {
	h.lifecycleMu.Lock()
	defer h.lifecycleMu.Unlock()

	if h.lifecycleCtx == nil {
		h.lifecycleCtx, h.lifecycleCancel = context.WithCancel(context.Background())
	}

	return h.lifecycleCtx
}

func (h *Handler) cancelLifecycleContext() {
	h.lifecycleMu.Lock()
	defer h.lifecycleMu.Unlock()

	if h.lifecycleCancel != nil {
		h.lifecycleCancel()
		h.lifecycleCancel = nil
	}
}

func (h *Handler) shouldStopProcessing(ctx context.Context) bool {
	if h.state.IsStopped() {
		return true
	}

	if ctx == nil {
		return false
	}

	select {
	case <-ctx.Done():
		return true
	case <-h.state.ShouldQuit():
		return true
	default:
		return false
	}
}

func waitForDelayWithContext(delay time.Duration, ctx context.Context) bool {
	if delay <= 0 {
		return ctx == nil || ctx.Err() == nil
	}

	timer := time.NewTimer(delay)
	defer func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}()

	if ctx == nil {
		<-timer.C
		return true
	}

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (h *Handler) cacheBotPeerFromAlbum(a *telegram.Album) {
	if a == nil {
		return
	}

	for _, msg := range a.Messages {
		h.cacheBotPeer(msg)
	}
}

func (h *Handler) isPaused() bool {
	paused, resumed, until := h.state.CheckPause(time.Now())
	if paused {
		log.Printf("[%s] Dating is paused until %s, skipping message", h.Name(), until.Format(time.RFC3339))
		return true
	}

	if resumed {
		h.clearPauseWakeup()
		log.Printf("[%s] Pause expired, resuming processing", h.Name())
	}

	return false
}

func (h *Handler) schedulePauseWakeup(deadline time.Time) {
	if deadline.IsZero() {
		h.clearPauseWakeup()
		return
	}

	ctx := h.lifecycleContext()
	delay := time.Until(deadline)
	if delay < 0 {
		delay = 0
	}

	h.pauseWakeMu.Lock()
	if h.pauseWakeTimer != nil {
		h.pauseWakeTimer.Stop()
	}
	h.pauseWakeDeadline = deadline
	h.pauseWakeTimer = time.AfterFunc(delay, func() {
		h.resumeAfterPause(ctx, deadline)
	})
	h.pauseWakeMu.Unlock()
}

func (h *Handler) clearPauseWakeup() {
	h.pauseWakeMu.Lock()
	timer := h.pauseWakeTimer
	h.pauseWakeTimer = nil
	h.pauseWakeDeadline = time.Time{}
	h.pauseWakeMu.Unlock()

	if timer != nil {
		timer.Stop()
	}
}

func (h *Handler) resumeAfterPause(ctx context.Context, deadline time.Time) {
	h.pauseWakeMu.Lock()
	if !h.pauseWakeDeadline.Equal(deadline) {
		h.pauseWakeMu.Unlock()
		return
	}
	h.pauseWakeTimer = nil
	h.pauseWakeMu.Unlock()

	if h.shouldStopProcessing(ctx) {
		return
	}

	paused, resumed, until := h.state.CheckPause(time.Now())
	if paused {
		h.schedulePauseWakeup(until)
		return
	}
	if !resumed {
		return
	}

	h.clearPauseWakeup()
	log.Printf("[%s] Pause expired, sending /start to re-check profile availability", h.Name())
	if err := h.sendStartCommand(ctx); err != nil && !h.shouldStopProcessing(ctx) {
		log.Printf("[%s] Failed to resume after pause: %v", h.Name(), err)
	}
}

func (h *Handler) shouldSkipOwnProfileByMessageID(messageID int32) bool {
	return h.state.ConsumeOwnProfileSkip(messageID, time.Now())
}

func firstAlbumMessageID(a *telegram.Album) int32 {
	if a == nil {
		return 0
	}

	var first int32
	for _, msg := range a.Messages {
		if msg == nil || msg.ID <= 0 {
			continue
		}
		if first == 0 || msg.ID < first {
			first = msg.ID
		}
	}

	return first
}

func maxAlbumMessageID(a *telegram.Album) int32 {
	if a == nil {
		return 0
	}

	var maxID int32
	for _, msg := range a.Messages {
		if msg == nil || msg.ID <= 0 {
			continue
		}
		if msg.ID > maxID {
			maxID = msg.ID
		}
	}

	return maxID
}

func firstAlbumGroupedID(a *telegram.Album) int64 {
	if a == nil {
		return 0
	}

	var groupedID int64
	var firstMessageID int32
	for _, msg := range a.Messages {
		if msg == nil || msg.Message == nil || msg.Message.GroupedID == 0 {
			continue
		}

		if groupedID == 0 || (msg.ID > 0 && (firstMessageID == 0 || msg.ID < firstMessageID)) {
			groupedID = msg.Message.GroupedID
			firstMessageID = msg.ID
		}
	}

	return groupedID
}

func visibleProfileCardFromAlbum(a *telegram.Album) (profileText string, messageID int32, ok bool) {
	if a == nil {
		return "", 0, false
	}

	msg := selectAlbumTextSourceMessage(a.Messages)
	if msg == nil {
		return "", 0, false
	}

	text := strings.TrimSpace(msg.Text())
	if text == "" {
		return "", 0, false
	}

	return text, msg.ID, true
}

func profileTextFromAlbumMessages(messages []*telegram.NewMessage) string {
	msg := selectAlbumTextSourceMessage(messages)
	if msg == nil {
		return ""
	}
	return msg.Text()
}

func selectAlbumTextSourceMessage(messages []*telegram.NewMessage) *telegram.NewMessage {
	var photoTextMessage *telegram.NewMessage
	photoTextIndex := -1
	var fallbackTextMessage *telegram.NewMessage
	fallbackTextIndex := -1

	for i, msg := range messages {
		if msg == nil || strings.TrimSpace(msg.Text()) == "" {
			continue
		}

		if (msg.Photo() != nil || msg.IsMedia()) && isEarlierAlbumTextCandidate(msg, i, photoTextMessage, photoTextIndex) {
			photoTextMessage = msg
			photoTextIndex = i
		}

		if isEarlierAlbumTextCandidate(msg, i, fallbackTextMessage, fallbackTextIndex) {
			fallbackTextMessage = msg
			fallbackTextIndex = i
		}
	}

	if photoTextMessage != nil {
		return photoTextMessage
	}

	return fallbackTextMessage
}

func isEarlierAlbumTextCandidate(candidate *telegram.NewMessage, candidateIndex int, current *telegram.NewMessage, currentIndex int) bool {
	if candidate == nil {
		return false
	}
	if current == nil {
		return true
	}

	candidateID := candidate.ID
	currentID := current.ID

	if candidateID > 0 && currentID > 0 && candidateID != currentID {
		return candidateID < currentID
	}
	if candidateID > 0 && currentID <= 0 {
		return true
	}
	if candidateID <= 0 && currentID > 0 {
		return false
	}

	return candidateIndex < currentIndex
}

func (h *Handler) ensureBotPeer(ctx context.Context) (telegram.InputPeer, error) {
	// 1. Check cache first
	if peer, ok := h.getBotPeer(); ok {
		return peer, nil
	}

	if h.tgClient == nil {
		return nil, fmt.Errorf("dating peer is not cached yet for chat %d", h.chatID)
	}

	// 2. Try to resolve by username (works with empty cache)
	if h.botUsername != "" {
		resolvedPeer, err := h.resolveBotByUsername(ctx)
		if err == nil {
			h.setBotPeer(resolvedPeer)
			log.Printf("[%s] Resolved bot peer via username: %s", h.Name(), h.botUsername)
			return resolvedPeer, nil
		}
		log.Printf("[%s] Failed to resolve bot by username %q: %v", h.Name(), h.botUsername, err)
	}

	// 3. Fallback: try old methods (for backward compatibility)
	peer, err := tghelper.RetryTelegram(ctx, "resolve_dating_peer_sendable", func() (telegram.InputPeer, error) {
		return h.tgClient.GetSendablePeer(h.chatID)
	})
	if err != nil {
		peer, err = tghelper.RetryTelegram(ctx, "resolve_dating_peer_input", func() (telegram.InputPeer, error) {
			return h.tgClient.GetInputPeer(h.chatID)
		})
		if err != nil {
			return nil, fmt.Errorf("dating peer is not cached yet for chat %d and resolve failed: %w", h.chatID, err)
		}
	}

	h.setBotPeer(peer)
	return peer, nil
}

func (h *Handler) downloadAlbumData(ctx context.Context, a *telegram.Album, profileText string) (ProfileData, func()) {
	var data ProfileData
	var photoPaths []string

	for _, msg := range a.Messages {
		if msg.Photo() != nil {
			path, err := tghelper.RetryTelegram(ctx, "download_album_photo", func() (string, error) {
				return msg.Download(&telegram.DownloadOptions{FileName: writableTempDownloadDir()})
			})
			if err == nil && path != "" {
				photoPaths = append(photoPaths, path)
			}
		}
	}

	data.PhotoPaths = photoPaths
	data.PhotoIdentifiers = photoIdentifiersFromAlbum(a)
	data.ProfileText = profileText

	cleanup := func() {
		for _, path := range photoPaths {
			tghelper.CleanupFile(path, nil, h.Name())
		}
	}

	return data, cleanup
}

func (h *Handler) resolveAlbumProfileText(a *telegram.Album) string {
	if a == nil {
		return ""
	}

	profileText := profileTextFromAlbumMessages(a.Messages)
	if strings.TrimSpace(profileText) != "" {
		return profileText
	}

	if groupedText, ok := h.state.ConsumeGroupedCaption(firstAlbumGroupedID(a), time.Now()); ok {
		return groupedText
	}

	return ""
}

func writableTempDownloadDir() string {
	return os.TempDir() + string(os.PathSeparator)
}

type photoIdentifier struct {
	messageID  int32
	index      int
	identifier string
}

func photoIdentifiersFromMessage(m *telegram.NewMessage) []string {
	if m == nil {
		return nil
	}

	photo := m.Photo()
	if photo == nil {
		return nil
	}

	return []string{fmt.Sprintf("%d:%d", photo.ID, photo.AccessHash)}
}

func photoIdentifiersFromAlbum(a *telegram.Album) []string {
	if a == nil {
		return nil
	}

	identifiers := make([]photoIdentifier, 0, len(a.Messages))
	for i, msg := range a.Messages {
		if msg == nil {
			continue
		}

		photo := msg.Photo()
		if photo == nil {
			continue
		}

		identifiers = append(identifiers, photoIdentifier{
			messageID:  msg.ID,
			index:      i,
			identifier: fmt.Sprintf("%d:%d", photo.ID, photo.AccessHash),
		})
	}

	if len(identifiers) == 0 {
		return nil
	}

	sort.SliceStable(identifiers, func(i, j int) bool {
		left := identifiers[i]
		right := identifiers[j]

		if left.messageID > 0 && right.messageID > 0 && left.messageID != right.messageID {
			return left.messageID < right.messageID
		}
		if left.messageID > 0 && right.messageID <= 0 {
			return true
		}
		if left.messageID <= 0 && right.messageID > 0 {
			return false
		}
		if left.index != right.index {
			return left.index < right.index
		}

		return left.identifier < right.identifier
	})

	keys := make([]string, 0, len(identifiers))
	for _, item := range identifiers {
		keys = append(keys, item.identifier)
	}

	return keys
}

func normalizeProfileTextForCache(text string) string {
	trimmed := strings.TrimSpace(strings.ToLower(text))
	if trimmed == "" {
		return ""
	}

	return strings.Join(strings.Fields(trimmed), " ")
}

func buildProfileLLMCacheKey(profileText string, photoIDs []string) string {
	normalizedText := normalizeProfileTextForCache(profileText)

	b := strings.Builder{}
	b.WriteString("v1|")
	b.WriteString(normalizedText)
	b.WriteString("|")
	for i, photoID := range photoIDs {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(strings.TrimSpace(photoID))
	}

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}
