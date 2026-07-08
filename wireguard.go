package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

const (
	wgInterface  = "wg0"
	wgSubnet     = "10.100.0.0/16"
	wgServerIP   = "10.100.0.1"
	wgPort       = "51820"
	wgEndpoint   = "31.129.96.100:51820"
	wgDNS        = "1.1.1.1"
	peersFile    = "/etc/itdoctor/peers.json"
	wgServerPrivKey = "/etc/wireguard/server_private.key"
	wgServerPubKey  = "/etc/wireguard/server_public.key"
)

// VPNConfig — конфиг WireGuard для агента
type VPNConfig struct {
	PrivateKey     string `json:"private_key"`
	AgentIP        string `json:"agent_ip"`
	ServerPubKey   string `json:"server_pub_key"`
	ServerEndpoint string `json:"server_endpoint"`
	DNS            string `json:"dns"`
}

// PeerInfo — информация о пире (сохраняется в peers.json)
type PeerInfo struct {
	AgentID    string `json:"agent_id"`
	PublicKey  string `json:"public_key"`
	AgentIP    string `json:"agent_ip"`
	AllowedIPs string `json:"allowed_ips"`
}

// WGManager управляет WireGuard пирами
type WGManager struct {
	mu           sync.Mutex
	peers        map[string]*PeerInfo // agentID → peer
	serverPubKey string
	nextIndex    int // следующий свободный индекс (10.100.0.x)
}

var wgMgr = &WGManager{
	peers: make(map[string]*PeerInfo),
}

// Init загружает ключи сервера и пиров из файлов
func (m *WGManager) Init() error {
	// Читаем публичный ключ сервера
	data, err := os.ReadFile(wgServerPubKey)
	if err != nil {
		return fmt.Errorf("read server pub key: %w", err)
	}
	m.serverPubKey = strings.TrimSpace(string(data))

	// Загружаем сохранённые пиры
	if err := m.loadPeers(); err != nil {
		log.Printf("[WG] no peers file yet: %v", err)
	}

	// Восстанавливаем пиры в wg0
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, peer := range m.peers {
		if err := m.addPeerToWG(peer.PublicKey, peer.AllowedIPs); err != nil {
			log.Printf("[WG] restore peer %s: %v", peer.AgentID, err)
		}
	}

	log.Printf("[WG] initialized, server pubkey=%s, peers=%d", m.serverPubKey[:8]+"...", len(m.peers))
	return nil
}

// SetupVPN генерирует или возвращает существующий VPN конфиг для агента
func (m *WGManager) SetupVPN(agentID string) (*VPNConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Если пир уже есть — генерируем новые ключи и обновляем
	existing := m.peers[agentID]

	// Генерируем новую пару ключей для агента
	privKey, pubKey, err := m.genKeyPair()
	if err != nil {
		return nil, fmt.Errorf("gen key pair: %w", err)
	}

	// Определяем IP
	var agentIP string
	if existing != nil {
		agentIP = existing.AgentIP
		// Удаляем старый пир если был
		if existing.PublicKey != "" {
			exec.Command("wg", "set", wgInterface, "peer", existing.PublicKey, "remove").Run()
		}
	} else {
		agentIP = m.nextFreeIP()
	}

	peer := &PeerInfo{
		AgentID:    agentID,
		PublicKey:  pubKey,
		AgentIP:    agentIP,
		AllowedIPs: agentIP + "/32",
	}

	// Добавляем пир в wg0
	if err := m.addPeerToWG(pubKey, agentIP+"/32"); err != nil {
		return nil, fmt.Errorf("add peer to wg: %w", err)
	}

	m.peers[agentID] = peer

	if err := m.savePeers(); err != nil {
		log.Printf("[WG] save peers error: %v", err)
	}

	cfg := &VPNConfig{
		PrivateKey:     privKey,
		AgentIP:        agentIP + "/16",
		ServerPubKey:   m.serverPubKey,
		ServerEndpoint: wgEndpoint,
		DNS:            wgDNS,
	}

	log.Printf("[WG] VPN setup for agent %s: ip=%s", agentID, agentIP)
	return cfg, nil
}

// GetVPNStatus возвращает статус VPN для агента
func (m *WGManager) GetVPNStatus(agentID string) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()

	peer, ok := m.peers[agentID]
	if !ok {
		return map[string]any{
			"vpn_configured": false,
		}
	}

	// Проверяем статус через wg show
	status := "unknown"
	out, err := exec.Command("wg", "show", wgInterface, "peers").Output()
	if err == nil {
		if strings.Contains(string(out), peer.PublicKey) {
			status = "configured"
		}
	}

	return map[string]any{
		"vpn_configured": true,
		"agent_ip":       peer.AgentIP,
		"status":         status,
		"public_key":     peer.PublicKey,
	}
}

// GetVPNIP возвращает VPN IP агента (или "")
func (m *WGManager) GetVPNIP(agentID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if peer, ok := m.peers[agentID]; ok {
		return peer.AgentIP
	}
	return ""
}

// genKeyPair генерирует пару ключей WireGuard через wg утилиту
func (m *WGManager) genKeyPair() (privKey, pubKey string, err error) {
	privOut, err := exec.Command("wg", "genkey").Output()
	if err != nil {
		return "", "", fmt.Errorf("wg genkey: %w", err)
	}
	privKey = strings.TrimSpace(string(privOut))

	pubCmd := exec.Command("wg", "pubkey")
	pubCmd.Stdin = strings.NewReader(privKey)
	pubOut, err := pubCmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("wg pubkey: %w", err)
	}
	pubKey = strings.TrimSpace(string(pubOut))
	return privKey, pubKey, nil
}

// addPeerToWG добавляет пир в wg0 интерфейс
func (m *WGManager) addPeerToWG(pubKey, allowedIPs string) error {
	cmd := exec.Command("wg", "set", wgInterface,
		"peer", pubKey,
		"allowed-ips", allowedIPs,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("wg set peer: %s", string(out))
	}
	return nil
}

// nextFreeIP возвращает следующий свободный IP в подсети
func (m *WGManager) nextFreeIP() string {
	// Собираем занятые IP
	used := make(map[string]bool)
	used[wgServerIP] = true
	for _, p := range m.peers {
		used[p.AgentIP] = true
	}

	// Ищем свободный начиная с 10.100.0.2
	_, network, _ := net.ParseCIDR(wgSubnet)
	ip := network.IP.Mask(network.Mask)
	// start from 10.100.0.2
	ip[3] = 2

	for {
		candidate := fmt.Sprintf("%d.%d.%d.%d", ip[0], ip[1], ip[2], ip[3])
		if !used[candidate] {
			return candidate
		}
		// increment
		for i := len(ip) - 1; i >= 0; i-- {
			ip[i]++
			if ip[i] != 0 {
				break
			}
		}
		// overflow protection: stop at 10.100.255.254
		if ip[2] == 255 && ip[3] == 255 {
			break
		}
	}
	return "10.100.0.2" // fallback
}

// savePeers сохраняет состояние пиров в JSON файл
func (m *WGManager) savePeers() error {
	dir := filepath.Dir(peersFile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m.peers, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(peersFile, data, 0600)
}

// loadPeers загружает состояние пиров из JSON файла
func (m *WGManager) loadPeers() error {
	data, err := os.ReadFile(peersFile)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, &m.peers)
}

// InitWireGuard инициализирует WireGuard менеджер
// Вызывается при старте сервера (не fatal если wg не установлен)
func InitWireGuard() {
	if _, err := exec.LookPath("wg"); err != nil {
		log.Printf("[WG] wg not found — VPN features disabled")
		return
	}
	if err := wgMgr.Init(); err != nil {
		log.Printf("[WG] init error: %v — VPN features may not work", err)
	}
}
