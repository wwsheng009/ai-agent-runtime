package commands

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// localBackgroundJobProjectionTimeout bounds the best-effort projection and the
// immediate wake attempt so a slow store can never delay job terminal
// bookkeeping.
const localBackgroundJobProjectionTimeout = 5 * time.Second

// localBackgroundJobEventRelay late-binds the background manager's event
// handler to the assembled host. The manager is built before the host (its
// config needs no supervision access), while the projection needs the host's
// supervision control plane; the relay closes that ordering gap without
// touching the background package.
type localBackgroundJobEventRelay struct {
	mu   sync.RWMutex
	host *localChatRuntimeHost
}

func (r *localBackgroundJobEventRelay) bind(host *localChatRuntimeHost) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.host = host
	r.mu.Unlock()
}

func (r *localBackgroundJobEventRelay) handle(event background.JobEvent) {
	if r == nil {
		return
	}
	r.mu.RLock()
	host := r.host
	r.mu.RUnlock()
	if host == nil {
		return
	}
	host.handleLocalBackgroundEvent(event)
}

// handleLocalBackgroundEvent is the CLI host's background event callback. It is
// on the manager's high-frequency output path, so the terminal whitelist check
// comes first and the projection runs asynchronously.
func (h *localChatRuntimeHost) handleLocalBackgroundEvent(event background.JobEvent) {
	if h == nil || h.Supervision == nil || h.Supervision.Store == nil {
		return
	}
	if !supervision.IsTerminalBackgroundJobStatus(event.Type) {
		return
	}
	jobID := strings.TrimSpace(event.JobID)
	sessionID := strings.TrimSpace(localBackgroundJobEventString(event.Payload["session_id"]))
	if jobID == "" || sessionID == "" {
		return
	}
	// 立刻返回 job 终态推进；投影与投递在后台完成（best-effort，绝不阻塞 manager）。
	go h.projectLocalBackgroundJobTerminal(event, sessionID)
}

// projectLocalBackgroundJobTerminal records the durable inbox item + wake for a
// terminal job and then tries one delivery: a runnable session gets the digest
// in a fresh turn, a busy one keeps the wake durable for the next runnable
// transition or the natural-turn preflight (same semantics as the progress
// check).
func (h *localChatRuntimeHost) projectLocalBackgroundJobTerminal(event background.JobEvent, sessionID string) {
	if h == nil || h.Supervision == nil || h.Supervision.Store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), localBackgroundJobProjectionTimeout)
	defer cancel()

	command := ""
	if h.Background != nil {
		if job, err := h.Background.GetJob(ctx, strings.TrimSpace(event.JobID)); err == nil && job != nil {
			command = job.Command
		}
	}
	rootScopeID := h.localBackgroundJobRootScope(sessionID)
	_, err := supervision.ProjectBackgroundJobTerminal(ctx, h.Supervision.Store, h.Supervision.Wakes, supervision.BackgroundJobTerminalInput{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: sessionID,
		JobID:                 strings.TrimSpace(event.JobID),
		Status:                event.Type,
		Command:               command,
		ExitCode:              localBackgroundJobEventString(event.Payload["exit_code"]),
		ErrorCode:             localBackgroundJobEventString(event.Payload["error_code"]),
		Message:               localBackgroundJobEventString(event.Payload["message"]),
		CancelSource:          localBackgroundJobEventString(event.Payload["cancel_source"]),
		Epoch:                 localBackgroundJobEventEpoch(event),
	})
	if err != nil {
		// 投影失败不改变 job 终态；下一次 turn 的 preflight 仍能读到 job store。
		return
	}
	// busy/rate-limited 都是既有 durable 语义，不作为错误上报。
	_ = h.wakeSupervisedParent(ctx, sessionID, rootScopeID)
}

// localBackgroundJobRootScope 沿用巡查/preflight 的作用域口径：CLI host 绑定
// 单个根会话；无法解析时退化为 job 所属会话本身。
func (h *localChatRuntimeHost) localBackgroundJobRootScope(sessionID string) string {
	if root := strings.TrimSpace(h.localSupervisionProgressCheckSessionID()); root != "" {
		return root
	}
	return strings.TrimSpace(sessionID)
}

// localBackgroundJobEventEpoch 与 API 宿主同口径：事件时间戳即「本次终态」的
// 稳定身份，重放不复发、重跑换 epoch 可再唤一次。
func localBackgroundJobEventEpoch(event background.JobEvent) int64 {
	if !event.CreatedAt.IsZero() {
		if epoch := event.CreatedAt.UTC().UnixNano(); epoch > 0 {
			return epoch
		}
	}
	return time.Now().UTC().UnixNano()
}

func localBackgroundJobEventString(value interface{}) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprintf("%v", value)
}
