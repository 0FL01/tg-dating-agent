package dating

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/0FL01/tg-dating-agent/internal/llm"
	"github.com/0FL01/tg-dating-agent/internal/storage"
)

const (
	replyAuditR2ObjectKeyPrefix   = "audit/replies"
	replyAuditR2ObjectContentType = "application/x-ndjson"
	defaultReplyAuditR2Timeout    = 5 * time.Second
)

type ReplyAuditLogger struct {
	path string
	mu   sync.Mutex
}

type replyAuditRecord struct {
	Timestamp string `json:"timestamp"`
	Event     string `json:"event"`
	Error     string `json:"error,omitempty"`
	llm.Decision
	Model       string `json:"model"`
	ProfileText string `json:"profile_text"`
	Prompt      string `json:"prompt"`
}

func NewReplyAuditLogger(path string) *ReplyAuditLogger {
	return &ReplyAuditLogger{path: path}
}

func (l *ReplyAuditLogger) Append(record replyAuditRecord) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if err := l.ensureDir(); err != nil {
		return err
	}

	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open reply audit log %q: %w", l.path, err)
	}
	defer f.Close()

	record.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)

	if err := json.NewEncoder(f).Encode(record); err != nil {
		return fmt.Errorf("encode reply audit record: %w", err)
	}

	return nil
}

func (l *ReplyAuditLogger) ensureDir() error {
	dir := filepath.Dir(l.path)
	if dir == "." || dir == "" {
		return nil
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create reply audit log directory %q: %w", dir, err)
	}

	return nil
}

type ReplyAuditR2Appender struct {
	store     storage.ObjectStore
	keyPrefix string
	now       func() time.Time
	timeout   time.Duration

	mu      sync.Mutex
	counter uint64
}

func NewReplyAuditR2Appender(store storage.ObjectStore) *ReplyAuditR2Appender {
	if store == nil {
		return nil
	}

	return &ReplyAuditR2Appender{
		store:     store,
		keyPrefix: replyAuditR2ObjectKeyPrefix,
		now:       time.Now,
		timeout:   defaultReplyAuditR2Timeout,
	}
}

func (a *ReplyAuditR2Appender) Append(record replyAuditRecord) error {
	if a == nil {
		return fmt.Errorf("reply audit r2 appender is nil")
	}

	record.Timestamp = a.now().UTC().Format(time.RFC3339Nano)

	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal reply audit record: %w", err)
	}
	payload = append(payload, '\n')

	ctx := context.Background()
	if a.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.timeout)
		defer cancel()
	}

	if err := a.store.PutObject(ctx, a.nextObjectKey(), bytes.NewReader(payload), replyAuditR2ObjectContentType); err != nil {
		return fmt.Errorf("persist reply audit record to r2: %w", err)
	}

	return nil
}

func (a *ReplyAuditR2Appender) nextObjectKey() string {
	now := a.now().UTC()

	a.mu.Lock()
	a.counter++
	counter := a.counter
	a.mu.Unlock()

	keyPrefix := strings.TrimSpace(a.keyPrefix)
	if keyPrefix == "" {
		keyPrefix = replyAuditR2ObjectKeyPrefix
	}

	return fmt.Sprintf("%s/%04d/%02d/%02d/%s-%06d.jsonl",
		strings.TrimSuffix(keyPrefix, "/"),
		now.Year(),
		now.Month(),
		now.Day(),
		now.Format("20060102T150405.000000000Z"),
		counter,
	)
}

type CompositeReplyAuditAppender struct {
	appenders []replyAuditAppender
}

func NewCompositeReplyAuditAppender(appenders ...replyAuditAppender) *CompositeReplyAuditAppender {
	filtered := make([]replyAuditAppender, 0, len(appenders))
	for _, appender := range appenders {
		if appender == nil {
			continue
		}
		filtered = append(filtered, appender)
	}

	if len(filtered) == 0 {
		return nil
	}

	return &CompositeReplyAuditAppender{appenders: filtered}
}

func (a *CompositeReplyAuditAppender) Append(record replyAuditRecord) error {
	if a == nil {
		return fmt.Errorf("composite reply audit appender is nil")
	}

	var errs []error
	for _, appender := range a.appenders {
		if err := appender.Append(record); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
