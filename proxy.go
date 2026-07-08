package main

import (
	"encoding/json"
	"fmt"
	"time"
)

const proxyTimeout = 30 * time.Second

// ProxyRequest отправляет запрос агенту через WebSocket и ждёт ответ
func ProxyRequest(agent *Agent, method, path string, body json.RawMessage) (*TunnelResponse, error) {
	if !hub.IsOnline(agent.ID) {
		return &TunnelResponse{Status: 503, Body: []byte(`{"error":"agent offline"}`)}, nil
	}

	reqID := newUUID()
	ch := make(chan *TunnelResponse, 1)
	agent.RegisterPending(reqID, ch)

	req := WSMessage{
		ID:     reqID,
		Type:   "request",
		Method: method,
		Path:   path,
		Body:   body,
	}

	if err := agent.Send(req); err != nil {
		// Удаляем pending
		agent.pendingMu.Lock()
		delete(agent.pending, reqID)
		agent.pendingMu.Unlock()
		return nil, fmt.Errorf("send error: %w", err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(proxyTimeout):
		agent.pendingMu.Lock()
		delete(agent.pending, reqID)
		agent.pendingMu.Unlock()
		return &TunnelResponse{Status: 504, Body: []byte(`{"error":"timeout waiting for agent response"}`)}, nil
	}
}
