package dating

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/0FL01/tg-dating-agent/internal/storage"
)

const (
	profileDedupeObjectKeyPrefix   = "profile-dedupe"
	profileDedupeObjectContentType = "application/json"
)

type ProfileDedupeStore struct {
	store storage.ObjectStore
	ttl   time.Duration
	now   func() time.Time
}

type profileDedupeRecord struct {
	ProfileHash string    `json:"profile_hash"`
	ProcessedAt time.Time `json:"processed_at"`
}

func NewProfileDedupeStore(store storage.ObjectStore, ttl time.Duration) *ProfileDedupeStore {
	return &ProfileDedupeStore{
		store: store,
		ttl:   ttl,
		now:   time.Now,
	}
}

func (s *ProfileDedupeStore) IsActive(ctx context.Context, profileHash string) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("profile dedupe store is nil")
	}
	if s.store == nil {
		return false, fmt.Errorf("object store is nil")
	}

	normalizedHash, err := normalizeProfileHash(profileHash)
	if err != nil {
		return false, err
	}

	if s.ttl <= 0 {
		return false, nil
	}

	body, err := s.store.GetObject(ctx, profileDedupeObjectKey(normalizedHash))
	if err != nil {
		if errors.Is(err, storage.ErrObjectNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("get profile dedupe record %q: %w", normalizedHash, err)
	}
	defer body.Close()

	var record profileDedupeRecord
	if err := json.NewDecoder(body).Decode(&record); err != nil {
		return false, fmt.Errorf("decode profile dedupe record %q: %w", normalizedHash, err)
	}

	if record.ProcessedAt.IsZero() {
		return false, nil
	}

	expiresAt := record.ProcessedAt.Add(s.ttl)
	return s.now().Before(expiresAt), nil
}

func (s *ProfileDedupeStore) MarkProcessed(ctx context.Context, profileHash string) error {
	if s == nil {
		return fmt.Errorf("profile dedupe store is nil")
	}
	if s.store == nil {
		return fmt.Errorf("object store is nil")
	}

	normalizedHash, err := normalizeProfileHash(profileHash)
	if err != nil {
		return err
	}

	record := profileDedupeRecord{
		ProfileHash: normalizedHash,
		ProcessedAt: s.now().UTC(),
	}

	body, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal profile dedupe record %q: %w", normalizedHash, err)
	}

	if err := s.store.PutObject(ctx, profileDedupeObjectKey(normalizedHash), bytes.NewReader(body), profileDedupeObjectContentType); err != nil {
		return fmt.Errorf("persist profile dedupe record %q: %w", normalizedHash, err)
	}

	return nil
}

func profileDedupeObjectKey(profileHash string) string {
	return profileDedupeObjectKeyPrefix + "/" + profileHash + ".json"
}

func normalizeProfileHash(profileHash string) (string, error) {
	normalized := strings.TrimSpace(profileHash)
	if normalized == "" {
		return "", fmt.Errorf("profile hash is required")
	}

	return normalized, nil
}
