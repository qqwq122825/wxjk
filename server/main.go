package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

//go:embed public
var webFiles embed.FS

type Config struct {
	Listen, DataDir, DatabaseURL, SetupToken, EnrollmentToken, TLSCert, TLSKey string
	SecureCookies                                                              bool
}

func envConfig() Config {
	c := Config{Listen: os.Getenv("LISTEN_ADDR"), DataDir: os.Getenv("DATA_DIR"), DatabaseURL: os.Getenv("DATABASE_URL"), SetupToken: os.Getenv("SETUP_TOKEN"), EnrollmentToken: os.Getenv("ENROLLMENT_TOKEN"), TLSCert: os.Getenv("TLS_CERT"), TLSKey: os.Getenv("TLS_KEY")}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	if c.DataDir == "" {
		c.DataDir = ".data"
	}
	c.SecureCookies = os.Getenv("COOKIE_SECURE") == "true" || c.TLSCert != ""
	return c
}

type App struct {
	db         *gorm.DB
	cfg        Config
	tokenKey   []byte
	writes     sync.Mutex
	hub        *Hub
	limiter    *Limiter
	imageSlots chan struct{}
}

func NewApp(c Config) (*App, error) {
	if err := os.MkdirAll(filepath.Join(c.DataDir, "images"), 0700); err != nil {
		return nil, err
	}
	var dialector gorm.Dialector
	if c.DatabaseURL != "" {
		dialector = postgres.Open(c.DatabaseURL)
	} else {
		dialector = sqlite.Open(filepath.Join(c.DataDir, "monitor.db") + "?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000")
	}
	db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), NowFunc: func() time.Time { return time.Now().UTC() }})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	if c.DatabaseURL == "" {
		sqlDB.SetMaxOpenConns(1)
	} else {
		sqlDB.SetMaxOpenConns(10)
	}
	if err = db.AutoMigrate(&User{}, &LoginSession{}, &Device{}, &Assignment{}, &Command{}, &ImageSession{}, &ImageReport{}, &LocationReport{}, &AuditLog{}); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(c.DataDir, "device-token.key")
	key, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		err = os.WriteFile(keyPath, key, 0600)
	}
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("device-token.key must contain 32 bytes")
	}
	a := &App{db: db, cfg: c, tokenKey: key, limiter: NewLimiter(), imageSlots: make(chan struct{}, 2)}
	a.hub = NewHub(a)
	return a, nil
}

func main() {
	c := envConfig()
	a, err := NewApp(c)
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go a.runWorker(ctx)
	server := &http.Server{Addr: c.Listen, Handler: a.Routes(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		a.hub.Close()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	scheme := "http"
	if c.TLSCert != "" {
		scheme = "https"
	}
	log.Printf("JK Monitor: %s://%s (data: %s)", scheme, c.Listen, c.DataDir)
	if c.TLSCert != "" {
		err = server.ListenAndServeTLS(c.TLSCert, c.TLSKey)
	} else {
		err = server.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	if errors.Is(err, http.ErrServerClosed) {
		<-shutdownDone
	}
	if database, e := a.db.DB(); e == nil {
		_ = database.Close()
	}
}

func (a *App) Routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) { jsonOut(w, 200, map[string]any{"status": "ok"}) })
	m.HandleFunc("GET /api/v1/auth/bootstrap", a.bootstrap)
	m.HandleFunc("POST /api/v1/auth/setup", a.setup)
	m.HandleFunc("POST /api/v1/auth/login", a.login)
	m.HandleFunc("GET /api/v1/auth/me", a.userRoute(a.me, false))
	m.HandleFunc("POST /api/v1/auth/logout", a.userRoute(a.logout, false))
	m.HandleFunc("POST /api/v1/auth/password", a.userRoute(a.changePassword, false))
	m.HandleFunc("GET /api/v1/dashboard", a.userRoute(a.dashboard, true))
	m.HandleFunc("GET /api/v1/devices", a.userRoute(a.devices, true))
	m.HandleFunc("GET /api/v1/devices/{id}", a.userRoute(a.deviceDetail, true))
	m.HandleFunc("PATCH /api/v1/devices/{id}", a.userRoute(a.renameDevice, true))
	m.HandleFunc("POST /api/v1/devices/{id}/commands", a.userRoute(a.createCommand, true))
	m.HandleFunc("GET /api/v1/devices/{id}/images", a.userRoute(a.images, true))
	m.HandleFunc("GET /api/v1/devices/{id}/locations", a.userRoute(a.locations, true))
	m.HandleFunc("GET /api/v1/commands", a.userRoute(a.commands, true))
	m.HandleFunc("GET /api/v1/images/{id}/file", a.userRoute(a.imageFile, true))
	m.HandleFunc("GET /api/v1/admin/agents", a.adminRoute(a.agents))
	m.HandleFunc("POST /api/v1/admin/agents", a.adminRoute(a.createAgent))
	m.HandleFunc("PATCH /api/v1/admin/agents/{id}", a.adminRoute(a.updateAgent))
	m.HandleFunc("POST /api/v1/admin/agents/{id}/password", a.adminRoute(a.resetAgentPassword))
	m.HandleFunc("POST /api/v1/admin/devices/{id}/assignment", a.adminRoute(a.assignDevice))
	m.HandleFunc("POST /api/v1/admin/devices/{id}/enabled", a.adminRoute(a.enableDevice))
	m.HandleFunc("GET /api/v1/admin/audit", a.adminRoute(a.auditLogs))
	m.HandleFunc("POST /api/v1/device/register", a.registerDevice)
	m.HandleFunc("GET /api/v1/device/ws", a.deviceRoute(a.deviceSocket))
	m.HandleFunc("POST /api/v1/device/heartbeat", a.deviceRoute(a.deviceHeartbeat))
	m.HandleFunc("POST /api/v1/device/images", a.deviceRoute(a.uploadImage))
	m.HandleFunc("POST /api/v1/device/locations", a.deviceRoute(a.uploadLocation))
	m.HandleFunc("POST /api/v1/device/commands/{id}/ack", a.deviceRoute(a.deviceCommandAck))
	m.HandleFunc("POST /api/v1/device/commands/{id}/progress", a.deviceRoute(a.deviceCommandProgress))
	m.HandleFunc("POST /api/v1/device/commands/{id}/result", a.deviceRoute(a.deviceCommandResult))
	public, _ := fs.Sub(webFiles, "public")
	m.Handle("GET /assets/", http.FileServer(http.FS(public)))
	m.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			fail(w, 404, "NOT_FOUND", "接口不存在")
			return
		}
		data, err := fs.ReadFile(public, "index.html")
		if err != nil {
			fail(w, 503, "FRONTEND_NOT_BUILT", "请先构建网页")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		defer func() {
			if x := recover(); x != nil {
				log.Printf("request panic: %v", x)
				fail(w, 500, "INTERNAL_ERROR", "服务器处理失败")
			}
		}()
		m.ServeHTTP(w, r)
	})
}

func jsonOut(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, code, message string) {
	jsonOut(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func readJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(value); err != nil {
		fail(w, 400, "INVALID_PAYLOAD", "请求内容不是有效 JSON")
		return false
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		fail(w, 400, "INVALID_PAYLOAD", "请求只能包含一个 JSON 对象")
		return false
	}
	return true
}
func newID(prefix string) string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return prefix + hex.EncodeToString(b)
}
func randomToken() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func hashSecret(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
func (a *App) deviceToken(id, registrationSecret string) string {
	mac := hmac.New(sha256.New, a.tokenKey)
	_, _ = fmt.Fprint(mac, id+":"+registrationSecret)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func ipOf(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
func (a *App) audit(tx *gorm.DB, actor, action, target string) error {
	return tx.Create(&AuditLog{ID: newID("audit_"), ActorID: actor, Action: action, TargetID: target, CreatedAt: time.Now().UTC()}).Error
}
func nullID(s string) any {
	if s == "" {
		return nil
	}
	return s
}

type rateBucket struct {
	count int
	reset time.Time
}
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]rateBucket
}

func NewLimiter() *Limiter { return &Limiter{buckets: map[string]rateBucket{}} }
func (l *Limiter) Allow(key string, limit int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b := l.buckets[key]
	if now.After(b.reset) {
		b = rateBucket{reset: now.Add(window)}
	}
	if b.count >= limit {
		return false
	}
	b.count++
	l.buckets[key] = b
	if len(l.buckets) > 10000 {
		for k, v := range l.buckets {
			if now.After(v.reset) {
				delete(l.buckets, k)
			}
		}
	}
	return true
}
