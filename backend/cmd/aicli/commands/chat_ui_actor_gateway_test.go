package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	outputpkg "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/output"
)

// ============================================================================
// Phase 6：production factory（gateway-backed）集成测试
// ============================================================================

// newGatewayCoordinator 构造带 gateway factory 的 coordinator fixture。
func newGatewayCoordinator(t *testing.T) (*chatInteractionCoordinator, *ChatSession, *outputpkg.RenderOutputGateway) {
	t.Helper()
	var terminal bytes.Buffer
	session := &ChatSession{}
	coordinator := newChatInteractionCoordinator(session)
	t.Cleanup(coordinator.Shutdown)
	session.Interaction = coordinator
	coordinator.SetWriter(&terminal)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(72, 20)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	gw := coordinator.EnableUnifiedRendererGateway()
	if gw == nil {
		t.Fatal("gateway factory did not attach")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = gw.Close(ctx)
	})
	return coordinator, session, gw
}

// TestEnableUnifiedRendererGatewayAttachesGatewayBackedSession：factory 安装
// gateway-backed session——TerminalSession 以 gateway 为 output，/debug 节可
// 观察；gateway 自理提交（直接 submit 验证 receipt）。
func TestEnableUnifiedRendererGatewayAttachesGatewayBackedSession(t *testing.T) {
	_, session, gw := newGatewayCoordinator(t)
	// TerminalSession 是 gateway-backed。
	if session.TerminalSession == nil {
		t.Fatal("gateway-backed session not installed on ChatSession")
	}
	snap := session.TerminalSession.RenderOutputSnapshot()
	if snap == nil || snap.State != outputpkg.GatewayOpen {
		t.Fatalf("render output snapshot: %+v", snap)
	}
	// /debug 文档包含 Render Output 节。
	doc := buildDebugRenderOutputDocument(session)
	text := renderDocumentText(doc)
	if !strings.Contains(text, "Render Output:") {
		t.Fatalf("debug doc missing render output section:\n%s", text)
	}
	// gateway 自理（直接提交的 receipt 落 journal）。
	r := gw.Submit(context.Background(), outputpkg.RenderIntent{
		IntentID: "factory-probe",
		Kind:     outputpkg.TransactionFrame,
		Bytes:    []byte("probe"),
	})
	if r.Admission.Decision != outputpkg.AdmissionAccepted || r.Primary == nil {
		t.Fatalf("gateway submit: %+v", r)
	}
	snap2 := gw.Snapshot()
	if snap2.AdmissionAccepted < 1 || snap2.PrimaryCommitted < 1 {
		t.Fatalf("gateway after direct submit: %+v", snap2)
	}
}

// TestEnableUnifiedRendererGatewayShutdownClosesGateway：Shutdown 收口关闭
// gateway（bounded close 后 state=closed）并释放引用。
func TestEnableUnifiedRendererGatewayShutdownClosesGateway(t *testing.T) {
	var terminal bytes.Buffer
	session := &ChatSession{}
	coordinator := newChatInteractionCoordinator(session)
	session.Interaction = coordinator
	coordinator.SetWriter(&terminal)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(72, 20)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)

	gw := coordinator.EnableUnifiedRendererGateway()
	if gw == nil {
		t.Fatal("gateway factory did not attach")
	}
	coordinator.Shutdown()
	if coordinator.renderGateway != nil {
		t.Fatal("gateway not released on shutdown")
	}
	snap := gw.Snapshot()
	if snap.State != outputpkg.GatewayClosed {
		t.Fatalf("gateway state after shutdown: %s, want closed", snap.State)
	}
}

// TestEnableUnifiedRendererGatewayNoSecondPhysicalWriter：gateway 安装后
// surface 的 legacy writer 保持 fenced（不产生第二 physical writer）。
func TestEnableUnifiedRendererGatewayNoSecondPhysicalWriter(t *testing.T) {
	coordinator, _, _ := newGatewayCoordinator(t)
	presenter := coordinator.primaryPresenter
	if presenter == nil {
		t.Fatal("presenter not attached")
	}
	// surface 物理写被禁用；gateway 是唯一 physical 提交者。
	if coordinator.surface != nil && coordinator.surface.PhysicalWritesEnabled() {
		t.Fatal("surface physical writes must stay fenced after gateway attach")
	}
}

// TestEnableUnifiedRendererGatewayMirrorsToRenderOutputFile：--render-output-file
// 装配时 gateway 挂 FileSink committed-only mirror——console primary 渲染不变，
// 提交的 terminal wire 字节镜像落盘；文件随 gateway Close 关闭并同步。
func TestEnableUnifiedRendererGatewayMirrorsToRenderOutputFile(t *testing.T) {
	var terminal bytes.Buffer
	session := &ChatSession{}
	coordinator := newChatInteractionCoordinator(session)
	session.Interaction = coordinator
	coordinator.SetWriter(&terminal)
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(72, 20)
	surface.SetPhysicalWritesEnabled(false)
	coordinator.SetSurface(surface)
	// 关键：指定镜像落盘路径（生产经 --render-output-file 注入）。
	coordinator.renderOutputFile = filepath.Join(t.TempDir(), "mirror.ans")

	gw := coordinator.EnableUnifiedRendererGateway()
	if gw == nil {
		t.Fatal("gateway factory did not attach")
	}
	// mirror 已登记（snapshot 可观测）。
	snap := gw.Snapshot()
	if len(snap.Mirrors) != 1 || snap.Mirrors[0].Sink.Descriptor.SinkID != "file-interactive" {
		t.Fatalf("expected file-interactive mirror, got: %+v", snap.Mirrors)
	}

	// 提交一个 committed batch；mirror committed-only 应镜像字节。
	r := gw.Submit(context.Background(), outputpkg.RenderIntent{
		IntentID: "mirror-probe",
		Kind:     outputpkg.TransactionFrame,
		Bytes:    []byte("hello mirror\n"),
	})
	if r.Admission.Decision != outputpkg.AdmissionAccepted || r.Primary == nil {
		t.Fatalf("gateway submit: %+v", r)
	}
	// Close 触发 mirror 文件 sync/close（SinkOwned）。
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := gw.Close(ctx); err != nil {
		t.Fatalf("gateway close: %v", err)
	}

	data, err := os.ReadFile(coordinator.renderOutputFile)
	if err != nil {
		t.Fatalf("read mirror file: %v", err)
	}
	if !strings.Contains(string(data), "hello mirror") {
		t.Fatalf("mirror file missing committed bytes, got: %q", string(data))
	}
}
