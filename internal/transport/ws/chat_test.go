package ws

import (
	"encoding/json"
	"testing"
	"time"
)

func decodeMessage(t *testing.T, raw json.RawMessage) ChatMessage {
	t.Helper()
	var message ChatMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	return message
}

func TestChatPostReachesEveryone(t *testing.T) {
	h := newHarness(t)

	author, _ := h.connect(t, "user-1", 0)
	listener, _ := h.connect(t, "user-2", 0)

	payload, _ := json.Marshal(ChatPostPayload{Text: "  the sheriff is watching  "})
	author.send(t, Frame{
		Lane:   LaneDocument,
		Type:   TypeIntent,
		Intent: &Intent{RequestID: 1, Kind: "chat.post", Payload: payload},
	})

	ack := author.nextOfType(t, TypeAck)
	posted := decodeMessage(t, ack.Ack.Result)
	if posted.Text != "the sheriff is watching" {
		t.Errorf("text = %q, whitespace should be trimmed", posted.Text)
	}
	if posted.Author != "user-1" && posted.Author == "" {
		t.Error("author name missing")
	}

	event := listener.nextOfType(t, TypeEvent)
	if event.Event.Kind != "chat.message" {
		t.Fatalf("event kind = %q", event.Event.Kind)
	}
	received := decodeMessage(t, event.Event.Payload)
	if received.Text != posted.Text || received.Seq != posted.Seq {
		t.Errorf("received = %+v, posted = %+v", received, posted)
	}
}

func TestEmptyChatMessageIsRejected(t *testing.T) {
	h := newHarness(t)
	conn, _ := h.connect(t, "user-1", 0)

	payload, _ := json.Marshal(ChatPostPayload{Text: "   "})
	conn.send(t, Frame{
		Lane:   LaneDocument,
		Type:   TypeIntent,
		Intent: &Intent{RequestID: 2, Kind: "chat.post", Payload: payload},
	})

	frame := conn.nextOfType(t, TypeError)
	if frame.Error.MessageKey != "core.chat.emptyMessage" {
		t.Errorf("message key = %q", frame.Error.MessageKey)
	}
}

func TestRollIsResolvedByTheServer(t *testing.T) {
	h := newHarness(t)
	conn, _ := h.connect(t, "user-1", 0)

	payload, _ := json.Marshal(ChatRollPayload{Expression: "4d6kh3+2", Reason: "strength"})
	conn.send(t, Frame{
		Lane:   LaneDocument,
		Type:   TypeIntent,
		Intent: &Intent{RequestID: 3, Kind: "chat.roll", Payload: payload},
	})

	ack := conn.nextOfType(t, TypeAck)
	message := decodeMessage(t, ack.Ack.Result)

	if message.Roll == nil {
		t.Fatal("no roll attached")
	}
	if message.Key != "core.chat.rolled" {
		t.Errorf("key = %q, generated text must be a descriptor not a sentence", message.Key)
	}
	if message.Text != "" {
		t.Errorf("a generated message must not carry pre-rendered text: %q", message.Text)
	}
	if message.Reason != "strength" {
		t.Errorf("reason = %q", message.Reason)
	}

	if len(message.Roll.Terms) != 2 {
		t.Fatalf("terms = %d, want a dice term and a constant", len(message.Roll.Terms))
	}
	if len(message.Roll.Terms[0].Dice) != 4 {
		t.Errorf("reported %d dice, the dropped one must still be visible",
			len(message.Roll.Terms[0].Dice))
	}

	kept := 0
	for _, die := range message.Roll.Terms[0].Dice {
		if die.Kept {
			kept++
		}
		if die.Value < 1 || die.Value > 6 {
			t.Errorf("d6 produced %d", die.Value)
		}
	}
	if kept != 3 {
		t.Errorf("kept %d dice, want 3", kept)
	}
	if message.Roll.Total < 5 || message.Roll.Total > 20 {
		t.Errorf("total %d is outside the possible range for 4d6kh3+2", message.Roll.Total)
	}
}

func TestMalformedRollIsRejected(t *testing.T) {
	h := newHarness(t)
	conn, _ := h.connect(t, "user-1", 0)

	payload, _ := json.Marshal(ChatRollPayload{Expression: "drop table documents"})
	conn.send(t, Frame{
		Lane:   LaneDocument,
		Type:   TypeIntent,
		Intent: &Intent{RequestID: 4, Kind: "chat.roll", Payload: payload},
	})

	frame := conn.nextOfType(t, TypeError)
	if frame.Error.MessageKey != "core.dice.malformedExpression" {
		t.Errorf("message key = %q", frame.Error.MessageKey)
	}
}

func TestGMWhisperDoesNotReachPlayers(t *testing.T) {
	h := newHarness(t)

	gm, _ := h.connect(t, "user-1", 0)
	player, _ := h.connect(t, "user-2", 0)

	payload, _ := json.Marshal(ChatRollPayload{Expression: "1d20", Audience: "gm"})
	gm.send(t, Frame{
		Lane:   LaneDocument,
		Type:   TypeIntent,
		Intent: &Intent{RequestID: 5, Kind: "chat.roll", Payload: payload},
	})
	gm.nextOfType(t, TypeAck)

	raw := string(drainRaw(t, player, 300*time.Millisecond))
	if raw != "" {
		t.Errorf("a gm-only roll reached a player socket:\n%s", raw)
	}
}

func TestPlayerCannotForgeAGMAudience(t *testing.T) {
	h := newHarness(t)

	player, _ := h.connect(t, "user-2", 0)
	listener, _ := h.connect(t, "user-3", 0)

	payload, _ := json.Marshal(ChatPostPayload{Text: "secret", Audience: "gm"})
	player.send(t, Frame{
		Lane:   LaneDocument,
		Type:   TypeIntent,
		Intent: &Intent{RequestID: 6, Kind: "chat.post", Payload: payload},
	})

	ack := player.nextOfType(t, TypeAck)
	message := decodeMessage(t, ack.Ack.Result)
	if message.Audience != AudiencePublic {
		t.Errorf("audience = %q, a player must not be able to claim a gm audience", message.Audience)
	}

	event := listener.nextOfType(t, TypeEvent)
	if event.Event.Kind != "chat.message" {
		t.Errorf("the downgraded message did not reach the other player")
	}
}
