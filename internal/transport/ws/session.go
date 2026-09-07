package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tavora-vtt/tavora-server/internal/core/access"
	"github.com/tavora-vtt/tavora-server/internal/core/perm"
	"github.com/tavora-vtt/tavora-server/internal/storage"
)

const (
	catchUpLimit    = 500
	handshakeWindow = 10 * time.Second
)

type Conn interface {
	Read(ctx context.Context) (data []byte, binary bool, err error)
	Write(ctx context.Context, binary bool, data []byte) error
	Close(reason string) error
}

type IntentError struct {
	Code       string
	MessageKey string
}

func (e *IntentError) Error() string {
	return fmt.Sprintf("%s (%s)", e.MessageKey, e.Code)
}

func NewIntentError(code, messageKey string) *IntentError {
	return &IntentError{Code: code, MessageKey: messageKey}
}

type IntentResult struct {
	Seq    int64
	Result json.RawMessage
}

type IntentFunc func(ctx context.Context, session *Session, intent Intent) (IntentResult, error)

type Router struct {
	handlers map[string]IntentFunc
}

func NewRouter() *Router {
	return &Router{handlers: make(map[string]IntentFunc)}
}

func (r *Router) Handle(kind string, fn IntentFunc) {
	r.handlers[kind] = fn
}

func (r *Router) lookup(kind string) (IntentFunc, bool) {
	fn, present := r.handlers[kind]
	return fn, present
}

type Deps struct {
	Store        storage.Store
	Registry     *Registry
	Tickets      *TicketStore
	Router       *Router
	Access       *access.Resolver
	Log          *slog.Logger
	DocumentCap  int
	EphemeralCap int
}

type Session struct {
	id      string
	conn    Conn
	codec   Codec
	outbox  *outbox
	deps    Deps
	log     *slog.Logger
	hub     *Hub
	userID  storage.ID
	worldID storage.ID
	role    string

	lastSeq atomic.Int64

	mu            sync.RWMutex
	subscriptions map[string]struct{}

	closed chan struct{}
}

var sessionCounter atomic.Uint64

func NewSession(conn Conn, codec Codec, deps Deps) *Session {
	id := fmt.Sprintf("s%d-%d", time.Now().UnixNano(), sessionCounter.Add(1))

	return &Session{
		id:            id,
		conn:          conn,
		codec:         codec,
		outbox:        newOutbox(deps.DocumentCap, deps.EphemeralCap),
		deps:          deps,
		log:           deps.Log.With("session", id),
		subscriptions: make(map[string]struct{}),
		closed:        make(chan struct{}),
	}
}

func (s *Session) ID() string              { return s.id }
func (s *Session) UserID() storage.ID      { return s.userID }
func (s *Session) WorldID() storage.ID     { return s.worldID }
func (s *Session) Role() string            { return s.role }
func (s *Session) LastSeq() int64          { return s.lastSeq.Load() }
func (s *Session) Store() storage.Store    { return s.deps.Store }
func (s *Session) Hub() *Hub               { return s.hub }
func (s *Session) Log() *slog.Logger       { return s.log }
func (s *Session) Closed() <-chan struct{} { return s.closed }

func (s *Session) Subscribe(channels ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, channel := range channels {
		s.subscriptions[channel] = struct{}{}
	}
}

func (s *Session) Unsubscribe(channels ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, channel := range channels {
		delete(s.subscriptions, channel)
	}
}

func (s *Session) Subscribed(channel string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, present := s.subscriptions[channel]
	return present
}

func (s *Session) deliver(publication Publication, frame Frame) error {
	switch publication.Lane {
	case LaneEphemeral:
		return s.outbox.PushEphemeral(publication.Key, frame)
	case LaneControl:
		return s.outbox.PushControl(frame)
	default:
		return s.outbox.PushDocument(frame)
	}
}

func (s *Session) CloseWithResync(reason string, fromSeq int64) {
	_ = s.outbox.PushControl(Frame{
		Lane:   LaneControl,
		Type:   TypeResync,
		Resync: &Resync{Reason: reason, FromSeq: fromSeq},
	})
	s.outbox.Close()
}

func (s *Session) Serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	hello, err := s.authenticate(ctx)
	if err != nil {
		_ = s.writeControlNow(ctx, errorFrame(0, codeForHandshake(err), "core.ws.handshakeFailed"))
		_ = s.conn.Close("handshake failed")
		return err
	}

	s.hub = s.deps.Registry.Hub(s.worldID)
	s.hub.Join(s)

	defer func() {
		s.hub.Leave(s)
		s.outbox.Close()
		close(s.closed)
		_ = s.conn.Close("session ended")
	}()

	if err := s.welcome(ctx, hello); err != nil {
		_ = s.writeControlNow(ctx, errorFrame(0, codeForHandshake(err), "core.ws.handshakeFailed"))
		return err
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.writeLoop(ctx)
	}()

	readErr := s.readLoop(ctx)
	s.outbox.Close()
	cancel()
	wg.Wait()
	return readErr
}

func (s *Session) authenticate(parent context.Context) (*Hello, error) {
	ctx, cancel := context.WithTimeout(parent, handshakeWindow)
	defer cancel()

	data, _, err := s.conn.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("read hello: %w", err)
	}

	frame, err := s.codec.Decode(data)
	if err != nil {
		return nil, err
	}
	if frame.Type != TypeHello || frame.Hello == nil {
		return nil, errors.New("ws: first frame must be hello")
	}

	hello := frame.Hello
	if hello.ProtocolVersion < MinProtocolVersion || hello.ProtocolVersion > ProtocolVersion {
		return nil, fmt.Errorf("ws: unsupported protocol version %d", hello.ProtocolVersion)
	}

	ticket, err := s.deps.Tickets.Redeem(hello.Ticket)
	if err != nil {
		return nil, err
	}
	if hello.WorldID != "" && storage.ID(hello.WorldID) != ticket.WorldID {
		return nil, errors.New("ws: ticket does not grant this world")
	}

	s.userID = ticket.UserID
	s.worldID = ticket.WorldID

	role, err := s.resolveRole(ctx)
	if err != nil {
		return nil, err
	}
	s.role = string(role)

	s.log = s.log.With("user", string(s.userID), "world", string(s.worldID), "role", s.role)
	s.Subscribe(WorldChannel(s.worldID), UserChannel(s.userID))

	return hello, nil
}

func (s *Session) welcome(ctx context.Context, hello *Hello) error {
	world, err := s.currentWorld(ctx)
	if err != nil {
		return err
	}

	deltaFrom := hello.LastSeq
	if deltaFrom < 0 || deltaFrom > world.EventSeq {
		deltaFrom = 0
	}
	s.lastSeq.Store(deltaFrom)

	err = s.writeControlNow(ctx, Frame{
		Lane: LaneControl,
		Type: TypeWelcome,
		Welcome: &Welcome{
			SessionID: s.id,
			UserID:    string(s.userID),
			Role:      s.role,
			Seq:       world.EventSeq,
			DeltaFrom: deltaFrom,
		},
	})
	if err != nil {
		return err
	}

	replay, err := s.replayFrames(ctx, deltaFrom)
	if err != nil {
		return err
	}
	return s.outbox.PrependDocument(replay)
}

func (s *Session) resolveRole(ctx context.Context) (perm.Role, error) {
	var role perm.Role
	err := s.deps.Store.ReadOnly(ctx, func(q storage.Query) error {
		var readErr error
		role, readErr = s.deps.Access.RoleOf(ctx, q, s.worldID, s.userID)
		return readErr
	})
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNotAMember, err)
	}
	return role, nil
}

func (s *Session) Subject() perm.Subject {
	return perm.Subject{UserID: s.userID, Role: perm.Role(s.role)}
}

func (s *Session) Access() *access.Resolver { return s.deps.Access }

func (s *Session) currentWorld(ctx context.Context) (*storage.World, error) {
	var world *storage.World
	err := s.deps.Store.ReadOnly(ctx, func(q storage.Query) error {
		var readErr error
		world, readErr = q.GetWorld(ctx, s.worldID)
		return readErr
	})
	if err != nil {
		return nil, fmt.Errorf("load world: %w", err)
	}
	return world, nil
}

func (s *Session) replayFrames(ctx context.Context, fromSeq int64) ([]Frame, error) {
	var frames []Frame

	err := s.deps.Store.ReadOnly(ctx, func(q storage.Query) error {
		events, err := q.EventsSince(ctx, s.worldID, fromSeq, catchUpLimit)
		if err != nil {
			return err
		}

		frames = make([]Frame, 0, len(events))
		for _, event := range events {
			if !audienceAllows(event.Audience, s.userID) {
				continue
			}

			payload, include, err := s.replayPayload(ctx, q, event)
			if err != nil {
				return err
			}
			if !include {
				continue
			}

			frames = append(frames, Frame{
				Lane: LaneDocument,
				Type: TypeEvent,
				Event: &Event{
					Seq:     event.Seq,
					Kind:    event.Kind,
					WorldID: string(event.WorldID),
					SceneID: string(event.SceneID),
					Payload: payload,
				},
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("catch up: %w", err)
	}
	return frames, nil
}

func (s *Session) replayPayload(ctx context.Context, q storage.Query, event storage.Event) (json.RawMessage, bool, error) {
	if event.TargetKind != "document" || event.TargetID == "" {
		return event.Payload, true, nil
	}

	doc, err := q.GetDocument(ctx, s.worldID, event.TargetID)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	subject := s.Subject()

	grant, err := s.deps.Access.Grant(ctx, q, subject, doc)
	if err != nil {
		return nil, false, err
	}

	visible, send, err := perm.RedactDocument(doc, subject, grant, s.deps.Access.Policy())
	if err != nil {
		return nil, false, err
	}
	if !send {
		return nil, false, nil
	}

	payload, err := json.Marshal(viewOf(access.View{Subject: subject, Grant: grant, Document: visible}, event.Seq))
	if err != nil {
		return nil, false, err
	}
	return payload, true, nil
}

func (s *Session) readLoop(ctx context.Context) error {
	for {
		data, _, err := s.conn.Read(ctx)
		if err != nil {
			return err
		}

		frame, err := s.codec.Decode(data)
		if err != nil {
			_ = s.outbox.PushControl(errorFrame(0, CodeBadRequest, "core.ws.malformedFrame"))
			continue
		}

		switch frame.Type {
		case TypePing:
			s.handlePing(frame)
		case TypeIntent:
			s.handleIntent(ctx, frame)
		case TypeEphemeral:
			s.handleEphemeral(frame)
		default:
			_ = s.outbox.PushControl(errorFrame(0, CodeBadRequest, "core.ws.unexpectedFrame"))
		}
	}
}

func (s *Session) handlePing(frame Frame) {
	sentAt := int64(0)
	if frame.Ping != nil {
		sentAt = frame.Ping.SentAtUnixMs
	}
	_ = s.outbox.PushControl(Frame{
		Lane: LaneControl,
		Type: TypePong,
		Ping: &Ping{SentAtUnixMs: sentAt},
	})
}

func (s *Session) handleEphemeral(frame Frame) {
	if frame.Ephemeral == nil || frame.Ephemeral.Key == "" {
		_ = s.outbox.PushControl(errorFrame(0, CodeBadRequest, "core.ws.ephemeralNeedsKey"))
		return
	}

	channel := WorldChannel(s.worldID)
	if frame.Ephemeral.SceneID != "" {
		channel = SceneChannel(storage.ID(frame.Ephemeral.SceneID))
	}

	s.hub.Publish(Publication{
		Channel: channel,
		Lane:    LaneEphemeral,
		Key:     frame.Ephemeral.Key,
		Frame:   Frame{Lane: LaneEphemeral, Type: TypeEphemeral, Ephemeral: frame.Ephemeral},
		Exclude: s.id,
	})
}

func (s *Session) handleIntent(ctx context.Context, frame Frame) {
	if frame.Intent == nil {
		_ = s.outbox.PushControl(errorFrame(0, CodeBadRequest, "core.ws.missingIntent"))
		return
	}
	intent := *frame.Intent

	handler, present := s.deps.Router.lookup(intent.Kind)
	if !present {
		_ = s.outbox.PushDocument(errorFrame(intent.RequestID, CodeUnsupported, "core.ws.unknownIntent"))
		return
	}

	outcome, err := handler(ctx, s, intent)
	if err != nil {
		var intentErr *IntentError
		if errors.As(err, &intentErr) {
			_ = s.outbox.PushDocument(errorFrame(intent.RequestID, intentErr.Code, intentErr.MessageKey))
			return
		}
		s.log.Error("intent failed", "kind", intent.Kind, "error", err)
		_ = s.outbox.PushDocument(errorFrame(intent.RequestID, CodeInternal, "core.ws.internalError"))
		return
	}

	sequence := outcome.Seq
	if sequence == 0 {
		sequence = s.lastSeq.Load()
	}

	_ = s.outbox.PushDocument(Frame{
		Lane: LaneDocument,
		Type: TypeAck,
		Ack: &Ack{
			RequestID: intent.RequestID,
			Seq:       sequence,
			Result:    outcome.Result,
		},
	})
}

func (s *Session) writeLoop(ctx context.Context) {
	for {
		for {
			frame, present := s.outbox.Pop()
			if !present {
				break
			}
			if frame.Type == TypeEvent && frame.Event != nil {
				if frame.Event.Seq <= s.lastSeq.Load() {
					continue
				}
				s.lastSeq.Store(frame.Event.Seq)
			}

			data, err := s.codec.Encode(frame)
			if err != nil {
				s.log.Error("encode frame", "error", err)
				continue
			}
			if err := s.conn.Write(ctx, s.codec.Binary(), data); err != nil {
				return
			}
		}

		if s.outbox.Closed() {
			return
		}

		select {
		case <-s.outbox.Signal():
		case <-ctx.Done():
			return
		}
	}
}

func (s *Session) writeControlNow(ctx context.Context, frame Frame) error {
	data, err := s.codec.Encode(frame)
	if err != nil {
		return err
	}
	return s.conn.Write(ctx, s.codec.Binary(), data)
}

func audienceAllows(audience json.RawMessage, userID storage.ID) bool {
	if len(audience) == 0 {
		return true
	}
	var allowed []string
	if err := json.Unmarshal(audience, &allowed); err != nil {
		return true
	}
	if len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		if storage.ID(candidate) == userID {
			return true
		}
	}
	return false
}

var ErrNotAMember = errors.New("ws: not a member of this world")

func codeForHandshake(err error) string {
	switch {
	case errors.Is(err, ErrTicketInvalid):
		return CodeUnauthorized
	case errors.Is(err, ErrNotAMember):
		return CodeForbidden
	default:
		return CodeBadRequest
	}
}
