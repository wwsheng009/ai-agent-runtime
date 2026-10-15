package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
)

// ============================================================================
// POST /web/api/schedule-deliver —— 调度投递接收端（S2b，联盟设计 §3.3 L2）
//
// driver（daemon / runtime-server）经 mesh.Call(op=schedule.deliver) 把到期提醒
// 就近投进本进程会话；本端点：
//  1. 校验目标会话就是本进程当前会话（aicli 单会话进程；不匹配回 409，
//     调用方据此降级到 L3/L4/L5）；
//  2. 按交付账本（occurrence 键）去重——已送达直接回 delivered（重放恒幂等）；
//  3. 否则把提醒注入会话输入队列（与 /web/api/input 同路径），成功后落账。
// ============================================================================

type chatWebScheduleDeliverRequest struct {
	SessionID   string   `json:"session_id"`
	Text        string   `json:"text"`
	Occurrences []string `json:"occurrences,omitempty"`
}

// webScheduleFuncDeliverer 把"注入一条 prompt 进当前会话"适配成 schedule.Deliverer。
type webScheduleFuncDeliverer func(sessionID, text string) error

func (f webScheduleFuncDeliverer) Deliver(_ context.Context, sessionID, text string) error {
	return f(sessionID, text)
}

// HandleChatWebAPIScheduleDeliver 处理调度投递。写 op——仅回环 + allow_write=true
// 由 mesh 调用层强制；本函数只做业务语义（会话归属 + 账本去重 + 注入）。
func HandleChatWebAPIScheduleDeliver(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"status": "rejected",
			"reason": "method not allowed",
		})
		return
	}
	session := chatWebSession()
	if session == nil {
		writeWebAPIJSON(w, http.StatusConflict, map[string]string{
			"status": "error",
			"reason": "no active chat session",
		})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "read body: " + err.Error(),
		})
		return
	}
	var req chatWebScheduleDeliverRequest
	if unmarshalErr := json.Unmarshal(body, &req); unmarshalErr != nil {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "invalid JSON body: " + unmarshalErr.Error(),
		})
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "text is required",
		})
		return
	}
	currentID := strings.TrimSpace(currentRuntimeSessionID(session))
	target := strings.TrimSpace(req.SessionID)
	if target != "" && target != currentID {
		writeWebAPIJSON(w, http.StatusConflict, map[string]string{
			"status": "error",
			"reason": fmt.Sprintf("session %s is not served by this node (%s)", target, currentID),
		})
		return
	}
	if target == "" {
		target = currentID
	}

	submit := webScheduleFuncDeliverer(func(_ string, prompt string) error {
		if _, ok := injectChatWebPrompt(session, prompt); !ok {
			return fmt.Errorf("input queue unavailable")
		}
		return nil
	})
	deliverer := schedule.Deliverer(submit)
	if ledger := scheduleLedgerForSession(session); ledger != nil {
		deliverer = schedule.GuardedDeliverer{
			Inner:  submit,
			Ledger: ledger,
			Logf: func(format string, args ...any) {
				fmt.Fprintf(os.Stderr, "schedule-deliver: "+format+"\n", args...)
			},
		}
	} else {
		// 去重不可用必须可见：静默跳过会让故障排查变成"提醒收了两遍/账本没落"。
		reason := "session-nil"
		if session != nil {
			switch {
			case session.LocalRuntimeHost == nil:
				reason = "local-runtime-host-nil"
			case session.LocalRuntimeHost.Schedule == nil:
				reason = "schedule-service-nil"
			default:
				reason = fmt.Sprintf("store-type=%T", session.LocalRuntimeHost.Schedule.Store())
			}
		}
		fmt.Fprintf(os.Stderr,
			"schedule-deliver: ledger unavailable (%s) for session %s; occurrence dedup disabled\n", reason, currentID)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := schedule.DeliverEnvelopeTo(ctx, deliverer, schedule.DeliveryEnvelope{
		SessionID:   target,
		Text:        text,
		Occurrences: req.Occurrences,
	}); err != nil {
		writeWebAPIJSON(w, http.StatusInternalServerError, map[string]string{
			"status": "error",
			"reason": err.Error(),
		})
		return
	}
	writeWebAPIJSON(w, http.StatusOK, map[string]any{
		"status":     "delivered",
		"session_id": currentID,
	})
}

// scheduleLedgerForSession 返回本会话宿主的交付账本（未启用 schedule → nil）。
// 优先任务会话自身的 host；web/mesh 处理器拿到的早期会话快照没有 host 时，
// 退回进程级注册位（见 chat_schedule_host.go 的 processScheduleService）。
func scheduleLedgerForSession(session *ChatSession) schedule.OccurrenceLedger {
	if session != nil && session.LocalRuntimeHost != nil && session.LocalRuntimeHost.Schedule != nil {
		if ledger, ok := session.LocalRuntimeHost.Schedule.Store().(schedule.OccurrenceLedger); ok {
			return ledger
		}
	}
	return processScheduleLedger()
}
