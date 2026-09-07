package ws

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tavora-vtt/tavora-server/internal/storage"
)

func drainRaw(t *testing.T, conn *fakeConn, within time.Duration) []byte {
	t.Helper()

	var collected bytes.Buffer
	deadline := time.After(within)

	for {
		select {
		case data := <-conn.outgoing:
			collected.Write(data)
			collected.WriteByte('\n')
		case <-deadline:
			return collected.Bytes()
		}
	}
}

func patchAs(t *testing.T, conn *fakeConn, requestID uint32, set map[string]any) {
	t.Helper()

	payload, err := json.Marshal(DocumentPatchPayload{ID: string(testDoc), Set: set})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	conn.send(t, Frame{
		Lane:   LaneDocument,
		Type:   TypeIntent,
		Intent: &Intent{RequestID: requestID, Kind: "document.patch", Payload: payload},
	})
}

func TestGMOnlyFieldNeverReachesAPlayerSocket(t *testing.T) {
	h := newHarness(t)

	gm, _ := h.connect(t, "user-1", 0)
	player, _ := h.connect(t, "user-2", 0)

	patchAs(t, gm, 1, map[string]any{"hunger": 4})
	gm.nextOfType(t, TypeAck)

	gmBytes := string(drainRaw(t, gm, 300*time.Millisecond))
	playerBytes := string(drainRaw(t, player, 300*time.Millisecond))

	if !strings.Contains(gmBytes, "secrets") {
		t.Error("the gm should see the secret field")
	}
	if !strings.Contains(gmBytes, "unknown") {
		t.Error("the gm should see the secret value")
	}

	for _, forbidden := range []string{"secrets", "unknown", "sire"} {
		if strings.Contains(playerBytes, forbidden) {
			t.Errorf("%q left the server on a player socket:\n%s", forbidden, playerBytes)
		}
	}

	if !strings.Contains(playerBytes, "hunger") {
		t.Error("the player should still see the observer-visible field")
	}
}

func TestPlayerWithoutAccessReceivesNothing(t *testing.T) {
	h := newHarness(t)

	gm, _ := h.connect(t, "user-1", 0)
	stranger, _ := h.connect(t, "user-3", 0)

	patchAs(t, gm, 1, map[string]any{"hunger": 4})
	gm.nextOfType(t, TypeAck)

	raw := string(drainRaw(t, stranger, 300*time.Millisecond))

	for _, forbidden := range []string{"hunger", "Ventrue", "actor-1", "secrets"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("%q reached a user with no access to the document:\n%s", forbidden, raw)
		}
	}
}

func TestCatchUpRedactsJustLikeLiveFanOut(t *testing.T) {
	h := newHarness(t)

	gm, _ := h.connect(t, "user-1", 0)
	patchAs(t, gm, 1, map[string]any{"hunger": 4})
	gm.nextOfType(t, TypeAck)

	returning, _ := h.connect(t, "user-2", 0)
	raw := string(drainRaw(t, returning, 300*time.Millisecond))

	if !strings.Contains(raw, "hunger") {
		t.Fatalf("the replayed event carried no document state:\n%s", raw)
	}
	for _, forbidden := range []string{"secrets", "unknown", "sire"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("catch-up leaked %q, replay must be redacted like live fan-out:\n%s", forbidden, raw)
		}
	}
}

func TestObserverCannotPatch(t *testing.T) {
	h := newHarness(t)

	player, _ := h.connect(t, "user-2", 0)
	patchAs(t, player, 7, map[string]any{"hunger": 0})

	frame := player.nextOfType(t, TypeError)
	if frame.Error.Code != CodeForbidden {
		t.Errorf("error code = %q, want %q", frame.Error.Code, CodeForbidden)
	}

	stored := loadDocument(t, h.store)
	if stored.UpdatedSeq != 0 {
		t.Errorf("a rejected patch advanced the document to sequence %d", stored.UpdatedSeq)
	}
}

func TestNonMemberIsRejectedAtHandshake(t *testing.T) {
	h := newHarness(t)

	token, _, err := h.tickets.Issue("outsider", testWorld, "gm")
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
		},
	})

	frame := conn.nextOfType(t, TypeError)
	if frame.Error.Code != CodeForbidden {
		t.Errorf("error code = %q, want %q: a ticket must not grant a role the world does not record",
			frame.Error.Code, CodeForbidden)
	}
}

func TestRoleComesFromTheWorldNotTheTicket(t *testing.T) {
	h := newHarness(t)

	token, _, err := h.tickets.Issue("user-2", testWorld, "gm")
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
		},
	})

	welcome := conn.nextOfType(t, TypeWelcome)
	if welcome.Welcome.Role != "player" {
		t.Errorf("role = %q, want player: the ticket claimed gm but the world records player",
			welcome.Welcome.Role)
	}
}

func TestOwnershipInheritanceFromFolder(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	err := h.store.Tx(ctx, func(tx storage.Tx) error {
		if err := tx.PutDocument(ctx, &storage.Document{
			WorldID: testWorld, ID: "folder-1", Kind: "folder", Name: "Coterie",
			Ownership: json.RawMessage(`{"user-3":"observer"}`),
		}); err != nil {
			return err
		}
		return tx.PutDocument(ctx, &storage.Document{
			WorldID: testWorld, ID: "actor-2", Kind: "actor", Subtype: "vampire",
			FolderID: "folder-1", Name: "Tomas",
			Data: json.RawMessage(`{"hunger":1,"secrets":{"haven":"docks"}}`),
		})
	})
	if err != nil {
		t.Fatalf("seed folder: %v", err)
	}

	gm, _ := h.connect(t, "user-1", 0)
	inherited, _ := h.connect(t, "user-3", 0)

	payload, _ := json.Marshal(DocumentPatchPayload{ID: "actor-2", Set: map[string]any{"hunger": 3}})
	gm.send(t, Frame{
		Lane:   LaneDocument,
		Type:   TypeIntent,
		Intent: &Intent{RequestID: 1, Kind: "document.patch", Payload: payload},
	})
	gm.nextOfType(t, TypeAck)

	raw := string(drainRaw(t, inherited, 300*time.Millisecond))

	if !strings.Contains(raw, "hunger") {
		t.Errorf("the folder grant did not reach the document:\n%s", raw)
	}
	if strings.Contains(raw, "haven") {
		t.Errorf("an inherited observer grant leaked a gm field:\n%s", raw)
	}
}
