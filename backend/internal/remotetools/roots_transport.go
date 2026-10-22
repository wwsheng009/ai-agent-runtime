package remotetools

import (
	"context"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// rootsResolver turns a client's declared MCP roots into a narrowed workspace.
//
// The client declares roots once per session; the server resolves them on first use
// and caches the result. Resolution is gated on the client actually advertising the
// roots capability: calling ListRoots against a client that never declared it would
// waste a round trip on a protocol error for every single tool call.
//
// Security direction: NewRoots intersects the declaration with the server's own
// allowed roots, so a client can only narrow (roots.go).
type rootsResolver struct {
	base *Workspace
	// principal labels the credential in audit records (never the secret itself).
	principal string
	// bySession caches the resolved narrowing per MCP session id.
	mu        sync.Mutex
	bySession map[string]*Roots
	// unsupported remembers sessions that do not support roots, so the capability
	// check is not repeated per call.
	unsupported map[string]bool
	// disabled short-circuits resolution when the server was started with roots
	// narrowing turned off.
	disabled bool
}

// newRootsResolver builds a resolver over the server's base workspace.
func newRootsResolver(base *Workspace, principal string) *rootsResolver {
	return &rootsResolver{
		base:        base,
		principal:   principal,
		bySession:   map[string]*Roots{},
		unsupported: map[string]bool{},
	}
}

// decorate produces the per-call context.
//
// When the client declared roots, the call executes against the narrowed workspace;
// otherwise it executes against the server's full workspace. The decorator never
// fails a call: a roots resolution error leaves the base workspace in place, because
// a malformed declaration must not turn every tool into an error.
func (r *rootsResolver) decorate(ctx context.Context, req *mcp.CallToolRequest) context.Context {
	if r == nil || req == nil {
		return ctx
	}
	info := RequestInfo{
		Principal:  r.principal,
		ToolCallID: newToolCallID(),
	}
	if req.Session != nil {
		info.SessionID = req.Session.ID()
	}
	ctx = mergeRequestInfo(ctx, info)

	if r.disabled || r.base == nil || req.Session == nil {
		return ctx
	}
	roots := r.resolve(req)
	if roots == nil {
		return ctx
	}
	return WithRoots(ctx, roots)
}

// resolve fetches and caches the session's roots.
func (r *rootsResolver) resolve(req *mcp.CallToolRequest) *Roots {
	if req == nil || req.Session == nil {
		return nil
	}
	// Only ask a client that advertised the capability. Without this gate every
	// call against an older client pays for a failed round trip.
	if !clientSupportsRoots(req.Session) {
		return nil
	}

	sessionID := strings.TrimSpace(req.Session.ID())
	// Cache only when the transport gives every session a distinct id.
	//
	// stdio has exactly one session, but stateless HTTP and in-memory transports can
	// report an empty id for every request. Caching on an empty key would then hand
	// one client's narrowing to another - a cross-session permission leak, not a
	// performance nit - so an empty id resolves fresh each time.
	cacheable := sessionID != ""

	if cacheable {
		r.mu.Lock()
		if cached, ok := r.bySession[sessionID]; ok {
			r.mu.Unlock()
			return cached
		}
		r.mu.Unlock()
	}

	result, err := req.Session.ListRoots(context.Background(), &mcp.ListRootsParams{})
	if err != nil || result == nil || len(result.Roots) == 0 {
		return nil
	}

	requested := make([]string, 0, len(result.Roots))
	for _, root := range result.Roots {
		if root == nil {
			continue
		}
		path, normalizeErr := NormalizeRootURI(root.URI)
		if normalizeErr != nil {
			continue
		}
		requested = append(requested, path)
	}
	if len(requested) == 0 {
		return nil
	}

	roots, resolveErr := NewRoots(r.base, requested)
	if resolveErr != nil {
		// An entirely-outside declaration is refused as a narrow-to-nothing request;
		// keep the base workspace rather than failing every subsequent call.
		return nil
	}

	if cacheable {
		r.mu.Lock()
		r.bySession[sessionID] = roots
		r.mu.Unlock()
	}
	return roots
}

// forget drops a cached session entry (called when a session closes).
func (r *rootsResolver) forget(sessionID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.bySession, sessionID)
	delete(r.unsupported, sessionID)
	r.mu.Unlock()
}

// clientSupportsRoots reports whether the client advertised the roots capability.
func clientSupportsRoots(session *mcp.ServerSession) bool {
	if session == nil {
		return false
	}
	params := session.InitializeParams()
	if params == nil || params.Capabilities == nil {
		return false
	}
	// RootsV2 is the field the SDK actually negotiates (see client.go's note about
	// issue #607); Roots is kept in sync for older peers. Either one being present
	// means the client can answer roots/list.
	if params.Capabilities.RootsV2 != nil {
		return true
	}
	return params.Capabilities.Roots.ListChanged
}

// workspaceForCall returns the workspace a tool call should use.
//
// Tools resolve their paths through this instead of closing over the server
// workspace, which is what makes session narrowing effective without rebuilding the
// tool registry per session.
func workspaceForCall(ctx context.Context, fallback *Workspace) *Workspace {
	if roots := RootsFromContext(ctx); roots != nil {
		if narrowed := roots.Workspace(); narrowed != nil {
			return narrowed
		}
	}
	return fallback
}
