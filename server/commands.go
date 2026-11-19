package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"gorm.io/gorm"
)

func commandEnvelope(c Command) map[string]any {
	var parameters any
	_ = json.Unmarshal([]byte(c.Parameters), &parameters)
	return map[string]any{"type": "command.dispatch", "command_id": c.ID, "device_id": c.DeviceID, "assignment_id": nullID(c.AssignmentID), "assignment_version": c.AssignmentVersion, "command": c.Type, "created_at": c.CreatedAt, "expires_at": c.ExpiresAt, "parameters": parameters}
}
func (a *App) createCommand(w http.ResponseWriter, r *http.Request, u *userRequest) {
	var in struct {
		Type              string `json:"type"`
		QueueTTL          int    `json:"queue_ttl_seconds"`
		Duration          int    `json:"duration_seconds"`
		IntervalMS        int    `json:"interval_ms"`
		ResolutionPercent int    `json:"resolution_percent"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Type != "GET_IMAGE" && in.Type != "GET_LOCATION" && in.Type != "START_IMAGE_SESSION" && in.Type != "STOP_IMAGE_SESSION" {
		fail(w, 400, "INVALID_COMMAND", "指令类型无效")
		return
	}
	if in.QueueTTL == 0 {
		in.QueueTTL = 300
	}
	if in.QueueTTL < 30 || in.QueueTTL > 86400 || in.Duration < 0 || in.Duration > 86400 {
		fail(w, 400, "INVALID_DURATION", "指令有效期或任务时长无效")
		return
	}
	if in.IntervalMS == 0 {
		in.IntervalMS = 3000
	}
	if in.ResolutionPercent == 0 {
		in.ResolutionPercent = 70
	}
	if in.IntervalMS < 500 || in.IntervalMS > 60000 || in.ResolutionPercent < 25 || in.ResolutionPercent > 100 {
		fail(w, 400, "INVALID_CAPTURE_OPTIONS", "采集间隔需为 500–60000ms，PNG 尺寸比例需为 25–100%")
		return
	}

	a.writes.Lock()
	defer a.writes.Unlock()
	d, ok := a.authorizedDevice(w, r, u)
	if !ok {
		return
	}
	if !d.Enabled {
		fail(w, 409, "DEVICE_DISABLED", "设备已停用")
		return
	}
	if in.Type == "GET_LOCATION" && !locationCapable(d) {
		fail(w, 422, "LOCATION_UNSUPPORTED", "该客户端尚未接入定位采集")
		return
	}
	var command Command
	var session *ImageSession
	reused := false
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if in.Type == "STOP_IMAGE_SESSION" {
			if d.ActiveSessionID == "" {
				return errors.New("no_active_session")
			}
			var s ImageSession
			if e := tx.First(&s, "id = ?", d.ActiveSessionID).Error; e != nil {
				return e
			}
			c, e := a.stopImageSession(tx, &s, u.User.ID)
			command = c
			session = &s
			return e
		}
		if in.Type == "START_IMAGE_SESSION" && d.ActiveSessionID != "" {
			var s ImageSession
			if e := tx.First(&s, "id = ?", d.ActiveSessionID).Error; e != nil {
				return e
			}
			if !s.DesiredRunning {
				return errors.New("stop_pending")
			}
			if e := tx.First(&command, "id = ?", s.StartCommandID).Error; e != nil {
				return e
			}
			session = &s
			reused = true
			return nil
		}
		parameters := map[string]any{"capture_source": "uikit_window", "capture_scope": "active_window", "allow_cached": false, "max_long_edge_px": 1280 * in.ResolutionPercent / 100, "resolution_percent": in.ResolutionPercent}
		if in.Type == "GET_LOCATION" {
			parameters = map[string]any{"max_age_seconds": 60, "timeout_seconds": 30}
		}
		now := time.Now().UTC()
		command = Command{ID: newID("cmd_"), DeviceID: d.ID, AssignmentID: d.AssignmentID, AssignmentVersion: d.AssignmentVersion, ActorID: u.User.ID, Type: in.Type, Status: "queued", CreatedAt: now, ExpiresAt: now.Add(time.Duration(in.QueueTTL) * time.Second)}
		if in.Type == "START_IMAGE_SESSION" {
			s := ImageSession{ID: newID("task_"), DeviceID: d.ID, AssignmentID: d.AssignmentID, AssignmentVersion: d.AssignmentVersion, ActorID: u.User.ID, Version: 1, DesiredRunning: true, ObservedState: "queued", StartCommandID: command.ID, CreatedAt: now}
			parameters["session_id"] = s.ID
			parameters["session_version"] = 1
			parameters["interval_ms"] = in.IntervalMS
			parameters["task_expires_at"] = nil
			if in.Duration > 0 {
				deadline := now.Add(time.Duration(in.Duration) * time.Second)
				s.TaskExpiresAt = &deadline
				parameters["task_expires_at"] = deadline
			}
			command.SessionID = s.ID
			if e := tx.Create(&s).Error; e != nil {
				return e
			}
			if e := tx.Model(&Device{}).Where("id = ?", d.ID).Update("active_session_id", s.ID).Error; e != nil {
				return e
			}
			session = &s
		}
		payload, e := json.Marshal(parameters)
		if e != nil {
			return e
		}
		command.Parameters = string(payload)
		if e = tx.Create(&command).Error; e != nil {
			return e
		}
		return a.audit(tx, u.User.ID, "command."+in.Type, d.ID)
	})
	if err != nil {
		if err.Error() == "stop_pending" {
			fail(w, 409, "STOP_PENDING", "正在等待手机确认停止，请稍后再开始")
		} else if err.Error() == "no_active_session" {
			fail(w, 409, "NO_ACTIVE_SESSION", "当前没有实时任务")
		} else {
			fail(w, 500, "DATABASE_ERROR", "指令创建失败")
		}
		return
	}
	a.hub.Sync(d.ID)
	jsonOut(w, 201, map[string]any{"command": command, "session": session, "reused": reused})
}

func (a *App) stopImageSession(tx *gorm.DB, s *ImageSession, actor string) (Command, error) {
	if !s.DesiredRunning && s.StopCommandID != "" {
		var existing Command
		err := tx.First(&existing, "id = ?", s.StopCommandID).Error
		return existing, err
	}
	now := time.Now().UTC()
	s.Version++
	s.DesiredRunning = false
	s.ObservedState = "stopping"
	s.StoppedAt = &now
	payload, _ := json.Marshal(map[string]any{"session_id": s.ID, "session_version": s.Version, "reason": "server_stop"})
	c := Command{ID: newID("cmd_"), DeviceID: s.DeviceID, AssignmentID: s.AssignmentID, AssignmentVersion: s.AssignmentVersion, ActorID: actor, Type: "STOP_IMAGE_SESSION", Parameters: string(payload), Status: "queued", SessionID: s.ID, CreatedAt: now, ExpiresAt: now.Add(30 * 24 * time.Hour)}
	s.StopCommandID = c.ID
	if err := tx.Model(&Command{}).Where("id = ? AND completed_at IS NULL", s.StartCommandID).Updates(map[string]any{"status": "canceled", "completed_at": now}).Error; err != nil {
		return c, err
	}
	if err := tx.Save(s).Error; err != nil {
		return c, err
	}
	return c, tx.Create(&c).Error
}

func (a *App) commands(w http.ResponseWriter, r *http.Request, u *userRequest) {
	q := a.db.Model(&Command{})
	if u.User.Role != "admin" {
		q = q.Where("device_id IN (?)", a.scopedDevices(a.db, u).Select("id")).Where("assignment_id IN (?)", a.scopedDevices(a.db, u).Select("assignment_id"))
	}
	if id := r.URL.Query().Get("device_id"); id != "" {
		q = q.Where("device_id = ?", id)
	}
	var total int64
	q.Count(&total)
	page, size := pagination(r)
	var list []Command
	q.Order("created_at desc").Offset((page - 1) * size).Limit(size).Find(&list)
	jsonOut(w, 200, map[string]any{"items": list, "total": total})
}

func (a *App) pendingCommands(d Device, redeliver bool) []Command {
	var stops []Command
	a.db.Where("device_id = ? AND type = ? AND completed_at IS NULL AND expires_at > ?", d.ID, "STOP_IMAGE_SESSION", time.Now().UTC()).Order("created_at asc").Find(&stops)
	if len(stops) > 0 {
		return stops
	}
	q := a.db.Where("device_id = ? AND completed_at IS NULL AND assignment_version = ? AND expires_at > ?", d.ID, d.AssignmentVersion, time.Now().UTC())
	if !redeliver {
		q = q.Where("acked_at IS NULL AND (last_attempt_at IS NULL OR last_attempt_at < ?)", time.Now().Add(-10*time.Second))
	}
	var result []Command
	q.Order("created_at asc").Limit(100).Find(&result)
	return result
}

func (a *App) applyAck(deviceID, id string, progress string) error {
	var c Command
	if err := a.db.Where("id = ? AND device_id = ?", id, deviceID).First(&c).Error; err != nil {
		return err
	}
	if c.CompletedAt != nil {
		return nil
	}
	now := time.Now().UTC()
	status := "received"
	if progress == "running" || progress == "waiting_foreground" || progress == "waiting_permission" {
		status = progress
	}
	return a.db.Model(&c).Updates(map[string]any{"acked_at": now, "status": status}).Error
}

func (a *App) applyResult(deviceID, id string, result map[string]any) error {
	a.writes.Lock()
	defer a.writes.Unlock()
	return a.db.Transaction(func(tx *gorm.DB) error {
		var c Command
		if err := tx.Where("id = ? AND device_id = ?", id, deviceID).First(&c).Error; err != nil {
			return err
		}
		if c.CompletedAt != nil {
			return nil
		}
		now := time.Now().UTC()
		status, _ := result["status"].(string)
		if status == "" {
			return errors.New("missing result status")
		}
		state := status
		switch status {
		case "accepted", "waiting_foreground", "stopped", "succeeded":
			state = "succeeded"
		case "failed", "expired", "canceled":
		default:
			return errors.New("invalid result status")
		}
		encoded, e := json.Marshal(result)
		if e != nil {
			return e
		}
		message, _ := result["error_message"].(string)
		if len(message) > 500 {
			message = message[:500]
		}
		if err := tx.Model(&c).Updates(map[string]any{"status": state, "result": string(encoded), "error_message": message, "completed_at": now, "acked_at": now}).Error; err != nil {
			return err
		}
		if c.SessionID != "" {
			var s ImageSession
			if e := tx.First(&s, "id = ?", c.SessionID).Error; e != nil {
				return e
			}
			if c.Type == "STOP_IMAGE_SESSION" && state == "succeeded" {
				if e := tx.Model(&s).Update("observed_state", "stopped").Error; e != nil {
					return e
				}
				return tx.Model(&Device{}).Where("id = ? AND active_session_id = ?", deviceID, s.ID).Update("active_session_id", "").Error
			}
			if c.Type == "START_IMAGE_SESSION" && s.DesiredRunning {
				observed := "waiting_foreground"
				if status == "accepted" {
					observed = "starting"
				}
				if state != "succeeded" {
					observed = state
					if e := tx.Model(&s).Updates(map[string]any{"desired_running": false, "observed_state": observed, "stopped_at": now}).Error; e != nil {
						return e
					}
					return tx.Model(&Device{}).Where("id = ? AND active_session_id = ?", deviceID, s.ID).Update("active_session_id", "").Error
				}
				return tx.Model(&s).Update("observed_state", observed).Error
			}
		}
		return nil
	})
}

func (a *App) deviceCommandAck(w http.ResponseWriter, r *http.Request, d *Device) {
	if err := a.applyAck(d.ID, r.PathValue("id"), ""); err != nil {
		fail(w, 404, "NOT_FOUND", "指令不存在")
		return
	}
	jsonOut(w, 200, map[string]bool{"ok": true})
}
func (a *App) deviceCommandProgress(w http.ResponseWriter, r *http.Request, d *Device) {
	var in struct{ Status string }
	if !readJSON(w, r, &in) {
		return
	}
	if err := a.applyAck(d.ID, r.PathValue("id"), in.Status); err != nil {
		fail(w, 404, "NOT_FOUND", "指令不存在")
		return
	}
	jsonOut(w, 200, map[string]bool{"ok": true})
}
func (a *App) deviceCommandResult(w http.ResponseWriter, r *http.Request, d *Device) {
	var in map[string]any
	if !readJSON(w, r, &in) {
		return
	}
	if err := a.applyResult(d.ID, r.PathValue("id"), in); err != nil {
		fail(w, 400, "INVALID_RESULT", "结果保存失败")
		return
	}
	a.hub.Sync(d.ID)
	jsonOut(w, 200, map[string]bool{"ok": true})
}

func (a *App) runWorker(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	cleanup := time.NewTicker(time.Hour)
	defer cleanup.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.expireWork()
			a.hub.Dispatch()
		case <-cleanup.C:
			a.cleanup()
		}
	}
}
func (a *App) expireWork() {
	a.writes.Lock()
	defer a.writes.Unlock()
	now := time.Now().UTC()
	_ = a.db.Transaction(func(tx *gorm.DB) error {
		var sessions []ImageSession
		tx.Where("desired_running = ? AND task_expires_at IS NOT NULL AND task_expires_at < ?", true, now).Find(&sessions)
		for _, s := range sessions {
			if _, err := a.stopImageSession(tx, &s, "system"); err != nil {
				return err
			}
		}
		var expired []Command
		tx.Where("completed_at IS NULL AND expires_at < ?", now).Find(&expired)
		for _, c := range expired {
			if c.Type == "STOP_IMAGE_SESSION" {
				var s ImageSession
				if e := tx.First(&s, "id = ?", c.SessionID).Error; e != nil {
					return e
				}
				s.StopCommandID = ""
				if _, e := a.stopImageSession(tx, &s, "system"); e != nil {
					return e
				}
				tx.Model(&c).Updates(map[string]any{"status": "expired", "completed_at": now})
				continue
			}
			if c.Type == "START_IMAGE_SESSION" {
				var s ImageSession
				if tx.First(&s, "id = ?", c.SessionID).Error == nil && s.DesiredRunning && c.AckedAt == nil {
					if _, err := a.stopImageSession(tx, &s, "system"); err != nil {
						return err
					}
				} else if c.AckedAt != nil {
					continue
				}
			}
			tx.Model(&c).Updates(map[string]any{"status": "expired", "completed_at": now})
		}
		return nil
	})
}
