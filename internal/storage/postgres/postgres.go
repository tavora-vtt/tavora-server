package postgres

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/tavora-vtt/tavora-server/internal/storage/sqlstore"
)

const uniqueViolation = "23505"

type dialect struct{}

func Open(dsn string) (*sqlstore.Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)

	return sqlstore.New(db, dialect{}), nil
}

func (dialect) Name() string { return "postgres" }

func (dialect) Rebind(query string) string { return sqlstore.RebindDollar(query) }

func (dialect) JSONArg() string { return "?::jsonb" }

func (dialect) JSONColumn(column string) string { return column + "::text" }

func (dialect) JSONSet(expr string, path []string) string {
	return fmt.Sprintf("jsonb_set(%s, '%s', ?::jsonb, true)", expr, pgPath(path))
}

func (dialect) JSONRemove(expr string, paths [][]string) string {
	result := expr
	for _, path := range paths {
		result = fmt.Sprintf("(%s #- '%s')", result, pgPath(path))
	}
	return result
}

func (dialect) JSONText(column string, path []string) string {
	return fmt.Sprintf("(%s #>> '%s')", column, pgPath(path))
}

func (dialect) JSONNumber(column string, path []string) string {
	literal := pgPath(path)
	return fmt.Sprintf(
		"CASE WHEN jsonb_typeof(%s #> '%s') = 'number' THEN (%s #>> '%s')::numeric ELSE NULL END",
		column, literal, column, literal)
}

func (dialect) JSONType(column string, path []string) string {
	return fmt.Sprintf("jsonb_typeof(%s #> '%s')", column, pgPath(path))
}

func (dialect) JSONBool(column string, path []string) string {
	literal := pgPath(path)
	return fmt.Sprintf(
		"CASE WHEN jsonb_typeof(%s #> '%s') = 'boolean' THEN %s #>> '%s' ELSE NULL END",
		column, literal, column, literal)
}

func (dialect) JSONArrayContains(column string, path []string) string {
	literal := pgPath(path)
	return fmt.Sprintf(
		"EXISTS (SELECT 1 FROM jsonb_array_elements_text("+
			"CASE WHEN jsonb_typeof(%s #> '%s') = 'array' THEN %s #> '%s' ELSE '[]'::jsonb END"+
			") AS element(value) WHERE element.value = ?)",
		column, literal, column, literal)
}

func (dialect) TimeArg(value time.Time) any {
	return value.UTC()
}

func (dialect) TimePtrArg(value *time.Time) any {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return utc
}

func (dialect) IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}

func pgPath(path []string) string {
	return "{" + strings.Join(path, ",") + "}"
}

func (dialect) Migrations() [][]string {
	return [][]string{
		{
			`CREATE TABLE worlds (
				id             TEXT PRIMARY KEY,
				slug           TEXT NOT NULL UNIQUE,
				title          TEXT NOT NULL,
				system_id      TEXT NOT NULL,
				system_version TEXT NOT NULL,
				default_locale TEXT NOT NULL DEFAULT 'en',
				settings       JSONB NOT NULL,
				event_seq      BIGINT NOT NULL DEFAULT 0,
				created_at     TIMESTAMPTZ NOT NULL,
				archived_at    TIMESTAMPTZ
			)`,
			`CREATE TABLE documents (
				world_id       TEXT NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
				id             TEXT NOT NULL,
				kind           TEXT NOT NULL,
				subtype        TEXT NOT NULL DEFAULT '',
				parent_id      TEXT,
				folder_id      TEXT,
				name           TEXT NOT NULL,
				sort           INTEGER NOT NULL DEFAULT 0,
				img            TEXT NOT NULL DEFAULT '',
				data           JSONB NOT NULL,
				flags          JSONB NOT NULL,
				ownership      JSONB NOT NULL,
				source_pack    TEXT NOT NULL DEFAULT '',
				schema_version TEXT NOT NULL DEFAULT '',
				created_at     TIMESTAMPTZ NOT NULL,
				updated_at     TIMESTAMPTZ NOT NULL,
				updated_seq    BIGINT NOT NULL DEFAULT 0,
				deleted_at     TIMESTAMPTZ,
				PRIMARY KEY (world_id, id)
			)`,
			`CREATE INDEX documents_kind_idx ON documents (world_id, kind, deleted_at)`,
			`CREATE INDEX documents_parent_idx ON documents (world_id, parent_id) WHERE parent_id IS NOT NULL`,
			`CREATE INDEX documents_folder_idx ON documents (world_id, folder_id) WHERE folder_id IS NOT NULL`,
			`CREATE INDEX documents_seq_idx ON documents (world_id, updated_seq)`,
			`CREATE INDEX documents_data_gin ON documents USING GIN (data jsonb_path_ops)`,
			`CREATE TABLE world_events (
				world_id      TEXT NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
				seq           BIGINT NOT NULL,
				ts            TIMESTAMPTZ NOT NULL,
				actor_user_id TEXT NOT NULL DEFAULT '',
				kind          TEXT NOT NULL,
				target_kind   TEXT NOT NULL DEFAULT '',
				target_id     TEXT NOT NULL DEFAULT '',
				scene_id      TEXT NOT NULL DEFAULT '',
				payload       JSONB NOT NULL,
				audience      JSONB NOT NULL,
				undo          JSONB,
				PRIMARY KEY (world_id, seq)
			)`,
			`CREATE INDEX world_events_ts_idx ON world_events (world_id, ts)`,
		},
		{
			`CREATE TABLE world_members (
				world_id  TEXT NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
				user_id   TEXT NOT NULL,
				role      TEXT NOT NULL,
				joined_at TIMESTAMPTZ NOT NULL,
				PRIMARY KEY (world_id, user_id)
			)`,
		},
	}
}
