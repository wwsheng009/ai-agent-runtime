package commands

import (
	"net/http"
	"strings"
	"sync"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// ============================================================================
// GET /web/api/turn — turn 后验查询（P2）
//
// 配合异步 /web/api/input 使用：input 立即返回 queued，调用方随后可凭
// turn_id（或直接查询最近 turn）拿到终态、耗时与 token 用量，而不必轮询
// 整个屏幕快照。数据源为进程内 turn 记录环：
//   - 记录器按需（首次 invoke / 首次查询时）订阅会话 host EventBus；
//   - session_start 打开记录（running），session_end / session_interrupted
//     收尾（completed / failed / interrupted）并结算本轮 token 增量。
// 只保留最近 chatWebTurnRecordMax 条、chatWebTurnRecordTTL 内有效的记录。
// ============================================================================

const (
	chatWebTurnRecordMax = 128
	chatWebTurnRecordTTL = 30 * time.Minute
)

// chatWebTurnRecord 是单个 turn 的终态/进行态记录。
type chatWebTurnRecord struct {
	TurnID     string              `json:"turn_id"`
	SessionID  string              `json:"session_id,omitempty"`
	Status     string              `json:"status"` // running | completed | failed | interrupted
	StartedAt  string              `json:"started_at,omitempty"`
	FinishedAt string              `json:"finished_at,omitempty"`
	DurationMs int64               `json:"duration_ms,omitempty"`
	Steps      int                 `json:"steps,omitempty"`
	Error      string              `json:"error,omitempty"`
	Usage      *chatWebInvokeUsage `json:"usage,omitempty"`

	// 内部字段：本轮起点 token 计数，用于结算增量（不参与 JSON）。
	baseInput  int `json:"-"`
	baseOutput int `json:"-"`
	baseTotal  int `json:"-"`
	startedAt  time.Time
	finishedAt time.Time
}

// chatWebTurnQueryResponse 是 GET /web/api/turn 的响应体。
type chatWebTurnQueryResponse struct {
	Found   bool                `json:"found"`
	Turn    *chatWebTurnRecord  `json:"turn,omitempty"`
	Current *chatWebTurnCurrent `json:"current,omitempty"`
	Recent  []chatWebTurnRecord `json:"recent,omitempty"`
	Reason  string              `json:"reason,omitempty"`
}

// chatWebTurnCurrent 是当前活动 turn 的实时探测结果。
type chatWebTurnCurrent struct {
	TurnID         string `json:"turn_id,omitempty"`
	Busy           bool   `json:"busy"`
	PendingInputs  int    `json:"pending_inputs"`
	PendingApprove bool   `json:"pending_approval"`
	PendingAnswer  bool   `json:"pending_question"`
}

// ---------------------------------------------------------------------------
// 记录器与注册表
// ---------------------------------------------------------------------------

// chatWebTurnRecorder 订阅单个会话 host EventBus 并维护 turn 记录环。
type chatWebTurnRecorder struct {
	session *ChatSession

	mu      sync.Mutex
	records map[string]*chatWebTurnRecord // turn_id → record
	order   []string                      // 打开顺序（最新在末尾）
}

var chatWebTurnRecorders = struct {
	mu sync.Mutex
	m  map[*runtimeevents.Bus]*chatWebTurnRecorder
}{m: map[*runtimeevents.Bus]*chatWebTurnRecorder{}}

// ensureChatWebTurnRecorder 为会话 host EventBus 幂等安装 turn 记录器。
// 无 host / 无 bus（测试或降级形态）时静默跳过。
func ensureChatWebTurnRecorder(session *ChatSession) {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.EventBus == nil {
		return
	}
	bus := session.LocalRuntimeHost.EventBus
	chatWebTurnRecorders.mu.Lock()
	if _, ok := chatWebTurnRecorders.m[bus]; ok {
		chatWebTurnRecorders.mu.Unlock()
		return
	}
	recorder := &chatWebTurnRecorder{session: session, records: map[string]*chatWebTurnRecord{}}
	chatWebTurnRecorders.m[bus] = recorder
	chatWebTurnRecorders.mu.Unlock()
	bus.SubscribeCancelable("", recorder.observe)
}

// recorderForSession 返回会话对应 bus 上已安装的记录器（可能为 nil）。
func recorderForSession(session *ChatSession) *chatWebTurnRecorder {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.EventBus == nil {
		return nil
	}
	chatWebTurnRecorders.mu.Lock()
	defer chatWebTurnRecorders.mu.Unlock()
	return chatWebTurnRecorders.m[session.LocalRuntimeHost.EventBus]
}

// observe 只做加锁更新，绝不阻塞（Bus.Publish 同步调用）。
func (r *chatWebTurnRecorder) observe(event runtimeevents.Event) {
	if r == nil {
		return
	}
	switch event.Type {
	case runtimechat.EventSessionStart:
		turnID := strings.TrimSpace(payloadStringValue(event.Payload["turn_id"]))
		if turnID == "" {
			return
		}
		r.mu.Lock()
		record := &chatWebTurnRecord{
			TurnID:    turnID,
			SessionID: event.SessionID,
			Status:    "running",
			StartedAt: time.Now().UTC().Format(time.RFC3339),
			startedAt: time.Now(),
		}
		if r.session != nil {
			record.baseInput = r.session.InputTokenCount
			record.baseOutput = r.session.OutputTokenCount
			record.baseTotal = r.session.TokenCount
		}
		r.records[turnID] = record
		r.order = append(r.order, turnID)
		r.pruneLocked()
		r.mu.Unlock()

	case runtimechat.EventSessionEnd:
		turnID := strings.TrimSpace(payloadStringValue(event.Payload["turn_id"]))
		status := "completed"
		if success, ok := event.Payload["success"].(bool); ok && !success {
			status = "failed"
		}
		if raw := strings.TrimSpace(payloadStringValue(event.Payload["status"])); raw != "" &&
			strings.EqualFold(raw, "interrupted") {
			status = "interrupted"
		}
		r.finish(turnID, event.SessionID, status,
			strings.TrimSpace(payloadStringValue(event.Payload["error"])),
			chatWebPayloadInt(event.Payload["steps"]))

	case runtimechat.EventSessionInterrupted:
		r.finish("", event.SessionID, "interrupted", "", 0)
	}
}

// finish 收尾指定 turn；turnID 为空时收尾该会话最近一条 running 记录。
func (r *chatWebTurnRecorder) finish(turnID, sessionID, status, errText string, steps int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	record := r.records[turnID]
	if turnID == "" || record == nil || record.Status != "running" {
		record = nil
		for i := len(r.order) - 1; i >= 0; i-- {
			candidate := r.records[r.order[i]]
			if candidate == nil || candidate.Status != "running" {
				continue
			}
			if sessionID != "" && candidate.SessionID != "" && candidate.SessionID != sessionID {
				continue
			}
			record = candidate
			break
		}
	}
	if record == nil || record.Status != "running" {
		return
	}
	now := time.Now()
	record.Status = status
	record.FinishedAt = now.UTC().Format(time.RFC3339)
	record.finishedAt = now
	record.Error = errText
	record.Steps = steps
	if !record.startedAt.IsZero() {
		record.DurationMs = now.Sub(record.startedAt).Milliseconds()
	}
	if r.session != nil {
		usage := &chatWebInvokeUsage{
			InputTokens:         r.session.InputTokenCount - record.baseInput,
			OutputTokens:        r.session.OutputTokenCount - record.baseOutput,
			TotalTokens:         r.session.TokenCount - record.baseTotal,
			ContextTokens:       r.session.ContextTokenCount,
			ContextWindowTokens: r.session.ContextWindowTokenCount,
		}
		if usage.InputTokens < 0 {
			usage.InputTokens = 0
		}
		if usage.OutputTokens < 0 {
			usage.OutputTokens = 0
		}
		if usage.TotalTokens < 0 {
			usage.TotalTokens = 0
		}
		if usage.InputTokens != 0 || usage.OutputTokens != 0 || usage.TotalTokens != 0 ||
			usage.ContextTokens != 0 {
			record.Usage = usage
		}
	}
}

// pruneLocked 淘汰过期与超量记录（调用方持锁）。
func (r *chatWebTurnRecorder) pruneLocked() {
	cutoff := time.Now().Add(-chatWebTurnRecordTTL)
	for i, turnID := range r.order {
		record := r.records[turnID]
		if record == nil {
			continue
		}
		if record.Status != "running" && !record.finishedAt.IsZero() && record.finishedAt.Before(cutoff) {
			delete(r.records, turnID)
			r.order[i] = ""
		}
	}
	if len(r.order) > chatWebTurnRecordMax {
		excess := len(r.order) - chatWebTurnRecordMax
		kept := r.order[:0]
		for _, turnID := range r.order {
			if excess > 0 && turnID != "" {
				if record := r.records[turnID]; record != nil && record.Status != "running" {
					delete(r.records, turnID)
					excess--
					continue
				}
			}
			if turnID != "" {
				kept = append(kept, turnID)
			}
		}
		r.order = kept
	}
}

// lookup 返回指定 turn 的记录副本；未找到返回 nil。
func (r *chatWebTurnRecorder) lookup(turnID string) *chatWebTurnRecord {
	if r == nil || turnID == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	record := r.records[turnID]
	if record == nil {
		return nil
	}
	clone := *record
	return &clone
}

// recent 返回最近 limit 条记录（最新在前）的副本。
func (r *chatWebTurnRecorder) recent(limit int) []chatWebTurnRecord {
	if r == nil || limit <= 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]chatWebTurnRecord, 0, limit)
	for i := len(r.order) - 1; i >= 0 && len(out) < limit; i-- {
		if r.order[i] == "" {
			continue
		}
		if record := r.records[r.order[i]]; record != nil {
			out = append(out, *record)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// HTTP handler
// ---------------------------------------------------------------------------

// HandleChatWebAPITurn 处理 turn 后验查询：
//
//	GET /web/api/turn?id={turn_id}  → 单条记录 + 当前活动 turn 实时探测
//	GET /web/api/turn               → 当前活动 turn + 最近 20 条记录
func HandleChatWebAPITurn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"status": "rejected",
			"reason": "method not allowed",
		})
		return
	}
	session := chatWebSession()
	if session == nil {
		writeWebAPIJSON(w, http.StatusServiceUnavailable, &chatWebTurnQueryResponse{
			Found:  false,
			Reason: "no active chat session",
		})
		return
	}
	ensureChatWebTurnRecorder(session)
	recorder := recorderForSession(session)

	sessionID, turnID, busy, approval, question := chatWebInvokeProbeFn(session)
	pending := 0
	if session.InputQueue != nil {
		pending = session.InputQueue.queuedSubmissionCount()
	}
	current := &chatWebTurnCurrent{
		TurnID:         turnID,
		Busy:           busy,
		PendingInputs:  pending,
		PendingApprove: approval != nil,
		PendingAnswer:  question != nil,
	}

	queryID := strings.TrimSpace(r.URL.Query().Get("id"))
	resp := &chatWebTurnQueryResponse{Current: current}
	if queryID != "" {
		if record := recorder.lookup(queryID); record != nil {
			resp.Found = true
			resp.Turn = record
		} else if turnID != "" && queryID == turnID {
			resp.Found = true
			resp.Turn = &chatWebTurnRecord{
				TurnID:    turnID,
				SessionID: sessionID,
				Status:    "running",
				StartedAt: time.Now().UTC().Format(time.RFC3339),
			}
		} else {
			resp.Reason = "turn not recorded (records keep the most recent " +
				"turns for up to 30m)"
		}
	} else {
		resp.Recent = recorder.recent(20)
	}
	writeWebAPIJSON(w, http.StatusOK, resp)
}

// chatWebPayloadInt 从事件 payload 中读取整数值（JSON 数字可能是 float64）。
func chatWebPayloadInt(value interface{}) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case float32:
		return int(v)
	default:
		return 0
	}
}
