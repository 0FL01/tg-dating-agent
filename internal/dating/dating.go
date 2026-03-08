package dating

import (
	"context"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0FL01/tg-dating-agent/internal/llm"
	"github.com/0FL01/tg-dating-agent/internal/standalone"
	"github.com/0FL01/tg-dating-agent/internal/tghelper"
	"github.com/0FL01/tg-dating-agent/internal/utils"
	"github.com/amarnathcjd/gogram/telegram"
)

const DailyLimitPauseDuration = 24 * time.Hour

// Handler handles the dating bot automation
type Handler struct {
	config      *standalone.Config
	client      llm.MultimodalSummarizer
	tgClient    *telegram.Client
	state       *StateMachine
	chatID      int64
	model       string
	prompt      string
	actionDelay time.Duration
	temperature float64
}

// NewHandler creates a new dating handler
func NewHandler(cfg *standalone.Config, client llm.MultimodalSummarizer, tgClient *telegram.Client) *Handler {
	return &Handler{
		config:      cfg,
		client:      client,
		tgClient:    tgClient,
		state:       NewStateMachine(),
		chatID:      cfg.DatingBotChatID,
		model:       cfg.DatingModel,
		prompt:      cfg.DatingPrompt,
		actionDelay: cfg.DatingActionDelay,
		temperature: cfg.DatingTemperature,
	}
}

// Name returns the handler name for logging
func (h *Handler) Name() string {
	return "dating"
}

// Filter returns a filter function for incoming messages from dating bot
func (h *Handler) Filter() func(*telegram.NewMessage) bool {
	return func(m *telegram.NewMessage) bool {
		log.Printf("[dating] Filter check: ChatID=%d, SenderID=%d, expected=%d",
			m.ChatID(), m.SenderID(), h.chatID)
		return m.ChatID() == h.chatID
	}
}

// Handle processes incoming messages from the dating bot
func (h *Handler) Handle(m *telegram.NewMessage) error {
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

	if text == PatternDailyLimitExact {
		pausedUntil := h.state.PauseFor(DailyLimitPauseDuration)
		h.state.ClearPendingMessage()
		h.state.ClearProfileData()
		h.state.ResetRetry()
		h.state.SetState(StateIdle)
		log.Printf("[%s] Daily limit exact message received, pausing until %s", h.Name(), pausedUntil.Format(time.RFC3339))
		return nil
	}

	if strings.Contains(strings.ToLower(text), PatternTooManyLikes) {
		log.Printf("[%s] Daily like limit reached, stopping", h.Name())
		h.state.ClearPendingMessage()
		h.state.ClearProfileData()
		h.state.ResetRetry()
		h.state.SetState(StateStopped)
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
		log.Printf("[%s] Detected main menu, starting profile viewing", h.Name())
		return h.clickButton(ButtonViewProfiles)
	}

	if m.Photo() != nil || m.IsMedia() {
		return h.processProfile(m)
	}

	if h.state.GetState() == StateViewingProfiles && hasReplyKeyboardButtonText(m, ButtonViewProfiles) {
		log.Printf("[%s] Recovering viewing flow from text-only message via reply keyboard", h.Name())
		return h.clickButton(ButtonViewProfiles)
	}

	return nil
}

func (h *Handler) processProfile(m *telegram.NewMessage) error {
	if !h.state.TryStartProcessing() {
		log.Printf("[%s] Already processing, skipping profile", h.Name())
		return nil
	}
	defer h.state.StopProcessing()

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
		return m.Download()
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
	_, err := tghelper.RetryTelegram(ctx, "send_dating_message", func() (*telegram.NewMessage, error) {
		return h.tgClient.SendMessage(h.chatID, msg)
	})

	h.state.ClearPendingMessage()
	h.state.ClearProfileData()
	h.state.ResetRetry()
	h.state.SetState(StateViewingProfiles)

	if err != nil {
		log.Printf("[%s] Failed to send message: %v", h.Name(), err)
		return err
	}

	log.Printf("[%s] Message sent successfully", h.Name())
	return nil
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
	_, err := tghelper.RetryTelegram(ctx, "send_dating_message", func() (*telegram.NewMessage, error) {
		return h.tgClient.SendMessage(h.chatID, msg)
	})

	if err != nil {
		log.Printf("[%s] Failed to send message: %v", h.Name(), err)
		return err
	}

	return nil
}

func truncateMessage(msg string, maxLen int) string {
	if len(msg) <= maxLen {
		return msg
	}

	truncated := msg[:maxLen]
	lastSpace := strings.LastIndex(truncated, " ")
	if lastSpace > maxLen/2 {
		return truncated[:lastSpace]
	}

	return truncated
}

func (h *Handler) clickButton(buttonText string) error {
	log.Printf("[%s] Clicking button: %s (delay: %v)", h.Name(), buttonText, h.actionDelay)

	time.Sleep(h.actionDelay)

	ctx := context.Background()
	_, err := tghelper.RetryTelegram(ctx, "click_button", func() (*telegram.NewMessage, error) {
		return h.tgClient.SendMessage(h.chatID, buttonText)
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

func (h *Handler) HandleAlbum(a *telegram.Album) error {
	if h.state.IsStopped() {
		return nil
	}

	if h.isPaused() {
		return nil
	}

	if !h.state.TryStartProcessing() {
		log.Printf("[%s] Already processing, skipping album", h.Name())
		return nil
	}
	defer h.state.StopProcessing()

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

func (h *Handler) downloadAlbumData(ctx context.Context, a *telegram.Album) (ProfileData, func()) {
	var data ProfileData
	var photoPaths []string

	for _, msg := range a.Messages {
		if msg.Photo() != nil {
			path, err := tghelper.RetryTelegram(ctx, "download_album_photo", func() (string, error) {
				return msg.Download()
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
