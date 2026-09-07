package auth

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tavora-vtt/tavora-server/internal/storage"
	"github.com/tavora-vtt/tavora-server/internal/storage/sqlite"
)

func fastParams() HashParams {
	return HashParams{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}
}

func newService(t *testing.T) (*Service, storage.Store) {
	t.Helper()

	store, err := sqlite.Open(filepath.Join(t.TempDir(), "tavora.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	return NewService(store, Options{HashParams: fastParams()}), store
}

func TestPasswordRoundTrip(t *testing.T) {
	encoded, err := HashPassword("correct horse battery staple", fastParams())
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	if !strings.HasPrefix(encoded, "$argon2id$v=19$") {
		t.Errorf("unexpected encoding: %s", encoded)
	}

	ok, err := VerifyPassword(encoded, "correct horse battery staple")
	if err != nil || !ok {
		t.Errorf("correct password rejected: ok=%v err=%v", ok, err)
	}

	ok, err = VerifyPassword(encoded, "correct horse battery stapl")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ok {
		t.Error("wrong password accepted")
	}
}

func TestPasswordHashesAreSalted(t *testing.T) {
	first, _ := HashPassword("same password", fastParams())
	second, _ := HashPassword("same password", fastParams())

	if first == second {
		t.Error("two hashes of the same password are identical, the salt is not random")
	}
}

func TestMalformedHashIsRejected(t *testing.T) {
	for _, encoded := range []string{
		"", "not-a-hash", "$bcrypt$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=1$m=1,t=1,p=1$c2FsdA$aGFzaA",
	} {
		if _, err := VerifyPassword(encoded, "whatever"); err == nil {
			t.Errorf("accepted a malformed hash: %q", encoded)
		}
	}
}

func TestLoginIssuesASessionAndAuthenticates(t *testing.T) {
	service, _ := newService(t)
	ctx := context.Background()

	if _, err := service.CreateUser(ctx, "Nadia", "a-long-enough-password", true); err != nil {
		t.Fatalf("create user: %v", err)
	}

	result, err := service.Login(ctx, "nadia", "a-long-enough-password", "10.0.0.1", "go-test")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if result.Token == "" {
		t.Fatal("no token issued")
	}

	user, err := service.Authenticate(ctx, result.Token)
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if user.Username != "nadia" || !user.IsAdmin {
		t.Errorf("user = %+v", user)
	}
}

func TestUsernameIsCaseInsensitive(t *testing.T) {
	service, _ := newService(t)
	ctx := context.Background()

	if _, err := service.CreateUser(ctx, "  Nadia  ", "a-long-enough-password", false); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := service.Login(ctx, "NADIA", "a-long-enough-password", "10.0.0.1", ""); err != nil {
		t.Errorf("login with different case failed: %v", err)
	}
	if _, err := service.CreateUser(ctx, "NADIA", "another-long-password", false); !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("duplicate username accepted: %v", err)
	}
}

func TestUnknownUserAndWrongPasswordAreIndistinguishable(t *testing.T) {
	service, _ := newService(t)
	ctx := context.Background()

	if _, err := service.CreateUser(ctx, "nadia", "a-long-enough-password", false); err != nil {
		t.Fatalf("create user: %v", err)
	}

	_, unknownErr := service.Login(ctx, "nobody", "a-long-enough-password", "10.0.0.1", "")
	_, wrongErr := service.Login(ctx, "nadia", "not-the-password", "10.0.0.2", "")

	if !errors.Is(unknownErr, ErrInvalidCredentials) || !errors.Is(wrongErr, ErrInvalidCredentials) {
		t.Fatalf("errors differ: unknown=%v wrong=%v", unknownErr, wrongErr)
	}
	if unknownErr.Error() != wrongErr.Error() {
		t.Errorf("error text differs and leaks whether the account exists:\n  %v\n  %v",
			unknownErr, wrongErr)
	}
}

func TestSessionTokensAreStoredHashed(t *testing.T) {
	service, store := newService(t)
	ctx := context.Background()

	if _, err := service.CreateUser(ctx, "nadia", "a-long-enough-password", false); err != nil {
		t.Fatalf("create user: %v", err)
	}
	result, err := service.Login(ctx, "nadia", "a-long-enough-password", "10.0.0.1", "")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	err = store.ReadOnly(ctx, func(q storage.Query) error {
		if _, err := q.GetUserSession(ctx, result.Token); err == nil {
			t.Error("the raw token is a lookup key, a database leak would hand over live sessions")
		}
		stored, err := q.GetUserSession(ctx, HashToken(result.Token))
		if err != nil {
			return err
		}
		if stored.TokenHash == result.Token {
			t.Error("the raw token was stored")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
}

func TestExpiredSessionIsRejectedAndRemoved(t *testing.T) {
	service, store := newService(t)
	ctx := context.Background()

	if _, err := service.CreateUser(ctx, "nadia", "a-long-enough-password", false); err != nil {
		t.Fatalf("create user: %v", err)
	}
	result, err := service.Login(ctx, "nadia", "a-long-enough-password", "10.0.0.1", "")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	service.now = func() time.Time { return time.Now().Add(2 * DefaultSessionTTL) }

	if _, err := service.Authenticate(ctx, result.Token); !errors.Is(err, ErrSessionExpired) {
		t.Errorf("err = %v, want ErrSessionExpired", err)
	}

	err = store.ReadOnly(ctx, func(q storage.Query) error {
		_, err := q.GetUserSession(ctx, HashToken(result.Token))
		return err
	})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("expired session was not cleaned up: %v", err)
	}
}

func TestLogoutInvalidatesTheSession(t *testing.T) {
	service, _ := newService(t)
	ctx := context.Background()

	if _, err := service.CreateUser(ctx, "nadia", "a-long-enough-password", false); err != nil {
		t.Fatalf("create user: %v", err)
	}
	result, _ := service.Login(ctx, "nadia", "a-long-enough-password", "10.0.0.1", "")

	if err := service.Logout(ctx, result.Token); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := service.Authenticate(ctx, result.Token); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("session survived logout: %v", err)
	}
}

func TestChangingThePasswordEndsEverySession(t *testing.T) {
	service, _ := newService(t)
	ctx := context.Background()

	user, err := service.CreateUser(ctx, "nadia", "a-long-enough-password", false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	first, _ := service.Login(ctx, "nadia", "a-long-enough-password", "10.0.0.1", "")
	second, _ := service.Login(ctx, "nadia", "a-long-enough-password", "10.0.0.2", "")

	if err := service.SetPassword(ctx, user.ID, "a-different-long-password"); err != nil {
		t.Fatalf("set password: %v", err)
	}

	for name, token := range map[string]string{"first": first.Token, "second": second.Token} {
		if _, err := service.Authenticate(ctx, token); err == nil {
			t.Errorf("the %s session survived a password change", name)
		}
	}
}

func TestWeakPasswordIsRejected(t *testing.T) {
	service, _ := newService(t)

	if _, err := service.CreateUser(context.Background(), "nadia", "short", false); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("err = %v, want ErrWeakPassword", err)
	}
}

func TestDisabledUserCannotSignIn(t *testing.T) {
	service, store := newService(t)
	ctx := context.Background()

	user, err := service.CreateUser(ctx, "nadia", "a-long-enough-password", false)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	disabled := time.Now().UTC()
	user.DisabledAt = &disabled
	if err := store.Tx(ctx, func(tx storage.Tx) error { return tx.PutUser(ctx, user) }); err != nil {
		t.Fatalf("disable: %v", err)
	}

	if _, err := service.Login(ctx, "nadia", "a-long-enough-password", "10.0.0.1", ""); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("disabled user signed in: %v", err)
	}
}

func TestRateLimiterLocksOutAfterRepeatedFailures(t *testing.T) {
	service, _ := newService(t)
	ctx := context.Background()

	if _, err := service.CreateUser(ctx, "nadia", "a-long-enough-password", false); err != nil {
		t.Fatalf("create user: %v", err)
	}

	limits := DefaultLimits()
	for i := 0; i < limits.Burst; i++ {
		if _, err := service.Login(ctx, "nadia", "wrong-password", "10.0.0.1", ""); !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("attempt %d returned %v", i, err)
		}
	}

	if _, err := service.Login(ctx, "nadia", "a-long-enough-password", "10.0.0.1", ""); !errors.Is(err, ErrRateLimited) {
		t.Errorf("err = %v, want ErrRateLimited even for the correct password", err)
	}
}

func TestSuccessfulLoginClearsTheFailureCount(t *testing.T) {
	limiter := NewLimiter(DefaultLimits())

	for i := 0; i < DefaultLimits().Burst-1; i++ {
		limiter.Fail("nadia", "10.0.0.1")
	}
	limiter.Succeed("nadia", "10.0.0.1")

	if limiter.Tracked() != 0 {
		t.Errorf("failures still tracked after a success: %d", limiter.Tracked())
	}
	if !limiter.Allow("nadia", "10.0.0.1") {
		t.Error("locked out after a successful login")
	}
}

func TestNeedsSetupFlipsAfterTheFirstUser(t *testing.T) {
	service, _ := newService(t)
	ctx := context.Background()

	needed, err := service.NeedsSetup(ctx)
	if err != nil || !needed {
		t.Fatalf("fresh install should need setup: needed=%v err=%v", needed, err)
	}

	if _, err := service.CreateUser(ctx, "nadia", "a-long-enough-password", true); err != nil {
		t.Fatalf("create user: %v", err)
	}

	needed, err = service.NeedsSetup(ctx)
	if err != nil || needed {
		t.Errorf("setup still reported as needed: needed=%v err=%v", needed, err)
	}
}
