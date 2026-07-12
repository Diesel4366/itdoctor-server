package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

var controlUpgrader = websocket.Upgrader{
	HandshakeTimeout: 5 * time.Second,
	CheckOrigin:      func(r *http.Request) bool { return true },
}

// ControlEvent — событие мыши или клавиатуры от браузера
type ControlEvent struct {
	// Мышь
	Action string `json:"action,omitempty"`
	X      int    `json:"x,omitempty"`
	Y      int    `json:"y,omitempty"`
	Button string `json:"button,omitempty"`
	Delta  int    `json:"delta,omitempty"`
	// Клавиатура
	Key string `json:"key,omitempty"`
}

// handleControlWS — WebSocket соединение от браузера для управления мышью/клавиатурой.
// Браузер подключается: ws://server/api/agents/:id/control?key=<pulse-key>
// Каждое сообщение — JSON событие мыши или клавиатуры.
// Сервер немедленно проксирует к агенту через туннель без HTTP overhead.
func handleControlWS(w http.ResponseWriter, r *http.Request, agentID string) {
	// Auth через query param (WS не может слать кастомные заголовки в браузере)
	if r.URL.Query().Get("key") != PulseKey {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	agent := hub.GetAgent(agentID)
	if agent == nil {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	conn, err := controlUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(4096)
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	// Пинг каждые 20 сек чтобы соединение не разрывалось
	pingTicker := time.NewTicker(20 * time.Second)
	defer pingTicker.Stop()

	done := make(chan struct{})

	go func() {
		defer close(done)
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}

			var evt ControlEvent
			if err := json.Unmarshal(msg, &evt); err != nil {
				continue
			}

			// Определяем путь: мышь или клавиатура
			var path string
			if evt.Key != "" {
				path = "/api/keyboard"
			} else {
				path = "/api/mouse"
			}

			// Проксируем к агенту через WebSocket туннель — fire and forget
			go func(p string, body []byte) {
				agent := hub.GetAgent(agentID)
				if agent == nil {
					return
				}
				ProxyRequest(agent, "POST", p, body)
			}(path, msg)
		}
	}()

	for {
		select {
		case <-done:
			return
		case <-pingTicker.C:
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
