package server

import "sync"

// Hub maintains the set of active broadcast channels.
type Hub struct {
	mu      sync.RWMutex
	clients map[chan []byte]struct{}
	closed  bool
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
	if h.closed {
		close(ch)
	} else {
		h.clients[ch] = struct{}{}
	}
	h.mu.Unlock()
}

// Close closes every registered channel, which makes the WebSocket handlers
// end, and closes channels registered afterwards at once.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for ch := range h.clients {
		close(ch)
		delete(h.clients, ch)
	}
}

// Unregister removes a channel from the hub.
func (h *Hub) Unregister(ch chan []byte) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
}

// ClientCount returns the number of registered clients.
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
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
