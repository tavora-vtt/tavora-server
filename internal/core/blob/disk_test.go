package blob_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tavora-vtt/tavora-server/internal/core/blob"
)

const sample = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"

func newDisk(t *testing.T) (*blob.Disk, string) {
	t.Helper()

	root := filepath.Join(t.TempDir(), "blobs")
	store, err := blob.NewDisk(root)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return store, root
}

func TestRoundTrip(t *testing.T) {
	store, _ := newDisk(t)
	ctx := context.Background()

	content := []byte("the chantry map")
	if err := store.Put(ctx, sample, content); err != nil {
		t.Fatalf("put: %v", err)
	}

	reader, size, err := store.Open(ctx, sample)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer reader.Close()

	if size != int64(len(content)) {
		t.Errorf("size is %d, want %d", size, len(content))
	}

	read, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(read) != string(content) {
		t.Errorf("read back %q", read)
	}
}

func TestKeysAreFannedOutIntoDirectories(t *testing.T) {
	store, root := newDisk(t)

	if err := store.Put(context.Background(), sample, []byte("x")); err != nil {
		t.Fatalf("put: %v", err)
	}

	expected := filepath.Join(root, sample[:2], sample[2:4], sample)
	if _, err := os.Stat(expected); err != nil {
		t.Errorf("nothing at %s: %v", expected, err)
	}
}

func TestKeysThatCouldEscapeTheRootAreRefused(t *testing.T) {
	store, root := newDisk(t)
	ctx := context.Background()

	for _, key := range []string{
		"../../etc/passwd",
		"a1b2c3d4/../../../etc/passwd",
		"/etc/passwd",
		"a1b2c3d4e5f6/nested",
		"short",
		"",
		"A1B2C3D4E5F6",
		"a1b2c3d4e5f6\x00",
	} {
		if err := store.Put(ctx, key, []byte("x")); !errors.Is(err, blob.ErrInvalidKey) {
			t.Errorf("put accepted %q: %v", key, err)
		}
		if _, _, err := store.Open(ctx, key); !errors.Is(err, blob.ErrInvalidKey) {
			t.Errorf("open accepted %q: %v", key, err)
		}
	}

	var found []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			found = append(found, path)
		}
		return nil
	})
	if len(found) != 0 {
		t.Errorf("refused keys still wrote files: %s", strings.Join(found, ", "))
	}
}

func TestVariantKeysAreAccepted(t *testing.T) {
	store, _ := newDisk(t)

	if err := store.Put(context.Background(), sample+"-thumb", []byte("x")); err != nil {
		t.Errorf("put refused a variant key: %v", err)
	}
}

func TestReadingWhatWasNeverWritten(t *testing.T) {
	store, _ := newDisk(t)

	if _, _, err := store.Open(context.Background(), sample); !errors.Is(err, blob.ErrNotFound) {
		t.Errorf("open reported %v, want not found", err)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	store, _ := newDisk(t)
	ctx := context.Background()

	if err := store.Put(ctx, sample, []byte("x")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := store.Delete(ctx, sample); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.Delete(ctx, sample); err != nil {
		t.Errorf("deleting twice failed: %v", err)
	}
	if _, _, err := store.Open(ctx, sample); !errors.Is(err, blob.ErrNotFound) {
		t.Errorf("the blob survived deletion: %v", err)
	}
}

func TestAFailedWriteLeavesNothingBehind(t *testing.T) {
	store, root := newDisk(t)

	if err := store.Put(context.Background(), sample, []byte("x")); err != nil {
		t.Fatalf("put: %v", err)
	}

	var temporaries []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasPrefix(filepath.Base(path), ".upload-") {
			temporaries = append(temporaries, path)
		}
		return nil
	})
	if len(temporaries) != 0 {
		t.Errorf("temporary files were left behind: %s", strings.Join(temporaries, ", "))
	}
}
