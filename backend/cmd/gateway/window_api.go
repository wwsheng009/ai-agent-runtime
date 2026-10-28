package main

import (
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/audit"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// handleNodeWindowAction 把一条「窗口动作」排给某个节点，随该节点下一次
// HEARTBEAT_ACK 下发，由节点在本机执行：
//
//	POST /api/nodes/{node_id}/sessions/{session_id}/window?action=open-terminal|resume|close
//	POST /api/nodes/{node_id}/window?action=new
//
// 为什么走「排队 + 心跳下发」而不是网关直连节点：反向隧道本来就是节点出站建立的
// （agent→gateway），网关没有入站监听节点的能力；watch/unwatch 用的就是同一条通道，
// 窗口控制再单独搞一套只会让协议长出第二套方言。
//
// 语义边界：
//   - 202 Accepted 只表示「已排进队列」，不表示窗口已经开出来——执行结果由节点
//     自己的会话上报体现（标题/状态/在场会随下一次 SESSION_UPDATE 回到控制台）；
//   - 节点离线 / 动作未知 / 队列已满 → 4xx + 一句话原因，绝不静默接受；
//   - 无论成功或被拒都落审计（谁、哪台节点、哪个会话、什么动作）。
func handleNodeWindowAction(server *tunnel.Server, auditLog *audit.Log, clientIP func(*http.Request) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		vars := mux.Vars(r)
		nodeID := strings.TrimSpace(vars["node_id"])
		sessionID := strings.TrimSpace(vars["session_id"])
		kind := strings.ToLower(strings.TrimSpace(firstNonEmpty(
			r.URL.Query().Get("action"), r.URL.Query().Get("kind"))))

		// 不带会话 ID 的路由只服务「新建窗口」：否则 close/resume 会被静默当成
		// 「对本机所有会话做点什么」，那是最危险的误读。
		if sessionID == "" && kind != wire.WindowActionNew {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":  "this route only accepts action=new; session actions go to /sessions/{session_id}/window",
				"action": kind,
			})
			return
		}

		ok, reason := server.QueueWindowAction(nodeID, kind, sessionID)
		ip := ""
		if clientIP != nil {
			ip = clientIP(r)
		}
		auditLog.Record(audit.Event{
			Type:   audit.TypeWindowAction,
			Detail: nodeID + " " + kind + " " + sessionID + " " + windowAuditSuffix(ok, reason),
			IP:     ip,
		})
		if !ok {
			status := http.StatusBadRequest
			switch {
			case strings.Contains(reason, "offline"):
				status = http.StatusConflict
			case strings.Contains(reason, "too many"):
				status = http.StatusTooManyRequests
			}
			writeJSON(w, status, map[string]any{"error": reason, "node": nodeID, "action": kind, "session": sessionID})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{
			"queued":   true,
			"node":     nodeID,
			"action":   kind,
			"session":  sessionID,
			"delivery": "next heartbeat (HEARTBEAT_ACK pending)",
		})
	}
}

func windowAuditSuffix(ok bool, reason string) string {
	if ok {
		return "queued"
	}
	return "refused:" + reason
}

// firstNonEmpty 返回第一个非空字符串（都空时返回 ""）。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
