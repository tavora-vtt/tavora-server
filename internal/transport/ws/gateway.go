package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/tavora-vtt/tavora-server/internal/storage"
)

const (
	readLimitBytes = 1 << 20
	closeReasonMax = 120
)

type Gateway struct {
	deps       Deps
	devTickets bool
	allowJSON  bool
}

func NewGateway(deps Deps, devTickets bool) *Gateway {
	if deps.Router == nil {
		deps.Router = NewRouter()
		RegisterCoreIntents(deps.Router)
	}
	if deps.Tickets == nil {
		deps.Tickets = NewTicketStore(DefaultTicketTTL)
	}
	if deps.Registry == nil {
		deps.Registry = NewRegistry(deps.Log)
	}
	return &Gateway{deps: deps, devTickets: devTickets, allowJSON: devTickets}
}

func (g *Gateway) AllowJSONFormat(allow bool) {
	g.allowJSON = allow
}

func (g *Gateway) codecFor(r *http.Request) Codec {
	if r.URL.Query().Get("format") == "json" && g.allowJSON {
		return JSONCodec{}
	}
	return ProtoCodec{}
}

func (g *Gateway) Tickets() *TicketStore { return g.deps.Tickets }

func (g *Gateway) Registry() *Registry { return g.deps.Registry }

func (g *Gateway) Close() { g.deps.Registry.Close() }

type ticketRequest struct {
	UserID  string `json:"userId"`
	WorldID string `json:"worldId"`
	Role    string `json:"role"`
}

type ticketResponse struct {
	Ticket    string `json:"ticket"`
	ExpiresAt string `json:"expiresAt"`
}

func (g *Gateway) TicketHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !g.devTickets {
			writeJSON(w, http.StatusNotImplemented, map[string]string{
				"code":       "not_implemented",
				"messageKey": "core.auth.notAvailableYet",
			})
			return
		}

		var request ticketRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"code": CodeBadRequest})
			return
		}
		if request.UserID == "" || request.WorldID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"code": CodeBadRequest})
			return
		}
		if request.Role == "" {
			request.Role = "player"
		}

		token, expires, err := g.deps.Tickets.Issue(
			storage.ID(request.UserID), storage.ID(request.WorldID), request.Role)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"code": CodeInternal})
			return
		}

		writeJSON(w, http.StatusOK, ticketResponse{
			Ticket:    token,
			ExpiresAt: expires.UTC().Format(time.RFC3339),
		})
	}
}

func (g *Gateway) WebSocketHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: false,
			OriginPatterns:     nil,
		})
		if err != nil {
			g.deps.Log.Debug("websocket upgrade failed", "error", err)
			return
		}
		conn.SetReadLimit(readLimitBytes)

		session := NewSession(&coderConn{conn: conn}, g.codecFor(r), g.deps)

		if err := session.Serve(r.Context()); err != nil {
			g.deps.Log.Debug("session ended", "session", session.ID(), "error", err)
		}
	}
}

type coderConn struct {
	conn *websocket.Conn
}

func (c *coderConn) Read(ctx context.Context) ([]byte, bool, error) {
	messageType, data, err := c.conn.Read(ctx)
	if err != nil {
		return nil, false, err
	}
	return data, messageType == websocket.MessageBinary, nil
}

func (c *coderConn) Write(ctx context.Context, binary bool, data []byte) error {
	messageType := websocket.MessageText
	if binary {
		messageType = websocket.MessageBinary
	}
	return c.conn.Write(ctx, messageType, data)
}

func (c *coderConn) Close(reason string) error {
	if len(reason) > closeReasonMax {
		reason = reason[:closeReasonMax]
	}
	return c.conn.Close(websocket.StatusNormalClosure, reason)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
