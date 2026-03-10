package forwarder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type TelegramSender struct {
	sendMessageURL string
	targetChatID   int64
	httpClient     *http.Client
}

type sendMessageRequest struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}

func NewTelegramSender(cfg *Config) (*TelegramSender, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate forwarder config: %w", err)
	}

	baseURL := strings.TrimRight(strings.TrimSpace(cfg.TelegramAPIBaseURL), "/")
	sendMessageURL := fmt.Sprintf("%s/bot%s/sendMessage", baseURL, strings.TrimSpace(cfg.BotToken))

	return &TelegramSender{
		sendMessageURL: sendMessageURL,
		targetChatID:   cfg.TargetChatID,
		httpClient: &http.Client{
			Timeout: cfg.HTTPTimeout,
		},
	}, nil
}

func (s *TelegramSender) SendMessage(ctx context.Context, text string) error {
	if s == nil {
		return fmt.Errorf("telegram sender is nil")
	}

	body, err := json.Marshal(sendMessageRequest{
		ChatID: s.targetChatID,
		Text:   text,
	})
	if err != nil {
		return fmt.Errorf("marshal sendMessage payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.sendMessageURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build sendMessage request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send sendMessage request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}

	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if readErr != nil {
		return fmt.Errorf("sendMessage returned status %d and body read failed: %w", resp.StatusCode, readErr)
	}

	return fmt.Errorf("sendMessage returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
}
