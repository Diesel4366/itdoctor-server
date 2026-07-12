package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// corsMiddleware добавляет CORS заголовки
func corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "X-Pulse-Key, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

// extractAgentID вытаскивает ID агента из URL вида /api/agents/{id}/...
func extractAgentID(path string) (agentID, rest string) {
	// path: /api/agents/{id} или /api/agents/{id}/system
	trimmed := strings.TrimPrefix(path, "/api/agents/")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) == 0 {
		return "", ""
	}
	agentID = parts[0]
	if len(parts) > 1 {
		rest = "/" + parts[1]
	}
	return agentID, rest
}

// handleAgentsList — GET /api/agents
func handleAgentsList(w http.ResponseWriter, r *http.Request) {
	agents := hub.GetAllAgents()
	list := make([]map[string]any, 0, len(agents))
	for _, a := range agents {
		info := a.Info()
		info.Online = hub.IsOnline(a.ID)
		list = append(list, map[string]any{
			"id":           info.ID,
			"hostname":     info.Hostname,
			"ip":           info.IP,
			"version":      info.Version,
			"connected_at": info.ConnectedAt,
			"last_seen":    info.LastSeen,
			"online":       info.Online,
		})
	}
	jsonOK(w, list)
}

// handleAgentDetail — GET /api/agents/:id
func handleAgentDetail(w http.ResponseWriter, r *http.Request, agentID string) {
	agent, ok := hub.GetAgent(agentID)
	if !ok {
		jsonError(w, "agent not found", http.StatusNotFound)
		return
	}
	info := agent.Info()
	info.Online = hub.IsOnline(agentID)
	jsonOK(w, info)
}

// handleRegister — POST /api/agents/register
// Агент регистрируется впервые: получает ID и токен
func handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Hostname string `json:"hostname"`
		Version  string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Hostname == "" {
		jsonError(w, "invalid body: hostname required", http.StatusBadRequest)
		return
	}

	agentID := newUUID()
	token := newUUID()

	// Сохраняем в hub (без WS соединения — подключится позже)
	hub.mu.Lock()
	hub.tokMu.Lock()
	hub.agents[agentID] = &Agent{
		ID:          agentID,
		Hostname:    body.Hostname,
		Token:       token,
		Version:     body.Version,
		ConnectedAt: time.Now(),
		LastSeen:    time.Now(),
		pending:     make(map[string]chan *TunnelResponse),
	}
	hub.tokens[token] = agentID
	hub.tokMu.Unlock()
	hub.mu.Unlock()

	jsonOK(w, map[string]string{
		"agent_id": agentID,
		"token":    token,
	})
}

// handleProxy — проксирует запросы к агенту через WebSocket
func handleProxy(w http.ResponseWriter, r *http.Request, agentID, remotePath string) {
	agent, ok := hub.GetAgent(agentID)
	if !ok {
		jsonError(w, "agent not found", http.StatusNotFound)
		return
	}
	if !hub.IsOnline(agentID) {
		jsonError(w, "agent offline", http.StatusServiceUnavailable)
		return
	}

	var reqBody json.RawMessage
	if r.Body != nil {
		data, _ := io.ReadAll(r.Body)
		if len(data) > 0 {
			reqBody = data
		}
	}

	resp, err := ProxyRequest(agent, r.Method, remotePath, reqBody)
	if err != nil {
		jsonError(w, "proxy error: "+err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(resp.Status)
	w.Write(resp.Body)
}

// SetupAPIRoutes регистрирует маршруты REST API
func SetupAPIRoutes(mux *http.ServeMux) {
	// Список агентов
	mux.HandleFunc("/api/agents", corsMiddleware(pulseAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleAgentsList(w, r)
	})))

	// Регистрация нового агента
	mux.HandleFunc("/api/agents/register", corsMiddleware(func(w http.ResponseWriter, r *http.Request) {
		handleRegister(w, r)
	}))

	// Роутинг /api/agents/{id}/*
	mux.HandleFunc("/api/agents/", corsMiddleware(pulseAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		agentID, rest := extractAgentID(r.URL.Path)
		if agentID == "" {
			jsonError(w, "invalid path", http.StatusBadRequest)
			return
		}

		// GET /api/agents/:id — детали агента
		if rest == "" || rest == "/" {
			if r.Method == http.MethodGet {
				handleAgentDetail(w, r, agentID)
				return
			}
			jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Проксирующие маршруты
		switch rest {
		case "/system":
			handleProxy(w, r, agentID, "/api/system")
		case "/kkt":
			handleProxy(w, r, agentID, "/api/kkt")
		case "/command":
			handleProxy(w, r, agentID, "/api/command")
		case "/exec":
			handleProxy(w, r, agentID, "/api/exec")
		case "/health":
			handleProxy(w, r, agentID, "/api/health")
		case "/screen":
			handleProxyScreen(w, r, agentID)
		case "/mouse":
			handleProxy(w, r, agentID, "/api/mouse")
		case "/keyboard":
			handleProxy(w, r, agentID, "/api/keyboard")
		case "/vpn/setup":
			handleVPNSetup(w, r, agentID)
		case "/vpn":
			handleVPNStatus(w, r, agentID)
		default:
			jsonError(w, "unknown endpoint: "+rest, http.StatusNotFound)
		}
	})))

	// Health check самого сервера
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]any{
			"status":       "ok",
			"agents_total": len(hub.GetAllAgents()),
		})
	})
}

// handleVPNSetup — POST /api/agents/:id/vpn/setup — выдать VPN конфиг
func handleVPNSetup(w http.ResponseWriter, r *http.Request, agentID string) {
	if r.Method != http.MethodPost {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, ok := hub.GetAgent(agentID)
	if !ok {
		jsonError(w, "agent not found", http.StatusNotFound)
		return
	}

	cfg, err := wgMgr.SetupVPN(agentID)
	if err != nil {
		jsonError(w, "vpn setup error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, cfg)
}

// handleVPNStatus — GET /api/agents/:id/vpn — статус VPN
func handleVPNStatus(w http.ResponseWriter, r *http.Request, agentID string) {
	if r.Method != http.MethodGet {
		jsonError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	_, ok := hub.GetAgent(agentID)
	if !ok {
		jsonError(w, "agent not found", http.StatusNotFound)
		return
	}
	status := wgMgr.GetVPNStatus(agentID)
	jsonOK(w, status)
}

// handleProxyScreen — GET /api/agents/:id/screen — скриншот агента
func handleProxyScreen(w http.ResponseWriter, r *http.Request, agentID string) {
	agent, ok := hub.GetAgent(agentID)
	if !ok {
		jsonError(w, "agent not found", http.StatusNotFound)
		return
	}
	if !hub.IsOnline(agentID) {
		jsonError(w, "agent offline", http.StatusServiceUnavailable)
		return
	}

	resp, err := ProxyRequest(agent, http.MethodGet, "/api/screen", nil)
	if err != nil {
		jsonError(w, "proxy error: "+err.Error(), http.StatusBadGateway)
		return
	}

	if resp.Status != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.WriteHeader(resp.Status)
		w.Write(resp.Body)
		return
	}

	// Агент возвращает JSON: {"jpeg_b64": "...base64..."}
	var result struct {
		JpegB64 string `json:"jpeg_b64"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		http.Error(w, "invalid screen response", 500)
		return
	}
	if result.Error != "" {
		http.Error(w, result.Error, 500)
		return
	}

	data, err := base64.StdEncoding.DecodeString(result.JpegB64)
	if err != nil {
		http.Error(w, "base64 decode error", 500)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}
