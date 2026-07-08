package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Hub управляет всеми подключёнными агентами
type Hub struct {
	agents map[string]*Agent // ключ — agent.ID
	mu     sync.RWMutex

	// Хранилище токенов: token → агент ID
	tokens map[string]string
	tokMu  sync.RWMutex
}

var hub = &Hub{
	agents: make(map[string]*Agent),
	tokens: make(map[string]string),
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// WS-протокол между сервером и агентом
type WSMessage struct {
	Type     string          `json:"type"`
	ID       string          `json:"id,omitempty"`
	Method   string          `json:"method,omitempty"`
	Path     string          `json:"path,omitempty"`
	Body     json.RawMessage `json:"body,omitempty"`
	Status   int             `json:"status,omitempty"`
	Hostname string          `json:"hostname,omitempty"`
	Version  string          `json:"version,omitempty"`
	Token    string          `json:"token,omitempty"`
	AgentID  string          `json:"agent_id,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// TunnelRequest — запрос к агенту через WS
type TunnelRequest struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"` // "request"
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// TunnelResponse — ответ от агента
type TunnelResponse struct {
	Status int
	Body   []byte
}

// ServeAgentWS обрабатывает WebSocket подключение агента
func (h *Hub) ServeAgentWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[WS] upgrade error: %v", err)
		return
	}

	realIP := r.Header.Get("X-Real-IP")
	if realIP == "" {
		realIP = r.Header.Get("X-Forwarded-For")
	}
	if realIP == "" {
		realIP = r.RemoteAddr
		// убираем порт
		if idx := strings.LastIndex(realIP, ":"); idx != -1 {
			realIP = realIP[:idx]
		}
	}

	// Ожидаем hello-сообщение
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var hello WSMessage
	if err := conn.ReadJSON(&hello); err != nil || hello.Type != "hello" {
		log.Printf("[WS] bad hello from %s: %v", realIP, err)
		conn.Close()
		return
	}
	conn.SetReadDeadline(time.Time{}) // сбрасываем дедлайн

	// Идентифицируем агента
	agent := h.identify(hello, conn, realIP)

	// Отправляем welcome
	welcome := WSMessage{
		Type:    "welcome",
		AgentID: agent.ID,
		Token:   agent.Token,
	}
	if err := conn.WriteJSON(welcome); err != nil {
		log.Printf("[WS] welcome error for %s: %v", agent.Hostname, err)
		conn.Close()
		return
	}

	log.Printf("[WS] agent connected: %s (%s) v%s from %s", agent.Hostname, agent.ID, agent.Version, realIP)

	// Запускаем ping-loop
	go h.pingLoop(agent)

	// Читаем сообщения от агента
	h.readLoop(agent)
}

// identify регистрирует или обновляет агента
func (h *Hub) identify(hello WSMessage, conn *websocket.Conn, ip string) *Agent {
	h.tokMu.Lock()
	defer h.tokMu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()

	var agentID string

	// Ищем существующий агент по токену
	if hello.Token != "" {
		if id, ok := h.tokens[hello.Token]; ok {
			agentID = id
		}
	}

	if agentID == "" {
		// Новый агент — генерируем ID и токен
		agentID = newUUID()
	}

	token := hello.Token
	if token == "" {
		token = newUUID()
		h.tokens[token] = agentID
	}

	now := time.Now()

	existing, exists := h.agents[agentID]
	if exists {
		// Закрываем старое соединение если есть
		existing.mu.Lock()
		if existing.conn != nil {
			existing.conn.Close()
		}
		existing.conn = conn
		existing.IP = ip
		existing.Version = hello.Version
		existing.LastSeen = now
		existing.mu.Unlock()
		existing.CancelAllPending()
		return existing
	}

	agent := &Agent{
		ID:          agentID,
		Hostname:    hello.Hostname,
		Token:       token,
		IP:          ip,
		Version:     hello.Version,
		ConnectedAt: now,
		LastSeen:    now,
		conn:        conn,
		pending:     make(map[string]chan *TunnelResponse),
	}
	h.agents[agentID] = agent
	if _, ok := h.tokens[token]; !ok {
		h.tokens[token] = agentID
	}

	return agent
}

// readLoop читает сообщения от агента
func (h *Hub) readLoop(agent *Agent) {
	defer h.disconnect(agent)

	agent.conn.SetPongHandler(func(string) error {
		agent.mu.Lock()
		agent.LastSeen = time.Now()
		agent.mu.Unlock()
		agent.conn.SetReadDeadline(time.Now().Add(time.Duration(PingInterval+PongTimeout) * time.Second))
		return nil
	})

	for {
		agent.conn.SetReadDeadline(time.Now().Add(time.Duration(PingInterval+PongTimeout) * time.Second))
		var msg WSMessage
		if err := agent.conn.ReadJSON(&msg); err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				log.Printf("[WS] read error for %s: %v", agent.Hostname, err)
			}
			return
		}

		agent.mu.Lock()
		agent.LastSeen = time.Now()
		agent.mu.Unlock()

		switch msg.Type {
		case "response":
			// Ответ на проксированный запрос
			resp := &TunnelResponse{
				Status: msg.Status,
				Body:   msg.Body,
			}
			if msg.Error != "" {
				resp.Status = 500
				resp.Body = []byte(fmt.Sprintf(`{"error":%q}`, msg.Error))
			}
			agent.ResolvePending(msg.ID, resp)

		case "ping":
			// Агент пингует нас
			agent.Send(WSMessage{Type: "pong"})

		default:
			log.Printf("[WS] unknown message type %q from %s", msg.Type, agent.Hostname)
		}
	}
}

// pingLoop отправляет пинги агенту
func (h *Hub) pingLoop(agent *Agent) {
	ticker := time.NewTicker(time.Duration(PingInterval) * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		agent.mu.Lock()
		conn := agent.conn
		agent.mu.Unlock()

		if conn == nil {
			return
		}

		agent.mu.Lock()
		err := conn.WriteMessage(websocket.PingMessage, nil)
		agent.mu.Unlock()

		if err != nil {
			return
		}
	}
}

// disconnect удаляет агента из активных
func (h *Hub) disconnect(agent *Agent) {
	log.Printf("[WS] agent disconnected: %s (%s)", agent.Hostname, agent.ID)
	agent.CancelAllPending()

	h.mu.Lock()
	// Не удаляем из map — оставляем запись (offline будет видно через LastSeen)
	// Но обнуляем conn
	if a, ok := h.agents[agent.ID]; ok {
		a.mu.Lock()
		a.conn = nil
		a.mu.Unlock()
	}
	h.mu.Unlock()
}

// GetAgent возвращает агента по ID
func (h *Hub) GetAgent(id string) (*Agent, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	a, ok := h.agents[id]
	return a, ok
}

// GetAllAgents возвращает список всех агентов
func (h *Hub) GetAllAgents() []*Agent {
	h.mu.RLock()
	defer h.mu.RUnlock()
	list := make([]*Agent, 0, len(h.agents))
	for _, a := range h.agents {
		list = append(list, a)
	}
	return list
}

// IsOnline проверяет, подключён ли агент
func (h *Hub) IsOnline(id string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	a, ok := h.agents[id]
	if !ok {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.conn != nil
}
