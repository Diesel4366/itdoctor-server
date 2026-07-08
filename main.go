package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
)

func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:]),
	)
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	log.Println("IT-Doctor Server starting...")

	// Инициализация WireGuard (не фатальна если wg не установлен)
	InitWireGuard()

	// ── REST API для Pulse (порт 8765) + Web UI ───────────────────────────────
	apiMux := http.NewServeMux()
	SetupWebRoutes(apiMux)
	SetupAPIRoutes(apiMux)

	// ── WebSocket Hub для агентов (порт 8766) ─────────────────────────────────
	wsMux := http.NewServeMux()
	wsMux.HandleFunc("/agent/ws", hub.ServeAgentWS)
	wsMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	log.Printf("Web UI    → http://0.0.0.0:%s/", PulseAPIPort)
	log.Printf("REST API  → http://0.0.0.0:%s/api/agents", PulseAPIPort)
	log.Printf("Agent WS  → ws://0.0.0.0:%s/agent/ws", AgentWSPort)

	errCh := make(chan error, 2)

	go func() {
		log.Printf("Starting REST API server on :%s", PulseAPIPort)
		if err := http.ListenAndServe(":"+PulseAPIPort, apiMux); err != nil {
			errCh <- fmt.Errorf("REST API: %w", err)
		}
	}()

	go func() {
		log.Printf("Starting Agent WS server on :%s", AgentWSPort)
		if err := http.ListenAndServe(":"+AgentWSPort, wsMux); err != nil {
			errCh <- fmt.Errorf("Agent WS: %w", err)
		}
	}()

	err := <-errCh
	log.Printf("FATAL: %v", err)
	os.Exit(1)
}
