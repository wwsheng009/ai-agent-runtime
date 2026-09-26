package commands

// §4.6 CLI/TUI「常驻模式标识」的边界测试：与 Web 横幅（session-mode-banner-shared.ts）
// 的 tone 映射、读法优先级、未知枚举回落逐条对齐。

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

func TestChatSurfacePlanModeStatusSegmentModes(t *testing.T) {
	// default：保持页脚既有词表与文本（向后兼容）。
	off := chatSurfacePlanModeStatusSegment(&ChatSession{})
	if off.full != "Plan OFF" || off.compact != "Plan OFF" {
		t.Fatalf("default mode should keep Plan OFF, got %+v", off)
	}

	// bypass_permissions：常驻可见 + 告警角色（对应 Web 的 danger tone）。
	bypass := chatSurfacePlanModeStatusSegment(&ChatSession{PermissionMode: runtimepolicy.ModeBypassPermissions})
	if bypass.full != "Full Access" || bypass.compact != "Full Access" {
		t.Fatalf("bypass should be persistently visible as Full Access, got %+v", bypass)
	}
	if bypass.role != style.RoleWarning {
		t.Fatalf("bypass segment must carry the warning role, got %q", bypass.role)
	}

	// accept_edits：常驻可见但中性角色（Web 同为 neutral）。
	edits := chatSurfacePlanModeStatusSegment(&ChatSession{PermissionMode: runtimepolicy.ModeAcceptEdits})
	if edits.full != "Accept edits" || edits.role != "" {
		t.Fatalf("accept_edits should stay neutral in the footer, got %+v", edits)
	}

	// 未知枚举：不写死文案，回落后端原文（Web 同样是 raw fallback）。
	raw := runtimepolicy.Mode("custom_mode")
	odd := chatSurfacePlanModeStatusSegment(&ChatSession{PermissionMode: raw})
	if want := formatChatStatusModeValue(string(raw)); odd.full != want || odd.full == "" {
		t.Fatalf("unknown mode should fall back to %q, got %+v", want, odd)
	}
}

func TestChatSurfacePlanModeStatusSegmentPlanReadings(t *testing.T) {
	withPlanArtifactStore(t)
	workspace := t.TempDir()
	planPath := "plan.md"
	session := newPlanCommandSession(runtimepolicy.ModeDefault)
	session.RuntimeSession.Metadata.Context[chatPlanWorkspacePathKey] = workspace
	if err := enterChatPlanMode(session, planPath); err != nil {
		t.Fatalf("enter: %v", err)
	}

	// 计划尚未写就：只有 Plan ON（Web 该档的文案是整句 hint，页脚不占位）。
	if seg := chatSurfacePlanModeStatusSegment(session); seg.full != "Plan ON" {
		t.Fatalf("empty plan should render a bare Plan ON, got %+v", seg)
	}

	// 正文可用：已就绪（与 Web 的 ready 同档）。
	absolute := filepath.Join(workspace, filepath.FromSlash(planPath))
	if err := os.WriteFile(absolute, []byte("# Plan\n\nreviewable body\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	if seg := chatSurfacePlanModeStatusSegment(session); seg.full != "Plan ON · 已就绪" {
		t.Fatalf("available plan should render 已就绪, got %+v", seg)
	}

	// 模型已请求裁决：优先于「已就绪」（截断后的紧凑态保留两个语义）。
	state := loadChatPlanMode(session)
	state.PendingExitRequest = true
	saveChatPlanMode(session, state)
	seg := chatSurfacePlanModeStatusSegment(session)
	if seg.full != "Plan ON · 待裁决" || seg.compact != "Plan·待裁决" {
		t.Fatalf("pending verdict must win over ready, got %+v", seg)
	}
}
