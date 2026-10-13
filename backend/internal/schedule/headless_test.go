package schedule

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHeadlessExecutor 实现 SessionExecutor + HeadlessExecutor，记录 headless 请求。
type fakeHeadlessExecutor struct {
	mu   sync.Mutex
	reqs []HeadlessTurnRequest
}

func (e *fakeHeadlessExecutor) ExecuteTurn(context.Context, string, string) error { return nil }

func (e *fakeHeadlessExecutor) ExecuteHeadlessTurn(_ context.Context, req HeadlessTurnRequest) (HeadlessTurnResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reqs = append(e.reqs, req)
	return HeadlessTurnResult{SessionID: "ephemeral-" + req.JobID, Output: "job output"}, nil
}

func (e *fakeHeadlessExecutor) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.reqs)
}

func newHeadlessJob(t *testing.T, svc *Service, req CreateRequest) Record {
	t.Helper()
	rec, err := svc.Create("", req, OriginModel)
	if err != nil {
		t.Fatalf("Create headless job: %v", err)
	}
	return rec
}

// TestHeadlessJobExecutesAndCommits 验证 J2：headless 作业到点经 HeadlessExecutor
// 执行、携带 goal/plan、并推进任务（一次性转 inactive）。
func TestHeadlessJobExecutesAndCommits(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	svc := NewService(store, func() time.Time { return now })
	delay := int64(60)
	rec := newHeadlessJob(t, svc, CreateRequest{
		Prompt: "p", Title: "t", AfterSeconds: &delay,
		Goal: "做一次系统检查", Plan: []string{"检查磁盘", "检查内存"},
		TargetKind: TargetHeadless,
	})

	exec := &fakeHeadlessExecutor{}
	rt := NewRuntime(RuntimeOptions{
		Store: store, Service: svc, Clock: func() time.Time { return now },
		Deliverer: &flakyDeliverer{}, MaxAttempts: 3, FallbackToMailbox: true,
		Mode: ModeEmbedded, Executor: exec,
	})
	defer rt.Stop()

	now = now.Add(2 * time.Minute)
	rt.RequestDrive()
	time.Sleep(150 * time.Millisecond)

	if exec.count() != 1 {
		t.Fatalf("headless job must execute exactly once, got %d", exec.count())
	}
	got := exec.reqs[0]
	if got.JobID != rec.ID || got.Goal != "做一次系统检查" || len(got.Plan) != 2 || got.TargetKind != TargetHeadless {
		t.Fatalf("headless request mismatch: %+v", got)
	}
	task, ok, err := store.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if task.Status != StatusInactive {
		t.Fatalf("one-time headless job must be inactive after execution, got %s", task.Status)
	}
}

// TestHeadlessJobDeferredWithoutHeadlessExecutor 验证 J2：执行核不支持 headless 时，
// 非 session 目标被**延后**（保持 active、不空转、不计死信、不落 pending）。
func TestHeadlessJobDeferredWithoutHeadlessExecutor(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	svc := NewService(store, func() time.Time { return now })
	delay := int64(60)
	rec := newHeadlessJob(t, svc, CreateRequest{
		Prompt: "p", Title: "t", AfterSeconds: &delay, Goal: "做一次系统检查", TargetKind: TargetHeadless,
	})

	// embeddedExecutor 只实现 SessionExecutor，不实现 HeadlessExecutor。
	exec := &embeddedExecutor{}
	rt := NewRuntime(RuntimeOptions{
		Store: store, Service: svc, Clock: func() time.Time { return now },
		Deliverer: &flakyDeliverer{}, MaxAttempts: 3, FallbackToMailbox: true,
		Mode: ModeEmbedded, Executor: exec,
	})
	defer rt.Stop()

	now = now.Add(2 * time.Minute)
	rt.RequestDrive()
	time.Sleep(150 * time.Millisecond)

	if exec.count() != 0 {
		t.Fatalf("no session turn expected for a headless job, got %d", exec.count())
	}
	task, ok, err := store.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if task.Status != StatusActive {
		t.Fatalf("deferred headless job must stay active, got %s", task.Status)
	}
	if snap := rt.Metrics(); snap.DeadLetterTotal != 0 {
		t.Fatalf("deferral must not count as dead letter, got %d", snap.DeadLetterTotal)
	}
	if pending, _ := store.ListPendingMessages("", 10); len(pending) != 0 {
		t.Fatalf("headless job has no session to fall back to; pending must stay empty, got %d", len(pending))
	}
}

// TestHeadlessJobRecordsRunAndDeliversResult 验证 J3：headless 作业执行后
// 落运行历史（含输出）并把结果回投给创建者会话。
func TestHeadlessJobRecordsRunAndDeliversResult(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	svc := NewService(store, func() time.Time { return now })
	delay := int64(60)
	rec, err := svc.Create("creator-session", CreateRequest{
		Prompt: "p", Title: "t", AfterSeconds: &delay, Goal: "做系统检查",
		TargetKind: TargetHeadless,
	}, OriginModel)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	exec := &fakeHeadlessExecutor{}
	deliverer := &flakyDeliverer{} // failFirst=0 → 全部成功并记录
	rt := NewRuntime(RuntimeOptions{
		Store: store, Service: svc, Clock: func() time.Time { return now },
		Deliverer: deliverer, MaxAttempts: 3, Mode: ModeEmbedded, Executor: exec,
	})
	defer rt.Stop()

	now = now.Add(2 * time.Minute)
	rt.RequestDrive()
	time.Sleep(150 * time.Millisecond)

	// 创建者会话被保留（用于结果回投）。
	task, ok, err := store.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if task.CreatedBySessionID != "creator-session" {
		t.Fatalf("creator session must be preserved, got %q", task.CreatedBySessionID)
	}

	// 运行历史落库（含输出）。
	runs, err := store.ListJobRuns(rec.ID, 10)
	if err != nil {
		t.Fatalf("ListJobRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected exactly one job run, got %d", len(runs))
	}
	if runs[0].Status != JobRunDone || runs[0].Output != "job output" {
		t.Fatalf("unexpected job run: %+v", runs[0])
	}

	// 结果回投创建者会话。
	if len(deliverer.records) != 1 {
		t.Fatalf("expected one result delivery, got %d", len(deliverer.records))
	}
	if !strings.HasPrefix(deliverer.records[0], "creator-session|") {
		t.Fatalf("result must be delivered to the creator session, got %q", deliverer.records[0])
	}
	if !strings.Contains(deliverer.records[0], "[SCHEDULE JOB RESULT]") {
		t.Fatalf("result framing missing, got %q", deliverer.records[0])
	}
}

// TestHeadlessResultDeliveryFailureFallsBackToPending 验证 J3 收尾：结果回投失败时
// 落持久 pending（宿主恢复后消费），而不是静默丢失。
func TestHeadlessResultDeliveryFailureFallsBackToPending(t *testing.T) {
	store, err := NewMemoryStore()
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	svc := NewService(store, func() time.Time { return now })
	delay := int64(60)
	rec, err := svc.Create("creator-session", CreateRequest{
		Prompt: "p", Title: "t", AfterSeconds: &delay, Goal: "做系统检查",
		TargetKind: TargetHeadless,
	}, OriginModel)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	exec := &fakeHeadlessExecutor{}
	deliverer := &flakyDeliverer{failFirst: 1} // 回投失败（headless 路径只有这一次投递）
	rt := NewRuntime(RuntimeOptions{
		Store: store, Service: svc, Clock: func() time.Time { return now },
		Deliverer: deliverer, MaxAttempts: 3, FallbackToMailbox: true,
		Mode: ModeEmbedded, Executor: exec,
	})
	defer rt.Stop()

	now = now.Add(2 * time.Minute)
	rt.RequestDrive()
	time.Sleep(150 * time.Millisecond)

	// 作业本身仍成功推进（执行成功，只有回投失败）。
	task, ok, err := store.Get(rec.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if task.Status != StatusInactive {
		t.Fatalf("job must still advance despite result-delivery failure, got %s", task.Status)
	}

	// 回投失败 → 落 pending 到创建者会话。
	pending, err := store.ListPendingMessages("creator-session", 10)
	if err != nil {
		t.Fatalf("ListPendingMessages: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected one pending fallback for the job result, got %d", len(pending))
	}
	if !strings.Contains(pending[0].Prompt, "[SCHEDULE JOB RESULT]") {
		t.Fatalf("pending fallback must carry the result framing, got %q", pending[0].Prompt)
	}
}
