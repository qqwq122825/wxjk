package main

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func imageResponse(t *testing.T, b *browser, id string) (int, string, []byte) {
	t.Helper()
	r, err := b.client.Get(b.base + "/api/v1/images/" + id + "/file")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return r.StatusCode, r.Header.Get("Content-Type"), data
}

func TestJPEGStorageAndFormatMetadata(t *testing.T) {
	f := setupFixture(t)
	d, token, _ := f.register(false)
	c := f.admin.command(d.ID, "GET_IMAGE")
	meta := map[string]any{"report_id": "png-to-jpg", "command_id": c.ID}
	id := f.upload(d, token, meta, testPNG(t), 201)["id"].(string)
	status, mime, data := imageResponse(t, f.admin, id)
	if status != 200 || mime != "image/jpeg" || !bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}) {
		t.Fatalf("PNG upload must return JPEG: status=%d mime=%q magic=%x", status, mime, data[:min(8, len(data))])
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width != 40 || config.Height != 60 {
		t.Fatalf("JPEG dimensions: %+v %v", config, err)
	}
	var report ImageReport
	f.a.db.First(&report, "id = ?", id)
	if !strings.HasSuffix(report.FileName, ".jpg") || report.Bytes != int64(len(data)) {
		t.Fatalf("stored report metadata: %+v", report)
	}
	list := f.admin.request("GET", "/api/v1/devices/"+d.ID+"/images", nil, 200)["items"].([]any)
	row := list[0].(map[string]any)
	detail := f.admin.request("GET", "/api/v1/devices/"+d.ID, nil, 200)["latest_image"].(map[string]any)
	for _, value := range []map[string]any{row, detail} {
		if value["format"] != "jpg" || value["content_type"] != "image/jpeg" || value["file_name"] != nil {
			t.Fatalf("format metadata or file-name exposure: %#v", value)
		}
	}
	if retry := f.upload(d, token, meta, testPNG(t), 201); retry["id"] != id {
		t.Fatal("JPEG conversion broke report idempotency")
	}
	files, _ := os.ReadDir(filepath.Join(f.a.cfg.DataDir, "images"))
	if len(files) != 1 {
		t.Fatalf("retry leaked %d stored files", len(files))
	}
	t.Logf("PNG upload -> JPEG %d bytes, 40x60, MIME image/jpeg, format=jpg, idempotent", len(data))
}

func TestJPEGInputAndTransparentPNG(t *testing.T) {
	for _, kind := range []string{"jpeg", "transparent-png"} {
		t.Run(kind, func(t *testing.T) {
			f := setupFixture(t)
			d, token, _ := f.register(false)
			c := f.admin.command(d.ID, "GET_IMAGE")
			img := image.NewNRGBA(image.Rect(0, 0, 40, 60))
			var input bytes.Buffer
			if kind == "jpeg" {
				for y := 0; y < 60; y++ {
					for x := 0; x < 40; x++ {
						img.Set(x, y, color.NRGBA{R: 30, G: 80, B: 120, A: 255})
					}
				}
				if err := jpeg.Encode(&input, img, &jpeg.Options{Quality: 90}); err != nil {
					t.Fatal(err)
				}
			} else if err := png.Encode(&input, img); err != nil {
				t.Fatal(err)
			}
			id := f.upload(d, token, map[string]any{"report_id": kind, "command_id": c.ID}, input.Bytes(), 201)["id"].(string)
			status, mime, data := imageResponse(t, f.admin, id)
			decoded, err := jpeg.Decode(bytes.NewReader(data))
			if status != 200 || mime != "image/jpeg" || err != nil {
				t.Fatalf("format %s: %d %q %v", kind, status, mime, err)
			}
			if kind == "transparent-png" {
				r, g, b, _ := decoded.At(0, 0).RGBA()
				if r < 65000 || g < 65000 || b < 65000 {
					t.Fatalf("transparent PNG not flattened white: %d %d %d", r, g, b)
				}
			}
		})
	}
}

func TestLegacyPNGAndImagePathSafety(t *testing.T) {
	f := setupFixture(t)
	d, _, _ := f.register(false)
	name := newID("") + ".png"
	data := testPNG(t)
	report := ImageReport{ID: newID("img_"), DeviceID: d.ID, ReportID: "legacy-png", FileName: name, KeepHistory: true, Width: 40, Height: 60, CapturedAt: time.Now().UTC(), ReceivedAt: time.Now().UTC()}
	if err := os.WriteFile(f.a.imagePath(name), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.Create(&report).Error; err != nil {
		t.Fatal(err)
	}
	status, mime, got := imageResponse(t, f.admin, report.ID)
	if status != 200 || mime != "image/png" || !bytes.Equal(got, data) {
		t.Fatalf("legacy PNG changed: %d %q", status, mime)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	if value["format"] != "png" || value["content_type"] != "image/png" {
		t.Fatalf("legacy format: %s", raw)
	}
	for _, name := range []string{"../outside.png", newID("") + ".svg", newID("") + ".jpg/../../x"} {
		if f.a.imagePath(name) != "" {
			t.Fatalf("unsafe image path accepted: %s", name)
		}
	}
	t.Log("legacy PNG bytes/MIME preserved; invalid image paths rejected")
}

func TestTransientImageGraceAndHistoryFilter(t *testing.T) {
	f := setupFixture(t)
	d, token, _ := f.register(false)
	start := f.admin.command(d.ID, "START_IMAGE_SESSION")
	var session ImageSession
	f.a.db.First(&session, "id = ?", start.SessionID)
	upload := func(seq uint64, label string) string {
		return f.upload(d, token, map[string]any{"report_id": label, "session_id": session.ID, "session_version": session.Version, "frame_seq": seq}, testPNG(t), 201)["id"].(string)
	}
	history := upload(1, "history")
	previous := upload(2, "previous")
	latest := upload(3, "latest")
	var previousRow ImageReport
	f.a.db.First(&previousRow, "id = ?", previous)
	previousPath := f.a.imagePath(previousRow.FileName)
	status, _, _ := imageResponse(t, f.admin, previous)
	if status != 200 {
		t.Fatalf("previous frame must remain readable during decode grace, got %d", status)
	}
	items := f.admin.request("GET", "/api/v1/devices/"+d.ID+"/images", nil, 200)["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("transient frames leaked into history: got %d items", len(items))
	}
	for _, item := range items {
		if id := item.(map[string]any)["id"]; id != history && id != latest {
			t.Fatalf("unexpected history ID: %s", id)
		}
	}
	old := time.Now().UTC().Add(-31 * time.Second)
	f.a.db.Model(&ImageReport{}).Where("id IN ?", []string{previous, latest}).Update("received_at", old)
	late := upload(2, "out-of-order")
	if status, _, _ := imageResponse(t, f.admin, previous); status != 404 {
		t.Fatalf("expired transient frame still available: %d", status)
	}
	if _, err := os.Stat(previousPath); !os.IsNotExist(err) {
		t.Fatalf("expired transient file was not removed after commit: %v", err)
	}
	if status, _, _ := imageResponse(t, f.admin, latest); status != 200 {
		t.Fatalf("cleanup removed current latest: %d", status)
	}
	if status, _, _ := imageResponse(t, f.admin, history); status != 200 {
		t.Fatalf("cleanup removed history: %d", status)
	}
	if status, _, _ := imageResponse(t, f.admin, late); status != 404 {
		t.Fatalf("late frame unexpectedly retained: %d", status)
	}
	var fresh Device
	f.a.db.First(&fresh, "id = ?", d.ID)
	if fresh.LatestImageID != latest {
		t.Fatal("out-of-order frame replaced latest")
	}
	newest := upload(4, "newest")
	if status, _, _ := imageResponse(t, f.admin, latest); status != 404 {
		t.Fatalf("expired nonlatest not cleaned after next frame: %d", status)
	}
	if status, _, _ := imageResponse(t, f.admin, newest); status != http.StatusOK {
		t.Fatal("new latest unavailable")
	}
	t.Log("previous frame readable for 30s; history list excludes transient; expired nonlatest cleared; latest and history protected; late sequence cannot replace latest")
}
