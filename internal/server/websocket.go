package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		// Allow all origins for now; can be tightened later.
		return true
	},
}

// TaskEvent is broadcast to WebSocket clients on task status changes.
type TaskEvent struct {
	Type    string `json:"type"`
	Task    string `json:"task"`
	Phase   string `json:"phase"`
	Step    string `json:"step,omitempty"`
	Message string `json:"message,omitempty"`
}

// Hub manages WebSocket clients and broadcasts task events.
type Hub struct {
	mu      sync.RWMutex
	clients map[*wsClient]struct{}
}

type wsClient struct {
	conn *websocket.Conn
	send chan []byte
}

// NewHub creates a new WebSocket hub.
func NewHub() *Hub {
	return &Hub{
		clients: make(map[*wsClient]struct{}),
	}
}

// Run starts the hub's main loop. It exits when ctx is cancelled.
func (h *Hub) Run(ctx context.Context) {
	<-ctx.Done()

	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		close(c.send)
		_ = c.conn.Close()
		delete(h.clients, c)
	}
}

// BroadcastMessage sends an arbitrary JSON-serializable message to all connected clients.
func (h *Hub) BroadcastMessage(data any) {
	msg, err := json.Marshal(data)
	if err != nil {
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.clients {
		select {
		case c.send <- msg:
		default:
		}
	}
}

// Broadcast sends a TaskEvent to all connected clients.
func (h *Hub) Broadcast(event TaskEvent) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}

	h.mu.RLock()
	defer h.mu.RUnlock()

	for c := range h.clients {
		select {
		case c.send <- data:
		default:
			// Client buffer full — drop message.
		}
	}
}

func (h *Hub) register(c *wsClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[c] = struct{}{}
}

func (h *Hub) unregister(c *wsClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.clients[c]; ok {
		close(c.send)
		delete(h.clients, c)
	}
}

func (s *APIServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Error(err, "failed to upgrade to websocket")
		return
	}

	c := &wsClient{
		conn: conn,
		send: make(chan []byte, 64),
	}

	s.hub.register(c)

	// Writer goroutine: sends messages from the send channel.
	go func() {
		defer func() {
			s.hub.unregister(c)
			_ = conn.Close()
		}()
		for msg := range c.send {
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		}
	}()

	// Reader goroutine: reads (and discards) messages to detect disconnect.
	go func() {
		defer func() {
			s.hub.unregister(c)
			_ = conn.Close()
		}()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
}
