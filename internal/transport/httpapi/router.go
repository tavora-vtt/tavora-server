package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

type ReadinessCheck func(ctx context.Context) error

type Deps struct {
	Log       *slog.Logger
	Ready     ReadinessCheck
	Backend   string
	Auth      AuthDeps
	WebSocket http.HandlerFunc
}

func NewRouter(deps Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if deps.Ready != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()

			if err := deps.Ready(ctx); err != nil {
				deps.Log.Warn("readiness check failed", "error", err)
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{
					"status": "unavailable",
					"reason": "storage",
				})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "ready",
			"storage": deps.Backend,
		})
	})

	if deps.Auth.Service != nil {
		mux.HandleFunc("GET /api/setup", deps.Auth.setupStateHandler())
		mux.HandleFunc("POST /api/setup", deps.Auth.setupHandler())
		mux.HandleFunc("POST /api/auth/login", deps.Auth.loginHandler())
		mux.HandleFunc("POST /api/auth/logout", deps.Auth.logoutHandler())
		mux.HandleFunc("GET /api/auth/me", deps.Auth.meHandler())
		mux.HandleFunc("POST /api/session/ticket", deps.Auth.ticketHandler())

		mux.HandleFunc("GET /api/worlds", deps.Auth.listWorldsHandler())
		mux.HandleFunc("POST /api/worlds", deps.Auth.createWorldHandler())
		mux.HandleFunc("GET /api/worlds/{worldId}/members", deps.Auth.listMembersHandler())
		mux.HandleFunc("PUT /api/worlds/{worldId}/members/{userId}", deps.Auth.setMemberRoleHandler())
		mux.HandleFunc("DELETE /api/worlds/{worldId}/members/{userId}", deps.Auth.removeMemberHandler())
		mux.HandleFunc("GET /api/worlds/{worldId}/invites", deps.Auth.listInvitesHandler())
		mux.HandleFunc("POST /api/worlds/{worldId}/invites", deps.Auth.createInviteHandler())
		mux.HandleFunc("DELETE /api/worlds/{worldId}/invites/{inviteId}", deps.Auth.revokeInviteHandler())

		mux.HandleFunc("GET /api/worlds/{worldId}/scenes", deps.Auth.listScenesHandler())
		mux.HandleFunc("POST /api/worlds/{worldId}/scenes", deps.Auth.createSceneHandler())
		mux.HandleFunc("GET /api/worlds/{worldId}/scenes/{sceneId}/tokens", deps.Auth.listTokensHandler())
		mux.HandleFunc("POST /api/worlds/{worldId}/scenes/{sceneId}/tokens", deps.Auth.createTokenHandler())

		mux.HandleFunc("GET /api/invites/{token}", deps.Auth.previewInviteHandler())
		mux.HandleFunc("POST /api/invites/{token}/accept", deps.Auth.acceptInviteHandler())
	}
	if deps.WebSocket != nil {
		mux.HandleFunc("GET /ws", deps.WebSocket)
	}

	var handler http.Handler = mux
	if deps.Auth.Service != nil {
		handler = deps.Auth.withUser(handler)
	}

	return withRequestLog(deps.Log, handler)
}

func withRequestLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		log.Debug("request",
			"method", r.Method,
			"path", r.URL.Path,
			"duration", time.Since(started),
		)
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
