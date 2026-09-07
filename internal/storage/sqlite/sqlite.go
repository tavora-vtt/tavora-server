package sqlite

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/tavora-vtt/tavora-server/internal/storage/sqlstore"
)

type dialect struct{}

func Open(dsn string) (*sqlstore.Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		"PRAGMA synchronous = NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}

	return sqlstore.New(db, dialect{}), nil
}

func (dialect) Name() string { return "sqlite" }

func (dialect) Rebind(query string) string { return query }

func (dialect) JSONArg() string { return "jsonb(?)" }

func (dialect) JSONColumn(column string) string { return "json(" + column + ")" }

func (dialect) JSONSet(expr string, path []string) string {
	return fmt.Sprintf("jsonb_set(%s, '%s', json(?))", expr, sqlitePath(path))
}

func (dialect) JSONRemove(expr string, paths [][]string) string {
	literals := make([]string, 0, len(paths))
	for _, path := range paths {
		literals = append(literals, "'"+sqlitePath(path)+"'")
	}
	return fmt.Sprintf("jsonb_remove(%s, %s)", expr, strings.Join(literals, ", "))
}

func (dialect) JSONText(column string, path []string) string {
	return fmt.Sprintf("CAST(json_extract(%s, '%s') AS TEXT)", column, sqlitePath(path))
}

func (dialect) JSONNumber(column string, path []string) string {
	literal := sqlitePath(path)
	return fmt.Sprintf(
		"CASE WHEN json_type(%s, '%s') IN ('integer', 'real') THEN CAST(json_extract(%s, '%s') AS REAL) ELSE NULL END",
		column, literal, column, literal)
}

func (dialect) JSONType(column string, path []string) string {
	return fmt.Sprintf("json_type(%s, '%s')", column, sqlitePath(path))
}

func (dialect) JSONBool(column string, path []string) string {
	literal := sqlitePath(path)
	return fmt.Sprintf(
		"CASE WHEN json_type(%s, '%s') IN ('true', 'false') THEN json_type(%s, '%s') ELSE NULL END",
		column, literal, column, literal)
}

func (dialect) JSONArrayContains(column string, path []string) string {
	return fmt.Sprintf(
		"EXISTS (SELECT 1 FROM json_each(%s, '%s') WHERE CAST(json_each.value AS TEXT) = ?)",
		column, sqlitePath(path))
}

func (dialect) TimeArg(value time.Time) any {
	return value.UTC().Format(time.RFC3339Nano)
}

func (d dialect) TimePtrArg(value *time.Time) any {
	if value == nil {
		return nil
	}
	return d.TimeArg(*value)
}

func (dialect) IsUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func sqlitePath(path []string) string {
	return "$." + strings.Join(path, ".")
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
				settings       BLOB NOT NULL,
				event_seq      INTEGER NOT NULL DEFAULT 0,
				created_at     TEXT NOT NULL,
				archived_at    TEXT
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
				data           BLOB NOT NULL,
				flags          BLOB NOT NULL,
				ownership      BLOB NOT NULL,
				source_pack    TEXT NOT NULL DEFAULT '',
				schema_version TEXT NOT NULL DEFAULT '',
				created_at     TEXT NOT NULL,
				updated_at     TEXT NOT NULL,
				updated_seq    INTEGER NOT NULL DEFAULT 0,
				deleted_at     TEXT,
				PRIMARY KEY (world_id, id)
			)`,
			`CREATE INDEX documents_kind_idx ON documents (world_id, kind, deleted_at)`,
			`CREATE INDEX documents_parent_idx ON documents (world_id, parent_id) WHERE parent_id IS NOT NULL`,
			`CREATE INDEX documents_folder_idx ON documents (world_id, folder_id) WHERE folder_id IS NOT NULL`,
			`CREATE INDEX documents_seq_idx ON documents (world_id, updated_seq)`,
			`CREATE TABLE world_events (
				world_id      TEXT NOT NULL REFERENCES worlds(id) ON DELETE CASCADE,
				seq           INTEGER NOT NULL,
				ts            TEXT NOT NULL,
				actor_user_id TEXT NOT NULL DEFAULT '',
				kind          TEXT NOT NULL,
				target_kind   TEXT NOT NULL DEFAULT '',
				target_id     TEXT NOT NULL DEFAULT '',
				scene_id      TEXT NOT NULL DEFAULT '',
				payload       BLOB NOT NULL,
				audience      BLOB NOT NULL,
				undo          BLOB,
				PRIMARY KEY (world_id, seq)
			)`,
			`CREATE INDEX world_events_ts_idx ON world_events (world_id, ts)`,
		},
	}
}
