package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tavora-vtt/tavora-server/internal/storage"
)

const worldColumns = `id, slug, title, system_id, system_version, default_locale, active_scene, %s, event_seq, created_at, archived_at`

func (c *conn) scanWorld(scan func(...any) error) (*storage.World, error) {
	var world storage.World
	err := scan(
		&world.ID, &world.Slug, &world.Title, &world.SystemID, &world.SystemVersion, &world.DefaultLocale,
		&world.ActiveScene,
		jsonScanner{dst: &world.Settings, fallback: "{}"},
		&world.EventSeq,
		timeScanner{dst: &world.CreatedAt},
		nullTimeScanner{dst: &world.ArchivedAt},
	)
	if err != nil {
		return nil, err
	}
	return &world, nil
}

func (c *conn) collectWorlds(ctx context.Context, query string, args ...any) ([]storage.World, error) {
	rows, err := c.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	worlds := make([]storage.World, 0, 8)
	for rows.Next() {
		world, err := c.scanWorld(rows.Scan)
		if err != nil {
			return nil, err
		}
		worlds = append(worlds, *world)
	}
	return worlds, rows.Err()
}

func (c *conn) ListWorlds(ctx context.Context) ([]storage.World, error) {
	query := fmt.Sprintf(`SELECT `+worldColumns+` FROM worlds ORDER BY title`,
		c.dialect.JSONColumn("settings"))
	return c.collectWorlds(ctx, query)
}

func (c *conn) ListWorldsForUser(ctx context.Context, userID storage.ID) ([]storage.World, error) {
	query := fmt.Sprintf(`SELECT `+worldColumns+` FROM worlds
		 WHERE id IN (SELECT world_id FROM world_members WHERE user_id = ?)
		 ORDER BY title`, c.dialect.JSONColumn("settings"))
	return c.collectWorlds(ctx, query, userID)
}

func (c *conn) DeleteMember(ctx context.Context, worldID, userID storage.ID) error {
	if err := c.requireWritable(); err != nil {
		return err
	}

	result, err := c.exec(ctx,
		`DELETE FROM world_members WHERE world_id = ? AND user_id = ?`, worldID, userID)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: member %s in world %s", storage.ErrNotFound, userID, worldID)
	}
	return nil
}

const inviteColumns = `id, token_hash, world_id, role, created_by, created_at, expires_at, max_uses, uses, revoked_at`

func (c *conn) scanInvite(scan func(...any) error) (*storage.Invite, error) {
	var invite storage.Invite
	err := scan(
		&invite.ID, &invite.TokenHash, &invite.WorldID, &invite.Role, &invite.CreatedBy,
		timeScanner{dst: &invite.CreatedAt},
		timeScanner{dst: &invite.ExpiresAt},
		&invite.MaxUses, &invite.Uses,
		nullTimeScanner{dst: &invite.RevokedAt},
	)
	if err != nil {
		return nil, err
	}
	return &invite, nil
}

func (c *conn) GetInviteByTokenHash(ctx context.Context, tokenHash string) (*storage.Invite, error) {
	invite, err := c.scanInvite(c.queryRow(ctx,
		`SELECT `+inviteColumns+` FROM invites WHERE token_hash = ?`, tokenHash).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: invite", storage.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return invite, nil
}

func (c *conn) ListInvites(ctx context.Context, worldID storage.ID) ([]storage.Invite, error) {
	rows, err := c.query(ctx,
		`SELECT `+inviteColumns+` FROM invites WHERE world_id = ? ORDER BY created_at DESC`, worldID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	invites := make([]storage.Invite, 0, 8)
	for rows.Next() {
		invite, err := c.scanInvite(rows.Scan)
		if err != nil {
			return nil, err
		}
		invites = append(invites, *invite)
	}
	return invites, rows.Err()
}

func (c *conn) PutInvite(ctx context.Context, invite *storage.Invite) error {
	if err := c.requireWritable(); err != nil {
		return err
	}
	if invite.CreatedAt.IsZero() {
		invite.CreatedAt = time.Now().UTC()
	}

	_, err := c.exec(ctx,
		`INSERT INTO invites (id, token_hash, world_id, role, created_by, created_at, expires_at, max_uses, uses, revoked_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET
		   role = excluded.role, expires_at = excluded.expires_at,
		   max_uses = excluded.max_uses, revoked_at = excluded.revoked_at`,
		invite.ID, invite.TokenHash, invite.WorldID, invite.Role, invite.CreatedBy,
		c.dialect.TimeArg(invite.CreatedAt), c.dialect.TimeArg(invite.ExpiresAt),
		invite.MaxUses, invite.Uses, c.dialect.TimePtrArg(invite.RevokedAt),
	)
	return err
}

func (c *conn) ConsumeInvite(ctx context.Context, worldID, id storage.ID) error {
	if err := c.requireWritable(); err != nil {
		return err
	}

	result, err := c.exec(ctx,
		`UPDATE invites SET uses = uses + 1
		 WHERE world_id = ? AND id = ? AND revoked_at IS NULL
		   AND (max_uses = 0 OR uses < max_uses)`,
		worldID, id)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: invite %s is spent, revoked or unknown", storage.ErrNotFound, id)
	}
	return nil
}

func (c *conn) RevokeInvite(ctx context.Context, worldID, id storage.ID) error {
	if err := c.requireWritable(); err != nil {
		return err
	}

	result, err := c.exec(ctx,
		`UPDATE invites SET revoked_at = ? WHERE world_id = ? AND id = ? AND revoked_at IS NULL`,
		c.dialect.TimeArg(time.Now().UTC()), worldID, id)
	if err != nil {
		return err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("%w: invite %s", storage.ErrNotFound, id)
	}
	return nil
}
