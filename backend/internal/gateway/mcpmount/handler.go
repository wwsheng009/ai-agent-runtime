package mcpmount

import (
	"bytes"
	"encoding/json"
	"io"
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
	// UserAgent, RequestBytes and ResponseBytes describe the proxied HTTP
	// exchange for the console call log. RequestBytes is best-effort: it is
	// only captured when the request advertises a Content-Length under the
	// inspection cap, otherwise it reflects the declared length when known.
	UserAgent     string
	RequestBytes  int64
	ResponseBytes int64
	// Command is the MCP JSON-RPC method parsed from the request body, e.g.
	// "tools/call:read_file". Empty when the body is not an inspectable
	// JSON-RPC call (GET, health probe, non-JSON payload, ...).
	Command string
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
	// DefaultStreams bounds long-lived GET SSE streams per mount when the node
	// does not declare limits.max_streams (default 64). Streams are session
	// state, not work, so they have their own budget.
	DefaultStreams int
	// ClientIP optionally resolves the effective client IP. It lets the caller
	// honor X-Forwarded-For / X-Real-IP only from trusted proxies (the same
	// policy the rate limiter uses) so the call log shows the real caller, not
	// a local reverse proxy. nil keeps the raw socket peer.
	ClientIP func(*http.Request) string
	// OnEvent receives one Event per request; nil disables observation.
	OnEvent func(Event)
}

// Handler serves the public mount namespace.
type Handler struct {
	cfg HandlerConfig

	semMu sync.Mutex
	sem   map[string]chan struct{}

	streamMu  sync.Mutex
	streamSem map[string]chan struct{}
}

// NewHandler builds a mount handler. Registry and Tokens are required; a nil
// Proxy makes every authorized request fail closed with 502.
func NewHandler(cfg HandlerConfig) *Handler {
	if cfg.DefaultConcurrency <= 0 {
		cfg.DefaultConcurrency = 16
	}
	if cfg.DefaultStreams <= 0 {
		cfg.DefaultStreams = 64
	}
	return &Handler{
		cfg:       cfg,
		sem:       make(map[string]chan struct{}),
		streamSem: make(map[string]chan struct{}),
	}
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
	remoteAddr := r.RemoteAddr
	if h.cfg.ClientIP != nil {
		if ip := strings.TrimSpace(h.cfg.ClientIP(r)); ip != "" {
			remoteAddr = ip
		}
	}
	event := Event{
		Slug:       slug,
		Method:     r.Method,
		Path:       localPath,
		RemoteAddr: remoteAddr,
	}
	event.UserAgent = r.UserAgent()
	// Capture the request size and the MCP command from the body before the
	// proxy reads it. Only inspectable (Content-Length advertised, under the
	// cap) JSON bodies are buffered; everything else is forwarded untouched.
	if r.ContentLength >= 0 && r.ContentLength <= maxMountLogBody {
		if buf, err := io.ReadAll(io.LimitReader(r.Body, maxMountLogBody)); err == nil {
			event.RequestBytes = int64(len(buf))
			event.Command = extractMCPCommand(buf)
			r.Body = io.NopCloser(bytes.NewReader(buf))
			r.ContentLength = int64(len(buf))
		}
	} else if r.ContentLength > maxMountLogBody {
		event.RequestBytes = r.ContentLength
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

	switch r.Method {
	case http.MethodDelete:
		// DELETE closes the MCP session. It must never compete with the work
		// or stream slots it is trying to release, otherwise a saturated mount
		// can never recover.
	case http.MethodGet:
		release, ok := h.acquireStream(slug, mountStreams(record, h.cfg.DefaultStreams))
		if !ok {
			w.Header().Set("Retry-After", "1")
			deny(http.StatusTooManyRequests, "mount_stream_busy", "too many concurrent streams for this mount")
			return
		}
		defer release()
	case http.MethodPost:
		release, ok := h.acquire(slug, mountConcurrency(record, h.cfg.DefaultConcurrency))
		if !ok {
			w.Header().Set("Retry-After", "1")
			deny(http.StatusTooManyRequests, "mount_busy", "too many concurrent requests for this mount")
			return
		}
		defer release()
	default:
		// Unknown methods are cheap and the MCP handler rejects them with 405;
		// do not let them consume a work slot.
	}

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
	event.ResponseBytes = recorder.written
	reason = ""
	if r.Method == http.MethodGet && status == http.StatusMethodNotAllowed {
		// MCP Streamable HTTP §2.2.3: GET opens the optional standalone SSE
		// stream; 405 means the server does not offer it. The Go SDK answers a
		// stateless GET with 405 + Allow: POST, so this is expected negotiation,
		// not a failure. Classify it so the console/audit do not cry wolf.
		reason = "sse_unavailable"
	}
	finish(status, reason)
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

func mountStreams(record *Record, fallback int) int {
	if record != nil && record.Limits.MaxStreams > 0 {
		return record.Limits.MaxStreams
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

// acquireStream takes a per-mount stream slot without blocking. GET SSE is
// long-lived session state; it has a separate budget from tool-call work.
func (h *Handler) acquireStream(slug string, limit int) (func(), bool) {
	if limit <= 0 {
		return func() {}, true
	}
	h.streamMu.Lock()
	slots := h.streamSem[slug]
	if slots == nil || cap(slots) != limit {
		slots = make(chan struct{}, limit)
		h.streamSem[slug] = slots
	}
	h.streamMu.Unlock()

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

// maxMountLogBody caps how large a request body the gateway buffers to extract
// the MCP command for the call log. Bodies larger than this (or with no declared
// Content-Length) are forwarded untouched and only have a size recorded when one
// is advertised.
const maxMountLogBody = 1 << 20

// extractMCPCommand returns a human-readable label for an MCP JSON-RPC payload,
// e.g. "tools/call:read_file" or "resources/read". A batch request is rendered
// as a comma-joined list of its methods. Non-RPC payloads yield "".
func extractMCPCommand(body []byte) string {
	if len(bytes.TrimSpace(body)) == 0 {
		return ""
	}
	var batch []json.RawMessage
	if err := json.Unmarshal(body, &batch); err == nil && len(batch) > 0 {
		parts := make([]string, 0, len(batch))
		for _, item := range batch {
			if m := rpcMethod(item); m != "" {
				parts = append(parts, m)
			}
		}
		return strings.Join(parts, ", ")
	}
	return rpcMethod(body)
}

// rpcMethod parses a single JSON-RPC frame. It returns the method name, suffixing
// tools/call with the invoked tool name; responses (no method, has result/error)
// are labelled "response"; anything else yields "".
func rpcMethod(raw json.RawMessage) string {
	var frame struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		return ""
	}
	if frame.Method == "" {
		if len(frame.Result) > 0 || len(frame.Error) > 0 {
			return "response"
		}
		return ""
	}
	if frame.Method == "tools/call" && frame.Params.Name != "" {
		return "tools/call:" + frame.Params.Name
	}
	return frame.Method
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
	// written counts bytes written to the underlying writer, so the console
	// call log can report response size even for streamed MCP responses.
	written int64
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
	n, err := r.ResponseWriter.Write(data)
	r.written += int64(n)
	return n, err
}

func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap lets http.ResponseController reach the original writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
