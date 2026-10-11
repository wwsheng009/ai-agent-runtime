package agent

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
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
	// Client overrides the HTTP client (tests).
	Client *http.Client
	// MaxBodyBytes bounds a buffered request body (default 1 MiB).
	MaxBodyBytes int64
}

// defaultMaxForwardBody bounds a buffered request body.
const defaultMaxForwardBody = 1 << 20

// inboundStream assembles a gateway-initiated request until STREAM_END.
type inboundStream struct {
	open   wire.StreamOpen
	body   []byte
	cancel context.CancelFunc
}

func (c *wsConn) handleStreamOpen(id uint32, frame wire.Frame) {
	open, err := wire.DecodePayload[wire.StreamOpen](frame)
	if err != nil {
		_ = c.resetStream(id, "bad_stream_open", err.Error())
		return
	}
	_, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	if c.streams == nil {
		c.streams = make(map[uint32]*inboundStream)
	}
	c.streams[id] = &inboundStream{open: open, cancel: cancel}
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
	max := c.forward.MaxBodyBytes
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
	base := strings.TrimRight(strings.TrimSpace(c.forward.LocalBaseURL), "/")
	if base == "" {
		_ = c.resetStream(id, "no_local_base", "agent has no local base url")
		return
	}
	target, err := joinLocalURL(base, in.open.Path)
	if err != nil {
		_ = c.resetStream(id, "bad_path", err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
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
	if token := strings.TrimSpace(c.forward.LocalToken); token != "" {
		req.Header.Set("X-AICLI-Token", token)
	}

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
		if isHopByHopHeader(key) || lower == "host" || lower == "authorization" || lower == "cookie" {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}
