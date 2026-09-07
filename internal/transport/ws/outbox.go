package ws

import (
	"errors"
	"sync"
)

var (
	ErrOutboxClosed     = errors.New("ws: outbox closed")
	ErrSlowConsumer     = errors.New("ws: document lane overflowed")
	defaultDocumentCap  = 256
	defaultEphemeralCap = 64
)

type outbox struct {
	mu sync.Mutex

	control   []Frame
	document  []Frame
	ephemeral map[string]Frame
	order     []string

	documentCap  int
	ephemeralCap int

	signal   chan struct{}
	closed   bool
	overflow bool

	droppedEphemeral int
}

func newOutbox(documentCap, ephemeralCap int) *outbox {
	if documentCap <= 0 {
		documentCap = defaultDocumentCap
	}
	if ephemeralCap <= 0 {
		ephemeralCap = defaultEphemeralCap
	}
	return &outbox{
		ephemeral:    make(map[string]Frame, ephemeralCap),
		documentCap:  documentCap,
		ephemeralCap: ephemeralCap,
		signal:       make(chan struct{}, 1),
	}
}

func (o *outbox) wake() {
	select {
	case o.signal <- struct{}{}:
	default:
	}
}

func (o *outbox) Signal() <-chan struct{} {
	return o.signal
}

func (o *outbox) PushControl(frame Frame) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.closed {
		return ErrOutboxClosed
	}
	o.control = append(o.control, frame)
	o.wake()
	return nil
}

func (o *outbox) PushDocument(frame Frame) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.closed {
		return ErrOutboxClosed
	}
	if len(o.document) >= o.documentCap {
		o.overflow = true
		return ErrSlowConsumer
	}
	o.document = append(o.document, frame)
	o.wake()
	return nil
}

func (o *outbox) PushEphemeral(key string, frame Frame) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.closed {
		return ErrOutboxClosed
	}

	if _, exists := o.ephemeral[key]; exists {
		o.ephemeral[key] = frame
		o.wake()
		return nil
	}

	if len(o.order) >= o.ephemeralCap {
		oldest := o.order[0]
		o.order = o.order[1:]
		delete(o.ephemeral, oldest)
		o.droppedEphemeral++
	}

	o.ephemeral[key] = frame
	o.order = append(o.order, key)
	o.wake()
	return nil
}

func (o *outbox) PrependDocument(frames []Frame) error {
	if len(frames) == 0 {
		return nil
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	if o.closed {
		return ErrOutboxClosed
	}

	combined := make([]Frame, 0, len(frames)+len(o.document))
	combined = append(combined, frames...)
	combined = append(combined, o.document...)
	o.document = combined
	o.wake()
	return nil
}

func (o *outbox) Pop() (Frame, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()

	if len(o.control) > 0 {
		frame := o.control[0]
		o.control = o.control[1:]
		return frame, true
	}
	if len(o.document) > 0 {
		frame := o.document[0]
		o.document = o.document[1:]
		return frame, true
	}
	if len(o.order) > 0 {
		key := o.order[0]
		o.order = o.order[1:]
		frame := o.ephemeral[key]
		delete(o.ephemeral, key)
		return frame, true
	}
	return Frame{}, false
}

func (o *outbox) Overflowed() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.overflow
}

func (o *outbox) DroppedEphemeral() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.droppedEphemeral
}

func (o *outbox) Depth() (control, document, ephemeral int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.control), len(o.document), len(o.order)
}

func (o *outbox) Close() {
	o.mu.Lock()
	defer o.mu.Unlock()

	if o.closed {
		return
	}
	o.closed = true
	o.wake()
}

func (o *outbox) Closed() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.closed
}
