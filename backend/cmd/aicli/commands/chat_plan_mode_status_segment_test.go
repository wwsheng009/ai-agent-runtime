package commands

// §4.6 CLI/TUI「常驻模式标识」的边界测试：与 Web 横幅（session-mode-banner-shared.ts）
// 的 tone 映射、读法优先级、未知枚举回落逐条对齐。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

func TestChatSurfacePlanModeStatusSegmentModes(t *testing.T) {
	// default：保持页脚既有词表与文本（向后兼容）。
	off := chatSurfacePlanModeStatusSegment(&ChatSession{})
	if off.full != "Plan OFF" || off.compact != "Plan OFF" {
		t.Fatalf("default mode should keep Plan OFF, got %+v", off)
	}
	if strings.Contains(off.full, "/plan review") {
		t.Fatalf("plan OFF must not advertise plan-mode read hints, got %+v", off)
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
	planPath := "docs/review-plan.md"
	session := newPlanCommandSession(runtimepolicy.ModeDefault)
	session.RuntimeSession.Metadata.Context[chatPlanWorkspacePathKey] = workspace
	if err := enterChatPlanMode(session, planPath); err != nil {
		t.Fatalf("enter: %v", err)
	}

	// 计划尚未写就：full 仍给全三要素（状态 + 路径 + 读法），compact 保状态与路径。
	seg := chatSurfacePlanModeStatusSegment(session)
	for _, want := range []string{"Plan ON", planPath, "/plan review 查看正文"} {
		if !strings.Contains(seg.full, want) {
			t.Fatalf("empty-plan footer must contain %q, got %+v", want, seg)
		}
	}
	if !strings.Contains(seg.compact, "Plan ON") || !strings.Contains(seg.compact, planPath) ||
		!strings.HasSuffix(seg.compact, " · /plan") || strings.Contains(seg.compact, "/plan review") {
		t.Fatalf("compact footer must keep state, path and the short read hint, got %+v", seg)
	}

	// 正文可用：已就绪（与 Web 的 ready 同档），三要素不变。
	absolute := filepath.Join(workspace, filepath.FromSlash(planPath))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatalf("mkdir plan dir: %v", err)
	}
	if err := os.WriteFile(absolute, []byte("# Plan\n\nreviewable body\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}
	seg = chatSurfacePlanModeStatusSegment(session)
	for _, want := range []string{"Plan ON", "已就绪", planPath, "/plan review 查看正文"} {
		if !strings.Contains(seg.full, want) {
			t.Fatalf("ready footer must contain %q, got %+v", want, seg)
		}
	}
	if !strings.Contains(seg.compact, "Plan·就绪") || !strings.Contains(seg.compact, planPath) ||
		!strings.HasSuffix(seg.compact, " · /plan") {
		t.Fatalf("compact ready footer must keep state, path and the short read hint, got %+v", seg)
	}

	// 模型已请求裁决：优先于「已就绪」（截断后的紧凑态同样保留状态与路径）。
	state := loadChatPlanMode(session)
	state.PendingExitRequest = true
	saveChatPlanMode(session, state)
	seg = chatSurfacePlanModeStatusSegment(session)
	for _, want := range []string{"Plan ON", "待裁决", planPath, "/plan review 查看正文"} {
		if !strings.Contains(seg.full, want) {
			t.Fatalf("pending footer must contain %q, got %+v", want, seg)
		}
	}
	if !strings.Contains(seg.compact, "Plan·待裁决") || !strings.Contains(seg.compact, planPath) ||
		!strings.HasSuffix(seg.compact, " · /plan") {
		t.Fatalf("compact pending footer must keep state, path and the short read hint, got %+v", seg)
	}
}

// 路径缺失回落默认 plan 文件名（与 /plan status、runtimeapi 的 plan 快照同口径）；
// 超长路径按既有 compactStatusValue 惯例截断，不挤压其它段。
func TestChatSurfacePlanModeStatusSegmentPlanPathDegradesGracefully(t *testing.T) {
	withPlanArtifactStore(t)

	// 裸 permission-mode=plan（未记录 plan 状态）：显示默认 plan 文件名 + 读法。
	bare := newPlanCommandSession(runtimepolicy.ModePlan)
	seg := chatSurfacePlanModeStatusSegment(bare)
	for _, want := range []string{planmode.DefaultPlanPath, "/plan review 查看正文"} {
		if !strings.Contains(seg.full, want) {
			t.Fatalf("missing plan path must fall back to %q and keep the read hint, got %+v", want, seg)
		}
	}
	if !strings.Contains(seg.compact, planmode.DefaultPlanPath) || !strings.HasSuffix(seg.compact, " · /plan") {
		t.Fatalf("compact footer must keep the fallback path and read hint, got %+v", seg)
	}

	// 超长路径：截断到既有上限并带省略号；full 仍保留读法。
	workspace := t.TempDir()
	session := newPlanCommandSession(runtimepolicy.ModeDefault)
	session.RuntimeSession.Metadata.Context[chatPlanWorkspacePathKey] = workspace
	longPath := "docs/" + strings.Repeat("very-long-plan-name-", 4) + ".md"
	if err := enterChatPlanMode(session, longPath); err != nil {
		t.Fatalf("enter: %v", err)
	}
	seg = chatSurfacePlanModeStatusSegment(session)
	pathText := strings.TrimSuffix(strings.TrimPrefix(seg.compact, "Plan ON · "), " · /plan")
	if pathText == longPath || !strings.HasSuffix(pathText, "...") {
		t.Fatalf("long plan path must be truncated with an ellipsis, got %q", pathText)
	}
	if ui.DisplayWidth(pathText) > chatSurfacePlanStatusPathMaxWidth {
		t.Fatalf("truncated plan path must fit %d cells, got %q (%d)", chatSurfacePlanStatusPathMaxWidth, pathText, ui.DisplayWidth(pathText))
	}
	if !strings.Contains(seg.full, "/plan review 查看正文") {
		t.Fatalf("long-path footer must keep the read hint, got %+v", seg)
	}
}

// 上屏形态走既有 compact/full 规则：宽终端 full 一次给全三要素；关闭档仍只有 Plan OFF。
func TestChatSurfacePlanStatusLineCarriesPlanContextWhenActive(t *testing.T) {
	withPlanArtifactStore(t)
	workspace := t.TempDir()
	planPath := "docs/status-plan.md"
	session := newPlanCommandSession(runtimepolicy.ModeDefault)
	session.RuntimeSession.Metadata.Context[chatPlanWorkspacePathKey] = workspace
	if err := enterChatPlanMode(session, planPath); err != nil {
		t.Fatalf("enter: %v", err)
	}
	absolute := filepath.Join(workspace, filepath.FromSlash(planPath))
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		t.Fatalf("mkdir plan dir: %v", err)
	}
	if err := os.WriteFile(absolute, []byte("# Plan\n\nbody\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	// 80 列：full 形态放不下（既有规则只在整行可容纳时升级），compact 仍保三要素。
	line := buildChatSurfaceStatusLineForWidth(session, "Ready", 80)
	for _, want := range []string{"Plan·就绪", planPath, " · /plan"} {
		if !strings.Contains(line, want) {
			t.Fatalf("active plan status line must contain %q, got %q", want, line)
		}
	}

	// 200 列：full 形态给出状态词全称与完整读法。
	wide := buildChatSurfaceStatusLineForWidth(session, "Ready", 200)
	for _, want := range []string{"Plan ON", "已就绪", planPath, "/plan review 查看正文"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("wide plan status line must contain %q, got %q", want, wide)
		}
	}

	off := buildChatSurfaceStatusLineForWidth(&ChatSession{Model: "gpt-5.4-code"}, "Ready", 120)
	if !strings.Contains(off, "Plan OFF") || strings.Contains(off, "/plan review") {
		t.Fatalf("inactive plan status line must keep the exact Plan OFF wording, got %q", off)
	}
}
