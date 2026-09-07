package ws

import (
	"log/slog"
	"sync"

	"github.com/tavora-vtt/tavora-server/internal/storage"
)

func WorldChannel(id storage.ID) string { return "world:" + string(id) }
func SceneChannel(id storage.ID) string { return "scene:" + string(id) }
func ActorChannel(id storage.ID) string { return "actor:" + string(id) }
func UserChannel(id storage.ID) string  { return "user:" + string(id) }

type Publication struct {
	Channel  string
	Lane     Lane
	Key      string
	Frame    Frame
	Audience []storage.ID
	Exclude  string
}

func (p Publication) allows(userID storage.ID) bool {
	if len(p.Audience) == 0 {
		return true
	}
	for _, allowed := range p.Audience {
		if allowed == userID {
			return true
		}
	}
	return false
}

type Hub struct {
	worldID storage.ID
	log     *slog.Logger

	join    chan *Session
	leave   chan *Session
	publish chan Publication
	inspect chan func([]*Session)
	quit    chan struct{}

	once sync.Once
	done chan struct{}
}

func NewHub(worldID storage.ID, log *slog.Logger) *Hub {
	hub := &Hub{
		worldID: worldID,
		log:     log.With("world", string(worldID)),
		join:    make(chan *Session),
		leave:   make(chan *Session),
		publish: make(chan Publication, 256),
		inspect: make(chan func([]*Session)),
		quit:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go hub.run()
	return hub
}

func (h *Hub) run() {
	defer close(h.done)

	sessions := make(map[string]*Session)

	for {
		select {
		case session := <-h.join:
			sessions[session.ID()] = session
			h.log.Debug("session joined", "session", session.ID(), "sessions", len(sessions))

		case session := <-h.leave:
			delete(sessions, session.ID())
			h.log.Debug("session left", "session", session.ID(), "sessions", len(sessions))

		case publication := <-h.publish:
			h.fanOut(sessions, publication)

		case fn := <-h.inspect:
			list := make([]*Session, 0, len(sessions))
			for _, session := range sessions {
				list = append(list, session)
			}
			fn(list)

		case <-h.quit:
			for _, session := range sessions {
				session.CloseWithResync("world closed", 0)
			}
			return
		}
	}
}

func (h *Hub) fanOut(sessions map[string]*Session, publication Publication) {
	for id, session := range sessions {
		if id == publication.Exclude {
			continue
		}
		if !session.Subscribed(publication.Channel) {
			continue
		}
		if !publication.allows(session.UserID()) {
			continue
		}

		if err := session.deliver(publication); err != nil {
			h.log.Warn("dropping session",
				"session", id,
				"user", string(session.UserID()),
				"error", err,
			)
			session.CloseWithResync("slow consumer", session.LastSeq())
			delete(sessions, id)
		}
	}
}

func (h *Hub) Join(session *Session) {
	select {
	case h.join <- session:
	case <-h.done:
	}
}

func (h *Hub) Leave(session *Session) {
	select {
	case h.leave <- session:
	case <-h.done:
	}
}

func (h *Hub) Publish(publication Publication) {
	select {
	case h.publish <- publication:
	case <-h.done:
	}
}

func (h *Hub) Sessions() []*Session {
	result := make(chan []*Session, 1)
	select {
	case h.inspect <- func(sessions []*Session) { result <- sessions }:
		return <-result
	case <-h.done:
		return nil
	}
}

func (h *Hub) Close() {
	h.once.Do(func() { close(h.quit) })
	<-h.done
}

type Registry struct {
	mu   sync.Mutex
	log  *slog.Logger
	hubs map[storage.ID]*Hub
}

func NewRegistry(log *slog.Logger) *Registry {
	return &Registry{log: log, hubs: make(map[storage.ID]*Hub)}
}

func (r *Registry) Hub(worldID storage.ID) *Hub {
	r.mu.Lock()
	defer r.mu.Unlock()

	if hub, present := r.hubs[worldID]; present {
		return hub
	}
	hub := NewHub(worldID, r.log)
	r.hubs[worldID] = hub
	return hub
}

func (r *Registry) Close() {
	r.mu.Lock()
	hubs := make([]*Hub, 0, len(r.hubs))
	for _, hub := range r.hubs {
		hubs = append(hubs, hub)
	}
	r.hubs = make(map[storage.ID]*Hub)
	r.mu.Unlock()

	for _, hub := range hubs {
		hub.Close()
	}
}
