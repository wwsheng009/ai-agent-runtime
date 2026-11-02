package tunnel

import (
	"io"
	"net/http"
	pathpkg "path"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// maxProxyRequestBody bounds a buffered reverse-proxy request body.
const maxProxyRequestBody = 1 << 20

// ServeProxy reverse-proxies one HTTP request to the node's local service.
// nodeID and path (already stripped of the /n/{nodeID} prefix) are resolved by
// the caller's router. The response is streamed as it arrives, so SSE and large
// downloads work without buffering the whole body.
func (s *Server) ServeProxy(w http.ResponseWriter, r *http.Request, nodeID, path string) {
	s.serveProxy(w, r, nodeID, path, "", false)
}

// ServeProxyMount reverse-proxies one request that belongs to a published MCP
// mount. The mount slug is written into the tunnel header here (overwriting any
// client-supplied value), so the agent can only ever route it to the endpoint
// that slug names.
func (s *Server) ServeProxyMount(w http.ResponseWriter, r *http.Request, nodeID, path, mount string) {
	s.serveProxy(w, r, nodeID, path, mount, false)
}

// ServeProxyPinned reverse-proxies a trusted, gateway-selected path. Unlike
// ServeProxy it does not consult the node's advertised route allowlist; callers
// must pass a constant, server-side path and must never forward a raw
// caller-controlled path here. It exists for read-only gateway surfaces that
// need a node-local admin view without widening the general /n/ proxy.
func (s *Server) ServeProxyPinned(w http.ResponseWriter, r *http.Request, nodeID, path string) {
	s.serveProxy(w, r, nodeID, path, "", true)
}

func (s *Server) serveProxy(w http.ResponseWriter, r *http.Request, nodeID, path, mount string, pinned bool) {
	sess, ok := s.session(nodeID)
	if !ok {
		http.Error(w, "node offline", http.StatusBadGateway)
		return
	}
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}

	// Route allowlist: only paths under the node's registered routes are
	// proxied, so the gateway can never act as an open proxy.
	node, ok := s.registry.Get(nodeID)
	if !ok {
		http.Error(w, "node offline", http.StatusBadGateway)
		return
	}
	if !pinned && !routeAllowed(node.Routes, path) {
		if s.cfg.OnRouteDenied != nil {
			s.cfg.OnRouteDenied(r)
		}
		http.Error(w, "route not allowed", http.StatusForbidden)
		return
	}

	limit := s.cfg.MaxProxyRequestBody
	if limit <= 0 {
		limit = maxProxyRequestBody
	}
	if r.Body == nil {
		// POST/PUT with no body (e.g. connector handoff) yields a nil Body on
		// the gateway-side synthetic request; io.LimitReader(nil) would panic
		// inside LimitedReader.Read. Treat it as an empty body.
		r.Body = http.NoBody
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	if int64(len(body)) > limit {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}

	id, ch := sess.allocStream()
	defer sess.releaseStream(id)

	// Forward the mount prefix so the local service can inject a <base href>
	// (runtime-server SPA base-path awareness, docs/gateway/06 §4.1). The
	// gateway overwrites any client-supplied value.
	headers := make(map[string][]string, len(r.Header)+1)
	for key, values := range r.Header {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "x-aicli-mount", "x-aicli-token":
			// Agent-owned headers: a caller must never be able to select the
			// mount (or inject the local write token) directly.
			continue
		}
		headers[key] = values
	}
	prefix := "/n/" + nodeID
	if mount = strings.TrimSpace(mount); mount != "" {
		prefix = "/m/" + mount
		headers["X-AICLI-Mount"] = []string{mount}
	}
	headers["X-Forwarded-Prefix"] = []string{prefix}
	open := wire.StreamOpen{
		Method:  r.Method,
		Path:    path,
		Headers: headers,
		BodyLen: int64(len(body)),
	}
	if err := sess.writeJSON(wire.TypeStreamOpen, id, open); err != nil {
		http.Error(w, "gateway write failed", http.StatusBadGateway)
		return
	}
	if len(body) > 0 {
		if err := sess.writeFrame(wire.Frame{Version: wire.ProtocolVersion, Type: wire.TypeStreamData, StreamID: id, Payload: body}); err != nil {
			http.Error(w, "gateway write failed", http.StatusBadGateway)
			return
		}
	}
	if err := sess.writeFrame(wire.Frame{Version: wire.ProtocolVersion, Type: wire.TypeStreamEnd, StreamID: id}); err != nil {
		http.Error(w, "gateway write failed", http.StatusBadGateway)
		return
	}

	flusher, _ := w.(http.Flusher)
	headersWritten := false
	for {
		select {
		case <-r.Context().Done():
			// The caller is gone. Tell the agent to cancel the local request;
			// otherwise an idle SSE GET can keep an upstream concurrency slot
			// pinned until its mount timeout.
			_ = sess.writeJSON(wire.TypeStreamReset, id, wire.StreamReset{
				Code:    "client_gone",
				Message: "client disconnected",
			})
			return
		case ev := <-ch:
			switch ev.kind {
			case streamHeaders:
				for key, values := range ev.headers {
					for _, value := range values {
						w.Header().Add(key, value)
					}
				}
				w.WriteHeader(ev.status)
				headersWritten = true
				if flusher != nil {
					flusher.Flush()
				}
			case streamData:
				if !headersWritten {
					w.WriteHeader(http.StatusOK)
					headersWritten = true
				}
				if _, err := w.Write(ev.data); err != nil {
					return
				}
				// Replenish the agent's send credit for the consumed bytes.
				_ = sess.writeFrame(wire.EncodeWindowUpdate(id, uint32(len(ev.data))))
				if flusher != nil {
					flusher.Flush()
				}
			case streamEnd:
				if !headersWritten {
					w.WriteHeader(http.StatusOK)
				}
				return
			case streamReset:
				if !headersWritten {
					http.Error(w, proxyResetMessage(ev.code, ev.message), http.StatusBadGateway)
				}
				return
			}
		}
	}
}

// proxyResetMessage renders an agent-side STREAM_RESET as an actionable HTTP
// body. Reset codes are protocol-internal; `no_local_base` in particular used
// to surface as the cryptic "agent has no local base url" even though the
// operator-visible condition is "this node has no local Web service". Nodes
// now trim their routes instead of accepting such requests (fail closed with
// 403), but an older agent can still answer this way, so keep the translation.
func proxyResetMessage(code, message string) string {
	switch code {
	case "no_local_base":
		return "node has no local web service (the agent was started without its web client); reverse proxy is unavailable for this node"
	default:
		message = strings.TrimSpace(message)
		if message == "" {
			return "stream reset by node"
		}
		return message
	}
}

// routeAllowed reports whether requestPath is covered by the node's registered
// route allowlist. An empty allowlist denies everything (fail-closed), and a
// "/" route allows all. Traversal is rejected.
func routeAllowed(routes []string, requestPath string) bool {
	if len(routes) == 0 {
		return false
	}
	cleanPath := requestPath
	if i := strings.IndexByte(cleanPath, '?'); i >= 0 {
		cleanPath = cleanPath[:i]
	}
	cleanPath = pathpkg.Clean("/" + cleanPath)
	if strings.Contains(cleanPath, "..") {
		return false
	}
	for _, route := range routes {
		route = strings.TrimSpace(route)
		if route == "" {
			continue
		}
		cleanRoute := pathpkg.Clean("/" + route)
		if cleanRoute == "/" {
			return true
		}
		if cleanPath == cleanRoute {
			return true
		}
		if strings.HasPrefix(cleanPath, cleanRoute) {
			if strings.HasSuffix(cleanRoute, "/") || (len(cleanPath) > len(cleanRoute) && cleanPath[len(cleanRoute)] == '/') {
				return true
			}
		}
	}
	return false
}

// RouteAllowed exposes the allowlist predicate so mount registration can reject
// a mount whose local path the node never declared (fail closed at REGISTER time
// instead of a confusing 403 on the first call).
func RouteAllowed(routes []string, requestPath string) bool {
	return routeAllowed(routes, requestPath)
}
