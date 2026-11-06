package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// 托管挂起（chatSurfaceStatusParked）期间，动态状态行不能只显示一句静态的
// "Waiting for subagents"：父 turn 已经交出自己的 run，进度只存在于子代理侧，
// 用户看不到任何推进证据时很容易把长时间实现/构建当成卡死。
//
// 本文件在 parked 生命周期内挂一个低频只读采样器：读取当前 turn 的 durable
// obligations、supervision descendant 状态与 live-only 子代理进度镜像，把
// "N running / M done · 最近事件" 投影到动态状态行的 detail 上。采样器不写
// transcript、不产生 wake、不改变 turn 语义，离开 parked 或 Shutdown 时立即退出。

const (
	chatParkedSubagentDefaultRefreshInterval = 3 * time.Second
	chatParkedSubagentDefaultRefreshTimeout  = 2 * time.Second
	chatParkedSubagentEventMaxWidth          = 56
	chatParkedSubagentLabelMaxWidth          = 12
)

var (
	chatParkedSubagentRefreshInterval = chatParkedSubagentDefaultRefreshInterval
	chatParkedSubagentRefreshTimeout  = chatParkedSubagentDefaultRefreshTimeout
)

// chatParkedSubagentActivityLoader is a test seam: production code reads the
// local runtime host; tests can return a deterministic projection without
// constructing a full supervision store.
var chatParkedSubagentActivityLoader = collectChatParkedSubagentActivity

// chatParkedSubagentActivity is the bounded one-line projection rendered by the
// dynamic status line while the parent turn is parked.
type chatParkedSubagentActivity struct {
	Running int
	Done    int
	Total   int
	// Known distinguishes "no data could be read" from a real zero-obligation
	// projection: a transient store error must not erase the last useful line.
	Known bool

	LatestLabel string
	LatestEvent string
	LatestAt    time.Time
}

// formatChatParkedSubagentDetail renders the activity as the detail suffix of
// "Waiting for subagents". Kept pure so width/age formatting has direct tests.
func formatChatParkedSubagentDetail(activity chatParkedSubagentActivity, now time.Time) string {
	if !activity.Known {
		return ""
	}
	parts := make([]string, 0, 2)
	switch {
	case activity.Running > 0 && activity.Done > 0:
		parts = append(parts, fmt.Sprintf("%d running · %d done", activity.Running, activity.Done))
	case activity.Running > 0:
		parts = append(parts, fmt.Sprintf("%d running", activity.Running))
	case activity.Done > 0:
		parts = append(parts, fmt.Sprintf("%d done", activity.Done))
	}
	if event := strings.TrimSpace(activity.LatestEvent); event != "" {
		event = compactStatusValue(oneLineStatusDetail(event), chatParkedSubagentEventMaxWidth)
		label := compactStatusValue(strings.TrimSpace(activity.LatestLabel), chatParkedSubagentLabelMaxWidth)
		item := event
		if label != "" {
			item = label + " " + event
		}
		if !activity.LatestAt.IsZero() {
			age := now.Sub(activity.LatestAt)
			if age < 0 {
				age = 0
			}
			item += " (" + formatChatDynamicStatusElapsed(age) + " ago)"
		}
		parts = append(parts, item)
	}
	return strings.Join(parts, " · ")
}

// collectChatParkedSubagentActivity is the production sampler. It is strictly
// read-only and bounded: one suspension read, one descendant projection and,
// only when a batch obligation is present, one batch progress projection.
func collectChatParkedSubagentActivity(ctx context.Context, session *ChatSession, parentID string) chatParkedSubagentActivity {
	if session == nil || session.LocalRuntimeHost == nil || strings.TrimSpace(parentID) == "" {
		return chatParkedSubagentActivity{}
	}
	host := session.LocalRuntimeHost

	var obligations []string
	obligationsRead := false
	hasRecord := false
	if host.SubagentBatches != nil {
		records, err := host.SubagentBatches.ListTurnSuspensions(ctx, parentID)
		if err == nil {
			obligationsRead = true
			if len(records) > 0 && records[0] != nil {
				hasRecord = true
				// ListTurnSuspensions returns newest parked first. Only the current
				// park epoch is shown; historical records must not inflate counts.
				obligations = records[0].RequiredObligationIDs()
			}
		}
	}

	var states []supervision.DescendantState
	statesRead := false
	if host.Supervision != nil && host.Supervision.Provider != nil {
		if list, err := host.Supervision.Provider.ListDescendants(ctx, supervision.Scope{RootSessionID: parentID}); err == nil {
			states = list
			statesRead = true
		}
	}

	var groups []supervision.ProgressGroup
	if chatParkedObligationsNeedBatchProgress(obligations) {
		if source := host.newLocalBatchProgressSource(); source != nil {
			if list, err := source.ListProgress(ctx, supervision.ProgressRequest{ParentSessionID: parentID, RowLimit: 8}); err == nil {
				groups = list
			}
		}
	}

	var latest func(string) (state, message string, at time.Time, ok bool)
	if mirror := host.subagentProgressMirror(); mirror != nil {
		latest = mirror.Latest
	}

	activity := buildChatParkedSubagentActivity(obligations, states, groups, latest)
	switch {
	case hasRecord:
		activity.Known = true
	case obligationsRead:
		// The record disappeared between the parked transition and this sample:
		// the turn is settling, so keep the last useful line instead of
		// projecting every historical descendant as "done".
		activity.Known = false
	case statesRead || len(groups) > 0:
		activity.Known = true
	default:
		activity.Known = false
	}
	return activity
}

func chatParkedObligationsNeedBatchProgress(obligations []string) bool {
	for _, obligation := range obligations {
		if subagentbatch.AgentSessionIDFromObligation(obligation) != "" {
			continue
		}
		if subagentbatch.IsTeamObligation(obligation) || subagentbatch.IsBackgroundJobObligation(obligation) {
			continue
		}
		return true
	}
	return false
}

// buildChatParkedSubagentActivity projects obligations through the descendant
// state matrix and batch progress rollup. Obligations are the accounting unit:
// a batch (plus its task ids) is counted once via the batch rollup so task rows
// cannot double count, while agent-session obligations resolve one-to-one.
func buildChatParkedSubagentActivity(
	obligations []string,
	states []supervision.DescendantState,
	groups []supervision.ProgressGroup,
	latest func(string) (state, message string, at time.Time, ok bool),
) chatParkedSubagentActivity {
	activity := chatParkedSubagentActivity{}
	statesByID := make(map[string]supervision.DescendantState, len(states))
	for _, state := range states {
		if id := strings.TrimSpace(state.ID); id != "" {
			statesByID[id] = state
		}
	}
	groupsByID := make(map[string]supervision.ProgressGroup, len(groups))
	runningTasksByID := make(map[string]supervision.ProgressTask)
	taskGroupByID := make(map[string]string)
	for _, group := range groups {
		groupID := strings.TrimSpace(group.GroupID)
		if groupID != "" {
			groupsByID[groupID] = group
		}
		for _, task := range group.RunningTasks {
			taskID := strings.TrimSpace(task.TaskID)
			if taskID == "" {
				continue
			}
			runningTasksByID[taskID] = task
			taskGroupByID[taskID] = groupID
		}
	}

	handledGroups := make(map[string]bool)
	childIDs := make([]string, 0, len(obligations))
	seenChildren := make(map[string]bool)
	addChild := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" || seenChildren[id] {
			return
		}
		seenChildren[id] = true
		childIDs = append(childIDs, id)
	}

	countEach := func(terminal bool) {
		if terminal {
			activity.Done++
			return
		}
		activity.Running++
	}

	for _, obligation := range obligations {
		subjectID := subagentbatch.SubjectIDFromObligation(obligation)
		if subjectID == "" {
			continue
		}

		// Lightweight subagent_spawn child: one obligation == one child session.
		if childID := subagentbatch.AgentSessionIDFromObligation(obligation); childID != "" {
			addChild(childID)
			countEach(chatParkedSubagentStateTerminal(statesByID[childID]))
			continue
		}

		// Batch-backed obligation: use the task rollup, not the (often stale)
		// batch counter columns, and never count the same batch twice when the
		// record also lists task ids.
		if group, ok := groupsByID[subjectID]; ok {
			if handledGroups[subjectID] {
				continue
			}
			handledGroups[subjectID] = true
			activity.Running += group.Running + group.Pending
			activity.Done += group.Completed + group.Failed + group.Skipped
			for _, task := range group.RunningTasks {
				addChild(task.ChildSessionID)
			}
			continue
		}

		// A task id from a batch already counted by its parent row: keep the
		// child id for the live event lookup, but do not add a second count.
		if task, ok := runningTasksByID[subjectID]; ok {
			addChild(task.ChildSessionID)
			groupID := taskGroupByID[subjectID]
			if groupID != "" && handledGroups[groupID] {
				continue
			}
			countEach(false)
			continue
		}

		// Team / agent-run / job-backed obligations resolve through the same
		// descendant matrix when a row exists; an unknown obligation is still
		// pending until its own completion edge arrives.
		if state, ok := statesByID[subjectID]; ok {
			if state.Kind == supervision.SubjectAgentSession {
				addChild(state.ID)
			}
			countEach(chatParkedSubagentStateTerminal(state))
			continue
		}
		countEach(false)
	}

	// Startup/race fallback: no suspension record was readable, but the
	// supervision matrix already knows about active descendants.
	if len(obligations) == 0 {
		for _, state := range states {
			if state.Kind == supervision.SubjectAgentSession {
				addChild(state.ID)
			}
			countEach(chatParkedSubagentStateTerminal(state))
		}
		for _, group := range groups {
			groupID := strings.TrimSpace(group.GroupID)
			if groupID == "" || handledGroups[groupID] {
				continue
			}
			handledGroups[groupID] = true
			activity.Running += group.Running + group.Pending
			activity.Done += group.Completed + group.Failed + group.Skipped
			for _, task := range group.RunningTasks {
				addChild(task.ChildSessionID)
			}
		}
	}

	activity.Total = activity.Running + activity.Done

	// Live-only tool progress is the freshest "what is the child doing now"
	// signal; prefer it over the durable progress stamp.
	if latest != nil {
		for _, childID := range childIDs {
			state, message, at, ok := latest(childID)
			if !ok || at.IsZero() {
				continue
			}
			if !activity.LatestAt.IsZero() && !at.After(activity.LatestAt) {
				continue
			}
			event := chatParkedSubagentEventText(state, message)
			if event == "" {
				continue
			}
			activity.LatestAt = at
			activity.LatestLabel = chatParkedSubagentLabel(childID)
			activity.LatestEvent = event
		}
	}
	// Batch progress carries its own last message/time; use it when no live
	// mirror row exists (for example after the mirror's stale window pruned a
	// quiet child).
	if activity.LatestEvent == "" {
		for _, group := range groups {
			for _, task := range group.RunningTasks {
				if strings.TrimSpace(task.LastMessage) == "" || task.LastProgressAt.IsZero() {
					continue
				}
				if !activity.LatestAt.IsZero() && !task.LastProgressAt.After(activity.LatestAt) {
					continue
				}
				activity.LatestAt = task.LastProgressAt
				activity.LatestLabel = chatParkedSubagentLabel(firstNonEmptyTaskLabel(task.ChildSessionID, task.TaskID))
				activity.LatestEvent = task.LastMessage
			}
		}
	}
	return activity
}

func chatParkedSubagentStateTerminal(state supervision.DescendantState) bool {
	if state.SupervisionState == supervision.SupervisionTerminated {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(state.ExecutionStatus)) {
	case "closed", "completed", "succeeded", "failed", "canceled", "cancelled",
		"timed_out", "timeout", "orphaned", "stale":
		return true
	default:
		return false
	}
}

func chatParkedSubagentEventText(state, message string) string {
	if message = strings.TrimSpace(oneLineStatusDetail(message)); message != "" {
		return message
	}
	state = strings.ToLower(strings.TrimSpace(state))
	switch state {
	case "", "progress", "running", "active":
		return ""
	default:
		return state
	}
}

func firstNonEmptyTaskLabel(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func chatParkedSubagentLabel(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if index := strings.LastIndex(id, "_"); index >= 0 && index+1 < len(id) {
		suffix := id[index+1:]
		if len(suffix) >= 4 && len(suffix) <= 16 {
			return suffix
		}
	}
	return compactStatusValue(id, chatParkedSubagentLabelMaxWidth)
}

// syncParkedSubagentWatchLocked starts/stops the sampler with the parked
// surface. Callers hold c.mu; the goroutine itself only does I/O outside the
// lock and re-validates the epoch before writing the cached status.
func (c *chatInteractionCoordinator) syncParkedSubagentWatchLocked(s chatSurfaceStatus) {
	if c == nil || c.shutdown {
		return
	}
	if s.kind != chatSurfaceStatusParked {
		c.stopParkedSubagentWatchLocked()
		return
	}
	if c.parkedSubagentWatchCancel != nil && c.parkedSubagentWatchSession == c.session {
		return
	}
	// A session switch can leave the previous sampler attached to the old
	// ChatSession; cancel it before starting the new epoch.
	c.stopParkedSubagentWatchLocked()
	if c.session == nil || c.session.LocalRuntimeHost == nil {
		return
	}
	parentID := chatSessionID(c.session)
	if parentID == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.parkedSubagentWatchCancel = cancel
	done := make(chan struct{})
	c.parkedSubagentWatchDone = done
	c.parkedSubagentWatchSeq++
	sequence := c.parkedSubagentWatchSeq
	session := c.session
	c.parkedSubagentWatchSession = session
	go c.runParkedSubagentWatch(ctx, sequence, session, parentID, done)
}

func (c *chatInteractionCoordinator) stopParkedSubagentWatchLocked() {
	if c == nil {
		return
	}
	c.parkedSubagentWatchSeq++
	if c.parkedSubagentWatchCancel != nil {
		c.parkedSubagentWatchCancel()
		c.parkedSubagentWatchCancel = nil
	}
	c.parkedSubagentWatchDone = nil
	c.parkedSubagentWatchSession = nil
}

func (c *chatInteractionCoordinator) runParkedSubagentWatch(ctx context.Context, sequence uint64, session *ChatSession, parentID string, done chan struct{}) {
	defer close(done)
	interval := chatParkedSubagentRefreshInterval
	if interval <= 0 {
		interval = chatParkedSubagentDefaultRefreshInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	c.refreshParkedSubagentStatus(ctx, sequence, session, parentID)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.refreshParkedSubagentStatus(ctx, sequence, session, parentID)
		}
	}
}

func (c *chatInteractionCoordinator) refreshParkedSubagentStatus(ctx context.Context, sequence uint64, session *ChatSession, parentID string) {
	if c == nil {
		return
	}
	timeout := chatParkedSubagentRefreshTimeout
	if timeout <= 0 {
		timeout = chatParkedSubagentDefaultRefreshTimeout
	}
	refreshCtx, cancel := context.WithTimeout(ctx, timeout)
	activity := chatParkedSubagentActivityLoader(refreshCtx, session, parentID)
	cancel()
	if !activity.Known {
		return
	}
	detail := formatChatParkedSubagentDetail(activity, time.Now())

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown || c.parkedSubagentWatchSeq != sequence ||
		c.session != session || c.surfaceStatus.kind != chatSurfaceStatusParked {
		return
	}
	if detail == c.surfaceStatus.detail {
		return
	}
	c.surfaceStatus.detail = detail
	c.repaintStatusModelsLocked()
	// The web status lane is driven by updateSurfaceStatusLocked; the sampler
	// bypasses it to avoid restarting itself, so publish the refreshed snapshot
	// explicitly. The enqueue is non-blocking and happens under the same lock as
	// the state transition (the actual Bus publish stays off-lock).
	c.enqueueWebDynamicStatusLocked(c.surfaceStatus, time.Now())
}
