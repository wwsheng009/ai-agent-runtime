package main

import (
	"net/http"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/audit"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
)

// handleNodes serves the owner-protected node registry API:
//
//	GET    /api/nodes            list every registry entry (live + stale)
//	DELETE /api/nodes?stale=1    remove all offline snapshot (stale) nodes
//
// The bulk DELETE backs the console's one-click "clear offline snapshots"
// action. It requires an explicit ?stale=1 so a bare collection DELETE can
// never be mistaken for "wipe the registry": live nodes are never touched,
// because removing them would race their next heartbeat sync and only re-add
// the entry. Each removed node is audited individually (same node_removed
// event as the single-node DELETE) and subscribers are notified once per call.
func handleNodes(registry *tunnel.Registry, auditLog *audit.Log, clientIP func(*http.Request) string, onChanged func()) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{"nodes": registry.List()})
		case http.MethodDelete:
			if r.URL.Query().Get("stale") != "1" {
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": "DELETE /api/nodes requires ?stale=1 (bulk clear of offline snapshot nodes)",
				})
				return
			}
			removed := registry.RemoveStale()
			if len(removed) > 0 {
				ip := ""
				if clientIP != nil {
					ip = clientIP(r)
				}
				for _, nodeID := range removed {
					auditLog.Record(audit.Event{Type: audit.TypeNodeRemoved, Detail: nodeID, IP: ip})
				}
				if onChanged != nil {
					onChanged()
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{"removed": removed, "count": len(removed)})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}
