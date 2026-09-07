package ws

import (
	"encoding/json"
	"fmt"
)

type Lane uint8

const (
	LaneControl Lane = iota
	LaneDocument
	LaneEphemeral
)

func (l Lane) String() string {
	switch l {
	case LaneControl:
		return "control"
	case LaneDocument:
		return "document"
	case LaneEphemeral:
		return "ephemeral"
	default:
		return fmt.Sprintf("lane(%d)", uint8(l))
	}
}

type FrameType string

const (
	TypeHello     FrameType = "hello"
	TypeWelcome   FrameType = "welcome"
	TypeIntent    FrameType = "intent"
	TypeAck       FrameType = "ack"
	TypeEvent     FrameType = "event"
	TypeEphemeral FrameType = "ephemeral"
	TypeError     FrameType = "error"
	TypePing      FrameType = "ping"
	TypePong      FrameType = "pong"
	TypeResync    FrameType = "resync"
)

type Frame struct {
	Lane      Lane       `json:"lane"`
	Type      FrameType  `json:"type"`
	Hello     *Hello     `json:"hello,omitempty"`
	Welcome   *Welcome   `json:"welcome,omitempty"`
	Intent    *Intent    `json:"intent,omitempty"`
	Ack       *Ack       `json:"ack,omitempty"`
	Event     *Event     `json:"event,omitempty"`
	Ephemeral *Ephemeral `json:"ephemeral,omitempty"`
	Error     *Error     `json:"error,omitempty"`
	Ping      *Ping      `json:"ping,omitempty"`
	Resync    *Resync    `json:"resync,omitempty"`
}

type Hello struct {
	Ticket          string   `json:"ticket"`
	WorldID         string   `json:"worldId"`
	ProtocolVersion uint32   `json:"protocolVersion"`
	LastSeq         int64    `json:"lastSeq"`
	Locale          string   `json:"locale,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
}

type Welcome struct {
	SessionID   string `json:"sessionId"`
	UserID      string `json:"userId"`
	Role        string `json:"role"`
	Seq         int64  `json:"seq"`
	DeltaFrom   int64  `json:"deltaFrom"`
	SnapshotRef string `json:"snapshotRef,omitempty"`
}

type Intent struct {
	RequestID uint32          `json:"requestId"`
	Kind      string          `json:"kind"`
	WorldID   string          `json:"worldId"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type Ack struct {
	RequestID uint32          `json:"requestId"`
	Seq       int64           `json:"seq"`
	Result    json.RawMessage `json:"result,omitempty"`
}

type Event struct {
	Seq     int64           `json:"seq"`
	Kind    string          `json:"kind"`
	WorldID string          `json:"worldId"`
	SceneID string          `json:"sceneId,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type Ephemeral struct {
	Kind    string          `json:"kind"`
	Key     string          `json:"key"`
	WorldID string          `json:"worldId"`
	SceneID string          `json:"sceneId,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type Error struct {
	RequestID  uint32            `json:"requestId,omitempty"`
	Code       string            `json:"code"`
	MessageKey string            `json:"messageKey"`
	Params     map[string]string `json:"params,omitempty"`
}

type Ping struct {
	SentAtUnixMs int64 `json:"sentAtUnixMs"`
}

type Resync struct {
	Reason  string `json:"reason"`
	FromSeq int64  `json:"fromSeq"`
}

const (
	CodeBadRequest     = "bad_request"
	CodeUnauthorized   = "unauthorized"
	CodeForbidden      = "forbidden"
	CodeNotFound       = "not_found"
	CodeUnsupported    = "unsupported_protocol"
	CodeRateLimited    = "rate_limited"
	CodeInternal       = "internal"
	CodeSlowConsumer   = "slow_consumer"
	ProtocolVersion    = 1
	MinProtocolVersion = 1
)

type Codec interface {
	Name() string
	Binary() bool
	Encode(frame Frame) ([]byte, error)
	Decode(data []byte) (Frame, error)
}

type JSONCodec struct{}

func (JSONCodec) Name() string { return "json" }

func (JSONCodec) Binary() bool { return false }

func (JSONCodec) Encode(frame Frame) ([]byte, error) {
	return json.Marshal(frame)
}

func (JSONCodec) Decode(data []byte) (Frame, error) {
	var frame Frame
	if err := json.Unmarshal(data, &frame); err != nil {
		return Frame{}, fmt.Errorf("decode frame: %w", err)
	}
	if frame.Type == "" {
		return Frame{}, fmt.Errorf("decode frame: missing type")
	}
	return frame, nil
}

func errorFrame(requestID uint32, code, messageKey string) Frame {
	return Frame{
		Lane: LaneControl,
		Type: TypeError,
		Error: &Error{
			RequestID:  requestID,
			Code:       code,
			MessageKey: messageKey,
		},
	}
}
