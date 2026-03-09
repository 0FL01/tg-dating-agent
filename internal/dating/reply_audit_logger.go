package dating

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type ReplyAuditLogger struct {
	path string
	mu   sync.Mutex
}

type replyAuditRecord struct {
	Timestamp string `json:"timestamp"`
	MBTI      string `json:"mbti"`
	Prompt    string `json:"prompt"`
	Response  string `json:"response"`
}

func NewReplyAuditLogger(path string) *ReplyAuditLogger {
	return &ReplyAuditLogger{path: path}
}

func (l *ReplyAuditLogger) Append(mbti, prompt, response string) error {
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

	rec := replyAuditRecord{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		MBTI:      mbti,
		Prompt:    prompt,
		Response:  response,
	}

	if err := json.NewEncoder(f).Encode(rec); err != nil {
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
