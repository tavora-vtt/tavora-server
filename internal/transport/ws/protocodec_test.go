package ws

import (
	"encoding/json"
	"reflect"
	"testing"
)

func representativeFrames() []Frame {
	return []Frame{
		{
			Lane: LaneControl,
			Type: TypeHello,
			Hello: &Hello{
				Ticket:          "abc123",
				WorldID:         "world-1",
				ProtocolVersion: ProtocolVersion,
				LastSeq:         42,
				Locale:          "de-AT",
				Capabilities:    []string{"webrtc", "ktx2"},
			},
		},
		{
			Lane: LaneControl,
			Type: TypeWelcome,
			Welcome: &Welcome{
				SessionID: "s-1", UserID: "user-1", Role: "gm",
				Seq: 99, DeltaFrom: 42, SnapshotRef: "snap-7",
			},
		},
		{
			Lane: LaneDocument,
			Type: TypeIntent,
			Intent: &Intent{
				RequestID: 17, Kind: "scene.token.move", WorldID: "world-1",
				Payload: json.RawMessage(`{"tokenId":"t1","x":12,"y":30}`),
			},
		},
		{
			Lane: LaneDocument,
			Type: TypeAck,
			Ack:  &Ack{RequestID: 17, Seq: 100, Result: json.RawMessage(`{"ok":true}`)},
		},
		{
			Lane: LaneDocument,
			Type: TypeEvent,
			Event: &Event{
				Seq: 100, Kind: "scene.token.move", WorldID: "world-1", SceneID: "scene-1",
				Payload: json.RawMessage(`{"x":12,"y":30}`),
			},
		},
		{
			Lane: LaneEphemeral,
			Type: TypeEphemeral,
			Ephemeral: &Ephemeral{
				Kind: "cursor", Key: "cursor:user-1", WorldID: "world-1", SceneID: "scene-1",
				Payload: json.RawMessage(`{"x":1,"y":2}`),
			},
		},
		{
			Lane: LaneControl,
			Type: TypeError,
			Error: &Error{
				RequestID: 17, Code: CodeForbidden, MessageKey: "core.ws.forbidden",
				Params: map[string]string{"document": "actor-1"},
			},
		},
		{Lane: LaneControl, Type: TypePing, Ping: &Ping{SentAtUnixMs: 1730000000000}},
		{Lane: LaneControl, Type: TypePong, Ping: &Ping{SentAtUnixMs: 1730000000000}},
		{
			Lane:   LaneControl,
			Type:   TypeResync,
			Resync: &Resync{Reason: "slow consumer", FromSeq: 88},
		},
	}
}

func TestCodecsRoundTripEveryFrameType(t *testing.T) {
	for _, codec := range []Codec{ProtoCodec{}, JSONCodec{}} {
		t.Run(codec.Name(), func(t *testing.T) {
			for _, original := range representativeFrames() {
				encoded, err := codec.Encode(original)
				if err != nil {
					t.Fatalf("%s encode %s: %v", codec.Name(), original.Type, err)
				}

				decoded, err := codec.Decode(encoded)
				if err != nil {
					t.Fatalf("%s decode %s: %v", codec.Name(), original.Type, err)
				}

				if !reflect.DeepEqual(original, decoded) {
					t.Errorf("%s round trip changed %s:\n  before %+v\n  after  %+v",
						codec.Name(), original.Type, original, decoded)
				}
			}
		})
	}
}

func TestCodecsAgreeOnEveryFrameType(t *testing.T) {
	proto := ProtoCodec{}
	text := JSONCodec{}

	for _, original := range representativeFrames() {
		viaProto, err := proto.Encode(original)
		if err != nil {
			t.Fatalf("protobuf encode %s: %v", original.Type, err)
		}
		viaJSON, err := text.Encode(original)
		if err != nil {
			t.Fatalf("json encode %s: %v", original.Type, err)
		}

		fromProtoFrame, err := proto.Decode(viaProto)
		if err != nil {
			t.Fatalf("protobuf decode %s: %v", original.Type, err)
		}
		fromJSONFrame, err := text.Decode(viaJSON)
		if err != nil {
			t.Fatalf("json decode %s: %v", original.Type, err)
		}

		if !reflect.DeepEqual(fromProtoFrame, fromJSONFrame) {
			t.Errorf("the two codecs disagree about %s:\n  protobuf %+v\n  json     %+v",
				original.Type, fromProtoFrame, fromJSONFrame)
		}
	}
}

func TestProtobufIsSmallerThanJSON(t *testing.T) {
	proto := ProtoCodec{}
	text := JSONCodec{}

	frame := Frame{
		Lane: LaneEphemeral,
		Type: TypeEphemeral,
		Ephemeral: &Ephemeral{
			Kind: "cursor", Key: "cursor:user-1", WorldID: "world-1", SceneID: "scene-1",
			Payload: json.RawMessage(`{"x":12,"y":30}`),
		},
	}

	binary, err := proto.Encode(frame)
	if err != nil {
		t.Fatalf("protobuf encode: %v", err)
	}
	readable, err := text.Encode(frame)
	if err != nil {
		t.Fatalf("json encode: %v", err)
	}

	if len(binary) >= len(readable) {
		t.Errorf("protobuf %d bytes, json %d bytes: the wire format should be the smaller one",
			len(binary), len(readable))
	}
	t.Logf("cursor frame: protobuf %d bytes, json %d bytes", len(binary), len(readable))
}

func TestEncodingRejectsFrameWithoutBody(t *testing.T) {
	if _, err := (ProtoCodec{}).Encode(Frame{Lane: LaneDocument, Type: TypeIntent}); err == nil {
		t.Error("encoding an intent frame without an intent should fail")
	}
	if _, err := (ProtoCodec{}).Encode(Frame{Lane: LaneDocument, Type: "nonsense"}); err == nil {
		t.Error("encoding an unknown frame type should fail")
	}
}
