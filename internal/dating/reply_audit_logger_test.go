package dating

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestReplyAuditLoggerAppendWritesValidJSONLine(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "audit", "reply.jsonl")
	logger := NewReplyAuditLogger(logPath)

	if err := logger.Append("INTJ", "hello", "hi there"); err != nil {
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

	if err := logger.Append("INTJ", "prompt 1", "response 1"); err != nil {
		t.Fatalf("first Append() error = %v", err)
	}
	if err := logger.Append("INFJ", "prompt 2", "response 2"); err != nil {
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
	if first.MBTI != "INTJ" || first.Prompt != "prompt 1" || first.Response != "response 1" {
		t.Fatalf("first record = %+v, want mbti/prompt/response for first append", first)
	}

	var second replyAuditRecord
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("second line unmarshal error = %v", err)
	}
	if second.MBTI != "INFJ" || second.Prompt != "prompt 2" || second.Response != "response 2" {
		t.Fatalf("second record = %+v, want mbti/prompt/response for second append", second)
	}
}

func TestReplyAuditLoggerAppendReturnsErrorOnOpenFailure(t *testing.T) {
	dirPath := t.TempDir()
	logger := NewReplyAuditLogger(dirPath)

	if err := logger.Append("INTJ", "prompt", "response"); err == nil {
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
			if err := logger.Append("INTJ", "prompt "+strconv.Itoa(i), "response"); err != nil {
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
		if rec.MBTI != "INTJ" || rec.Response != "response" {
			t.Fatalf("line %d record = %+v, want mbti INTJ and response response", idx, rec)
		}
	}
}
