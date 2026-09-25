package commands

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
)

func writeMCPConfigFileForTest(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// 显式 --config-file 仍然精确加载单个文件（不合并发现链）。
func TestLoadMCPManagerLayeredHonoursExplicitOverride(t *testing.T) {
	dir := t.TempDir()
	explicit := writeMCPConfigFileForTest(t, dir, "explicit.yaml", `mcpServers:
  only:
    type: stdio
    command: only-cmd
    timeout: 30s
`)

	previous := mcpConfigFile
	mcpConfigFile = explicit
	t.Cleanup(func() { mcpConfigFile = previous })

	mgr := manager.NewManager()
	if err := loadMCPManagerLayered(mgr); err != nil {
		t.Fatalf("loadMCPManagerLayered: %v", err)
	}
	statuses := mgr.ListMCPs()
	if len(statuses) != 1 || statuses[0].Name != "only" {
		t.Fatalf("statuses = %#v", statuses)
	}
	if statuses[0].ConfigSource != "explicit" || statuses[0].ConfigPath != explicit {
		t.Fatalf("来源应指向显式文件: %#v", statuses[0])
	}
}

func TestLoadMCPManagerLayeredMissingExplicitFile(t *testing.T) {
	previous := mcpConfigFile
	mcpConfigFile = t.TempDir() + string(os.PathSeparator) + "missing.yaml"
	t.Cleanup(func() { mcpConfigFile = previous })

	mgr := manager.NewManager()
	err := loadMCPManagerLayered(mgr)
	if err == nil || !strings.Contains(err.Error(), "missing.yaml") {
		t.Fatalf("显式文件缺失应给出路径: %v", err)
	}
}

// list 的文本输出必须标注来源与覆盖关系，避免「改了不生效」静默发生。
func TestRenderMCPStatusesShowsLayerOriginAndShadow(t *testing.T) {
	statuses := []*mcpconfig.MCPStatus{
		{
			Name:            "shared",
			Type:            "streamable",
			Enabled:         true,
			ConfigSource:    "project",
			ConfigPath:      "/repo/.aicli/mcp.yaml",
			ShadowedSources: []mcpconfig.SourceRef{{Source: "user", Path: "/home/.aicli/mcp.yaml"}},
		},
	}

	stdout := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = write
	renderMCPStatuses(statuses, mcpCommandOptions{})
	_ = write.Close()
	os.Stdout = stdout
	data, _ := io.ReadAll(read)
	_ = read.Close()
	output := string(data)

	for _, want := range []string{
		"来源: project (/repo/.aicli/mcp.yaml)",
		"覆盖: user (/home/.aicli/mcp.yaml)",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("输出缺少 %q:\n%s", want, output)
		}
	}
}

// chat /mcp 面板同样要显示来源与覆盖。
func TestChatMCPItemLinesShowsLayerOrigin(t *testing.T) {
	item := mcpadmin.Item{
		Config: mcpconfig.MCPConfig{Name: "shared", Type: "stdio", Command: "cmd"},
		Status: &mcpconfig.MCPStatus{
			Name:            "shared",
			Enabled:         true,
			ConfigSource:    "user",
			ConfigPath:      "/home/.aicli/mcp.yaml",
			ShadowedSources: []mcpconfig.SourceRef{{Source: "default", Path: "configs/mcp.yaml"}},
		},
	}
	joined := strings.Join(chatMCPItemLines(item), "\n")
	if !strings.Contains(joined, "来源: user (/home/.aicli/mcp.yaml)") {
		t.Fatalf("缺少来源行:\n%s", joined)
	}
	if !strings.Contains(joined, "覆盖: default (configs/mcp.yaml)") {
		t.Fatalf("缺少覆盖行:\n%s", joined)
	}
}
