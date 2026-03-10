package forwarder

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"strings"
)

type TelegramSender struct {
	sendMessageURL string
	sendPhotoURL   string
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
	sendPhotoURL := fmt.Sprintf("%s/bot%s/sendPhoto", baseURL, strings.TrimSpace(cfg.BotToken))

	return &TelegramSender{
		sendMessageURL: sendMessageURL,
		sendPhotoURL:   sendPhotoURL,
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

	return checkTelegramStatus(resp, "sendMessage")
}

func (s *TelegramSender) SendPhotos(ctx context.Context, photos []Photo) error {
	if s == nil {
		return fmt.Errorf("telegram sender is nil")
	}

	for _, photo := range photos {
		if err := s.sendPhoto(ctx, photo); err != nil {
			return err
		}
	}

	return nil
}

func (s *TelegramSender) sendPhoto(ctx context.Context, photo Photo) error {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	if err := writer.WriteField("chat_id", fmt.Sprintf("%d", s.targetChatID)); err != nil {
		return fmt.Errorf("build sendPhoto payload chat_id: %w", err)
	}

	filename := strings.TrimSpace(photo.Filename)
	if filename == "" {
		filename = "photo.jpg"
	} else {
		filename = filepath.Base(filename)
	}

	contentType := strings.TrimSpace(photo.ContentType)
	if contentType == "" {
		contentType = mime.TypeByExtension(strings.ToLower(filepath.Ext(filename)))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	photoHeader := make(textproto.MIMEHeader)
	photoHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="photo"; filename=%q`, filename))
	photoHeader.Set("Content-Type", contentType)

	part, err := writer.CreatePart(photoHeader)
	if err != nil {
		return fmt.Errorf("build sendPhoto payload photo part: %w", err)
	}
	if _, err := part.Write(photo.Data); err != nil {
		return fmt.Errorf("build sendPhoto payload photo bytes: %w", err)
	}

	if err := writer.Close(); err != nil {
		return fmt.Errorf("finalize sendPhoto payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.sendPhotoURL, body)
	if err != nil {
		return fmt.Errorf("build sendPhoto request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send sendPhoto request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	return checkTelegramStatus(resp, "sendPhoto")
}

func checkTelegramStatus(resp *http.Response, endpoint string) error {
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}

	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if readErr != nil {
		return fmt.Errorf("%s returned status %d and body read failed: %w", endpoint, resp.StatusCode, readErr)
	}

	return fmt.Errorf("%s returned status %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(respBody)))
}
