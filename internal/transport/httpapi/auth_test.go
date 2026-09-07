package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tavora-vtt/tavora-server/internal/core/access"
	"github.com/tavora-vtt/tavora-server/internal/core/auth"
	"github.com/tavora-vtt/tavora-server/internal/core/blob"
	"github.com/tavora-vtt/tavora-server/internal/core/perm"
	"github.com/tavora-vtt/tavora-server/internal/storage"
	"github.com/tavora-vtt/tavora-server/internal/storage/sqlite"
	"github.com/tavora-vtt/tavora-server/internal/transport/ws"
)

const testWorld = storage.ID("world-1")

const testWorldQuota = 8 << 20

type authHarness struct {
	server  *httptest.Server
	client  *http.Client
	service *auth.Service
	store   storage.Store
}

func newAuthHarness(t *testing.T) *authHarness {
	t.Helper()

	store, err := sqlite.Open(filepath.Join(t.TempDir(), "tavora.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	err = store.Tx(ctx, func(tx storage.Tx) error {
		return tx.PutWorld(ctx, &storage.World{
			ID: testWorld, Slug: "seeded-world", Title: "Seeded World",
			SystemID: "wod5e", SystemVersion: "0.0.0",
		})
	})
	if err != nil {
		t.Fatalf("seed world: %v", err)
	}

	blobs, err := blob.NewDisk(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatalf("open blob store: %v", err)
	}

	service := auth.NewService(store, auth.Options{
		HashParams: auth.HashParams{Memory: 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32},
	})

	router := NewRouter(Deps{
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Ready:   func(context.Context) error { return nil },
		Backend: "sqlite",
		Auth: AuthDeps{
			Service: service,
			Store:   store,
			Tickets: ws.NewTicketStore(ws.DefaultTicketTTL),
			Access:  access.NewResolver(store, perm.OpenPolicy{}),
			Assets:  AssetDeps{Blobs: blobs, WorldQuota: testWorldQuota},
		},
	})

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}

	return &authHarness{
		server:  server,
		client:  &http.Client{Jar: jar},
		service: service,
		store:   store,
	}
}

func (h *authHarness) post(t *testing.T, path, body string) *http.Response {
	t.Helper()
	response, err := h.client.Post(h.server.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post %s: %v", path, err)
	}
	return response
}

func (h *authHarness) get(t *testing.T, path string) *http.Response {
	t.Helper()
	response, err := h.client.Get(h.server.URL + path)
	if err != nil {
		t.Fatalf("get %s: %v", path, err)
	}
	return response
}

func (h *authHarness) seedAdmin(t *testing.T) {
	t.Helper()
	response := h.post(t, "/api/setup", `{"username":"nadia","password":"a-long-enough-password"}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("setup returned %d", response.StatusCode)
	}
}

func (h *authHarness) signIn(t *testing.T) {
	t.Helper()
	response := h.post(t, "/api/auth/login", `{"username":"nadia","password":"a-long-enough-password"}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login returned %d", response.StatusCode)
	}
}

func TestSetupIsAvailableOnceAndThenClosed(t *testing.T) {
	h := newAuthHarness(t)

	state := h.get(t, "/api/setup")
	var before setupState
	_ = json.NewDecoder(state.Body).Decode(&before)
	state.Body.Close()
	if !before.NeedsSetup {
		t.Fatal("a fresh install should report needsSetup")
	}

	h.seedAdmin(t)

	second := h.post(t, "/api/setup", `{"username":"someone","password":"another-long-password"}`)
	defer second.Body.Close()
	if second.StatusCode != http.StatusConflict {
		t.Errorf("second setup returned %d, want %d", second.StatusCode, http.StatusConflict)
	}

	state = h.get(t, "/api/setup")
	var after setupState
	_ = json.NewDecoder(state.Body).Decode(&after)
	state.Body.Close()
	if after.NeedsSetup {
		t.Error("setup still open after the first administrator was created")
	}
}

func TestSessionCookieIsHttpOnlyAndLax(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)

	response := h.post(t, "/api/auth/login", `{"username":"nadia","password":"a-long-enough-password"}`)
	defer response.Body.Close()

	var found *http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Name == SessionCookie {
			found = cookie
		}
	}
	if found == nil {
		t.Fatal("no session cookie issued")
	}
	if !found.HttpOnly {
		t.Error("session cookie is readable from JavaScript")
	}
	if found.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", found.SameSite)
	}
	if found.Path != "/" {
		t.Errorf("Path = %q", found.Path)
	}
	if strings.Contains(response.Header.Get("Set-Cookie"), "a-long-enough-password") {
		t.Error("the password appears in the cookie header")
	}
}

func TestMeRequiresASession(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)

	anonymous := &http.Client{}
	response, err := anonymous.Get(h.server.URL + "/api/auth/me")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}
}

func TestLogoutEndsTheSession(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)
	h.signIn(t)

	me := h.get(t, "/api/auth/me")
	me.Body.Close()
	if me.StatusCode != http.StatusOK {
		t.Fatalf("me returned %d before logout", me.StatusCode)
	}

	out := h.post(t, "/api/auth/logout", "")
	out.Body.Close()
	if out.StatusCode != http.StatusNoContent {
		t.Errorf("logout returned %d", out.StatusCode)
	}

	after := h.get(t, "/api/auth/me")
	after.Body.Close()
	if after.StatusCode != http.StatusUnauthorized {
		t.Errorf("me returned %d after logout, want %d", after.StatusCode, http.StatusUnauthorized)
	}
}

func TestWrongPasswordAndUnknownUserLookIdentical(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)

	wrong := h.post(t, "/api/auth/login", `{"username":"nadia","password":"not-the-password"}`)
	wrongBody, _ := io.ReadAll(wrong.Body)
	wrong.Body.Close()

	unknown := h.post(t, "/api/auth/login", `{"username":"nobody","password":"not-the-password"}`)
	unknownBody, _ := io.ReadAll(unknown.Body)
	unknown.Body.Close()

	if wrong.StatusCode != unknown.StatusCode {
		t.Errorf("status differs: %d against %d", wrong.StatusCode, unknown.StatusCode)
	}
	if string(wrongBody) != string(unknownBody) {
		t.Errorf("body differs and reveals whether the account exists:\n  %s\n  %s",
			wrongBody, unknownBody)
	}
}

func TestTicketRequiresASession(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)

	anonymous := &http.Client{}
	response, err := anonymous.Post(h.server.URL+"/api/session/ticket", "application/json",
		strings.NewReader(`{"worldId":"world-1"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d: a ticket must not be obtainable without signing in",
			response.StatusCode, http.StatusUnauthorized)
	}
}

func TestTicketRequiresMembership(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)
	h.signIn(t)

	response := h.post(t, "/api/session/ticket", `{"worldId":"world-1"}`)
	defer response.Body.Close()

	if response.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want %d: an administrator is not automatically a member of a world",
			response.StatusCode, http.StatusForbidden)
	}
}

func TestTicketCarriesTheRoleRecordedByTheWorld(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)
	h.signIn(t)

	ctx := context.Background()
	var admin *storage.User
	err := h.store.ReadOnly(ctx, func(q storage.Query) error {
		var readErr error
		admin, readErr = q.GetUserByUsername(ctx, "nadia")
		return readErr
	})
	if err != nil {
		t.Fatalf("load user: %v", err)
	}

	err = h.store.Tx(ctx, func(tx storage.Tx) error {
		return tx.PutMember(ctx, &storage.Member{
			WorldID: testWorld, UserID: admin.ID, Role: "player",
		})
	})
	if err != nil {
		t.Fatalf("add membership: %v", err)
	}

	response := h.post(t, "/api/session/ticket", `{"worldId":"world-1"}`)
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}

	var issued ticketResponse
	if err := json.NewDecoder(response.Body).Decode(&issued); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if issued.Ticket == "" {
		t.Error("no ticket issued")
	}
	if issued.Role != "player" {
		t.Errorf("role = %q, want player: a server administrator is still a player in this world", issued.Role)
	}
}

func TestHealthNeedsNoSession(t *testing.T) {
	h := newAuthHarness(t)

	anonymous := &http.Client{}
	for _, path := range []string{"/healthz", "/readyz"} {
		response, err := anonymous.Get(h.server.URL + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Errorf("%s returned %d", path, response.StatusCode)
		}
	}
}

func TestSetupSignsTheAdministratorIn(t *testing.T) {
	h := newAuthHarness(t)

	response := h.post(t, "/api/setup", `{"username":"nadia","password":"a-long-enough-password"}`)
	defer response.Body.Close()

	if response.StatusCode != http.StatusCreated {
		t.Fatalf("setup returned %d", response.StatusCode)
	}

	var issued *http.Cookie
	for _, cookie := range response.Cookies() {
		if cookie.Name == SessionCookie {
			issued = cookie
		}
	}
	if issued == nil {
		t.Fatal("setup did not issue a session, the first administrator would land on a 401")
	}

	me := h.get(t, "/api/auth/me")
	defer me.Body.Close()
	if me.StatusCode != http.StatusOK {
		t.Errorf("me returned %d right after setup", me.StatusCode)
	}
}
