package dating

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/0FL01/tg-dating-agent/internal/llm"
	"github.com/0FL01/tg-dating-agent/internal/standalone"
	"github.com/0FL01/tg-dating-agent/internal/tghelper"
	"github.com/0FL01/tg-dating-agent/internal/utils"
	"github.com/amarnathcjd/gogram/telegram"
)

const DailyLimitPauseDuration = 24 * time.Hour
const stopCommandTimeout = 10 * time.Second

// jitterRand is a thread-safe local random generator for jitter calculations
var jitterRand = rand.New(rand.NewSource(time.Now().UnixNano()))

// Handler handles the dating bot automation
type Handler struct {
	config          *standalone.Config
	client          llm.MultimodalSummarizer
	replyAudit      replyAuditAppender
	tgClient        *telegram.Client
	state           *StateMachine
	chatID          int64
	botUsername     string
	model           string
	prompt          string
	actionDelay     time.Duration
	jitterDelay     time.Duration
	temperature     float64
	sendMessageFn   func(context.Context, telegram.InputPeer, string) error
	sendSleepFn     func(context.Context) error
	botPeerMu       sync.RWMutex
	botPeer         telegram.InputPeer
	lifecycleMu     sync.Mutex
	lifecycleCtx    context.Context
	lifecycleCancel context.CancelFunc
	stopSleepOnce   sync.Once
}

type replyAuditAppender interface {
	Append(mbti, prompt, response string) error
}

// NewHandler creates a new dating handler
func NewHandler(cfg *standalone.Config, client llm.MultimodalSummarizer, tgClient *telegram.Client) *Handler {
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
	if h.jitterDelay <= 0 {
		return h.actionDelay
	}
	jitter := time.Duration(jitterRand.Int63n(int64(h.jitterDelay)))
	return h.actionDelay + jitter
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

	if h.isPaused() {
		return nil
	}

	text := m.Text()
	h.rememberGroupedCaption(m, text)
	log.Printf("[%s] Received message: %s...", h.Name(), utils.Truncate(text, 50))

	if strings.Contains(strings.ToLower(text), PatternLikedYou) {
		log.Printf("[%s] Received match notification, stopping", h.Name())
		h.Shutdown()
		return nil
	}

	if isDailyLimitMessage(text) {
		pausedUntil := h.state.PauseFor(DailyLimitPauseDuration)
		h.state.ClearPendingMessage()
		h.state.ClearProfileData()
		h.state.ResetRetry()
		h.state.SetState(StateIdle)
		log.Printf("[%s] Daily limit message received, pausing until %s", h.Name(), pausedUntil.Format(time.RFC3339))
		return nil
	}

	if strings.Contains(strings.ToLower(text), PatternTooLong) {
		log.Printf("[%s] Message was too long, retrying...", h.Name())
		return h.retryGenerateMessage(h.lifecycleContext(), RetryTooLong)
	}

	if strings.Contains(strings.ToLower(text), PatternTooShort) {
		log.Printf("[%s] Message was too short, retrying with different prompt...", h.Name())
		return h.retryGenerateMessage(h.lifecycleContext(), RetryTooShort)
	}

	if strings.Contains(text, PatternWriteMessage) {
		return h.sendPendingMessage(m)
	}

	if strings.Contains(text, PatternViewProfiles) {
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
		if !h.state.Enqueue(ProfileJob{Type: "message", Message: m, ProfileMessageID: m.ID}) {
			log.Printf("[%s] Queue full, skipping profile", h.Name())
		}
		return nil
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

	if hasReplyMarkup(m) {
		return false
	}

	return isNonEmptyTextOnlyMessage(m.Text())
}

func isNonEmptyTextOnlyMessage(text string) bool {
	return strings.TrimSpace(text) != ""
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
	profileText := m.Text()
	bioText := extractBioText(profileText)
	bioLen := utf8.RuneCountInString(bioText)

	if h.isLowQuality(profileText) {
		log.Printf("[%s] Skipping low quality profile (bio_len=%d, min=%d): %s...",
			h.Name(), bioLen, h.config.DatingMinBioLength, utils.Truncate(profileText, 20))
		if !h.state.SetStateIfNotStopped(StateViewingProfiles) {
			return nil
		}
		return h.clickButtonWithContext(ctx, ButtonDislike)
	}

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
	content := llm.MultimodalContent{
		Text:       data.ProfileText,
		ImagePaths: data.PhotoPaths,
	}

	cacheKey := buildProfileLLMCacheKey(data.ProfileText, data.PhotoIdentifiers)
	cacheKeyLog := cacheKey
	if len(cacheKeyLog) > 12 {
		cacheKeyLog = cacheKeyLog[:12]
	}

	var (
		mbti         string
		generatedMsg string
	)

	if cachedMBTI, cachedOpener, ok := h.state.GetProfileLLMCache(cacheKey); ok && cachedMBTI != "" && cachedOpener != "" {
		mbti = cachedMBTI
		generatedMsg = cachedOpener
		log.Printf("[%s] Profile LLM cache hit (key=%s, text_len=%d, photos=%d)", h.Name(), cacheKeyLog, len(strings.TrimSpace(data.ProfileText)), len(data.PhotoIdentifiers))
	} else {
		log.Printf("[%s] Profile LLM cache miss (key=%s, text_len=%d, photos=%d)", h.Name(), cacheKeyLog, len(strings.TrimSpace(data.ProfileText)), len(data.PhotoIdentifiers))

		mbtiRaw, err := h.client.SummarizeMultimodal(ctx, h.model, h.config.DatingMBTIPrompt, content, h.temperature)
		if err != nil {
			log.Printf("[%s] Failed to analyze MBTI: %v, skipping profile", h.Name(), err)
			return h.clickButtonWithContext(ctx, ButtonDislike)
		}

		parsedMBTI, ok := parseMBTI(mbtiRaw)
		if !ok {
			log.Printf("[%s] Failed to parse MBTI from response %q, skipping profile", h.Name(), utils.Truncate(mbtiRaw, 60))
			return h.clickButtonWithContext(ctx, ButtonDislike)
		}
		mbti = parsedMBTI

		if !isMBTIAllowed(mbti, h.config.DatingMBTIAllowlist) {
			log.Printf("[%s] MBTI %s is not in allowlist %v, skipping profile", h.Name(), mbti, h.config.DatingMBTIAllowlist)
			return h.clickButtonWithContext(ctx, ButtonDislike)
		}

		log.Printf("[%s] MBTI %s is allowed, generating reply", h.Name(), mbti)
		if shouldStopWorker(ctx, h.state.ShouldQuit()) || h.state.IsStopped() {
			return nil
		}

		generatedMsg, err = h.client.SummarizeMultimodal(ctx, h.model, h.prompt, content, h.temperature)
		if err != nil {
			log.Printf("[%s] Failed to generate message: %v", h.Name(), err)
			return h.clickButtonWithContext(ctx, ButtonDislike)
		}
		h.appendReplyAudit(mbti, h.prompt, generatedMsg)

		h.state.SetProfileLLMCache(cacheKey, mbti, generatedMsg)
		log.Printf("[%s] Profile LLM cache stored (key=%s)", h.Name(), cacheKeyLog)
	}

	if !isMBTIAllowed(mbti, h.config.DatingMBTIAllowlist) {
		log.Printf("[%s] MBTI %s is not in allowlist %v, skipping profile", h.Name(), mbti, h.config.DatingMBTIAllowlist)
		return h.clickButtonWithContext(ctx, ButtonDislike)
	}

	log.Printf("[%s] MBTI %s is allowed, generating reply", h.Name(), mbti)
	if shouldStopWorker(ctx, h.state.ShouldQuit()) || h.state.IsStopped() {
		return nil
	}

	data.MBTI = mbti
	h.state.SetProfileData(&data)
	h.state.ResetRetry()

	log.Printf("[%s] Generated message: %s", h.Name(), utils.Truncate(generatedMsg, 100))
	if shouldStopWorker(ctx, h.state.ShouldQuit()) || h.state.IsStopped() {
		return nil
	}

	h.state.SetPendingMessage(generatedMsg)
	if !h.state.SetStateIfNotStopped(StateWaitingPrompt) {
		return nil
	}

	return h.clickButtonWithContext(ctx, ButtonLikeMessage)
}

func parseMBTI(response string) (string, bool) {
	upper := strings.ToUpper(response)
	tokens := strings.FieldsFunc(upper, func(r rune) bool {
		return r < 'A' || r > 'Z'
	})

	for _, token := range tokens {
		if isValidMBTI(token) {
			return token, true
		}
	}

	return "", false
}

func isMBTIAllowed(mbti string, allowlist []string) bool {
	if len(allowlist) == 0 {
		return false
	}

	for _, allowed := range allowlist {
		if mbti == allowed {
			return true
		}
	}

	return false
}

func isValidMBTI(mbti string) bool {
	switch mbti {
	case "INTJ", "INTP", "ENTJ", "ENTP",
		"INFJ", "INFP", "ENFJ", "ENFP",
		"ISTJ", "ISFJ", "ESTJ", "ESFJ",
		"ISTP", "ISFP", "ESTP", "ESFP":
		return true
	default:
		return false
	}
}

func (h *Handler) sendPendingMessage(m *telegram.NewMessage) error {
	msg := h.state.GetPendingMessage()
	if msg == "" {
		log.Printf("[%s] No pending message to send, skipping", h.Name())
		return nil
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
	err := h.sendDatingMessage(ctx, peer, msg)
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

func (h *Handler) retryGenerateMessage(ctx context.Context, retryType RetryType) error {
	if h.shouldStopProcessing(ctx) {
		return nil
	}

	retryCount := h.state.IncrementRetry()
	pendingMsg := h.state.GetPendingMessage()

	log.Printf("[%s] Retry attempt %d/%d (type: %v)", h.Name(), retryCount, MaxRetries, retryType)

	if retryCount > MaxRetries {
		log.Printf("[%s] Max retries reached, using fallback", h.Name())
		return h.handleMaxRetriesReached(ctx, retryType, pendingMsg)
	}

	profileData := h.state.GetProfileData()
	if profileData == nil {
		log.Printf("[%s] No profile data for retry, using fallback", h.Name())
		return h.handleMaxRetriesReached(ctx, retryType, pendingMsg)
	}

	var retryPrompt string
	switch retryType {
	case RetryTooShort:
		retryPrompt = TooShortRetryPrompt
	case RetryTooLong:
		retryPrompt = TooLongRetryPrompt + pendingMsg
	}

	content := llm.MultimodalContent{
		Text:       profileData.ProfileText,
		ImagePaths: profileData.PhotoPaths,
	}

	generatedMsg, err := h.client.SummarizeMultimodal(ctx, h.model, retryPrompt, content, h.temperature)
	if err != nil {
		if h.shouldStopProcessing(ctx) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil
		}
		log.Printf("[%s] Failed to regenerate message: %v, using fallback", h.Name(), err)
		return h.handleMaxRetriesReached(ctx, retryType, pendingMsg)
	}
	h.appendReplyAudit(profileData.MBTI, retryPrompt, generatedMsg)

	log.Printf("[%s] Regenerated message (attempt %d): %s", h.Name(), retryCount, utils.Truncate(generatedMsg, 100))
	if h.shouldStopProcessing(ctx) {
		return nil
	}

	h.state.SetPendingMessage(generatedMsg)
	return h.sendTruncatedMessage(ctx, generatedMsg)
}

func (h *Handler) handleMaxRetriesReached(ctx context.Context, retryType RetryType, pendingMsg string) error {
	if h.shouldStopProcessing(ctx) {
		return nil
	}

	switch retryType {
	case RetryTooLong:
		truncatedMsg := truncateMessage(pendingMsg, MaxMsgLength)
		if h.shouldStopProcessing(ctx) {
			return nil
		}
		h.state.SetPendingMessage(truncatedMsg)
		return h.sendTruncatedMessage(ctx, truncatedMsg)
	case RetryTooShort:
		fallbackMsg := "Привет! Заинтересовал(а) твой профиль, давай познакомимся? 😊"
		if h.shouldStopProcessing(ctx) {
			return nil
		}
		h.state.SetPendingMessage(fallbackMsg)
		return h.sendTruncatedMessage(ctx, fallbackMsg)
	}
	return nil
}

func (h *Handler) sendTruncatedMessage(ctx context.Context, msg string) error {
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
	err := h.sendDatingMessage(ctx, peer, msg)
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

func truncateMessage(msg string, maxLen int) string {
	runes := []rune(msg)
	if len(runes) <= maxLen {
		return msg
	}

	truncatedRunes := runes[:maxLen]
	lastSpace := -1
	for i := len(truncatedRunes) - 1; i >= 0; i-- {
		if truncatedRunes[i] == ' ' {
			lastSpace = i
			break
		}
	}

	if lastSpace > maxLen/2 {
		return string(truncatedRunes[:lastSpace])
	}

	return string(truncatedRunes)
}

func (h *Handler) appendReplyAudit(mbti, prompt, response string) {
	if h.replyAudit == nil {
		return
	}

	if err := h.replyAudit.Append(mbti, prompt, response); err != nil {
		log.Printf("[%s] Reply audit append failed: %v", h.Name(), err)
	}
}

func (h *Handler) clickButton(buttonText string) error {
	return h.clickButtonWithContext(h.lifecycleContext(), buttonText)
}

func (h *Handler) clickButtonWithContext(ctx context.Context, buttonText string) error {
	log.Printf("[%s] Clicking button: %s", h.Name(), buttonText)

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

	// Add to queue instead of direct processing
	if !h.state.Enqueue(ProfileJob{Type: "album", Album: a, ProfileMessageID: maxAlbumMessageID(a)}) {
		log.Printf("[%s] Queue full, skipping album", h.Name())
	}
	return nil
}

// handleAlbumJob performs actual album processing (inside worker)
func (h *Handler) handleAlbumJob(ctx context.Context, a *telegram.Album) error {
	h.cacheBotPeerFromAlbum(a)

	profileText := h.resolveAlbumProfileText(a)
	bioText := extractBioText(profileText)
	bioLen := utf8.RuneCountInString(bioText)

	if h.isLowQuality(profileText) {
		log.Printf("[%s] Skipping low quality album profile (bio_len=%d, min=%d): %s...",
			h.Name(), bioLen, h.config.DatingMinBioLength, utils.Truncate(profileText, 20))
		if !h.state.SetStateIfNotStopped(StateViewingProfiles) {
			return nil
		}
		return h.clickButtonWithContext(ctx, ButtonDislike)
	}

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
		return h.handleAlbumJob(ctx, job.Album)
	case "menu_recovery":
		log.Printf("[%s] Processing menu recovery job", h.Name())
		return h.clickButtonWithContext(ctx, ButtonViewProfiles)
	case "stuck_recovery":
		log.Printf("[%s] Processing stuck recovery job", h.Name())
		return h.clickButtonWithContext(ctx, ButtonViewProfiles)
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
		log.Printf("[%s] 24h pause expired, resuming processing", h.Name())
	}

	return false
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

func (h *Handler) isLowQuality(text string) bool {
	if !h.config.DatingSkipLowQuality {
		return false
	}
	bioText := extractBioText(text)
	return utf8.RuneCountInString(bioText) < h.config.DatingMinBioLength
}

func extractBioText(profileText string) string {
	trimmed := strings.TrimSpace(profileText)
	if trimmed == "" {
		return ""
	}

	separators := []string{" – ", " — ", " - ", "–", "—", "-"}
	for _, sep := range separators {
		idx := strings.Index(trimmed, sep)
		if idx < 0 {
			continue
		}

		bio := strings.TrimSpace(trimmed[idx+len(sep):])
		return bio
	}

	return trimmed
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
