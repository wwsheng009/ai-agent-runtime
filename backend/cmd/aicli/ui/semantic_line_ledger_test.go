package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/vt"
)

// ==== 物理屏幕诊断工具（owned-render-simplification Phase 0）====
//
// `HistoryCommitLedger`（history_commit.go）是 handoff exactly-once 的权威
// 测试账本：身份来自 Token + CellID + Revision + SourceRange + DisplayRange
// + LayoutGeneration，绝不来自文本。这里保留 vt 回放，仅用于观察最终物理
// 屏幕/scrollback；文本相同的合法重绘或不同 CellID 的同文内容不得被它误判。
//
// 依赖：vt.Screen 已扩展 scrollback 记录（N6，ui/vt/screen.go recordScrollbackRows，
// 仅记录 full-width region 即 top==1 的滚动；sub-region 滚动不记录）。

// semanticLineCounts 回放 raw 到 vt.Screen，返回每个语义行（TrimSpace 后
// 非空）在 scrollback 序列 + 屏幕各行中的出现次数。
func semanticLineCounts(t *testing.T, width, height int, raw string) map[string]int {
	t.Helper()
	screen := vt.NewScreen(width, height)
	screen.Feed(raw)
	counts := map[string]int{}
	add := func(line string) {
		line = strings.TrimSpace(line)
		if line == "" {
			return
		}
		counts[line]++
	}
	for _, line := range screen.ScrollbackLines() {
		add(line)
	}
	for row := 1; row <= screen.Height(); row++ {
		add(screen.Line(row))
	}
	return counts
}

// assertSemanticLinesAppearAtMost 是历史回归的物理显示诊断断言，而非
// HistoryCommit exactly-once 判据。调用点必须为每一行提供唯一测试标记；
// token/range 行为由 HistoryCommitLedger 测试覆盖。
func assertSemanticLinesAppearAtMost(t *testing.T, raw string, width, height, max int, lines ...string) {
	t.Helper()
	if max < 1 {
		t.Fatalf("max must be >= 1, got %d", max)
	}
	counts := semanticLineCounts(t, width, height, raw)
	var want, dups []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		want = append(want, trimmed)
		if n := counts[trimmed]; n > max {
			dups = append(dups, fmt.Sprintf("%q ×%d", trimmed, n))
		}
	}
	if len(dups) == 0 {
		return
	}

	var diag strings.Builder
	for _, trimmed := range want {
		n := strings.Count(raw, trimmed)
		if n <= max {
			continue
		}
		diag.WriteString(fmt.Sprintf("== %q（字节流 %d 次）==\n", trimmed, n))
		idx := 0
		shown := 0
		for k := 0; k < n && shown < 3; k++ {
			pos := strings.Index(raw[idx:], trimmed)
			if pos < 0 {
				break
			}
			pos += idx
			start := pos - 60
			if start < 0 {
				start = 0
			}
			end := pos + len(trimmed) + 40
			if end > len(raw) {
				end = len(raw)
			}
			diag.WriteString(fmt.Sprintf("  #%d @%d: %q\n", k+1, pos, strings.ReplaceAll(raw[start:end], "\x1b", "<ESC>")))
			idx = pos + len(trimmed)
			shown++
		}
	}
	t.Fatalf("语义行出现次数超过 %d（scrollback+屏幕重复渲染）:\n%s\n%s", max, strings.Join(dups, "\n"), diag.String())
}

// assertSemanticLinesAbsentFromScrollback 断言 lines 中每个语义行不出现在
// vt.Screen 的 scrollback 序列中（用于"未 handoff 的行不得被提交进
// scrollback"类语义断言；注意与 §4.2 规则 3 的区分：band 恢复场景下物理
// 滚动不在本断言范围，本断言只查语义提交路径）。
func assertSemanticLinesAbsentFromScrollback(t *testing.T, width, height int, raw string, lines ...string) {
	t.Helper()
	screen := vt.NewScreen(width, height)
	screen.Feed(raw)
	inScrollback := map[string]bool{}
	for _, line := range screen.ScrollbackLines() {
		line = strings.TrimSpace(line)
		if line != "" {
			inScrollback[line] = true
		}
	}
	var leaked []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && inScrollback[trimmed] {
			leaked = append(leaked, trimmed)
		}
	}
	if len(leaked) > 0 {
		t.Fatalf("语义上未 handoff 的行被提交进 scrollback: %s\nscrollback=%q\n%s",
			strings.Join(leaked, ", "), screen.ScrollbackLines(), dumpScreen(screen))
	}
}
