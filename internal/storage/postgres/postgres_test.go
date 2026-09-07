package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync/atomic"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/tavora-vtt/tavora-server/internal/storage"
	"github.com/tavora-vtt/tavora-server/internal/storage/postgres"
	"github.com/tavora-vtt/tavora-server/internal/storage/storetest"
)

const (
	dsnEnv     = "TAVORA_TEST_POSTGRES_DSN"
	requireEnv = "TAVORA_TEST_REQUIRE_POSTGRES"
)

var schemaCounter atomic.Int64

func TestConformance(t *testing.T) {
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		if os.Getenv(requireEnv) != "" {
			t.Fatalf("%s is set but %s is not: the conformance suite would silently cover only one backend", requireEnv, dsnEnv)
		}
		t.Skipf("%s not set", dsnEnv)
	}

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("admin connection: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })

	if err := admin.PingContext(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}

	storetest.Run(t, func(t *testing.T) storage.Store {
		schema := fmt.Sprintf("conformance_%d", schemaCounter.Add(1))
		ctx := context.Background()

		if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
			t.Fatalf("create schema: %v", err)
		}
		t.Cleanup(func() {
			_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		})

		store, err := postgres.Open(withSearchPath(t, dsn, schema))
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	})
}

func withSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	query := parsed.Query()
	query.Set("options", "-c search_path="+schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
