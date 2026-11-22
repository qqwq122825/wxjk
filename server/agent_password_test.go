package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Run this unchanged against both the saved baseline and the modified source.
// Strict modified-policy assertions live in the additional tests below.
func TestAgentPasswordSixCharacterProbe(t *testing.T) {
	f := setupFixture(t)
	input, _ := json.Marshal(map[string]any{"username": "sixprobe", "display_name": "sixprobe", "password": "123456"})
	request, _ := http.NewRequest("POST", f.server.URL+"/api/v1/admin/agents", bytes.NewReader(input))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", f.admin.csrf)
	response, err := f.admin.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	t.Logf("SIX_CHARACTER_PROBE INPUT=username:sixprobe,password:123456 CREATE_HTTP=%d RESPONSE=%s", response.StatusCode, body)
	if response.StatusCode == 400 {
		t.Log("SIX_CHARACTER_PROBE LOGIN=NOT_CREATED DEVICES=NOT_CREATED")
		return
	}
	if response.StatusCode != 201 {
		t.Fatalf("unexpected creation status %d", response.StatusCode)
	}
	agent := newBrowser(t, f.server.URL)
	login := agent.request("POST", "/api/v1/auth/login", map[string]any{"username": "sixprobe", "password": "123456"}, 200)
	agent.csrf = login["csrf_token"].(string)
	agent.request("GET", "/api/v1/devices", nil, 200)
	if login["user"].(map[string]any)["must_change_password"] != false {
		t.Fatal("agent still forced to change password")
	}
	t.Log("SIX_CHARACTER_PROBE LOGIN_HTTP=200 DEVICES_HTTP=200 MUST_CHANGE_PASSWORD=false")
}

func loginAgentPassword(t *testing.T, f *fixture, username, password string) *browser {
	t.Helper()
	b := newBrowser(t, f.server.URL)
	login := b.request("POST", "/api/v1/auth/login", map[string]any{"username": username, "password": password}, 200)
	b.csrf = login["csrf_token"].(string)
	user := login["user"].(map[string]any)
	if user["role"] != "agent" || user["must_change_password"] != false {
		t.Fatalf("unexpected agent login flags: %v", user)
	}
	me := b.request("GET", "/api/v1/auth/me", nil, 200)
	if me["user"].(map[string]any)["must_change_password"] != false {
		t.Fatal("me still forces an agent password change")
	}
	b.request("GET", "/api/v1/devices", nil, 200)
	return b
}

func TestAgentPasswordSixCharacterCreateResetAndChange(t *testing.T) {
	f := setupFixture(t)
	created := f.admin.request("POST", "/api/v1/admin/agents", map[string]any{"username": "simpleagent", "password": "123456"}, 201)
	if created["role"] != "agent" || created["must_change_password"] != false {
		t.Fatalf("new agent flags: %v", created)
	}
	id := created["id"].(string)
	var stored User
	if err := f.a.db.First(&stored, "id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored.PasswordHash, "$argon2id$") || stored.PasswordHash == "123456" || stored.MustChangePassword {
		t.Fatal("agent password storage or force-change flag regressed")
	}
	agent := loginAgentPassword(t, f, "simpleagent", "123456")
	agent.request("GET", "/api/v1/admin/agents", nil, 403)
	for _, invalid := range []string{"12345", "1234567", "ab12!3", "１２３４５６"} {
		f.admin.request("POST", "/api/v1/admin/agents", map[string]any{"username": "invalidagent", "password": invalid}, 400)
		f.admin.request("POST", "/api/v1/admin/agents/"+id+"/password", map[string]any{"password": invalid}, 400)
		agent.request("POST", "/api/v1/auth/password", map[string]any{"current_password": "123456", "new_password": invalid}, 400)
		agent.request("GET", "/api/v1/devices", nil, 200)
	}
	f.admin.request("POST", "/api/v1/admin/agents/"+id+"/password", map[string]any{"password": "654321"}, 200)
	agent.request("GET", "/api/v1/devices", nil, 401)
	newBrowser(t, f.server.URL).request("POST", "/api/v1/auth/login", map[string]any{"username": "simpleagent", "password": "123456"}, 401)
	agent = loginAgentPassword(t, f, "simpleagent", "654321")
	otherSession := loginAgentPassword(t, f, "simpleagent", "654321")
	agent.request("POST", "/api/v1/auth/password", map[string]any{"current_password": "654321", "new_password": "Ab12cD"}, 200)
	otherSession.request("GET", "/api/v1/devices", nil, 401)
	agent.request("GET", "/api/v1/devices", nil, 200)
	loginAgentPassword(t, f, "simpleagent", "Ab12cD")
	newBrowser(t, f.server.URL).request("POST", "/api/v1/auth/login", map[string]any{"username": "simpleagent", "password": "654321"}, 401)
}

func TestAgentLegacyPasswordsAndFlagsRemainCompatible(t *testing.T) {
	f := setupFixture(t)
	password := "LegacyAgentPassword123!"
	legacy := User{ID: newID("usr_"), Username: "legacyagent", DisplayName: "legacy", Role: "agent", Enabled: true, MustChangePassword: true, PasswordHash: passwordHash(password)}
	if err := f.a.db.Create(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	agent := loginAgentPassword(t, f, legacy.Username, password)
	if err := f.a.db.Model(&User{}).Where("id = ?", legacy.ID).Update("must_change_password", true).Error; err != nil {
		t.Fatal(err)
	}
	me := agent.request("GET", "/api/v1/auth/me", nil, 200)
	if me["user"].(map[string]any)["must_change_password"] != false {
		t.Fatal("legacy session still reports forced change")
	}
	agent.request("GET", "/api/v1/devices", nil, 200)
	agent.request("GET", "/api/v1/admin/agents", nil, 403)
	var after User
	if err := f.a.db.First(&after, "id = ?", legacy.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.PasswordHash != legacy.PasswordHash || !validPassword(after.PasswordHash, password) {
		t.Fatal("legacy long password was rewritten or truncated")
	}
	// A legacy password may be retained until the agent voluntarily changes it.
	agent.request("POST", "/api/v1/auth/password", map[string]any{"current_password": password, "new_password": "abcdef"}, 200)
	loginAgentPassword(t, f, legacy.Username, "abcdef")
}

func TestAgentPasswordStartupMigrationIsRoleScoped(t *testing.T) {
	config := Config{DataDir: t.TempDir()}
	first, err := NewApp(config)
	if err != nil {
		t.Fatal(err)
	}
	agent := User{ID: newID("usr_"), Username: "migrateagent", Role: "agent", Enabled: true, MustChangePassword: true, PasswordHash: passwordHash("LegacyAgent123!")}
	admin := User{ID: newID("usr_"), Username: "migrateadmin", Role: "admin", Enabled: true, MustChangePassword: true, PasswordHash: passwordHash("LegacyAdmin123!")}
	if err = first.db.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	if err = first.db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	session := LoginSession{TokenHash: hashSecret("legacy-session-token"), UserID: agent.ID, CSRF: "legacy-csrf", ExpiresAt: time.Now().UTC().Add(time.Hour)}
	if err = first.db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	first.hub.Close()
	firstDB, _ := first.db.DB()
	if err = firstDB.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewApp(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.hub.Close(); database, _ := reopened.db.DB(); _ = database.Close() })
	var afterAgent, afterAdmin User
	if err = reopened.db.First(&afterAgent, "id = ?", agent.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err = reopened.db.First(&afterAdmin, "id = ?", admin.ID).Error; err != nil {
		t.Fatal(err)
	}
	if afterAgent.MustChangePassword || !afterAdmin.MustChangePassword || afterAgent.PasswordHash != agent.PasswordHash || afterAdmin.PasswordHash != admin.PasswordHash {
		t.Fatal("startup compatibility migration changed passwords or administrator policy")
	}
	var sessions int64
	reopened.db.Model(&LoginSession{}).Where("token_hash = ?", session.TokenHash).Count(&sessions)
	if sessions != 1 {
		t.Fatal("startup migration revoked a legacy agent session")
	}
}

func TestAdminPasswordRulesAndForcedChangeRemain(t *testing.T) {
	f := setupFixture(t)
	for _, password := range []string{"123456", "123456789", strings.Repeat("a", 129)} {
		f.admin.request("POST", "/api/v1/auth/password", map[string]any{"current_password": "OwnerPassword123!", "new_password": password}, 400)
	}
	f.admin.request("POST", "/api/v1/auth/setup", map[string]any{"username": "otherowner", "password": "123456", "setup_token": "test-setup"}, 400)
	id := f.admin.userID()
	// The agent reset endpoint cannot be used to replace an administrator password.
	f.admin.request("POST", "/api/v1/admin/agents/"+id+"/password", map[string]any{"password": "123456"}, 404)
	if err := f.a.db.Model(&User{}).Where("id = ?", id).Update("must_change_password", true).Error; err != nil {
		t.Fatal(err)
	}
	me := f.admin.request("GET", "/api/v1/auth/me", nil, 200)
	if me["user"].(map[string]any)["must_change_password"] != true {
		t.Fatal("administrator force-change flag bypassed")
	}
	f.admin.request("GET", "/api/v1/devices", nil, 403)
	f.admin.request("GET", "/api/v1/admin/agents", nil, 403)
	f.admin.request("POST", "/api/v1/auth/password", map[string]any{"current_password": "OwnerPassword123!", "new_password": "OwnerNew12"}, 200)
	f.admin.request("GET", "/api/v1/devices", nil, 200)
	login := newBrowser(t, f.server.URL)
	login.request("POST", "/api/v1/auth/login", map[string]any{"username": "owner", "password": "OwnerNew12"}, 200)
	for _, sample := range []struct {
		value string
		valid bool
	}{{strings.Repeat("a", 9), false}, {strings.Repeat("a", 10), true}, {strings.Repeat("a", 128), true}, {strings.Repeat("a", 129), false}} {
		if passwordOK(sample.value) != sample.valid {
			t.Fatalf("administrator length boundary changed: %d", len(sample.value))
		}
	}
}
