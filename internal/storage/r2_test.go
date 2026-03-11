package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewR2ObjectStoreValidation(t *testing.T) {
	valid := R2Config{
		Bucket:          "bucket",
		Endpoint:        "https://example.r2.cloudflarestorage.com",
		Region:          "auto",
		AccessKeyID:     "access",
		SecretAccessKey: "secret",
		InstanceName:    "instance-a",
	}

	tests := []struct {
		name string
		cfg  R2Config
	}{
		{name: "missing bucket", cfg: R2Config{Endpoint: valid.Endpoint, Region: valid.Region, AccessKeyID: valid.AccessKeyID, SecretAccessKey: valid.SecretAccessKey, InstanceName: valid.InstanceName}},
		{name: "missing endpoint", cfg: R2Config{Bucket: valid.Bucket, Region: valid.Region, AccessKeyID: valid.AccessKeyID, SecretAccessKey: valid.SecretAccessKey, InstanceName: valid.InstanceName}},
		{name: "invalid endpoint", cfg: R2Config{Bucket: valid.Bucket, Endpoint: "::://bad", Region: valid.Region, AccessKeyID: valid.AccessKeyID, SecretAccessKey: valid.SecretAccessKey, InstanceName: valid.InstanceName}},
		{name: "missing access key", cfg: R2Config{Bucket: valid.Bucket, Endpoint: valid.Endpoint, Region: valid.Region, SecretAccessKey: valid.SecretAccessKey, InstanceName: valid.InstanceName}},
		{name: "missing secret", cfg: R2Config{Bucket: valid.Bucket, Endpoint: valid.Endpoint, Region: valid.Region, AccessKeyID: valid.AccessKeyID, InstanceName: valid.InstanceName}},
		{name: "missing instance", cfg: R2Config{Bucket: valid.Bucket, Endpoint: valid.Endpoint, Region: valid.Region, AccessKeyID: valid.AccessKeyID, SecretAccessKey: valid.SecretAccessKey}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewR2ObjectStore(tc.cfg)
			if err == nil {
				t.Fatal("NewR2ObjectStore() error = nil, want error")
			}
		})
	}
}

func TestR2ObjectStorePutGetWithInstancePrefix(t *testing.T) {
	type object struct {
		body        []byte
		contentType string
	}

	store := make(map[string]object)
	var mu sync.Mutex

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bucket, key, ok := parsePathStylePath(r.URL.Path)
		if !ok {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		if bucket != "bucket" {
			http.Error(w, "unexpected bucket", http.StatusBadRequest)
			return
		}

		switch r.Method {
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read body", http.StatusInternalServerError)
				return
			}
			mu.Lock()
			store[key] = object{body: body, contentType: r.Header.Get("Content-Type")}
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			mu.Lock()
			obj, exists := store[key]
			mu.Unlock()
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>not found</Message></Error>`))
				return
			}
			w.Header().Set("Content-Type", obj.contentType)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(obj.body)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	client, err := NewR2ObjectStore(R2Config{
		Bucket:          "bucket",
		Endpoint:        server.URL,
		Region:          "auto",
		AccessKeyID:     "access",
		SecretAccessKey: "secret",
		InstanceName:    "prod-a",
		HTTPClient:      &http.Client{Timeout: 2 * time.Second},
	})
	if err != nil {
		t.Fatalf("NewR2ObjectStore() error = %v", err)
	}

	payload := []byte("hello-storage")
	err = client.PutObject(context.Background(), "audit/replies.jsonl", bytes.NewReader(payload), "text/plain")
	if err != nil {
		t.Fatalf("PutObject() error = %v", err)
	}

	mu.Lock()
	stored, ok := store["prod-a/audit/replies.jsonl"]
	mu.Unlock()
	if !ok {
		t.Fatal("expected key with instance prefix")
	}
	if !bytes.Equal(stored.body, payload) {
		t.Fatalf("stored body = %q, want %q", stored.body, payload)
	}
	if stored.contentType != "text/plain" {
		t.Fatalf("stored content type = %q, want text/plain", stored.contentType)
	}

	body, err := client.GetObject(context.Background(), "audit/replies.jsonl")
	if err != nil {
		t.Fatalf("GetObject() error = %v", err)
	}
	defer func() {
		_ = body.Close()
	}()

	readBack, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !bytes.Equal(readBack, payload) {
		t.Fatalf("read back body = %q, want %q", readBack, payload)
	}
}

func TestR2ObjectStoreGetObjectNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>not found</Message></Error>`))
	}))
	defer server.Close()

	client, err := NewR2ObjectStore(R2Config{
		Bucket:          "bucket",
		Endpoint:        server.URL,
		Region:          "auto",
		AccessKeyID:     "access",
		SecretAccessKey: "secret",
		InstanceName:    "prod-a",
		HTTPClient:      &http.Client{Timeout: 2 * time.Second},
	})
	if err != nil {
		t.Fatalf("NewR2ObjectStore() error = %v", err)
	}

	_, err = client.GetObject(context.Background(), "missing-key")
	if err == nil {
		t.Fatal("GetObject() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "missing-key") {
		t.Fatalf("GetObject() error = %v, want key mention", err)
	}
	if !strings.Contains(err.Error(), ErrObjectNotFound.Error()) {
		t.Fatalf("GetObject() error = %v, want not found mention", err)
	}
	if !isErrObjectNotFound(err) {
		t.Fatalf("GetObject() error = %v, want errors.Is(..., ErrObjectNotFound)", err)
	}
}

func isErrObjectNotFound(err error) bool {
	return errors.Is(err, ErrObjectNotFound)
}

func parsePathStylePath(path string) (string, string, bool) {
	trimmed := strings.TrimPrefix(path, "/")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	if strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", false
	}

	return parts[0], parts[1], true
}

func TestR2ObjectStoreRejectsEmptyKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewR2ObjectStore(R2Config{
		Bucket:          "bucket",
		Endpoint:        server.URL,
		Region:          "auto",
		AccessKeyID:     "access",
		SecretAccessKey: "secret",
		InstanceName:    "prod-a",
	})
	if err != nil {
		t.Fatalf("NewR2ObjectStore() error = %v", err)
	}

	err = client.PutObject(context.Background(), "  ", bytes.NewReader([]byte("x")), "text/plain")
	if err == nil || !strings.Contains(err.Error(), "object key is required") {
		t.Fatalf("PutObject() error = %v, want key validation error", err)
	}

	_, err = client.GetObject(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "object key is required") {
		t.Fatalf("GetObject() error = %v, want key validation error", err)
	}
}

func TestR2ObjectStoreDefaultsRegion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = fmt.Fprint(w, "unused")
	}))
	defer server.Close()

	_, err := NewR2ObjectStore(R2Config{
		Bucket:          "bucket",
		Endpoint:        server.URL,
		AccessKeyID:     "access",
		SecretAccessKey: "secret",
		InstanceName:    "prod-a",
	})
	if err != nil {
		t.Fatalf("NewR2ObjectStore() error = %v", err)
	}
}
