package skills

import (
	"context"
	stderrors "errors"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

const (
	executionDiagnosticsSchemaVersion = 1
	executionDiagnosticsSourceTimeout = 2 * time.Second
	executionDiagnosticsSessionPage   = 1000
	// executionDiagnosticsSnapshotTimeout 是整个快照的硬上限。单个 source
	// 的查询已受 executionDiagnosticsSourceTimeout 约束，但底层 sqlite 打开/
	// 锁重试可能无视 ctx（历史实现用 context.Background()），导致单个
	// source 阻塞远超其 2s 预算。若没有这一层，/api/runtime/health 会跟着
	// wait.Wait() 一起挂起。该值应明显大于单个 source 的超时，给正常路径
	// 留足余量，同时保证 health 端点有界返回。
	executionDiagnosticsSnapshotTimeout = 5 * time.Second

	executionDiagnosticsSourceOK          = "ok"
	executionDiagnosticsSourceDegraded    = "degraded"
	executionDiagnosticsSourceUnavailable = "unavailable"
)

type executionDiagnosticsSessionLister interface {
	ListAll(ctx context.Context, limit, offset int) ([]*chat.Session, error)
}

type executionDiagnosticsBackgroundReader interface {
	ListJobs(ctx context.Context, filter background.JobFilter) ([]background.Job, error)
}

type executionDiagnosticsTeamReader interface {
	ListTeams(ctx context.Context, filter team.TeamFilter) ([]team.Team, error)
	ListTasks(ctx context.Context, filter team.TaskFilter) ([]team.Task, error)
}

type executionDiagnosticsAgentReader interface {
	ListAgentControlAgents(ctx context.Context, filter agentcontrol.AgentFilter) ([]agentcontrol.AgentRecord, error)
}

type executionDiagnosticsResult struct {
	source map[string]interface{}
	counts map[string]int
}

type executionDiagnosticsTeamResult struct {
	source             map[string]interface{}
	teamCounts         map[string]int
	taskCounts         map[string]int
	orchestratorCounts map[string]int
}

type executionDiagnosticsSessionResult struct {
	source         map[string]interface{}
	sessionCounts  map[string]int
	approvalCounts map[string]int
}

func (h *Handler) executionDiagnosticsSnapshot(ctx context.Context) map[string]interface{} {
	if ctx == nil {
		ctx = context.Background()
	}

	var (
		mu         sync.Mutex
		sessions   executionDiagnosticsSessionResult
		background executionDiagnosticsResult
		teams      executionDiagnosticsTeamResult
		agents     executionDiagnosticsResult
	)
	// 每个 source 独立 goroutine，结果写入互斥保护的结构；主路径只做
	// 有界等待，超时后立即返回部分结果，绝不阻塞 health 端点。
	doneCh := make(chan struct{}, 4)
	runSource := func(fn func()) {
		go func() {
			defer func() { doneCh <- struct{}{} }()
			fn()
		}()
	}
	runSource(func() {
		result := h.sessionExecutionDiagnostics(ctx)
		mu.Lock()
		sessions = result
		mu.Unlock()
	})
	runSource(func() {
		result := h.backgroundExecutionDiagnostics(ctx)
		mu.Lock()
		background = result
		mu.Unlock()
	})
	runSource(func() {
		result := h.teamExecutionDiagnostics(ctx)
		mu.Lock()
		teams = result
		mu.Unlock()
	})
	runSource(func() {
		result := h.agentExecutionDiagnostics(ctx)
		mu.Lock()
		agents = result
		mu.Unlock()
	})

	timedOut := false
	deadline := time.Now().Add(executionDiagnosticsSnapshotTimeout)
	for i := 0; i < 4; i++ {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			timedOut = true
			break
		}
		select {
		case <-doneCh:
		case <-ctx.Done():
			// 外层调用方已取消（客户端断开/请求超时）：立即返回部分结果，
			// 不再等待剩余 source goroutine。
			timedOut = true
		case <-time.After(remaining):
			timedOut = true
		}
		if timedOut {
			break
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if timedOut {
		// 未完成的 source 标记为 unavailable/timeout，避免返回 nil map。
		if sessions.source == nil {
			sessions.source = executionDiagnosticsSource("session_runtime_store", executionDiagnosticsSourceUnavailable, "timeout")
		}
		if sessions.sessionCounts == nil {
			sessions.sessionCounts = newExecutionDiagnosticsCounts(
				"idle", "running", "waiting_approval", "waiting_input", "rewinding", "stopped",
			)
		}
		if sessions.approvalCounts == nil {
			sessions.approvalCounts = newExecutionDiagnosticsCounts("waiting")
		}
		if background.source == nil {
			background.source = executionDiagnosticsSource("background_manager", executionDiagnosticsSourceUnavailable, "timeout")
		}
		if background.counts == nil {
			background.counts = newExecutionDiagnosticsCounts(
				"pending", "running", "completed", "failed", "timed_out", "cancelled", "orphaned",
			)
		}
		if teams.source == nil {
			teams.source = executionDiagnosticsSource("team_store", executionDiagnosticsSourceUnavailable, "timeout")
		}
		if teams.teamCounts == nil {
			teams.teamCounts = newExecutionDiagnosticsCounts(
				"active", "paused", "done", "failed", "partially_completed", "canceled",
			)
		}
		if teams.taskCounts == nil {
			teams.taskCounts = newExecutionDiagnosticsCounts(
				"pending", "ready", "running", "blocked", "done", "failed", "cancelled",
			)
		}
		if teams.orchestratorCounts == nil {
			teams.orchestratorCounts = map[string]int{}
		}
		if agents.source == nil {
			agents.source = executionDiagnosticsSource("agent_control_registry_store", executionDiagnosticsSourceUnavailable, "timeout")
		}
		if agents.counts == nil {
			agents.counts = newExecutionDiagnosticsCounts("active", "closed")
		}
	}

	return map[string]interface{}{
		"schema_version": executionDiagnosticsSchemaVersion,
		"generated_at":   time.Now().UTC(),
		"sources": map[string]interface{}{
			"sessions":   sessions.source,
			"background": background.source,
			"teams":      teams.source,
			"agents":     agents.source,
		},
		"counts": map[string]interface{}{
			"sessions":           sessions.sessionCounts,
			"background":         background.counts,
			"teams":              teams.teamCounts,
			"team_tasks":         teams.taskCounts,
			"team_orchestrators": teams.orchestratorCounts,
			"agents":             agents.counts,
			"approvals":          sessions.approvalCounts,
		},
		"capabilities": map[string]bool{
			"event_consumer_lag":      false,
			"goal_todo_consistency":   false,
			"unified_execution_nodes": false,
			"team_loop_consistency":   true,
		},
	}
}

func executionDiagnosticsContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, executionDiagnosticsSourceTimeout)
}

func executionDiagnosticsSource(authority, status, errorCode string) map[string]interface{} {
	result := map[string]interface{}{
		"authority": authority,
		"status":    status,
		"degraded":  status != executionDiagnosticsSourceOK,
	}
	if errorCode != "" {
		result["error"] = errorCode
	}
	return result
}

func executionDiagnosticsErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case stderrors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case stderrors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "query_failed"
	}
}

func (h *Handler) sessionExecutionDiagnostics(parent context.Context) executionDiagnosticsSessionResult {
	result := executionDiagnosticsSessionResult{
		sessionCounts: newExecutionDiagnosticsCounts(
			"idle", "running", "waiting_approval", "waiting_input", "rewinding", "stopped",
		),
		approvalCounts: newExecutionDiagnosticsCounts("waiting"),
	}
	if h == nil {
		result.source = executionDiagnosticsSource("session_runtime_store", executionDiagnosticsSourceUnavailable, "not_configured")
		return result
	}

	h.sessionRuntimeMu.RLock()
	stateStore := h.sessionRuntimeStore
	h.sessionRuntimeMu.RUnlock()
	if stateStore == nil {
		result.source = executionDiagnosticsSource("session_runtime_store", executionDiagnosticsSourceUnavailable, "not_configured")
		return result
	}
	if h.sessionManager == nil || h.sessionManager.GetStorage() == nil {
		result.source = executionDiagnosticsSource("session_runtime_store", executionDiagnosticsSourceUnavailable, "session_discovery_not_configured")
		return result
	}
	sessionLister, ok := h.sessionManager.GetStorage().(executionDiagnosticsSessionLister)
	if !ok || sessionLister == nil {
		result.source = executionDiagnosticsSource("session_runtime_store", executionDiagnosticsSourceUnavailable, "session_discovery_unsupported")
		return result
	}

	ctx, cancel := executionDiagnosticsContext(parent)
	defer cancel()
	result = collectSessionExecutionDiagnostics(ctx, sessionLister, stateStore, result)
	result.source["discovery_authority"] = "session_storage"
	result.source["orphan_runtime_states_included"] = false
	result.source["pending_approval_authority"] = "runtime_state"
	return result
}

func collectSessionExecutionDiagnostics(
	ctx context.Context,
	sessionLister executionDiagnosticsSessionLister,
	stateStore chat.RuntimeStateStore,
	result executionDiagnosticsSessionResult,
) executionDiagnosticsSessionResult {
	seen := make(map[string]struct{})
	failedRecords := 0
	offset := 0
	for {
		sessions, err := sessionLister.ListAll(ctx, executionDiagnosticsSessionPage, offset)
		if err != nil {
			result.source = executionDiagnosticsSource("session_runtime_store", executionDiagnosticsSourceDegraded, executionDiagnosticsErrorCode(err))
			if failedRecords > 0 {
				result.source["failed_records"] = failedRecords
			}
			return result
		}
		for _, session := range sessions {
			if session == nil || strings.TrimSpace(session.ID) == "" {
				failedRecords++
				continue
			}
			sessionID := strings.TrimSpace(session.ID)
			if _, exists := seen[sessionID]; exists {
				continue
			}
			seen[sessionID] = struct{}{}
			state, err := stateStore.LoadState(ctx, sessionID)
			if err != nil {
				failedRecords++
				continue
			}
			if state == nil {
				continue
			}
			incrementExecutionDiagnosticsCount(result.sessionCounts, string(state.Status), executionDiagnosticsSessionStatuses)
			if state.PendingApproval != nil {
				result.approvalCounts["waiting"]++
				result.approvalCounts["total"]++
			}
		}
		offset += len(sessions)
		if len(sessions) < executionDiagnosticsSessionPage {
			break
		}
	}

	if failedRecords > 0 {
		result.source = executionDiagnosticsSource("session_runtime_store", executionDiagnosticsSourceDegraded, "partial_query_failed")
		result.source["failed_records"] = failedRecords
		return result
	}
	result.source = executionDiagnosticsSource("session_runtime_store", executionDiagnosticsSourceOK, "")
	return result
}

var executionDiagnosticsSessionStatuses = map[string]string{
	string(chat.SessionIdle):            "idle",
	string(chat.SessionRunning):         "running",
	string(chat.SessionWaitingApproval): "waiting_approval",
	string(chat.SessionWaitingInput):    "waiting_input",
	string(chat.SessionRewinding):       "rewinding",
	string(chat.SessionStopped):         "stopped",
}

func (h *Handler) backgroundExecutionDiagnostics(parent context.Context) executionDiagnosticsResult {
	result := executionDiagnosticsResult{
		counts: newExecutionDiagnosticsCounts(
			"pending", "running", "completed", "failed", "timed_out", "cancelled", "orphaned",
		),
	}
	if h == nil {
		result.source = executionDiagnosticsSource("background_manager", executionDiagnosticsSourceUnavailable, "not_configured")
		return result
	}

	h.backgroundMu.Lock()
	manager := h.backgroundManager
	h.backgroundMu.Unlock()
	if manager == nil {
		result.source = executionDiagnosticsSource("background_manager", executionDiagnosticsSourceUnavailable, "not_configured")
		return result
	}
	ctx, cancel := executionDiagnosticsContext(parent)
	defer cancel()
	return collectBackgroundExecutionDiagnostics(ctx, manager, result)
}

func collectBackgroundExecutionDiagnostics(
	ctx context.Context,
	reader executionDiagnosticsBackgroundReader,
	result executionDiagnosticsResult,
) executionDiagnosticsResult {
	jobs, err := reader.ListJobs(ctx, background.JobFilter{})
	if err != nil {
		result.source = executionDiagnosticsSource("background_manager", executionDiagnosticsSourceDegraded, executionDiagnosticsErrorCode(err))
		return result
	}
	for _, job := range jobs {
		incrementExecutionDiagnosticsCount(result.counts, string(job.Status), executionDiagnosticsBackgroundStatuses)
	}
	result.source = executionDiagnosticsSource("background_manager", executionDiagnosticsSourceOK, "")
	return result
}

var executionDiagnosticsBackgroundStatuses = map[string]string{
	string(background.StatusPending):   "pending",
	string(background.StatusRunning):   "running",
	string(background.StatusCompleted): "completed",
	string(background.StatusFailed):    "failed",
	string(background.StatusTimedOut):  "timed_out",
	string(background.StatusCancelled): "cancelled",
	string(background.StatusOrphaned):  "orphaned",
}

func (h *Handler) teamExecutionDiagnostics(parent context.Context) executionDiagnosticsTeamResult {
	result := executionDiagnosticsTeamResult{
		teamCounts: newExecutionDiagnosticsCounts(
			"active", "paused", "done", "failed", "partially_completed", "canceled",
		),
		taskCounts: newExecutionDiagnosticsCounts(
			"pending", "ready", "running", "blocked", "done", "failed", "cancelled",
		),
		orchestratorCounts: map[string]int{
			"active_teams":    0,
			"live_loops":      0,
			"loop_gap":        0,
			"extra_loops":     0,
			"restart_total":   0,
			"restart_pending": 0,
			"degraded_loops":  0,
		},
	}
	if h == nil {
		result.source = executionDiagnosticsSource("team_store", executionDiagnosticsSourceUnavailable, "not_configured")
		return result
	}

	h.teamStoreMu.RLock()
	store := h.teamStore
	h.teamStoreMu.RUnlock()
	if store == nil {
		result.source = executionDiagnosticsSource("team_store", executionDiagnosticsSourceUnavailable, "not_configured")
		return result
	}
	ctx, cancel := executionDiagnosticsContext(parent)
	defer cancel()
	result = collectTeamExecutionDiagnostics(ctx, store, result)
	activeTeams := result.teamCounts["active"]
	supervisor := h.teamLifecycleService().SupervisorSnapshot()
	liveLoops := supervisor.LoopCount
	result.orchestratorCounts["active_teams"] = activeTeams
	result.orchestratorCounts["live_loops"] = liveLoops
	result.orchestratorCounts["restart_total"] = supervisor.RestartTotal
	result.orchestratorCounts["restart_pending"] = supervisor.RestartPending
	result.orchestratorCounts["degraded_loops"] = supervisor.DegradedLoops
	if activeTeams > liveLoops {
		result.orchestratorCounts["loop_gap"] = activeTeams - liveLoops
	} else if liveLoops > activeTeams {
		result.orchestratorCounts["extra_loops"] = liveLoops - activeTeams
	}
	return result
}

func collectTeamExecutionDiagnostics(
	ctx context.Context,
	reader executionDiagnosticsTeamReader,
	result executionDiagnosticsTeamResult,
) executionDiagnosticsTeamResult {
	teams, teamErr := reader.ListTeams(ctx, team.TeamFilter{})
	if teamErr == nil {
		for _, item := range teams {
			incrementExecutionDiagnosticsCount(result.teamCounts, string(item.Status), executionDiagnosticsTeamStatuses)
		}
	}

	tasks, taskErr := reader.ListTasks(ctx, team.TaskFilter{})
	if taskErr == nil {
		for _, task := range tasks {
			incrementExecutionDiagnosticsCount(result.taskCounts, string(task.Status), executionDiagnosticsTaskStatuses)
		}
	}

	if teamErr == nil && taskErr == nil {
		result.source = executionDiagnosticsSource("team_store", executionDiagnosticsSourceOK, "")
		return result
	}
	errorCode := "query_failed"
	if teamErr != nil && taskErr == nil {
		errorCode = "team_query_failed"
	} else if teamErr == nil && taskErr != nil {
		errorCode = "task_query_failed"
	} else if code := executionDiagnosticsErrorCode(teamErr); code == "timeout" || code == "cancelled" {
		errorCode = code
	} else if code := executionDiagnosticsErrorCode(taskErr); code == "timeout" || code == "cancelled" {
		errorCode = code
	}
	result.source = executionDiagnosticsSource("team_store", executionDiagnosticsSourceDegraded, errorCode)
	return result
}

var executionDiagnosticsTeamStatuses = map[string]string{
	string(team.TeamStatusActive):             "active",
	string(team.TeamStatusPaused):             "paused",
	string(team.TeamStatusDone):               "done",
	string(team.TeamStatusFailed):             "failed",
	string(team.TeamStatusPartiallyCompleted): "partially_completed",
	string(team.TeamStatusCanceled):           "canceled",
}

var executionDiagnosticsTaskStatuses = map[string]string{
	string(team.TaskStatusPending):   "pending",
	string(team.TaskStatusReady):     "ready",
	string(team.TaskStatusRunning):   "running",
	string(team.TaskStatusBlocked):   "blocked",
	string(team.TaskStatusDone):      "done",
	string(team.TaskStatusFailed):    "failed",
	string(team.TaskStatusCancelled): "cancelled",
}

func (h *Handler) agentExecutionDiagnostics(parent context.Context) executionDiagnosticsResult {
	result := executionDiagnosticsResult{
		counts: newExecutionDiagnosticsCounts("active", "closed"),
	}
	if h == nil {
		result.source = executionDiagnosticsSource("agent_control_registry_store", executionDiagnosticsSourceUnavailable, "not_configured")
		return result
	}

	h.agentControlMu.RLock()
	store := h.agentControlAgentStore
	h.agentControlMu.RUnlock()
	if store == nil {
		result.source = executionDiagnosticsSource("agent_control_registry_store", executionDiagnosticsSourceUnavailable, "not_configured")
		return result
	}
	ctx, cancel := executionDiagnosticsContext(parent)
	defer cancel()
	return collectAgentExecutionDiagnostics(ctx, store, result)
}

func collectAgentExecutionDiagnostics(
	ctx context.Context,
	reader executionDiagnosticsAgentReader,
	result executionDiagnosticsResult,
) executionDiagnosticsResult {
	agents, err := reader.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{IncludeClosed: true})
	if err != nil {
		result.source = executionDiagnosticsSource("agent_control_registry_store", executionDiagnosticsSourceDegraded, executionDiagnosticsErrorCode(err))
		return result
	}
	for _, record := range agents {
		incrementExecutionDiagnosticsCount(result.counts, record.Status, executionDiagnosticsAgentStatuses)
	}
	result.source = executionDiagnosticsSource("agent_control_registry_store", executionDiagnosticsSourceOK, "")
	return result
}

var executionDiagnosticsAgentStatuses = map[string]string{
	agentcontrol.AgentStatusActive: "active",
	agentcontrol.AgentStatusClosed: "closed",
}

func newExecutionDiagnosticsCounts(statuses ...string) map[string]int {
	counts := make(map[string]int, len(statuses)+2)
	counts["total"] = 0
	counts["unknown"] = 0
	for _, status := range statuses {
		counts[status] = 0
	}
	return counts
}

func incrementExecutionDiagnosticsCount(counts map[string]int, rawStatus string, known map[string]string) {
	if counts == nil {
		return
	}
	status := strings.ToLower(strings.TrimSpace(rawStatus))
	key, ok := known[status]
	if !ok {
		key = "unknown"
	}
	counts[key]++
	counts["total"]++
}
