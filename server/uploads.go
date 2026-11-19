package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"image/png"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"gorm.io/gorm"
)

type imageMetadata struct {
	ReportID          string    `json:"report_id"`
	DeviceID          string    `json:"device_id"`
	CommandID         string    `json:"command_id"`
	SessionID         string    `json:"session_id"`
	SessionVersion    uint64    `json:"session_version"`
	FrameSeq          uint64    `json:"frame_seq"`
	AssignmentID      string    `json:"assignment_id"`
	AssignmentVersion uint64    `json:"assignment_version"`
	Source            string    `json:"capture_source"`
	Scope             string    `json:"capture_scope"`
	CapturedAt        time.Time `json:"captured_at"`
	Width             int       `json:"pixel_width"`
	Height            int       `json:"pixel_height"`
	Cached            bool      `json:"is_cached"`
}
type apiError struct {
	Status        int
	Code, Message string
}

func (e *apiError) Error() string { return e.Message }
func uploadFailure(w http.ResponseWriter, err error) {
	var api *apiError
	if errors.As(err, &api) {
		fail(w, api.Status, api.Code, api.Message)
	} else {
		fail(w, 500, "STORAGE_ERROR", "数据保存失败")
	}
}
func assignmentMatches(d Device, id string, version uint64) bool {
	return d.AssignmentID == id && d.AssignmentVersion == version
}

var fileNamePattern = regexp.MustCompile(`^[a-f0-9]{32}\.png$`)

func (a *App) imagePath(name string) string {
	if !fileNamePattern.MatchString(name) {
		return ""
	}
	return filepath.Join(a.cfg.DataDir, "images", name)
}

func (a *App) uploadImage(w http.ResponseWriter, r *http.Request, authenticated *Device) {
	if !a.limiter.Allow("image:"+authenticated.ID, 30, 10*time.Second) {
		w.Header().Set("Retry-After", "3")
		fail(w, 429, "RATE_LIMITED", "图片上传过于频繁")
		return
	}
	select {
	case a.imageSlots <- struct{}{}:
		defer func() { <-a.imageSlots }()
	default:
		w.Header().Set("Retry-After", "1")
		fail(w, 503, "BUSY", "图片处理繁忙，请稍后重试")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, (8<<20)+(128<<10))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		fail(w, 413, "IMAGE_TOO_LARGE", "图片或请求超出限制")
		return
	}
	defer r.MultipartForm.RemoveAll()
	raw := r.FormValue("metadata")
	var meta imageMetadata
	if len(raw) > 64<<10 || json.Unmarshal([]byte(raw), &meta) != nil || len(meta.ReportID) == 0 || len(meta.ReportID) > 200 || meta.CapturedAt.IsZero() || meta.Source != "uikit_window" || meta.Scope != "active_window" || (meta.DeviceID != "" && meta.DeviceID != authenticated.ID) {
		fail(w, 400, "INVALID_PAYLOAD", "图片元数据无效")
		return
	}
	if !assignmentMatches(*authenticated, meta.AssignmentID, meta.AssignmentVersion) {
		fail(w, 409, "ASSIGNMENT_CHANGED", "设备归属已改变")
		return
	}
	input, _, err := r.FormFile("image")
	if err != nil {
		fail(w, 400, "INVALID_IMAGE", "缺少 PNG 图片")
		return
	}
	defer input.Close()
	data, err := io.ReadAll(io.LimitReader(input, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		fail(w, 413, "IMAGE_TOO_LARGE", "PNG 超过 8 MB")
		return
	}
	info, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || info.Width <= 0 || info.Height <= 0 || info.Width > 2560 || info.Height > 2560 || info.Width*info.Height > 2560*2560 || info.Width != meta.Width || info.Height != meta.Height {
		fail(w, 422, "INVALID_PNG", "PNG 无效或像素尺寸不匹配")
		return
	}
	if _, err = png.Decode(bytes.NewReader(data)); err != nil {
		fail(w, 422, "INVALID_PNG", "PNG 内容不完整")
		return
	}
	name := newID("") + ".png"
	path := a.imagePath(name)
	if err = os.WriteFile(path, data, 0600); err != nil {
		fail(w, 500, "STORAGE_ERROR", "图片写入失败")
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(path)
		}
	}()
	a.writes.Lock()
	defer a.writes.Unlock()
	var report ImageReport
	var removeOld string
	err = a.db.Transaction(func(tx *gorm.DB) error {
		var d Device
		if e := tx.First(&d, "id = ?", authenticated.ID).Error; e != nil {
			return e
		}
		if !d.Enabled {
			return &apiError{403, "DEVICE_DISABLED", "设备已停用"}
		}
		if !assignmentMatches(d, meta.AssignmentID, meta.AssignmentVersion) {
			return &apiError{409, "ASSIGNMENT_CHANGED", "设备归属已改变"}
		}
		if e := tx.Where("device_id = ? AND report_id = ?", d.ID, meta.ReportID).First(&report).Error; e == nil {
			if report.AssignmentID != meta.AssignmentID {
				return &apiError{409, "REPORT_CONFLICT", "报告身份不一致"}
			}
			return nil
		} else if !errors.Is(e, gorm.ErrRecordNotFound) {
			return e
		}
		now := time.Now().UTC()
		if d.AgentID == "" {
			var used int64
			start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
			if e := tx.Model(&ImageReport{}).Where("device_id = ? AND received_at >= ?", d.ID, start).Select("COALESCE(SUM(bytes),0)").Scan(&used).Error; e != nil {
				return e
			}
			if used+int64(len(data)) > 64<<20 {
				return &apiError{429, "PENDING_UPLOAD_QUOTA", "待分配设备每日图片额度已用完，请由总台分配后继续"}
			}
		}
		report = ImageReport{ID: newID("img_"), DeviceID: d.ID, ReportID: meta.ReportID, AssignmentID: d.AssignmentID, CommandID: meta.CommandID, SessionID: meta.SessionID, FrameSeq: meta.FrameSeq, FileName: name, Width: info.Width, Height: info.Height, Bytes: int64(len(data)), CapturedAt: meta.CapturedAt, ReceivedAt: now, IsCached: meta.Cached}
		updateLatest := true
		if meta.SessionID != "" {
			var s ImageSession
			if tx.Where("id = ? AND device_id = ?", meta.SessionID, d.ID).First(&s).Error != nil || !s.DesiredRunning || s.Version != meta.SessionVersion || s.AssignmentID != d.AssignmentID || (s.TaskExpiresAt != nil && !s.TaskExpiresAt.After(now)) {
				return &apiError{410, "SESSION_EXPIRED", "实时任务已停止或过期"}
			}
			if meta.Cached || meta.FrameSeq == 0 {
				return &apiError{422, "INVALID_FRAME", "实时画面必须是新采集且具有帧序号"}
			}
			updateLatest = meta.FrameSeq > s.LastFrameSeq
			report.KeepHistory = updateLatest && (s.LastHistoryAt == nil || now.Sub(*s.LastHistoryAt) >= 30*time.Second)
			if updateLatest {
				changes := map[string]any{"last_frame_seq": meta.FrameSeq, "observed_state": "running"}
				if report.KeepHistory {
					changes["last_history_at"] = now
				}
				if e := tx.Model(&s).Updates(changes).Error; e != nil {
					return e
				}
			}
		} else {
			var c Command
			if meta.CommandID == "" || tx.Where("id = ? AND device_id = ? AND type = ?", meta.CommandID, d.ID, "GET_IMAGE").First(&c).Error != nil {
				return &apiError{422, "INVALID_COMMAND", "图片必须关联有效获取指令"}
			}
			if c.AssignmentID != d.AssignmentID || c.Status == "canceled" || c.Status == "expired" || (c.ExpiresAt.Before(now) && c.AckedAt == nil) {
				return &apiError{410, "COMMAND_EXPIRED", "截图指令已过期或取消"}
			}
			if meta.Cached {
				return &apiError{422, "CACHED_IMAGE_REJECTED", "本次请求需要新采集的窗口画面"}
			}
			report.KeepHistory = true
			if e := tx.Model(&c).Updates(map[string]any{"status": "succeeded", "completed_at": now, "acked_at": now}).Error; e != nil {
				return e
			}
		}
		if !updateLatest {
			report.FileName = ""
		} else {
			if d.LatestImageID != "" {
				var old ImageReport
				if tx.First(&old, "id = ?", d.LatestImageID).Error == nil && !old.KeepHistory && old.FileName != "" {
					removeOld = a.imagePath(old.FileName)
					if e := tx.Model(&old).Update("file_name", "").Error; e != nil {
						return e
					}
				}
			}
			if e := tx.Model(&d).Updates(map[string]any{"latest_image_id": report.ID, "last_image_at": now, "last_seen_at": now, "capture_state": "running"}).Error; e != nil {
				return e
			}
		}
		return tx.Create(&report).Error
	})
	if err != nil {
		uploadFailure(w, err)
		return
	}
	if report.FileName == name {
		committed = true
	}
	if removeOld != "" {
		_ = os.Remove(removeOld)
	}
	jsonOut(w, 201, map[string]any{"id": report.ID, "report_id": report.ReportID, "status": "succeeded"})
}

func (a *App) images(w http.ResponseWriter, r *http.Request, u *userRequest) {
	d, ok := a.authorizedDevice(w, r, u)
	if !ok {
		return
	}
	q := a.db.Where("device_id = ? AND file_name <> ?", d.ID, "")
	if u.User.Role != "admin" {
		q = q.Where("assignment_id = ?", d.AssignmentID)
	}
	var rows []ImageReport
	q.Order("received_at desc").Limit(60).Find(&rows)
	jsonOut(w, 200, map[string]any{"items": rows})
}
func (a *App) imageFile(w http.ResponseWriter, r *http.Request, u *userRequest) {
	var image ImageReport
	if a.db.First(&image, "id = ?", r.PathValue("id")).Error != nil || image.FileName == "" {
		fail(w, 404, "NOT_FOUND", "图片不存在或已清理")
		return
	}
	var d Device
	if a.scopedDevices(a.db, u).Where("id = ?", image.DeviceID).First(&d).Error != nil || (u.User.Role != "admin" && image.AssignmentID != d.AssignmentID) {
		fail(w, 404, "NOT_FOUND", "图片不属于当前账号")
		return
	}
	path := a.imagePath(image.FileName)
	if path == "" {
		fail(w, 404, "NOT_FOUND", "图片不存在")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeFile(w, r, path)
}

func (a *App) uploadLocation(w http.ResponseWriter, r *http.Request, authenticated *Device) {
	var meta struct {
		ReportID          string    `json:"report_id"`
		CommandID         string    `json:"command_id"`
		AssignmentID      string    `json:"assignment_id"`
		AssignmentVersion uint64    `json:"assignment_version"`
		Latitude          float64   `json:"latitude"`
		Longitude         float64   `json:"longitude"`
		CoordinateSystem  string    `json:"coordinate_system"`
		Accuracy          float64   `json:"horizontal_accuracy_m"`
		CollectedAt       time.Time `json:"collected_at"`
	}
	if !readJSON(w, r, &meta) {
		return
	}
	if meta.ReportID == "" || len(meta.ReportID) > 200 || meta.CoordinateSystem != "WGS84" || meta.CollectedAt.IsZero() || math.IsNaN(meta.Latitude) || math.IsNaN(meta.Longitude) || math.IsNaN(meta.Accuracy) || meta.Latitude < -90 || meta.Latitude > 90 || meta.Longitude < -180 || meta.Longitude > 180 || meta.Accuracy < 0 {
		fail(w, 422, "INVALID_LOCATION", "定位坐标、精度或时间无效")
		return
	}
	a.writes.Lock()
	defer a.writes.Unlock()
	var result LocationReport
	err := a.db.Transaction(func(tx *gorm.DB) error {
		var d Device
		if e := tx.First(&d, "id = ?", authenticated.ID).Error; e != nil {
			return e
		}
		if !d.Enabled {
			return &apiError{403, "DEVICE_DISABLED", "设备已停用"}
		}
		if !assignmentMatches(d, meta.AssignmentID, meta.AssignmentVersion) {
			return &apiError{409, "ASSIGNMENT_CHANGED", "设备归属已改变"}
		}
		if e := tx.Where("device_id = ? AND report_id = ?", d.ID, meta.ReportID).First(&result).Error; e == nil {
			return nil
		}
		now := time.Now().UTC()
		result = LocationReport{ID: newID("loc_"), DeviceID: d.ID, ReportID: meta.ReportID, AssignmentID: d.AssignmentID, CommandID: meta.CommandID, Latitude: meta.Latitude, Longitude: meta.Longitude, CoordinateSystem: meta.CoordinateSystem, Accuracy: meta.Accuracy, CollectedAt: meta.CollectedAt, ReceivedAt: now}
		if meta.CommandID != "" {
			var c Command
			if tx.Where("id = ? AND device_id = ? AND type = ?", meta.CommandID, d.ID, "GET_LOCATION").First(&c).Error != nil || c.AssignmentID != d.AssignmentID || c.Status == "expired" || c.Status == "canceled" {
				return &apiError{410, "COMMAND_EXPIRED", "定位指令无效"}
			}
			tx.Model(&c).Updates(map[string]any{"status": "succeeded", "completed_at": now, "acked_at": now})
		}
		if e := tx.Create(&result).Error; e != nil {
			return e
		}
		return tx.Model(&d).Updates(map[string]any{"last_location_at": now, "last_seen_at": now}).Error
	})
	if err != nil {
		uploadFailure(w, err)
		return
	}
	jsonOut(w, 201, map[string]any{"id": result.ID, "report_id": result.ReportID, "status": "succeeded"})
}
func (a *App) locations(w http.ResponseWriter, r *http.Request, u *userRequest) {
	d, ok := a.authorizedDevice(w, r, u)
	if !ok {
		return
	}
	q := a.db.Where("device_id = ?", d.ID)
	if u.User.Role != "admin" {
		q = q.Where("assignment_id = ?", d.AssignmentID)
	}
	var list []LocationReport
	q.Order("received_at desc").Limit(100).Find(&list)
	jsonOut(w, 200, map[string]any{"items": list})
}
func (a *App) cleanup() {
	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour)
	var old []ImageReport
	a.db.Where("received_at < ?", cutoff).Limit(1000).Find(&old)
	for _, image := range old {
		if path := a.imagePath(image.FileName); path != "" {
			_ = os.Remove(path)
		}
		a.db.Delete(&image)
	}
	a.db.Where("received_at < ?", cutoff).Delete(&LocationReport{})
	a.db.Where("expires_at < ?", time.Now().UTC()).Delete(&LoginSession{})
}
