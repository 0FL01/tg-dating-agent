package dating

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
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

// jitterRand is a thread-safe local random generator for jitter calculations
var jitterRand = rand.New(rand.NewSource(time.Now().UnixNano()))

// Handler handles the dating bot automation
type Handler struct {
	config      *standalone.Config
	client      llm.MultimodalSummarizer
	tgClient    *telegram.Client
	state       *StateMachine
	chatID      int64
	botUsername string
	model       string
	prompt      string
	actionDelay time.Duration
	jitterDelay time.Duration
	temperature float64
	botPeerMu   sync.RWMutex
	botPeer     telegram.InputPeer
}

// NewHandler creates a new dating handler
func NewHandler(cfg *standalone.Config, client llm.MultimodalSummarizer, tgClient *telegram.Client) *Handler {
	return &Handler{
		config:      cfg,
		client:      client,
		tgClient:    tgClient,
		state:       NewStateMachine(),
		chatID:      cfg.DatingBotChatID,
		botUsername: cfg.DatingBotUsername,
		model:       cfg.DatingModel,
		prompt:      cfg.DatingPrompt,
		actionDelay: cfg.DatingActionDelay,
		jitterDelay: cfg.DatingJitterDelay,
		temperature: cfg.DatingTemperature,
	}
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
	log.Printf("[%s] Received message: %s...", h.Name(), utils.Truncate(text, 50))

	if strings.Contains(strings.ToLower(text), PatternLikedYou) {
		log.Printf("[%s] Received match notification, stopping", h.Name())
		h.state.SetState(StateStopped)
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
		return h.retryGenerateMessage(RetryTooLong)
	}

	if strings.Contains(strings.ToLower(text), PatternTooShort) {
		log.Printf("[%s] Message was too short, retrying with different prompt...", h.Name())
		return h.retryGenerateMessage(RetryTooShort)
	}

	if strings.Contains(text, PatternWriteMessage) {
		return h.sendPendingMessage(m)
	}

	if strings.Contains(text, PatternViewProfiles) {
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
		// Check if this is our own profile that should be skipped
		if h.shouldSkipOwnProfileByMessageID(m.ID) {
			log.Printf("[%s] Skipping own profile photo", h.Name())
			// Continue to actual profiles by clicking "View profiles"
			if !h.state.Enqueue(ProfileJob{Type: "menu_recovery", Message: m}) {
				log.Printf("[%s] Queue full, skipping view profiles after own profile", h.Name())
			}
			return nil
		}
		if !h.state.Enqueue(ProfileJob{Type: "message", Message: m}) {
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

func (h *Handler) processProfile(m *telegram.NewMessage) error {
	if h.isLowQuality(m.Text()) {
		log.Printf("[%s] Skipping low quality profile (len=%d): %s...",
			h.Name(), utf8.RuneCountInString(m.Text()), utils.Truncate(m.Text(), 20))
		h.state.SetState(StateViewingProfiles)
		return h.clickButton(ButtonDislike)
	}

	ctx := context.Background()
	h.state.SetState(StateViewingProfiles)

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

	mbtiRaw, err := h.client.SummarizeMultimodal(h.model, h.config.DatingMBTIPrompt, content, h.temperature)
	if err != nil {
		log.Printf("[%s] Failed to analyze MBTI: %v, skipping profile", h.Name(), err)
		return h.clickButton(ButtonDislike)
	}

	mbti, ok := parseMBTI(mbtiRaw)
	if !ok {
		log.Printf("[%s] Failed to parse MBTI from response %q, skipping profile", h.Name(), utils.Truncate(mbtiRaw, 60))
		return h.clickButton(ButtonDislike)
	}

	if !isMBTIAllowed(mbti, h.config.DatingMBTIAllowlist) {
		log.Printf("[%s] MBTI %s is not in allowlist %v, skipping profile", h.Name(), mbti, h.config.DatingMBTIAllowlist)
		return h.clickButton(ButtonDislike)
	}

	log.Printf("[%s] MBTI %s is allowed, generating reply", h.Name(), mbti)

	h.state.SetProfileData(&data)
	h.state.ResetRetry()

	generatedMsg, err := h.client.SummarizeMultimodal(h.model, h.prompt, content, h.temperature)
	if err != nil {
		log.Printf("[%s] Failed to generate message: %v", h.Name(), err)
		return h.clickButton(ButtonDislike)
	}

	log.Printf("[%s] Generated message: %s", h.Name(), utils.Truncate(generatedMsg, 100))

	h.state.SetPendingMessage(generatedMsg)
	h.state.SetState(StateWaitingPrompt)

	return h.clickButton(ButtonLikeMessage)
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

	log.Printf("[%s] Sending message: %s (delay: %v)", h.Name(), utils.Truncate(msg, 50), h.actionDelay)

	time.Sleep(h.actionDelay)

	ctx := context.Background()
	peer, ok := h.getBotPeer()
	if !ok {
		err := fmt.Errorf("dating peer is not cached yet for chat %d", h.chatID)
		log.Printf("[%s] %v", h.Name(), err)
		return err
	}
	_, err := tghelper.RetryTelegram(ctx, "send_dating_message", func() (*telegram.NewMessage, error) {
		return h.tgClient.SendMessage(peer, msg)
	})

	h.finalizeSendState()

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

func (h *Handler) retryGenerateMessage(retryType RetryType) error {
	retryCount := h.state.IncrementRetry()
	pendingMsg := h.state.GetPendingMessage()

	log.Printf("[%s] Retry attempt %d/%d (type: %v)", h.Name(), retryCount, MaxRetries, retryType)

	if retryCount > MaxRetries {
		log.Printf("[%s] Max retries reached, using fallback", h.Name())
		return h.handleMaxRetriesReached(retryType, pendingMsg)
	}

	profileData := h.state.GetProfileData()
	if profileData == nil {
		log.Printf("[%s] No profile data for retry, using fallback", h.Name())
		return h.handleMaxRetriesReached(retryType, pendingMsg)
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

	generatedMsg, err := h.client.SummarizeMultimodal(h.model, retryPrompt, content, h.temperature)
	if err != nil {
		log.Printf("[%s] Failed to regenerate message: %v, using fallback", h.Name(), err)
		return h.handleMaxRetriesReached(retryType, pendingMsg)
	}

	log.Printf("[%s] Regenerated message (attempt %d): %s", h.Name(), retryCount, utils.Truncate(generatedMsg, 100))

	h.state.SetPendingMessage(generatedMsg)
	return h.sendTruncatedMessage(generatedMsg)
}

func (h *Handler) handleMaxRetriesReached(retryType RetryType, pendingMsg string) error {
	switch retryType {
	case RetryTooLong:
		truncatedMsg := truncateMessage(pendingMsg, MaxMsgLength)
		h.state.SetPendingMessage(truncatedMsg)
		return h.sendTruncatedMessage(truncatedMsg)
	case RetryTooShort:
		fallbackMsg := "Привет! Заинтересовал(а) твой профиль, давай познакомимся? 😊"
		h.state.SetPendingMessage(fallbackMsg)
		return h.sendTruncatedMessage(fallbackMsg)
	}
	return nil
}

func (h *Handler) sendTruncatedMessage(msg string) error {
	log.Printf("[%s] Sending message directly: %s (delay: %v)", h.Name(), utils.Truncate(msg, 50), h.actionDelay)

	time.Sleep(h.actionDelay)

	ctx := context.Background()
	peer, ok := h.getBotPeer()
	if !ok {
		err := fmt.Errorf("dating peer is not cached yet for chat %d", h.chatID)
		log.Printf("[%s] %v", h.Name(), err)
		return err
	}
	_, err := tghelper.RetryTelegram(ctx, "send_dating_message", func() (*telegram.NewMessage, error) {
		return h.tgClient.SendMessage(peer, msg)
	})

	h.finalizeSendState()

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

func (h *Handler) clickButton(buttonText string) error {
	log.Printf("[%s] Clicking button: %s", h.Name(), buttonText)

	// Note: Rate limiting is now handled in worker loop via getProcessingDelay()
	// This method no longer includes time.Sleep to avoid double delays

	ctx := context.Background()
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
	h.state.SetState(StateStopped)
	_ = h.clickButton(ButtonSleep)
}

func (h *Handler) Start() {
	log.Printf("[%s] Starting...", h.Name())
	h.state.SetState(StateIdle)
}

func (h *Handler) Bootstrap() error {
	return h.bootstrapWithActions(func() error {
		return h.sendBootstrapCommand("/start")
	}, func() error {
		return h.clickButton(ButtonViewProfiles)
	})
}

func (h *Handler) bootstrapWithActions(sendStart func() error, startSearch func() error) error {
	if h.isPaused() {
		log.Printf("[%s] Startup bootstrap skipped: pause is active", h.Name())
		return nil
	}

	log.Printf("[%s] Startup bootstrap: sending /start then %s", h.Name(), ButtonViewProfiles)

	startErr := sendStart()
	if startErr != nil {
		log.Printf("[%s] Startup bootstrap: /start failed: %v", h.Name(), startErr)
	}

	searchErr := startSearch()
	if searchErr != nil {
		log.Printf("[%s] Startup bootstrap: %s failed: %v", h.Name(), ButtonViewProfiles, searchErr)
	}

	if startErr != nil || searchErr != nil {
		return fmt.Errorf("startup bootstrap errors: send /start: %v; start search: %v", startErr, searchErr)
	}

	log.Printf("[%s] Startup bootstrap completed", h.Name())
	return nil
}

func (h *Handler) sendBootstrapCommand(command string) error {
	log.Printf("[%s] Sending bootstrap command: %s (delay: %v)", h.Name(), command, h.actionDelay)

	time.Sleep(h.actionDelay)

	ctx := context.Background()
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
	if !h.state.Enqueue(ProfileJob{Type: "album", Album: a}) {
		log.Printf("[%s] Queue full, skipping album", h.Name())
	}
	return nil
}

// handleAlbumJob performs actual album processing (inside worker)
func (h *Handler) handleAlbumJob(a *telegram.Album) error {
	h.cacheBotPeerFromAlbum(a)

	var profileText string
	for _, msg := range a.Messages {
		if text := msg.Text(); text != "" {
			profileText = text
			break
		}
	}

	if h.isLowQuality(profileText) {
		log.Printf("[%s] Skipping low quality album profile (len=%d): %s...",
			h.Name(), utf8.RuneCountInString(profileText), utils.Truncate(profileText, 20))
		h.state.SetState(StateViewingProfiles)
		return h.clickButton(ButtonDislike)
	}

	ctx := context.Background()
	h.state.SetState(StateViewingProfiles)

	data, cleanup := h.downloadAlbumData(ctx, a)
	defer cleanup()

	log.Printf("[%s] Album: %d photos, text: %s", h.Name(), len(data.PhotoPaths), utils.Truncate(data.ProfileText, 100))

	return h.generateAndSendLike(ctx, data)
}

// processJob processes jobs from the queue sequentially
func (h *Handler) processJob(job ProfileJob) error {
	switch job.Type {
	case "message":
		if job.Message == nil {
			return nil
		}
		return h.processProfile(job.Message)
	case "album":
		if job.Album == nil {
			return nil
		}
		return h.handleAlbumJob(job.Album)
	case "menu_recovery":
		log.Printf("[%s] Processing menu recovery job", h.Name())
		return h.clickButton(ButtonViewProfiles)
	case "stuck_recovery":
		log.Printf("[%s] Processing stuck recovery job", h.Name())
		return h.clickButton(ButtonViewProfiles)
	default:
		log.Printf("[%s] Unknown job type: %s", h.Name(), job.Type)
		return nil
	}
}

// StartWorker starts a goroutine to process the profile queue
func (h *Handler) StartWorker() {
	go func() {
		log.Printf("[%s] Worker started", h.Name())
		defer log.Printf("[%s] Worker stopped", h.Name())

		for {
			select {
			case job := <-h.state.GetQueue():
				// Rate limiting: wait before processing each profile
				delay := h.getProcessingDelay()
				log.Printf("[%s] Rate limiting: waiting %v before processing", h.Name(), delay)
				time.Sleep(delay)

				log.Printf("[%s] Processing job type: %s", h.Name(), job.Type)
				if err := h.processJob(job); err != nil {
					log.Printf("[%s] Job processing error: %v", h.Name(), err)
				}
			case <-h.state.ShouldQuit():
				return
			}
		}
	}()
}

// StopWorker stops the worker goroutine
func (h *Handler) StopWorker() {
	h.state.StopWorker()
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

func (h *Handler) downloadAlbumData(ctx context.Context, a *telegram.Album) (ProfileData, func()) {
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

	for _, msg := range a.Messages {
		if text := msg.Text(); text != "" {
			data.ProfileText = text
			break
		}
	}

	data.PhotoPaths = photoPaths

	cleanup := func() {
		for _, path := range photoPaths {
			tghelper.CleanupFile(path, nil, h.Name())
		}
	}

	return data, cleanup
}

func (h *Handler) isLowQuality(text string) bool {
	if !h.config.DatingSkipLowQuality {
		return false
	}
	trimmed := strings.TrimSpace(text)
	return utf8.RuneCountInString(trimmed) < h.config.DatingMinBioLength
}

func writableTempDownloadDir() string {
	return os.TempDir() + string(os.PathSeparator)
}
