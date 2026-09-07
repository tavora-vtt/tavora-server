package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/tavora-vtt/tavora-server/internal/storage"
)

const userColumns = `id, username, email, password_hash, locale, is_admin, created_at, disabled_at`

func (c *conn) scanUser(scan func(...any) error) (*storage.User, error) {
	var user storage.User
	var email sql.NullString

	err := scan(
		&user.ID, &user.Username, &email, &user.PasswordHash, &user.Locale,
		boolScanner{dst: &user.IsAdmin},
		timeScanner{dst: &user.CreatedAt},
		nullTimeScanner{dst: &user.DisabledAt},
	)
	if err != nil {
		return nil, err
	}

	user.Email = email.String
	return &user, nil
}

func (c *conn) GetUser(ctx context.Context, id storage.ID) (*storage.User, error) {
	user, err := c.scanUser(c.queryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: user %s", storage.ErrNotFound, id)
	}
	if err != nil {
		return nil, err
	}
	return user, nil
}

func (c *conn) GetUserByUsername(ctx context.Context, username string) (*storage.User, error) {
	user, err := c.scanUser(c.queryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE username = ?`, username).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: user %q", storage.ErrNotFound, username)
	}
	if err != nil {
		return nil, err
	}
	return user, nil
}

func (c *conn) CountUsers(ctx context.Context) (int, error) {
	var count int
	if err := c.queryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (c *conn) PutUser(ctx context.Context, user *storage.User) error {
	if err := c.requireWritable(); err != nil {
		return err
	}
	if user.CreatedAt.IsZero() {
		user.CreatedAt = time.Now().UTC()
	}

	var email any
	if user.Email != "" {
		email = user.Email
	}

	_, err := c.exec(ctx,
		`INSERT INTO users (id, username, email, password_hash, locale, is_admin, created_at, disabled_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET
		   username = excluded.username, email = excluded.email,
		   password_hash = excluded.password_hash, locale = excluded.locale,
		   is_admin = excluded.is_admin, disabled_at = excluded.disabled_at`,
		user.ID, user.Username, email, user.PasswordHash, user.Locale,
		user.IsAdmin, c.dialect.TimeArg(user.CreatedAt), c.dialect.TimePtrArg(user.DisabledAt),
	)
	if err != nil && c.dialect.IsUniqueViolation(err) {
		return fmt.Errorf("%w: user %q", storage.ErrAlreadyExists, user.Username)
	}
	return err
}

func (c *conn) GetUserSession(ctx context.Context, tokenHash string) (*storage.UserSession, error) {
	var session storage.UserSession

	err := c.queryRow(ctx,
		`SELECT token_hash, user_id, created_at, expires_at, last_seen_at, user_agent
		 FROM user_sessions WHERE token_hash = ?`, tokenHash,
	).Scan(
		&session.TokenHash, &session.UserID,
		timeScanner{dst: &session.CreatedAt},
		timeScanner{dst: &session.ExpiresAt},
		timeScanner{dst: &session.LastSeenAt},
		&session.UserAgent,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: session", storage.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func (c *conn) PutUserSession(ctx context.Context, session *storage.UserSession) error {
	if err := c.requireWritable(); err != nil {
		return err
	}

	now := time.Now().UTC()
	if session.CreatedAt.IsZero() {
		session.CreatedAt = now
	}
	if session.LastSeenAt.IsZero() {
		session.LastSeenAt = now
	}

	_, err := c.exec(ctx,
		`INSERT INTO user_sessions (token_hash, user_id, created_at, expires_at, last_seen_at, user_agent)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (token_hash) DO UPDATE SET
		   expires_at = excluded.expires_at, last_seen_at = excluded.last_seen_at`,
		session.TokenHash, session.UserID,
		c.dialect.TimeArg(session.CreatedAt),
		c.dialect.TimeArg(session.ExpiresAt),
		c.dialect.TimeArg(session.LastSeenAt),
		session.UserAgent,
	)
	return err
}

func (c *conn) TouchUserSession(ctx context.Context, tokenHash string, seenAt time.Time) error {
	if err := c.requireWritable(); err != nil {
		return err
	}
	_, err := c.exec(ctx,
		`UPDATE user_sessions SET last_seen_at = ? WHERE token_hash = ?`,
		c.dialect.TimeArg(seenAt), tokenHash)
	return err
}

func (c *conn) DeleteUserSession(ctx context.Context, tokenHash string) error {
	if err := c.requireWritable(); err != nil {
		return err
	}
	_, err := c.exec(ctx, `DELETE FROM user_sessions WHERE token_hash = ?`, tokenHash)
	return err
}

func (c *conn) DeleteUserSessionsOf(ctx context.Context, userID storage.ID) error {
	if err := c.requireWritable(); err != nil {
		return err
	}
	_, err := c.exec(ctx, `DELETE FROM user_sessions WHERE user_id = ?`, userID)
	return err
}
