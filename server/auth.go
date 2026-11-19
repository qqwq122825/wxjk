package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
	"gorm.io/gorm"
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,32}$`)
var passwordSlots = make(chan struct{}, 2)

func passwordHash(password string) string {
	passwordSlots <- struct{}{}
	defer func() { <-passwordSlots }()
	salt := make([]byte, 16)
	if _, e := rand.Read(salt); e != nil {
		panic(e)
	}
	key := argon2.IDKey([]byte(password), salt, 2, 19456, 1, 32)
	return "$argon2id$v=19$m=19456,t=2,p=1$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key)
}
func validPassword(encoded, password string) bool {
	passwordSlots <- struct{}{}
	defer func() { <-passwordSlots }()
	p := strings.Split(encoded, "$")
	salt := make([]byte, 16)
	expected := make([]byte, 32)
	valid := len(p) == 6 && p[3] == "m=19456,t=2,p=1"
	if valid {
		var e error
		salt, e = base64.RawStdEncoding.DecodeString(p[4])
		valid = e == nil && len(salt) == 16
		expected, e = base64.RawStdEncoding.DecodeString(p[5])
		valid = valid && e == nil && len(expected) == 32
	}
	actual := argon2.IDKey([]byte(password), salt, 2, 19456, 1, 32)
	return subtle.ConstantTimeCompare(actual, expected) == 1 && valid
}
func passwordOK(s string) bool { return len(s) >= 10 && len(s) <= 128 }

type userRequest struct {
	User    User
	Session LoginSession
}
type userHandler func(http.ResponseWriter, *http.Request, *userRequest)

func (a *App) authenticate(r *http.Request) (*userRequest, error) {
	cookie, err := r.Cookie("jk_session")
	if err != nil {
		return nil, err
	}
	var s LoginSession
	if err = a.db.Where("token_hash = ? AND expires_at > ?", hashSecret(cookie.Value), time.Now().UTC()).First(&s).Error; err != nil {
		return nil, err
	}
	var u User
	if err = a.db.Where("id = ? AND enabled = ?", s.UserID, true).First(&u).Error; err != nil {
		return nil, err
	}
	return &userRequest{User: u, Session: s}, nil
}
func (a *App) userRoute(handler userHandler, requirePassword bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := a.authenticate(r)
		if err != nil {
			fail(w, 401, "LOGIN_REQUIRED", "请先登录")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(u.Session.CSRF)) != 1 {
				fail(w, 403, "CSRF_REJECTED", "页面已失效，请刷新后重试")
				return
			}
		}
		if requirePassword && u.User.MustChangePassword {
			fail(w, 403, "PASSWORD_CHANGE_REQUIRED", "请先修改初始密码")
			return
		}
		handler(w, r, u)
	}
}
func (a *App) adminRoute(handler userHandler) http.HandlerFunc {
	return a.userRoute(func(w http.ResponseWriter, r *http.Request, u *userRequest) {
		if u.User.Role != "admin" {
			fail(w, 403, "ADMIN_REQUIRED", "需要总台权限")
			return
		}
		handler(w, r, u)
	}, true)
}
func localSetup(r *http.Request) bool {
	ip := net.ParseIP(ipOf(r))
	host := r.Host
	if h, _, e := net.SplitHostPort(host); e == nil {
		host = h
	}
	uip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback() && (host == "localhost" || (uip != nil && uip.IsLoopback())) && r.Header.Get("X-Forwarded-For") == "" && r.Header.Get("Forwarded") == ""
}
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}
func (a *App) bootstrap(w http.ResponseWriter, r *http.Request) {
	var count int64
	a.db.Model(&User{}).Count(&count)
	jsonOut(w, 200, map[string]any{"needs_setup": count == 0, "requires_setup_token": !localSetup(r) || a.cfg.SetupToken != ""})
}
func (a *App) setup(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		fail(w, 403, "ORIGIN_REJECTED", "请求来源无效")
		return
	}
	var in struct {
		Username   string `json:"username"`
		Password   string `json:"password"`
		SetupToken string `json:"setup_token"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if (!localSetup(r) || a.cfg.SetupToken != "") && (a.cfg.SetupToken == "" || subtle.ConstantTimeCompare([]byte(in.SetupToken), []byte(a.cfg.SetupToken)) != 1) {
		fail(w, 403, "SETUP_TOKEN_REQUIRED", "请输入部署时设置的初始化口令")
		return
	}
	if !usernamePattern.MatchString(in.Username) || !passwordOK(in.Password) {
		fail(w, 400, "INVALID_ACCOUNT", "账号需为 3 至 32 位字母、数字或 ._-，密码需为 10 至 128 位")
		return
	}
	a.writes.Lock()
	defer a.writes.Unlock()
	var count int64
	if err := a.db.Model(&User{}).Count(&count).Error; err != nil {
		fail(w, 500, "DATABASE_ERROR", "无法初始化")
		return
	}
	if count != 0 {
		fail(w, 409, "ALREADY_INITIALIZED", "总台已经初始化")
		return
	}
	u := User{ID: newID("usr_"), Username: in.Username, DisplayName: "总台管理员", Role: "admin", Enabled: true, PasswordHash: passwordHash(in.Password)}
	if err := a.db.Create(&u).Error; err != nil {
		fail(w, 500, "DATABASE_ERROR", "账号创建失败")
		return
	}
	a.startLogin(w, r, u)
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		fail(w, 403, "ORIGIN_REJECTED", "请求来源无效")
		return
	}
	if !a.limiter.Allow("login:"+ipOf(r), 15, 5*time.Minute) {
		w.Header().Set("Retry-After", "300")
		fail(w, 429, "RATE_LIMITED", "登录尝试较多，请稍后重试")
		return
	}
	var in struct{ Username, Password string }
	if !readJSON(w, r, &in) {
		return
	}
	if len(in.Password) > 128 || len(in.Username) > 40 {
		fail(w, 401, "INVALID_LOGIN", "账号或密码不正确")
		return
	}
	var u User
	found := a.db.Where("username = ?", in.Username).First(&u).Error == nil
	ok := validPassword(u.PasswordHash, in.Password)
	if !found || !ok || !u.Enabled {
		fail(w, 401, "INVALID_LOGIN", "账号或密码不正确，或账号已被禁用")
		return
	}
	a.startLogin(w, r, u)
}
func (a *App) startLogin(w http.ResponseWriter, r *http.Request, u User) {
	token := randomToken()
	s := LoginSession{TokenHash: hashSecret(token), UserID: u.ID, CSRF: randomToken(), ExpiresAt: time.Now().UTC().Add(12 * time.Hour)}
	if err := a.db.Create(&s).Error; err != nil {
		fail(w, 500, "DATABASE_ERROR", "登录失败")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "jk_session", Value: token, Path: "/", HttpOnly: true, Secure: a.cfg.SecureCookies, SameSite: http.SameSiteStrictMode, Expires: s.ExpiresAt})
	jsonOut(w, 200, map[string]any{"user": u, "csrf_token": s.CSRF})
}
func (a *App) me(w http.ResponseWriter, r *http.Request, u *userRequest) {
	jsonOut(w, 200, map[string]any{"user": u.User, "csrf_token": u.Session.CSRF})
}
func (a *App) logout(w http.ResponseWriter, r *http.Request, u *userRequest) {
	a.db.Delete(&LoginSession{}, "token_hash = ?", u.Session.TokenHash)
	http.SetCookie(w, &http.Cookie{Name: "jk_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.cfg.SecureCookies, SameSite: http.SameSiteStrictMode})
	jsonOut(w, 200, map[string]bool{"ok": true})
}
func (a *App) changePassword(w http.ResponseWriter, r *http.Request, u *userRequest) {
	var in struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !passwordOK(in.NewPassword) || len(in.CurrentPassword) > 128 || !validPassword(u.User.PasswordHash, in.CurrentPassword) {
		fail(w, 400, "INVALID_PASSWORD", "当前密码不正确，或新密码不足 10 位")
		return
	}
	hash := passwordHash(in.NewPassword)
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if e := tx.Model(&User{}).Where("id = ?", u.User.ID).Updates(map[string]any{"password_hash": hash, "must_change_password": false}).Error; e != nil {
			return e
		}
		return tx.Where("user_id = ? AND token_hash <> ?", u.User.ID, u.Session.TokenHash).Delete(&LoginSession{}).Error
	})
	if err != nil {
		fail(w, 500, "DATABASE_ERROR", "密码修改失败")
		return
	}
	u.User.MustChangePassword = false
	jsonOut(w, 200, map[string]any{"user": u.User})
}

func (a *App) agents(w http.ResponseWriter, r *http.Request, u *userRequest) {
	var users []User
	a.db.Where("role = ?", "agent").Order("created_at desc").Find(&users)
	type row struct {
		User
		Devices int64 `json:"devices"`
	}
	rows := make([]row, 0, len(users))
	for _, v := range users {
		var count int64
		a.db.Model(&Device{}).Where("agent_id = ?", v.ID).Count(&count)
		rows = append(rows, row{v, count})
	}
	jsonOut(w, 200, map[string]any{"items": rows})
}
func (a *App) createAgent(w http.ResponseWriter, r *http.Request, u *userRequest) {
	var in struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		Password    string `json:"password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if !usernamePattern.MatchString(in.Username) || !passwordOK(in.Password) || len(in.DisplayName) > 100 {
		fail(w, 400, "INVALID_ACCOUNT", "请填写有效账号和至少 10 位的初始密码")
		return
	}
	agent := User{ID: newID("usr_"), Username: in.Username, DisplayName: strings.TrimSpace(in.DisplayName), PasswordHash: passwordHash(in.Password), Role: "agent", Enabled: true, MustChangePassword: true}
	if agent.DisplayName == "" {
		agent.DisplayName = in.Username
	}
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if e := tx.Create(&agent).Error; e != nil {
			return e
		}
		return a.audit(tx, u.User.ID, "agent.create", agent.ID)
	})
	if err != nil {
		fail(w, 409, "ACCOUNT_EXISTS", "账号已存在或创建失败")
		return
	}
	jsonOut(w, 201, agent)
}
func (a *App) updateAgent(w http.ResponseWriter, r *http.Request, u *userRequest) {
	var in struct {
		DisplayName *string `json:"display_name"`
		Enabled     *bool   `json:"enabled"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	a.writes.Lock()
	defer a.writes.Unlock()
	var target User
	if a.db.Where("id = ? AND role = ?", r.PathValue("id"), "agent").First(&target).Error != nil {
		fail(w, 404, "NOT_FOUND", "代理不存在")
		return
	}
	updates := map[string]any{}
	if in.DisplayName != nil {
		if len(*in.DisplayName) > 100 {
			fail(w, 400, "INVALID_NAME", "名称过长")
			return
		}
		updates["display_name"] = *in.DisplayName
	}
	if in.Enabled != nil {
		updates["enabled"] = *in.Enabled
	}
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if e := tx.Model(&target).Updates(updates).Error; e != nil {
			return e
		}
		if in.Enabled != nil && !*in.Enabled {
			if e := tx.Where("user_id = ?", target.ID).Delete(&LoginSession{}).Error; e != nil {
				return e
			}
			var sessions []ImageSession
			tx.Where("actor_id = ? AND desired_running = ?", target.ID, true).Find(&sessions)
			for _, s := range sessions {
				if _, e := a.stopImageSession(tx, &s, u.User.ID); e != nil {
					return e
				}
			}
			tx.Model(&Command{}).Where("actor_id = ? AND type <> ? AND completed_at IS NULL", target.ID, "STOP_IMAGE_SESSION").Updates(map[string]any{"status": "canceled", "completed_at": time.Now().UTC()})
		}
		return a.audit(tx, u.User.ID, "agent.update", target.ID)
	})
	if err != nil {
		fail(w, 500, "DATABASE_ERROR", "修改失败")
		return
	}
	a.hub.SyncAll()
	jsonOut(w, 200, target)
}
func (a *App) resetAgentPassword(w http.ResponseWriter, r *http.Request, u *userRequest) {
	var in struct{ Password string }
	if !readJSON(w, r, &in) {
		return
	}
	if !passwordOK(in.Password) {
		fail(w, 400, "INVALID_PASSWORD", "初始密码至少 10 位")
		return
	}
	var target User
	if a.db.Where("id = ? AND role = ?", r.PathValue("id"), "agent").First(&target).Error != nil {
		fail(w, 404, "NOT_FOUND", "代理不存在")
		return
	}
	hash := passwordHash(in.Password)
	err := a.db.Transaction(func(tx *gorm.DB) error {
		if e := tx.Model(&target).Updates(map[string]any{"password_hash": hash, "must_change_password": true}).Error; e != nil {
			return e
		}
		if e := tx.Where("user_id = ?", target.ID).Delete(&LoginSession{}).Error; e != nil {
			return e
		}
		return a.audit(tx, u.User.ID, "agent.password_reset", target.ID)
	})
	if err != nil {
		fail(w, 500, "DATABASE_ERROR", fmt.Sprint("重置失败"))
		return
	}
	jsonOut(w, 200, map[string]bool{"ok": true})
}
