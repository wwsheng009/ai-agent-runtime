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
	family := supervision.ClassifyBackgroundJobEvent(event.Type)
	if family == supervision.BackgroundJobFamilyNone {
		return
	}
	jobID := strings.TrimSpace(event.JobID)
	sessionID := strings.TrimSpace(localBackgroundJobEventString(event.Payload["session_id"]))
	if jobID == "" || sessionID == "" {
		return
	}
	if family == supervision.BackgroundJobFamilyMonitor {
		// 巡检到点：progress 类唤醒 + 一次即时投递尝试；busy/限流保持 durable。
		go h.projectLocalBackgroundJobMonitor(event, sessionID)
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
	observed := false
	if h.Background != nil {
		if job, err := h.Background.GetJob(ctx, strings.TrimSpace(event.JobID)); err == nil && job != nil {
			command = job.Command
			observed = background.TerminalObserved(job)
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
		Observed:              observed,
		Epoch:                 localBackgroundJobEventEpoch(event),
	})
	if err != nil {
		// 投影失败不改变 job 终态；下一次 turn 的 preflight 仍能读到 job store。
		return
	}
	if observed {
		// 终态证据已由 wait 结果送达模型：不再补一次投递；其他待投递 wake
		// 仍由 turn 边界 / preflight 负责。
		return
	}
	// busy/rate-limited 都是既有 durable 语义，不作为错误上报。
	_ = h.wakeSupervisedParent(ctx, sessionID, rootScopeID)
}

// projectLocalBackgroundJobMonitor 处理巡检到点（task_monitor）：把 job 当前状态
// 投影成 durable 记录 + progress 类唤醒，然后尝试一次即时投递；busy/限流保持
// durable，由 turn 边界或 preflight 补投。best-effort，绝不阻塞 manager。
func (h *localChatRuntimeHost) projectLocalBackgroundJobMonitor(event background.JobEvent, sessionID string) {
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
	_, err := supervision.ProjectBackgroundJobMonitor(ctx, h.Supervision.Store, h.Supervision.Wakes, supervision.BackgroundJobMonitorInput{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: sessionID,
		JobID:                 strings.TrimSpace(event.JobID),
		Status:                localBackgroundJobEventString(event.Payload["status"]),
		Command:               command,
		Elapsed:               localBackgroundJobPayloadDuration(event.Payload["elapsed_ms"]),
		CheckAfter:            localBackgroundJobPayloadDuration(event.Payload["check_after_ms"]),
		MaxDuration:           localBackgroundJobPayloadDuration(event.Payload["max_duration_ms"]),
		Epoch:                 localBackgroundJobEventEpoch(event),
	})
	if err != nil {
		// 投影失败不改变 job 状态；下一次 turn 的 preflight 仍能读到 job store。
		return
	}
	_ = h.wakeSupervisedParent(ctx, sessionID, rootScopeID)
}

// localBackgroundJobPayloadDuration 把 manager 事件里的毫秒字段读成 duration。
func localBackgroundJobPayloadDuration(value interface{}) time.Duration {
	switch typed := value.(type) {
	case time.Duration:
		return typed
	case int64:
		return time.Duration(typed) * time.Millisecond
	case int:
		return time.Duration(typed) * time.Millisecond
	case float64:
		return time.Duration(typed) * time.Millisecond
	default:
		return 0
	}
}

// disarmLocalChatSessionMonitors 会话关闭时停掉本会话的巡检计时器：投递目标
// （本会话的下一轮 turn）已经不存在，迟到的 check 只会给不存在的会话补记录。
func disarmLocalChatSessionMonitors(session *ChatSession) int {
	if session == nil || session.RuntimeSession == nil {
		return 0
	}
	host := session.LocalRuntimeHost
	if host == nil || host.Background == nil {
		return 0
	}
	sessionID := strings.TrimSpace(session.RuntimeSession.ID)
	if sessionID == "" {
		return 0
	}
	return host.Background.CancelSessionMonitors(sessionID)
}

// localBackgroundJobRootScope 沿用巡查/preflight 的作用域口径：CLI host 绑定
// 单个根会话；无法解析时退化为 job 所属会话本身。
func (h *localChatRuntimeHost) localBackgroundJobRootScope(sessionID string) string {
	if root := strings.TrimSpace(h.localSupervisionProgressCheckSessionID()); root != "" {
		return root
	}
	return strings.TrimSpace(sessionID)
}

// resolveLocalChatSessionPendingWakes 在会话关闭时作废本会话的待投递 wake 义务：
// 投递目标（本会话的下一轮 turn）已不存在，而证据仍在 notification / job store 里，
// 由 resume 后首个自然 turn 的 preflight digest 呈现。不清理的话，恢复会话后
// turn 结束会再补投一轮已经过时的 digest。
//
// best-effort：退出路径上的失败只影响一次冗余投递，绝不能让会话关不掉。
func resolveLocalChatSessionPendingWakes(session *ChatSession) int {
	if session == nil || session.RuntimeSession == nil {
		return 0
	}
	host := session.LocalRuntimeHost
	if host == nil || host.Supervision == nil || host.Supervision.Store == nil {
		return 0
	}
	// 冷 store 不在退出路径上打开：本进程从未写过监督面时，与既有「退出不冷开
	// 存储」的约定保持一致（上一进程的遗留 wake 由下次真正使用监督面时再过期）。
	if opened, ok := host.Supervision.Store.(interface{ Opened() bool }); ok && !opened.Opened() {
		return 0
	}
	sessionID := strings.TrimSpace(session.RuntimeSession.ID)
	if sessionID == "" {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), localBackgroundJobProjectionTimeout)
	defer cancel()
	resolved, err := supervision.ResolvePendingWakesForSession(ctx, host.Supervision.Store, host.localBackgroundJobRootScope(sessionID), sessionID)
	if err != nil {
		return resolved
	}
	return resolved
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
