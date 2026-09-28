package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// chat_screen_guard_test.go 是副屏框架的防漂移守卫。
//
// 批次 2（A2）：A 族 picker 不得自行取副屏租约，租约编排只能由
// chat_screen_framework.go 的 chatScreenAcquireLease 完成。
//
// 批次 5（D-E）：CommandResult 的旧 Open* 效果字段与 legacy 回滚分支已删除，
// 命令一律返回 CommandResult.Screen（或经统一 dispatch 派发）。本文件扫描
// 生产源码，防止旧字段/旧开关/框架外租约旁路回归。

// chatScreenGuardLegacyResultFields 是批次 5 删除的 CommandResult 旧字段名。
// 生产源码中不得再出现这些字段的声明或字面量赋值（`Name:` 形式）；注释里的
// 历史说明不算（chatScreenGuardSourceReferences 跳过注释行）。
var chatScreenGuardLegacyResultFields = []string{
	"OpenTranscript",
	"OpenDebugOverlay",
	"OpenWebEndpointsScreen",
	"OpenUsageScreen",
	"OpenAccountScreen",
	"OpenAccountsScreen",
	"OpenResumePicker",
	"OpenBacktrackPicker",
	"OpenModelPicker",
	"OpenThemePicker",
	"OpenSkillPicker",
	"OpenExportPicker",
	"OpenMCPPicker",
}

// chatScreenGuardLegacyFrameworkSymbols 是批次 5 删除的 legacy 开关/映射层
// 符号；生产源码中不得再出现（注释历史说明除外）。
var chatScreenGuardLegacyFrameworkSymbols = []string{
	"chatScreenFrameworkLegacy",
	"dispatchLegacyChatScreenOpeners",
	"chatScreenLegacySpecs",
	"runChatPickerScreenLegacy",
	"chatPickerOpen(",
	"chatPickerClose(",
}

// chatScreenGuardFrameworkFile 是唯一允许调用 AcquireAlternateScreen 的文件。
const chatScreenGuardFrameworkFile = "chat_screen_framework.go"

func chatScreenGuardScanSources(t *testing.T, needle string) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var hits []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(".", name)
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if chatScreenGuardSourceReferences(string(content), needle) {
			hits = append(hits, name)
		}
	}
	sort.Strings(hits)
	return hits
}

// chatScreenGuardSourceReferences 只看真实代码行：注释里提到 API 名字（例如
// 解释租约预算的文档注释）不算调用点，守卫度量的是 A2 语义（框架外调用点）。
func chatScreenGuardSourceReferences(content, needle string) bool {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") ||
			strings.HasPrefix(trimmed, "/*") ||
			strings.HasPrefix(trimmed, "*") {
			continue
		}
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

// chatScreenGuardScanAllGoSources 扫描包目录内所有 *.go（含 _test.go）：批次 5
// 删除的旧字段/legacy 符号不得在生产代码或测试代码中复活。返回 "file:line: needle"
// 形式的命中列表；注释（整行注释与行内 //）里的历史说明不算。
func chatScreenGuardScanAllGoSources(t *testing.T, needles []string) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var hits []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if name == "chat_screen_guard_test.go" {
			// 守卫自身持有 needle 清单（字面量即引用）；跳过以免自证，
			// 其余测试文件照常扫描。
			continue
		}
		content, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, needle := range needles {
			for lineNo, line := range strings.Split(string(content), "\n") {
				if chatScreenGuardLineIsComment(line) {
					continue
				}
				if strings.Contains(line, needle) {
					hits = append(hits, fmt.Sprintf("%s:%d: %s", name, lineNo+1, needle))
				}
			}
		}
	}
	sort.Strings(hits)
	return hits
}

// chatScreenGuardLineIsComment 判定整行/行内注释：行内出现 // 之后的部分不算
// 代码（守卫只度量真实引用）。
func chatScreenGuardLineIsComment(line string) bool {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "//") ||
		strings.HasPrefix(trimmed, "/*") ||
		strings.HasPrefix(trimmed, "*") {
		return true
	}
	if idx := strings.Index(line, "//"); idx >= 0 && strings.TrimSpace(line[:idx]) == "" {
		return true
	}
	return false
}

// chatScreenGuardIdentifierBoundary 判断字节是否构成 Go 标识符边界。
func chatScreenGuardIdentifierBoundary(b byte) bool {
	return !(b == '_' ||
		(b >= '0' && b <= '9') ||
		(b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z'))
}

// chatScreenGuardLegacyFieldOffsets 返回源码中旧字段名的真实使用偏移：
//   - 必须是完整标识符（OpenTranscriptOverlay 等更长的 UI 动作名不算）；
//   - 注释中的历史说明不算；
//   - ui.OpenThemePicker 等 UI actor 动作身份保留（picker 的 OpenAction/
//     CloseAction 仍是这些 ui 动作），只有裸字段用法（result.OpenUsageScreen、
//     OpenUsageScreen: ...）才算违规。
func chatScreenGuardLegacyFieldOffsets(content, field string) []int {
	var offsets []int
	cursor := 0
	for {
		idx := strings.Index(content[cursor:], field)
		if idx < 0 {
			return offsets
		}
		idx += cursor
		cursor = idx + 1
		if idx > 0 && !chatScreenGuardIdentifierBoundary(content[idx-1]) {
			continue
		}
		end := idx + len(field)
		if end < len(content) && !chatScreenGuardIdentifierBoundary(content[end]) {
			continue
		}
		if strings.HasSuffix(content[:idx], "ui.") {
			continue
		}
		lineStart := strings.LastIndexByte(content[:idx], '\n') + 1
		lineEnd := strings.IndexByte(content[idx:], '\n')
		if lineEnd < 0 {
			lineEnd = len(content)
		} else {
			lineEnd += idx
		}
		if chatScreenGuardLineIsComment(content[lineStart:lineEnd]) {
			continue
		}
		offsets = append(offsets, idx)
	}
}

// chatScreenGuardLineNumber 返回偏移处的 1-based 行号。
func chatScreenGuardLineNumber(content string, offset int) int {
	return strings.Count(content[:offset], "\n") + 1
}

// 批次 5：legacy picker 内联租约（chatPickerOpen/chatPickerClose）已随开关删除，
// A 族 picker 只能经统一框架的 chatScreenAcquireLease 取租约。
func TestChatScreenGuardA2PickerLeaseOnlyThroughFramework(t *testing.T) {
	for _, needle := range []string{"chatPickerOpen(", "chatPickerClose("} {
		if hits := chatScreenGuardScanSources(t, needle); len(hits) != 0 {
			t.Fatalf("%v 仍引用 %q：批次 5 后 picker 租约只允许经 chat_screen_framework.go（A2 守卫）", hits, needle)
		}
	}
}

func TestChatScreenGuardA2AlternateScreenSitesAreWhitelisted(t *testing.T) {
	hits := chatScreenGuardScanSources(t, "AcquireAlternateScreen")
	if len(hits) != 1 || hits[0] != chatScreenGuardFrameworkFile {
		t.Fatalf("AcquireAlternateScreen 调用点必须仅剩 %s，实际 %v（批次 5：框架外调用点 = 0）",
			chatScreenGuardFrameworkFile, hits)
	}
}

// 批次 5（D-E）：CommandResult 只保留 Screen 作为副屏效应字段；旧 Open* 字段
// 不得在生产源码或测试源码中重新出现（声明、`result.OpenX` 访问、`OpenX:`
// 字面量赋值都会被 needle 捕获；ui.OpenX 动作身份除外）。
func TestChatScreenGuardB5LegacyResultFieldsRemoved(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var hits []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		if name == "chat_screen_guard_test.go" {
			// 同上：字段 needle 清单定义在本文件里，跳过自证。
			continue
		}
		content, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, field := range chatScreenGuardLegacyResultFields {
			for _, offset := range chatScreenGuardLegacyFieldOffsets(string(content), field) {
				hits = append(hits, fmt.Sprintf("%s:%d: %s", name, chatScreenGuardLineNumber(string(content), offset), field))
			}
		}
	}
	sort.Strings(hits)
	if len(hits) != 0 {
		t.Fatalf("旧 Open* 字段仍在以下位置出现（批次 5 已删除，统一用 CommandResult.Screen）：\n%s", strings.Join(hits, "\n"))
	}
}

// 批次 5：legacy 开关与映射层符号必须彻底删除，只保留 unknown_env 警告路径。
func TestChatScreenGuardB5LegacyFrameworkSymbolsRemoved(t *testing.T) {
	if hits := chatScreenGuardScanAllGoSources(t, chatScreenGuardLegacyFrameworkSymbols); len(hits) != 0 {
		t.Fatalf("legacy 符号仍出现在以下位置（批次 5 已删除 legacy 分支）：\n%s", strings.Join(hits, "\n"))
	}
}

// I8：框架不读 stdin、不直接写 TTY、不落 legacy stdout 直写；输入一律经既有
// reducer / actor 通道（§4 不变量）。
func TestChatScreenGuardI8FrameworkDoesNotTouchTTYDirectly(t *testing.T) {
	content, err := os.ReadFile("chat_screen_framework.go")
	if err != nil {
		t.Fatalf("read framework: %v", err)
	}
	source := string(content)
	for _, needle := range []string{"os.Stdin", "os.Stdout", "os.Stderr", "printChatCommandOutput"} {
		if strings.Contains(source, needle) {
			t.Fatalf("chat_screen_framework.go 引用了 %q：框架不得直接读写 TTY/落 legacy 直写（I8）", needle)
		}
	}
}
