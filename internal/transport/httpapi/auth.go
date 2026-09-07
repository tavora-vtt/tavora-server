package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/tavora-vtt/tavora-server/internal/core/access"
	"github.com/tavora-vtt/tavora-server/internal/core/auth"
	"github.com/tavora-vtt/tavora-server/internal/storage"
	"github.com/tavora-vtt/tavora-server/internal/transport/ws"
)

const (
	SessionCookie = "tavora_session"
	maxBodyBytes  = 8 << 10
)

type contextKey int

const userContextKey contextKey = iota

func UserFrom(ctx context.Context) (*storage.User, bool) {
	user, ok := ctx.Value(userContextKey).(*storage.User)
	return user, ok
}

type AuthDeps struct {
	Service       *auth.Service
	Store         storage.Store
	Tickets       *ws.TicketStore
	Access        *access.Resolver
	SecureCookies bool
}

func (d AuthDeps) issueCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   d.SecureCookies || r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

func (d AuthDeps) clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   d.SecureCookies || r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

func (d AuthDeps) withUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookie)
		if err != nil || cookie.Value == "" {
			next.ServeHTTP(w, r)
			return
		}

		user, err := d.Service.Authenticate(r.Context(), cookie.Value)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userContextKey, user)))
	})
}

func requireUser(w http.ResponseWriter, r *http.Request) (*storage.User, bool) {
	user, present := UserFrom(r.Context())
	if !present {
		writeJSON(w, http.StatusUnauthorized, apiError{
			Code:       "unauthorized",
			MessageKey: "core.auth.signInRequired",
		})
		return nil, false
	}
	return user, true
}

type apiError struct {
	Code       string `json:"code"`
	MessageKey string `json:"messageKey"`
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type identity struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	IsAdmin  bool   `json:"isAdmin"`
	Locale   string `json:"locale,omitempty"`
}

func identityOf(user *storage.User) identity {
	return identity{
		ID:       string(user.ID),
		Username: user.Username,
		IsAdmin:  user.IsAdmin,
		Locale:   user.Locale,
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{
			Code:       "bad_request",
			MessageKey: "core.api.malformedBody",
		})
		return false
	}
	return true
}

func (d AuthDeps) loginHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body credentials
		if !decodeBody(w, r, &body) {
			return
		}

		result, err := d.Service.Login(r.Context(), body.Username, body.Password,
			clientAddr(r), r.UserAgent())

		switch {
		case errors.Is(err, auth.ErrRateLimited):
			writeJSON(w, http.StatusTooManyRequests, apiError{
				Code:       "rate_limited",
				MessageKey: "core.auth.tooManyAttempts",
			})
			return
		case err != nil:
			writeJSON(w, http.StatusUnauthorized, apiError{
				Code:       "unauthorized",
				MessageKey: "core.auth.invalidCredentials",
			})
			return
		}

		d.issueCookie(w, r, result.Token, result.ExpiresAt)
		writeJSON(w, http.StatusOK, identityOf(result.User))
	}
}

func (d AuthDeps) logoutHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if cookie, err := r.Cookie(SessionCookie); err == nil {
			_ = d.Service.Logout(r.Context(), cookie.Value)
		}
		d.clearCookie(w, r)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d AuthDeps) meHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireUser(w, r)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, identityOf(user))
	}
}

type setupState struct {
	NeedsSetup bool `json:"needsSetup"`
}

func (d AuthDeps) setupStateHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		needed, err := d.Service.NeedsSetup(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{
				Code:       "internal",
				MessageKey: "core.api.internalError",
			})
			return
		}
		writeJSON(w, http.StatusOK, setupState{NeedsSetup: needed})
	}
}

func (d AuthDeps) setupHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		needed, err := d.Service.NeedsSetup(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{
				Code:       "internal",
				MessageKey: "core.api.internalError",
			})
			return
		}
		if !needed {
			writeJSON(w, http.StatusConflict, apiError{
				Code:       "already_configured",
				MessageKey: "core.setup.alreadyDone",
			})
			return
		}

		var body credentials
		if !decodeBody(w, r, &body) {
			return
		}

		var (
			user    *storage.User
			token   string
			expires time.Time
		)

		err = d.Store.Tx(r.Context(), func(tx storage.Tx) error {
			built, buildErr := d.Service.BuildUser(body.Username, body.Password, true)
			if buildErr != nil {
				return buildErr
			}
			if built.PasswordHash == "" {
				return auth.ErrWeakPassword
			}
			if insertErr := d.Service.InsertUser(r.Context(), tx, built); insertErr != nil {
				return insertErr
			}

			user = built
			token, expires, buildErr = d.Service.IssueSession(r.Context(), tx, built.ID, r.UserAgent())
			return buildErr
		})
		switch {
		case errors.Is(err, auth.ErrWeakPassword):
			writeJSON(w, http.StatusBadRequest, apiError{
				Code:       "weak_password",
				MessageKey: "core.auth.passwordTooShort",
			})
			return
		case errors.Is(err, auth.ErrUsernameTaken):
			writeJSON(w, http.StatusConflict, apiError{
				Code:       "username_taken",
				MessageKey: "core.auth.usernameTaken",
			})
			return
		case errors.Is(err, auth.ErrInvalidCredentials):
			writeJSON(w, http.StatusBadRequest, apiError{
				Code:       "bad_request",
				MessageKey: "core.auth.usernameRequired",
			})
			return
		case err != nil:
			writeJSON(w, http.StatusInternalServerError, apiError{
				Code:       "internal",
				MessageKey: "core.api.internalError",
			})
			return
		}

		d.issueCookie(w, r, token, expires)
		writeJSON(w, http.StatusCreated, identityOf(user))
	}
}

type ticketRequest struct {
	WorldID string `json:"worldId"`
}

type ticketResponse struct {
	Ticket    string `json:"ticket"`
	ExpiresAt string `json:"expiresAt"`
	WorldID   string `json:"worldId"`
	Role      string `json:"role"`
}

func (d AuthDeps) ticketHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := requireUser(w, r)
		if !ok {
			return
		}

		var body ticketRequest
		if !decodeBody(w, r, &body) {
			return
		}
		if body.WorldID == "" {
			writeJSON(w, http.StatusBadRequest, apiError{
				Code:       "bad_request",
				MessageKey: "core.api.missingWorldId",
			})
			return
		}

		var member *storage.Member
		err := d.Store.ReadOnly(r.Context(), func(q storage.Query) error {
			found, err := q.GetMember(r.Context(), storage.ID(body.WorldID), user.ID)
			if err != nil {
				return err
			}
			member = found
			return nil
		})
		if err != nil {
			writeJSON(w, http.StatusForbidden, apiError{
				Code:       "forbidden",
				MessageKey: "core.auth.notAMemberOfThisWorld",
			})
			return
		}

		token, expires, err := d.Tickets.Issue(user.ID, storage.ID(body.WorldID), member.Role)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{
				Code:       "internal",
				MessageKey: "core.api.internalError",
			})
			return
		}

		writeJSON(w, http.StatusOK, ticketResponse{
			Ticket:    token,
			ExpiresAt: expires.UTC().Format(time.RFC3339),
			WorldID:   body.WorldID,
			Role:      member.Role,
		})
	}
}

func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
