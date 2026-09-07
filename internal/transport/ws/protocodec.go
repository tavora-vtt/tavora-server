package ws

import (
	"fmt"

	"google.golang.org/protobuf/proto"

	tavorav1 "github.com/tavora-vtt/tavora-protocol/gen/go/tavora/v1"
)

type ProtoCodec struct{}

func (ProtoCodec) Name() string { return "protobuf" }

func (ProtoCodec) Binary() bool { return true }

func (ProtoCodec) Encode(frame Frame) ([]byte, error) {
	message, err := toProto(frame)
	if err != nil {
		return nil, err
	}
	return proto.Marshal(message)
}

func (ProtoCodec) Decode(data []byte) (Frame, error) {
	var message tavorav1.Frame
	if err := proto.Unmarshal(data, &message); err != nil {
		return Frame{}, fmt.Errorf("decode frame: %w", err)
	}
	return fromProto(&message)
}

func toProto(frame Frame) (*tavorav1.Frame, error) {
	message := &tavorav1.Frame{Lane: uint32(frame.Lane)}

	switch frame.Type {
	case TypeHello:
		if frame.Hello == nil {
			return nil, missingBody(frame.Type)
		}
		message.Body = &tavorav1.Frame_Hello{Hello: &tavorav1.Hello{
			Ticket:          frame.Hello.Ticket,
			WorldId:         frame.Hello.WorldID,
			ProtocolVersion: frame.Hello.ProtocolVersion,
			LastSeq:         frame.Hello.LastSeq,
			Locale:          frame.Hello.Locale,
			Capabilities:    frame.Hello.Capabilities,
		}}

	case TypeWelcome:
		if frame.Welcome == nil {
			return nil, missingBody(frame.Type)
		}
		message.Body = &tavorav1.Frame_Welcome{Welcome: &tavorav1.Welcome{
			SessionId:   frame.Welcome.SessionID,
			UserId:      frame.Welcome.UserID,
			Role:        frame.Welcome.Role,
			Seq:         frame.Welcome.Seq,
			SnapshotRef: frame.Welcome.SnapshotRef,
			DeltaFrom:   frame.Welcome.DeltaFrom,
		}}

	case TypeIntent:
		if frame.Intent == nil {
			return nil, missingBody(frame.Type)
		}
		message.Body = &tavorav1.Frame_Intent{Intent: &tavorav1.Intent{
			RequestId: frame.Intent.RequestID,
			Kind:      frame.Intent.Kind,
			WorldId:   frame.Intent.WorldID,
			Payload:   frame.Intent.Payload,
		}}

	case TypeAck:
		if frame.Ack == nil {
			return nil, missingBody(frame.Type)
		}
		message.Body = &tavorav1.Frame_Ack{Ack: &tavorav1.Ack{
			RequestId: frame.Ack.RequestID,
			Seq:       frame.Ack.Seq,
			Result:    frame.Ack.Result,
		}}

	case TypeEvent:
		if frame.Event == nil {
			return nil, missingBody(frame.Type)
		}
		message.Body = &tavorav1.Frame_Event{Event: &tavorav1.Event{
			Seq:     frame.Event.Seq,
			Kind:    frame.Event.Kind,
			WorldId: frame.Event.WorldID,
			SceneId: frame.Event.SceneID,
			Payload: frame.Event.Payload,
		}}

	case TypeEphemeral:
		if frame.Ephemeral == nil {
			return nil, missingBody(frame.Type)
		}
		message.Body = &tavorav1.Frame_Ephemeral{Ephemeral: &tavorav1.Ephemeral{
			Kind:    frame.Ephemeral.Kind,
			Key:     frame.Ephemeral.Key,
			WorldId: frame.Ephemeral.WorldID,
			SceneId: frame.Ephemeral.SceneID,
			Payload: frame.Ephemeral.Payload,
		}}

	case TypeError:
		if frame.Error == nil {
			return nil, missingBody(frame.Type)
		}
		message.Body = &tavorav1.Frame_Error{Error: &tavorav1.Error{
			RequestId:  frame.Error.RequestID,
			Code:       frame.Error.Code,
			MessageKey: frame.Error.MessageKey,
			Params:     frame.Error.Params,
		}}

	case TypePing:
		sentAt := int64(0)
		if frame.Ping != nil {
			sentAt = frame.Ping.SentAtUnixMs
		}
		message.Body = &tavorav1.Frame_Ping{Ping: &tavorav1.Ping{SentAtUnixMs: sentAt}}

	case TypePong:
		sentAt := int64(0)
		if frame.Ping != nil {
			sentAt = frame.Ping.SentAtUnixMs
		}
		message.Body = &tavorav1.Frame_Pong{Pong: &tavorav1.Pong{SentAtUnixMs: sentAt}}

	case TypeResync:
		if frame.Resync == nil {
			return nil, missingBody(frame.Type)
		}
		message.Body = &tavorav1.Frame_Resync{Resync: &tavorav1.Resync{
			Reason:  frame.Resync.Reason,
			FromSeq: frame.Resync.FromSeq,
		}}

	default:
		return nil, fmt.Errorf("encode frame: unknown type %q", frame.Type)
	}

	return message, nil
}

func fromProto(message *tavorav1.Frame) (Frame, error) {
	frame := Frame{Lane: Lane(message.GetLane())}

	switch body := message.GetBody().(type) {
	case *tavorav1.Frame_Hello:
		frame.Type = TypeHello
		frame.Hello = &Hello{
			Ticket:          body.Hello.GetTicket(),
			WorldID:         body.Hello.GetWorldId(),
			ProtocolVersion: body.Hello.GetProtocolVersion(),
			LastSeq:         body.Hello.GetLastSeq(),
			Locale:          body.Hello.GetLocale(),
			Capabilities:    body.Hello.GetCapabilities(),
		}

	case *tavorav1.Frame_Welcome:
		frame.Type = TypeWelcome
		frame.Welcome = &Welcome{
			SessionID:   body.Welcome.GetSessionId(),
			UserID:      body.Welcome.GetUserId(),
			Role:        body.Welcome.GetRole(),
			Seq:         body.Welcome.GetSeq(),
			DeltaFrom:   body.Welcome.GetDeltaFrom(),
			SnapshotRef: body.Welcome.GetSnapshotRef(),
		}

	case *tavorav1.Frame_Intent:
		frame.Type = TypeIntent
		frame.Intent = &Intent{
			RequestID: body.Intent.GetRequestId(),
			Kind:      body.Intent.GetKind(),
			WorldID:   body.Intent.GetWorldId(),
			Payload:   body.Intent.GetPayload(),
		}

	case *tavorav1.Frame_Ack:
		frame.Type = TypeAck
		frame.Ack = &Ack{
			RequestID: body.Ack.GetRequestId(),
			Seq:       body.Ack.GetSeq(),
			Result:    body.Ack.GetResult(),
		}

	case *tavorav1.Frame_Event:
		frame.Type = TypeEvent
		frame.Event = &Event{
			Seq:     body.Event.GetSeq(),
			Kind:    body.Event.GetKind(),
			WorldID: body.Event.GetWorldId(),
			SceneID: body.Event.GetSceneId(),
			Payload: body.Event.GetPayload(),
		}

	case *tavorav1.Frame_Ephemeral:
		frame.Type = TypeEphemeral
		frame.Ephemeral = &Ephemeral{
			Kind:    body.Ephemeral.GetKind(),
			Key:     body.Ephemeral.GetKey(),
			WorldID: body.Ephemeral.GetWorldId(),
			SceneID: body.Ephemeral.GetSceneId(),
			Payload: body.Ephemeral.GetPayload(),
		}

	case *tavorav1.Frame_Error:
		frame.Type = TypeError
		frame.Error = &Error{
			RequestID:  body.Error.GetRequestId(),
			Code:       body.Error.GetCode(),
			MessageKey: body.Error.GetMessageKey(),
			Params:     body.Error.GetParams(),
		}

	case *tavorav1.Frame_Ping:
		frame.Type = TypePing
		frame.Ping = &Ping{SentAtUnixMs: body.Ping.GetSentAtUnixMs()}

	case *tavorav1.Frame_Pong:
		frame.Type = TypePong
		frame.Ping = &Ping{SentAtUnixMs: body.Pong.GetSentAtUnixMs()}

	case *tavorav1.Frame_Resync:
		frame.Type = TypeResync
		frame.Resync = &Resync{
			Reason:  body.Resync.GetReason(),
			FromSeq: body.Resync.GetFromSeq(),
		}

	default:
		return Frame{}, fmt.Errorf("decode frame: unknown or missing body")
	}

	return frame, nil
}

func missingBody(frameType FrameType) error {
	return fmt.Errorf("encode frame: %s has no body", frameType)
}
