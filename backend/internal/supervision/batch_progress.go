package supervision

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// ProgressMessageProvider optionally enriches a rollup row with live-only
// detail that the durable batch control plane does not store (which tool a
// child is currently running). Hosts that keep a long-lived
// SubagentProgressMirror can pass MirrorProgressMessages; hosts that do not
// simply leave it nil and the rollup keeps its durable-only shape.
type ProgressMessageProvider interface {
	LastProgressMessage(childSessionID string) string
}

const (
	// defaultBatchProgressRecentWindow keeps just-finished background batches in
	// the rollup long enough for the parent to converge ("3/3 completed" is the
	// line that tells it to close the finished children). Older batches would
	// permanently eat the 3-group budget.
	defaultBatchProgressRecentWindow = 10 * time.Minute
	// defaultBatchProgressMaxBatches bounds each store read. The digest budget
	// (maxProgressGroups) is applied afterwards.
	defaultBatchProgressMaxBatches = 32
)

// BatchProgressSource adapts the durable subagent batch control plane into the
// P0-B progress rollup. It is strictly read-only (ListBatches/ListTasks): no new
// table, no new event and no wake — the rollup only shows up inside the parent
// preflight digest, which preserves the "成功不唤醒" contract.
type BatchProgressSource struct {
	Store subagentbatch.BatchStore
	// Messages is an optional live-only enrichment hook (see
	// ProgressMessageProvider).
	Messages ProgressMessageProvider
	// RecentTerminalWindow overrides how long finished background batches stay
	// visible. Non-positive falls back to defaultBatchProgressRecentWindow.
	RecentTerminalWindow time.Duration
	// MaxBatches overrides the per-read bound. Non-positive falls back to
	// defaultBatchProgressMaxBatches.
	MaxBatches int
}

// NewBatchProgressSource builds the durable progress projection for a host.
// A nil store returns a nil ProgressSource so unwired hosts keep injecting
// byte-identical prompts.
func NewBatchProgressSource(store subagentbatch.BatchStore) ProgressSource {
	if store == nil {
		return nil
	}
	return &BatchProgressSource{Store: store}
}

var activeBatchStatuses = []subagentbatch.BatchStatus{
	subagentbatch.BatchQueued,
	subagentbatch.BatchRunning,
	subagentbatch.BatchPartiallyCompleted,
}

// ListProgress implements ProgressSource. Scope口径：只读属于该父会话的批次
// （BatchFilter.ParentSessionID，由宿主按自己的 scope 解析注入，模型不可指定）。
func (s *BatchProgressSource) ListProgress(ctx context.Context, req ProgressRequest) ([]ProgressGroup, error) {
	if s == nil || s.Store == nil {
		return nil, nil
	}
	parentSessionID := strings.TrimSpace(req.ParentSessionID)
	if parentSessionID == "" {
		return nil, nil
	}
	limit := s.MaxBatches
	if limit <= 0 {
		limit = defaultBatchProgressMaxBatches
	}
	now := timeNow().UTC()
	window := s.RecentTerminalWindow
	if window <= 0 {
		window = defaultBatchProgressRecentWindow
	}

	// Two bounded reads instead of one: an active batch created long ago must
	// not fall out of a "newest first" page behind newer short-lived batches.
	active, err := s.Store.ListBatches(ctx, subagentbatch.BatchFilter{
		ParentSessionID: parentSessionID,
		Status:          activeBatchStatuses,
		Limit:           limit,
	})
	if err != nil {
		return nil, err
	}
	finished, err := s.Store.ListBatches(ctx, subagentbatch.BatchFilter{
		ParentSessionID: parentSessionID,
		Status: []subagentbatch.BatchStatus{
			subagentbatch.BatchCompleted,
			subagentbatch.BatchFailed,
			subagentbatch.BatchCanceled,
			subagentbatch.BatchTimedOut,
			subagentbatch.BatchOrphaned,
		},
		ExecutionMode: []subagentbatch.ExecutionMode{subagentbatch.ExecutionModeBackground},
		Limit:         limit,
	})
	if err != nil {
		return nil, err
	}

	sort.SliceStable(active, func(i, j int) bool {
		return batchProgressTime(active[i]).After(batchProgressTime(active[j]))
	})
	recent := make([]subagentbatch.SubagentBatch, 0, len(finished))
	for _, batch := range finished {
		end := batchFinishedTime(batch)
		if end.IsZero() || now.Sub(end) > window {
			continue
		}
		recent = append(recent, batch)
	}
	sort.SliceStable(recent, func(i, j int) bool {
		return batchFinishedTime(recent[i]).After(batchFinishedTime(recent[j]))
	})

	ordered := append(active, recent...)
	groups := make([]ProgressGroup, 0, len(ordered))
	for _, batch := range ordered {
		groups = append(groups, s.progressGroup(ctx, batch))
	}
	return groups, nil
}

func (s *BatchProgressSource) progressGroup(ctx context.Context, batch subagentbatch.SubagentBatch) ProgressGroup {
	group := ProgressGroup{
		GroupID:        strings.TrimSpace(batch.BatchID),
		Total:          batch.TaskCount,
		Completed:      batch.CompletedCount,
		Failed:         batch.FailedCount + batch.TimedOutCount + batch.CanceledCount,
		Running:        batch.RunningCount,
		Pending:        batch.QueuedCount,
		Terminal:       batch.Status.Terminal(),
		LastProgressAt: batchProgressTime(batch),
	}
	// 单一事实源（P1-1 / H6+H9）：batch 计数列只在建批与终态收敛时刷新，运行期快照
	// 会出现「queued=3, running=0」而三行 task 已是 running 的自相矛盾。因此计数一律
	// 由 task 行派生；只有 task 行读取失败时才退回存储列（best-effort 降级）。
	tasks, err := s.Store.ListTasks(ctx, batch.BatchID)
	if err != nil {
		// 任务级读取失败只降级这一组的运行期明细，批次计数仍然可见：
		// 进度投影是 best-effort，不能因此让整块进度消失。
		return group
	}
	counts := subagentbatch.DeriveTaskCounts(tasks)
	if counts.Total > 0 || group.Total == 0 {
		if group.Total < counts.Total {
			group.Total = counts.Total
		}
		group.Completed = counts.Completed
		// Failed keeps its existing digest semantics: every non-completed
		// terminal cohort the parent has to account for.
		group.Failed = counts.FailedTotal() + counts.Canceled + counts.TimedOut
		group.Running = counts.Running
		group.Pending = counts.Queued
		group.Skipped = counts.Skipped
	}
	if group.Terminal {
		// 终态批次不再逐任务展开：对父 agent 有用的信息就是"已完成/失败"计数，
		// 任务级细节走 supervision_descendants。
		return group
	}
	for _, task := range tasks {
		if task.Status.Terminal() {
			continue
		}
		entry := ProgressTask{
			TaskID:         strings.TrimSpace(task.TaskID),
			ChildSessionID: strings.TrimSpace(task.ChildSessionID),
			State:          string(task.Status),
			LastProgressAt: taskProgressTime(task),
		}
		if s.Messages != nil && entry.ChildSessionID != "" {
			entry.LastMessage = strings.TrimSpace(s.Messages.LastProgressMessage(entry.ChildSessionID))
		}
		group.RunningTasks = append(group.RunningTasks, entry)
	}
	sort.SliceStable(group.RunningTasks, func(i, j int) bool {
		left, right := group.RunningTasks[i], group.RunningTasks[j]
		if !left.LastProgressAt.Equal(right.LastProgressAt) {
			return left.LastProgressAt.After(right.LastProgressAt)
		}
		return left.TaskID < right.TaskID
	})
	for _, task := range group.RunningTasks {
		if task.LastProgressAt.After(group.LastProgressAt) {
			group.LastProgressAt = task.LastProgressAt
		}
	}
	return group
}

// batchProgressTime is the newest stamp a batch itself carries (heartbeat wins:
// it is refreshed while children run, UpdatedAt changes on every mutation).
func batchProgressTime(batch subagentbatch.SubagentBatch) time.Time {
	if !batch.HeartbeatAt.IsZero() {
		return batch.HeartbeatAt
	}
	return batch.UpdatedAt
}

func batchFinishedTime(batch subagentbatch.SubagentBatch) time.Time {
	if batch.FinishedAt != nil && !batch.FinishedAt.IsZero() {
		return *batch.FinishedAt
	}
	return batch.UpdatedAt
}

// taskProgressTime prefers the child-reported progress stamp, then the record's
// own update time, then the start time: a running child that never reported
// still shows a sane age instead of "no progress yet" forever.
func taskProgressTime(task subagentbatch.SubagentTaskRecord) time.Time {
	if task.LastProgressAt != nil && !task.LastProgressAt.IsZero() {
		return *task.LastProgressAt
	}
	if !task.UpdatedAt.IsZero() {
		return task.UpdatedAt
	}
	if task.StartedAt != nil && !task.StartedAt.IsZero() {
		return *task.StartedAt
	}
	return time.Time{}
}

// MirrorProgressMessages adapts the live-only SubagentProgressMirror into the
// optional ProgressMessageProvider hook.
type MirrorProgressMessages struct {
	Mirror *SubagentProgressMirror
}

// LastProgressMessage implements ProgressMessageProvider.
func (m MirrorProgressMessages) LastProgressMessage(childSessionID string) string {
	if m.Mirror == nil {
		return ""
	}
	state, message, _, ok := m.Mirror.Latest(childSessionID)
	if !ok {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(state), "progress") {
		return strings.TrimSpace(message)
	}
	if strings.TrimSpace(message) != "" {
		return strings.TrimSpace(state) + ": " + strings.TrimSpace(message)
	}
	return strings.TrimSpace(state)
}
