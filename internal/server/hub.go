package server

import (
	"sync"

	"github.com/sherifhamad/shixo-msn/internal/proto"
)

// Hub fans out events to all connected websocket clients.
type Hub struct {
	mu      sync.Mutex
	clients map[chan proto.Event]struct{}
}

func NewHub() *Hub {
	return &Hub{clients: map[chan proto.Event]struct{}{}}
}

func (h *Hub) Subscribe() chan proto.Event {
	ch := make(chan proto.Event, 32)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) Unsubscribe(ch chan proto.Event) {
	h.mu.Lock()
	if _, ok := h.clients[ch]; ok {
		delete(h.clients, ch)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *Hub) Publish(ev proto.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- ev:
		default:
			// drop on slow consumer; they'll reconnect and refetch list
		}
	}
}
