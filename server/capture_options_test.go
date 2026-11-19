package main

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestCaptureOptionsAndRate(t *testing.T) {
	f := setupFixture(t)
	d, token, _ := f.register(false)
	created := f.admin.request("POST", "/api/v1/devices/"+d.ID+"/commands", map[string]any{"type": "START_IMAGE_SESSION", "interval_ms": 500, "resolution_percent": 50}, 201)
	c := created["command"].(map[string]any)
	id := c["id"].(string)
	var saved Command
	f.a.db.First(&saved, "id = ?", id)
	var params map[string]any
	json.Unmarshal([]byte(saved.Parameters), &params)
	if params["interval_ms"] != float64(500) || params["max_long_edge_px"] != float64(640) || params["resolution_percent"] != float64(50) {
		t.Fatal(params)
	}
	detail := f.admin.request("GET", "/api/v1/devices/"+d.ID, nil, 200)
	if detail["capture_options"].(map[string]any)["interval_ms"] != float64(500) {
		t.Fatal(detail)
	}
	png := testPNG(t)
	for i := 1; i <= 20; i++ {
		f.upload(d, token, map[string]any{"report_id": fmt.Sprintf("fast-%d", i), "session_id": saved.SessionID, "session_version": 1, "frame_seq": i}, png, 201)
	}
	for _, invalid := range []map[string]any{{"type": "GET_IMAGE", "interval_ms": 499}, {"type": "GET_IMAGE", "interval_ms": 60001}, {"type": "GET_IMAGE", "resolution_percent": 24}, {"type": "GET_IMAGE", "resolution_percent": 101}} {
		f.admin.request("POST", "/api/v1/devices/"+d.ID+"/commands", invalid, 400)
	}
	c2 := f.admin.request("POST", "/api/v1/devices/"+d.ID+"/commands", map[string]any{"type": "GET_IMAGE", "resolution_percent": 70}, 201)
	var single Command
	f.a.db.First(&single, "id = ?", c2["command"].(map[string]any)["id"])
	json.Unmarshal([]byte(single.Parameters), &params)
	if params["max_long_edge_px"] != float64(896) {
		t.Fatal(params)
	}
}
