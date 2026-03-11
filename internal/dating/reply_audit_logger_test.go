package dating

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type stubReplyAuditObjectStore struct {
	mu          sync.Mutex
	putCalls    []stubPutCall
	putErr      error
	putContexts []context.Context
}

type stubPutCall struct {
	key         string
	contentType string
	body        []byte
}

func (s *stubReplyAuditObjectStore) PutObject(ctx context.Context, key string, body io.Reader, contentType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	payload, err := io.ReadAll(body)
	if err != nil {
		return err
	}

	s.putCalls = append(s.putCalls, stubPutCall{key: key, contentType: contentType, body: payload})
	s.putContexts = append(s.putContexts, ctx)

	if s.putErr != nil {
		return s.putErr
	}

	return nil
}

func (s *stubReplyAuditObjectStore) GetObject(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (s *stubReplyAuditObjectStore) snapshotCalls() []stubPutCall {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]stubPutCall, len(s.putCalls))
	copy(out, s.putCalls)
	return out
}

func TestReplyAuditLoggerAppendWritesValidJSONLine(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "audit", "reply.jsonl")
	logger := NewReplyAuditLogger(logPath)

	if err := logger.Append("INTJ", "Alice - bio", "hello", "hi there"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("line count = %d, want 1", len(lines))
	}

	var rec replyAuditRecord
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if rec.MBTI != "INTJ" {
		t.Fatalf("mbti = %q, want %q", rec.MBTI, "INTJ")
	}
	if rec.ProfileText != "Alice - bio" {
		t.Fatalf("profile_text = %q, want %q", rec.ProfileText, "Alice - bio")
	}
	if rec.Prompt != "hello" {
		t.Fatalf("prompt = %q, want %q", rec.Prompt, "hello")
	}
	if rec.Response != "hi there" {
		t.Fatalf("response = %q, want %q", rec.Response, "hi there")
	}
	if rec.Timestamp == "" {
		t.Fatal("timestamp is empty")
	}
	if !strings.HasSuffix(rec.Timestamp, "Z") {
		t.Fatalf("timestamp = %q, want UTC RFC3339Nano string", rec.Timestamp)
	}
	if _, err := time.Parse(time.RFC3339Nano, rec.Timestamp); err != nil {
		t.Fatalf("timestamp parse error = %v", err)
	}
}

func TestReplyAuditLoggerAppendAppendsMultipleEntries(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "reply.jsonl")
	logger := NewReplyAuditLogger(logPath)

	if err := logger.Append("INTJ", "bio 1", "prompt 1", "response 1"); err != nil {
		t.Fatalf("first Append() error = %v", err)
	}
	if err := logger.Append("INFJ", "bio 2", "prompt 2", "response 2"); err != nil {
		t.Fatalf("second Append() error = %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("line count = %d, want 2", len(lines))
	}

	var first replyAuditRecord
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("first line unmarshal error = %v", err)
	}
	if first.MBTI != "INTJ" || first.ProfileText != "bio 1" || first.Prompt != "prompt 1" || first.Response != "response 1" {
		t.Fatalf("first record = %+v, want mbti/profile_text/prompt/response for first append", first)
	}

	var second replyAuditRecord
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("second line unmarshal error = %v", err)
	}
	if second.MBTI != "INFJ" || second.ProfileText != "bio 2" || second.Prompt != "prompt 2" || second.Response != "response 2" {
		t.Fatalf("second record = %+v, want mbti/profile_text/prompt/response for second append", second)
	}
}

func TestReplyAuditLoggerAppendReturnsErrorOnOpenFailure(t *testing.T) {
	dirPath := t.TempDir()
	logger := NewReplyAuditLogger(dirPath)

	if err := logger.Append("INTJ", "bio", "prompt", "response"); err == nil {
		t.Fatal("Append() error = nil, want non-nil")
	}
}

func TestReplyAuditLoggerAppendConcurrentWrites(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "audit", "reply.jsonl")
	logger := NewReplyAuditLogger(logPath)

	const workers = 100
	var wg sync.WaitGroup
	errCh := make(chan error, workers)

	for i := 0; i < workers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := logger.Append("INTJ", "bio "+strconv.Itoa(i), "prompt "+strconv.Itoa(i), "response"); err != nil {
				errCh <- err
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("Append() concurrent error = %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != workers {
		t.Fatalf("line count = %d, want %d", len(lines), workers)
	}

	for idx, line := range lines {
		var rec replyAuditRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %d unmarshal error = %v", idx, err)
		}
		if rec.MBTI != "INTJ" || !strings.HasPrefix(rec.ProfileText, "bio ") || rec.Response != "response" {
			t.Fatalf("line %d record = %+v, want mbti INTJ, profile_text with bio prefix, and response response", idx, rec)
		}
	}
}

func TestReplyAuditR2AppenderAppendWritesJSONLChunk(t *testing.T) {
	store := &stubReplyAuditObjectStore{}
	appender := NewReplyAuditR2Appender(store)
	appender.now = func() time.Time {
		return time.Date(2026, time.March, 11, 9, 10, 11, 123456789, time.UTC)
	}

	if err := appender.Append("INTJ", "Alice bio", "prompt", "hello"); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	calls := store.snapshotCalls()
	if len(calls) != 1 {
		t.Fatalf("put call count = %d, want 1", len(calls))
	}

	if calls[0].contentType != replyAuditR2ObjectContentType {
		t.Fatalf("content type = %q, want %q", calls[0].contentType, replyAuditR2ObjectContentType)
	}

	if !strings.HasPrefix(calls[0].key, "audit/replies/2026/03/11/") {
		t.Fatalf("key = %q, want prefix %q", calls[0].key, "audit/replies/2026/03/11/")
	}
	if !strings.HasSuffix(calls[0].key, "-000001.jsonl") {
		t.Fatalf("key = %q, want suffix %q", calls[0].key, "-000001.jsonl")
	}

	if !strings.HasSuffix(string(calls[0].body), "\n") {
		t.Fatalf("payload = %q, want trailing newline", string(calls[0].body))
	}

	var rec replyAuditRecord
	if err := json.Unmarshal(bytes.TrimSpace(calls[0].body), &rec); err != nil {
		t.Fatalf("payload unmarshal error = %v", err)
	}

	if rec.Timestamp != "2026-03-11T09:10:11.123456789Z" {
		t.Fatalf("timestamp = %q, want %q", rec.Timestamp, "2026-03-11T09:10:11.123456789Z")
	}
	if rec.MBTI != "INTJ" || rec.ProfileText != "Alice bio" || rec.Prompt != "prompt" || rec.Response != "hello" {
		t.Fatalf("record = %+v, want expected payload fields", rec)
	}
}

func TestReplyAuditR2AppenderAppendGeneratesUniqueKeys(t *testing.T) {
	store := &stubReplyAuditObjectStore{}
	appender := NewReplyAuditR2Appender(store)
	appender.now = func() time.Time {
		return time.Date(2026, time.March, 11, 9, 10, 11, 123456789, time.UTC)
	}

	if err := appender.Append("INTJ", "bio1", "p1", "r1"); err != nil {
		t.Fatalf("first Append() error = %v", err)
	}
	if err := appender.Append("INFJ", "bio2", "p2", "r2"); err != nil {
		t.Fatalf("second Append() error = %v", err)
	}

	calls := store.snapshotCalls()
	if len(calls) != 2 {
		t.Fatalf("put call count = %d, want 2", len(calls))
	}
	if calls[0].key == calls[1].key {
		t.Fatalf("keys are equal = %q, want unique keys", calls[0].key)
	}
	if !strings.HasSuffix(calls[1].key, "-000002.jsonl") {
		t.Fatalf("second key = %q, want suffix %q", calls[1].key, "-000002.jsonl")
	}
}

type compositeStubReplyAuditAppender struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (s *compositeStubReplyAuditAppender) Append(_, _, _, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls++
	return s.err
}

func (s *compositeStubReplyAuditAppender) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestCompositeReplyAuditAppenderAppendAttemptsAllAppenders(t *testing.T) {
	first := &compositeStubReplyAuditAppender{}
	second := &compositeStubReplyAuditAppender{err: errors.New("r2 failed")}
	third := &compositeStubReplyAuditAppender{}

	composite := NewCompositeReplyAuditAppender(first, second, third)
	if composite == nil {
		t.Fatal("NewCompositeReplyAuditAppender() = nil, want non-nil")
	}

	err := composite.Append("INTJ", "bio", "prompt", "response")
	if err == nil {
		t.Fatal("Append() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "r2 failed") {
		t.Fatalf("Append() error = %v, want contains %q", err, "r2 failed")
	}

	if first.callCount() != 1 {
		t.Fatalf("first call count = %d, want 1", first.callCount())
	}
	if second.callCount() != 1 {
		t.Fatalf("second call count = %d, want 1", second.callCount())
	}
	if third.callCount() != 1 {
		t.Fatalf("third call count = %d, want 1", third.callCount())
	}
}
