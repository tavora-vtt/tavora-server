package ws

import (
	"context"
	"net/http"

	"github.com/coder/websocket"

	"github.com/tavora-vtt/tavora-server/internal/core/access"
	"github.com/tavora-vtt/tavora-server/internal/core/perm"
)

const (
	readLimitBytes = 1 << 20
	closeReasonMax = 120
)

type Gateway struct {
	deps      Deps
	allowJSON bool
}

func NewGateway(deps Deps) *Gateway {
	if deps.Router == nil {
		deps.Router = NewRouter()
		RegisterCoreIntents(deps.Router)
		RegisterChatIntents(deps.Router)
	}
	if deps.Tickets == nil {
		deps.Tickets = NewTicketStore(DefaultTicketTTL)
	}
	if deps.Registry == nil {
		deps.Registry = NewRegistry(deps.Log)
	}
	if deps.Access == nil {
		deps.Access = access.NewResolver(deps.Store, perm.OpenPolicy{})
	}
	return &Gateway{deps: deps}
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
