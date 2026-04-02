package server

import "sync"

// Hub maintains the set of active broadcast channels.
type Hub struct {
	mu      sync.RWMutex
	clients map[chan []byte]struct{}
}

// NewHub creates an initialized Hub.
func NewHub() *Hub {
	return &Hub{
		clients: make(map[chan []byte]struct{}),
	}
}

// Run is a no-op; the hub is passive and methods are called directly.
func (h *Hub) Run() {}

// Register adds a channel to the hub.
func (h *Hub) Register(ch chan []byte) {
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
}

// Unregister removes a channel from the hub.
func (h *Hub) Unregister(ch chan []byte) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
}

// Broadcast sends msg to all registered clients; skips slow ones.
func (h *Hub) Broadcast(msg []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.clients {
		select {
		case ch <- msg:
		default:
		}
	}
}
