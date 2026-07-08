package main

const (
	AgentWSPort  = "8766"                     // WebSocket для агентов
	PulseAPIPort = "8765"                     // REST API для Pulse
	PulseKey     = "itdoctor-pulse-key-2025"  // Auth для Pulse
	PingInterval = 30                         // секунды между пингами агентов
	PongTimeout  = 10                         // секунды ожидания понга
)
