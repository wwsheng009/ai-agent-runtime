package mcpmount

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Proxy forwards one authorized request into the reverse tunnel. It is
// implemented by *tunnel.Server; keeping it an interface here means the auth and
// routing rules stay unit-testable without a live WebSocket.
type Proxy interface {
	ServeProxyMount(w http.ResponseWriter, r *http.Request, nodeID, path, mount string)
}

// Event is emitted for every mount request after the auth/routing decision.
// Hosts feed it into the audit log and Prometheus counters.
type Event struct {
	Slug       string
	NodeID     string
	TokenID    string
	Method     string
	Path       string
	Status     int
	Duration   time.Duration
	RemoteAddr string
	// Reason is a stable machine-readable code for rejected requests
	// (mount_not_found, token_invalid, token_scope, mount_disabled, ...).
	Reason string
}

// HandlerConfig wires the mount endpoint.
type HandlerConfig struct {
	Registry *Registry
	Tokens   *TokenStore
	Proxy    Proxy
	// NodeLive optionally answers whether nodeID still has a live tunnel
	// session. When set, a disconnected node is rejected with a JSON 502 before
	// the proxy is entered.
	NodeLive func(nodeID string) bool
	// DefaultConcurrency bounds in-flight requests per mount when the node does
	// not declare limits.max_concurrency (default 16).
	DefaultConcurrency int
	// OnEvent receives one Event per request; nil disables observation.
	OnEvent func(Event)
}

// Handler serves the public mount namespace.
type Handler struct {
	cfg HandlerConfig

	semMu sync.Mutex
	sem   map[string]chan struct{}
}

// NewHandler builds a mount handler. Registry and Tokens are required; a nil
// Proxy makes every authorized request fail closed with 502.
func NewHandler(cfg HandlerConfig) *Handler {
	if cfg.DefaultConcurrency <= 0 {
		cfg.DefaultConcurrency = 16
	}
	return &Handler{cfg: cfg, sem: make(map[string]chan struct{})}
}

// ServeMount authenticates and proxies one mount request. The router strips the
// namespace prefix (/m/{slug} or /{slug} in root style) and passes the remainder
// as localPath.
//
// Authentication precedes the registry lookup on purpose: an unauthenticated
// caller must not be able to tell a registered slug from an unknown one.
func (h *Handler) ServeMount(w http.ResponseWriter, r *http.Request, slug, localPath string) {
	started := time.Now()
	slug = strings.TrimSpace(slug)
	event := Event{
		Slug:       slug,
		Method:     r.Method,
		Path:       localPath,
		RemoteAddr: r.RemoteAddr,
	}
	finish := func(status int, reason string) {
		event.Status = status
		event.Reason = reason
		event.Duration = time.Since(started)
		if h.cfg.OnEvent != nil {
			h.cfg.OnEvent(event)
		}
	}
	deny := func(status int, reason, message string) {
		writeMountError(w, status, reason, message)
		finish(status, reason)
	}

	token, reason := h.authenticate(r)
	if reason != "" {
		w.Header().Set("WWW-Authenticate", `Bearer realm="aicli-gateway-mount"`)
		deny(http.StatusUnauthorized, reason, "invalid mount token")
		return
	}
	event.TokenID = token.ID

	record, ok := h.cfg.Registry.Get(slug)
	if !ok {
		deny(http.StatusNotFound, "mount_not_found", "mount not found")
		return
	}
	event.NodeID = record.NodeID
	if record.Disabled {
		deny(http.StatusForbidden, "mount_disabled", "mount is disabled")
		return
	}
	if !token.Allows(slug) {
		deny(http.StatusForbidden, "token_scope", "token is not scoped to this mount")
		return
	}
	if record.Stale || (h.cfg.NodeLive != nil && !h.cfg.NodeLive(record.NodeID)) {
		w.Header().Set("Retry-After", "5")
		deny(http.StatusBadGateway, "node_offline", "node is offline; retry shortly")
		return
	}

	path, err := NormalizeLocalPath(localPath)
	if err != nil {
		deny(http.StatusForbidden, "bad_local_path", err.Error())
		return
	}
	event.Path = path

	release, ok := h.acquire(slug, mountConcurrency(record, h.cfg.DefaultConcurrency))
	if !ok {
		w.Header().Set("Retry-After", "1")
		deny(http.StatusTooManyRequests, "mount_busy", "too many concurrent requests for this mount")
		return
	}
	defer release()

	if h.cfg.Proxy == nil {
		deny(http.StatusBadGateway, "proxy_unavailable", "mount proxy is not configured")
		return
	}
	recorder := &statusRecorder{ResponseWriter: w}
	h.cfg.Proxy.ServeProxyMount(recorder, r, record.NodeID, path, slug)
	status := recorder.status
	if status == 0 {
		status = http.StatusOK
	}
	finish(status, "")
}

// authenticate returns the token or a stable failure reason.
func (h *Handler) authenticate(r *http.Request) (Token, string) {
	if h.cfg.Tokens == nil {
		return Token{}, "token_store_unavailable"
	}
	presented := bearerToken(r)
	if presented == "" {
		return Token{}, "token_missing"
	}
	token, ok := h.cfg.Tokens.Authenticate(presented)
	if !ok {
		return Token{}, "token_invalid"
	}
	return token, ""
}

func mountConcurrency(record *Record, fallback int) int {
	if record != nil && record.Limits.MaxConcurrency > 0 {
		return record.Limits.MaxConcurrency
	}
	return fallback
}

// acquire takes a per-mount slot without blocking. A full mount returns 429
// rather than queueing unbounded work behind the tunnel.
func (h *Handler) acquire(slug string, limit int) (func(), bool) {
	if limit <= 0 {
		return func() {}, true
	}
	h.semMu.Lock()
	slots := h.sem[slug]
	if slots == nil || cap(slots) != limit {
		slots = make(chan struct{}, limit)
		h.sem[slug] = slots
	}
	h.semMu.Unlock()

	select {
	case slots <- struct{}{}:
		return func() { <-slots }, true
	default:
		return nil, false
	}
}

func writeMountError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error":   code,
		"message": message,
	})
}

func bearerToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(header) > 7 && strings.EqualFold(header[:7], "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return ""
}

// statusRecorder captures the proxied status for auditing without changing
// streaming behavior (the tunnel flushes as data arrives).
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(data)
}

func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap lets http.ResponseController reach the original writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
