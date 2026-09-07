package blob

import (
	"context"
	"errors"
	"io"
)

var (
	ErrNotFound   = errors.New("blob: not found")
	ErrInvalidKey = errors.New("blob: invalid key")
)

// Store holds the binary content the database only records metadata for. Keys are
// derived from a content hash by the caller, never from a user supplied path.
type Store interface {
	Put(ctx context.Context, key string, content []byte) error
	Open(ctx context.Context, key string) (io.ReadSeekCloser, int64, error)
	Delete(ctx context.Context, key string) error
}
