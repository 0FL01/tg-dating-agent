package storage

import (
	"context"
	"errors"
	"io"
)

var ErrObjectNotFound = errors.New("storage: object not found")

type ObjectStore interface {
	PutObject(ctx context.Context, key string, body io.Reader, contentType string) error
	GetObject(ctx context.Context, key string) (io.ReadCloser, error)
}
