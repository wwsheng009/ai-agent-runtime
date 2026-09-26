package policy_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// 手册里的每个 ```yaml 示例都必须能被真实 loader 接受——docs/aicli/permissions.md
// 承诺 recipes「可直接抄」，这条测试就是那句承诺的回归护栏。
//
// 加示例时不需要改本文件：新块自动纳入校验；如果示例故意演示「非法写法」，
// 请把它放进非 yaml 围栏（例如 ```text），或在该块上方标注 # 非法示例 之外的
// 说明——本测试只遍历 ```yaml 块，且要求它们**全部可装载**。
func TestPermissionsDocExamplesParse(t *testing.T) {
	docPath := filepath.Join("..", "..", "..", "docs", "aicli", "permissions.md")
	data, err := os.ReadFile(docPath)
	require.NoError(t, err, "docs/aicli/permissions.md 必须存在（手册与示例校验同源）")

	blocks := extractYAMLBlocks(string(data))
	require.NotEmpty(t, blocks, "手册里应当至少有一个 yaml 示例")

	for i, block := range blocks {
		_, err := policy.ParsePermissionsFile([]byte(block))
		require.NoErrorf(t, err, "第 %d 个 yaml 示例必须可装载:\n%s", i+1, block)
	}
}

func extractYAMLBlocks(markdown string) []string {
	var blocks []string
	lines := strings.Split(markdown, "\n")
	inFence := false
	var current []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inFence {
			if trimmed == "```yaml" {
				inFence = true
				current = nil
			}
			continue
		}
		if trimmed == "```" {
			inFence = false
			blocks = append(blocks, strings.Join(current, "\n"))
			continue
		}
		current = append(current, line)
	}
	return blocks
}
