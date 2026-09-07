package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"

	"github.com/tavora-vtt/tavora-server/internal/storage"
)

func (h *authHarness) anonymous(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	return &http.Client{Jar: jar}
}

func (h *authHarness) postAs(t *testing.T, client *http.Client, path, body string) *http.Response {
	t.Helper()
	response, err := client.Post(h.server.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post %s: %v", path, err)
	}
	return response
}

func (h *authHarness) do(t *testing.T, method, path, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, h.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := h.client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return response
}

func (h *authHarness) createWorld(t *testing.T) worldView {
	t.Helper()
	response := h.post(t, "/api/worlds", `{"title":"Blood and Rain","systemId":"wod5e"}`)
	defer response.Body.Close()

	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create world returned %d", response.StatusCode)
	}

	var world worldView
	if err := json.NewDecoder(response.Body).Decode(&world); err != nil {
		t.Fatalf("decode world: %v", err)
	}
	return world
}

func (h *authHarness) createInvite(t *testing.T, worldID, body string) inviteView {
	t.Helper()
	response := h.post(t, "/api/worlds/"+worldID+"/invites", body)
	defer response.Body.Close()

	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create invite returned %d", response.StatusCode)
	}

	var invite inviteView
	if err := json.NewDecoder(response.Body).Decode(&invite); err != nil {
		t.Fatalf("decode invite: %v", err)
	}
	return invite
}

func TestCreatingAWorldMakesTheCreatorItsGameMaster(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)
	h.signIn(t)

	world := h.createWorld(t)
	if world.Role != "gm" {
		t.Errorf("role = %q, want gm", world.Role)
	}
	if world.Slug != "blood-and-rain" {
		t.Errorf("slug = %q, want a slug derived from the title", world.Slug)
	}

	listed := h.get(t, "/api/worlds")
	defer listed.Body.Close()

	var worlds []worldView
	if err := json.NewDecoder(listed.Body).Decode(&worlds); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(worlds) != 1 || worlds[0].ID != world.ID {
		t.Errorf("worlds = %+v", worlds)
	}

	ticket := h.post(t, "/api/session/ticket", `{"worldId":"`+world.ID+`"}`)
	defer ticket.Body.Close()
	if ticket.StatusCode != http.StatusOK {
		t.Errorf("the creator cannot get a ticket for their own world: %d", ticket.StatusCode)
	}
}

func TestWorldListShowsOnlyOwnMemberships(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)
	h.signIn(t)
	h.createWorld(t)

	invite := h.createInvite(t, h.createWorld(t).ID, `{"role":"player"}`)

	outsider := h.anonymous(t)
	accepted := h.postAs(t, outsider, "/api/invites/"+invite.Token+"/accept",
		`{"username":"tomas","password":"a-long-enough-password"}`)
	accepted.Body.Close()
	if accepted.StatusCode != http.StatusOK {
		t.Fatalf("accept returned %d", accepted.StatusCode)
	}

	listed, err := outsider.Get(h.server.URL + "/api/worlds")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer listed.Body.Close()

	var worlds []worldView
	if err := json.NewDecoder(listed.Body).Decode(&worlds); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(worlds) != 1 {
		t.Errorf("the invited player sees %d worlds, want only the one they joined", len(worlds))
	}
	if len(worlds) == 1 && worlds[0].Role != "player" {
		t.Errorf("role = %q", worlds[0].Role)
	}
}

func TestInviteCreatesAnAccountAndAMembership(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)
	h.signIn(t)

	world := h.createWorld(t)
	invite := h.createInvite(t, world.ID, `{"role":"observer"}`)

	if invite.Token == "" {
		t.Fatal("no token returned to the creator")
	}

	preview := h.get(t, "/api/invites/"+invite.Token)
	var shown invitePreview
	_ = json.NewDecoder(preview.Body).Decode(&shown)
	preview.Body.Close()

	if !shown.Valid || shown.Role != "observer" || shown.WorldTitle != "Blood and Rain" {
		t.Errorf("preview = %+v", shown)
	}

	guest := h.anonymous(t)
	response := h.postAs(t, guest, "/api/invites/"+invite.Token+"/accept", `{"username":"tomas"}`)
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("accept returned %d", response.StatusCode)
	}

	var identity identity
	if err := json.NewDecoder(response.Body).Decode(&identity); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if identity.Username != "tomas" || identity.IsAdmin {
		t.Errorf("identity = %+v", identity)
	}

	me, err := guest.Get(h.server.URL + "/api/auth/me")
	if err != nil {
		t.Fatalf("me: %v", err)
	}
	me.Body.Close()
	if me.StatusCode != http.StatusOK {
		t.Errorf("accepting an invite did not sign the guest in: %d", me.StatusCode)
	}

	ticket := h.postAs(t, guest, "/api/session/ticket", `{"worldId":"`+world.ID+`"}`)
	defer ticket.Body.Close()
	if ticket.StatusCode != http.StatusOK {
		t.Fatalf("ticket returned %d", ticket.StatusCode)
	}

	var issued ticketResponse
	_ = json.NewDecoder(ticket.Body).Decode(&issued)
	if issued.Role != "observer" {
		t.Errorf("role = %q, want the role the invite carried", issued.Role)
	}
}

func TestPasswordlessInviteAccountCannotSignInWithAPassword(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)
	h.signIn(t)

	invite := h.createInvite(t, h.createWorld(t).ID, `{}`)

	guest := h.anonymous(t)
	accepted := h.postAs(t, guest, "/api/invites/"+invite.Token+"/accept", `{"username":"tomas"}`)
	accepted.Body.Close()

	attempt := h.post(t, "/api/auth/login", `{"username":"tomas","password":""}`)
	defer attempt.Body.Close()
	if attempt.StatusCode != http.StatusUnauthorized {
		t.Errorf("an account without a password signed in: %d", attempt.StatusCode)
	}
}

func TestSingleUseInviteIsSpentAfterOneAccept(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)
	h.signIn(t)

	invite := h.createInvite(t, h.createWorld(t).ID, `{"maxUses":1}`)

	first := h.postAs(t, h.anonymous(t), "/api/invites/"+invite.Token+"/accept",
		`{"username":"tomas","password":"a-long-enough-password"}`)
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first accept returned %d", first.StatusCode)
	}

	second := h.postAs(t, h.anonymous(t), "/api/invites/"+invite.Token+"/accept",
		`{"username":"mira","password":"a-long-enough-password"}`)
	defer second.Body.Close()
	if second.StatusCode != http.StatusGone {
		t.Errorf("second accept returned %d, want %d", second.StatusCode, http.StatusGone)
	}

	err := h.store.ReadOnly(context.Background(), func(q storage.Query) error {
		_, readErr := q.GetUserByUsername(context.Background(), "mira")
		return readErr
	})
	if err == nil {
		t.Error("a spent invite still created an account, the transaction did not roll back")
	}
}

func TestRevokedInviteIsRefused(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)
	h.signIn(t)

	world := h.createWorld(t)
	invite := h.createInvite(t, world.ID, `{}`)

	revoked := h.do(t, http.MethodDelete, "/api/worlds/"+world.ID+"/invites/"+invite.ID, "")
	revoked.Body.Close()
	if revoked.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke returned %d", revoked.StatusCode)
	}

	preview := h.get(t, "/api/invites/"+invite.Token)
	var shown invitePreview
	_ = json.NewDecoder(preview.Body).Decode(&shown)
	preview.Body.Close()
	if shown.Valid {
		t.Error("a revoked invite still previews as valid")
	}

	accepted := h.postAs(t, h.anonymous(t), "/api/invites/"+invite.Token+"/accept",
		`{"username":"tomas","password":"a-long-enough-password"}`)
	defer accepted.Body.Close()
	if accepted.StatusCode != http.StatusGone {
		t.Errorf("accept returned %d, want %d", accepted.StatusCode, http.StatusGone)
	}
}

func TestOnlyGameMastersManageMembership(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)
	h.signIn(t)

	world := h.createWorld(t)
	invite := h.createInvite(t, world.ID, `{"role":"player"}`)

	player := h.anonymous(t)
	accepted := h.postAs(t, player, "/api/invites/"+invite.Token+"/accept",
		`{"username":"tomas","password":"a-long-enough-password"}`)
	accepted.Body.Close()

	forbidden := h.postAs(t, player, "/api/worlds/"+world.ID+"/invites", `{"role":"gm"}`)
	defer forbidden.Body.Close()
	if forbidden.StatusCode != http.StatusForbidden {
		t.Errorf("a player created an invite: %d", forbidden.StatusCode)
	}

	members := h.get(t, "/api/worlds/"+world.ID+"/members")
	var listed []memberView
	_ = json.NewDecoder(members.Body).Decode(&listed)
	members.Body.Close()

	if len(listed) != 2 {
		t.Fatalf("members = %+v", listed)
	}

	var playerID string
	for _, member := range listed {
		if member.Role == "player" {
			playerID = member.UserID
		}
	}
	if playerID == "" {
		t.Fatal("the invited player is not listed")
	}

	promoted := h.do(t, http.MethodPut, "/api/worlds/"+world.ID+"/members/"+playerID, `{"role":"assistant"}`)
	promoted.Body.Close()
	if promoted.StatusCode != http.StatusNoContent {
		t.Errorf("promotion returned %d", promoted.StatusCode)
	}

	removed := h.do(t, http.MethodDelete, "/api/worlds/"+world.ID+"/members/"+playerID, "")
	removed.Body.Close()
	if removed.StatusCode != http.StatusNoContent {
		t.Errorf("removal returned %d", removed.StatusCode)
	}

	ticket := h.postAs(t, player, "/api/session/ticket", `{"worldId":"`+world.ID+`"}`)
	defer ticket.Body.Close()
	if ticket.StatusCode != http.StatusForbidden {
		t.Errorf("a removed member still gets a ticket: %d", ticket.StatusCode)
	}
}

func TestGameMasterCannotLockThemselvesOut(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)
	h.signIn(t)

	world := h.createWorld(t)

	me := h.get(t, "/api/auth/me")
	var own identity
	_ = json.NewDecoder(me.Body).Decode(&own)
	me.Body.Close()

	demote := h.do(t, http.MethodPut, "/api/worlds/"+world.ID+"/members/"+own.ID, `{"role":"player"}`)
	demote.Body.Close()
	if demote.StatusCode != http.StatusConflict {
		t.Errorf("self demotion returned %d, want %d", demote.StatusCode, http.StatusConflict)
	}

	remove := h.do(t, http.MethodDelete, "/api/worlds/"+world.ID+"/members/"+own.ID, "")
	remove.Body.Close()
	if remove.StatusCode != http.StatusConflict {
		t.Errorf("self removal returned %d, want %d", remove.StatusCode, http.StatusConflict)
	}
}

func TestNonMemberCannotSeeMembers(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)
	h.signIn(t)

	world := h.createWorld(t)
	invite := h.createInvite(t, world.ID, `{}`)

	stranger := h.anonymous(t)
	accepted := h.postAs(t, stranger, "/api/invites/"+invite.Token+"/accept",
		`{"username":"tomas","password":"a-long-enough-password"}`)
	accepted.Body.Close()

	other := h.createWorld(t)

	response, err := stranger.Get(h.server.URL + "/api/worlds/" + other.ID + "/members")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want %d", response.StatusCode, http.StatusForbidden)
	}
}

func TestTokenListingRespectsLineOfSight(t *testing.T) {
	h := newAuthHarness(t)
	h.seedAdmin(t)
	h.signIn(t)

	ctx := context.Background()
	world := h.createWorld(t)
	worldID := storage.ID(world.ID)

	invite := h.createInvite(t, world.ID, `{"role":"player"}`)
	player := h.anonymous(t)
	accepted := h.postAs(t, player, "/api/invites/"+invite.Token+"/accept",
		`{"username":"tomas","password":"a-long-enough-password"}`)
	accepted.Body.Close()

	me, err := player.Get(h.server.URL + "/api/auth/me")
	if err != nil {
		t.Fatalf("me: %v", err)
	}
	var playerIdentity identity
	_ = json.NewDecoder(me.Body).Decode(&playerIdentity)
	me.Body.Close()

	err = h.store.Tx(ctx, func(tx storage.Tx) error {
		if err := tx.PutDocument(ctx, &storage.Document{
			WorldID: worldID, ID: "scene-1", Kind: "scene", Name: "Chantry",
			Data:      json.RawMessage(`{"width":1000,"height":800,"gridSize":100}`),
			Ownership: json.RawMessage(`{"default":"observer"}`),
		}); err != nil {
			return err
		}
		if err := tx.PutDocument(ctx, &storage.Document{
			WorldID: worldID, ID: "wall-1", Kind: "wall", ParentID: "scene-1", Name: "Wall",
			Data:      json.RawMessage(`{"x1":5,"y1":0,"x2":5,"y2":20,"blocksSight":true}`),
			Ownership: json.RawMessage(`{"default":"observer"}`),
		}); err != nil {
			return err
		}
		if err := tx.PutDocument(ctx, &storage.Document{
			WorldID: worldID, ID: "mine", Kind: "token", ParentID: "scene-1", Name: "Tomas",
			Data:      json.RawMessage(`{"x":1,"y":5,"disposition":"friendly"}`),
			Ownership: json.RawMessage(`{"` + playerIdentity.ID + `":"owner"}`),
		}); err != nil {
			return err
		}
		return tx.PutDocument(ctx, &storage.Document{
			WorldID: worldID, ID: "hidden", Kind: "token", ParentID: "scene-1", Name: "Sheriff",
			Data:      json.RawMessage(`{"x":9,"y":5,"disposition":"hostile"}`),
			Ownership: json.RawMessage(`{"default":"observer"}`),
		})
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	response, err := player.Get(h.server.URL + "/api/worlds/" + world.ID + "/scenes/scene-1/tokens")
	if err != nil {
		t.Fatalf("list tokens: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()

	if strings.Contains(string(body), "Sheriff") {
		t.Errorf("the token listing leaked a token behind a wall:\n%s", body)
	}
	if !strings.Contains(string(body), "Tomas") {
		t.Errorf("the player's own token is missing:\n%s", body)
	}
}
