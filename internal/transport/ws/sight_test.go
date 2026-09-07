package ws

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tavora-vtt/tavora-server/internal/storage"
)

func seedWalledScene(t *testing.T, h *harness) {
	t.Helper()
	ctx := context.Background()

	err := h.store.Tx(ctx, func(tx storage.Tx) error {
		if err := tx.PutDocument(ctx, &storage.Document{
			WorldID: testWorld, ID: "scene-1", Kind: "scene", Name: "Chantry",
			Data:      json.RawMessage(`{"width":1000,"height":800,"gridSize":100}`),
			Ownership: json.RawMessage(`{"default":"observer"}`),
		}); err != nil {
			return err
		}

		if err := tx.PutDocument(ctx, &storage.Document{
			WorldID: testWorld, ID: "wall-1", Kind: "wall", ParentID: "scene-1", Name: "Wall",
			Data:      json.RawMessage(`{"x1":5,"y1":0,"x2":5,"y2":20,"blocksSight":true}`),
			Ownership: json.RawMessage(`{"default":"observer"}`),
		}); err != nil {
			return err
		}

		if err := tx.PutDocument(ctx, &storage.Document{
			WorldID: testWorld, ID: "own-token", Kind: "token", ParentID: "scene-1", Name: "Nadia",
			Data:      json.RawMessage(`{"x":1,"y":5,"disposition":"friendly"}`),
			Ownership: json.RawMessage(`{"user-2":"owner"}`),
		}); err != nil {
			return err
		}

		return tx.PutDocument(ctx, &storage.Document{
			WorldID: testWorld, ID: "far-token", Kind: "token", ParentID: "scene-1", Name: "Sheriff",
			Data:      json.RawMessage(`{"x":9,"y":5,"disposition":"hostile"}`),
			Ownership: json.RawMessage(`{"default":"observer"}`),
		})
	})
	if err != nil {
		t.Fatalf("seed walled scene: %v", err)
	}
}

func moveToken(t *testing.T, conn *fakeConn, requestID uint32, id string, x, y float64) {
	t.Helper()

	payload, _ := json.Marshal(TokenMovePayload{TokenID: id, X: x, Y: y})
	conn.send(t, Frame{
		Lane:   LaneDocument,
		Type:   TypeIntent,
		Intent: &Intent{RequestID: requestID, Kind: "scene.token.move", Payload: payload},
	})
}

func TestATokenBehindAWallIsNotSentToThePlayer(t *testing.T) {
	h := newHarness(t)
	seedWalledScene(t, h)

	gm, _ := h.connect(t, "user-1", 0)
	player, _ := h.connect(t, "user-2", 0)

	moveToken(t, gm, 1, "far-token", 9, 6)
	gm.nextOfType(t, TypeAck)

	raw := string(drainRaw(t, player, 400*time.Millisecond))
	if strings.Contains(raw, "far-token") || strings.Contains(raw, "Sheriff") {
		t.Errorf("a token behind a wall reached the player socket:\n%s", raw)
	}
}

func TestTheSameTokenArrivesOnceItStepsIntoView(t *testing.T) {
	h := newHarness(t)
	seedWalledScene(t, h)

	gm, _ := h.connect(t, "user-1", 0)
	player, _ := h.connect(t, "user-2", 0)

	moveToken(t, gm, 1, "far-token", 2, 5)
	gm.nextOfType(t, TypeAck)

	raw := drainUntil(t, player, "far-token", waitFor)
	if !strings.Contains(raw, "Sheriff") {
		t.Errorf("the token stepped onto the player's side but did not arrive:\n%s", raw)
	}
}

func TestTheGameMasterAlwaysSeesTheMove(t *testing.T) {
	h := newHarness(t)
	seedWalledScene(t, h)

	gm, _ := h.connect(t, "user-1", 0)
	watcher, _ := h.connect(t, "user-3", 0)

	moveToken(t, gm, 1, "far-token", 9, 7)

	raw := drainUntil(t, gm, "far-token", waitFor)
	if !strings.Contains(raw, "Sheriff") {
		t.Errorf("the game master did not receive their own move:\n%s", raw)
	}

	_ = watcher
}

func TestAViewerWithoutATokenIsNotBlinded(t *testing.T) {
	h := newHarness(t)
	seedWalledScene(t, h)

	gm, _ := h.connect(t, "user-1", 0)
	watcher, _ := h.connect(t, "user-3", 0)

	moveToken(t, gm, 1, "far-token", 9, 7)
	gm.nextOfType(t, TypeAck)

	raw := drainUntil(t, watcher, "far-token", waitFor)
	if !strings.Contains(raw, "Sheriff") {
		t.Errorf("a viewer with no token on the scene was blinded:\n%s", raw)
	}
}

func TestAnOpenDoorLetsSightThrough(t *testing.T) {
	h := newHarness(t)
	seedWalledScene(t, h)
	ctx := context.Background()

	err := h.store.Tx(ctx, func(tx storage.Tx) error {
		return tx.PutDocument(ctx, &storage.Document{
			WorldID: testWorld, ID: "wall-1", Kind: "wall", ParentID: "scene-1", Name: "Door",
			Data:      json.RawMessage(`{"x1":5,"y1":0,"x2":5,"y2":20,"blocksSight":true,"door":true,"doorOpen":true}`),
			Ownership: json.RawMessage(`{"default":"observer"}`),
		})
	})
	if err != nil {
		t.Fatalf("open the door: %v", err)
	}

	gm, _ := h.connect(t, "user-1", 0)
	player, _ := h.connect(t, "user-2", 0)

	moveToken(t, gm, 1, "far-token", 9, 6)
	gm.nextOfType(t, TypeAck)

	raw := drainUntil(t, player, "far-token", waitFor)
	if !strings.Contains(raw, "Sheriff") {
		t.Errorf("an open door still blocked sight:\n%s", raw)
	}
}

func seedDoorScene(t *testing.T, h *harness) {
	t.Helper()
	ctx := context.Background()

	err := h.store.Tx(ctx, func(tx storage.Tx) error {
		if err := tx.PutDocument(ctx, &storage.Document{
			WorldID: testWorld, ID: "scene-1", Kind: "scene", Name: "Chantry",
			Data:      json.RawMessage(`{"width":1000,"height":800,"gridSize":100}`),
			Ownership: json.RawMessage(`{"default":"observer"}`),
		}); err != nil {
			return err
		}
		if err := tx.PutDocument(ctx, &storage.Document{
			WorldID: testWorld, ID: "door-1", Kind: "wall", ParentID: "scene-1", Name: "Door",
			Data:      json.RawMessage(`{"x1":5,"y1":0,"x2":5,"y2":20,"blocksSight":true,"door":true,"doorOpen":false}`),
			Ownership: json.RawMessage(`{"default":"observer"}`),
		}); err != nil {
			return err
		}
		if err := tx.PutDocument(ctx, &storage.Document{
			WorldID: testWorld, ID: "own-token", Kind: "token", ParentID: "scene-1", Name: "Nadia",
			Data:      json.RawMessage(`{"x":1,"y":5,"disposition":"friendly"}`),
			Ownership: json.RawMessage(`{"user-2":"owner"}`),
		}); err != nil {
			return err
		}
		return tx.PutDocument(ctx, &storage.Document{
			WorldID: testWorld, ID: "far-token", Kind: "token", ParentID: "scene-1", Name: "Sheriff",
			Data:      json.RawMessage(`{"x":9,"y":5,"disposition":"hostile"}`),
			Ownership: json.RawMessage(`{"default":"observer"}`),
		})
	})
	if err != nil {
		t.Fatalf("seed door scene: %v", err)
	}
}

func TestOpeningADoorRevealsWhatWasHidden(t *testing.T) {
	h := newHarness(t)
	seedDoorScene(t, h)

	gm, _ := h.connect(t, "user-1", 0)
	player, _ := h.connect(t, "user-2", 0)

	payload, _ := json.Marshal(DoorTogglePayload{WallID: "door-1"})
	gm.send(t, Frame{
		Lane:   LaneDocument,
		Type:   TypeIntent,
		Intent: &Intent{RequestID: 1, Kind: "scene.door.toggle", Payload: payload},
	})

	ack := gm.nextOfType(t, TypeAck)
	var state DoorState
	if err := json.Unmarshal(ack.Ack.Result, &state); err != nil {
		t.Fatalf("decode door: %v", err)
	}
	if !state.DoorOpen {
		t.Errorf("the door did not open: %+v", state)
	}

	raw := drainUntil(t, player, "scene.visibility", waitFor)
	if !strings.Contains(raw, "Sheriff") {
		t.Errorf("opening the door did not reveal the token behind it:\n%s", raw)
	}
}

func TestClosingADoorHidesAgain(t *testing.T) {
	h := newHarness(t)
	seedDoorScene(t, h)

	gm, _ := h.connect(t, "user-1", 0)
	player, _ := h.connect(t, "user-2", 0)

	toggle := func(requestID uint32) {
		t.Helper()
		payload, _ := json.Marshal(DoorTogglePayload{WallID: "door-1"})
		gm.send(t, Frame{
			Lane:   LaneDocument,
			Type:   TypeIntent,
			Intent: &Intent{RequestID: requestID, Kind: "scene.door.toggle", Payload: payload},
		})
		gm.nextOfType(t, TypeAck)
	}

	toggle(1)
	drainUntil(t, player, "scene.visibility", waitFor)

	toggle(2)
	raw := drainUntil(t, player, "scene.visibility", waitFor)

	if strings.Contains(raw, "Sheriff") {
		t.Errorf("closing the door left the hidden token in the player's view:\n%s", raw)
	}
	if !strings.Contains(raw, "Nadia") {
		t.Errorf("the player lost sight of their own token:\n%s", raw)
	}
}

func TestAPlayerCannotOpenADoor(t *testing.T) {
	h := newHarness(t)
	seedDoorScene(t, h)

	player, _ := h.connect(t, "user-2", 0)

	payload, _ := json.Marshal(DoorTogglePayload{WallID: "door-1"})
	player.send(t, Frame{
		Lane:   LaneDocument,
		Type:   TypeIntent,
		Intent: &Intent{RequestID: 1, Kind: "scene.door.toggle", Payload: payload},
	})

	frame := player.nextOfType(t, TypeError)
	if frame.Error.Code != CodeForbidden {
		t.Errorf("error code = %q", frame.Error.Code)
	}
}

func TestTogglingAPlainWallIsRefused(t *testing.T) {
	h := newHarness(t)
	seedWalledScene(t, h)

	gm, _ := h.connect(t, "user-1", 0)

	payload, _ := json.Marshal(DoorTogglePayload{WallID: "wall-1"})
	gm.send(t, Frame{
		Lane:   LaneDocument,
		Type:   TypeIntent,
		Intent: &Intent{RequestID: 1, Kind: "scene.door.toggle", Payload: payload},
	})

	frame := gm.nextOfType(t, TypeError)
	if frame.Error.MessageKey != "core.scene.notADoor" {
		t.Errorf("message key = %q", frame.Error.MessageKey)
	}
}

func TestTokenArtReachesEveryoneWhoCanSeeTheToken(t *testing.T) {
	h := newHarness(t)
	seedWalledScene(t, h)

	gm, _ := h.connect(t, "user-1", 0)
	player, _ := h.connect(t, "user-2", 0)

	art := "/assets/world-1/asset-portrait"
	payload, err := json.Marshal(DocumentPatchPayload{ID: "own-token", Img: &art})
	if err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	gm.send(t, Frame{
		Lane:   LaneDocument,
		Type:   TypeIntent,
		Intent: &Intent{RequestID: 1, Kind: "document.patch", Payload: payload},
	})

	if received := drainUntil(t, player, art, waitFor); !strings.Contains(received, "own-token") {
		t.Errorf("the art arrived without its token:\n%s", received)
	}

	var stored *storage.Document
	err = h.store.ReadOnly(context.Background(), func(q storage.Query) error {
		var readErr error
		stored, readErr = q.GetDocument(context.Background(), testWorld, "own-token")
		return readErr
	})
	if err != nil {
		t.Fatalf("read token: %v", err)
	}
	if stored.Img != art {
		t.Errorf("the token kept %q as its art, want %q", stored.Img, art)
	}
}
