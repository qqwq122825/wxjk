package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Peer struct {
	conn *websocket.Conn
	mu   sync.Mutex
	once sync.Once
}

func (p *Peer) Send(v any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return p.conn.WriteJSON(v)
}
func (p *Peer) Close() { p.once.Do(func() { _ = p.conn.Close() }) }

type Hub struct {
	a     *App
	mu    sync.RWMutex
	peers map[string]*Peer
}

func NewHub(a *App) *Hub           { return &Hub{a: a, peers: map[string]*Peer{}} }
func (h *Hub) get(id string) *Peer { h.mu.RLock(); defer h.mu.RUnlock(); return h.peers[id] }
func (h *Hub) Has(id string) bool  { return h.get(id) != nil }
func (h *Hub) IDs() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ids := make([]string, 0, len(h.peers))
	for id := range h.peers {
		ids = append(ids, id)
	}
	return ids
}
func (h *Hub) add(id string, p *Peer) {
	h.mu.Lock()
	old := h.peers[id]
	h.peers[id] = p
	h.mu.Unlock()
	if old != nil {
		old.Close()
	}
}
func (h *Hub) remove(id string, p *Peer) {
	h.mu.Lock()
	if h.peers[id] == p {
		delete(h.peers, id)
	}
	h.mu.Unlock()
	p.Close()
}
func (h *Hub) Close() {
	for _, id := range h.IDs() {
		if p := h.get(id); p != nil {
			p.Close()
		}
	}
}
func (h *Hub) Kick(id, kind string) {
	if p := h.get(id); p != nil {
		_ = p.Send(map[string]any{"type": kind})
		p.Close()
	}
}
func (h *Hub) Sync(id string) {
	p := h.get(id)
	if p == nil {
		return
	}
	var d Device
	if h.a.db.First(&d, "id = ?", id).Error != nil {
		return
	}
	if !d.Enabled {
		h.Kick(id, "device.disabled")
		return
	}
	commands := h.a.pendingCommands(d, true)
	payload := make([]map[string]any, 0, len(commands))
	for _, c := range commands {
		payload = append(payload, commandEnvelope(c))
	}
	if p.Send(map[string]any{"type": "device.sync", "assignment": map[string]any{"id": nullID(d.AssignmentID), "version": d.AssignmentVersion}, "commands": payload, "server_time": time.Now().UTC()}) == nil && len(commands) > 0 {
		ids := make([]string, 0, len(commands))
		for _, c := range commands {
			ids = append(ids, c.ID)
		}
		h.a.db.Model(&Command{}).Where("id IN ?", ids).Update("last_attempt_at", time.Now().UTC())
	}
}
func (h *Hub) SyncAll() {
	for _, id := range h.IDs() {
		h.Sync(id)
	}
}
func (h *Hub) Dispatch() {
	for _, id := range h.IDs() {
		p := h.get(id)
		if p == nil {
			continue
		}
		var d Device
		if h.a.db.First(&d, "id = ?", id).Error != nil {
			continue
		}
		if !d.Enabled {
			h.Kick(id, "device.disabled")
			continue
		}
		for _, c := range h.a.pendingCommands(d, false) {
			if c.AckedAt != nil {
				continue
			}
			if c.LastAttemptAt != nil && c.LastAttemptAt.After(time.Now().Add(-10*time.Second)) {
				continue
			}
			if p.Send(commandEnvelope(c)) != nil {
				p.Close()
				break
			}
			h.a.db.Model(&Command{}).Where("id = ?", c.ID).Update("last_attempt_at", time.Now().UTC())
		}
	}
}

func (a *App) deviceSocket(w http.ResponseWriter, r *http.Request, d *Device) {
	upgrade := websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096, CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		parsed, err := url.Parse(origin)
		return err == nil && parsed.Host == r.Host
	}}
	conn, err := upgrade.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	peer := &Peer{conn: conn}
	a.hub.add(d.ID, peer)
	defer a.hub.remove(d.ID, peer)
	conn.SetReadLimit(64 << 10)
	_ = conn.SetReadDeadline(time.Now().Add(120 * time.Second))
	conn.SetPingHandler(func(payload string) error {
		_ = conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		return conn.WriteControl(websocket.PongMessage, []byte(payload), time.Now().Add(5*time.Second))
	})
	_ = peer.Send(map[string]any{"type": "device.welcome", "connection_id": newID("conn_"), "server_time": time.Now().UTC()})
	for {
		var message map[string]json.RawMessage
		if err = conn.ReadJSON(&message); err != nil {
			return
		}
		if a.hub.get(d.ID) != peer {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		var kind string
		_ = json.Unmarshal(message["type"], &kind)
		var current Device
		if a.db.First(&current, "id = ?", d.ID).Error != nil || !current.Enabled {
			a.hub.Kick(d.ID, "device.disabled")
			return
		}
		now := time.Now().UTC()
		updates := map[string]any{"last_seen_at": now}
		var appState string
		if json.Unmarshal(message["app_state"], &appState) == nil && (appState == "foreground" || appState == "background" || appState == "inactive") {
			updates["app_state"] = appState
		}
		a.db.Model(&Device{}).Where("id = ?", d.ID).Updates(updates)
		switch kind {
		case "device.hello":
			var startupID string
			if json.Unmarshal(message["startup_capture_id"], &startupID) == nil {
				if err := a.queueStartupImage(current.ID, startupID); err != nil {
					return
				}
			}
			a.applyCaptureState(current, message["capture"])
			a.hub.Sync(d.ID)
		case "device.heartbeat":
			a.applyCaptureState(current, message["capture"])
			_ = peer.Send(map[string]any{"type": "device.heartbeat_ack", "server_time": now})
		case "device.capture_state":
			a.applyCaptureState(current, message["capture"])
		case "command.ack", "command.progress":
			var id, status string
			_ = json.Unmarshal(message["command_id"], &id)
			_ = json.Unmarshal(message["status"], &status)
			if id != "" {
				_ = a.applyAck(d.ID, id, status)
			}
		case "command.result":
			var id string
			_ = json.Unmarshal(message["command_id"], &id)
			var result map[string]any
			encoded, _ := json.Marshal(message)
			if json.Unmarshal(encoded, &result) == nil && id != "" {
				if a.applyResult(d.ID, id, result) == nil {
					_ = peer.Send(map[string]any{"type": "command.result_ack", "command_id": id})
					a.hub.Sync(d.ID)
				}
			}
		}
	}
}
func (a *App) applyCaptureState(d Device, raw json.RawMessage) {
	var state struct {
		State     string `json:"state"`
		SessionID string `json:"session_id"`
		Version   uint64 `json:"session_version"`
	}
	if json.Unmarshal(raw, &state) != nil || len(state.State) > 80 {
		return
	}
	if state.State != "" {
		a.db.Model(&Device{}).Where("id = ?", d.ID).Update("capture_state", state.State)
	}
	if state.SessionID == "" {
		return
	}
	var s ImageSession
	if a.db.Where("id = ? AND device_id = ?", state.SessionID, d.ID).First(&s).Error != nil || state.Version != s.Version {
		return
	}
	if s.DesiredRunning {
		switch state.State {
		case "running", "ready", "waiting_foreground", "capture_failed", "upload_failed", "persistence_failed":
			a.db.Model(&s).Update("observed_state", state.State)
		}
	}
}
func (a *App) deviceHeartbeat(w http.ResponseWriter, r *http.Request, d *Device) {
	var in map[string]json.RawMessage
	if !readJSON(w, r, &in) {
		return
	}
	now := time.Now().UTC()
	a.db.Model(d).Update("last_seen_at", now)
	a.applyCaptureState(*d, in["capture"])
	commands := a.pendingCommands(*d, true)
	payload := make([]map[string]any, 0, len(commands))
	for _, c := range commands {
		payload = append(payload, commandEnvelope(c))
	}
	jsonOut(w, 200, map[string]any{"server_time": now, "heartbeat_interval_seconds": 30, "commands": payload, "assignment": map[string]any{"id": nullID(d.AssignmentID), "version": d.AssignmentVersion}})
}
