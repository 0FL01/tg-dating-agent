package dating

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0FL01/tg-dating-agent/internal/llm"
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

	if err := logger.Append(replyAuditRecord{Event: "decision", Decision: llm.Decision{Action: "send", Reason: "fit", Message: "hi there"}, Model: "model", ProfileText: "Alice - bio", Prompt: "hello"}); err != nil {
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

	if rec.Event != "decision" || rec.Action != "send" {
		t.Fatalf("action = %q, want %q", rec.Action, "send")
	}
	if rec.Reason != "fit" || rec.Model != "model" {
		t.Fatalf("missing reason/model: %+v", rec)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["response"] != nil {
		t.Fatal("legacy response field emitted")
	}
	if rec.ProfileText != "Alice - bio" {
		t.Fatalf("profile_text = %q, want %q", rec.ProfileText, "Alice - bio")
	}
	if rec.Prompt != "hello" {
		t.Fatalf("prompt = %q, want %q", rec.Prompt, "hello")
	}
	if rec.Message != "hi there" {
		t.Fatalf("response = %q, want %q", rec.Message, "hi there")
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

	if err := logger.Append(replyAuditRecord{Event: "decision", Decision: llm.Decision{Action: "send", Reason: "fit", Message: "response 1"}, Model: "model", ProfileText: "bio 1", Prompt: "prompt 1"}); err != nil {
		t.Fatalf("first Append() error = %v", err)
	}
	if err := logger.Append(replyAuditRecord{Event: "decision", Decision: llm.Decision{Action: "skip", Reason: "no hook"}, Model: "model", ProfileText: "bio 2", Prompt: "prompt 2"}); err != nil {
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
	if first.Action != "send" || first.ProfileText != "bio 1" || first.Prompt != "prompt 1" || first.Message != "response 1" {
		t.Fatalf("first record = %+v, want action/profile_text/prompt/message for first append", first)
	}

	var second replyAuditRecord
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("second line unmarshal error = %v", err)
	}
	if second.Action != "skip" || second.Reason != "no hook" || second.Model != "model" || second.ProfileText != "bio 2" || second.Prompt != "prompt 2" || second.Message != "" {
		t.Fatalf("second record = %+v, want action/profile_text/prompt/message for second append", second)
	}
}

func TestReplyAuditLoggerAppendReturnsErrorOnOpenFailure(t *testing.T) {
	dirPath := t.TempDir()
	logger := NewReplyAuditLogger(dirPath)

	if err := logger.Append(replyAuditRecord{Event: "decision", Decision: llm.Decision{Action: "send", Reason: "fit", Message: "response"}, Model: "model", ProfileText: "bio", Prompt: "prompt"}); err == nil {
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
			if err := logger.Append(replyAuditRecord{Event: "decision", Decision: llm.Decision{Action: "send", Reason: "fit", Message: "response"}, Model: "model", ProfileText: "bio " + strconv.Itoa(i), Prompt: "prompt " + strconv.Itoa(i)}); err != nil {
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
		if rec.Action != "send" || !strings.HasPrefix(rec.ProfileText, "bio ") || rec.Message != "response" {
			t.Fatalf("line %d record = %+v, want send, profile_text with bio prefix, and message response", idx, rec)
		}
	}
}

func TestReplyAuditR2AppenderAppendWritesJSONLChunk(t *testing.T) {
	store := &stubReplyAuditObjectStore{}
	appender := NewReplyAuditR2Appender(store)
	appender.now = func() time.Time {
		return time.Date(2026, time.March, 11, 9, 10, 11, 123456789, time.UTC)
	}

	if err := appender.Append(replyAuditRecord{Event: "sent", Decision: llm.Decision{Action: "send", Reason: "fit", Message: "hello"}, Model: "model", ProfileText: "Alice bio", Prompt: "prompt"}); err != nil {
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
	if rec.Event != "sent" || rec.Action != "send" || rec.Reason != "fit" || rec.Model != "model" || rec.ProfileText != "Alice bio" || rec.Prompt != "prompt" || rec.Message != "hello" {
		t.Fatalf("record = %+v, want expected payload fields", rec)
	}
}

func TestReplyAuditR2AppenderAppendGeneratesUniqueKeys(t *testing.T) {
	store := &stubReplyAuditObjectStore{}
	appender := NewReplyAuditR2Appender(store)
	appender.now = func() time.Time {
		return time.Date(2026, time.March, 11, 9, 10, 11, 123456789, time.UTC)
	}

	if err := appender.Append(replyAuditRecord{Event: "decision", Decision: llm.Decision{Action: "send", Reason: "fit", Message: "r1"}, Model: "model", ProfileText: "bio1", Prompt: "p1"}); err != nil {
		t.Fatalf("first Append() error = %v", err)
	}
	if err := appender.Append(replyAuditRecord{Event: "sent", Decision: llm.Decision{Action: "send", Reason: "fit", Message: "r2"}, Model: "model", ProfileText: "bio2", Prompt: "p2"}); err != nil {
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

func (s *compositeStubReplyAuditAppender) Append(_ replyAuditRecord) error {
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

	err := composite.Append(replyAuditRecord{Event: "decision", Decision: llm.Decision{Action: "send", Reason: "fit", Message: "response"}, Model: "model", ProfileText: "bio", Prompt: "prompt"})
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

func TestAuditEventsPersistToLocalAndR2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	store := &stubReplyAuditObjectStore{}
	appender := NewCompositeReplyAuditAppender(NewReplyAuditLogger(path), NewReplyAuditR2Appender(store))
	for _, event := range []string{"decision", "invalid_response", "error", "sent"} {
		record := replyAuditRecord{Event: event, Model: "model", ProfileText: "bio", Prompt: "criteria"}
		if event == "decision" || event == "sent" {
			record.Decision = llm.Decision{Action: "send", Reason: "fit", Message: "hello"}
		} else {
			record.Error = "Request or validation failed"
		}
		if err := appender.Append(record); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	calls := store.snapshotCalls()
	if len(lines) != 4 || len(calls) != 4 {
		t.Fatalf("local=%d R2=%d", len(lines), len(calls))
	}
	for i, event := range []string{"decision", "invalid_response", "error", "sent"} {
		var local, remote replyAuditRecord
		if err := json.Unmarshal([]byte(lines[i]), &local); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(calls[i].body, &remote); err != nil {
			t.Fatal(err)
		}
		local.Timestamp, remote.Timestamp = "", ""
		if !reflect.DeepEqual(local, remote) || local.Event != event || local.Model != "model" {
			t.Fatalf("local=%+v remote=%+v", local, remote)
		}
		if (event == "error" || event == "invalid_response") && (local.Error == "" || local.Decision != (llm.Decision{})) {
			t.Fatalf("failure audit=%+v", local)
		}
	}
}
