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
	if !routeAllowed(node.Routes, path) {
		http.Error(w, "route not allowed", http.StatusForbidden)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxProxyRequestBody+1))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	if int64(len(body)) > maxProxyRequestBody {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}

	id, ch := sess.allocStream()
	defer sess.releaseStream(id)

	open := wire.StreamOpen{
		Method:  r.Method,
		Path:    path,
		Headers: map[string][]string(r.Header),
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
					http.Error(w, ev.message, http.StatusBadGateway)
				}
				return
			}
		}
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
