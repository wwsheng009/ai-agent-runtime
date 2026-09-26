package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// docs_mcp_layering_regression_test.go 把 MCP 文档里最容易漂移的两处契约钉住：
//
//  1. 分层顺序（低→高）：configs/mcp.yaml < ~/.aicli/mcp.yaml（user） < ./.aicli/mcp.yaml（project）
//     < ~/.aicli/projects/<项目>/mcp.yaml（local）—— 与 internal/aiclipaths 的候选链
//     + internal/mcp/config.LoadLayered 的反转合并顺序一致；M3 落地 local 层后 install.md 曾漏写该层。
//  2. quickstart 入口可达：docs/mcp/quickstart.md 存在，且被 docs/mcp/README.md 与 install.md 链接。
//
// 只对「分层说明那几行」做顺序断言（全文里这些路径会多次出现，按首次出现比对会误报）。
func TestMCPDocsDeclareLayeredScopeOrder(t *testing.T) {
	repoRoot := helpDocsRepoRoot(t)

	// 低 → 高。
	layers := []string{
		"configs/mcp.yaml",
		"~/.aicli/mcp.yaml",
		"./.aicli/mcp.yaml",
		"~/.aicli/projects/",
	}
	cases := []struct {
		path    string
		marker  string
		context int // 从 marker 所在行起，纳入断言的行数
	}{
		{"docs/aicli/install.md", "层级顺序（低→高）", 2},
		{"docs/mcp/quickstart.md", "同级多个文件同名时按", 2},
		{"docs/user-guide/aicli.md", "配置分层：", 2},
	}
	for _, tc := range cases {
		raw, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(tc.path)))
		if err != nil {
			t.Fatalf("read %s: %v", tc.path, err)
		}
		lines := strings.Split(string(raw), "\n")
		start := -1
		for index, line := range lines {
			if strings.Contains(line, tc.marker) {
				start = index
				break
			}
		}
		if start < 0 {
			t.Fatalf("%s 缺少分层说明（找不到 %q）", tc.path, tc.marker)
		}
		end := start + tc.context
		if end > len(lines) {
			end = len(lines)
		}
		snippet := strings.Join(lines[start:end], "\n")
		cursor := -1
		for _, layer := range layers {
			index := strings.Index(snippet, layer)
			if index < 0 {
				t.Fatalf("%s 分层说明缺少 %q\n%s", tc.path, layer, snippet)
			}
			if index < cursor {
				t.Fatalf("%s 分层说明顺序写反（%q 位置提前）\n%s", tc.path, layer, snippet)
			}
			cursor = index
		}
	}

	for _, rel := range []string{"docs/mcp/README.md", "docs/aicli/install.md"} {
		raw, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if !strings.Contains(string(raw), "quickstart.md") {
			t.Fatalf("%s 应链接 docs/mcp/quickstart.md（新用户入口）", rel)
		}
	}
}

// quickstart 必须覆盖用户要求的四块内容：一分钟 quickstart、命令参考表、recipes、症状式排错。
func TestMCPQuickstartCoversRequiredSections(t *testing.T) {
	repoRoot := helpDocsRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash("docs/mcp/quickstart.md")))
	if err != nil {
		t.Fatalf("read docs/mcp/quickstart.md: %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		"一分钟跑通",
		"命令参考表",
		"复制即用 recipes",
		"症状 → 命令",
		"/mcp select",
		"aicli mcp test-server",
		"aicli mcp import --dry-run",
		"aicli mcp get context7 --json",
		"--scope project",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("docs/mcp/quickstart.md 缺少 %q", want)
		}
	}
}
