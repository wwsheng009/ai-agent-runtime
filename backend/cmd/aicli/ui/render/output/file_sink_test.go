package output

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ============================================================================
// FileSink 单元测试
// ============================================================================

// TestFileSinkWritesToFile 基本写入：多次 Submit 后文件内容等于提交字节拼接。
func TestFileSinkWritesToFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test-output.ans")
	sink, err := NewFileSink(
		TargetDescriptor{SinkID: "file-test", Class: TargetClassCapture, ProjectionTargetID: "pt-file"},
		path,
		FileSinkOptions{SyncOnClose: true},
	)
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	defer sink.Close(context.Background())

	batch1 := []byte("hello\n")
	batch2 := []byte("world\n")
	batch3 := []byte("\x1b[32mgreen\x1b[0m\n")

	r1 := sink.Submit(context.Background(), makeBatch(1, batch1))
	r2 := sink.Submit(context.Background(), makeBatch(2, batch2))
	r3 := sink.Submit(context.Background(), makeBatch(3, batch3))

	assertCommitted(t, "batch1", r1, batch1)
	assertCommitted(t, "batch2", r2, batch2)
	assertCommitted(t, "batch3", r3, batch3)

	// Close 触发 sync 并关闭文件
	if err := sink.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 读文件验证
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := string(batch1) + string(batch2) + string(batch3)
	if string(data) != want {
		t.Fatalf("file content:\ngot:  %q\nwant: %q", string(data), want)
	}
}

// TestFileSinkClosedRejects 关闭后提交返回 Rejected。
func TestFileSinkClosedRejects(t *testing.T) {
	path := filepath.Join(t.TempDir(), "closed-test.ans")
	sink, err := NewFileSink(
		TargetDescriptor{SinkID: "file-reject", Class: TargetClassCapture, ProjectionTargetID: "pt-reject"},
		path,
		FileSinkOptions{},
	)
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	if err := sink.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r := sink.Submit(context.Background(), makeBatch(1, []byte("must-reject")))
	if r.Status != DeliveryRejected {
		t.Fatalf("expected rejected after close, got %s", r.Status)
	}
}

// TestFileSinkFlushError 当底层 Sync 失败时 Flush 返回错误。
func TestFileSinkFlushError(t *testing.T) {
	// 用一个 sync 必失败的 writer
	w := &failSyncWriter{}
	fs := NewFileSinkWriter(
		TargetDescriptor{SinkID: "file-flush-err", Class: TargetClassCapture, ProjectionTargetID: "pt-flush"},
		w,
		FileSinkOptions{SyncOnClose: false},
	)
	defer fs.Close(context.Background())

	fs.Submit(context.Background(), makeBatch(1, []byte("ok")))
	err := fs.Flush(context.Background())
	if err == nil {
		t.Fatal("expected flush error from failSyncWriter")
	}
}

// TestFileSinkSyncEveryWriteFailed 当 SyncEveryWrite 且 sync 失败时降级
// 为 unknown partial。
func TestFileSinkSyncEveryWriteFailed(t *testing.T) {
	w := &failSyncWriter{}
	fs := NewFileSinkWriter(
		TargetDescriptor{SinkID: "file-sync-fail", Class: TargetClassCapture, ProjectionTargetID: "pt-sync-fail"},
		w,
		FileSinkOptions{SyncEveryWrite: true, SyncOnClose: false},
	)
	defer fs.Close(context.Background())

	r := fs.Submit(context.Background(), makeBatch(1, []byte("data")))
	if r.Status != DeliveryUnknownPartial {
		t.Fatalf("expected unknown_partial after sync failure, got %s", r.Status)
	}
}

// TestFileSinkWriterCommitted 用 writer 构造的 sink 正常提交。
func TestFileSinkWriterCommitted(t *testing.T) {
	var buf bytes.Buffer
	fs := NewFileSinkWriter(
		TargetDescriptor{SinkID: "file-writer-test", Class: TargetClassCapture, ProjectionTargetID: "pt-writer"},
		&buf,
		FileSinkOptions{},
	)
	defer fs.Close(context.Background())

	r := fs.Submit(context.Background(), makeBatch(1, []byte("test")))
	assertCommitted(t, "writer submit", r, []byte("test"))
	if buf.String() != "test" {
		t.Fatalf("buffer content: got %q, want %q", buf.String(), "test")
	}
}

// TestFileSinkSyncOnCloseDefault 默认 SyncOnClose=true：Close 时对底层
// Sync 能力恰好调用一次。
func TestFileSinkSyncOnCloseDefault(t *testing.T) {
	w := &countingSyncWriter{}
	fs := NewFileSinkWriter(
		TargetDescriptor{SinkID: "file-sync-close", Class: TargetClassCapture, ProjectionTargetID: "pt-sync-close"},
		w,
		FileSinkOptions{},
	)
	if err := fs.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := w.syncs.Load(); got != 1 {
		t.Fatalf("expected 1 sync on close, got %d", got)
	}
	if !w.closed {
		t.Fatal("underlying writer was not closed")
	}
}

// TestFileSinkSyncEveryWrite 每次提交后 Sync 被调用。
func TestFileSinkSyncEveryWrite(t *testing.T) {
	w := &countingSyncWriter{}
	fs := NewFileSinkWriter(
		TargetDescriptor{SinkID: "file-sync-each", Class: TargetClassCapture, ProjectionTargetID: "pt-sync-each"},
		w,
		FileSinkOptions{SyncEveryWrite: true, SyncOnClose: false},
	)
	defer fs.Close(context.Background())

	fs.Submit(context.Background(), makeBatch(1, []byte("a")))
	fs.Submit(context.Background(), makeBatch(2, []byte("b")))
	if got := w.syncs.Load(); got != 2 {
		t.Fatalf("expected 2 syncs (one per write), got %d", got)
	}
}

// ============================================================================
// FileSink + Gateway 集成测试
// ============================================================================

// TestFileSinkAsMirrorWithGateway 验证 FileSink 作为 mirror，gateway 提交
// 的字节并行写入文件。
func TestFileSinkAsMirrorWithGateway(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mirror-output.ans")
	fileSink, err := NewFileSink(
		TargetDescriptor{SinkID: "file-mirror", Class: TargetClassCapture, ProjectionTargetID: "pt-mirror"},
		path,
		FileSinkOptions{SyncOnClose: true},
	)
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	t.Cleanup(func() { fileSink.Close(context.Background()) })

	f := NewRenderTestFixture(t, WithMirror(RenderMirror{
		Sink:      fileSink,
		Policy:    MirrorBestEffort,
		ApplyMode: MirrorApplyBytes,
		Ownership: SinkBorrowed,
		Timeout:   2 * time.Second,
	}))

	payload1 := []byte("frame one\n")
	payload2 := []byte("frame two\n")
	payload3 := []byte("\x1b[31mred\x1b[0m\n")

	f.SubmitIntent(t, TransactionFrame, payload1)
	f.SubmitIntent(t, TransactionFrame, payload2)
	f.SubmitIntent(t, TransactionFrame, payload3)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := f.Gateway.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if err := fileSink.Close(context.Background()); err != nil {
		t.Fatalf("FileSink.Close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := string(payload1) + string(payload2) + string(payload3)
	if string(data) != want {
		t.Fatalf("mirror file content:\ngot:  %q\nwant: %q", string(data), want)
	}
}

// TestFileSinkAsPrimaryWithGateway 验证 FileSink 作为 primary，gateway
// 提交后 receipt 正常、文件内容正确。
func TestFileSinkAsPrimaryWithGateway(t *testing.T) {
	path := filepath.Join(t.TempDir(), "primary-output.ans")
	primary, err := NewFileSink(
		TargetDescriptor{SinkID: "file-primary", Class: TargetClassPhysical, ProjectionTargetID: "pt-primary"},
		path,
		FileSinkOptions{SyncOnClose: true, SyncEveryWrite: false},
	)
	if err != nil {
		t.Fatalf("NewFileSink: %v", err)
	}
	t.Cleanup(func() { primary.Close(context.Background()) })

	desc := primary.Descriptor()
	gw, err := NewRenderOutputGateway("primary-file-session-"+randomID("s"), gatewayOptions(),
		RenderRouteConfig{
			Primary:            primary,
			PrimaryOwnership:   SinkOwned,
			ProjectionTargetID: desc.ProjectionTargetID,
		})
	if err != nil {
		t.Fatalf("NewRenderOutputGateway: %v", err)
	}
	gw.Run()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = gw.Close(ctx)
	})

	payload := []byte("primary frame\n")
	receipt := gw.Submit(context.Background(), RenderIntent{
		IntentID: randomID("int"),
		Kind:     TransactionFrame,
		Source:   "test",
		Cause:    "test",
		Bytes:    payload,
	})

	if receipt.Admission.Decision != AdmissionAccepted {
		t.Fatalf("admission not accepted: %+v", receipt.Admission)
	}
	if receipt.Primary == nil || receipt.Primary.Status != DeliveryCommitted {
		t.Fatalf("primary receipt not committed: %+v", receipt.Primary)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := gw.WaitIdle(ctx); err != nil {
		t.Fatalf("WaitIdle: %v", err)
	}
	if err := primary.Close(context.Background()); err != nil {
		t.Fatalf("primary.Close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != string(payload) {
		t.Fatalf("primary file content:\ngot:  %q\nwant: %q", string(data), string(payload))
	}
}

// TestFileSinkPlainTextViaVirtual 演示纯文本导出路径：VirtualTerminalSink
// mirror 的 Projection().Rows 生成可读文本，FileSink wire mirror 负责
// 原始字节。两条路径互不干扰。
func TestFileSinkPlainTextViaVirtual(t *testing.T) {
	wirePath := filepath.Join(t.TempDir(), "wire-output.ans")
	textPath := filepath.Join(t.TempDir(), "plain-text.txt")

	wireSink, err := NewFileSink(
		TargetDescriptor{SinkID: "file-wire", Class: TargetClassCapture, ProjectionTargetID: "pt-wire"},
		wirePath,
		FileSinkOptions{SyncOnClose: true},
	)
	if err != nil {
		t.Fatalf("NewFileSink wire: %v", err)
	}
	t.Cleanup(func() { wireSink.Close(context.Background()) })

	f := NewRenderTestFixture(t,
		WithVirtualTerminal("pt-virtual"),
		WithMirror(RenderMirror{
			Sink:      wireSink,
			Policy:    MirrorBestEffort,
			ApplyMode: MirrorApplyBytes,
			Ownership: SinkBorrowed,
			Timeout:   2 * time.Second,
		}),
	)

	// 提交带 ANSI 的字节；fakeEmulator 按 \n 拆分 Rows，不解析 ANSI 转义序列。
	// virtual mirror 需要合法 geometry 才能应用字节（ApplyContext 校验
	// Width/Height >= 1），所以这里直接提交带 Terminal 上下文的 intent。
	payload1 := []byte("line one\n")
	payload2 := []byte("line two\n")
	payload3 := []byte("\x1b[32mcolored\x1b[0m\n")

	submit := func(payload []byte) {
		t.Helper()
		f.Gateway.Submit(context.Background(), RenderIntent{
			IntentID: randomID("int"),
			Kind:     TransactionFrame,
			Source:   "file-sink-test",
			Cause:    "test",
			Bytes:    payload,
			Terminal: RenderTerminalContext{
				Geometry: TerminalGeometry{Width: 80, Height: 24},
				Profile:  TerminalProfileRef{ID: "ansi", Version: 1},
			},
		})
	}
	submit(payload1)
	submit(payload2)
	submit(payload3)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := f.Gateway.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}

	// --- 路径 A（wire 文件）：FileSink 已收到原始字节 ---
	if err := wireSink.Close(context.Background()); err != nil {
		t.Fatalf("wireSink.Close: %v", err)
	}
	wireData, err := os.ReadFile(wirePath)
	if err != nil {
		t.Fatalf("ReadFile wire: %v", err)
	}
	wantWire := string(payload1) + string(payload2) + string(payload3)
	if string(wireData) != wantWire {
		t.Fatalf("wire file:\ngot:  %q\nwant: %q", string(wireData), wantWire)
	}

	// --- 路径 B（纯文本文件）：从 VirtualTerminalSink 的 Projection 导出
	// primary 已 fully committed，mirror 保持 MirrorApplyBytes 且带合法
	// geometry → validity 应为 valid。
	snap := f.Virtual.Projection()
	if snap.Validity != ProjectionValid {
		t.Fatalf("virtual projection should be valid, got %s: %+v", snap.Validity, snap)
	}
	// 组装纯文本
	plainText := strings.Join(snap.Rows, "\n")
	if err := os.WriteFile(textPath, []byte(plainText), 0o644); err != nil {
		t.Fatalf("WriteFile text: %v", err)
	}

	textData, err := os.ReadFile(textPath)
	if err != nil {
		t.Fatalf("ReadFile text: %v", err)
	}
	// fakeEmulator 的 bytes 是提交按 \n 追加，Snapshot 用
	// strings.Split(string(bytes), "\n") 产生 Rows，末尾空串来自
	// 最后一个 \n。断言前三个 Rows 内容即可。
	gotText := string(textData)
	if !strings.Contains(gotText, "line one") {
		t.Fatalf("plain text missing 'line one': got %q", gotText)
	}
	if !strings.Contains(gotText, "line two") {
		t.Fatalf("plain text missing 'line two': got %q", gotText)
	}
	if !strings.Contains(gotText, "colored") {
		t.Fatalf("plain text missing 'colored': got %q", gotText)
	}
	if len(snap.Rows) < 3 {
		t.Fatalf("expected at least 3 rows, got %d: %v", len(snap.Rows), snap.Rows)
	}
	wantRows := []string{"line one", "line two", "\x1b[32mcolored\x1b[0m"}
	for i, want := range wantRows {
		if snap.Rows[i] != want {
			t.Fatalf("row %d: got %q, want %q (full: %v)", i, snap.Rows[i], want, snap.Rows)
		}
	}
}

// ============================================================================
// helpers
// ============================================================================

func makeBatch(seq uint64, payload []byte) RenderBatch {
	return RenderBatch{
		RenderIntent: RenderIntent{
			IntentID: randomID("int"),
			Kind:     TransactionFrame,
			Source:   "file-sink-test",
			Cause:    "test",
			Bytes:    payload,
		},
		SessionID: "file-sink-test-session",
		Sequence:  seq,
		BatchID:   randomID("batch"),
	}
}

func assertCommitted(t *testing.T, label string, r SinkDeliveryResult, payload []byte) {
	t.Helper()
	if r.Status != DeliveryCommitted {
		t.Fatalf("%s: status=%s, expected committed", label, r.Status)
	}
	if r.Certainty != WriteCertaintyFull {
		t.Fatalf("%s: certainty=%s, expected full", label, r.Certainty)
	}
	if r.AttemptedBytes != len(payload) {
		t.Fatalf("%s: attempted=%d, want %d", label, r.AttemptedBytes, len(payload))
	}
	if r.AcceptedBytes != len(payload) {
		t.Fatalf("%s: accepted=%d, want %d", label, r.AcceptedBytes, len(payload))
	}
}

// failSyncWriter 写入成功但 Sync 始终失败。
type failSyncWriter struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	closed bool
}

func (w *failSyncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *failSyncWriter) Sync() error {
	return errSyncFail
}

func (w *failSyncWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return nil
}

var errSyncFail = &syncFailError{}

type syncFailError struct{}

func (e *syncFailError) Error() string { return "sync failed" }

// countingSyncWriter 记录 Sync 调用次数。
type countingSyncWriter struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	syncs  atomic.Int64
	closed bool
}

func (w *countingSyncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *countingSyncWriter) Sync() error {
	w.syncs.Add(1)
	return nil
}

func (w *countingSyncWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return nil
}