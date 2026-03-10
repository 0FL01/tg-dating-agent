package dating

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"

	"github.com/0FL01/tg-dating-agent/internal/standalone"
)

const datingInstanceHeader = "X-Dating-Instance-Name"

type ReciprocalLikeFinalWebhookClient struct {
	url          string
	token        string
	instanceName string
	httpClient   *http.Client
}

func NewReciprocalLikeFinalWebhookClient(cfg *standalone.Config) (*ReciprocalLikeFinalWebhookClient, error) {
	if cfg == nil {
		return nil, fmt.Errorf("webhook config is nil")
	}

	webhookURL := strings.TrimSpace(cfg.DatingMatchWebhookURL)
	if webhookURL == "" {
		return nil, nil
	}

	if _, err := url.ParseRequestURI(webhookURL); err != nil {
		return nil, fmt.Errorf("invalid DATING_MATCH_WEBHOOK_URL %q: %w", webhookURL, err)
	}

	timeout := cfg.DatingMatchWebhookTimeout
	if timeout <= 0 {
		timeout = standalone.DefaultDatingMatchWebhookTimeout
	}

	return &ReciprocalLikeFinalWebhookClient{
		url:          webhookURL,
		token:        strings.TrimSpace(cfg.DatingMatchWebhookToken),
		instanceName: strings.TrimSpace(cfg.DatingInstanceName),
		httpClient:   &http.Client{Timeout: timeout},
	}, nil
}

func (c *ReciprocalLikeFinalWebhookClient) DeliverReciprocalLikeFinal(ctx context.Context, payload ReciprocalLikeFinalPayload, photos []ReciprocalLikePhoto) error {
	if c == nil {
		return fmt.Errorf("webhook client is nil")
	}

	var (
		body        []byte
		contentType string
		err         error
	)

	if len(photos) > 0 {
		body, contentType, err = marshalReciprocalLikeFinalMultipartBody(payload, photos)
		if err != nil {
			return err
		}
	} else {
		body, err = json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal reciprocal-like payload: %w", err)
		}
		contentType = "application/json"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build webhook request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.instanceName != "" {
		req.Header.Set(datingInstanceHeader, c.instanceName)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send webhook request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		return nil
	}

	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if readErr != nil {
		return fmt.Errorf("webhook returned status %d and body read failed: %w", resp.StatusCode, readErr)
	}

	return fmt.Errorf("webhook returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
}

func marshalReciprocalLikeFinalMultipartBody(payload ReciprocalLikeFinalPayload, photos []ReciprocalLikePhoto) ([]byte, string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, "", fmt.Errorf("marshal reciprocal-like payload: %w", err)
	}
	if err := writer.WriteField("payload", string(payloadBytes)); err != nil {
		return nil, "", fmt.Errorf("write multipart payload field: %w", err)
	}

	added := 0
	for idx, photo := range photos {
		if added >= maxReciprocalLikePhotos {
			break
		}
		if len(photo.Data) == 0 {
			continue
		}

		fileName := strings.TrimSpace(photo.FileName)
		if fileName == "" {
			fileName = fmt.Sprintf("photo_%02d.jpg", idx+1)
		}

		contentType := strings.TrimSpace(photo.ContentType)
		if contentType == "" {
			contentType = http.DetectContentType(photo.Data)
		}

		headers := make(textproto.MIMEHeader)
		headers.Set("Content-Disposition", fmt.Sprintf(`form-data; name="photos"; filename=%q`, fileName))
		headers.Set("Content-Type", contentType)

		part, err := writer.CreatePart(headers)
		if err != nil {
			return nil, "", fmt.Errorf("create multipart photo part: %w", err)
		}
		if _, err := part.Write(photo.Data); err != nil {
			return nil, "", fmt.Errorf("write multipart photo part: %w", err)
		}
		added++
	}

	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("close multipart writer: %w", err)
	}

	return body.Bytes(), writer.FormDataContentType(), nil
}
