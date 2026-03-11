package dating

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/0FL01/tg-dating-agent/internal/storage"
)

func TestProfileDedupeStoreIsActiveHit(t *testing.T) {
	now := time.Date(2026, time.March, 11, 12, 0, 0, 0, time.UTC)
	ttl := 24 * time.Hour
	hash := "hash-active"

	record := profileDedupeRecord{
		ProfileHash: hash,
		ProcessedAt: now.Add(-12 * time.Hour),
	}
	recordBody, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("json.Marshal(record) error = %v", err)
	}

	fakeStore := &fakeProfileDedupeObjectStore{
		objects: map[string][]byte{
			profileDedupeObjectKey(hash): recordBody,
		},
	}

	store := NewProfileDedupeStore(fakeStore, ttl)
	store.now = func() time.Time { return now }

	active, err := store.IsActive(context.Background(), hash)
	if err != nil {
		t.Fatalf("IsActive() error = %v", err)
	}
	if !active {
		t.Fatal("IsActive() = false, want true")
	}
}

func TestProfileDedupeStoreIsActiveExpiredMiss(t *testing.T) {
	now := time.Date(2026, time.March, 11, 12, 0, 0, 0, time.UTC)
	ttl := 24 * time.Hour
	hash := "hash-expired"

	record := profileDedupeRecord{
		ProfileHash: hash,
		ProcessedAt: now.Add(-25 * time.Hour),
	}
	recordBody, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("json.Marshal(record) error = %v", err)
	}

	fakeStore := &fakeProfileDedupeObjectStore{
		objects: map[string][]byte{
			profileDedupeObjectKey(hash): recordBody,
		},
	}

	store := NewProfileDedupeStore(fakeStore, ttl)
	store.now = func() time.Time { return now }

	active, err := store.IsActive(context.Background(), hash)
	if err != nil {
		t.Fatalf("IsActive() error = %v", err)
	}
	if active {
		t.Fatal("IsActive() = true, want false")
	}
}

func TestProfileDedupeStoreIsActiveNotFound(t *testing.T) {
	store := NewProfileDedupeStore(&fakeProfileDedupeObjectStore{}, 24*time.Hour)

	active, err := store.IsActive(context.Background(), "missing-hash")
	if err != nil {
		t.Fatalf("IsActive() error = %v, want nil", err)
	}
	if active {
		t.Fatal("IsActive() = true, want false for missing record")
	}
}

func TestProfileDedupeStoreMarkProcessedPersistsPayloadAndKeyShape(t *testing.T) {
	now := time.Date(2026, time.March, 11, 12, 34, 56, 0, time.UTC)
	hash := "hash-shape"
	fakeStore := &fakeProfileDedupeObjectStore{}

	store := NewProfileDedupeStore(fakeStore, 48*time.Hour)
	store.now = func() time.Time { return now }

	if err := store.MarkProcessed(context.Background(), hash); err != nil {
		t.Fatalf("MarkProcessed() error = %v", err)
	}

	if len(fakeStore.putCalls) != 1 {
		t.Fatalf("put calls = %d, want 1", len(fakeStore.putCalls))
	}

	call := fakeStore.putCalls[0]
	if call.key != "profile-dedupe/hash-shape.json" {
		t.Fatalf("put key = %q, want %q", call.key, "profile-dedupe/hash-shape.json")
	}
	if call.contentType != "application/json" {
		t.Fatalf("put contentType = %q, want %q", call.contentType, "application/json")
	}

	var record profileDedupeRecord
	if err := json.Unmarshal(call.body, &record); err != nil {
		t.Fatalf("json.Unmarshal(put body) error = %v", err)
	}
	if record.ProfileHash != hash {
		t.Fatalf("record profile_hash = %q, want %q", record.ProfileHash, hash)
	}
	if !record.ProcessedAt.Equal(now) {
		t.Fatalf("record processed_at = %v, want %v", record.ProcessedAt, now)
	}
}

type fakeProfileDedupeObjectStore struct {
	objects  map[string][]byte
	putCalls []fakeProfileDedupePutCall
	getErr   error
}

type fakeProfileDedupePutCall struct {
	key         string
	body        []byte
	contentType string
}

func (f *fakeProfileDedupeObjectStore) PutObject(_ context.Context, key string, body io.Reader, contentType string) error {
	bodyBytes, err := io.ReadAll(body)
	if err != nil {
		return err
	}

	f.putCalls = append(f.putCalls, fakeProfileDedupePutCall{
		key:         key,
		body:        bodyBytes,
		contentType: contentType,
	})

	if f.objects == nil {
		f.objects = make(map[string][]byte)
	}
	f.objects[key] = append([]byte(nil), bodyBytes...)

	return nil
}

func (f *fakeProfileDedupeObjectStore) GetObject(_ context.Context, key string) (io.ReadCloser, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}

	obj, ok := f.objects[key]
	if !ok {
		return nil, storage.ErrObjectNotFound
	}

	return io.NopCloser(bytes.NewReader(append([]byte(nil), obj...))), nil
}

var _ storage.ObjectStore = (*fakeProfileDedupeObjectStore)(nil)

func TestProfileDedupeStoreIsActivePropagatesUnexpectedGetError(t *testing.T) {
	wantErr := errors.New("boom")
	store := NewProfileDedupeStore(&fakeProfileDedupeObjectStore{getErr: wantErr}, 24*time.Hour)

	_, err := store.IsActive(context.Background(), "hash")
	if err == nil {
		t.Fatal("IsActive() error = nil, want non-nil")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("IsActive() error = %v, want wrapped %v", err, wantErr)
	}
}
