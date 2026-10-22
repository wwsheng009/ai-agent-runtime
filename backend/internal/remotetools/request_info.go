package remotetools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
)

// RequestInfo carries per-call identity facts from the transport layer to the
// audit wrapper. The values are informational: authorization never depends on
// them.
type RequestInfo struct {
	Principal  string
	RemoteAddr string
	SessionID  string
	ToolCallID string
}

type requestInfoKey struct{}

// WithRequestInfo stores a request identity on the context.
func WithRequestInfo(ctx context.Context, info RequestInfo) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestInfoKey{}, info)
}

// RequestInfoFromContext reads the request identity, if the transport supplied one.
func RequestInfoFromContext(ctx context.Context) RequestInfo {
	if ctx == nil {
		return RequestInfo{}
	}
	info, _ := ctx.Value(requestInfoKey{}).(RequestInfo)
	return info
}

// mergeRequestInfo fills empty fields from the existing value. A transport-level
// remote address must survive the tool-context decorator, which only knows the
// MCP session and principal.
func mergeRequestInfo(ctx context.Context, info RequestInfo) context.Context {
	current := RequestInfoFromContext(ctx)
	if info.Principal == "" {
		info.Principal = current.Principal
	}
	if info.RemoteAddr == "" {
		info.RemoteAddr = current.RemoteAddr
	}
	if info.SessionID == "" {
		info.SessionID = current.SessionID
	}
	if info.ToolCallID == "" {
		info.ToolCallID = current.ToolCallID
	}
	return WithRequestInfo(ctx, info)
}

// newToolCallID returns a short local correlation id. It is deliberately not the
// JSON-RPC id: the SDK handler does not expose the wire id, and a local id is
// sufficient to correlate audit and access records.
func newToolCallID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "call_unknown"
	}
	return "call_" + hex.EncodeToString(buf[:])
}

// principalFor labels the credential without recording it. A bearer token is
// reduced to a short hash prefix; stdio uses the OS user that owns the process.
func principalFor(cfg *Config, token string) string {
	if cfg != nil && strings.EqualFold(strings.TrimSpace(cfg.Transport), "stdio") {
		if user := strings.TrimSpace(os.Getenv("USER")); user != "" {
			return "user:" + user
		}
		if user := strings.TrimSpace(os.Getenv("USERNAME")); user != "" {
			return "user:" + user
		}
		return "stdio"
	}
	if strings.TrimSpace(token) != "" {
		return "bearer:" + shortHash(token)
	}
	return "http"
}

// shortHash is a non-reversible 8-hex label for a credential.
func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:4])
}
