package main

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Agent описывает подключённый агент
type Agent struct {
	ID          string          `json:"id"`
	Hostname    string          `json:"hostname"`
	Token       string          `json:"-"`
	IP          string          `json:"ip"`
	Version     string          `json:"version"`
	ConnectedAt time.Time       `json:"connected_at"`
	LastSeen    time.Time       `json:"last_seen"`
	conn        *websocket.Conn `json:"-"`
	mu          sync.Mutex      `json:"-"`

	// Ожидающие ответа запросы: requestID → канал с ответом
	pending   map[string]chan *TunnelResponse `json:"-"`
	pendingMu sync.Mutex                      `json:"-"`
}

// AgentInfo — безопасная копия данных агента для JSON API
type AgentInfo struct {
	ID          string    `json:"id"`
	Hostname    string    `json:"hostname"`
	IP          string    `json:"ip"`
	Version     string    `json:"version"`
	ConnectedAt time.Time `json:"connected_at"`
	LastSeen    time.Time `json:"last_seen"`
	Online      bool      `json:"online"`
}

func (a *Agent) Info() AgentInfo {
	return AgentInfo{
		ID:          a.ID,
		Hostname:    a.Hostname,
		IP:          a.IP,
		Version:     a.Version,
		ConnectedAt: a.ConnectedAt,
		LastSeen:    a.LastSeen,
		Online:      true,
	}
}

// Send отправляет сообщение агенту (thread-safe)
func (a *Agent) Send(msg interface{}) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.conn.WriteJSON(msg)
}

// RegisterPending регистрирует канал ожидания ответа для requestID
func (a *Agent) RegisterPending(id string, ch chan *TunnelResponse) {
	a.pendingMu.Lock()
	defer a.pendingMu.Unlock()
	a.pending[id] = ch
}

// ResolvePending отправляет ответ в канал и удаляет из map
func (a *Agent) ResolvePending(id string, resp *TunnelResponse) {
	a.pendingMu.Lock()
	ch, ok := a.pending[id]
	if ok {
		delete(a.pending, id)
	}
	a.pendingMu.Unlock()

	if ok {
		select {
		case ch <- resp:
		default:
		}
	}
}

// CancelAllPending завершает все ожидающие запросы с ошибкой
func (a *Agent) CancelAllPending() {
	a.pendingMu.Lock()
	for id, ch := range a.pending {
		ch <- &TunnelResponse{Status: 503, Body: []byte(`{"error":"agent disconnected"}`)}
		delete(a.pending, id)
	}
	a.pendingMu.Unlock()
}
