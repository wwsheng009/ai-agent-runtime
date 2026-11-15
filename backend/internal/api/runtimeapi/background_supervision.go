package runtimeapi

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// backgroundJobTerminalProjectionTimeout bounds the best-effort supervision
// projection so a slow store can never delay job terminal bookkeeping.
const backgroundJobTerminalProjectionTimeout = 5 * time.Second

// isTerminalBackgroundJobEventType is the API host's whitelist check. The
// disposition itself lives in the supervision package so every host produces
// the same inbox item and wake identity.
func isTerminalBackgroundJobEventType(eventType string) bool {
	return supervision.IsTerminalBackgroundJobStatus(eventType)
}

// projectBackgroundJobTerminal is the API host adapter: it resolves the owning
// session's root scope, enriches the projection with the job's command and
// event payload, then delegates to the host-neutral supervision projection.
// It is best-effort and asynchronous: failures never change the job outcome.
func (h *Handler) projectBackgroundJobTerminal(parent context.Context, event background.JobEvent, sessionID string) {
	jobID := strings.TrimSpace(event.JobID)
	sessionID = strings.TrimSpace(sessionID)
	if h == nil || jobID == "" || sessionID == "" {
		return
	}
	store := h.getSupervisionStore()
	if store == nil {
		return
	}
	if !supervision.IsTerminalBackgroundJobStatus(event.Type) {
		return
	}

	ctx := parent
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, backgroundJobTerminalProjectionTimeout)
	defer cancel()

	command, observed := h.backgroundJobDigestInfo(ctx, jobID)
	origin := h.backgroundJobOrigin(ctx, jobID)
	rootScopeID := h.backgroundJobRootScope(ctx, sessionID)
	_, err := supervision.ProjectBackgroundJobTerminal(ctx, store, h.getSupervisionWakeScheduler(),
		supervision.BackgroundJobTerminalInputFromEvent(
			event, sessionID, rootScopeID, command, observed, origin))
	if err != nil || observed {
		return
	}
	// Same one-shot delivery attempt as the CLI host. Busy/rate-limited
	// parents keep the durable wake for the next runnable transition.
	_ = h.drainSupervisedParentWake(ctx, rootScopeID, sessionID)
}

// projectBackgroundJobMonitor is the API host adapter for a scheduled job check:
// it enriches the projection with the job's command and delegates to the shared
// monitor projection (durable inbox item + progress-class wake), so a waiting
// session is nudged once instead of polling. Best-effort like the terminal path:
// a supervision outage never changes the job outcome.
func (h *Handler) projectBackgroundJobMonitor(parent context.Context, event background.JobEvent, sessionID string) {
	jobID := strings.TrimSpace(event.JobID)
	sessionID = strings.TrimSpace(sessionID)
	if h == nil || jobID == "" || sessionID == "" {
		return
	}
	if !supervision.IsBackgroundJobMonitorCheck(event.Type) {
		return
	}
	store := h.getSupervisionStore()
	if store == nil {
		return
	}
	ctx := parent
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, backgroundJobTerminalProjectionTimeout)
	defer cancel()

	command, _ := h.backgroundJobDigestInfo(ctx, jobID)
	origin := h.backgroundJobOrigin(ctx, jobID)
	rootScopeID := h.backgroundJobRootScope(ctx, sessionID)
	_, err := supervision.ProjectBackgroundJobMonitor(ctx, store, h.getSupervisionWakeScheduler(),
		supervision.BackgroundJobMonitorInputFromEvent(event, sessionID, rootScopeID, command, origin))
	if err != nil {
		return
	}
	_ = h.drainSupervisedParentWake(ctx, rootScopeID, sessionID)
}

// releaseSessionSupervisionObligations 在会话关闭/删除时收口监督面：作废本会话的
// 待投递 wake 义务（投递目标已不存在；证据仍在 notification / job store，由恢复后
// 首个 turn 的 preflight digest 呈现），并解除该会话的巡检计时器。不清理的话，
// 会话恢复后 turn 结束会补投一轮已经过时的 digest，而巡检到点只会给不存在的会话
// 记一条记录。
//
// best-effort：监督面未接线、store 未注入或清理失败都不改变会话的关闭/删除结果。
func (h *Handler) releaseSessionSupervisionObligations(parent context.Context, sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if h == nil || sessionID == "" {
		return
	}
	// 巡检计时器是纯进程内资源：监督面没落库也要解除。
	h.backgroundMu.Lock()
	manager := h.backgroundManager
	h.backgroundMu.Unlock()
	if manager != nil {
		manager.CancelSessionMonitors(sessionID)
	}
	store := h.getSupervisionStore()
	if store == nil {
		return
	}
	ctx := parent
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, backgroundJobTerminalProjectionTimeout)
	defer cancel()
	_, _ = supervision.ResolvePendingWakesForSession(ctx, store, h.backgroundJobRootScope(ctx, sessionID), sessionID)
}

// backgroundJobRootScope resolves the budget/inbox root for the owning
// session. A session lookup failure degrades to the session id itself, which
// keeps the projection usable in hosts without a session manager.
func (h *Handler) backgroundJobRootScope(ctx context.Context, sessionID string) string {
	if h != nil && h.sessionManager != nil {
		if session, err := h.sessionManager.Get(ctx, sessionID); err == nil && session != nil {
			if root := strings.TrimSpace(apiAgentRootSessionID(session, sessionID)); root != "" {
				return root
			}
		}
	}
	return sessionID
}

// backgroundJobDigestInfo reads the job's command for the digest reason plus
// whether the terminal state was already observed by an in-flight task_output
// wait (in which case the projection records the inbox item without scheduling
// a redundant wake). The manager is cached on the handler and may not exist yet
// (or at all) in tests and embedded hosts; a missing manager yields no digest
// info at all.
func (h *Handler) backgroundJobDigestInfo(ctx context.Context, jobID string) (string, bool) {
	if h == nil {
		return "", false
	}
	h.backgroundMu.Lock()
	manager := h.backgroundManager
	h.backgroundMu.Unlock()
	if manager == nil {
		return "", false
	}
	job, err := manager.GetJob(ctx, jobID)
	if err != nil || job == nil {
		return "", false
	}
	return job.Command, background.TerminalObserved(job)
}

// backgroundJobOrigin reads the parent identity captured at job submission.
// Missing metadata is a legacy/direct job and deliberately remains zero, which
// forces the projection's detached notification-only fallback.
func (h *Handler) backgroundJobOrigin(ctx context.Context, jobID string) background.JobOrigin {
	if h == nil {
		return background.JobOrigin{}
	}
	h.backgroundMu.Lock()
	manager := h.backgroundManager
	h.backgroundMu.Unlock()
	if manager == nil {
		return background.JobOrigin{}
	}
	job, err := manager.GetJob(ctx, jobID)
	if err != nil || job == nil {
		return background.JobOrigin{}
	}
	return background.JobOriginFromMetadata(job.Metadata)
}
