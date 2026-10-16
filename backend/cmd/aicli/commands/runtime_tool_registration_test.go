package commands

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRuntimeToolFunctionWrappingIsSingleSourced 钉死 P2 slice 2 的收敛结果。
//
// 生产代码里"包装 runtime 工具函数"只允许出现在 function_catalog.go 的
// registerRuntimeBuiltinTool 内；别处再出现 `functions.NewRuntimeToolFunction(`
// 就意味着又多了一份"各写一遍"的惯用法——正是本战役要消灭的多入口。
//
// 为什么用源码扫描而不是行为断言：这里要钉的是**调用形状**（谁在包装、有几处在包装），
// 行为上区分不出"5 处各写一遍"与"1 处收敛"。
//
// 粒度：按**非注释行**里的出现次数计（注释里提到该调用不算），故"同一个文件里再抄一份"
// 也会被抓到，而不只是"多出一个文件"。
func TestRuntimeToolFunctionWrappingIsSingleSourced(t *testing.T) {
	const needle = "functions.NewRuntimeToolFunction("
	// 唯一允许出现该调用的生产文件（helper 本体）。
	allowed := map[string]bool{"function_catalog.go": true}

	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	scanned, occurrences := 0, 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++
		data, err := os.ReadFile(name)
		require.NoError(t, err)
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "//") {
				continue
			}
			if !strings.Contains(line, needle) {
				continue
			}
			occurrences++
			if !allowed[name] {
				t.Errorf("%s 直接包装了 runtime 工具函数；请改用 registerRuntimeBuiltinTool（P2 slice 2 已把它收敛为唯一惯用法）", name)
			}
		}
	}
	// 下限/上限：既防"扫不到任何东西"的假绿，也防"同文件里又抄一份"。
	require.Greater(t, scanned, 50, "scanned too few production files; the assertion would silently pass")
	require.Equal(t, 1, occurrences,
		"expected exactly one production call site of %s (the helper body in function_catalog.go)", needle)
}
