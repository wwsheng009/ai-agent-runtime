package ui

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/markdown"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

func resetActiveStableCollectors() {
	activeStableCollectors.mu.Lock()
	defer activeStableCollectors.mu.Unlock()
	activeStableCollectors.entries = map[scene.CellID]*activeStableCollectorEntry{}
}

// deriveActiveStableEnd 的增量缓存路径必须与「每次新建收集器全量切分」逐步
// 一致（含非前缀替换后的重建）。
func TestDeriveActiveStableEndIncrementalMatchesFreshCollector(t *testing.T) {
	resetActiveStableCollectors()
	chunks := []string{
		"## 流式小节\n\n", "第一段文字。\n\n",
		"- 列表项一\n", "- 列表项二\n\n",
		"```go\n", "func f() error {\n", "\treturn nil\n", "}\n", "```\n\n",
		"| 列一 | 列二 |\n", "|---|---|\n", "| a | b |\n",
		"\n> 引用\n> 续行\n\n",
		"结尾。\n",
	}
	var source strings.Builder
	check := func(label string) {
		t.Helper()
		var fresh markdown.StreamCollector
		_ = fresh.SetContent(source.String())
		if got, want := deriveActiveStableEnd(78, source.String()), len(fresh.Stable()); got != want {
			t.Fatalf("%s: incremental stable=%d, fresh=%d", label, got, want)
		}
	}
	for step, chunk := range chunks {
		source.WriteString(chunk)
		check("step-" + string(rune('a'+step)))
	}
	// 非前缀替换：必须重建而不是复用旧收集器。
	source.Reset()
	source.WriteString("完全不同的新内容。\n\n```go\nfunc g() {}\n```\n")
	check("replacement")
}
