package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/tavora-vtt/tavora-server/internal/storage"
)

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type Store struct {
	db      *sql.DB
	dialect Dialect
}

func New(db *sql.DB, dialect Dialect) *Store {
	return &Store{db: db, dialect: dialect}
}

func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) Backend() string {
	return s.dialect.Name()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) ReadOnly(ctx context.Context, fn func(storage.Query) error) error {
	return fn(&conn{q: s.db, dialect: s.dialect})
}

func (s *Store) Tx(ctx context.Context, fn func(storage.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}

	if err := fn(&conn{q: tx, dialect: s.dialect, writable: true}); err != nil {
		_ = tx.Rollback()
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
	); err != nil {
		return fmt.Errorf("migration table: %w", err)
	}

	var applied int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`,
	).Scan(&applied); err != nil {
		return fmt.Errorf("read migration state: %w", err)
	}

	for index, statements := range s.dialect.Migrations() {
		version := index + 1
		if version <= applied {
			continue
		}

		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", version, err)
		}
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migration %d: %w", version, err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			s.dialect.Rebind(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`),
			version, time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", version, err)
		}
	}
	return nil
}

type conn struct {
	q        querier
	dialect  Dialect
	writable bool
}

func (c *conn) exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return c.q.ExecContext(ctx, c.dialect.Rebind(query), args...)
}

func (c *conn) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return c.q.QueryContext(ctx, c.dialect.Rebind(query), args...)
}

func (c *conn) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return c.q.QueryRowContext(ctx, c.dialect.Rebind(query), args...)
}

func (c *conn) requireWritable() error {
	if !c.writable {
		return storage.ErrReadOnly
	}
	return nil
}

type timeScanner struct {
	dst *time.Time
}

func (s timeScanner) Scan(value any) error {
	switch typed := value.(type) {
	case nil:
		*s.dst = time.Time{}
		return nil
	case time.Time:
		*s.dst = typed.UTC()
		return nil
	case string:
		return parseTimeInto(typed, s.dst)
	case []byte:
		return parseTimeInto(string(typed), s.dst)
	default:
		return fmt.Errorf("storage: cannot scan %T as time", value)
	}
}

type nullTimeScanner struct {
	dst **time.Time
}

func (s nullTimeScanner) Scan(value any) error {
	if value == nil {
		*s.dst = nil
		return nil
	}
	var parsed time.Time
	if err := (timeScanner{dst: &parsed}).Scan(value); err != nil {
		return err
	}
	*s.dst = &parsed
	return nil
}

func parseTimeInto(raw string, dst *time.Time) error {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			*dst = parsed.UTC()
			return nil
		}
	}
	return fmt.Errorf("storage: cannot parse time %q", raw)
}

type boolScanner struct {
	dst *bool
}

func (s boolScanner) Scan(value any) error {
	switch typed := value.(type) {
	case nil:
		*s.dst = false
	case bool:
		*s.dst = typed
	case int64:
		*s.dst = typed != 0
	case []byte:
		*s.dst = len(typed) == 1 && (typed[0] == 't' || typed[0] == '1')
	case string:
		*s.dst = typed == "t" || typed == "true" || typed == "1"
	default:
		return fmt.Errorf("storage: cannot scan %T as bool", value)
	}
	return nil
}

type jsonScanner struct {
	dst      *json.RawMessage
	fallback string
}

func (s jsonScanner) Scan(value any) error {
	switch typed := value.(type) {
	case nil:
		*s.dst = json.RawMessage(s.fallback)
	case string:
		*s.dst = json.RawMessage(typed)
	case []byte:
		*s.dst = json.RawMessage(append([]byte(nil), typed...))
	default:
		return fmt.Errorf("storage: cannot scan %T as json", value)
	}
	return nil
}

func jsonArg(raw json.RawMessage, fallback string) any {
	if len(raw) == 0 {
		return fallback
	}
	return string(raw)
}
