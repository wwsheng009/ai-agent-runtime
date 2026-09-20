package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// §8.2-1 复现批次：batch_42d0a52d2a24e401 的同构场景。
//
// 取证批次终态 failed（completed=1 / failed=2），但两个「失败」任务的
// result_json.summary 里已经存有完整交付物（6817 / 6113 字符），失败原因只是
// 只读策略拒绝了复合 shell 命令——一个与任务目标无关的执行细节。父代理若只读
// 状态、不读正文，会把已经完成的活判为失败并重派。
//
// 本用例把同一场景打到真实协调器 + 真实批存储 + 真实 read_agent_result 渲染器上，
// 断言修复后的契约：
//  1. 交付物仍可被父代理按 task 取回（H1 读出口 + H10 交付物保全）；
//  2. 终态诚实地报告「失败但有交付物」（failed_with_result 子计数 + 读出口
//     result_available / do_not_retry 指引），父代理不必在「状态说失败」和
//     「正文存在」之间二选一；
//  3. 不触发重派：worker 只被派发一次。
func TestBatchPolicyRefusalKeepsDeliverableAndDoesNotRedispatch(t *testing.T) {
	store := testStore(t)
	second := policyRefusalDeliverable("DELIVERABLE-T2", 6113)
	third := policyRefusalDeliverable("DELIVERABLE-T3", 6817)
	exec := &countingExecutor{results: []SubagentResult{
		{ID: "t1", Role: "researcher", SessionID: "child-1", Success: true, Summary: "audit A: nothing to change"},
		{ID: "t2", Role: "researcher", SessionID: "child-2", Success: false,
			Error: "policy: read-only mode refused compound shell command", Summary: second},
		{ID: "t3", Role: "researcher", SessionID: "child-3", Success: false,
			Error: "policy: read-only mode refused compound shell command", Summary: third},
	}}
	c := &SubagentBatchCoordinator{
		store:    store,
		executor: exec,
		deadline: time.Minute,
		cancels:  make(map[string]context.CancelFunc),
	}

	batch, err := c.StartBackground(context.Background(), BatchStartOptions{
		TraceID:         "trace-replay-42d0a52d",
		ParentSessionID: "session-replay",
		ParentTurnID:    "turn-1",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
	}, []SubagentTask{
		{ID: "t1", Role: "researcher", Goal: "read-only audit A", ReadOnly: true, Difficulty: "hard"},
		{ID: "t2", Role: "researcher", Goal: "read-only audit B", ReadOnly: true, Difficulty: "hard"},
		{ID: "t3", Role: "researcher", Goal: "read-only audit C", ReadOnly: true, Difficulty: "hard"},
	})
	if err != nil {
		t.Fatalf("StartBackground: %v", err)
	}

	term := waitTerminal(t, store, batch.BatchID)
	// The batch stays honestly failed (the tasks did not do what was asked);
	// what changed is that the failure now carries its deliverable and says so.
	if term.Status != subagentbatch.BatchFailed {
		t.Fatalf("terminal status = %s, want failed (honest failure, not a bare one)", term.Status)
	}
	var summary subagentbatch.BatchSummary
	if err := json.Unmarshal(term.ResultSummary, &summary); err != nil {
		t.Fatalf("unmarshal terminal summary: %v", err)
	}
	if summary.CompletedCount != 1 || summary.FailedCount != 2 || summary.FailedWithResultCount != 2 {
		t.Fatalf("summary counts = completed:%d failed:%d failed_with_result:%d, want 1/2/2",
			summary.CompletedCount, summary.FailedCount, summary.FailedWithResultCount)
	}
	if got := summary.TaskStatuses["t2"]; got != string(subagentbatch.TaskFailedWithResult) {
		t.Fatalf("summary task status t2 = %q, want %q", got, subagentbatch.TaskFailedWithResult)
	}

	records, err := store.ListTasks(context.Background(), batch.BatchID)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	byID := make(map[string]subagentbatch.SubagentTaskRecord, len(records))
	for _, rec := range records {
		byID[rec.TaskID] = rec
	}
	for taskID, deliverable := range map[string]string{"t2": second, "t3": third} {
		rec, ok := byID[taskID]
		if !ok {
			t.Fatalf("task %s missing from durable records", taskID)
		}
		if rec.Status != subagentbatch.TaskFailedWithResult {
			t.Fatalf("task %s status = %s, want failed_with_result", taskID, rec.Status)
		}
		var result subagentbatch.TaskResult
		if err := json.Unmarshal(rec.ResultSummary, &result); err != nil {
			t.Fatalf("task %s: unmarshal result capsule: %v", taskID, err)
		}
		if len(result.Summary) < 6000 {
			t.Fatalf("task %s deliverable = %d runes, want the 6000-rune-class payload to survive", taskID, len(result.Summary))
		}
		if !strings.Contains(result.Summary, deliverable) {
			t.Fatalf("task %s deliverable was altered in the durable capsule", taskID)
		}

		// The parent-facing read surface must hand back the body (H1) and flag
		// the failure as "read, do not re-dispatch" (H10).
		payload := supervision.BuildReadResultPayload(supervision.AgentResultRecord{
			Source:    "task_result",
			TaskID:    taskID,
			SessionID: rec.ChildSessionID,
			Status:    string(rec.Status),
			Success:   false,
			Summary:   result.Summary,
		}, supervision.ReadResultArgs{
			TaskID:   taskID,
			Sections: []string{supervision.ReadResultSectionSummary},
			MaxChars: 4000,
		})
		if strings.TrimSpace(payload.Summary) == "" {
			t.Fatalf("task %s: read_agent_result returned an empty body for a stored deliverable", taskID)
		}
		if !strings.Contains(payload.Summary, "DELIVERABLE-"+strings.ToUpper(taskID)) {
			t.Fatalf("task %s: read payload body does not contain the deliverable head", taskID)
		}
		if !payload.ResultAvailable || !payload.DoNotRetry {
			t.Fatalf("task %s: read payload does not warn against re-dispatch: %+v", taskID, payload)
		}

		// Pagination is the H4 contract: an offset window must skip the head
		// instead of silently returning the same bounded view.
		paged := supervision.BuildReadResultPayload(supervision.AgentResultRecord{
			Source:  "task_result",
			TaskID:  taskID,
			Status:  string(rec.Status),
			Success: false,
			Summary: result.Summary,
		}, supervision.ReadResultArgs{
			TaskID:   taskID,
			Sections: []string{supervision.ReadResultSectionSummary},
			Offset:   100,
			Limit:    200,
		})
		if strings.Contains(paged.Summary, "DELIVERABLE-"+strings.ToUpper(taskID)) {
			t.Fatalf("task %s: offset window still returned the head (offset ignored)", taskID)
		}
		if paged.TotalRunes < 6000 {
			t.Fatalf("task %s: paged read reports total_runes=%d, want the full deliverable size", taskID, paged.TotalRunes)
		}
	}

	if got := atomic.LoadInt32(&exec.calls); got != 1 {
		t.Fatalf("worker dispatched %d times, want 1 (the parent must not re-dispatch work that already exists)", got)
	}
}

// §8.2-4 运行期可观测：批次运行中用 task_id 解析子会话与进度锚点。
//
// 取证结论是「父代理在子代理运行期间，通过工具面和持久层都拿不到任何可解析的
// 进度锚点」。修复（H7 早期绑定 + H8 任务级 deadline/进度戳 + H6 读侧派生）之后，
// 运行中的 task 行必须在子会话仍工作时就带齐 child_session_id / task_deadline /
// last_progress_at，并且父代理读到的进度投影必须与 task 行一致——而不是照抄 batch
// 行上允许滞后的计数列（P1-1 方案 1 二选一：读侧派生，禁止双写漂移）。
func TestBatchRunningTaskExposesProgressAnchor(t *testing.T) {
	store := testStore(t)
	exec := &anchoredExecutor{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	c := &SubagentBatchCoordinator{
		store:    store,
		executor: exec,
		deadline: time.Minute,
		// M1 progress write-back is opt-in via interval > 0 and background
		// mode; the short interval keeps last_progress_at fresh so the test
		// covers the write-back path while the task runs.
		taskProgressEvery: 25 * time.Millisecond,
		cancels:           make(map[string]context.CancelFunc),
	}

	batch, err := c.StartBackground(context.Background(), BatchStartOptions{
		ParentSessionID: "session-observable",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
	}, []SubagentTask{{ID: "t1", Role: "researcher", Goal: "long running audit"}})
	if err != nil {
		t.Fatalf("StartBackground: %v", err)
	}

	select {
	case <-exec.started:
	case <-time.After(5 * time.Second):
		t.Fatalf("worker never started")
	}

	// While the child is still working, the task row is the parent's only
	// durable anchor. Every field below was NULL/absent before the fix.
	rec, err := store.GetTask(context.Background(), batch.BatchID, "t1")
	if err != nil {
		t.Fatalf("GetTask(running): %v", err)
	}
	if rec == nil {
		t.Fatalf("running task row missing")
	}
	if rec.Status != subagentbatch.TaskRunning {
		t.Fatalf("running task status = %s, want running", rec.Status)
	}
	if strings.TrimSpace(rec.ChildSessionID) != "child-live-t1" {
		t.Fatalf("child_session_id = %q, want the early-bound child session (H7)", rec.ChildSessionID)
	}
	if rec.TaskDeadline.IsZero() {
		t.Fatalf("task_deadline is zero while the task is running (H8)")
	}
	if rec.StartedAt == nil {
		t.Fatalf("started_at is nil while the task is running")
	}
	if rec.LastProgressAt == nil {
		t.Fatalf("last_progress_at is nil right after the started transition (H8)")
	}

	// H6 的落地契约是「读侧一律由 task 行派生计数」，batch 行上的计数列只在建批与
	// 终态收敛时刷新。因此断言父代理真正读到的那条投影（progressGroup 的派生路径），
	// 而不是允许滞后的存储列——取证批次的 `queued=3, running=0` 正是列与明细互相否定。
	groups, err := (&supervision.BatchProgressSource{Store: store}).ListProgress(
		context.Background(), supervision.ProgressRequest{ParentSessionID: "session-observable"})
	if err != nil {
		t.Fatalf("ListProgress: %v", err)
	}
	var group *supervision.ProgressGroup
	for i := range groups {
		if groups[i].GroupID == batch.BatchID {
			group = &groups[i]
			break
		}
	}
	if group == nil {
		t.Fatalf("running batch missing from the parent progress rollup: %+v", groups)
	}
	if group.Terminal {
		t.Fatalf("running batch reported as terminal")
	}
	if group.Total != 1 || group.Running != 1 || group.Pending != 0 || group.Completed != 0 || group.Failed != 0 {
		t.Fatalf("progress counters = total:%d running:%d pending:%d completed:%d failed:%d, want 1/1/0/0/0 (H6: derived from task rows)",
			group.Total, group.Running, group.Pending, group.Completed, group.Failed)
	}
	if len(group.RunningTasks) != 1 {
		t.Fatalf("running task detail = %+v, want exactly one expandable task", group.RunningTasks)
	}
	detail := group.RunningTasks[0]
	if detail.TaskID != "t1" || detail.ChildSessionID != "child-live-t1" || detail.State != string(subagentbatch.TaskRunning) {
		t.Fatalf("running task detail = %+v, want t1/child-live-t1/running", detail)
	}
	if detail.LastProgressAt.IsZero() {
		t.Fatalf("running task detail carries no progress stamp")
	}

	close(exec.release)
	term := waitTerminal(t, store, batch.BatchID)
	if term.Status != subagentbatch.BatchCompleted {
		t.Fatalf("terminal status = %s, want completed", term.Status)
	}
	if term.CompletedCount != 1 || term.RunningCount != 0 {
		t.Fatalf("terminal counters = completed:%d running:%d, want 1/0", term.CompletedCount, term.RunningCount)
	}
	settled, err := store.GetTask(context.Background(), batch.BatchID, "t1")
	if err != nil || settled == nil {
		t.Fatalf("GetTask(terminal): rec=%v err=%v", settled, err)
	}
	if settled.Status != subagentbatch.TaskSucceeded {
		t.Fatalf("terminal task status = %s, want succeeded", settled.Status)
	}
	if strings.TrimSpace(settled.ChildSessionID) != "child-live-t1" {
		t.Fatalf("child_session_id after settle = %q, want the same early binding", settled.ChildSessionID)
	}
}

// policyRefusalDeliverable builds a 6000-rune-class deliverable with a stable
// head sentinel, so assertions can tell "the payload survived" from "an empty
// capsule was rendered".
func policyRefusalDeliverable(sentinel string, size int) string {
	head := sentinel + "-HEAD"
	line := "read-only audit finding: the compound shell command was refused by policy; the analysis was still produced.\n"
	var b strings.Builder
	b.WriteString(head + "\n")
	for b.Len() < size-len(sentinel)-len("-TAIL") {
		b.WriteString(line)
	}
	b.WriteString(sentinel + "-TAIL")
	return b.String()
}

// countingExecutor fires the task lifecycle events and counts dispatches, so a
// replay test can assert the worker ran exactly once.
type countingExecutor struct {
	results []SubagentResult
	calls   int32
}

func (e *countingExecutor) RunChildren(_ context.Context, options SubagentRunOptions, tasks []SubagentTask) ([]SubagentResult, error) {
	atomic.AddInt32(&e.calls, 1)
	for _, task := range tasks {
		options.notifyTaskEvent(task.ID, "started")
	}
	for _, task := range tasks {
		options.notifyTaskEvent(task.ID, "completed")
	}
	return e.results, nil
}

// anchoredExecutor mirrors what the real scheduler does before a child starts:
// bind the child session (H7) and fire the started transition (H8), then block
// so the test can inspect the durable row while the task is still running.
type anchoredExecutor struct {
	started chan struct{}
	release chan struct{}
}

func (e *anchoredExecutor) RunChildren(ctx context.Context, options SubagentRunOptions, tasks []SubagentTask) ([]SubagentResult, error) {
	for _, task := range tasks {
		options.notifyTaskBound(task.ID, "child-live-"+task.ID)
		options.notifyTaskEvent(task.ID, "started")
	}
	close(e.started)
	select {
	case <-ctx.Done():
	case <-e.release:
	}
	results := make([]SubagentResult, len(tasks))
	for i, task := range tasks {
		options.notifyTaskEvent(task.ID, "completed")
		results[i] = SubagentResult{
			ID:        task.ID,
			Role:      task.Role,
			SessionID: "child-live-" + task.ID,
			Success:   true,
			Summary:   "audit complete",
		}
	}
	return results, nil
}
