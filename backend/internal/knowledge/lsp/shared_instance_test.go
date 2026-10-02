package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	baselsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
)

// shared_instance_test.go 锁定知识层语义通道的单实例契约。
//
// 这些断言覆盖的是**不该发生的事**：共享模式下不得 spawn、不得关停宿主进程、
// 不得动锁文件、借不到时不得退化成自建。每一条都对应一种具体的静默失效。

// ---- 夹具 ----

// recordingDial 记录 spawn 次数：单实例的第一判据就是"没有第二次 spawn"。
type recordingDial struct {
	calls int
	dial  baselsp.DialFunc
}

func (r *recordingDial) dialFunc(ctx context.Context, spec baselsp.ServerSpec, root string, logger baselsp.Logger) (*baselsp.DialResult, error) {
	r.calls++
	if r.dial == nil {
		return nil, context.Canceled
	}
	return r.dial(ctx, spec, root, logger)
}

func newSharedTestManager(t *testing.T, borrow *baselsp.Client) (*Manager, *recordingDial) {
	t.Helper()
	recorder := &recordingDial{}
	opts := Options{
		Root:           t.TempDir(),
		Spec:           baselsp.ServerSpec{Name: "gopls", Command: "gopls", Extensions: []string{".go"}},
		SkipLock:       true, // 单实例断言与锁无关：锁行为另有用例
		StartupTimeout: 2 * time.Second,
		Dial:           recorder.dialFunc,
		SharedClient:   func(context.Context, string) *baselsp.Client { return borrow },
	}
	return NewManager(opts), recorder
}

func TestSharedModeNeverSpawns(t *testing.T) {
	recorder := &recordingDial{}
	borrowed := newFakeClient(t)
	manager := NewManager(Options{
		Root:           t.TempDir(),
		Spec:           baselsp.ServerSpec{Name: "gopls", Command: "gopls", Extensions: []string{".go"}},
		Dial:           recorder.dialFunc,
		SharedClient:   func(context.Context, string) *baselsp.Client { return borrowed },
		StartupTimeout: 2 * time.Second,
	})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := manager.Ensure(ctx); err != nil {
			t.Fatalf("Ensure #%d: %v", i, err)
		}
	}
	if recorder.calls != 0 {
		t.Fatalf("共享模式 spawn 了 %d 次进程：单实例约束被破坏", recorder.calls)
	}
	if manager.Client() != borrowed {
		t.Fatal("Client() 必须返回借来的那个")
	}
	if !manager.Available() {
		t.Fatal("借来的进程活着时 Available 必须为 true")
	}
	status := manager.Status()
	if !status.Shared {
		t.Fatal("状态必须标记 Shared（否则用户无法解释 pid 与诊断池相同）")
	}
	if status.PID != borrowed.PID() {
		t.Fatalf("status pid = %d, want %d（借来的进程）", status.PID, borrowed.PID())
	}
	if status.LockPath != "" {
		t.Fatalf("共享模式不得报告锁路径（我们从未持锁）：%q", status.LockPath)
	}
}

func TestSharedModeDegradesInsteadOfSpawningWhenHostHasNone(t *testing.T) {
	recorder := &recordingDial{}
	manager := NewManager(Options{
		Root:           t.TempDir(),
		Spec:           baselsp.ServerSpec{Name: "gopls", Command: "gopls", Extensions: []string{".go"}},
		Dial:           recorder.dialFunc,
		SharedClient:   func(context.Context, string) *baselsp.Client { return nil },
		StartupTimeout: 2 * time.Second,
	})
	err := manager.Ensure(context.Background())
	if err == nil {
		t.Fatal("宿主没有进程时必须降级")
	}
	if !strings.Contains(err.Error(), ErrSharedClientUnavailable.Error()) {
		t.Fatalf("降级原因 = %v, want 含 %q", err, ErrSharedClientUnavailable)
	}
	if recorder.calls != 0 {
		t.Fatalf("宿主拿不出进程时却 spawn 了 %d 次——那正是要消除的第二个实例", recorder.calls)
	}
	if manager.Available() {
		t.Fatal("降级后 Available 必须为 false")
	}
	if st := manager.Status(); !strings.Contains(st.Reason, ErrSharedClientUnavailable.Error()) {
		t.Fatalf("状态必须带降级原因，实际: %q", st.Reason)
	}
}

func TestSharedModeCloseDoesNotShutDownHostProcess(t *testing.T) {
	borrowed := newFakeClient(t)
	manager, _ := newSharedTestManager(t, borrowed)
	if err := manager.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// 宿主进程必须还活着：关停它会连带打断诊断池。
	select {
	case <-borrowed.Done():
		t.Fatal("Close 关停了借来的进程——进程归宿主所有，语义通道无权关停")
	default:
	}
	if manager.Available() {
		t.Fatal("Close 后本通道必须不可用")
	}
}

func TestSharedModeReacquiresAfterHostRestarts(t *testing.T) {
	first := newFakeClient(t)
	second := newFakeClient(t)
	var current *baselsp.Client = first
	manager := NewManager(Options{
		Root:           t.TempDir(),
		Spec:           baselsp.ServerSpec{Name: "gopls", Command: "gopls", Extensions: []string{".go"}},
		Dial:           (&recordingDial{}).dialFunc,
		SharedClient:   func(context.Context, string) *baselsp.Client { return current },
		StartupTimeout: 2 * time.Second,
	})
	if err := manager.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if manager.Client() != first {
		t.Fatal("首次借用错误")
	}
	// 宿主重启：旧 client 关闭，新 client 就位。
	first.Shutdown(context.Background())
	current = second
	// 关键：不得因为"进程退出"把降级原因钉死——否则宿主重启一次，语义通道
	// 在整个进程生命周期里都不可用。
	if manager.Available() {
		t.Fatal("旧 client 已退出，Available 应为 false")
	}
	if reason := manager.DegradeReason(); reason != "" {
		t.Fatalf("共享模式下进程退出不得钉死降级原因（宿主随时会重启它）：%q", reason)
	}
	if err := manager.Ensure(context.Background()); err != nil {
		t.Fatalf("宿主重启后必须能重新借用：%v", err)
	}
	if manager.Client() != second {
		t.Fatal("重新借用必须拿到新 client，不得复用已退出的旧句柄")
	}
	if st := manager.Status(); !st.Shared || st.State != "ready" {
		t.Fatalf("状态 = %+v, want shared+ready", st)
	}
}

func TestSelfModeStillSpawnsAndLocks(t *testing.T) {
	// 反向断言：没有借用缝时（诊断池关闭）语义通道仍然自建 + 持锁。
	// 没有这条，单实例改造可能把"只有 knowledge.lsp 打开"的配置弄坏。
	recorder := &recordingDial{}
	manager := NewManager(Options{
		Root:           t.TempDir(),
		Spec:           baselsp.ServerSpec{Name: "gopls", Command: "gopls", Extensions: []string{".go"}},
		Dial: func(context.Context, baselsp.ServerSpec, string, baselsp.Logger) (*baselsp.DialResult, error) {
			recorder.calls++
			return newFakeServer(t).dial()(context.Background(), baselsp.ServerSpec{}, "", nil)
		},
		StartupTimeout: 5 * time.Second,
	})
	lockPath := manager.LockPath()
	if err := manager.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if recorder.calls != 1 {
		t.Fatalf("自建模式 spawn %d 次，want 1", recorder.calls)
	}
	raw, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("自建模式必须持锁（ADR-0002 §4.4）: %v", err)
	}
	var record LockRecord
	if err := json.Unmarshal(raw, &record); err != nil || record.PID != os.Getpid() {
		t.Fatalf("锁记录 = %+v（err=%v），want 本进程 pid", record, err)
	}
	if st := manager.Status(); st.Shared {
		t.Fatal("自建模式不得标记 Shared")
	}
	if err := manager.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("Close 后应释放锁，stat err = %v", err)
	}
}

func TestSharedModeTransientBorrowFailureIsRetryable(t *testing.T) {
	// 宿主"暂时"借不出（还在启动 / 刚 restart）不得被判成永久降级：
	// 一次 /lsp restart 就把语义通道废掉整个会话，是最容易被忽略的失效。
	var borrow *baselsp.Client
	manager := NewManager(Options{
		Root:         t.TempDir(),
		Spec:         baselsp.ServerSpec{Name: "gopls", Command: "gopls", Extensions: []string{".go"}},
		SharedClient: func(context.Context, string) *baselsp.Client { return borrow },
	})
	if err := manager.Ensure(context.Background()); err == nil {
		t.Fatal("宿主没有进程时必须降级")
	}
	if !manager.RetryableDegrade() {
		t.Fatal("共享模式借不到必须可重试（宿主随时会恢复）")
	}
	// 宿主恢复后下一次 Ensure 必须能借到。
	borrow = newFakeClient(t)
	if err := manager.Ensure(context.Background()); err != nil {
		t.Fatalf("宿主恢复后必须能重新借用: %v", err)
	}
	if st := manager.Status(); st.State != "ready" || !st.Shared {
		t.Fatalf("状态 = %+v, want ready+shared", st)
	}
}

func TestSelfModeStartupFailureIsNotRetryable(t *testing.T) {
	// 反向断言：自建模式的启动失败不可逐查询重试（重启风暴）。
	manager := NewManager(Options{
		Root: t.TempDir(),
		Spec: baselsp.ServerSpec{Name: "gopls", Command: "gopls", Extensions: []string{".go"}},
		Dial: func(context.Context, baselsp.ServerSpec, string, baselsp.Logger) (*baselsp.DialResult, error) {
			return nil, errors.New("exec: gopls not found")
		},
	})
	if err := manager.Ensure(context.Background()); err == nil {
		t.Fatal("启动失败必须降级")
	}
	if manager.RetryableDegrade() {
		t.Fatal("自建模式的启动失败不可逐查询重试")
	}
}
