package main

import (
	"net/http"
	pathpkg "path"
	"strings"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
)

// nodeConnectorProxyBase is the public gateway path that exposes a read-only
// view of one node's local connector admin API:
//
//	GET /api/nodes/{node_id}/connectors          -> /api/connectors
//	GET /api/nodes/{node_id}/connectors/schema   -> /api/connectors/schema
//	GET /api/nodes/{node_id}/connectors/{id}     -> /api/connectors/{id}
//
// The route is owner-authenticated and GET-only. It intentionally uses
// ServeProxyPinned because aicli's reverse-proxy allowlist does not include the
// connector admin surface; the target namespace is still a constant server-side
// prefix, never a caller-controlled path.
const nodeConnectorProxyBase = "/api/nodes/"

// handleNodeConnectors serves the console's read-only aggregation view for one
// online node. It never forwards writes: connector mutations stay on the node's
// own admin surface.
func handleNodeConnectors(server *tunnel.Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if server == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "node proxy is not wired"})
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "node connector view is read-only"})
			return
		}
		nodeID := strings.TrimSpace(mux.Vars(r)["node_id"])
		if nodeID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "node_id is required"})
			return
		}

		prefix := nodeConnectorProxyBase + nodeID + "/connectors"
		suffix := strings.TrimPrefix(r.URL.Path, prefix)
		lower := strings.ToLower(suffix)
		if strings.Contains(lower, "..") || strings.Contains(lower, "%2e") || strings.Contains(lower, "%2f") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid connector path"})
			return
		}
		if suffix != "" && !strings.HasPrefix(suffix, "/") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid connector path"})
			return
		}

		target := pathpkg.Clean("/api/connectors" + suffix)
		if target != "/api/connectors" && !strings.HasPrefix(target, "/api/connectors/") {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid connector path"})
			return
		}
		server.ServeProxyPinned(w, r, nodeID, target)
	}
}
