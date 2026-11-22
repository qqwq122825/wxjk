package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func seedHistoryImage(t *testing.T, f *fixture, d Device, id, assignment string, keep bool, at time.Time) ImageReport {
	t.Helper()
	r := ImageReport{ID: id, DeviceID: d.ID, ReportID: id, AssignmentID: assignment, FileName: newID("") + ".png", KeepHistory: keep, Width: 40, Height: 60, Bytes: int64(len(testPNG(t))), CapturedAt: at, ReceivedAt: at}
	if err := os.WriteFile(f.a.imagePath(r.FileName), testPNG(t), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	return r
}

func TestImageHistoryPagination(t *testing.T) {
	f := setupFixture(t)
	d, _, _ := f.register(false)
	at := time.Date(2026, 11, 22, 1, 0, 0, 0, time.UTC)
	want := make([]string, 0, 86)
	for i := 0; i < 85; i++ {
		id := fmt.Sprintf("history-%03d", i)
		seedHistoryImage(t, f, d, id, d.AssignmentID, true, at)
		want = append(want, id)
	}
	latest := seedHistoryImage(t, f, d, "history-current", d.AssignmentID, false, at)
	want = append(want, latest.ID)
	seedHistoryImage(t, f, d, "history-transient", d.AssignmentID, false, at)
	f.a.db.Model(&d).Update("latest_image_id", latest.ID)
	other, _, _ := f.register(false)
	seedHistoryImage(t, f, other, "history-other-device", other.AssignmentID, true, at)
	sort.Sort(sort.Reverse(sort.StringSlice(want)))
	defaultPage := f.admin.request("GET", "/api/v1/devices/"+d.ID+"/images", nil, 200)
	if defaultPage["total"] != float64(86) || defaultPage["page"] != float64(1) || defaultPage["page_size"] != float64(14) || defaultPage["total_pages"] != float64(7) || len(defaultPage["items"].([]any)) != 14 {
		t.Fatalf("default pagination: %#v", defaultPage)
	}
	seen := make(map[string]bool)
	var got []string
	for page := 1; page <= 7; page++ {
		result := f.admin.request("GET", fmt.Sprintf("/api/v1/devices/%s/images?page=%d&page_size=14", d.ID, page), nil, 200)
		if result["total"] != float64(86) {
			t.Fatalf("wrong page count: %#v", result)
		}
		for _, value := range result["items"].([]any) {
			id := value.(map[string]any)["id"].(string)
			if seen[id] {
				t.Fatal("duplicate image across stable pages: " + id)
			}
			seen[id] = true
			got = append(got, id)
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("stable tie-break/order changed: got %v want %v", got, want)
	}
	end := f.admin.request("GET", "/api/v1/devices/"+d.ID+"/images?page=8", nil, 200)
	if len(end["items"].([]any)) != 0 || end["page"] != float64(8) || end["total"] != float64(86) {
		t.Fatalf("out-of-range page: %#v", end)
	}
	for _, query := range []string{"page=0", "page=-1", "page=x", "page=1&page=2", "page=", "page_size=0", "page_size=61", "page_size=x", "page=9223372036854775807&page_size=60", "page=9999999999999999999999999"} {
		f.admin.request("GET", "/api/v1/devices/"+d.ID+"/images?"+query, nil, 400)
	}
	newBrowser(t, f.server.URL).request("GET", "/api/v1/devices/"+d.ID+"/images", nil, 401)
	cleared := f.admin.request("DELETE", "/api/v1/devices/"+d.ID+"/images", nil, 200)
	if cleared["cleared"] != float64(87) || cleared["files_removed"] != float64(87) {
		t.Fatalf("clear must include all pages and grace transient, not only the visible14: %#v", cleared)
	}
	if empty := f.admin.request("GET", "/api/v1/devices/"+d.ID+"/images", nil, 200); empty["total"] != float64(0) {
		t.Fatal("clear did not remove all history pages")
	}
	if otherPage := f.admin.request("GET", "/api/v1/devices/"+other.ID+"/images", nil, 200); otherPage["total"] != float64(1) {
		t.Fatal("clear removed another device's image")
	}
	t.Log("86 retained/latest images paginated 14/page beyond old60 limit; transient/otherdevice excluded; stable tie-break, no overlap; malformed/overflow rejected; clear removes all87 stored files across all pages")
}

func TestImageHistoryClearScopesAndTasks(t *testing.T) {
	f := setupFixture(t)
	north, south := f.agent("historynorth"), f.agent("historysouth")
	d, token, _ := f.register(true)
	d = f.assign(d, north)
	at := time.Now().UTC()
	old := seedHistoryImage(t, f, d, "old-assignment-image", d.AssignmentID, true, at)
	d = f.assign(d, south)
	start := south.command(d.ID, "START_IMAGE_SESSION")
	var session ImageSession
	f.a.db.First(&session, "id = ?", start.SessionID)
	current := seedHistoryImage(t, f, d, "current-history-image", d.AssignmentID, true, at)
	transient := seedHistoryImage(t, f, d, "current-transient-image", d.AssignmentID, false, at)
	f.a.db.Model(&d).Updates(map[string]any{"latest_image_id": transient.ID, "last_image_at": at})
	other, _, _ := f.register(false)
	other = f.assign(other, north)
	otherImage := seedHistoryImage(t, f, other, "other-device-image", other.AssignmentID, true, at)
	if err := f.a.db.Create(&LocationReport{ID: newID("loc_"), DeviceID: d.ID, ReportID: "location-before-clear", AssignmentID: d.AssignmentID, CoordinateSystem: "WGS84", Latitude: 1, Longitude: 2, CollectedAt: at, ReceivedAt: at}).Error; err != nil {
		t.Fatal(err)
	}
	beforeDetail := south.request("GET", "/api/v1/devices/"+d.ID+"/images", nil, 200)
	if beforeDetail["total"] != float64(2) {
		t.Fatalf("assignment image list scope: %#v", beforeDetail)
	}
	north.request("DELETE", "/api/v1/devices/"+d.ID+"/images", nil, 404)
	south.request("DELETE", "/api/v1/devices/"+other.ID+"/images", nil, 404)
	newBrowser(t, f.server.URL).request("DELETE", "/api/v1/devices/"+d.ID+"/images", nil, 401)
	csrf := south.csrf
	south.csrf = ""
	south.request("DELETE", "/api/v1/devices/"+d.ID+"/images", nil, 403)
	south.csrf = csrf
	var beforeAudits int64
	f.a.db.Model(&AuditLog{}).Count(&beforeAudits)
	var beforeCommands, beforeLocations int64
	f.a.db.Model(&Command{}).Count(&beforeCommands)
	f.a.db.Model(&LocationReport{}).Count(&beforeLocations)
	cleared := south.request("DELETE", "/api/v1/devices/"+d.ID+"/images", nil, 200)
	if cleared["cleared"] != float64(2) || cleared["files_removed"] != float64(2) || cleared["file_cleanup_failed"] != float64(0) {
		t.Fatalf("clear response: %#v", cleared)
	}
	list := south.request("GET", "/api/v1/devices/"+d.ID+"/images", nil, 200)
	if list["total"] != float64(0) || len(list["items"].([]any)) != 0 || list["total_pages"] != float64(0) {
		t.Fatalf("cleared gallery not empty: %#v", list)
	}
	var fresh Device
	f.a.db.First(&fresh, "id = ?", d.ID)
	var afterSession ImageSession
	f.a.db.First(&afterSession, "id = ?", session.ID)
	if fresh.LatestImageID != "" || fresh.LastImageAt != nil || fresh.AgentID != d.AgentID || fresh.AssignmentID != d.AssignmentID || fresh.ActiveSessionID != session.ID || !reflect.DeepEqual(afterSession, session) {
		t.Fatalf("clear modified nonimage state: device=%+v session=%+v wantsession=%+v", fresh, afterSession, session)
	}
	for _, row := range []ImageReport{current, transient} {
		var saved ImageReport
		f.a.db.First(&saved, "id = ?", row.ID)
		if saved.FileName != "" || saved.ReportID != row.ReportID {
			t.Fatal("clear must preserve report identity tombstone")
		}
		if _, err := os.Stat(f.a.imagePath(row.FileName)); !os.IsNotExist(err) {
			t.Fatalf("cleared file still exists: %v", err)
		}
		if status, _, _ := imageResponse(t, south, row.ID); status != 404 {
			t.Fatalf("cleared image remains retrievable: %d", status)
		}
	}
	for _, row := range []ImageReport{old, otherImage} {
		if _, err := os.Stat(f.a.imagePath(row.FileName)); err != nil {
			t.Fatalf("cross-assignment/device file removed: %v", err)
		}
	}
	var afterAudits, afterCommands, afterLocations int64
	f.a.db.Model(&AuditLog{}).Count(&afterAudits)
	f.a.db.Model(&Command{}).Count(&afterCommands)
	f.a.db.Model(&LocationReport{}).Count(&afterLocations)
	if afterAudits != beforeAudits+1 || afterCommands != beforeCommands || afterLocations != beforeLocations {
		t.Fatal("clear erased audit/command/location records")
	}
	var event AuditLog
	f.a.db.Where("action = ? AND target_id = ?", "images.clear", d.ID).First(&event)
	if event.ActorID != south.userID() {
		t.Fatal("clear audit missing actor")
	}
	// A retried old upload is a successful idempotent tombstone, not a resurrected image.
	retry := f.upload(d, token, map[string]any{"report_id": transient.ReportID, "session_id": session.ID, "session_version": session.Version, "frame_seq": 1}, testPNG(t), 201)
	if retry["id"] != transient.ID {
		t.Fatal("retry changed report identity")
	}
	if list := south.request("GET", "/api/v1/devices/"+d.ID+"/images", nil, 200); list["total"] != float64(0) {
		t.Fatal("cleared old retry resurrected gallery")
	}
	// Capture task remains active, so a genuinely new frame may appear after clear.
	freshFrame := f.upload(d, token, map[string]any{"report_id": "new-after-clear", "session_id": session.ID, "session_version": session.Version, "frame_seq": 2}, testPNG(t), 201)["id"].(string)
	if status, _, _ := imageResponse(t, south, freshFrame); status != 200 {
		t.Fatal("new capture stopped after clearing old pictures")
	}
	adminClear := f.admin.request("DELETE", "/api/v1/devices/"+d.ID+"/images", nil, 200)
	if adminClear["cleared"] != float64(2) { // old assignment + newly captured current frame
		t.Fatalf("administrator scope: %#v", adminClear)
	}
	if status, _, _ := imageResponse(t, f.admin, old.ID); status != 404 {
		t.Fatal("admin failed to clear old assignment history")
	}
	if status, _, _ := imageResponse(t, north, otherImage.ID); status != 200 {
		t.Fatal("admin clear touched another device")
	}
	if again := f.admin.request("DELETE", "/api/v1/devices/"+d.ID+"/images", nil, 200); again["cleared"] != float64(0) {
		t.Fatal("empty clear must be idempotent")
	}
	t.Log("clear scopes agent assignment/admin device; CSRF/auth enforced; files and latest cleared; tombstone prevents replay; capture/location/commands/audits preserved; fresh upload succeeds")
}

func TestImageHistoryClearReportsFileCleanupFailure(t *testing.T) {
	f := setupFixture(t)
	d, _, _ := f.register(false)
	row := seedHistoryImage(t, f, d, "unremovable-image", d.AssignmentID, true, time.Now().UTC())
	path := f.a.imagePath(row.FileName)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "synthetic-child"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	result := f.admin.request("DELETE", "/api/v1/devices/"+d.ID+"/images", nil, 200)
	if result["cleared"] != float64(1) || result["files_removed"] != float64(0) || result["file_cleanup_failed"] != float64(1) || !strings.Contains(result["message"].(string), "部分文件清理未完成") {
		t.Fatalf("failed physical cleanup must be explicit: %#v", result)
	}
	if strings.Contains(fmt.Sprint(result), path) {
		t.Fatal("file path disclosed")
	}
	if list := f.admin.request("GET", "/api/v1/devices/"+d.ID+"/images", nil, 200); list["total"] != float64(0) {
		t.Fatal("logical clear should still succeed with cleanup warning")
	}
	t.Log("physical cleanup failure is counted and warned without exposing a path or claiming physical removal")
}

func TestImageHistoryClearRepairsTombstoneLatestPointer(t *testing.T) {
	f := setupFixture(t)
	d, _, _ := f.register(false)
	now := time.Now().UTC()
	row := seedHistoryImage(t, f, d, "already-cleared-latest", d.AssignmentID, true, now)
	if err := f.a.db.Model(&row).Update("file_name", "").Error; err != nil {
		t.Fatal(err)
	}
	if err := f.a.db.Model(&d).Updates(map[string]any{"latest_image_id": row.ID, "last_image_at": now}).Error; err != nil {
		t.Fatal(err)
	}
	result := f.admin.request("DELETE", "/api/v1/devices/"+d.ID+"/images", nil, 200)
	var fresh Device
	f.a.db.First(&fresh, "id = ?", d.ID)
	if result["cleared"] != float64(0) || fresh.LatestImageID != "" || fresh.LastImageAt != nil {
		t.Fatalf("stale tombstone latest pointer survives: %#v %+v", result, fresh)
	}
	t.Log("already-cleared latest pointer repaired without reviving old frames")
}

func TestImageHistoryDeleteEndpoint(t *testing.T) {
	f := setupFixture(t)
	d, _, _ := f.register(false)
	seedHistoryImage(t, f, d, "delete-endpoint-image", d.AssignmentID, true, time.Now().UTC())
	result := f.admin.request("DELETE", "/api/v1/devices/"+d.ID+"/images", nil, 200)
	if result["cleared"] != float64(1) {
		t.Fatalf("clear endpoint response: %#v", result)
	}
	t.Log("DELETE clears the selected device image through authenticated CSRF-protected route")
}
