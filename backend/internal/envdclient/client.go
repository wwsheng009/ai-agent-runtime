package envdclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Wire-level constants, all taken from observed envd behaviour.
const (
	// DefaultPort is the port envd listens on by default.
	DefaultPort = 49983

	// contentTypeUnary is the content type for a unary call: the body is bare
	// JSON with no envelope.
	contentTypeUnary = "application/json"

	// contentTypeStream is the content type for a streaming call: the body is a
	// sequence of envelope frames.
	contentTypeStream = "application/connect+json"

	// protocolVersionHeader is required by Connect for streaming calls.
	protocolVersionHeader = "Connect-Protocol-Version"
	protocolVersion       = "1"

	// accessTokenHeader is the header envd enforces once an access token has been
	// installed through POST /init. Until then the daemon is open to any client
	// that can reach its port.
	accessTokenHeader = "X-Access-Token"

	// defaultTimeout bounds a unary call. Streaming calls are deliberately not
	// bounded by it: a client-side deadline would kill a long-running process.
	defaultTimeout = 30 * time.Second
)

// Config describes how to reach one envd instance.
type Config struct {
	// Addr is host:port, or a full http:// / https:// URL. A bare host gets
	// DefaultPort appended, so both "10.0.0.9" and "http://10.0.0.9:49983" work.
	Addr string

	// User is the OS user envd executes as.
	//
	// This is not optional bookkeeping: envd reads the user from HTTP Basic
	// credentials and rejects anything without one. Verified against a live envd,
	// where the filesystem service answered 401 `unauthenticated: no user
	// specified` until Basic auth was supplied. The message names the user rather
	// than the token, which is why guessing extra token headers does not help.
	User string

	// Password is the Basic auth password slot.
	//
	// envd's Basic middleware only maps the *user*; it ignores this value. It is
	// kept for servers that sit behind a proxy which validates the slot itself.
	Password string

	// AccessToken is envd's access token, sent as X-Access-Token on every call.
	//
	// envd enforces the token only after it has been installed (POST /init), so
	// this may be empty for an untokened daemon. Once a token is set, every path
	// except GET /health and the /files endpoints answers 401 without it, which
	// is the difference between "LAN-reachable execution daemon" and "open
	// remote shell".
	AccessToken string

	// HTTPClient overrides the default client. Supplying one is how a caller adds
	// a private CA, a client certificate, or custom retry behaviour.
	HTTPClient *http.Client

	// TLS enables HTTPS. With TLS, HTTP/2 is negotiated automatically and envd's
	// client-streaming methods (SendInput, SendSignal, Update) become usable;
	// plaintext envd only offers HTTP/1.1, where those answer 501.
	TLS *tls.Config

	// Timeout bounds each unary call. Zero uses defaultTimeout.
	Timeout time.Duration
}

// Client is a Connect-RPC client for one envd instance.
//
// It is safe for concurrent use: every method builds its own request rather than
// mutating shared state.
type Client struct {
	base  string
	user  string
	pass  string
	token string
	hc    *http.Client
	tmo   time.Duration
}

// Dial validates the configuration and returns a client.
//
// Dial performs no network I/O. envd has no health RPC, so a connect-time probe
// would add a failure mode without proving much; call Ping or any real method to
// establish reachability.
func Dial(cfg Config) (*Client, error) {
	base, err := normalizeAddr(cfg.Addr)
	if err != nil {
		return nil, err
	}
	user := strings.TrimSpace(cfg.User)
	if user == "" {
		return nil, errors.New("envdclient: User is required; envd rejects a request with no user")
	}
	tmo := cfg.Timeout
	if tmo <= 0 {
		tmo = defaultTimeout
	}

	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{
			Transport: &http.Transport{
				Proxy:                 nil,
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: time.Second,
				ForceAttemptHTTP2:     true,
				TLSClientConfig:       cfg.TLS,
			},
			// No client-wide timeout: it would apply to streaming bodies too and
			// cut long-running processes. Unary calls set their own deadline.
		}
	}

	return &Client{
		base:  base,
		user:  user,
		pass:  cfg.Password,
		token: strings.TrimSpace(cfg.AccessToken),
		hc:    hc,
		tmo:   tmo,
	}, nil
}

// normalizeAddr turns a config address into a base URL string.
func normalizeAddr(addr string) (string, error) {
	raw := strings.TrimSpace(addr)
	if raw == "" {
		return "", errors.New("envdclient: Addr is required")
	}
	if !strings.Contains(raw, "://") {
		// A bare host or host:port. Add the scheme, and the port when absent.
		if _, _, err := net.SplitHostPort(raw); err != nil {
			raw = net.JoinHostPort(strings.Trim(raw, "[]"), strconv.Itoa(DefaultPort))
		}
		raw = "http://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("envdclient: parse addr %q: %w", addr, err)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("envdclient: addr %q has no host", addr)
	}
	if parsed.Port() == "" {
		parsed.Host = net.JoinHostPort(parsed.Hostname(), strconv.Itoa(DefaultPort))
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	return parsed.String(), nil
}

// Addr returns the normalized base URL, for logs and audit records.
func (c *Client) Addr() string {
	if c == nil {
		return ""
	}
	return c.base
}

// User returns the OS user this client acts as.
func (c *Client) User() string {
	if c == nil {
		return ""
	}
	return c.user
}

// applyHeaders sets the Basic credential envd uses to choose the executing user.
func (c *Client) applyHeaders(req *http.Request) {
	req.SetBasicAuth(c.user, c.pass)
	if c.token != "" {
		req.Header.Set(accessTokenHeader, c.token)
	}
}

// methodURL builds the endpoint for one RPC, e.g. /process.Process/Start.
func (c *Client) methodURL(service, method string) string {
	return c.base + "/" + service + "/" + method
}

// doUnary performs a unary call.
//
// A unary body is bare JSON. A non-200 response carries a ConnectError as its
// whole body, so it is decoded rather than surfaced as an opaque status.
func (c *Client) doUnary(ctx context.Context, service, method string, request, response any) error {
	payload, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("envdclient: encode %s.%s request: %w", service, method, err)
	}

	ctx, cancel := context.WithTimeout(ctx, c.tmo)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.methodURL(service, method), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("envdclient: build %s.%s request: %w", service, method, err)
	}
	req.Header.Set("Content-Type", contentTypeUnary)
	c.applyHeaders(req)

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("envdclient: call %s.%s: %w", service, method, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFramePayload))
	if err != nil {
		return fmt.Errorf("envdclient: read %s.%s response: %w", service, method, err)
	}
	if resp.StatusCode != http.StatusOK {
		return decodeUnaryError(resp.StatusCode, body)
	}
	if response == nil {
		return nil
	}
	if err := json.Unmarshal(body, response); err != nil {
		return fmt.Errorf("envdclient: decode %s.%s response: %w", service, method, err)
	}
	return nil
}

// doUnaryRaw performs a unary call and returns the raw body, for endpoints that
// are not JSON (metrics).
func (c *Client) doUnaryRaw(ctx context.Context, service, method string, request any) ([]byte, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("envdclient: encode %s.%s request: %w", service, method, err)
	}
	ctx, cancel := context.WithTimeout(ctx, c.tmo)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.methodURL(service, method), bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("envdclient: build %s.%s request: %w", service, method, err)
	}
	req.Header.Set("Content-Type", contentTypeUnary)
	c.applyHeaders(req)

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("envdclient: call %s.%s: %w", service, method, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFramePayload))
	if err != nil {
		return nil, fmt.Errorf("envdclient: read %s.%s response: %w", service, method, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, decodeUnaryError(resp.StatusCode, body)
	}
	return body, nil
}

// decodeUnaryError turns a non-200 unary response into an error.
func decodeUnaryError(status int, body []byte) error {
	var connectErr ConnectError
	if err := json.Unmarshal(body, &connectErr); err == nil && connectErr.Code != "" {
		return &connectErr
	}
	preview := strings.TrimSpace(string(body))
	if len(preview) > 200 {
		preview = preview[:200] + "..."
	}
	return fmt.Errorf("envdclient: HTTP %d: %s", status, preview)
}

// StreamReader reads a server-streaming response frame by frame.
//
// It is not safe for concurrent use; call Recv from one goroutine.
type StreamReader struct {
	service  string
	method   string
	resp     *http.Response
	buf      *bufio.Reader
	finished bool
}

// Stream opens a server-streaming call and sends the single request message.
//
// The caller must Close the returned reader. Cancelling ctx cancels the stream,
// which is the supported way to stop an ongoing process: envd reaps the child
// when its stream client goes away.
func (c *Client) Stream(ctx context.Context, service, method string, request any) (*StreamReader, error) {
	frame, err := EncodeJSONFrame(request)
	if err != nil {
		return nil, fmt.Errorf("envdclient: encode %s.%s request: %w", service, method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.methodURL(service, method), bytes.NewReader(frame))
	if err != nil {
		return nil, fmt.Errorf("envdclient: build %s.%s request: %w", service, method, err)
	}
	req.Header.Set("Content-Type", contentTypeStream)
	req.Header.Set(protocolVersionHeader, protocolVersion)
	c.applyHeaders(req)

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("envdclient: open %s.%s stream: %w", service, method, err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, decodeUnaryError(resp.StatusCode, body)
	}
	return &StreamReader{
		service: service,
		method:  method,
		resp:    resp,
		buf:     bufio.NewReaderSize(resp.Body, 32<<10),
	}, nil
}

// ProtoMajor reports the negotiated HTTP version.
//
// A caller that needs envd's client-streaming methods should check for 2 first,
// because plaintext envd only speaks HTTP/1.1 there.
func (s *StreamReader) ProtoMajor() int {
	if s == nil || s.resp == nil {
		return 0
	}
	return s.resp.ProtoMajor
}

// Recv decodes the next message into v.
//
// It returns io.EOF when the stream ends cleanly. A terminal error frame comes
// back as *ConnectError so callers can tell an auth failure from a process
// failure.
func (s *StreamReader) Recv(v any) error {
	if s == nil {
		return errors.New("envdclient: Recv on a nil stream")
	}
	if s.finished {
		return io.EOF
	}
	for {
		frame, err := ReadFrame(s.buf)
		if err != nil {
			s.finished = true
			if errors.Is(err, io.EOF) {
				return io.EOF
			}
			return fmt.Errorf("envdclient: read %s.%s stream: %w", s.service, s.method, err)
		}
		switch frame.Flags {
		case flagMessage:
			if v == nil {
				continue
			}
			if err := json.Unmarshal(frame.Payload, v); err != nil {
				return fmt.Errorf("envdclient: decode %s.%s message: %w", s.service, s.method, err)
			}
			return nil
		case flagEndStream:
			s.finished = true
			if err := parseEndStream(frame.Payload); err != nil {
				return err
			}
			return io.EOF
		case flagCompressed:
			return fmt.Errorf("envdclient: %s.%s sent a compressed frame; this client does not negotiate compression", s.service, s.method)
		default:
			return fmt.Errorf("envdclient: %s.%s sent an unknown frame flag 0x%02x", s.service, s.method, frame.Flags)
		}
	}
}

// Close releases the response body.
func (s *StreamReader) Close() error {
	if s == nil || s.resp == nil {
		return nil
	}
	return s.resp.Body.Close()
}
