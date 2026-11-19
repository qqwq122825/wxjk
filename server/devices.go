package main

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

type deviceHandler func(http.ResponseWriter, *http.Request, *Device)

func (a *App) deviceRoute(handler deviceHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || len(token) > 512 || token == r.Header.Get("Authorization") {
			fail(w, 401, "DEVICE_UNAUTHENTICATED", "设备身份无效")
			return
		}
		var d Device
		if a.db.Where("token_hash = ?", hashSecret(token)).First(&d).Error != nil {
			fail(w, 401, "DEVICE_UNAUTHENTICATED", "设备身份无效")
			return
		}
		if !d.Enabled {
			fail(w, 403, "DEVICE_DISABLED", "设备已停用")
			return
		}
		handler(w, r, &d)
	}
}
func (a *App) scopedDevices(tx *gorm.DB, u *userRequest) *gorm.DB {
	q := tx.Model(&Device{})
	if u.User.Role != "admin" {
		q = q.Where("agent_id = ?", u.User.ID)
	}
	return q
}
func (a *App) authorizedDevice(w http.ResponseWriter, r *http.Request, u *userRequest) (Device, bool) {
	var d Device
	if a.scopedDevices(a.db, u).Where("id = ?", r.PathValue("id")).First(&d).Error != nil {
		fail(w, 404, "NOT_FOUND", "设备不存在或不属于当前账号")
		return d, false
	}
	return d, true
}
func (a *App) deviceView(d Device) map[string]any {
	var value map[string]any
	b, _ := json.Marshal(d)
	_ = json.Unmarshal(b, &value)
	value["connected"] = a.hub.Has(d.ID) && d.Enabled && d.LastSeenAt != nil && d.LastSeenAt.After(time.Now().Add(-120*time.Second))
	value["location_supported"] = locationCapable(d)
	var caps map[string]any
	_ = json.Unmarshal([]byte(d.Capabilities), &caps)
	value["capabilities"] = caps
	if d.AgentID != "" {
		var agent User
		if a.db.First(&agent, "id = ?", d.AgentID).Error == nil {
			value["agent_name"] = agent.DisplayName
		}
	}
	return value
}
func locationCapable(d Device) bool {
	var caps struct {
		Location bool `json:"location"`
	}
	_ = json.Unmarshal([]byte(d.Capabilities), &caps)
	return caps.Location
}

func (a *App) registerDevice(w http.ResponseWriter, r *http.Request) {
	if !a.limiter.Allow("register:"+ipOf(r), 120, time.Hour) {
		w.Header().Set("Retry-After", "3600")
		fail(w, 429, "RATE_LIMITED", "登记次数较多，请稍后重试")
		return
	}
	var in struct {
		InstallationID     string          `json:"installation_id"`
		RegistrationSecret string          `json:"registration_secret"`
		EnrollmentToken    string          `json:"enrollment_token"`
		Platform           string          `json:"platform"`
		OSVersion          string          `json:"os_version"`
		ClientVersion      string          `json:"client_version"`
		Capabilities       json.RawMessage `json:"capabilities"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	secret, err := base64.StdEncoding.DecodeString(in.RegistrationSecret)
	if err != nil || len(secret) != 32 || len(in.InstallationID) < 8 || len(in.InstallationID) > 200 || len(in.OSVersion) > 40 || len(in.ClientVersion) > 40 || len(in.Platform) > 20 {
		fail(w, 400, "INVALID_REGISTRATION", "安装标识或注册秘密无效")
		return
	}
	if a.cfg.EnrollmentToken != "" && subtle.ConstantTimeCompare([]byte(in.EnrollmentToken), []byte(a.cfg.EnrollmentToken)) != 1 {
		fail(w, 403, "ENROLLMENT_REQUIRED", "注册准入凭据无效")
		return
	}
	var caps map[string]any
	if len(in.Capabilities) == 0 {
		in.Capabilities = json.RawMessage(`{}`)
	}
	if json.Unmarshal(in.Capabilities, &caps) != nil {
		fail(w, 400, "INVALID_CAPABILITIES", "设备能力格式无效")
		return
	}
	a.writes.Lock()
	defer a.writes.Unlock()
	var d Device
	err = a.db.Where("installation_id = ?", in.InstallationID).First(&d).Error
	now := time.Now().UTC()
	if err == nil {
		if subtle.ConstantTimeCompare([]byte(d.RegistrationHash), []byte(hashSecret(in.RegistrationSecret))) != 1 {
			fail(w, 401, "REGISTRATION_REJECTED", "安装身份不匹配")
			return
		}
		if !d.Enabled {
			fail(w, 403, "DEVICE_DISABLED", "设备已停用")
			return
		}
		a.db.Model(&d).Updates(map[string]any{"os_version": in.OSVersion, "client_version": in.ClientVersion, "capabilities": string(in.Capabilities), "last_seen_at": now})
	} else if errors.Is(err, gorm.ErrRecordNotFound) {
		var count int64
		a.db.Model(&Device{}).Where("agent_id = ?", "").Count(&count)
		if count >= 1000 {
			fail(w, 429, "PENDING_POOL_FULL", "待分配设备池已满")
			return
		}
		d = Device{ID: newID("dev_"), InstallationID: in.InstallationID, RegistrationHash: hashSecret(in.RegistrationSecret), Name: "iPhone · " + in.InstallationID[:6], Platform: in.Platform, OSVersion: in.OSVersion, ClientVersion: in.ClientVersion, Capabilities: string(in.Capabilities), Enabled: true, LastSeenAt: &now, AppState: "unknown", CaptureState: "idle"}
		d.TokenHash = hashSecret(a.deviceToken(d.ID, in.RegistrationSecret))
		if err = a.db.Create(&d).Error; err != nil {
			fail(w, 500, "DATABASE_ERROR", "设备登记失败")
			return
		}
	} else {
		fail(w, 500, "DATABASE_ERROR", "设备登记失败")
		return
	}
	jsonOut(w, 200, map[string]any{"device_id": d.ID, "device_token": a.deviceToken(d.ID, in.RegistrationSecret), "server_time": now, "heartbeat_interval_seconds": 30, "assignment": map[string]any{"id": nullID(d.AssignmentID), "version": d.AssignmentVersion}})
}

func pagination(r *http.Request) (int, int) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if page < 1 {
		page = 1
	}
	if page > 100000 {
		page = 100000
	}
	if size < 1 || size > 100 {
		size = 50
	}
	return page, size
}
func (a *App) devices(w http.ResponseWriter, r *http.Request, u *userRequest) {
	q := a.scopedDevices(a.db, u)
	if search := r.URL.Query().Get("search"); search != "" {
		if len(search) > 200 {
			fail(w, 400, "INVALID_SEARCH", "搜索内容过长")
			return
		}
		q = q.Where("name LIKE ? OR id LIKE ? OR note LIKE ?", "%"+search+"%", "%"+search+"%", "%"+search+"%")
	}
	switch r.URL.Query().Get("state") {
	case "unassigned":
		q = q.Where("agent_id = ?", "")
	case "online":
		ids := a.hub.IDs()
		if len(ids) == 0 {
			q = q.Where("1=0")
		} else {
			q = q.Where("id IN ? AND last_seen_at > ?", ids, time.Now().Add(-120*time.Second))
		}
	case "offline":
		ids := a.hub.IDs()
		if len(ids) > 0 {
			q = q.Where("id NOT IN ? OR last_seen_at < ?", ids, time.Now().Add(-120*time.Second))
		}
	}
	var total int64
	q.Count(&total)
	page, size := pagination(r)
	var list []Device
	if err := q.Order("created_at desc").Offset((page - 1) * size).Limit(size).Find(&list).Error; err != nil {
		fail(w, 500, "DATABASE_ERROR", "设备读取失败")
		return
	}
	items := make([]map[string]any, 0, len(list))
	for _, d := range list {
		items = append(items, a.deviceView(d))
	}
	jsonOut(w, 200, map[string]any{"items": items, "total": total, "page": page, "page_size": size})
}
func (a *App) dashboard(w http.ResponseWriter, r *http.Request, u *userRequest) {
	var total, unassigned, enabled, agents int64
	a.scopedDevices(a.db, u).Count(&total)
	a.scopedDevices(a.db, u).Where("agent_id = ?", "").Count(&unassigned)
	a.scopedDevices(a.db, u).Where("enabled = ?", true).Count(&enabled)
	if u.User.Role == "admin" {
		a.db.Model(&User{}).Where("role = ? AND enabled = ?", "agent", true).Count(&agents)
	}
	ids := a.hub.IDs()
	var online int64
	if len(ids) > 0 {
		a.scopedDevices(a.db, u).Where("id IN ? AND enabled = ? AND last_seen_at > ?", ids, true, time.Now().Add(-120*time.Second)).Count(&online)
	}
	var active int64
	q := a.db.Model(&ImageSession{}).Where("desired_running = ?", true)
	if u.User.Role != "admin" {
		q = q.Where("assignment_id IN (?)", a.scopedDevices(a.db, u).Select("assignment_id"))
	}
	q.Count(&active)
	jsonOut(w, 200, map[string]any{"total": total, "online": online, "offline": total - online, "unassigned": unassigned, "enabled": enabled, "agents": agents, "active_tasks": active, "server_time": time.Now().UTC()})
}
func (a *App) deviceDetail(w http.ResponseWriter, r *http.Request, u *userRequest) {
	d, ok := a.authorizedDevice(w, r, u)
	if !ok {
		return
	}
	result := a.deviceView(d)
	if d.LatestImageID != "" {
		var image ImageReport
		if a.db.First(&image, "id = ?", d.LatestImageID).Error == nil && (u.User.Role == "admin" || image.AssignmentID == d.AssignmentID) {
			result["latest_image"] = image
		}
	}
	var loc LocationReport
	q := a.db.Where("device_id = ?", d.ID)
	if u.User.Role != "admin" {
		q = q.Where("assignment_id = ?", d.AssignmentID)
	}
	if q.Order("received_at desc").First(&loc).Error == nil {
		result["latest_location"] = loc
	}
	if d.ActiveSessionID != "" {
		var session ImageSession
		if a.db.First(&session, "id = ?", d.ActiveSessionID).Error == nil {
			result["session"] = session
		}
	}
	var cmds []Command
	cq := a.db.Where("device_id = ?", d.ID)
	if u.User.Role != "admin" {
		cq = cq.Where("assignment_id = ?", d.AssignmentID)
	}
	cq.Order("created_at desc").Limit(8).Find(&cmds)
	result["recent_commands"] = cmds
	jsonOut(w, 200, result)
}
func (a *App) renameDevice(w http.ResponseWriter, r *http.Request, u *userRequest) {
	a.writes.Lock()
	defer a.writes.Unlock()
	d, ok := a.authorizedDevice(w, r, u)
	if !ok {
		return
	}
	var in struct {
		Name string
		Note string
	}
	if !readJSON(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 100 || len(in.Note) > 500 {
		fail(w, 400, "INVALID_NAME", "设备名称或备注无效")
		return
	}
	a.db.Model(&d).Updates(map[string]any{"name": strings.TrimSpace(in.Name), "note": in.Note})
	_ = a.audit(a.db, u.User.ID, "device.rename", d.ID)
	jsonOut(w, 200, a.deviceView(d))
}

func (a *App) assignDevice(w http.ResponseWriter, r *http.Request, u *userRequest) {
	var in struct {
		AgentID string `json:"agent_id"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	a.writes.Lock()
	defer a.writes.Unlock()
	var d Device
	if a.db.First(&d, "id = ?", r.PathValue("id")).Error != nil {
		fail(w, 404, "NOT_FOUND", "设备不存在")
		return
	}
	if in.AgentID != "" {
		var agent User
		if a.db.Where("id = ? AND role = ? AND enabled = ?", in.AgentID, "agent", true).First(&agent).Error != nil {
			fail(w, 400, "INVALID_AGENT", "请选择正常启用的代理")
			return
		}
	}
	if d.AgentID == in.AgentID {
		jsonOut(w, 200, a.deviceView(d))
		return
	}
	now := time.Now().UTC()
	err := a.db.Transaction(func(tx *gorm.DB) error {
		var live []ImageSession
		tx.Where("device_id = ? AND desired_running = ?", d.ID, true).Find(&live)
		for _, s := range live {
			if _, e := a.stopImageSession(tx, &s, u.User.ID); e != nil {
				return e
			}
		}
		if e := tx.Model(&Command{}).Where("device_id = ? AND type <> ? AND completed_at IS NULL", d.ID, "STOP_IMAGE_SESSION").Updates(map[string]any{"status": "canceled", "completed_at": now}).Error; e != nil {
			return e
		}
		if d.AssignmentID != "" {
			if e := tx.Model(&Assignment{}).Where("id = ?", d.AssignmentID).Update("ended_at", now).Error; e != nil {
				return e
			}
		}
		d.AssignmentVersion++
		d.AgentID = in.AgentID
		d.AssignmentID = ""
		d.LatestImageID = ""
		d.ActiveSessionID = ""
		d.LastImageAt = nil
		d.LastLocationAt = nil
		if in.AgentID != "" {
			assignment := Assignment{ID: newID("asg_"), DeviceID: d.ID, AgentID: in.AgentID, Version: d.AssignmentVersion, StartedAt: now}
			if e := tx.Create(&assignment).Error; e != nil {
				return e
			}
			d.AssignmentID = assignment.ID
		}
		if e := tx.Save(&d).Error; e != nil {
			return e
		}
		return a.audit(tx, u.User.ID, "device.assign", d.ID)
	})
	if err != nil {
		fail(w, 500, "DATABASE_ERROR", "设备分配失败")
		return
	}
	a.hub.Sync(d.ID)
	jsonOut(w, 200, a.deviceView(d))
}
func (a *App) enableDevice(w http.ResponseWriter, r *http.Request, u *userRequest) {
	var in struct{ Enabled bool }
	if !readJSON(w, r, &in) {
		return
	}
	a.writes.Lock()
	defer a.writes.Unlock()
	var d Device
	if a.db.First(&d, "id = ?", r.PathValue("id")).Error != nil {
		fail(w, 404, "NOT_FOUND", "设备不存在")
		return
	}
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if !in.Enabled {
			var sessions []ImageSession
			tx.Where("device_id = ? AND desired_running = ?", d.ID, true).Find(&sessions)
			for _, s := range sessions {
				if _, e := a.stopImageSession(tx, &s, u.User.ID); e != nil {
					return e
				}
			}
		}
		if e := tx.Model(&d).Update("enabled", in.Enabled).Error; e != nil {
			return e
		}
		return a.audit(tx, u.User.ID, "device.enabled", d.ID)
	})
	if err != nil {
		fail(w, 500, "DATABASE_ERROR", "设备状态修改失败")
		return
	}
	if !in.Enabled {
		a.hub.Kick(d.ID, "device.disabled")
	}
	jsonOut(w, 200, map[string]bool{"enabled": in.Enabled})
}
func (a *App) auditLogs(w http.ResponseWriter, r *http.Request, u *userRequest) {
	var list []AuditLog
	a.db.Order("created_at desc").Limit(100).Find(&list)
	type row struct {
		AuditLog
		ActorName string `json:"actor_name"`
	}
	items := make([]row, 0, len(list))
	for _, v := range list {
		var actor User
		a.db.First(&actor, "id = ?", v.ActorID)
		items = append(items, row{v, actor.DisplayName})
	}
	jsonOut(w, 200, map[string]any{"items": items})
}
