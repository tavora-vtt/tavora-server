package sqlite_test

import (
	"path/filepath"
	"testing"

	"github.com/tavora-vtt/tavora-server/internal/storage"
	"github.com/tavora-vtt/tavora-server/internal/storage/sqlite"
	"github.com/tavora-vtt/tavora-server/internal/storage/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) storage.Store {
		store, err := sqlite.Open(filepath.Join(t.TempDir(), "tavora.db"))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	})
}
