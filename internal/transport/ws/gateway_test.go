package ws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func newTestGateway(t *testing.T, devTickets bool) (*Gateway, *httptest.Server) {
	t.Helper()

	h := newHarness(t)
	gateway := NewGateway(h.deps, devTickets)
	t.Cleanup(gateway.Close)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/session/ticket", gateway.TicketHandler())
	mux.HandleFunc("GET /ws", gateway.WebSocketHandler())

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return gateway, server
}

func TestTicketEndpointIsOffUnlessExplicitlyEnabled(t *testing.T) {
	_, server := newTestGateway(t, false)

	response, err := http.Post(server.URL+"/api/session/ticket", "application/json",
		strings.NewReader(`{"userId":"user-1","worldId":"world-1"}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d, want %d: an unauthenticated ticket endpoint must not be reachable by default",
			response.StatusCode, http.StatusNotImplemented)
	}
}

func wsURLFor(server *httptest.Server, codec Codec) string {
	base := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	if codec.Name() == "json" {
		return base + "?format=json"
	}
	return base
}

func TestWebSocketRoundTripOverHTTP(t *testing.T) {
	for _, codec := range []Codec{ProtoCodec{}, JSONCodec{}} {
		t.Run(codec.Name(), func(t *testing.T) {
			_, server := newTestGateway(t, true)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			response, err := http.Post(server.URL+"/api/session/ticket", "application/json",
				strings.NewReader(`{"userId":"user-1","worldId":"world-1","role":"gm"}`))
			if err != nil {
				t.Fatalf("post: %v", err)
			}
			defer response.Body.Close()

			if response.StatusCode != http.StatusOK {
				t.Fatalf("ticket status = %d", response.StatusCode)
			}

			var issued ticketResponse
			if err := json.NewDecoder(response.Body).Decode(&issued); err != nil {
				t.Fatalf("decode ticket: %v", err)
			}

			conn, _, err := websocket.Dial(ctx, wsURLFor(server, codec), nil)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer conn.CloseNow()

			messageType := websocket.MessageText
			if codec.Binary() {
				messageType = websocket.MessageBinary
			}

			send := func(frame Frame) {
				t.Helper()
				data, err := codec.Encode(frame)
				if err != nil {
					t.Fatalf("encode: %v", err)
				}
				if err := conn.Write(ctx, messageType, data); err != nil {
					t.Fatalf("write: %v", err)
				}
			}

			receive := func(want FrameType) Frame {
				t.Helper()
				for i := 0; i < 10; i++ {
					_, data, err := conn.Read(ctx)
					if err != nil {
						t.Fatalf("read: %v", err)
					}
					frame, err := codec.Decode(data)
					if err != nil {
						t.Fatalf("decode: %v", err)
					}
					if frame.Type == want {
						return frame
					}
				}
				t.Fatalf("never saw a %s frame", want)
				return Frame{}
			}

			send(Frame{
				Lane: LaneControl,
				Type: TypeHello,
				Hello: &Hello{
					Ticket:          issued.Ticket,
					WorldID:         string(testWorld),
					ProtocolVersion: ProtocolVersion,
				},
			})

			welcome := receive(TypeWelcome)
			if welcome.Welcome.UserID != "user-1" || welcome.Welcome.Role != "gm" {
				t.Errorf("welcome = %+v", welcome.Welcome)
			}

			payload, _ := json.Marshal(DocumentPatchPayload{
				ID:  string(testDoc),
				Set: map[string]any{"hunger": 5},
			})
			send(Frame{
				Lane: LaneDocument,
				Type: TypeIntent,
				Intent: &Intent{
					RequestID: 1,
					Kind:      "document.patch",
					WorldID:   string(testWorld),
					Payload:   payload,
				},
			})

			ack := receive(TypeAck)
			if ack.Ack.RequestID != 1 {
				t.Errorf("ack request id = %d", ack.Ack.RequestID)
			}

			var result DocumentView
			if err := json.Unmarshal(ack.Ack.Result, &result); err != nil {
				t.Fatalf("decode result: %v", err)
			}

			var data map[string]any
			if err := json.Unmarshal(result.Data, &data); err != nil {
				t.Fatalf("decode data: %v", err)
			}
			if data["hunger"] != float64(5) {
				t.Errorf("hunger = %v, want 5", data["hunger"])
			}

			send(Frame{Lane: LaneControl, Type: TypePing, Ping: &Ping{SentAtUnixMs: 99}})
			pong := receive(TypePong)
			if pong.Ping.SentAtUnixMs != 99 {
				t.Errorf("pong = %d", pong.Ping.SentAtUnixMs)
			}
		})
	}
}

func TestJSONFormatIsRefusedWhenNotAllowed(t *testing.T) {
	gateway, server := newTestGateway(t, true)
	gateway.AllowJSONFormat(false)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURLFor(server, JSONCodec{}), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	data, _ := JSONCodec{}.Encode(Frame{
		Lane:  LaneControl,
		Type:  TypeHello,
		Hello: &Hello{Ticket: "anything", ProtocolVersion: ProtocolVersion},
	})
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, received, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := (JSONCodec{}).Decode(received); err == nil {
		t.Error("server answered in JSON although the format was not allowed")
	}
}

func TestWebSocketRejectsMissingTicket(t *testing.T) {
	_, server := newTestGateway(t, true)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	codec := ProtoCodec{}
	conn, _, err := websocket.Dial(ctx, wsURLFor(server, codec), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	data, _ := codec.Encode(Frame{
		Lane:  LaneControl,
		Type:  TypeHello,
		Hello: &Hello{Ticket: "forged", ProtocolVersion: ProtocolVersion},
	})
	if err := conn.Write(ctx, websocket.MessageBinary, data); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, received, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	frame, err := codec.Decode(received)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if frame.Type != TypeError || frame.Error.Code != CodeUnauthorized {
		t.Errorf("frame = %+v", frame)
	}
}
