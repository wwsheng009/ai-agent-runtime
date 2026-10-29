package agent

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// ForwardConfig describes how the agent forwards reverse-proxied requests to
// its local loopback service.
type ForwardConfig struct {
	// LocalBaseURL is the local service origin, e.g. http://127.0.0.1:61772.
	LocalBaseURL string
	// LocalToken, when set, is injected as X-AICLI-Token. The local write token
	// never leaves the machine; it is added here, not by the gateway.
	LocalToken string
	// Mounts maps gateway mount slugs to local MCP endpoints
	// (docs/gateway/16-mcp-mount-tunnel.md). A stream carrying X-AICLI-Mount
	// resolves here instead of LocalBaseURL; an unknown slug fails closed.
	Mounts []MountForward
	// Getenv resolves mount credentials. nil uses os.Getenv. Tests inject it so
	// no real environment variable has to carry a secret.
	Getenv func(string) string
	// Client overrides the HTTP client (tests).
	Client *http.Client
	// MaxBodyBytes bounds a buffered request body (default 1 MiB).
	MaxBodyBytes int64
}

// MountForward is one locally hosted MCP endpoint reachable through the gateway.
type MountForward struct {
	// Slug is the public path segment the gateway routes to this mount.
	Slug string
	// BaseURL is the local origin, e.g. http://127.0.0.1:18765.
	BaseURL string
	// StripPrefix is optional: a path prefix the agent removes before dialing
	// the local endpoint. Empty means the gateway already stripped its
	// namespace and the request path is the local path.
	StripPrefix string
	// AuthType is "bearer" (default), "header" or "none".
	AuthType string
	// TokenEnv names the local environment variable holding the credential.
	// Only the name travels over the wire; the value stays on this machine.
	TokenEnv string
	// TokenValue is a credential the host already resolved (for example from a
	// 0600 token file). It takes precedence over TokenEnv and is never
	// serialized into a registry or status payload.
	TokenValue string
	// Header overrides the injected header for AuthType "header".
	Header string
	// Timeout bounds one forwarded call; zero uses defaultForwardTimeout.
	Timeout time.Duration
	// MaxBodyBytes overrides the buffered request body cap for this mount.
	MaxBodyBytes int64
}

// mountHeader carries the mount slug inside the tunnel. The gateway overwrites
// any client-supplied value, exactly like X-Forwarded-Prefix.
const mountHeader = "X-AICLI-Mount"

// defaultForwardTimeout bounds one forwarded call for the default target.
const defaultForwardTimeout = 5 * time.Minute

// defaultMaxForwardBody bounds a buffered request body.
const defaultMaxForwardBody = 1 << 20

// forwardError carries a STREAM_RESET code so the gateway can render an
// actionable message instead of a generic 502.
type forwardError struct {
	code    string
	message string
}

func (e *forwardError) Error() string { return e.message }

func errorCode(err error) (string, string) {
	if forwarded, ok := err.(*forwardError); ok {
		return forwarded.code, forwarded.message
	}
	return "bad_request", err.Error()
}

// streamTarget is the resolved upstream for one inbound stream.
type streamTarget struct {
	baseURL     string
	path        string
	headerName  string
	headerValue string
	timeout     time.Duration
	maxBody     int64
	// mount is the slug when this stream was routed through a mount; empty for
	// the default (non-mount) forwarding target.
	mount string
}

// inboundStream assembles a gateway-initiated request until STREAM_END.
type inboundStream struct {
	open   wire.StreamOpen
	body   []byte
	cancel context.CancelFunc
	target streamTarget
}

func (c *wsConn) handleStreamOpen(id uint32, frame wire.Frame) {
	open, err := wire.DecodePayload[wire.StreamOpen](frame)
	if err != nil {
		_ = c.resetStream(id, "bad_stream_open", err.Error())
		return
	}
	target, err := c.resolveStreamTarget(open)
	if err != nil {
		code, message := errorCode(err)
		_ = c.resetStream(id, code, message)
		return
	}
	_, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	if c.streams == nil {
		c.streams = make(map[uint32]*inboundStream)
	}
	c.streams[id] = &inboundStream{open: open, cancel: cancel, target: target}
	c.mu.Unlock()

	c.flowMu.Lock()
	c.sendCredit[id] = flowControlWindow
	c.flowMu.Unlock()
}

// flowControlWindow bounds in-flight response bytes per stream (docs/gateway/03-protocol.md §5).
const flowControlWindow = 1 << 20

func (c *wsConn) addCredit(id uint32, delta int64) {
	if delta <= 0 {
		return
	}
	c.flowMu.Lock()
	c.sendCredit[id] += delta
	c.flowMu.Unlock()
	select {
	case c.flowSignal <- struct{}{}:
	default:
	}
}

// acquireCredit blocks until n bytes of credit are available for the stream, or
// ctx is done. The gateway replenishes credit with WINDOW_UPDATE as it consumes.
func (c *wsConn) acquireCredit(ctx context.Context, id uint32, n int64) error {
	if n <= 0 {
		return nil
	}
	for {
		c.flowMu.Lock()
		if c.sendCredit[id] >= n {
			c.sendCredit[id] -= n
			c.flowMu.Unlock()
			return nil
		}
		c.flowMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.flowSignal:
		}
	}
}

func (c *wsConn) handleStreamData(id uint32, frame wire.Frame) {
	c.mu.Lock()
	in := c.streams[id]
	c.mu.Unlock()
	if in == nil {
		return
	}
	max := in.target.maxBody
	if max <= 0 {
		max = c.forward.MaxBodyBytes
	}
	if max <= 0 {
		max = defaultMaxForwardBody
	}
	if int64(len(in.body))+int64(len(frame.Payload)) > max {
		c.mu.Lock()
		delete(c.streams, id)
		c.mu.Unlock()
		_ = c.resetStream(id, "body_too_large", "request body exceeds limit")
		return
	}
	in.body = append(in.body, frame.Payload...)
}

func (c *wsConn) handleStreamEnd(id uint32) {
	c.mu.Lock()
	in := c.streams[id]
	delete(c.streams, id)
	c.mu.Unlock()
	if in == nil {
		return
	}
	go c.forwardStream(id, in)
}

func (c *wsConn) handleStreamReset(id uint32) {
	c.mu.Lock()
	in := c.streams[id]
	delete(c.streams, id)
	c.mu.Unlock()
	if in != nil && in.cancel != nil {
		in.cancel()
	}
}

// forwardStream performs the local HTTP request and streams the response back
// as STREAM_DATA/STREAM_END frames.
func (c *wsConn) forwardStream(id uint32, in *inboundStream) {
	base := strings.TrimRight(strings.TrimSpace(in.target.baseURL), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(c.forward.LocalBaseURL), "/")
	}
	if base == "" {
		_ = c.resetStream(id, "no_local_base", "agent has no local base url")
		return
	}
	target, err := joinLocalURL(base, in.target.path)
	if err != nil {
		_ = c.resetStream(id, "bad_path", err.Error())
		return
	}

	timeout := in.target.timeout
	if timeout <= 0 {
		timeout = defaultForwardTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var body io.Reader
	if len(in.body) > 0 {
		body = bytes.NewReader(in.body)
	}
	req, err := http.NewRequestWithContext(ctx, methodOrGet(in.open.Method), target, body)
	if err != nil {
		_ = c.resetStream(id, "bad_request", err.Error())
		return
	}
	copyForwardRequestHeaders(req.Header, in.open.Headers)
	if in.target.headerName != "" && in.target.headerValue != "" {
		req.Header.Set(in.target.headerName, in.target.headerValue)
	} else if in.target.mount == "" {
		// Default target (runtime-server / web UI): keep the historical local
		// write-token injection. Mount streams never receive it.
		token := strings.TrimSpace(c.forward.LocalToken)
		if token != "" {
			req.Header.Set("X-AICLI-Token", token)
		}
	}
	// Rewrite Origin/Referer to the local service origin: the browser speaks to
	// the gateway, so a forwarded Origin points at the gateway while the local
	// server's same-origin check compares it against its own host — leaving the
	// browser value in place fails every asset/GET request with 403
	// cross-origin (observed: /web/app.js -> 403 through the tunnel). A reverse
	// proxy rewrites these to the upstream origin; do the same.
	rewriteForwardOriginHeaders(req.Header, base)

	client := c.forward.Client
	if client == nil {
		client = &http.Client{}
	}
	resp, err := client.Do(req)
	if err != nil {
		_ = c.resetStream(id, "upstream_unreachable", err.Error())
		return
	}
	defer resp.Body.Close()

	if err := c.writeResponseHeaders(id, resp); err != nil {
		return
	}
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if err := c.acquireCredit(ctx, id, int64(len(chunk))); err != nil {
				return
			}
			if err := c.writeStreamData(id, chunk, 0); err != nil {
				return
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			_ = c.resetStream(id, "upstream_read_error", readErr.Error())
			return
		}
	}
	_ = c.writeStreamEnd(id)
}

func (c *wsConn) writeResponseHeaders(id uint32, resp *http.Response) error {
	headers := make(map[string][]string, len(resp.Header))
	for key, values := range resp.Header {
		if isHopByHopHeader(key) {
			continue
		}
		headers[key] = values
	}
	frame, err := wire.NewJSONFrame(wire.TypeStreamData, id, wire.ResponseHeaders{
		Status:  resp.StatusCode,
		Headers: headers,
	})
	if err != nil {
		return err
	}
	return c.writeFrame(frame)
}

func (c *wsConn) writeStreamData(id uint32, data []byte, flags uint16) error {
	return c.writeFrame(wire.Frame{
		Version:  wire.ProtocolVersion,
		Type:     wire.TypeStreamData,
		Flags:    flags,
		StreamID: id,
		Payload:  data,
	})
}

func (c *wsConn) writeStreamEnd(id uint32) error {
	return c.writeFrame(wire.Frame{
		Version:  wire.ProtocolVersion,
		Type:     wire.TypeStreamEnd,
		StreamID: id,
	})
}

func (c *wsConn) resetStream(id uint32, code, message string) error {
	frame, err := wire.NewJSONFrame(wire.TypeStreamReset, id, wire.StreamReset{Code: code, Message: message})
	if err != nil {
		return err
	}
	return c.writeFrame(frame)
}

// joinLocalURL appends a request path (with query) to the local origin.
func joinLocalURL(base, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "/"
	}
	parsed, err := url.Parse(path)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(parsed.Path, "/") {
		parsed.Path = "/" + parsed.Path
	}
	out := base + parsed.Path
	if parsed.RawQuery != "" {
		out += "?" + parsed.RawQuery
	}
	return out, nil
}

// resolveStreamTarget maps one STREAM_OPEN to its local upstream.
//
// The mount header is authoritative: a stream that names an unknown mount fails
// closed instead of falling back to LocalBaseURL. Falling back would let a
// revoked mount reach whatever else the node happens to serve.
func (c *wsConn) resolveStreamTarget(open wire.StreamOpen) (streamTarget, error) {
	slug := headerValue(open.Headers, mountHeader)
	if slug == "" {
		return streamTarget{
			baseURL: strings.TrimRight(strings.TrimSpace(c.forward.LocalBaseURL), "/"),
			path:    open.Path,
			timeout: defaultForwardTimeout,
			maxBody: c.forward.MaxBodyBytes,
		}, nil
	}

	mount, ok := c.mountForward(slug)
	if !ok {
		return streamTarget{}, &forwardError{code: "mount_unknown", message: "agent has no mount " + slug}
	}
	path, err := mountPath(mount, open.Path)
	if err != nil {
		return streamTarget{}, err
	}
	name, value, err := mountAuthHeader(mount, c.envLookup())
	if err != nil {
		return streamTarget{}, err
	}
	timeout := mount.Timeout
	if timeout <= 0 {
		timeout = defaultForwardTimeout
	}
	maxBody := mount.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = defaultMaxForwardBody
	}
	return streamTarget{
		baseURL:     strings.TrimRight(strings.TrimSpace(mount.BaseURL), "/"),
		path:        path,
		headerName:  name,
		headerValue: value,
		timeout:     timeout,
		maxBody:     maxBody,
		mount:       slug,
	}, nil
}

// mountForward finds the local endpoint for a mount slug.
func (c *wsConn) mountForward(slug string) (MountForward, bool) {
	slug = strings.TrimSpace(slug)
	for _, mount := range c.forward.Mounts {
		if strings.TrimSpace(mount.Slug) == slug {
			return mount, true
		}
	}
	return MountForward{}, false
}

// envLookup resolves mount credentials; tests inject a fake.
func (c *wsConn) envLookup() func(string) string {
	if c.forward.Getenv != nil {
		return c.forward.Getenv
	}
	return os.Getenv
}

// mountToken reads the local credential named by TokenEnv. An empty TokenEnv
// means "no injection" (a loopback-only endpoint without a token); a named but
// empty variable is a hard configuration error, not a silent unauthenticated
// request.
func mountToken(mount MountForward, getenv func(string) string) (string, error) {
	name := strings.TrimSpace(mount.TokenEnv)
	if name == "" {
		return "", nil
	}
	if getenv == nil {
		return "", &forwardError{code: "mount_token_missing", message: "no environment lookup for " + name}
	}
	token := strings.TrimSpace(getenv(name))
	if token == "" {
		return "", &forwardError{code: "mount_token_missing", message: "environment variable " + name + " is empty"}
	}
	return token, nil
}

// mountAuthHeader builds the credential header for one mount stream.
func mountAuthHeader(mount MountForward, getenv func(string) string) (string, string, error) {
	resolve := func() (string, error) {
		if value := strings.TrimSpace(mount.TokenValue); value != "" {
			return value, nil
		}
		return mountToken(mount, getenv)
	}
	switch strings.ToLower(strings.TrimSpace(mount.AuthType)) {
	case "", "bearer":
		token, err := resolve()
		if err != nil {
			return "", "", err
		}
		if token == "" {
			return "", "", nil
		}
		return "Authorization", "Bearer " + token, nil
	case "header":
		token, err := resolve()
		if err != nil {
			return "", "", err
		}
		if token == "" {
			return "", "", &forwardError{code: "mount_token_missing", message: "mount auth type header needs token_env"}
		}
		name := strings.TrimSpace(mount.Header)
		if name == "" {
			name = "Authorization"
		}
		return name, token, nil
	case "none":
		return "", "", nil
	default:
		return "", "", &forwardError{code: "mount_auth_unsupported", message: "unsupported mount auth type " + mount.AuthType}
	}
}

// mountPath strips an optional local prefix and rejects traversal. The gateway
// normally strips its own namespace, so StripPrefix is empty and the request
// path is already the local path (for example "/mcp").
func mountPath(mount MountForward, requestPath string) (string, error) {
	requestPath = strings.TrimSpace(requestPath)
	if requestPath == "" {
		requestPath = "/"
	}
	pathPart, query, hasQuery := strings.Cut(requestPath, "?")

	prefix := strings.TrimRight(strings.TrimSpace(mount.StripPrefix), "/")
	if prefix != "" {
		switch {
		case pathPart == prefix:
			pathPart = "/"
		case strings.HasPrefix(pathPart, prefix+"/"):
			pathPart = pathPart[len(prefix):]
		default:
			return "", &forwardError{code: "bad_path", message: "path is outside the mount prefix " + prefix}
		}
	}
	if !strings.HasPrefix(pathPart, "/") {
		pathPart = "/" + pathPart
	}
	for _, segment := range strings.Split(pathPart, "/") {
		if segment == ".." {
			return "", &forwardError{code: "bad_path", message: "path traversal is not allowed"}
		}
	}
	if hasQuery {
		pathPart += "?" + query
	}
	return pathPart, nil
}

// headerValue performs a case-insensitive lookup in the tunnel header map.
func headerValue(headers map[string][]string, name string) string {
	for key, values := range headers {
		if !strings.EqualFold(strings.TrimSpace(key), name) || len(values) == 0 {
			continue
		}
		return strings.TrimSpace(values[0])
	}
	return ""
}

func methodOrGet(method string) string {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		return http.MethodGet
	}
	return method
}

// hopByHopHeaders are connection-scoped and must not be forwarded.
var hopByHopHeaders = map[string]bool{
	"connection":          true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"proxy-connection":    true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
}

func isHopByHopHeader(name string) bool {
	return hopByHopHeaders[strings.ToLower(strings.TrimSpace(name))]
}

// copyForwardRequestHeaders copies request headers, dropping hop-by-hop and
// credential headers (the browser never controls the local token).
func copyForwardRequestHeaders(dst http.Header, src map[string][]string) {
	for key, values := range src {
		lower := strings.ToLower(strings.TrimSpace(key))
		if isHopByHopHeader(key) || isInternalForwardHeader(lower) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

// isInternalForwardHeader reports headers the agent owns. The gateway overwrites
// X-AICLI-Mount and X-Forwarded-Prefix, and it never forwards the caller's
// credentials; X-AICLI-Token is injected locally for the default target only.
func isInternalForwardHeader(lower string) bool {
	switch lower {
	case "host", "authorization", "cookie", "x-aicli-mount", "x-aicli-token":
		return true
	default:
		return false
	}
}

// rewriteForwardOriginHeaders points Origin/Referer at the local service origin
// so same-origin checks on the loopback server accept tunneled requests. base is
// the local base URL (scheme://host:port).
func rewriteForwardOriginHeaders(h http.Header, base string) {
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" {
		return
	}
	localOrigin := parsed.Scheme + "://" + parsed.Host
	if origin := strings.TrimSpace(h.Get("Origin")); origin != "" {
		h.Set("Origin", localOrigin)
	}
	if referer := strings.TrimSpace(h.Get("Referer")); referer != "" {
		if ref, err := url.Parse(referer); err == nil && ref.Host != "" {
			ref.Host = parsed.Host
			if parsed.Scheme != "" {
				ref.Scheme = parsed.Scheme
			}
			h.Set("Referer", ref.String())
		}
	}
}
