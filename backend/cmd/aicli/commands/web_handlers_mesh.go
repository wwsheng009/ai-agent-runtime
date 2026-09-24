package commands

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// ChatWebAPIHealthResponse 是 GET /web/api/health 的响应体（架构 §5.2）。
//
// 契约：不依赖会话、不依赖渲染器——无会话时同样返回 200，仅
// session_active=false。字段被网格探活、外部脚本就绪等待与
// aicli-mesh doctor 复用，因此保持稳定且极轻量（不扫盘、不派生重活）。
type ChatWebAPIHealthResponse struct {
	Available     bool   `json:"available"`
	NodeID        string `json:"node_id,omitempty"`
	PID           int    `json:"pid"`
	UptimeSec     int64  `json:"uptime_sec"`
	SessionActive bool   `json:"session_active"`
	Busy          bool   `json:"busy"`
	MeshReady     bool   `json:"mesh_ready"`
}

// HandleChatWebAPIHealth 处理 GET /web/api/health（架构 §5.2 存活探针）。
//
// 数据源是进程内实时态：网格宿主的内存快照（可用时）+ 当前 chat 会话兜底，
// 因此高频探活也不会触盘或依赖 UI 渲染面。
func HandleChatWebAPIHealth(w http.ResponseWriter, r *http.Request) {
	writeWebAPIJSON(w, http.StatusOK, buildChatWebAPIHealth())
}

func buildChatWebAPIHealth() ChatWebAPIHealthResponse {
	response := ChatWebAPIHealthResponse{
		Available: true,
		PID:       os.Getpid(),
		UptimeSec: int64(time.Since(chatDebugProcessStartedAt).Round(time.Second) / time.Second),
	}
	// 网格宿主存在时以档案内存快照为准（与 peers 视图同源，避免两处口径漂移）；
	// 网格关闭（--mesh=false）或会话尚未同步到网格时，退回进程内 chat 会话。
	if host := mesh.Current(); host != nil {
		record := host.RecordSnapshot()
		response.NodeID = record.NodeID
		response.MeshReady = host.Paths().Enabled()
		if record.Session != nil {
			response.SessionActive = strings.TrimSpace(record.Session.ID) != ""
			response.Busy = record.Session.Busy
		}
	}
	if !response.SessionActive {
		if session := chatWebSession(); session != nil {
			response.SessionActive = strings.TrimSpace(currentRuntimeSessionID(session)) != ""
			if actor := chatWebSessionActor(session); actor != nil {
				if state := actor.State(); state != nil {
					response.Busy = state.Summary().Busy()
				}
			}
		}
	}
	return response
}
