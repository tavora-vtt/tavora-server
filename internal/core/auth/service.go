package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tavora-vtt/tavora-server/internal/storage"
)

const (
	DefaultSessionTTL = 30 * 24 * time.Hour
	tokenBytes        = 32
	MinPasswordLength = 10
)

var (
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrRateLimited        = errors.New("auth: too many attempts")
	ErrSessionExpired     = errors.New("auth: session expired")
	ErrWeakPassword       = fmt.Errorf("auth: password must be at least %d characters", MinPasswordLength)
	ErrUsernameTaken      = errors.New("auth: username already taken")
)

type Service struct {
	store      storage.Store
	params     HashParams
	sessionTTL time.Duration
	limiter    *Limiter
	now        func() time.Time
}

type Options struct {
	HashParams HashParams
	SessionTTL time.Duration
	Limiter    *Limiter
}

func NewService(store storage.Store, options Options) *Service {
	if options.HashParams.Memory == 0 {
		options.HashParams = DefaultHashParams()
	}
	if options.SessionTTL <= 0 {
		options.SessionTTL = DefaultSessionTTL
	}
	if options.Limiter == nil {
		options.Limiter = NewLimiter(DefaultLimits())
	}
	return &Service{
		store:      store,
		params:     options.HashParams,
		sessionTTL: options.SessionTTL,
		limiter:    options.Limiter,
		now:        time.Now,
	}
}

func (s *Service) SessionTTL() time.Duration { return s.sessionTTL }

func NormaliseUsername(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func (s *Service) CreateUser(ctx context.Context, username, password string, admin bool) (*storage.User, error) {
	username = NormaliseUsername(username)
	if username == "" {
		return nil, ErrInvalidCredentials
	}
	if len(password) < MinPasswordLength {
		return nil, ErrWeakPassword
	}

	hash, err := HashPassword(password, s.params)
	if err != nil {
		return nil, err
	}

	user := &storage.User{
		ID:           storage.ID("user-" + randomID()),
		Username:     username,
		PasswordHash: hash,
		IsAdmin:      admin,
		CreatedAt:    s.now().UTC(),
	}

	err = s.store.Tx(ctx, func(tx storage.Tx) error {
		return s.insertUser(ctx, tx, user)
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}

func (s *Service) BuildUser(username, password string, admin bool) (*storage.User, error) {
	username = NormaliseUsername(username)
	if username == "" {
		return nil, ErrInvalidCredentials
	}

	hash := ""
	if password != "" {
		if len(password) < MinPasswordLength {
			return nil, ErrWeakPassword
		}
		encoded, err := HashPassword(password, s.params)
		if err != nil {
			return nil, err
		}
		hash = encoded
	}

	return &storage.User{
		ID:           storage.ID("user-" + randomID()),
		Username:     username,
		PasswordHash: hash,
		IsAdmin:      admin,
		CreatedAt:    s.now().UTC(),
	}, nil
}

func (s *Service) InsertUser(ctx context.Context, tx storage.Tx, user *storage.User) error {
	return s.insertUser(ctx, tx, user)
}

func (s *Service) insertUser(ctx context.Context, tx storage.Tx, user *storage.User) error {
	if _, err := tx.GetUserByUsername(ctx, user.Username); err == nil {
		return ErrUsernameTaken
	} else if !errors.Is(err, storage.ErrNotFound) {
		return err
	}

	if err := tx.PutUser(ctx, user); err != nil {
		if errors.Is(err, storage.ErrAlreadyExists) {
			return ErrUsernameTaken
		}
		return err
	}
	return nil
}

func (s *Service) IssueSession(ctx context.Context, tx storage.Tx, userID storage.ID, userAgent string) (string, time.Time, error) {
	token, err := newToken()
	if err != nil {
		return "", time.Time{}, err
	}

	now := s.now().UTC()
	session := &storage.UserSession{
		TokenHash:  HashToken(token),
		UserID:     userID,
		CreatedAt:  now,
		ExpiresAt:  now.Add(s.sessionTTL),
		LastSeenAt: now,
		UserAgent:  truncate(userAgent, 256),
	}

	if err := tx.PutUserSession(ctx, session); err != nil {
		return "", time.Time{}, err
	}
	return token, session.ExpiresAt, nil
}

func GenerateToken() (string, error) {
	return newToken()
}

func GenerateID(prefix string) storage.ID {
	return storage.ID(prefix + "-" + randomID())
}

type LoginResult struct {
	User      *storage.User
	Token     string
	ExpiresAt time.Time
}

func (s *Service) Login(ctx context.Context, username, password, remoteAddr, userAgent string) (*LoginResult, error) {
	username = NormaliseUsername(username)

	if !s.limiter.Allow(username, remoteAddr) {
		return nil, ErrRateLimited
	}

	var user *storage.User
	err := s.store.ReadOnly(ctx, func(q storage.Query) error {
		found, err := q.GetUserByUsername(ctx, username)
		if err != nil {
			return err
		}
		user = found
		return nil
	})

	if err != nil || user == nil || user.Disabled() || user.PasswordHash == "" {
		burnTime(password)
		s.limiter.Fail(username, remoteAddr)
		return nil, ErrInvalidCredentials
	}

	ok, err := VerifyPassword(user.PasswordHash, password)
	if err != nil || !ok {
		s.limiter.Fail(username, remoteAddr)
		return nil, ErrInvalidCredentials
	}

	s.limiter.Succeed(username, remoteAddr)

	token, err := newToken()
	if err != nil {
		return nil, err
	}

	now := s.now().UTC()
	session := &storage.UserSession{
		TokenHash:  HashToken(token),
		UserID:     user.ID,
		CreatedAt:  now,
		ExpiresAt:  now.Add(s.sessionTTL),
		LastSeenAt: now,
		UserAgent:  truncate(userAgent, 256),
	}

	if err := s.store.Tx(ctx, func(tx storage.Tx) error {
		return tx.PutUserSession(ctx, session)
	}); err != nil {
		return nil, err
	}

	return &LoginResult{User: user, Token: token, ExpiresAt: session.ExpiresAt}, nil
}

func (s *Service) Authenticate(ctx context.Context, token string) (*storage.User, error) {
	if token == "" {
		return nil, ErrInvalidCredentials
	}
	hash := HashToken(token)

	var (
		session *storage.UserSession
		user    *storage.User
	)

	err := s.store.ReadOnly(ctx, func(q storage.Query) error {
		found, err := q.GetUserSession(ctx, hash)
		if err != nil {
			return err
		}
		session = found

		user, err = q.GetUser(ctx, session.UserID)
		return err
	})
	if errors.Is(err, storage.ErrNotFound) {
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}

	if s.now().After(session.ExpiresAt) {
		_ = s.Logout(ctx, token)
		return nil, ErrSessionExpired
	}
	if user.Disabled() {
		return nil, ErrInvalidCredentials
	}

	return user, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	hash := HashToken(token)
	return s.store.Tx(ctx, func(tx storage.Tx) error {
		return tx.DeleteUserSession(ctx, hash)
	})
}

func (s *Service) LogoutEverywhere(ctx context.Context, userID storage.ID) error {
	return s.store.Tx(ctx, func(tx storage.Tx) error {
		return tx.DeleteUserSessionsOf(ctx, userID)
	})
}

func (s *Service) SetPassword(ctx context.Context, userID storage.ID, password string) error {
	if len(password) < MinPasswordLength {
		return ErrWeakPassword
	}

	hash, err := HashPassword(password, s.params)
	if err != nil {
		return err
	}

	return s.store.Tx(ctx, func(tx storage.Tx) error {
		user, err := tx.GetUser(ctx, userID)
		if err != nil {
			return err
		}
		user.PasswordHash = hash
		if err := tx.PutUser(ctx, user); err != nil {
			return err
		}
		return tx.DeleteUserSessionsOf(ctx, userID)
	})
}

func (s *Service) NeedsSetup(ctx context.Context) (bool, error) {
	var count int
	err := s.store.ReadOnly(ctx, func(q storage.Query) error {
		var readErr error
		count, readErr = q.CountUsers(ctx)
		return readErr
	})
	if err != nil {
		return false, err
	}
	return count == 0, nil
}

func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func newToken() (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("auth: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func randomID() string {
	raw := make([]byte, 10)
	if _, err := rand.Read(raw); err != nil {
		panic(err)
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw))
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
