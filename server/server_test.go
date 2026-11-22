package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type browser struct {
	t          *testing.T
	client     *http.Client
	base, csrf string
}

func (b *browser) request(method, path string, body any, status int) map[string]any {
	b.t.Helper()
	var data io.Reader
	if body != nil {
		encoded, _ := json.Marshal(body)
		data = bytes.NewReader(encoded)
	}
	request, _ := http.NewRequest(method, b.base+path, data)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if b.csrf != "" {
		request.Header.Set("X-CSRF-Token", b.csrf)
	}
	response, err := b.client.Do(request)
	if err != nil {
		b.t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode != status {
		b.t.Fatalf("%s %s: got %d, want %d: %s", method, path, response.StatusCode, status, raw)
	}
	var result map[string]any
	_ = json.Unmarshal(raw, &result)
	return result
}
func newBrowser(t *testing.T, base string) *browser {
	jar, _ := cookiejar.New(nil)
	return &browser{t: t, client: &http.Client{Jar: jar}, base: base}
}

type fixture struct {
	t      *testing.T
	a      *App
	server *httptest.Server
	admin  *browser
}

func setupFixture(t *testing.T) *fixture {
	t.Helper()
	a, err := NewApp(Config{DataDir: t.TempDir(), SetupToken: "test-setup"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a.Routes())
	f := &fixture{t, a, server, newBrowser(t, server.URL)}
	created := f.admin.request("POST", "/api/v1/auth/setup", map[string]any{"username": "owner", "password": "OwnerPassword123!", "setup_token": "test-setup"}, 200)
	f.admin.csrf = created["csrf_token"].(string)
	t.Cleanup(func() { a.hub.Close(); server.Close(); db, _ := a.db.DB(); _ = db.Close() })
	return f
}
func (f *fixture) agent(name string) *browser {
	f.t.Helper()
	f.admin.request("POST", "/api/v1/admin/agents", map[string]any{"username": name, "display_name": name, "password": "123456"}, 201)
	b := newBrowser(f.t, f.server.URL)
	login := b.request("POST", "/api/v1/auth/login", map[string]any{"username": name, "password": "123456"}, 200)
	b.csrf = login["csrf_token"].(string)
	b.request("GET", "/api/v1/devices", nil, 200)
	return b
}
func (b *browser) userID() string {
	return b.request("GET", "/api/v1/auth/me", nil, 200)["user"].(map[string]any)["id"].(string)
}
func (f *fixture) register(location bool) (Device, string, map[string]any) {
	f.t.Helper()
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	body := map[string]any{"installation_id": newID("install_"), "registration_secret": base64.StdEncoding.EncodeToString(secret), "platform": "ios", "os_version": "18.1", "client_version": "0.1.0", "capabilities": map[string]any{"capture_sources": []string{"uikit_window"}, "capture_scopes": []string{"active_window"}, "location": location}}
	reply := newBrowser(f.t, f.server.URL).request("POST", "/api/v1/device/register", body, 200)
	var d Device
	if err := f.a.db.First(&d, "id = ?", reply["device_id"]).Error; err != nil {
		f.t.Fatal(err)
	}
	return d, reply["device_token"].(string), body
}
func (f *fixture) assign(d Device, b *browser) Device {
	f.t.Helper()
	f.admin.request("POST", "/api/v1/admin/devices/"+d.ID+"/assignment", map[string]any{"agent_id": b.userID()}, 200)
	f.a.db.First(&d, "id = ?", d.ID)
	return d
}
func (b *browser) command(device, kind string) Command {
	reply := b.request("POST", "/api/v1/devices/"+device+"/commands", map[string]any{"type": kind}, 201)
	raw, _ := json.Marshal(reply["command"])
	var c Command
	_ = json.Unmarshal(raw, &c)
	return c
}
func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 60))
	for y := 0; y < 60; y++ {
		for x := 0; x < 40; x++ {
			img.Set(x, y, color.RGBA{uint8(20 + x), uint8(70 + y), 90, 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
func (f *fixture) upload(d Device, token string, meta map[string]any, picture []byte, status int) map[string]any {
	f.t.Helper()
	meta["device_id"] = d.ID
	meta["assignment_id"] = nullID(d.AssignmentID)
	meta["assignment_version"] = d.AssignmentVersion
	meta["capture_source"] = "uikit_window"
	meta["capture_scope"] = "active_window"
	meta["captured_at"] = time.Now().UTC().Format(time.RFC3339)
	meta["pixel_width"] = 40
	meta["pixel_height"] = 60
	var data bytes.Buffer
	writer := multipart.NewWriter(&data)
	raw, _ := json.Marshal(meta)
	_ = writer.WriteField("metadata", string(raw))
	part, _ := writer.CreateFormFile("image", "window.png")
	_, _ = part.Write(picture)
	_ = writer.Close()
	req, _ := http.NewRequest("POST", f.server.URL+"/api/v1/device/images", &data)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != status {
		f.t.Fatalf("upload: got %d, want %d: %s", response.StatusCode, status, body)
	}
	var value map[string]any
	_ = json.Unmarshal(body, &value)
	return value
}
func waitFor(t *testing.T, message string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(message)
}
func readWS(t *testing.T, conn *websocket.Conn, kind string) map[string]any {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for i := 0; i < 10; i++ {
		var value map[string]any
		if err := conn.ReadJSON(&value); err != nil {
			t.Fatal(err)
		}
		if value["type"] == kind {
			return value
		}
	}
	t.Fatalf("missing WSS message %s", kind)
	return nil
}

func TestSetupGateAndCSRF(t *testing.T) {
	a, err := NewApp(Config{DataDir: t.TempDir(), SetupToken: "secret-setup"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(a.Routes())
	defer server.Close()
	defer func() { db, _ := a.db.DB(); _ = db.Close() }()
	b := newBrowser(t, server.URL)
	b.request("POST", "/api/v1/auth/setup", map[string]any{"username": "owner", "password": "OwnerPassword123!", "setup_token": "wrong"}, 403)
	reply := b.request("POST", "/api/v1/auth/setup", map[string]any{"username": "owner", "password": "OwnerPassword123!", "setup_token": "secret-setup"}, 200)
	b.request("POST", "/api/v1/admin/agents", map[string]any{"username": "north", "password": "123456"}, 403)
	b.csrf = reply["csrf_token"].(string)
	b.request("POST", "/api/v1/auth/setup", map[string]any{"username": "other", "password": "OwnerPassword123!", "setup_token": "secret-setup"}, 409)
	request, _ := http.NewRequest("POST", server.URL+"/api/v1/auth/login", strings.NewReader(`{"username":"owner","password":"OwnerPassword123!"}`))
	request.Header.Set("Origin", "https://other.example")
	response, err := b.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("cross-origin login accepted")
	}
}

func TestRegistrationAndRoleIsolation(t *testing.T) {
	f := setupFixture(t)
	north := f.agent("north")
	south := f.agent("south")
	d, token, registration := f.register(false)
	reply := newBrowser(t, f.server.URL).request("POST", "/api/v1/device/register", registration, 200)
	if reply["device_id"] != d.ID || reply["device_token"] != token {
		t.Fatal("retry changed installation identity")
	}
	forged := map[string]any{}
	for k, v := range registration {
		forged[k] = v
	}
	forged["registration_secret"] = base64.StdEncoding.EncodeToString(make([]byte, 32))
	newBrowser(t, f.server.URL).request("POST", "/api/v1/device/register", forged, 401)
	pending := f.admin.request("GET", "/api/v1/devices?state=unassigned", nil, 200)
	if pending["total"].(float64) != 1 {
		t.Fatal("new device did not enter pending pool")
	}
	if north.request("GET", "/api/v1/devices", nil, 200)["total"].(float64) != 0 {
		t.Fatal("agent can see unassigned device")
	}
	d = f.assign(d, north)
	north.request("GET", "/api/v1/devices/"+d.ID, nil, 200)
	south.request("GET", "/api/v1/devices/"+d.ID, nil, 404)
	south.request("POST", "/api/v1/devices/"+d.ID+"/commands", map[string]any{"type": "GET_IMAGE"}, 404)
	north.request("GET", "/api/v1/admin/agents", nil, 403)
	north.request("POST", "/api/v1/devices/"+d.ID+"/commands", map[string]any{"type": "GET_LOCATION"}, 422)
	encoded, _ := json.Marshal(north.request("GET", "/api/v1/devices/"+d.ID, nil, 200))
	if strings.Contains(string(encoded), "token_hash") || strings.Contains(string(encoded), "registration_hash") {
		t.Fatal("device secrets leaked into browser response")
	}
}

func TestPNGUploadHistoryAndTransferRevocation(t *testing.T) {
	f := setupFixture(t)
	north := f.agent("north")
	south := f.agent("south")
	d, token, _ := f.register(false)
	d = f.assign(d, north)
	command := north.command(d.ID, "GET_IMAGE")
	meta := map[string]any{"report_id": "one-image", "command_id": command.ID}
	saved := f.upload(d, token, meta, testPNG(t), 201)
	id := saved["id"].(string)
	if again := f.upload(d, token, meta, testPNG(t), 201); again["id"] != id {
		t.Fatal("upload retry created duplicate report")
	}
	request, _ := http.NewRequest("GET", f.server.URL+"/api/v1/images/"+id+"/file", nil)
	response, err := north.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "image/jpeg" || !bytes.HasPrefix(body, []byte{0xff, 0xd8, 0xff}) {
		t.Fatal("owner cannot retrieve converted JPEG")
	}
	south.request("GET", "/api/v1/images/"+id+"/file", nil, 404)
	old := d
	d = f.assign(d, south)
	north.request("GET", "/api/v1/images/"+id+"/file", nil, 404)
	south.request("GET", "/api/v1/images/"+id+"/file", nil, 404)
	f.admin.request("GET", "/api/v1/devices/"+d.ID+"/images", nil, 200)
	f.upload(old, token, map[string]any{"report_id": "late-old", "command_id": command.ID}, testPNG(t), 409)
	detail := south.request("GET", "/api/v1/devices/"+d.ID, nil, 200)
	if detail["latest_image"] != nil {
		t.Fatal("new assignment inherited old screenshot")
	}
}

func TestAutonomousTaskOrderingAndStop(t *testing.T) {
	f := setupFixture(t)
	agent := f.agent("north")
	d, token, _ := f.register(false)
	d = f.assign(d, agent)
	start := agent.command(d.ID, "START_IMAGE_SESSION")
	duplicate := agent.command(d.ID, "START_IMAGE_SESSION")
	if duplicate.ID != start.ID {
		t.Fatal("duplicate start created a second task")
	}
	var task ImageSession
	f.a.db.First(&task, "id = ?", start.SessionID)
	if err := f.a.applyResult(d.ID, start.ID, map[string]any{"status": "accepted"}); err != nil {
		t.Fatal(err)
	}
	seq3 := f.upload(d, token, map[string]any{"report_id": "frame-3", "session_id": task.ID, "session_version": 1, "frame_seq": 3}, testPNG(t), 201)
	f.upload(d, token, map[string]any{"report_id": "frame-1", "session_id": task.ID, "session_version": 1, "frame_seq": 1}, testPNG(t), 201)
	var fresh Device
	f.a.db.First(&fresh, "id = ?", d.ID)
	if fresh.LatestImageID != seq3["id"] {
		t.Fatal("late frame replaced the latest frame")
	}
	agent.request("POST", "/api/v1/auth/logout", nil, 200)
	f.a.db.First(&task, "id = ?", task.ID)
	if !task.DesiredRunning {
		t.Fatal("browser logout stopped the autonomous phone task")
	}
	stop := f.admin.command(d.ID, "STOP_IMAGE_SESSION")
	f.upload(d, token, map[string]any{"report_id": "frame-4", "session_id": task.ID, "session_version": 1, "frame_seq": 4}, testPNG(t), 410)
	if err := f.a.applyResult(d.ID, stop.ID, map[string]any{"status": "stopped"}); err != nil {
		t.Fatal(err)
	}
	f.a.db.First(&fresh, "id = ?", d.ID)
	if fresh.ActiveSessionID != "" {
		t.Fatal("stop confirmation did not release active task")
	}
	for _, c := range f.a.pendingCommands(fresh, true) {
		if c.ID == start.ID {
			t.Fatal("stopped task's start command was replayed")
		}
	}
}

func TestWSSReconnectKeepsQueuedCommands(t *testing.T) {
	f := setupFixture(t)
	agent := f.agent("north")
	d, token, _ := f.register(false)
	d = f.assign(d, agent)
	headers := http.Header{"Authorization": []string{"Bearer " + token}}
	url := "ws" + strings.TrimPrefix(f.server.URL, "http") + "/api/v1/device/ws"
	first, _, err := websocket.DefaultDialer.Dial(url, headers)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	readWS(t, first, "device.welcome")
	_ = first.WriteJSON(map[string]any{"type": "device.hello", "app_state": "foreground"})
	readWS(t, first, "device.sync")
	c := agent.command(d.ID, "GET_IMAGE")
	sync := readWS(t, first, "device.sync")
	if len(sync["commands"].([]any)) != 1 {
		t.Fatal("queued command not dispatched")
	}
	_ = first.WriteJSON(map[string]any{"type": "command.ack", "command_id": c.ID, "status": "received"})
	waitFor(t, "ack was not persisted", func() bool { var saved Command; f.a.db.First(&saved, "id = ?", c.ID); return saved.AckedAt != nil })
	second, _, err := websocket.DefaultDialer.Dial(url, headers)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	readWS(t, second, "device.welcome")
	_ = second.WriteJSON(map[string]any{"type": "device.hello", "app_state": "foreground"})
	again := readWS(t, second, "device.sync")
	commands := again["commands"].([]any)
	if len(commands) != 1 || commands[0].(map[string]any)["command_id"] != c.ID {
		t.Fatal("reconnect lost or duplicated pending command")
	}
	_ = second.WriteJSON(map[string]any{"type": "device.heartbeat"})
	readWS(t, second, "device.heartbeat_ack")
	if !f.a.hub.Has(d.ID) {
		t.Fatal("old connection cleanup removed the replacement socket")
	}
}

func TestImageAndLocationValidation(t *testing.T) {
	f := setupFixture(t)
	d, token, _ := f.register(true)
	command := f.admin.command(d.ID, "GET_IMAGE")
	f.upload(d, token, map[string]any{"report_id": "broken-png", "command_id": command.ID}, []byte("not a PNG"), 422)
	c := f.admin.command(d.ID, "GET_LOCATION")
	send := func(latitude float64, status int) {
		payload := map[string]any{"report_id": newID("position_"), "command_id": c.ID, "assignment_id": nil, "assignment_version": 0, "latitude": latitude, "longitude": 121.47, "coordinate_system": "WGS84", "horizontal_accuracy_m": 20, "collected_at": time.Now().UTC().Format(time.RFC3339)}
		raw, _ := json.Marshal(payload)
		request, _ := http.NewRequest("POST", f.server.URL+"/api/v1/device/locations", bytes.NewReader(raw))
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != status {
			t.Fatalf("location status %d, want %d", response.StatusCode, status)
		}
	}
	send(999, 422)
	send(31.23, 201)
	reply := f.admin.request("GET", "/api/v1/devices/"+d.ID+"/locations", nil, 200)
	if len(reply["items"].([]any)) != 1 {
		t.Fatal("valid location was not saved")
	}
}

func TestAgentDisableAndPasswordResetRevokeSessions(t *testing.T) {
	f := setupFixture(t)
	agent := f.agent("north")
	id := agent.userID()
	f.admin.request("POST", "/api/v1/admin/agents/"+id+"/password", map[string]any{"password": "654321"}, 200)
	agent.request("GET", "/api/v1/auth/me", nil, 401)
	login := agent.request("POST", "/api/v1/auth/login", map[string]any{"username": "north", "password": "654321"}, 200)
	agent.csrf = login["csrf_token"].(string)
	agent.request("POST", "/api/v1/auth/password", map[string]any{"current_password": "654321", "new_password": "Ab12cD"}, 200)
	f.admin.request("PATCH", "/api/v1/admin/agents/"+id, map[string]any{"enabled": false}, 200)
	agent.request("GET", "/api/v1/auth/me", nil, 401)
	agent.request("POST", "/api/v1/auth/login", map[string]any{"username": "north", "password": "Ab12cD"}, 401)
}

func TestRestartPreservesIdentityCommandsAndTask(t *testing.T) {
	f := setupFixture(t)
	d, token, registration := f.register(false)
	command := f.admin.command(d.ID, "START_IMAGE_SESSION")
	f.a.hub.Close()
	f.server.Close()
	db, _ := f.a.db.DB()
	_ = db.Close()
	again, err := NewApp(f.a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(again.Routes())
	defer server.Close()
	defer func() { db, _ := again.db.DB(); _ = db.Close() }()
	reply := newBrowser(t, server.URL).request("POST", "/api/v1/device/register", registration, 200)
	if reply["device_token"] != token {
		t.Fatal("restart changed device credential")
	}
	var saved Command
	if err = again.db.First(&saved, "id = ?", command.ID).Error; err != nil {
		t.Fatal("restart lost command")
	}
	var task ImageSession
	if again.db.First(&task, "id = ?", command.SessionID).Error != nil || !task.DesiredRunning {
		t.Fatal("restart lost autonomous task")
	}
}

func TestPendingDeviceQuotaCleansTemporaryPNG(t *testing.T) {
	f := setupFixture(t)
	d, token, _ := f.register(false)
	c := f.admin.command(d.ID, "GET_IMAGE")
	if err := f.a.db.Create(&ImageReport{ID: newID("img_"), DeviceID: d.ID, ReportID: "quota", Bytes: 64 << 20, ReceivedAt: time.Now().UTC(), CapturedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	f.upload(d, token, map[string]any{"report_id": "quota-exceeded", "command_id": c.ID}, testPNG(t), 429)
	files, err := os.ReadDir(filepath.Join(f.a.cfg.DataDir, "images"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatal("rejected upload left an orphan file")
	}
}

func TestExpiredStopGetsNewCommandIdentity(t *testing.T) {
	f := setupFixture(t)
	d, _, _ := f.register(false)
	start := f.admin.command(d.ID, "START_IMAGE_SESSION")
	stop := f.admin.command(d.ID, "STOP_IMAGE_SESSION")
	f.a.db.Model(&Command{}).Where("id = ?", stop.ID).Update("expires_at", time.Now().UTC().Add(-time.Hour))
	f.a.expireWork()
	var task ImageSession
	f.a.db.First(&task, "id = ?", start.SessionID)
	if task.StopCommandID == stop.ID || task.Version <= 2 {
		t.Fatal("expired stop was replayed with an immutable old command identity")
	}
	var old Command
	f.a.db.First(&old, "id = ?", stop.ID)
	if old.Status != "expired" || old.CompletedAt == nil {
		t.Fatal("expired stop was not finalized")
	}
}
