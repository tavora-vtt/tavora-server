package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

const (
	fanOut         = 2
	directoryPerm  = 0o750
	filePerm       = 0o640
	minimumKeySize = 8
)

var keyPattern = regexp.MustCompile(`^[a-f0-9]{8,}(-[a-z0-9]{1,16})?$`)

// Disk stores blobs under a root directory, fanned out by the first bytes of the key so
// no single directory grows without bound.
type Disk struct {
	root string
}

func NewDisk(root string) (*Disk, error) {
	if err := os.MkdirAll(root, directoryPerm); err != nil {
		return nil, fmt.Errorf("blob: create root: %w", err)
	}
	return &Disk{root: root}, nil
}

func (d *Disk) path(key string) (string, error) {
	if len(key) < minimumKeySize || !keyPattern.MatchString(key) {
		return "", fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}
	return filepath.Join(d.root, key[:fanOut], key[fanOut:fanOut*2], key), nil
}

func (d *Disk) Put(ctx context.Context, key string, content []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	target, err := d.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), directoryPerm); err != nil {
		return fmt.Errorf("blob: create directory: %w", err)
	}

	temporary, err := os.CreateTemp(filepath.Dir(target), ".upload-*")
	if err != nil {
		return fmt.Errorf("blob: create temporary file: %w", err)
	}
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
	}()

	if _, err := temporary.Write(content); err != nil {
		return fmt.Errorf("blob: write: %w", err)
	}
	if err := temporary.Chmod(filePerm); err != nil {
		return fmt.Errorf("blob: set permissions: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("blob: sync: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("blob: close: %w", err)
	}
	if err := os.Rename(temporary.Name(), target); err != nil {
		return fmt.Errorf("blob: publish: %w", err)
	}
	return nil
}

func (d *Disk) Open(ctx context.Context, key string) (io.ReadSeekCloser, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}

	target, err := d.path(key)
	if err != nil {
		return nil, 0, err
	}

	file, err := os.Open(target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	if err != nil {
		return nil, 0, fmt.Errorf("blob: open: %w", err)
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, 0, fmt.Errorf("blob: stat: %w", err)
	}
	return file, info.Size(), nil
}

func (d *Disk) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	target, err := d.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("blob: delete: %w", err)
	}
	return nil
}
