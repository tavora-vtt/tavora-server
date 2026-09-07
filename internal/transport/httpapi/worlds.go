package httpapi

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tavora-vtt/tavora-server/internal/core/auth"
	"github.com/tavora-vtt/tavora-server/internal/core/perm"
	"github.com/tavora-vtt/tavora-server/internal/storage"
)

const (
	DefaultInviteTTL = 7 * 24 * time.Hour
	maxInviteTTL     = 90 * 24 * time.Hour
)

type worldView struct {
	ID       string `json:"id"`
	Slug     string `json:"slug"`
	Title    string `json:"title"`
	SystemID string `json:"systemId"`
	Role     string `json:"role,omitempty"`
}

type createWorldRequest struct {
	Title    string `json:"title"`
	Slug     string `json:"slug"`
	SystemID string `json:"systemId"`
}

type memberView struct {
	UserID   string `json:"userId"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

type roleRequest struct {
	Role string `json:"role"`
}

type inviteView struct {
	ID        string `json:"id"`
	Role      string `json:"role"`
	ExpiresAt string `json:"expiresAt"`
	MaxUses   int    `json:"maxUses"`
	Uses      int    `json:"uses"`
	Revoked   bool   `json:"revoked"`
	Token     string `json:"token,omitempty"`
}

type createInviteRequest struct {
	Role       string `json:"role"`
	MaxUses    int    `json:"maxUses"`
	TTLSeconds int    `json:"ttlSeconds"`
}

type invitePreview struct {
	WorldTitle string `json:"worldTitle"`
	Role       string `json:"role"`
	Valid      bool   `json:"valid"`
}

type acceptInviteRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (d AuthDeps) memberOf(r *http.Request, worldID storage.ID, userID storage.ID) (*storage.Member, error) {
	var member *storage.Member
	err := d.Store.ReadOnly(r.Context(), func(q storage.Query) error {
		found, err := q.GetMember(r.Context(), worldID, userID)
		if err != nil {
			return err
		}
		member = found
		return nil
	})
	return member, err
}

func (d AuthDeps) requireRole(w http.ResponseWriter, r *http.Request, worldID storage.ID, gmOnly bool) (*storage.User, bool) {
	user, ok := requireUser(w, r)
	if !ok {
		return nil, false
	}

	member, err := d.memberOf(r, worldID, user.ID)
	if err != nil {
		writeJSON(w, http.StatusForbidden, apiError{
			Code:       "forbidden",
			MessageKey: "core.auth.notAMemberOfThisWorld",
		})
		return nil, false
	}

	if gmOnly && perm.Role(member.Role) != perm.RoleGM {
		writeJSON(w, http.StatusForbidden, apiError{
			Code:       "forbidden",
			MessageKey: "core.auth.gameMasterOnly",
		})
		return nil, false
	}

	return user, true
}

func (d AuthDeps) listWorldsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireUser(w, r)
		if !ok {
			return
		}

		var (
			worlds  []storage.World
			members []storage.Member
		)
		err := d.Store.ReadOnly(r.Context(), func(q storage.Query) error {
			var readErr error
			worlds, readErr = q.ListWorldsForUser(r.Context(), user.ID)
			if readErr != nil {
				return readErr
			}
			for _, world := range worlds {
				member, err := q.GetMember(r.Context(), world.ID, user.ID)
				if err != nil {
					return err
				}
				members = append(members, *member)
			}
			return nil
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		views := make([]worldView, 0, len(worlds))
		for index, world := range worlds {
			views = append(views, worldView{
				ID: string(world.ID), Slug: world.Slug, Title: world.Title,
				SystemID: world.SystemID, Role: members[index].Role,
			})
		}
		writeJSON(w, http.StatusOK, views)
	}
}

func (d AuthDeps) createWorldHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireUser(w, r)
		if !ok {
			return
		}

		var body createWorldRequest
		if !decodeBody(w, r, &body) {
			return
		}

		body.Title = strings.TrimSpace(body.Title)
		chosen := strings.TrimSpace(body.Slug) != ""
		body.Slug = slugify(body.Slug, body.Title)
		if body.Title == "" || body.Slug == "" || body.SystemID == "" {
			writeJSON(w, http.StatusBadRequest, apiError{
				Code:       "bad_request",
				MessageKey: "core.world.titleAndSystemRequired",
			})
			return
		}

		world := &storage.World{
			ID:            auth.GenerateID("world"),
			Slug:          body.Slug,
			Title:         body.Title,
			SystemID:      body.SystemID,
			SystemVersion: "0.0.0",
			CreatedAt:     time.Now().UTC(),
		}

		err := d.Store.Tx(r.Context(), func(tx storage.Tx) error {
			taken, err := takenSlugs(r, tx)
			if err != nil {
				return err
			}
			if taken[world.Slug] {
				if chosen {
					return storage.ErrAlreadyExists
				}
				world.Slug = nextFreeSlug(world.Slug, taken)
			}

			if err := tx.PutWorld(r.Context(), world); err != nil {
				return err
			}
			return tx.PutMember(r.Context(), &storage.Member{
				WorldID: world.ID, UserID: user.ID, Role: string(perm.RoleGM),
			})
		})
		if errors.Is(err, storage.ErrAlreadyExists) {
			writeJSON(w, http.StatusConflict, apiError{
				Code:       "slug_taken",
				MessageKey: "core.world.slugTaken",
			})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		writeJSON(w, http.StatusCreated, worldView{
			ID: string(world.ID), Slug: world.Slug, Title: world.Title,
			SystemID: world.SystemID, Role: string(perm.RoleGM),
		})
	}
}

func (d AuthDeps) listMembersHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		worldID := storage.ID(r.PathValue("worldId"))
		if _, ok := d.requireRole(w, r, worldID, false); !ok {
			return
		}

		var views []memberView
		err := d.Store.ReadOnly(r.Context(), func(q storage.Query) error {
			members, err := q.ListMembers(r.Context(), worldID)
			if err != nil {
				return err
			}
			views = make([]memberView, 0, len(members))
			for _, member := range members {
				name := string(member.UserID)
				if user, err := q.GetUser(r.Context(), member.UserID); err == nil {
					name = user.Username
				}
				views = append(views, memberView{
					UserID: string(member.UserID), Username: name, Role: member.Role,
				})
			}
			return nil
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		writeJSON(w, http.StatusOK, views)
	}
}

func (d AuthDeps) setMemberRoleHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		worldID := storage.ID(r.PathValue("worldId"))
		target := storage.ID(r.PathValue("userId"))

		actor, ok := d.requireRole(w, r, worldID, true)
		if !ok {
			return
		}

		var body roleRequest
		if !decodeBody(w, r, &body) {
			return
		}
		if !perm.Role(body.Role).Valid() {
			writeJSON(w, http.StatusBadRequest, apiError{
				Code:       "bad_request",
				MessageKey: "core.world.unknownRole",
			})
			return
		}
		if target == actor.ID && perm.Role(body.Role) != perm.RoleGM {
			writeJSON(w, http.StatusConflict, apiError{
				Code:       "would_lock_out",
				MessageKey: "core.world.cannotDemoteYourself",
			})
			return
		}

		err := d.Store.Tx(r.Context(), func(tx storage.Tx) error {
			if _, err := tx.GetUser(r.Context(), target); err != nil {
				return err
			}
			return tx.PutMember(r.Context(), &storage.Member{
				WorldID: worldID, UserID: target, Role: body.Role,
			})
		})
		if errors.Is(err, storage.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", MessageKey: "core.world.unknownUser"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

func (d AuthDeps) removeMemberHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		worldID := storage.ID(r.PathValue("worldId"))
		target := storage.ID(r.PathValue("userId"))

		actor, ok := d.requireRole(w, r, worldID, true)
		if !ok {
			return
		}
		if target == actor.ID {
			writeJSON(w, http.StatusConflict, apiError{
				Code:       "would_lock_out",
				MessageKey: "core.world.cannotRemoveYourself",
			})
			return
		}

		err := d.Store.Tx(r.Context(), func(tx storage.Tx) error {
			return tx.DeleteMember(r.Context(), worldID, target)
		})
		if errors.Is(err, storage.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", MessageKey: "core.world.notAMember"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

func (d AuthDeps) createInviteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		worldID := storage.ID(r.PathValue("worldId"))

		actor, ok := d.requireRole(w, r, worldID, true)
		if !ok {
			return
		}

		var body createInviteRequest
		if !decodeBody(w, r, &body) {
			return
		}
		if body.Role == "" {
			body.Role = string(perm.RolePlayer)
		}
		if !perm.Role(body.Role).Valid() {
			writeJSON(w, http.StatusBadRequest, apiError{Code: "bad_request", MessageKey: "core.world.unknownRole"})
			return
		}
		if body.MaxUses < 0 {
			body.MaxUses = 0
		}

		ttl := DefaultInviteTTL
		if body.TTLSeconds > 0 {
			ttl = time.Duration(body.TTLSeconds) * time.Second
		}
		if ttl > maxInviteTTL {
			ttl = maxInviteTTL
		}

		token, err := auth.GenerateToken()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		invite := &storage.Invite{
			ID:        auth.GenerateID("invite"),
			TokenHash: auth.HashToken(token),
			WorldID:   worldID,
			Role:      body.Role,
			CreatedBy: actor.ID,
			CreatedAt: time.Now().UTC(),
			ExpiresAt: time.Now().UTC().Add(ttl),
			MaxUses:   body.MaxUses,
		}

		if err := d.Store.Tx(r.Context(), func(tx storage.Tx) error {
			return tx.PutInvite(r.Context(), invite)
		}); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		writeJSON(w, http.StatusCreated, inviteView{
			ID:        string(invite.ID),
			Role:      invite.Role,
			ExpiresAt: invite.ExpiresAt.Format(time.RFC3339),
			MaxUses:   invite.MaxUses,
			Token:     token,
		})
	}
}

func (d AuthDeps) listInvitesHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		worldID := storage.ID(r.PathValue("worldId"))
		if _, ok := d.requireRole(w, r, worldID, true); !ok {
			return
		}

		var views []inviteView
		err := d.Store.ReadOnly(r.Context(), func(q storage.Query) error {
			invites, err := q.ListInvites(r.Context(), worldID)
			if err != nil {
				return err
			}
			views = make([]inviteView, 0, len(invites))
			for _, invite := range invites {
				views = append(views, inviteView{
					ID:        string(invite.ID),
					Role:      invite.Role,
					ExpiresAt: invite.ExpiresAt.Format(time.RFC3339),
					MaxUses:   invite.MaxUses,
					Uses:      invite.Uses,
					Revoked:   invite.RevokedAt != nil,
				})
			}
			return nil
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		writeJSON(w, http.StatusOK, views)
	}
}

func (d AuthDeps) revokeInviteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		worldID := storage.ID(r.PathValue("worldId"))
		inviteID := storage.ID(r.PathValue("inviteId"))

		if _, ok := d.requireRole(w, r, worldID, true); !ok {
			return
		}

		err := d.Store.Tx(r.Context(), func(tx storage.Tx) error {
			return tx.RevokeInvite(r.Context(), worldID, inviteID)
		})
		if errors.Is(err, storage.ErrNotFound) {
			writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", MessageKey: "core.world.unknownInvite"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

func (d AuthDeps) previewInviteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hash := auth.HashToken(r.PathValue("token"))

		var (
			invite *storage.Invite
			world  *storage.World
		)
		err := d.Store.ReadOnly(r.Context(), func(q storage.Query) error {
			found, err := q.GetInviteByTokenHash(r.Context(), hash)
			if err != nil {
				return err
			}
			invite = found
			world, err = q.GetWorld(r.Context(), invite.WorldID)
			return err
		})
		if err != nil {
			writeJSON(w, http.StatusNotFound, apiError{Code: "not_found", MessageKey: "core.invite.unknown"})
			return
		}

		writeJSON(w, http.StatusOK, invitePreview{
			WorldTitle: world.Title,
			Role:       invite.Role,
			Valid:      invite.Usable(time.Now().UTC()),
		})
	}
}

func (d AuthDeps) acceptInviteHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hash := auth.HashToken(r.PathValue("token"))

		var body acceptInviteRequest
		if !decodeBody(w, r, &body) {
			return
		}

		existing, signedIn := UserFrom(r.Context())

		var (
			token   string
			expires time.Time
			joined  *storage.User
		)

		err := d.Store.Tx(r.Context(), func(tx storage.Tx) error {
			invite, err := tx.GetInviteByTokenHash(r.Context(), hash)
			if err != nil {
				return err
			}
			if !invite.Usable(time.Now().UTC()) {
				return errInviteSpent
			}

			user := existing
			if !signedIn {
				built, err := d.Service.BuildUser(body.Username, body.Password, false)
				if err != nil {
					return err
				}
				if err := d.Service.InsertUser(r.Context(), tx, built); err != nil {
					return err
				}
				user = built

				token, expires, err = d.Service.IssueSession(r.Context(), tx, built.ID, r.UserAgent())
				if err != nil {
					return err
				}
			}

			if err := tx.PutMember(r.Context(), &storage.Member{
				WorldID: invite.WorldID, UserID: user.ID, Role: invite.Role,
			}); err != nil {
				return err
			}

			joined = user
			return tx.ConsumeInvite(r.Context(), invite.WorldID, invite.ID)
		})

		switch {
		case errors.Is(err, errInviteSpent), errors.Is(err, storage.ErrNotFound):
			writeJSON(w, http.StatusGone, apiError{Code: "invite_spent", MessageKey: "core.invite.noLongerValid"})
			return
		case errors.Is(err, auth.ErrUsernameTaken):
			writeJSON(w, http.StatusConflict, apiError{Code: "username_taken", MessageKey: "core.auth.usernameTaken"})
			return
		case errors.Is(err, auth.ErrWeakPassword):
			writeJSON(w, http.StatusBadRequest, apiError{Code: "weak_password", MessageKey: "core.auth.passwordTooShort"})
			return
		case errors.Is(err, auth.ErrInvalidCredentials):
			writeJSON(w, http.StatusBadRequest, apiError{Code: "bad_request", MessageKey: "core.auth.usernameRequired"})
			return
		case err != nil:
			writeJSON(w, http.StatusInternalServerError, apiError{Code: "internal", MessageKey: "core.api.internalError"})
			return
		}

		if token != "" {
			d.issueCookie(w, r, token, expires)
		}
		writeJSON(w, http.StatusOK, identityOf(joined))
	}
}

var errInviteSpent = errors.New("httpapi: invite is no longer usable")

func takenSlugs(r *http.Request, q storage.Query) (map[string]bool, error) {
	worlds, err := q.ListWorlds(r.Context())
	if err != nil {
		return nil, err
	}

	taken := make(map[string]bool, len(worlds))
	for _, world := range worlds {
		taken[world.Slug] = true
	}
	return taken, nil
}

func nextFreeSlug(base string, taken map[string]bool) string {
	for suffix := 2; ; suffix++ {
		candidate := base + "-" + strconv.Itoa(suffix)
		if !taken[candidate] {
			return candidate
		}
	}
}

func slugify(slug, fallback string) string {
	source := slug
	if strings.TrimSpace(source) == "" {
		source = fallback
	}

	var builder strings.Builder
	previousDash := false

	for _, r := range strings.ToLower(strings.TrimSpace(source)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			previousDash = false
		default:
			if !previousDash && builder.Len() > 0 {
				builder.WriteByte('-')
				previousDash = true
			}
		}
	}

	return strings.Trim(builder.String(), "-")
}
