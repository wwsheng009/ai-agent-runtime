package knowledge

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// openTestLayer 在 root 上打开一个 shadow 层（owner 或 reader 由锁仲裁决定）。
func openTestLayer(t *testing.T, root string) *Layer {
	t.Helper()
	cfg := DefaultConfig().WithWorkspace(root)
	cfg.Mode = ModeShadow
	layer, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Open(%s): %v", root, err)
	}
	t.Cleanup(func() { _ = layer.Close() })
	return layer
}

// TestLayerStatusOwnerAfterIndex 钉住状态面的核心事实：索引跑完后，
// 行数 / 落点 / 最近 job / 无降级 必须一次全部可见（06 §4 Phase 1 交付 5）。
func TestLayerStatusOwnerAfterIndex(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/demo.go", demoGoSource)

	layer := openTestLayer(t, root)
	result, err := layer.Index(ctx)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if result.Indexed == 0 {
		t.Fatalf("index produced no rows: %+v", result)
	}

	report, err := layer.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if report.Mode != ModeShadow || !report.Enabled {
		t.Fatalf("mode = %q enabled=%v, want shadow/true", report.Mode, report.Enabled)
	}
	if report.Role != RoleOwner {
		t.Fatalf("role = %q, want owner", report.Role)
	}
	if report.OwnerPID != os.Getpid() {
		t.Fatalf("owner_pid = %d, want %d", report.OwnerPID, os.Getpid())
	}
	if report.Workspace != root {
		t.Fatalf("workspace = %q, want %q", report.Workspace, root)
	}
	if report.DBPath == "" || report.DBSizeBytes <= 0 {
		t.Fatalf("db path/size = %q/%d, want non-empty/positive", report.DBPath, report.DBSizeBytes)
	}
	if report.SchemaVersion < 1 {
		t.Fatalf("schema_version = %d, want >= 1", report.SchemaVersion)
	}
	if report.Files < 1 || report.Symbols < 1 || report.Refs < 1 {
		t.Fatalf("files/symbols/refs = %d/%d/%d, want all >= 1", report.Files, report.Symbols, report.Refs)
	}
	if report.IndexedAt <= 0 || report.StalenessMS < 0 {
		t.Fatalf("indexed_at/staleness = %d/%d, want positive/non-negative", report.IndexedAt, report.StalenessMS)
	}
	if report.LastJob == nil {
		t.Fatalf("last_job = nil, want a done job (RunIndex 必须先写 index_jobs)")
	}
	if report.LastJob.Status != IndexJobStatusDone {
		t.Fatalf("last_job.status = %q, want done", report.LastJob.Status)
	}
	if report.LastJob.FilesTotal != result.Scanned {
		t.Fatalf("last_job.files_total = %d, want %d", report.LastJob.FilesTotal, result.Scanned)
	}
	wantDone := result.Indexed + result.Skipped + result.Errors
	if report.LastJob.FilesDone != wantDone {
		t.Fatalf("last_job.files_done = %d, want %d", report.LastJob.FilesDone, wantDone)
	}
	if report.LastJob.Error != "" || report.LastJob.FinishedAt == 0 {
		t.Fatalf("last_job error/finished = %q/%d, want empty/non-zero", report.LastJob.Error, report.LastJob.FinishedAt)
	}
	if report.IndexRunning {
		t.Fatalf("index_running = true, want false for a plain Layer")
	}
	if report.DegradedReason != "" {
		t.Fatalf("degraded_reason = %q, want empty", report.DegradedReason)
	}
	// 单写者稳态下不应有锁等待：样本为零是"没发生竞争"的证据。
	if report.LockWait.Samples != 0 || report.LockWait.RetryFailures != 0 {
		t.Fatalf("lock_wait = %+v, want zero samples", report.LockWait)
	}
}

// TestLayerStatusNotIndexedYet 钉住"未索引"是可解释状态而不是错误：
// 零值必须带 degraded_reason，否则状态面会静默说谎。
func TestLayerStatusNotIndexedYet(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	layer := openTestLayer(t, root)

	report, err := layer.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if report.Files != 0 || report.IndexedAt != 0 || report.LastJob != nil {
		t.Fatalf("unexpected rows before first index: %+v", report)
	}
	if report.DegradedReason != "workspace is not indexed yet" {
		t.Fatalf("degraded_reason = %q", report.DegradedReason)
	}
	if report.Role != RoleOwner {
		t.Fatalf("role = %q, want owner (未索引不改变角色)", report.Role)
	}
}

// TestLayerStatusReaderDegraded 钉住 reader 路径：状态面必须可读，
// 且明确标注"只读 + 谁在写"，否则多进程排障只能靠猜。
func TestLayerStatusReaderDegraded(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "demo/demo.go", demoGoSource)

	owner := openTestLayer(t, root)
	if owner.Role() != RoleOwner {
		t.Fatalf("first layer role = %q, want owner", owner.Role())
	}
	if _, err := owner.Index(ctx); err != nil {
		t.Fatalf("Index: %v", err)
	}

	reader := openTestLayer(t, root)
	if reader.Role() != RoleReader {
		t.Fatalf("second layer role = %q, want reader", reader.Role())
	}
	report, err := reader.Status(ctx)
	if err != nil {
		t.Fatalf("reader Status: %v", err)
	}
	if report.Role != RoleReader {
		t.Fatalf("role = %q, want reader", report.Role)
	}
	if report.OwnerPID != os.Getpid() {
		t.Fatalf("owner_pid = %d, want %d (本进程持有锁)", report.OwnerPID, os.Getpid())
	}
	if !strings.HasPrefix(report.DegradedReason, "read-only:") {
		t.Fatalf("degraded_reason = %q, want read-only prefix", report.DegradedReason)
	}
	// 读者能看到 owner 的索引成果与 job 账本（纯读，不写库）。
	if report.Files < 1 || report.Symbols < 1 {
		t.Fatalf("reader sees files/symbols = %d/%d, want >= 1", report.Files, report.Symbols)
	}
	if report.LastJob == nil || report.LastJob.Status != IndexJobStatusDone {
		t.Fatalf("reader last_job = %+v, want done", report.LastJob)
	}
	if report.LockWait.Samples != 0 {
		t.Fatalf("reader lock_wait samples = %d, want 0 (读者不写库)", report.LockWait.Samples)
	}
}

// TestIndexJobLifecycle 钉住 index_jobs 的读写契约：先写后跑、终态必落、
// 错误摘要截断、空 workspace 拒绝。
func TestIndexJobLifecycle(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	root := t.TempDir()
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: root})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	if _, err := store.StartIndexJob(ctx, IndexJob{}); err == nil {
		t.Fatalf("StartIndexJob with empty workspace must fail")
	}

	jobID, err := store.StartIndexJob(ctx, IndexJob{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("StartIndexJob: %v", err)
	}
	job, err := store.LatestIndexJob(ctx, wsID)
	if err != nil {
		t.Fatalf("LatestIndexJob: %v", err)
	}
	if job == nil || job.Status != IndexJobStatusRunning || job.Kind != IndexJobKindLight {
		t.Fatalf("running job = %+v, want running/light", job)
	}
	if err := store.UpdateIndexJob(ctx, jobID, 7); err != nil {
		t.Fatalf("UpdateIndexJob: %v", err)
	}
	longErr := strings.Repeat("x", maxIndexJobErrorBytes+64)
	if err := store.FinishIndexJob(ctx, jobID, IndexJobStatusFailed, 9, 7, longErr); err != nil {
		t.Fatalf("FinishIndexJob: %v", err)
	}
	job, err = store.LatestIndexJob(ctx, wsID)
	if err != nil {
		t.Fatalf("LatestIndexJob(after finish): %v", err)
	}
	if job.Status != IndexJobStatusFailed || job.FilesTotal != 9 || job.FilesDone != 7 {
		t.Fatalf("finished job = %+v", job)
	}
	if !strings.HasSuffix(job.Error, "(truncated)") || len(job.Error) > maxIndexJobErrorBytes+16 {
		t.Fatalf("error summary not truncated: len=%d", len(job.Error))
	}
	if job.FinishedAt == 0 || job.DurationMS < 0 {
		t.Fatalf("finished_at/duration = %d/%d", job.FinishedAt, job.DurationMS)
	}
	if err := store.FinishIndexJob(ctx, "job_missing", IndexJobStatusDone, 0, 0, ""); err == nil {
		t.Fatalf("FinishIndexJob on missing job must fail")
	}
}

// TestActivationStatusNilAndSkipIndex 钉住 off 与 SkipInitialIndex 的载荷形态。
func TestActivationStatusNilAndSkipIndex(t *testing.T) {
	ctx := context.Background()

	var off *Activation
	report, err := off.Status(ctx)
	if err != nil {
		t.Fatalf("nil Activation Status: %v", err)
	}
	if report.Mode != ModeOff || report.Role != RoleNone || report.Enabled {
		t.Fatalf("off report = %+v, want mode=off/role=none", report)
	}
	if report.IndexRunning || off.IndexRunning() {
		t.Fatalf("nil Activation must not report a running index")
	}

	root := t.TempDir()
	cfg := DefaultConfig().WithWorkspace(root)
	cfg.Mode = ModeShadow
	act, err := Activate(ctx, cfg, root, ActivationOptions{SkipInitialIndex: true})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	t.Cleanup(func() { _ = act.Close() })
	report, err = act.Status(ctx)
	if err != nil {
		t.Fatalf("Activation Status: %v", err)
	}
	if report.Mode != ModeShadow || report.Role != RoleOwner {
		t.Fatalf("report = %+v, want shadow/owner", report)
	}
	if report.IndexRunning {
		t.Fatalf("SkipInitialIndex 后 index_running 必须为 false")
	}
}

// TestLockWaitRecorderPercentile 钉住近似分位口径（nearest-rank + 128 环）。
func TestLockWaitRecorderPercentile(t *testing.T) {
	var rec lockWaitRecorder
	for i := 1; i <= 100; i++ {
		rec.observe(time.Duration(i) * time.Millisecond)
	}
	stats := rec.snapshot()
	if stats.Samples != 100 {
		t.Fatalf("samples = %d, want 100", stats.Samples)
	}
	if stats.P50MS != 50 || stats.P95MS != 95 || stats.MaxMS != 100 {
		t.Fatalf("p50/p95/max = %v/%v/%v, want 50/95/100", stats.P50MS, stats.P95MS, stats.MaxMS)
	}

	for i := 101; i <= 200; i++ {
		rec.observe(time.Duration(i) * time.Millisecond)
	}
	stats = rec.snapshot()
	if stats.Samples != lockWaitSamples {
		t.Fatalf("samples = %d, want %d (有界环)", stats.Samples, lockWaitSamples)
	}
	// 环内是 73..200ms；nearest-rank p95 = ceil(0.95*128) = 122 → 73+121 = 194。
	if stats.P95MS != 194 || stats.MaxMS != 200 {
		t.Fatalf("p95/max = %v/%v, want 194/200", stats.P95MS, stats.MaxMS)
	}

	rec.observeFailure()
	if got := rec.snapshot().RetryFailures; got != 1 {
		t.Fatalf("retry_failures = %d, want 1", got)
	}
}
