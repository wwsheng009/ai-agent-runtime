package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	baselsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
)

// code_lsp_semantic_status_test.go 覆盖语义通道观测面：状态快照的降级原因
// 可读性与 lsp_servers 的渲染。这组断言的价值在于「配了却不生效」不再静默。

func TestFormatSemanticChannelStatusRendersDegradeReason(t *testing.T) {
	line := formatSemanticChannelStatus(knowledge.SemanticChannelStatus{
		Server:   "gopls",
		State:    "degraded",
		Root:     "/w/backend",
		Reason:   knowledge.SemanticReasonNoLanguage,
		LockPath: "/w/backend/.aicli/knowledge/lsp/gopls-abc.lock",
	})
	for _, want := range []string{
		"semantic channel",
		"gopls",
		"degraded",
		"module_root=/w/backend",
		"reason=" + knowledge.SemanticReasonNoLanguage,
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("line %q 缺少 %q", line, want)
		}
	}
}

func TestFormatSemanticChannelStatusRendersReady(t *testing.T) {
	line := formatSemanticChannelStatus(knowledge.SemanticChannelStatus{
		Server: "gopls", State: "ready", PID: 7, Root: "/w/backend",
	})
	if !strings.Contains(line, "pid=7") {
		t.Fatalf("ready 行缺少 pid: %q", line)
	}
	if !strings.Contains(line, "module_root=/w/backend") {
		t.Fatalf("ready 行缺少模块根: %q", line)
	}
	// 无降级原因时不得输出空的 reason=（纯噪音）。
	if strings.Contains(line, "reason=") {
		t.Fatalf("无降级时不得输出 reason: %q", line)
	}
}

func TestFormatSemanticChannelStatusOmitsEmptyRoot(t *testing.T) {
	line := formatSemanticChannelStatus(knowledge.SemanticChannelStatus{Server: "gopls", State: "idle"})
	if strings.Contains(line, "module_root=") {
		t.Fatalf("无根路径时不得输出空 module_root: %q", line)
	}
}

func TestFormatSemanticChannelStatusMarksSharedInstance(t *testing.T) {
	// 单实例观测：pid 与诊断池那行相同，不写明"共用"就会被读成重复实例。
	line := formatSemanticChannelStatus(knowledge.SemanticChannelStatus{
		Server: "gopls", State: "ready", PID: 1234, Shared: true, Root: "/w/backend",
	})
	if !strings.Contains(line, "pid=1234") {
		t.Fatalf("共享态仍应展示 pid: %q", line)
	}
	if !strings.Contains(line, "shared=diagnostics-pool") {
		t.Fatalf("共享态必须写明共用诊断池进程: %q", line)
	}
	// 自建态不得出现 shared（否则会误导成永远不额外起进程）。
	self := formatSemanticChannelStatus(knowledge.SemanticChannelStatus{Server: "gopls", State: "ready", PID: 7})
	if strings.Contains(self, "shared=") {
		t.Fatalf("自建态不得显示 shared: %q", self)
	}
}

func TestSemanticAdapterForThreadsSharedClientSeam(t *testing.T) {
	t.Cleanup(closeSemanticAdapters)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module demo\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	cfg := knowledge.DefaultConfig().WithWorkspace(root)
	cfg.LSP.Enabled = true
	cfg.LSP.Mode = "self"

	// 借用缝被真正传到了适配器：借不到进程时降级原因必须是"共享不可用"，
	// 而不是悄悄自建一个 gopls（那正是要消除的第二个实例）。
	borrowCalls := 0
adapter := semanticAdapterFor(cfg, root, func(context.Context, string) *baselsp.Client {
		borrowCalls++
		return nil
	})
	if adapter == nil {
		t.Fatal("启用 + Go 模块应构造出适配器")
	}
	_, _ = adapter.Definition(context.Background(), "demo.go", 0, 0)
	if borrowCalls == 0 {
		t.Fatal("借用缝未被调用：单实例选项没有传下去，语义通道会自建进程")
	}
	reporter, ok := adapter.(knowledge.SemanticStatusAdapter)
	if !ok {
		t.Fatal("适配器必须实现观测面")
	}
	status := reporter.SemanticStatus()
	if !strings.Contains(status.Reason, "shared server unavailable") {
		t.Fatalf("降级原因 = %q, want shared server unavailable", status.Reason)
	}

	// 自建模式（借用缝 nil）不共用缓存条目：否则一个模式会悄悄服务另一个模式。
	selfMode := semanticAdapterFor(cfg, root, nil)
	if selfMode == adapter {
		t.Fatal("共享/自建两种模式不得共用同一个缓存适配器")
	}
}
