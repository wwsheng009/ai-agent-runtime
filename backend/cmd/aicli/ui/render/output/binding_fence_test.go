package output

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"
)

// ============================================================================
// E1. 迟到 goroutine 隔离（SessionBinding fence 矩阵）
// ============================================================================
// 验收标准：unbind/close 后旧 binding 的 fencedPort.Submit 返回 pre-admission
// error（Primary=nil, Sequence=0）；迟到渲染字节不写入新 session 或 process
// compatibility writer。fence 全部由 fencedPort 承担，gateway 不参与 generation
// 校验——因此被拒提交必须零触达底层 port（calls 不变）。

// recordingSubmitPort 记录底层 port 的调用次数与最近字节，用于断言
// fence 后提交绝不触达底层 port。
type recordingSubmitPort struct {
	mu    sync.Mutex
	calls int
	last  []byte
}

func (p *recordingSubmitPort) Submit(_ context.Context, intent RenderIntent) OutputReceipt {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.last = append(p.last[:0], intent.Bytes...)
	return OutputReceipt{
		Admission: AdmissionReceipt{Decision: AdmissionAccepted},
	}
}

func (p *recordingSubmitPort) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *recordingSubmitPort) LastBytes() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.last...)
}

// assertPreAdmissionRejected 断言 pre-admission rejection 的完整契约：
// Decision=Rejected、ErrorClass=Closed、Primary=nil、Sequence=0、
// TargetInvoked=false。
func assertPreAdmissionRejected(t *testing.T, r OutputReceipt) {
	t.Helper()
	if r.Admission.Decision != AdmissionRejected {
		t.Fatalf("expected AdmissionRejected, got %s", r.Admission.Decision)
	}
	if r.Admission.ErrorClass != DeliveryErrorClosed {
		t.Fatalf("expected DeliveryErrorClosed, got %s", r.Admission.ErrorClass)
	}
	if r.Primary != nil {
		t.Fatalf("pre-admission rejection must have Primary=nil, got %+v", r.Primary)
	}
	if r.Sequence != 0 {
		t.Fatalf("pre-admission rejection must have Sequence=0, got %d", r.Sequence)
	}
	if r.TargetInvoked {
		t.Fatalf("pre-admission rejection must not have invoked any target")
	}
}

// TestE1UnbindRejectsOldFacadeWithoutTouchingPort：unbind 后旧 facade 返回
// pre-admission rejected（Primary=nil, Sequence=0），且底层 port 零触达。
func TestE1UnbindRejectsOldFacadeWithoutTouchingPort(t *testing.T) {
	registry := NewSessionBindingRegistry()
	port := &recordingSubmitPort{}
	ref := registry.Bind("ses-e1-unbind", port)

	ok := ref.Port.Submit(context.Background(), RenderIntent{
		Kind:  TransactionFrame,
		Bytes: []byte("live"),
	})
	if ok.Admission.Decision != AdmissionAccepted {
		t.Fatalf("pre-unbind submit should be accepted, got %+v", ok.Admission)
	}
	if port.Calls() != 1 {
		t.Fatalf("expected 1 underlying call, got %d", port.Calls())
	}

	registry.Unbind("ses-e1-unbind")

	late := ref.Port.Submit(context.Background(), RenderIntent{
		Kind:  TransactionFrame,
		Bytes: []byte("stale-after-unbind"),
	})
	assertPreAdmissionRejected(t, late)

	if port.Calls() != 1 {
		t.Fatalf("fenced submit must not reach underlying port; calls=%d", port.Calls())
	}
}

// TestE1RebindOldGenerationRejectedWithPrimaryNil：rebind 后旧 generation 的
// facade 被 fence，返回 pre-admission rejected 且 Primary=nil、Sequence=0。
func TestE1RebindOldGenerationRejectedWithPrimaryNil(t *testing.T) {
	registry := NewSessionBindingRegistry()
	port := &recordingSubmitPort{}

	old := registry.Bind("ses-e1-rebind", port)
	_ = old.Port.Submit(context.Background(), RenderIntent{Kind: TransactionFrame, Bytes: []byte("old-live")})

	next := registry.Bind("ses-e1-rebind", port)
	if next.BindingGeneration <= old.BindingGeneration {
		t.Fatalf("rebind must increase generation: old=%d next=%d", old.BindingGeneration, next.BindingGeneration)
	}
	// 新 facade 正常可用。
	ok := next.Port.Submit(context.Background(), RenderIntent{Kind: TransactionFrame, Bytes: []byte("next-live")})
	if ok.Admission.Decision != AdmissionAccepted {
		t.Fatalf("new facade submit should be accepted, got %+v", ok.Admission)
	}

	// 旧 facade 已被 fence：pre-admission rejected，零触达底层 port。
	late := old.Port.Submit(context.Background(), RenderIntent{Kind: TransactionFrame, Bytes: []byte("stale-generation")})
	assertPreAdmissionRejected(t, late)
}

// TestE1UnbindFenceAllRejectsEveryFacade：UnbindFenceAll（shutdown）后所有
// 会话的旧 facade 全部被 fence。
func TestE1UnbindFenceAllRejectsEveryFacade(t *testing.T) {
	registry := NewSessionBindingRegistry()
	sessions := []string{"ses-a", "ses-b", "ses-c"}
	refs := make([]SessionBindingRef, 0, len(sessions))
	ports := make([]*recordingSubmitPort, 0, len(sessions))
	for _, sid := range sessions {
		p := &recordingSubmitPort{}
		refs = append(refs, registry.Bind(sid, p))
		ports = append(ports, p)
	}

	registry.UnbindFenceAll()

	for i, ref := range refs {
		r := ref.Port.Submit(context.Background(), RenderIntent{Kind: TransactionFrame, Bytes: []byte("late")})
		assertPreAdmissionRejected(t, r)
		if ports[i].Calls() != 0 {
			t.Fatalf("session %s: fenced submit reached underlying port (calls=%d)", sessions[i], ports[i].Calls())
		}
	}
}

// TestE1UnbindOneSessionKeepsOthersActive：unbind 单个 session 不影响其他
// session 的绑定；隔离性保持。
func TestE1UnbindOneSessionKeepsOthersActive(t *testing.T) {
	registry := NewSessionBindingRegistry()
	portA := &recordingSubmitPort{}
	portB := &recordingSubmitPort{}

	refA := registry.Bind("ses-a", portA)
	refB := registry.Bind("ses-b", portB)

	registry.Unbind("ses-a")

	// A 被 fence。
	assertPreAdmissionRejected(t, refA.Port.Submit(context.Background(), RenderIntent{Kind: TransactionFrame, Bytes: []byte("a-late")}))
	if portA.Calls() != 0 {
		t.Fatalf("session A fenced submit must not reach port, calls=%d", portA.Calls())
	}
	// B 仍可用。
	ok := refB.Port.Submit(context.Background(), RenderIntent{Kind: TransactionFrame, Bytes: []byte("b-live")})
	if ok.Admission.Decision != AdmissionAccepted {
		t.Fatalf("session B should stay active, got %+v", ok.Admission)
	}
	if portB.Calls() != 1 {
		t.Fatalf("session B expected 1 call, got %d", portB.Calls())
	}
}

// TestE1LateBytesNeverReachNextSessionGateway：端到端——旧 session 的迟到
// goroutine 提交被 fence 拒绝，其字节既不进入旧 session 的 sink，也不进入
// 新 session 的 gateway sink（模拟 process compatibility writer）。
func TestE1LateBytesNeverReachNextSessionGateway(t *testing.T) {
	registry := NewSessionBindingRegistry()

	// Session A：真实 gateway + MemorySink。
	sinkA := memoryPrimary(t)
	gwA := mustGateway(t, sinkA)
	refA := registry.Bind("ses-e2e-a", gwA)

	okA := refA.Port.Submit(context.Background(), RenderIntent{
		IntentID: randomID("int"),
		Kind:     TransactionFrame,
		Source:   "e1-e2e",
		Bytes:    []byte("A-LIVE"),
	})
	if okA.Admission.Decision != AdmissionAccepted || okA.Primary == nil {
		t.Fatalf("session A live submit failed: %+v", okA)
	}
	drainSync(t, gwA)

	// Session A close/unbind → 旧 facade fence。
	registry.Unbind("ses-e2e-a")

	// 迟到的旧会话 goroutine（真实并发）用旧 facade 写 "A-STALE"。
	lateDone := make(chan OutputReceipt, 1)
	go func() {
		lateDone <- refA.Port.Submit(context.Background(), RenderIntent{
			IntentID: randomID("int"),
			Kind:     TransactionFrame,
			Source:   "e1-e2e",
			Bytes:    []byte("A-STALE-LATE"),
		})
	}()
	var late OutputReceipt
	select {
	case late = <-lateDone:
	case <-time.After(3 * time.Second):
		t.Fatal("late goroutine submit did not return")
	}
	assertPreAdmissionRejected(t, late)

	// Session B：新 gateway + 新 sink（模拟下一会话 + process writer）。
	sinkB := memoryPrimary(t)
	gwB := mustGateway(t, sinkB)
	refB := registry.Bind("ses-e2e-b", gwB)
	okB := refB.Port.Submit(context.Background(), RenderIntent{
		IntentID: randomID("int"),
		Kind:     TransactionFrame,
		Source:   "e1-e2e",
		Bytes:    []byte("B-LIVE"),
	})
	if okB.Admission.Decision != AdmissionAccepted || okB.Primary == nil {
		t.Fatalf("session B live submit failed: %+v", okB)
	}
	drainSync(t, gwB)

	// 断言：A-STALE 既不进入 A 的 sink，也不进入 B 的 sink。
	for _, batch := range sinkA.SnapshotBatches() {
		if bytes.Contains(batch.Bytes, []byte("A-STALE")) {
			t.Fatalf("stale bytes leaked into old session sink A: %q", batch.Bytes)
		}
	}
	for _, batch := range sinkB.SnapshotBatches() {
		if bytes.Contains(batch.Bytes, []byte("A-STALE")) {
			t.Fatalf("stale bytes leaked into next session sink B: %q", batch.Bytes)
		}
	}
	// 顺带确认各自 live 字节确实到达对应 sink（证明测试本身有效）。
	if !bytes.Contains(joinBatchBytes(t, sinkA), []byte("A-LIVE")) {
		t.Fatalf("session A live bytes missing from sink A")
	}
	if !bytes.Contains(joinBatchBytes(t, sinkB), []byte("B-LIVE")) {
		t.Fatalf("session B live bytes missing from sink B")
	}
}

// TestE1RebindWaitsForInFlightOldGenerationIsPreAdmission：既有线性化测试的
// 补强——rebind 完成后旧 facade 的迟到提交不仅被拒，还满足
// Primary=nil、Sequence=0、TargetInvoked=false 的 pre-admission 契约。
func TestE1RebindLateSubmitIsPreAdmissionRejected(t *testing.T) {
	registry := NewSessionBindingRegistry()
	port := &blockingSubmitPort{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	old := registry.Bind("ses-e1-linearized", port)

	submitDone := make(chan struct{})
	go func() {
		defer close(submitDone)
		old.Port.Submit(context.Background(), RenderIntent{Kind: TransactionFrame, Bytes: []byte("old")})
	}()
	select {
	case <-port.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("old submission did not enter underlying port")
	}

	rebindDone := make(chan struct{})
	go func() {
		registry.Bind("ses-e1-linearized", port)
		close(rebindDone)
	}()
	select {
	case <-rebindDone:
		t.Fatal("rebind returned while an old-generation submission was still active")
	case <-time.After(25 * time.Millisecond):
	}

	close(port.release)
	select {
	case <-submitDone:
	case <-time.After(2 * time.Second):
		t.Fatal("old submission did not finish")
	}
	select {
	case <-rebindDone:
	case <-time.After(2 * time.Second):
		t.Fatal("rebind did not finish after old submission returned")
	}

	late := old.Port.Submit(context.Background(), RenderIntent{Kind: TransactionFrame, Bytes: []byte("late")})
	assertPreAdmissionRejected(t, late)
}

// joinBatchBytes 拼接 MemorySink 中所有 batch 的字节。
func joinBatchBytes(t *testing.T, sink *MemorySink) []byte {
	t.Helper()
	var out []byte
	for _, b := range sink.SnapshotBatches() {
		out = append(out, b.Bytes...)
	}
	return out
}
