package ws

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/tavora-vtt/tavora-server/internal/storage"
)

const DefaultTicketTTL = 30 * time.Second

var ErrTicketInvalid = errors.New("ws: ticket invalid or expired")

type Ticket struct {
	UserID    storage.ID
	WorldID   storage.ID
	Role      string
	ExpiresAt time.Time
}

type TicketStore struct {
	mu      sync.Mutex
	tickets map[string]Ticket
	ttl     time.Duration
	now     func() time.Time
}

func NewTicketStore(ttl time.Duration) *TicketStore {
	if ttl <= 0 {
		ttl = DefaultTicketTTL
	}
	return &TicketStore{
		tickets: make(map[string]Ticket),
		ttl:     ttl,
		now:     time.Now,
	}
}

func (s *TicketStore) Issue(userID, worldID storage.ID, role string) (string, time.Time, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("ws: generate ticket: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	s.mu.Lock()
	defer s.mu.Unlock()

	expires := s.now().Add(s.ttl)
	s.tickets[token] = Ticket{
		UserID:    userID,
		WorldID:   worldID,
		Role:      role,
		ExpiresAt: expires,
	}
	s.sweepLocked()

	return token, expires, nil
}

func (s *TicketStore) Redeem(token string) (Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ticket, present := s.tickets[token]
	if !present {
		return Ticket{}, ErrTicketInvalid
	}

	delete(s.tickets, token)

	if s.now().After(ticket.ExpiresAt) {
		return Ticket{}, ErrTicketInvalid
	}
	return ticket, nil
}

func (s *TicketStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tickets)
}

func (s *TicketStore) sweepLocked() {
	now := s.now()
	for token, ticket := range s.tickets {
		if now.After(ticket.ExpiresAt) {
			delete(s.tickets, token)
		}
	}
}
