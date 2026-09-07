package ws

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tavora-vtt/tavora-server/internal/core/access"
	"github.com/tavora-vtt/tavora-server/internal/core/perm"
	"github.com/tavora-vtt/tavora-server/internal/storage"
	"github.com/tavora-vtt/tavora-server/internal/storage/sqlite"
)

const (
	testWorld = storage.ID("world-1")
	testDoc   = storage.ID("actor-1")
	waitFor   = 3 * time.Second
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakeConn struct {
	incoming chan []byte
	outgoing chan []byte
	closed   atomic.Bool
}

func newFakeConn() *fakeConn {
	return &fakeConn{
		incoming: make(chan []byte, 16),
		outgoing: make(chan []byte, 1024),
	}
}

func (c *fakeConn) Read(ctx context.Context) ([]byte, bool, error) {
	select {
	case data, open := <-c.incoming:
		if !open {
			return nil, false, io.EOF
		}
		return data, false, nil
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
}

func (c *fakeConn) Write(ctx context.Context, _ bool, data []byte) error {
	if c.closed.Load() {
		return errors.New("connection closed")
	}
	select {
	case c.outgoing <- append([]byte(nil), data...):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *fakeConn) Close(string) error {
	c.closed.Store(true)
	return nil
}

func (c *fakeConn) send(t *testing.T, frame Frame) {
	t.Helper()
	data, err := (JSONCodec{}).Encode(frame)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	select {
	case c.incoming <- data:
	case <-time.After(waitFor):
		t.Fatal("send blocked")
	}
}

func (c *fakeConn) next(t *testing.T) Frame {
	t.Helper()
	select {
	case data := <-c.outgoing:
		frame, err := (JSONCodec{}).Decode(data)
		if err != nil {
			t.Fatalf("decode %s: %v", data, err)
		}
		return frame
	case <-time.After(waitFor):
		t.Fatal("timed out waiting for a frame")
		return Frame{}
	}
}

func (c *fakeConn) nextOfType(t *testing.T, want FrameType) Frame {
	t.Helper()
	deadline := time.After(waitFor)
	for {
		select {
		case data := <-c.outgoing:
			frame, err := (JSONCodec{}).Decode(data)
			if err != nil {
				t.Fatalf("decode %s: %v", data, err)
			}
			if frame.Type == want {
				return frame
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a %s frame", want)
			return Frame{}
		}
	}
}

func newTestStore(t *testing.T) storage.Store {
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
		if err := tx.PutWorld(ctx, &storage.World{
			ID: testWorld, Slug: "blood-and-rain", Title: "Blood and Rain",
			SystemID: "wod5e", SystemVersion: "0.0.0",
		}); err != nil {
			return err
		}

		members := []storage.Member{
			{WorldID: testWorld, UserID: "user-1", Role: string(perm.RoleGM)},
			{WorldID: testWorld, UserID: "user-2", Role: string(perm.RolePlayer)},
			{WorldID: testWorld, UserID: "user-3", Role: string(perm.RolePlayer)},
		}
		for i := range members {
			if err := tx.PutMember(ctx, &members[i]); err != nil {
				return err
			}
		}

		return tx.PutDocument(ctx, &storage.Document{
			WorldID: testWorld, ID: testDoc, Kind: "actor", Subtype: "vampire",
			Name:      "Nadia",
			Data:      json.RawMessage(`{"hunger":2,"clan":"Ventrue","secrets":{"sire":"unknown"}}`),
			Ownership: json.RawMessage(`{"user-2":"observer"}`),
		})
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	return store
}

func testPolicy() perm.FieldPolicy {
	return perm.NewStaticPolicy().
		Set("actor", "vampire", "secrets", perm.VisibilityGM).
		Set("actor", "vampire", "hunger", perm.VisibilityObserver)
}

type harness struct {
	deps     Deps
	registry *Registry
	tickets  *TicketStore
	store    storage.Store
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	store := newTestStore(t)
	registry := NewRegistry(discardLogger())
	t.Cleanup(registry.Close)

	router := NewRouter()
	RegisterCoreIntents(router)
	RegisterChatIntents(router)
	RegisterCombatIntents(router)

	tickets := NewTicketStore(DefaultTicketTTL)

	return &harness{
		deps: Deps{
			Store:    store,
			Registry: registry,
			Tickets:  tickets,
			Router:   router,
			Access:   access.NewResolver(store, testPolicy()),
			Log:      discardLogger(),
		},
		registry: registry,
		tickets:  tickets,
		store:    store,
	}
}

func (h *harness) connect(t *testing.T, userID string, lastSeq int64) (*fakeConn, *Session) {
	t.Helper()

	token, _, err := h.tickets.Issue(storage.ID(userID), testWorld, "player")
	if err != nil {
		t.Fatalf("issue ticket: %v", err)
	}

	conn := newFakeConn()
	session := NewSession(conn, JSONCodec{}, h.deps)

	go func() { _ = session.Serve(context.Background()) }()

	conn.send(t, Frame{
		Lane: LaneControl,
		Type: TypeHello,
		Hello: &Hello{
			Ticket:          token,
			WorldID:         string(testWorld),
			ProtocolVersion: ProtocolVersion,
			LastSeq:         lastSeq,
		},
	})

	welcome := conn.nextOfType(t, TypeWelcome)
	if welcome.Welcome.UserID != userID {
		t.Fatalf("welcome user = %q", welcome.Welcome.UserID)
	}
	return conn, session
}

func TestTicketIsSingleUse(t *testing.T) {
	store := NewTicketStore(time.Minute)

	token, _, err := store.Issue("user-1", testWorld, "gm")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	ticket, err := store.Redeem(token)
	if err != nil {
		t.Fatalf("first redeem: %v", err)
	}
	if ticket.UserID != "user-1" || ticket.Role != "gm" {
		t.Errorf("ticket = %+v", ticket)
	}

	if _, err := store.Redeem(token); !errors.Is(err, ErrTicketInvalid) {
		t.Errorf("second redeem returned %v, want ErrTicketInvalid", err)
	}
}

func TestExpiredTicketIsRejected(t *testing.T) {
	store := NewTicketStore(time.Minute)
	now := time.Now()
	store.now = func() time.Time { return now }

	token, _, err := store.Issue("user-1", testWorld, "player")
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	now = now.Add(2 * time.Minute)

	if _, err := store.Redeem(token); !errors.Is(err, ErrTicketInvalid) {
		t.Errorf("redeem returned %v, want ErrTicketInvalid", err)
	}
	if store.Len() != 0 {
		t.Errorf("expired ticket left behind: %d", store.Len())
	}
}

func TestOutboxPrioritisesControlOverDocument(t *testing.T) {
	box := newOutbox(8, 8)

	_ = box.PushDocument(Frame{Type: TypeEvent})
	_ = box.PushEphemeral("token:1", Frame{Type: TypeEphemeral})
	_ = box.PushControl(Frame{Type: TypeResync})

	order := make([]FrameType, 0, 3)
	for {
		frame, present := box.Pop()
		if !present {
			break
		}
		order = append(order, frame.Type)
	}

	want := []FrameType{TypeResync, TypeEvent, TypeEphemeral}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

func TestOutboxCoalescesEphemeralByKey(t *testing.T) {
	box := newOutbox(8, 8)

	for i := 0; i < 20; i++ {
		payload, _ := json.Marshal(map[string]int{"x": i})
		if err := box.PushEphemeral("token:1", Frame{
			Type:      TypeEphemeral,
			Ephemeral: &Ephemeral{Key: "token:1", Payload: payload},
		}); err != nil {
			t.Fatalf("push: %v", err)
		}
	}

	if _, _, ephemeral := box.Depth(); ephemeral != 1 {
		t.Fatalf("ephemeral depth = %d, want 1", ephemeral)
	}

	frame, present := box.Pop()
	if !present {
		t.Fatal("nothing queued")
	}

	var decoded map[string]int
	if err := json.Unmarshal(frame.Ephemeral.Payload, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["x"] != 19 {
		t.Errorf("kept x = %d, want the newest value 19", decoded["x"])
	}
}

func TestOutboxEvictsOldestEphemeralKey(t *testing.T) {
	box := newOutbox(8, 2)

	_ = box.PushEphemeral("a", Frame{Type: TypeEphemeral})
	_ = box.PushEphemeral("b", Frame{Type: TypeEphemeral})
	_ = box.PushEphemeral("c", Frame{Type: TypeEphemeral})

	if _, _, ephemeral := box.Depth(); ephemeral != 2 {
		t.Errorf("ephemeral depth = %d, want 2", ephemeral)
	}
	if box.DroppedEphemeral() != 1 {
		t.Errorf("dropped = %d, want 1", box.DroppedEphemeral())
	}
}

func TestOutboxDocumentLaneNeverDrops(t *testing.T) {
	box := newOutbox(2, 8)

	if err := box.PushDocument(Frame{Type: TypeEvent}); err != nil {
		t.Fatalf("first push: %v", err)
	}
	if err := box.PushDocument(Frame{Type: TypeEvent}); err != nil {
		t.Fatalf("second push: %v", err)
	}

	err := box.PushDocument(Frame{Type: TypeEvent})
	if !errors.Is(err, ErrSlowConsumer) {
		t.Fatalf("third push returned %v, want ErrSlowConsumer", err)
	}
	if !box.Overflowed() {
		t.Error("overflow not recorded")
	}
	if _, document, _ := box.Depth(); document != 2 {
		t.Errorf("document depth = %d, the lane must not drop", document)
	}
}

func TestHandshakeRejectsUnknownTicket(t *testing.T) {
	h := newHarness(t)

	conn := newFakeConn()
	session := NewSession(conn, JSONCodec{}, h.deps)

	done := make(chan error, 1)
	go func() { done <- session.Serve(context.Background()) }()

	conn.send(t, Frame{
		Lane:  LaneControl,
		Type:  TypeHello,
		Hello: &Hello{Ticket: "not-a-ticket", ProtocolVersion: ProtocolVersion},
	})

	frame := conn.nextOfType(t, TypeError)
	if frame.Error.Code != CodeUnauthorized {
		t.Errorf("error code = %q, want %q", frame.Error.Code, CodeUnauthorized)
	}

	select {
	case err := <-done:
		if !errors.Is(err, ErrTicketInvalid) {
			t.Errorf("serve returned %v", err)
		}
	case <-time.After(waitFor):
		t.Fatal("session did not end")
	}
}

func TestHandshakeRejectsUnsupportedProtocolVersion(t *testing.T) {
	h := newHarness(t)

	token, _, _ := h.tickets.Issue("user-1", testWorld, "player")

	conn := newFakeConn()
	session := NewSession(conn, JSONCodec{}, h.deps)
	go func() { _ = session.Serve(context.Background()) }()

	conn.send(t, Frame{
		Lane:  LaneControl,
		Type:  TypeHello,
		Hello: &Hello{Ticket: token, ProtocolVersion: ProtocolVersion + 1},
	})

	frame := conn.nextOfType(t, TypeError)
	if frame.Error.Code != CodeBadRequest {
		t.Errorf("error code = %q", frame.Error.Code)
	}
}

func TestPatchAcksAndFansOutToOtherSession(t *testing.T) {
	h := newHarness(t)

	author, _ := h.connect(t, "user-1", 0)
	watcher, _ := h.connect(t, "user-2", 0)

	payload, _ := json.Marshal(DocumentPatchPayload{
		ID:  string(testDoc),
		Set: map[string]any{"hunger": 4},
	})

	author.send(t, Frame{
		Lane: LaneDocument,
		Type: TypeIntent,
		Intent: &Intent{
			RequestID: 17,
			Kind:      "document.patch",
			WorldID:   string(testWorld),
			Payload:   payload,
		},
	})

	ack := author.nextOfType(t, TypeAck)
	if ack.Ack.RequestID != 17 {
		t.Errorf("ack request id = %d", ack.Ack.RequestID)
	}
	if ack.Ack.Seq != 1 {
		t.Errorf("ack seq = %d, want the sequence the intent produced", ack.Ack.Seq)
	}

	var result DocumentView
	if err := json.Unmarshal(ack.Ack.Result, &result); err != nil {
		t.Fatalf("decode ack result: %v", err)
	}
	if result.Seq != 1 {
		t.Errorf("event sequence = %d, want 1", result.Seq)
	}

	var data map[string]any
	if err := json.Unmarshal(result.Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if data["hunger"] != float64(4) {
		t.Errorf("hunger = %v, want 4", data["hunger"])
	}

	event := watcher.nextOfType(t, TypeEvent)
	if event.Event.Kind != "document.patch" || event.Event.Seq != 1 {
		t.Errorf("event = %+v", event.Event)
	}

	stored := loadDocument(t, h.store)
	if stored.UpdatedSeq != 1 {
		t.Errorf("stored updated seq = %d, want 1", stored.UpdatedSeq)
	}
}

func TestReconnectCatchesUpFromLastSeq(t *testing.T) {
	h := newHarness(t)

	author, _ := h.connect(t, "user-1", 0)

	for i := 0; i < 3; i++ {
		payload, _ := json.Marshal(DocumentPatchPayload{
			ID:  string(testDoc),
			Set: map[string]any{"hunger": i},
		})
		author.send(t, Frame{
			Lane:   LaneDocument,
			Type:   TypeIntent,
			Intent: &Intent{RequestID: uint32(i + 1), Kind: "document.patch", Payload: payload},
		})
		author.nextOfType(t, TypeAck)
	}

	returning, _ := h.connect(t, "user-2", 1)

	seen := make([]int64, 0, 2)
	for i := 0; i < 2; i++ {
		frame := returning.nextOfType(t, TypeEvent)
		seen = append(seen, frame.Event.Seq)
	}

	if len(seen) != 2 || seen[0] != 2 || seen[1] != 3 {
		t.Errorf("replayed sequences = %v, want [2 3]", seen)
	}
}

func TestUnknownIntentIsRejected(t *testing.T) {
	h := newHarness(t)
	conn, _ := h.connect(t, "user-1", 0)

	conn.send(t, Frame{
		Lane:   LaneDocument,
		Type:   TypeIntent,
		Intent: &Intent{RequestID: 3, Kind: "does.not.exist", Payload: json.RawMessage(`{}`)},
	})

	frame := conn.nextOfType(t, TypeError)
	if frame.Error.Code != CodeUnsupported {
		t.Errorf("error code = %q, want %q", frame.Error.Code, CodeUnsupported)
	}
	if frame.Error.RequestID != 3 {
		t.Errorf("error request id = %d", frame.Error.RequestID)
	}
}

func TestPatchOfMissingDocumentIsNotFound(t *testing.T) {
	h := newHarness(t)
	conn, _ := h.connect(t, "user-1", 0)

	payload, _ := json.Marshal(DocumentPatchPayload{ID: "nope", Set: map[string]any{"hunger": 1}})
	conn.send(t, Frame{
		Lane:   LaneDocument,
		Type:   TypeIntent,
		Intent: &Intent{RequestID: 5, Kind: "document.patch", Payload: payload},
	})

	frame := conn.nextOfType(t, TypeError)
	if frame.Error.Code != CodeNotFound {
		t.Errorf("error code = %q", frame.Error.Code)
	}

	stored := loadDocument(t, h.store)
	if stored.UpdatedSeq != 0 {
		t.Errorf("failed patch advanced the sequence to %d", stored.UpdatedSeq)
	}
}

func TestEphemeralReachesOtherSessionsOnly(t *testing.T) {
	h := newHarness(t)

	sender, _ := h.connect(t, "user-1", 0)
	receiver, _ := h.connect(t, "user-2", 0)

	sender.send(t, Frame{
		Lane: LaneEphemeral,
		Type: TypeEphemeral,
		Ephemeral: &Ephemeral{
			Kind:    "cursor",
			Key:     "cursor:user-1",
			WorldID: string(testWorld),
			Payload: json.RawMessage(`{"x":12,"y":30}`),
		},
	})

	frame := receiver.nextOfType(t, TypeEphemeral)
	if frame.Ephemeral.Key != "cursor:user-1" {
		t.Errorf("key = %q", frame.Ephemeral.Key)
	}
	if frame.Lane != LaneEphemeral {
		t.Errorf("lane = %v", frame.Lane)
	}

	select {
	case echo := <-sender.outgoing:
		decoded, _ := (JSONCodec{}).Decode(echo)
		if decoded.Type == TypeEphemeral {
			t.Error("sender received its own ephemeral frame")
		}
	case <-time.After(200 * time.Millisecond):
	}
}

func TestSlowConsumerIsClosedWithResync(t *testing.T) {
	h := newHarness(t)
	h.deps.DocumentCap = 2

	conn := newFakeConn()
	session := NewSession(conn, JSONCodec{}, h.deps)

	session.userID = "user-1"
	session.worldID = testWorld
	session.Subscribe(WorldChannel(testWorld))

	hub := h.registry.Hub(testWorld)
	hub.Join(session)

	for i := 0; i < 10; i++ {
		hub.Publish(Publication{
			Channel: WorldChannel(testWorld),
			Lane:    LaneDocument,
			Frame:   Frame{Lane: LaneDocument, Type: TypeEvent, Event: &Event{Seq: int64(i + 1)}},
		})
	}

	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		if session.outbox.Closed() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !session.outbox.Closed() {
		t.Fatal("slow session was not closed")
	}

	var sawResync bool
	for {
		frame, present := session.outbox.Pop()
		if !present {
			break
		}
		if frame.Type == TypeResync {
			sawResync = true
			if frame.Resync.Reason != "slow consumer" {
				t.Errorf("resync reason = %q", frame.Resync.Reason)
			}
		}
	}
	if !sawResync {
		t.Error("no resync instruction was queued")
	}

	if sessions := hub.Sessions(); len(sessions) != 0 {
		t.Errorf("hub still holds %d sessions", len(sessions))
	}
}

func TestPingIsAnsweredOnControlLane(t *testing.T) {
	h := newHarness(t)
	conn, _ := h.connect(t, "user-1", 0)

	conn.send(t, Frame{Lane: LaneControl, Type: TypePing, Ping: &Ping{SentAtUnixMs: 4242}})

	frame := conn.nextOfType(t, TypePong)
	if frame.Ping.SentAtUnixMs != 4242 {
		t.Errorf("pong echoed %d", frame.Ping.SentAtUnixMs)
	}
	if frame.Lane != LaneControl {
		t.Errorf("pong lane = %v", frame.Lane)
	}
}

func loadDocument(t *testing.T, store storage.Store) *storage.Document {
	t.Helper()

	var doc *storage.Document
	err := store.ReadOnly(context.Background(), func(q storage.Query) error {
		var readErr error
		doc, readErr = q.GetDocument(context.Background(), testWorld, testDoc)
		return readErr
	})
	if err != nil {
		t.Fatalf("load document: %v", err)
	}
	return doc
}

func TestSceneActivateReachesEveryone(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	err := h.store.Tx(ctx, func(tx storage.Tx) error {
		for _, id := range []storage.ID{"scene-1", "scene-2"} {
			if err := tx.PutDocument(ctx, &storage.Document{
				WorldID: testWorld, ID: id, Kind: "scene", Name: string(id),
				Data:      json.RawMessage(`{"width":1000,"height":800,"gridSize":100}`),
				Ownership: json.RawMessage(`{"default":"observer"}`),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed scenes: %v", err)
	}

	gm, _ := h.connect(t, "user-1", 0)
	player, _ := h.connect(t, "user-2", 0)

	payload, _ := json.Marshal(SceneActivatePayload{SceneID: "scene-2"})
	gm.send(t, Frame{
		Lane:   LaneDocument,
		Type:   TypeIntent,
		Intent: &Intent{RequestID: 1, Kind: "scene.activate", Payload: payload},
	})

	ack := gm.nextOfType(t, TypeAck)
	var activated SceneActivated
	if err := json.Unmarshal(ack.Ack.Result, &activated); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if activated.SceneID != "scene-2" {
		t.Errorf("activated = %+v", activated)
	}

	event := player.nextOfType(t, TypeEvent)
	if event.Event.Kind != "scene.activate" || event.Event.SceneID != "scene-2" {
		t.Errorf("player event = %+v", event.Event)
	}

	var world *storage.World
	_ = h.store.ReadOnly(ctx, func(q storage.Query) error {
		var readErr error
		world, readErr = q.GetWorld(ctx, testWorld)
		return readErr
	})
	if world.ActiveScene != "scene-2" {
		t.Errorf("stored active scene = %q", world.ActiveScene)
	}
}

func TestOnlyStaffCanActivateAScene(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	err := h.store.Tx(ctx, func(tx storage.Tx) error {
		return tx.PutDocument(ctx, &storage.Document{
			WorldID: testWorld, ID: "scene-1", Kind: "scene", Name: "Chantry",
			Data:      json.RawMessage(`{"width":1000,"height":800,"gridSize":100}`),
			Ownership: json.RawMessage(`{"default":"observer"}`),
		})
	})
	if err != nil {
		t.Fatalf("seed scene: %v", err)
	}

	player, _ := h.connect(t, "user-2", 0)

	payload, _ := json.Marshal(SceneActivatePayload{SceneID: "scene-1"})
	player.send(t, Frame{
		Lane:   LaneDocument,
		Type:   TypeIntent,
		Intent: &Intent{RequestID: 4, Kind: "scene.activate", Payload: payload},
	})

	frame := player.nextOfType(t, TypeError)
	if frame.Error.Code != CodeForbidden {
		t.Errorf("error code = %q, want %q", frame.Error.Code, CodeForbidden)
	}
}
