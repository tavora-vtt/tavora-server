package ws

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tavora-vtt/tavora-server/internal/storage"
)

func seedScene(t *testing.T, h *harness, names ...string) storage.ID {
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
		for index, name := range names {
			if err := tx.PutDocument(ctx, &storage.Document{
				WorldID: testWorld, ID: storage.ID(name), Kind: "token", ParentID: "scene-1",
				Name: name, Sort: index,
				Data:      json.RawMessage(`{"x":1,"y":1,"disposition":"hostile"}`),
				Ownership: json.RawMessage(`{"default":"observer"}`),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed scene: %v", err)
	}
	return "scene-1"
}

func startCombat(t *testing.T, conn *fakeConn, sceneID storage.ID, requestID uint32) Combat {
	t.Helper()

	payload, _ := json.Marshal(CombatStartPayload{SceneID: string(sceneID)})
	conn.send(t, Frame{
		Lane:   LaneDocument,
		Type:   TypeIntent,
		Intent: &Intent{RequestID: requestID, Kind: "combat.start", Payload: payload},
	})

	ack := conn.nextOfType(t, TypeAck)
	var combat Combat
	if err := json.Unmarshal(ack.Ack.Result, &combat); err != nil {
		t.Fatalf("decode combat: %v", err)
	}
	return combat
}

func TestCombatStartsSortedByInitiative(t *testing.T) {
	h := newHarness(t)
	scene := seedScene(t, h, "Nadia", "Ghoul", "Sheriff")

	gm, _ := h.connect(t, "user-1", 0)
	combat := startCombat(t, gm, scene, 1)

	if !combat.Active || combat.Round != 1 || combat.Turn != 0 {
		t.Errorf("combat = %+v", combat)
	}
	if len(combat.Combatants) != 3 {
		t.Fatalf("combatants = %d, want 3", len(combat.Combatants))
	}
	for index := 1; index < len(combat.Combatants); index++ {
		if combat.Combatants[index-1].Initiative < combat.Combatants[index].Initiative {
			t.Errorf("order is not descending: %+v", combat.Combatants)
		}
	}
	for _, combatant := range combat.Combatants {
		if combatant.Initiative < 1 || combatant.Initiative > 20 {
			t.Errorf("%s rolled %d, outside 1d20", combatant.Name, combatant.Initiative)
		}
	}
}

func TestTurnsWrapIntoTheNextRound(t *testing.T) {
	h := newHarness(t)
	scene := seedScene(t, h, "Nadia", "Ghoul")

	gm, _ := h.connect(t, "user-1", 0)
	startCombat(t, gm, scene, 1)

	advance := func(requestID uint32) Combat {
		t.Helper()
		gm.send(t, Frame{
			Lane:   LaneDocument,
			Type:   TypeIntent,
			Intent: &Intent{RequestID: requestID, Kind: "combat.next", Payload: json.RawMessage(`{}`)},
		})
		ack := gm.nextOfType(t, TypeAck)
		var combat Combat
		if err := json.Unmarshal(ack.Ack.Result, &combat); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return combat
	}

	second := advance(2)
	if second.Round != 1 || second.Turn != 1 {
		t.Errorf("after one advance: round %d turn %d", second.Round, second.Turn)
	}

	wrapped := advance(3)
	if wrapped.Round != 2 || wrapped.Turn != 0 {
		t.Errorf("after wrapping: round %d turn %d", wrapped.Round, wrapped.Turn)
	}
}

func TestCombatReachesPlayers(t *testing.T) {
	h := newHarness(t)
	scene := seedScene(t, h, "Nadia", "Ghoul")

	gm, _ := h.connect(t, "user-1", 0)
	player, _ := h.connect(t, "user-2", 0)

	startCombat(t, gm, scene, 1)

	event := player.nextOfType(t, TypeEvent)
	if event.Event.Kind != "combat.update" {
		t.Fatalf("event kind = %q", event.Event.Kind)
	}

	var combat Combat
	if err := json.Unmarshal(event.Event.Payload, &combat); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(combat.Combatants) != 2 {
		t.Errorf("the player received %d combatants", len(combat.Combatants))
	}
}

func TestPlayersCannotDriveCombat(t *testing.T) {
	h := newHarness(t)
	scene := seedScene(t, h, "Nadia")

	player, _ := h.connect(t, "user-2", 0)

	payload, _ := json.Marshal(CombatStartPayload{SceneID: string(scene)})
	player.send(t, Frame{
		Lane:   LaneDocument,
		Type:   TypeIntent,
		Intent: &Intent{RequestID: 1, Kind: "combat.start", Payload: payload},
	})

	frame := player.nextOfType(t, TypeError)
	if frame.Error.Code != CodeForbidden {
		t.Errorf("error code = %q", frame.Error.Code)
	}
}

func TestAdvancingWithoutCombatIsRefused(t *testing.T) {
	h := newHarness(t)
	gm, _ := h.connect(t, "user-1", 0)

	gm.send(t, Frame{
		Lane:   LaneDocument,
		Type:   TypeIntent,
		Intent: &Intent{RequestID: 1, Kind: "combat.next", Payload: json.RawMessage(`{}`)},
	})

	frame := gm.nextOfType(t, TypeError)
	if frame.Error.MessageKey != "core.combat.notRunning" {
		t.Errorf("message key = %q", frame.Error.MessageKey)
	}
}

func TestEndingCombatClearsTheOrder(t *testing.T) {
	h := newHarness(t)
	scene := seedScene(t, h, "Nadia", "Ghoul")

	gm, _ := h.connect(t, "user-1", 0)
	startCombat(t, gm, scene, 1)

	gm.send(t, Frame{
		Lane:   LaneDocument,
		Type:   TypeIntent,
		Intent: &Intent{RequestID: 2, Kind: "combat.end", Payload: json.RawMessage(`{}`)},
	})

	ack := gm.nextOfType(t, TypeAck)
	var combat Combat
	if err := json.Unmarshal(ack.Ack.Result, &combat); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if combat.Active || len(combat.Combatants) != 0 {
		t.Errorf("combat after end = %+v", combat)
	}
}
