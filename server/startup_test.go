package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestStartupImageAuthenticatedAndIdempotent(t *testing.T) {
	f := setupFixture(t)
	d, token, _ := f.register(false)
	url := "ws" + strings.TrimPrefix(f.server.URL, "http") + "/api/v1/device/ws"
	if c, response, err := websocket.DefaultDialer.Dial(url, nil); err == nil {
		c.Close()
		t.Fatal("unauthenticated WSS accepted")
	} else if response.StatusCode != 401 {
		t.Fatalf("got %d", response.StatusCode)
	}
	c, _, err := websocket.DefaultDialer.Dial(url, http.Header{"Authorization": []string{"Bearer " + token}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	readWS(t, c, "device.welcome")
	hello := map[string]any{"type": "device.hello", "app_state": "foreground", "startup_capture_id": "12345678-1234-1234-1234-1234567890ab"}
	_ = c.WriteJSON(hello)
	sync := readWS(t, c, "device.sync")
	commands := sync["commands"].([]any)
	if len(commands) != 1 || commands[0].(map[string]any)["command"] != "GET_IMAGE" {
		t.Fatalf("no startup image command: %#v", commands)
	}
	id := commands[0].(map[string]any)["command_id"].(string)
	f.upload(d, token, map[string]any{"report_id": "startup-image", "command_id": id}, testPNG(t), 201)
	_ = c.WriteJSON(hello)
	readWS(t, c, "device.sync")
	var count int64
	f.a.db.Model(&Command{}).Where("device_id = ?", d.ID).Count(&count)
	if count != 1 {
		t.Fatalf("duplicate hello created %d commands", count)
	}
	if err := f.a.queueStartupImage(d.ID, "invalid"); err != nil {
		t.Fatal(err)
	}
	f.a.db.Model(&Command{}).Where("device_id = ?", d.ID).Count(&count)
	if count != 1 {
		t.Fatal("invalid startup ID created a command")
	}
	hello["startup_capture_id"] = "12345678-1234-1234-1234-1234567890ac"
	_ = c.WriteJSON(hello)
	readWS(t, c, "device.sync")
	f.a.db.Model(&Command{}).Where("device_id = ?", d.ID).Count(&count)
	if count != 2 {
		t.Fatal("a fresh app launch must queue one fresh image")
	}
	f.a.db.Model(&Device{}).Where("id = ?", d.ID).Update("enabled", false)
	if err := f.a.queueStartupImage(d.ID, "12345678-1234-1234-1234-1234567890ad"); err != nil {
		t.Fatal(err)
	}
	f.a.db.Model(&Command{}).Where("device_id = ?", d.ID).Count(&count)
	if count != 2 {
		t.Fatal("disabled device queued image")
	}
}
