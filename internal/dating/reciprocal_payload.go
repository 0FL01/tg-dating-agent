package dating

import (
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/amarnathcjd/gogram/telegram"
)

const reciprocalLikeFinalEventType = "reciprocal_like_final"

var telegramContactURLPattern = regexp.MustCompile(`(?i)(https?://)?(?:www\.)?(?:t\.me|telegram\.me)/[^\s]+`)

type ReciprocalLikeFinalPayload struct {
	EventType         string
	RawContactURL     string
	ContactUsername   string
	DeeplinkText      string
	ProfileText       string
	OpenerText        string
	MBTI              string
	ContextCapturedAt time.Time
	EventTimestamp    time.Time
}

func isStartChattingMessage(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	if normalized == "" {
		return false
	}

	return strings.Contains(normalized, PatternStartChatting)
}

func extractTelegramContactURL(m *telegram.NewMessage) (string, bool) {
	if m == nil {
		return "", false
	}

	if m.Message != nil {
		for _, entity := range m.Message.Entities {
			textURL, ok := entity.(*telegram.MessageEntityTextURL)
			if !ok {
				continue
			}

			rawURL := sanitizeURLCandidate(textURL.URL)
			if rawURL == "" {
				continue
			}

			if _, ok := parseTelegramContactURL(rawURL); ok {
				return rawURL, true
			}
		}
	}

	match := telegramContactURLPattern.FindString(m.Text())
	if match == "" {
		return "", false
	}

	rawURL := sanitizeURLCandidate(match)
	if rawURL == "" {
		return "", false
	}

	if _, ok := parseTelegramContactURL(rawURL); !ok {
		return "", false
	}

	return rawURL, true
}

func parseTelegramContactURL(rawURL string) (*url.URL, bool) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil, false
	}

	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, false
	}

	host := strings.ToLower(strings.TrimPrefix(parsed.Hostname(), "www."))
	if host != "t.me" && host != "telegram.me" {
		return nil, false
	}

	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) == 0 || segments[0] == "" {
		return nil, false
	}

	return parsed, true
}

func buildReciprocalLikeFinalPayload(m *telegram.NewMessage, latest RecentReciprocalLikeContext, hasContext bool, now time.Time) (ReciprocalLikeFinalPayload, bool) {
	if m == nil || !isStartChattingMessage(m.Text()) {
		return ReciprocalLikeFinalPayload{}, false
	}

	rawURL, ok := extractTelegramContactURL(m)
	if !ok {
		return ReciprocalLikeFinalPayload{}, false
	}

	parsedURL, ok := parseTelegramContactURL(rawURL)
	if !ok {
		return ReciprocalLikeFinalPayload{}, false
	}

	payload := ReciprocalLikeFinalPayload{
		EventType:      reciprocalLikeFinalEventType,
		RawContactURL:  rawURL,
		EventTimestamp: eventTimestampFromMessage(m, now),
	}

	payload.ContactUsername = strings.TrimPrefix(firstPathSegment(parsedURL.Path), "@")
	payload.DeeplinkText = parsedURL.Query().Get("text")

	if hasContext {
		payload.ProfileText = latest.ProfileText
		payload.OpenerText = latest.OpenerText
		payload.MBTI = latest.MBTI
		payload.ContextCapturedAt = latest.CapturedAt
	}

	return payload, true
}

func (h *Handler) BuildReciprocalLikeFinalPayload(m *telegram.NewMessage, now time.Time) (ReciprocalLikeFinalPayload, bool) {
	if h == nil || h.state == nil {
		return ReciprocalLikeFinalPayload{}, false
	}

	latest, hasContext := h.state.GetLatestReciprocalLikeContext(now)
	return buildReciprocalLikeFinalPayload(m, latest, hasContext, now)
}

func eventTimestampFromMessage(m *telegram.NewMessage, fallback time.Time) time.Time {
	if m != nil {
		if unixDate := m.Date(); unixDate > 0 {
			return time.Unix(int64(unixDate), 0)
		}
	}

	if fallback.IsZero() {
		return time.Now()
	}

	return fallback
}

func firstPathSegment(path string) string {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) == 0 {
		return ""
	}
	return segments[0]
}

func sanitizeURLCandidate(candidate string) string {
	return strings.TrimRight(strings.TrimSpace(candidate), ").,!?:;]}'\"")
}
