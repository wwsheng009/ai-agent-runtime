package commands

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
)

// ============================================================================
// /web/api/knowledge/status —— aicli micro web client 知识层状态端点
// （knowledge.status.v1）。
//
// 数据源与 `aicli knowledge status`、runtime-server 的
// GET /api/runtime/knowledge/status **完全同源同形**：直接返回
// knowledge.StatusReport（同一字段集，不做二次包装），避免三处漂移。
//
// 降级语义（与 runtime-server 的 knowledge_handlers.go 一致）：
//   - 未启用（mode=off）/ 未接线（启动期降级、无会话）→ 200 + mode=off 的
//     最小载荷：off 是合法状态，404/503 只会迫使调用方去猜；
//   - 只读：不触发索引、不写库；reader 角色同样可读，并以 degraded_reason
//     标注只读与锁持有者 pid；
//   - 查询失败 → 500 + {error:{code,message}}（与 /fs、/git 同形）。
//
// 端点存在动机（Phase 5 真实会话 E2E 登记①）：进程内已有 StatusReport
// （含 Watch/GC/LockWait/LastJob），但聊天会话的 web 面此前没有任何读取入口，
// 真实会话里无法确认 mode/watch/queue 的实际状态。
//
// 路由（GET；子路径手动解析，与 /web/api/lsp 等同形）：
//
//	GET /web/api/knowledge        等价于 /status
//	GET /web/api/knowledge/status 知识层状态快照（只读）
// ============================================================================

const (
	// chatWebKnowledgeMethodCode 非 GET 请求。
	chatWebKnowledgeMethodCode = "knowledge_method_not_allowed"
	// chatWebKnowledgeNotFoundCode 未知子路径。
	chatWebKnowledgeNotFoundCode = "knowledge_not_found"
	// chatWebKnowledgeStatusTimeout 状态查询的硬上限：只读查询（SchemaVersion /
	// FindWorkspace / Stats / LatestIndexJob）在百万行级库上是毫秒级，10s 是
	// 兜底，防止 store 异常时把 HTTP 请求挂死。
	chatWebKnowledgeStatusTimeout = 10 * time.Second
)

// HandleChatWebAPIKnowledge 知识层状态 HTTP 入口（挂载见 pprof.go）。
func HandleChatWebAPIKnowledge(w http.ResponseWriter, r *http.Request) {
	if r == nil || r.Method != http.MethodGet {
		writeWebAPIJSON(w, http.StatusMethodNotAllowed,
			chatWebKnowledgeErrorBody(chatWebKnowledgeMethodCode, "method not allowed"))
		return
	}
	switch chatWebKnowledgeSubPath(r.URL.Path) {
	case "", "status":
		ctx, cancel := context.WithTimeout(r.Context(), chatWebKnowledgeStatusTimeout)
		defer cancel()
		report, err := chatWebKnowledgeStatusReportFor(ctx, chatWebSession())
		if err != nil {
			writeWebAPIJSON(w, http.StatusInternalServerError,
				chatWebKnowledgeErrorBody("knowledge_status_failed", err.Error()))
			return
		}
		writeWebAPIJSON(w, http.StatusOK, report)
	default:
		writeWebAPIJSON(w, http.StatusNotFound,
			chatWebKnowledgeErrorBody(chatWebKnowledgeNotFoundCode, "unknown knowledge subpath"))
	}
}

// chatWebKnowledgeStatusReportFor 读取当前会话的知识层状态；会话缺失 /
// mode=off（Activation 为 nil）时返回 mode=off 的最小载荷
// （knowledge.Activation.Status 本身 nil-safe，这里只兜底 session 为 nil 的路径）。
func chatWebKnowledgeStatusReportFor(ctx context.Context, session *ChatSession) (knowledge.StatusReport, error) {
	if session == nil {
		var activation *knowledge.Activation
		return activation.Status(ctx)
	}
	return session.Knowledge.Status(ctx)
}

// chatWebKnowledgeSubPath 返回 /web/api/knowledge 之后的子路径（无首尾斜杠）。
func chatWebKnowledgeSubPath(path string) string {
	return strings.Trim(strings.TrimPrefix(path, ChatWebAPIKnowledgePath), "/")
}

// chatWebKnowledgeErrorBody 稳定错误 envelope（与其它 /web/api 端点族同形）。
func chatWebKnowledgeErrorBody(code, message string) map[string]interface{} {
	return map[string]interface{}{
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	}
}
