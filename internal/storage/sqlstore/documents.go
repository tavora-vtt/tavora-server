package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tavora-vtt/tavora-server/internal/storage"
)

const defaultLimit = 1000

func (c *conn) documentColumns() string {
	return strings.Join([]string{
		"world_id", "id", "kind", "subtype", "parent_id", "folder_id", "name", "sort", "img",
		c.dialect.JSONColumn("data"),
		c.dialect.JSONColumn("flags"),
		c.dialect.JSONColumn("ownership"),
		"source_pack", "schema_version", "created_at", "updated_at", "updated_seq", "deleted_at",
	}, ", ")
}

func (c *conn) scanDocument(scan func(...any) error) (*storage.Document, error) {
	var doc storage.Document
	var parentID, folderID sql.NullString

	err := scan(
		&doc.WorldID, &doc.ID, &doc.Kind, &doc.Subtype, &parentID, &folderID,
		&doc.Name, &doc.Sort, &doc.Img,
		jsonScanner{dst: &doc.Data, fallback: "{}"},
		jsonScanner{dst: &doc.Flags, fallback: "{}"},
		jsonScanner{dst: &doc.Ownership, fallback: "{}"},
		&doc.SourcePack, &doc.SchemaVersion,
		timeScanner{dst: &doc.CreatedAt},
		timeScanner{dst: &doc.UpdatedAt},
		&doc.UpdatedSeq,
		nullTimeScanner{dst: &doc.DeletedAt},
	)
	if err != nil {
		return nil, err
	}

	doc.ParentID = storage.ID(parentID.String)
	doc.FolderID = storage.ID(folderID.String)
	return &doc, nil
}

func (c *conn) GetWorld(ctx context.Context, id storage.ID) (*storage.World, error) {
	query := fmt.Sprintf(
		`SELECT id, slug, title, system_id, system_version, default_locale, %s, event_seq, created_at, archived_at
		 FROM worlds WHERE id = ?`, c.dialect.JSONColumn("settings"))

	var world storage.World
	err := c.queryRow(ctx, query, id).Scan(
		&world.ID, &world.Slug, &world.Title, &world.SystemID, &world.SystemVersion, &world.DefaultLocale,
		jsonScanner{dst: &world.Settings, fallback: "{}"},
		&world.EventSeq,
		timeScanner{dst: &world.CreatedAt},
		nullTimeScanner{dst: &world.ArchivedAt},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: world %s", storage.ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	return &world, nil
}

func (c *conn) PutWorld(ctx context.Context, world *storage.World) error {
	if err := c.requireWritable(); err != nil {
		return err
	}
	if world.CreatedAt.IsZero() {
		world.CreatedAt = time.Now().UTC()
	}
	if world.DefaultLocale == "" {
		world.DefaultLocale = "en"
	}

	query := fmt.Sprintf(
		`INSERT INTO worlds (id, slug, title, system_id, system_version, default_locale, settings, event_seq, created_at, archived_at)
		 VALUES (?, ?, ?, ?, ?, ?, %s, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET
		   slug = excluded.slug, title = excluded.title, system_id = excluded.system_id,
		   system_version = excluded.system_version, default_locale = excluded.default_locale,
		   settings = excluded.settings, archived_at = excluded.archived_at`, c.dialect.JSONArg())

	_, err := c.exec(ctx, query,
		world.ID, world.Slug, world.Title, world.SystemID, world.SystemVersion, world.DefaultLocale,
		jsonArg(world.Settings, "{}"), world.EventSeq,
		c.dialect.TimeArg(world.CreatedAt), c.dialect.TimePtrArg(world.ArchivedAt),
	)
	if err != nil && c.dialect.IsUniqueViolation(err) {
		return fmt.Errorf("%w: world %s", storage.ErrAlreadyExists, world.ID)
	}
	return err
}

func (c *conn) GetDocument(ctx context.Context, worldID, id storage.ID) (*storage.Document, error) {
	query := fmt.Sprintf(`SELECT %s FROM documents WHERE world_id = ? AND id = ?`, c.documentColumns())

	doc, err := c.scanDocument(c.queryRow(ctx, query, worldID, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: document %s", storage.ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	return doc, nil
}

func (c *conn) ListDocuments(ctx context.Context, worldID storage.ID, filter storage.DocumentFilter) ([]*storage.Document, error) {
	where := []string{"world_id = ?"}
	args := []any{worldID}

	if filter.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, filter.Kind)
	}
	if filter.Subtype != "" {
		where = append(where, "subtype = ?")
		args = append(args, filter.Subtype)
	}
	if filter.ParentID != nil {
		where = append(where, "parent_id = ?")
		args = append(args, string(*filter.ParentID))
	}
	if filter.FolderID != nil {
		where = append(where, "folder_id = ?")
		args = append(args, string(*filter.FolderID))
	}
	if !filter.IncludeDeleted {
		where = append(where, "deleted_at IS NULL")
	}
	if filter.UpdatedSince > 0 {
		where = append(where, "updated_seq > ?")
		args = append(args, filter.UpdatedSince)
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	args = append(args, limit)

	query := fmt.Sprintf(`SELECT %s FROM documents WHERE %s ORDER BY sort, id LIMIT ?`,
		c.documentColumns(), strings.Join(where, " AND "))

	return c.collectDocuments(ctx, query, args...)
}

func (c *conn) QuerySystemData(ctx context.Context, worldID storage.ID, spec storage.JSONQuery) ([]*storage.Document, error) {
	where := []string{"world_id = ?", "deleted_at IS NULL"}
	args := []any{worldID}

	if spec.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, spec.Kind)
	}
	if spec.Subtype != "" {
		where = append(where, "subtype = ?")
		args = append(args, spec.Subtype)
	}

	for _, condition := range spec.Conditions {
		fragment, conditionArgs, err := c.conditionSQL(condition)
		if err != nil {
			return nil, err
		}
		where = append(where, fragment)
		args = append(args, conditionArgs...)
	}

	limit := spec.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	args = append(args, limit)

	query := fmt.Sprintf(`SELECT %s FROM documents WHERE %s ORDER BY sort, id LIMIT ?`,
		c.documentColumns(), strings.Join(where, " AND "))

	return c.collectDocuments(ctx, query, args...)
}

func (c *conn) conditionSQL(condition storage.JSONCondition) (string, []any, error) {
	path, err := splitPath(condition.Path)
	if err != nil {
		return "", nil, err
	}

	if condition.Op == storage.OpExists {
		return fmt.Sprintf("%s IS NOT NULL", c.dialect.JSONType("data", path)), nil, nil
	}

	if condition.Op == storage.OpContains {
		return c.dialect.JSONArrayContains("data", path), []any{scalarText(condition.Value)}, nil
	}

	operator, err := comparisonOperator(condition.Op)
	if err != nil {
		return "", nil, err
	}

	switch value := condition.Value.(type) {
	case bool:
		literal := "false"
		if value {
			literal = "true"
		}
		return fmt.Sprintf("%s %s ?", c.dialect.JSONBool("data", path), operator), []any{literal}, nil
	case int, int32, int64, float32, float64:
		return fmt.Sprintf("%s %s ?", c.dialect.JSONNumber("data", path), operator), []any{value}, nil
	default:
		return fmt.Sprintf("%s %s ?", c.dialect.JSONText("data", path), operator), []any{scalarText(condition.Value)}, nil
	}
}

func comparisonOperator(op storage.JSONOp) (string, error) {
	switch op {
	case storage.OpEqual:
		return "=", nil
	case storage.OpNotEqual:
		return "<>", nil
	case storage.OpLess:
		return "<", nil
	case storage.OpLessEqual:
		return "<=", nil
	case storage.OpGreater:
		return ">", nil
	case storage.OpGreaterEqual:
		return ">=", nil
	default:
		return "", fmt.Errorf("storage: unsupported operator %q", op)
	}
}

func scalarText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}

func (c *conn) collectDocuments(ctx context.Context, query string, args ...any) ([]*storage.Document, error) {
	rows, err := c.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	documents := make([]*storage.Document, 0, 16)
	for rows.Next() {
		doc, err := c.scanDocument(rows.Scan)
		if err != nil {
			return nil, err
		}
		documents = append(documents, doc)
	}
	return documents, rows.Err()
}

func (c *conn) PutDocument(ctx context.Context, doc *storage.Document) error {
	if err := c.requireWritable(); err != nil {
		return err
	}

	now := time.Now().UTC()
	if doc.CreatedAt.IsZero() {
		doc.CreatedAt = now
	}
	doc.UpdatedAt = now

	jsonArgExpr := c.dialect.JSONArg()
	query := fmt.Sprintf(
		`INSERT INTO documents (world_id, id, kind, subtype, parent_id, folder_id, name, sort, img,
		                        data, flags, ownership, source_pack, schema_version,
		                        created_at, updated_at, updated_seq, deleted_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, %s, %s, %s, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (world_id, id) DO UPDATE SET
		   kind = excluded.kind, subtype = excluded.subtype, parent_id = excluded.parent_id,
		   folder_id = excluded.folder_id, name = excluded.name, sort = excluded.sort, img = excluded.img,
		   data = excluded.data, flags = excluded.flags, ownership = excluded.ownership,
		   source_pack = excluded.source_pack, schema_version = excluded.schema_version,
		   updated_at = excluded.updated_at, updated_seq = excluded.updated_seq,
		   deleted_at = excluded.deleted_at`,
		jsonArgExpr, jsonArgExpr, jsonArgExpr)

	_, err := c.exec(ctx, query,
		doc.WorldID, doc.ID, doc.Kind, doc.Subtype,
		nullableID(doc.ParentID), nullableID(doc.FolderID),
		doc.Name, doc.Sort, doc.Img,
		jsonArg(doc.Data, "{}"), jsonArg(doc.Flags, "{}"), jsonArg(doc.Ownership, "{}"),
		doc.SourcePack, doc.SchemaVersion,
		c.dialect.TimeArg(doc.CreatedAt), c.dialect.TimeArg(doc.UpdatedAt),
		doc.UpdatedSeq, c.dialect.TimePtrArg(doc.DeletedAt),
	)
	return err
}

func (c *conn) PatchDocument(ctx context.Context, worldID, id storage.ID, patch storage.Patch) (*storage.Document, error) {
	if err := c.requireWritable(); err != nil {
		return nil, err
	}
	if patch.IsEmpty() {
		return c.GetDocument(ctx, worldID, id)
	}

	assignments := []string{"updated_at = ?", "updated_seq = ?"}
	args := []any{c.dialect.TimeArg(time.Now().UTC()), patch.Seq}

	if patch.Name != nil {
		assignments = append(assignments, "name = ?")
		args = append(args, *patch.Name)
	}
	if patch.Sort != nil {
		assignments = append(assignments, "sort = ?")
		args = append(args, *patch.Sort)
	}
	if patch.Img != nil {
		assignments = append(assignments, "img = ?")
		args = append(args, *patch.Img)
	}
	if patch.FolderID != nil {
		assignments = append(assignments, "folder_id = ?")
		args = append(args, nullableID(*patch.FolderID))
	}
	if patch.Ownership != nil {
		assignments = append(assignments, "ownership = "+c.dialect.JSONArg())
		args = append(args, jsonArg(patch.Ownership, "{}"))
	}

	if len(patch.Set) > 0 || len(patch.Unset) > 0 {
		expression := "data"

		if len(patch.Unset) > 0 {
			paths, err := splitPaths(patch.Unset)
			if err != nil {
				return nil, err
			}
			expression = c.dialect.JSONRemove(expression, paths)
		}

		for _, key := range sortedKeys(patch.Set) {
			path, err := splitPath(key)
			if err != nil {
				return nil, err
			}
			encoded, err := json.Marshal(patch.Set[key])
			if err != nil {
				return nil, fmt.Errorf("storage: encode %q: %w", key, err)
			}
			expression = c.dialect.JSONSet(expression, path)
			args = append(args, string(encoded))
		}

		assignments = append(assignments, "data = "+expression)
	}

	args = append(args, worldID, id)

	query := fmt.Sprintf(`UPDATE documents SET %s WHERE world_id = ? AND id = ? RETURNING %s`,
		strings.Join(assignments, ", "), c.documentColumns())

	doc, err := c.scanDocument(c.queryRow(ctx, query, args...).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: document %s", storage.ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	return doc, nil
}

func (c *conn) DeleteDocument(ctx context.Context, worldID, id storage.ID, mode storage.DeleteMode) error {
	if err := c.requireWritable(); err != nil {
		return err
	}

	var (
		result sql.Result
		err    error
	)
	if mode == storage.DeleteHard {
		result, err = c.exec(ctx,
			`DELETE FROM documents WHERE world_id = ? AND (id = ? OR parent_id = ?)`,
			worldID, id, string(id))
	} else {
		result, err = c.exec(ctx,
			`UPDATE documents SET deleted_at = ? WHERE world_id = ? AND (id = ? OR parent_id = ?) AND deleted_at IS NULL`,
			c.dialect.TimeArg(time.Now().UTC()), worldID, id, string(id))
	}
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: document %s", storage.ErrNotFound, id)
	}
	return nil
}

func (c *conn) AppendEvent(ctx context.Context, event storage.Event) (int64, error) {
	if err := c.requireWritable(); err != nil {
		return 0, err
	}

	var seq int64
	err := c.queryRow(ctx,
		`UPDATE worlds SET event_seq = event_seq + 1 WHERE id = ? RETURNING event_seq`,
		event.WorldID).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: world %s", storage.ErrNotFound, event.WorldID)
	}
	if err != nil {
		return 0, err
	}

	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	jsonArgExpr := c.dialect.JSONArg()
	query := fmt.Sprintf(
		`INSERT INTO world_events (world_id, seq, ts, actor_user_id, kind, target_kind, target_id, scene_id, payload, audience, undo)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, %s, %s, %s)`,
		jsonArgExpr, jsonArgExpr, jsonArgExpr)

	_, err = c.exec(ctx, query,
		event.WorldID, seq, c.dialect.TimeArg(event.Timestamp),
		string(event.ActorUserID), event.Kind, event.TargetKind,
		string(event.TargetID), string(event.SceneID),
		jsonArg(event.Payload, "{}"), jsonArg(event.Audience, "[]"), jsonArg(event.Undo, "null"),
	)
	if err != nil {
		return 0, err
	}
	return seq, nil
}

func (c *conn) EventsSince(ctx context.Context, worldID storage.ID, seq int64, limit int) ([]storage.Event, error) {
	if limit <= 0 {
		limit = defaultLimit
	}

	query := fmt.Sprintf(
		`SELECT world_id, seq, ts, actor_user_id, kind, target_kind, target_id, scene_id, %s, %s, %s
		 FROM world_events WHERE world_id = ? AND seq > ? ORDER BY seq LIMIT ?`,
		c.dialect.JSONColumn("payload"), c.dialect.JSONColumn("audience"), c.dialect.JSONColumn("undo"))

	rows, err := c.query(ctx, query, worldID, seq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]storage.Event, 0, 16)
	for rows.Next() {
		var event storage.Event
		var actorUserID, targetID, sceneID string

		err := rows.Scan(
			&event.WorldID, &event.Seq,
			timeScanner{dst: &event.Timestamp},
			&actorUserID, &event.Kind, &event.TargetKind, &targetID, &sceneID,
			jsonScanner{dst: &event.Payload, fallback: "{}"},
			jsonScanner{dst: &event.Audience, fallback: "[]"},
			jsonScanner{dst: &event.Undo, fallback: "null"},
		)
		if err != nil {
			return nil, err
		}

		event.ActorUserID = storage.ID(actorUserID)
		event.TargetID = storage.ID(targetID)
		event.SceneID = storage.ID(sceneID)
		events = append(events, event)
	}
	return events, rows.Err()
}

func nullableID(id storage.ID) any {
	if id == "" {
		return nil
	}
	return string(id)
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
