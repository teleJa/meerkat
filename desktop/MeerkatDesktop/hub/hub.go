package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"tailscale.com/tsnet"
)

const ProtocolVersion = "1"

type Config struct {
	Host                string
	Port                int
	DataDir             string
	Version             string
	AdminToken          string
	TaskPoolBotDeviceID string
	TaskPoolChiefBotID  string
}

type Hub struct {
	cfg           Config
	db            *sql.DB
	adminToken    string
	startedAt     time.Time
	server        *http.Server
	listener      net.Listener
	connMu        sync.Mutex
	connections   map[string]*wsClient
	harnessMu     sync.Mutex
	harnessHTTP   map[string]*harnessProxy
	harnessClosed bool
	harnessLog    *harnessProxyLog
	turnWatcher   *turnWatcher
	poolCancel    context.CancelFunc
	poolDone      chan struct{}
	tsnetMu       sync.Mutex
	tsnetServer   *tsnet.Server
	tsnetCancel   context.CancelFunc
	tsnetState    tsnetStatus
}

type harnessProxy struct {
	listener          net.Listener
	server            *http.Server
	port              int
	target            string
	login             harnessLoginCache
	closed            atomic.Bool
	pathTransport     *http.Transport
	upstreamTransport *http.Transport
}

type wsClient struct {
	hub      *Hub
	conn     *websocket.Conn
	deviceID string
	writeMu  sync.Mutex
}

func tokenHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func randomHex(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}

func newID(prefix string) string { return prefix + randomHex(4) }

func loadAdminToken(dataDir, override string) (string, error) {
	if value := strings.TrimSpace(override); value != "" {
		return value, nil
	}
	file := filepath.Join(dataDir, "admin-token")
	if data, err := os.ReadFile(file); err == nil {
		value := strings.TrimSpace(string(data))
		if len(value) >= 16 {
			return value, nil
		}
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", err
	}
	value := randomHex(24)
	if err := os.WriteFile(file, []byte(value+"\n"), 0o600); err != nil {
		return "", err
	}
	return value, nil
}

func New(cfg Config) (*Hub, error) {
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	if cfg.Port == 0 {
		cfg.Port = 8787
	}
	if cfg.DataDir == "" {
		cfg.DataDir = "data"
	}
	if cfg.Version == "" {
		cfg.Version = "0.1.0"
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	db, err := openDB(filepath.Join(cfg.DataDir, "meerkat.db"))
	if err != nil {
		return nil, err
	}
	admin, err := loadAdminToken(cfg.DataDir, cfg.AdminToken)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	h := &Hub{
		cfg:         cfg,
		db:          db,
		adminToken:  admin,
		startedAt:   time.Now(),
		connections: map[string]*wsClient{},
		harnessHTTP: map[string]*harnessProxy{},
		harnessLog:  newHarnessProxyLog(cfg.DataDir),
	}
	h.turnWatcher = newTurnWatcher(h)
	mux := http.NewServeMux()
	h.registerHTTP(mux)
	h.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	return h, nil
}

func (h *Hub) AdminToken() string { return h.adminToken }

func (h *Hub) URL() string {
	host := h.cfg.Host
	if host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, fmt.Sprintf("%d", h.cfg.Port))
}

func (h *Hub) Start() error {
	listener, err := net.Listen("tcp", net.JoinHostPort(h.cfg.Host, fmt.Sprintf("%d", h.cfg.Port)))
	if err != nil {
		return err
	}
	h.listener = listener
	if strings.TrimSpace(h.cfg.TaskPoolBotDeviceID) != "" {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		h.poolCancel, h.poolDone = cancel, done
		go func() {
			defer close(done)
			h.runPoolDeliveries(ctx)
		}()
	}
	go func() {
		if err := h.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("go hub server stopped: %v", err)
		}
	}()
	return nil
}

func (h *Hub) Close() error {
	h.StopTurnWatcher()
	if h.poolCancel != nil {
		h.poolCancel()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = h.server.Shutdown(ctx)
	h.connMu.Lock()
	for _, client := range h.connections {
		_ = client.conn.Close()
	}
	h.connections = map[string]*wsClient{}
	h.connMu.Unlock()
	h.harnessMu.Lock()
	h.harnessClosed = true
	h.harnessMu.Unlock()
	h.resetHarnessProxies()
	if h.poolDone != nil {
		<-h.poolDone
	}
	h.harnessLog.close()
	return h.db.Close()
}

func (h *Hub) sendToDevice(deviceID string, message any) bool {
	h.connMu.Lock()
	client := h.connections[deviceID]
	h.connMu.Unlock()
	if client == nil {
		return false
	}
	return client.send(message)
}

func (c *wsClient) send(value any) bool {
	data, err := json.Marshal(value)
	if err != nil {
		return false
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return false
	}
	return true
}

func (h *Hub) logEvent(eventType string, deviceID *string, detail *string) {
	_, _ = h.db.Exec("INSERT INTO events (at, device_id, type, detail) VALUES (?, ?, ?, ?)",
		nowISO(time.Now()), deviceID, eventType, detail)
}

func (h *Hub) requireAuth(w http.ResponseWriter, r *http.Request, mutation bool) (*AuthInfo, bool) {
	auth, authErr := h.authenticateWithError(r)
	if auth == nil {
		switch {
		case errors.Is(authErr, ErrMobileCredentialExpired):
			writeError(w, http.StatusUnauthorized, "mobile_credential_expired", "移动设备凭证已过期，请重新配对")
		case errors.Is(authErr, ErrMobileCredentialInvalid):
			writeError(w, http.StatusUnauthorized, "mobile_credential_invalid", "移动设备凭证无效，请重新配对")
		default:
			writeError(w, http.StatusUnauthorized, "unauthorized", "缺少或错误的管理凭据")
		}
		return nil, false
	}
	if mutation && auth.Via == "mobile" {
		writeError(w, http.StatusForbidden, "mobile_read_only", "移动设备凭证只有只读权限")
		return nil, false
	}
	if mutation && auth.Via == "session" && r.Header.Get("x-meerkat-request") != "1" {
		writeError(w, http.StatusForbidden, "csrf_required", "缺少 x-meerkat-request 请求头")
		return nil, false
	}
	return auth, true
}

func (h *Hub) authenticate(r *http.Request) *AuthInfo {
	auth, _ := h.authenticateWithError(r)
	return auth
}

func (h *Hub) authenticateWithError(r *http.Request) (*AuthInfo, error) {
	header := r.Header.Get("Authorization")
	if strings.HasPrefix(header, "Bearer ") {
		token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		if strings.HasPrefix(token, mobileTokenPrefix) {
			return h.authenticateMobile(token, time.Now())
		}
		if token != "" && subtle.ConstantTimeCompare([]byte(tokenHash(token)), []byte(tokenHash(h.adminToken))) == 1 {
			return &AuthInfo{Via: "bearer"}, nil
		}
	}
	cookie, err := r.Cookie("meerkat_session")
	if err != nil || cookie.Value == "" {
		return nil, nil
	}
	session, err := h.getWebSession(cookie.Value, time.Now())
	if err != nil || session == nil {
		return nil, nil
	}
	return &AuthInfo{Via: "session", SessionID: cookie.Value, ExpiresAt: session.ExpiresAt}, nil
}

type AuthInfo struct {
	Via          string
	SessionID    string
	ExpiresAt    string
	CredentialID string
	Scopes       []string
}

func (a *AuthInfo) HasScope(scope string) bool {
	if a == nil || scope == "" {
		return true
	}
	for _, granted := range a.Scopes {
		if granted == scope {
			return true
		}
	}
	return false
}
