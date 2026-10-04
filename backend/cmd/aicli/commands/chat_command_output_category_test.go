package commands

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// T11（方案 §5.2(4) / §7.2）：输出类别清单守卫（防漂移）。
//
//  1. 目录中每条命令（含别名归并）必须命中且仅命中一个输出类别；
//  2. screen 档必须在运行时注册表中显式声明类别（未声明即失败并逐条列出）；
//  3. 声明类别与 Mode/Confirm/Effect 语义一致（§5.2(4) 规则 2）。
//
// 类别基线冻结见 TestChatCommandOutputCategoryScreenBaseline。
func TestChatCommandOutputCategoryDump(t *testing.T) {
	for _, command := range sortedRuntimeCommandNames() {
		for _, labeled := range runtimeCommandSpecsOfEntry(runtimeCommandRegistry[command]) {
			spec := labeled.spec
			if spec.Mode != runtimeModeScreen {
				continue
			}
			t.Logf("%s %s mode=%s effect=%s confirm=%v notice=%q derived=%s declared=%s",
				command, labeled.label, spec.Mode, spec.Effect, spec.Confirm, spec.Notice,
				chatCommandOutputCategoryFor(spec), spec.Output)
		}
	}
}

func TestChatCommandOutputCategoryCatalogGuard(t *testing.T) {
	var violations []string
	report := func(format string, args ...any) {
		violations = append(violations, fmt.Sprintf(format, args...))
	}

	for _, catalog := range chatSlashCommandCatalog() {
		for _, name := range catalog.allNames() {
			spec, ok := resolveRuntimeCommandSpec(name)
			if !ok {
				report("命令 %q 未在运行时注册表登记", name)
				continue
			}
			category := chatCommandOutputCategoryFor(spec)
			if !category.declared() {
				report("命令 %q 未命中任何输出类别（fail-closed）", name)
				continue
			}
			checkChatCommandOutputCategoryConsistency(report, name, spec, category)
		}
	}

	for _, command := range sortedRuntimeCommandNames() {
		for _, labeled := range runtimeCommandSpecsOfEntry(runtimeCommandRegistry[command]) {
			spec := labeled.spec
			category := chatCommandOutputCategoryFor(spec)
			if !category.declared() {
				report("%s %s 未命中输出类别", command, labeled.label)
				continue
			}
			if spec.Mode == runtimeModeScreen && !spec.Output.declared() {
				report("screen 档 %s %s 未显式声明输出类别（批次 5 守卫）", command, labeled.label)
			}
			checkChatCommandOutputCategoryConsistency(report, command+" "+labeled.label, spec, category)
		}
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("输出类别守卫失败（%d 条）:\n  %s", len(violations), joinLinesForTest(violations))
	}
}

func checkChatCommandOutputCategoryConsistency(report func(string, ...any), label string, spec runtimeCommandSpec, category chatCommandOutputCategory) {
	switch category {
	case chatOutputInline:
		if spec.Mode != runtimeModeInline {
			report("%s 声明 inline 但 Mode=%s", label, spec.Mode)
		}
	case chatOutputScreenDocument:
		if spec.Mode != runtimeModeScreen {
			report("%s 声明 screen-document 但 Mode=%s", label, spec.Mode)
		}
		if spec.Confirm {
			report("%s 声明 screen-document 但带确认门（应为 screen-interactive）", label)
		}
		if spec.Effect != runtimeEffectRead {
			report("%s 声明 screen-document 但 Effect=%s（只读页必须 Read）", label, spec.Effect)
		}
	case chatOutputScreenInteractive:
		if spec.Mode != runtimeModeScreen {
			report("%s 声明 screen-interactive 但 Mode=%s", label, spec.Mode)
		}
	case chatOutputSideEffect:
		if spec.Mode == runtimeModeScreen {
			report("%s 声明 side-effect 但 Mode=screen", label)
		}
	}
}

// 批次 3 尾批防漂移：只读 list/status 变体必须在注册表显式声明
// screen + read + screen-document，且在忙时通道解析为 S 档（白名单）。
// 该矩阵曾整体退回 inline（批次 3 收尾时的真实漂移），故逐条锁定。
func TestChatCommandOutputCategoryReadOnlyVariantMatrix(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")
	t.Setenv(runtimeInteractionEnv, "auto")

	variants := []string{
		"/model status",
		"/provider status",
		"/theme status",
		"/theme list",
		"/theme preview",
		"/skills list",
		"/mcp list",
		"/mcp status",
		"/profile status",
		"/profile list",
		"/profile show",
		"/profile diff",
	}
	for _, line := range variants {
		spec, ok := resolveRuntimeCommandSpec(line)
		if !ok {
			t.Fatalf("%q 未在运行时注册表登记", line)
		}
		if spec.Mode != runtimeModeScreen || spec.Effect != runtimeEffectRead {
			t.Errorf("%q 应为 screen+read，实际 mode=%s effect=%s", line, spec.Mode, spec.Effect)
		}
		if spec.Output != chatOutputScreenDocument {
			t.Errorf("%q 应显式声明 screen-document，实际 %q", line, spec.Output)
		}
		if spec.Confirm {
			t.Errorf("%q 是只读页，不应带确认门", line)
		}
		if got := chatSlashCommandBusyPolicyFor(line); got != chatBusyPolicyScreen {
			t.Errorf("%q 忙时应走副屏通道（S），实际 %s", line, got)
		}
	}
}

// 批次 5（方案 §6）：/help 必须为副屏命令标注「Esc 返回」，且标注数恰好等于
// 主入口为 screen 的可见命令数（防漂移：新增副屏命令漏标即失败）。
func TestChatSlashHelpMarksScreenCommandsWithEscHint(t *testing.T) {
	lines := buildChatSlashHelpLines()

	// 期望集合独立构造：不能用「输出里有多少个标注」反推期望，否则标注逻辑与
	// 断言共用同一个谓词（chat_slash_help.go 里按 chatSlashHelpPrimaryScreen 决定
	// 是否加标注），断言将恒成立、形同虚设。
	expected := map[string]bool{}
	for _, catalog := range chatSlashCommandCatalog() {
		if catalog.Hidden {
			continue
		}
		if chatSlashHelpPrimaryScreen(catalog.Name) {
			expected[catalog.Name] = true
		}
	}
	if len(expected) == 0 {
		t.Fatal("目录中没有主入口为 screen 的命令，标注守卫失去意义")
	}

	// 实际集合按「行首命令名」精确提取：必须逐名比对，否则 /agents 这类以 /agent
	// 为前缀的兄弟命令会把 /agent 的断言误判为失败（那曾是一个假阳性）。
	got := map[string]bool{}
	for _, line := range lines {
		if !strings.Contains(line, chatSlashHelpScreenMarker) {
			continue
		}
		if name := chatSlashHelpLineCommand(line); name != "" {
			got[name] = true
		}
	}
	for name := range got {
		if !expected[name] {
			t.Fatalf("命令 %s 带了 %q 标注，但主入口并非副屏", name, chatSlashHelpScreenMarker)
		}
	}
	for name := range expected {
		if !got[name] {
			t.Fatalf("副屏命令 %s 的帮助行缺少 %q 标注", name, chatSlashHelpScreenMarker)
		}
	}

	// 显式点名守卫：副屏命令必须标注，非副屏命令必须不标注。
	for _, name := range []string{"/todos", "/history", "/help"} {
		if !chatSlashHelpMarkedLine(lines, name) {
			t.Fatalf("副屏命令 %s 的帮助行缺少 %q 标注", name, chatSlashHelpScreenMarker)
		}
	}
	// /agents 与 /agent 必须分开断言：前者是副屏（runtimeModeScreen）应标注，
	// 后者是内联（runtimeModeInline）不应标注，二者仅靠前缀无法区分。
	for _, name := range []string{"/debug", "/theme", "/profile", "/agent"} {
		if chatSlashHelpMarkedLine(lines, name) {
			t.Fatalf("主入口非副屏的命令 %s 不应带 %q 标注", name, chatSlashHelpScreenMarker)
		}
	}
	for _, name := range []string{"/agents"} {
		if !chatSlashHelpMarkedLine(lines, name) {
			t.Fatalf("副屏命令 %s 的帮助行缺少 %q 标注", name, chatSlashHelpScreenMarker)
		}
	}
}

// chatSlashHelpLineCommand 从一行帮助里取行首命令名。
// 标签列可能形如 "/agents, /a"（别名）或 "/model [name]"，故按逗号/空白截断到
// 第一个命令名。用前缀匹配会把 /agents 误认成 /agent，必须精确相等。
func chatSlashHelpLineCommand(line string) string {
	head := strings.TrimSpace(line)
	if idx := strings.IndexAny(head, ", "); idx >= 0 {
		head = head[:idx]
	}
	if !strings.HasPrefix(head, "/") {
		return ""
	}
	return head
}

// chatSlashHelpMarkedLine 报告帮助行中是否存在该命令名且带副屏标注的行。
func chatSlashHelpMarkedLine(lines []string, label string) bool {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if chatSlashHelpLineCommand(trimmed) == label && strings.Contains(trimmed, chatSlashHelpScreenMarker) {
			return true
		}
	}
	return false
}

type labeledRuntimeCommandSpec struct {
	label string
	spec  runtimeCommandSpec
}

// runtimeCommandSpecsOfEntry 展开注册表条目下的全部 spec（裸/子命令/兜底）。
func runtimeCommandSpecsOfEntry(entry runtimeCommandEntry) []labeledRuntimeCommandSpec {
	out := make([]labeledRuntimeCommandSpec, 0, len(entry.Variants)+2)
	if entry.Bare != nil {
		out = append(out, labeledRuntimeCommandSpec{label: "bare", spec: *entry.Bare})
	}
	for name, spec := range entry.Variants {
		out = append(out, labeledRuntimeCommandSpec{label: "variant:" + name, spec: spec})
	}
	if entry.Wildcard != nil {
		out = append(out, labeledRuntimeCommandSpec{label: "wildcard", spec: *entry.Wildcard})
	}
	return out
}

func sortedRuntimeCommandNames() []string {
	names := make([]string, 0, len(runtimeCommandRegistry))
	for name := range runtimeCommandRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func joinLinesForTest(lines []string) string {
	out := ""
	for i, line := range lines {
		if i > 0 {
			out += "\n  "
		}
		out += line
	}
	return out
}
