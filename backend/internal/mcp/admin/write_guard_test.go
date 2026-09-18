package admin

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

// TestSaveFileAllowsTempDir 确认测试进程内临时目录写入不受保护逻辑影响。
func TestSaveFileAllowsTempDir(t *testing.T) {
	target := filepath.Join(t.TempDir(), "mcp.yaml")
	if err := SaveFile(target, &config.Config{MCPServers: map[string]config.MCPConfig{}}); err != nil {
		t.Fatalf("temp dir write should be allowed, got %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected temp config to exist: %v", err)
	}
}

// TestSaveFileRejectsNonTempPathInTests 固定隔离契约：测试进程不得改写真实工作区/主目录配置。
func TestSaveFileRejectsNonTempPathInTests(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir available")
	}
	protected := filepath.Join(home, ".aicli", "mcp-guard-probe.yaml")
	if err := SaveFile(protected, &config.Config{MCPServers: map[string]config.MCPConfig{}}); err == nil {
		t.Fatalf("expected non-temp write from a test process to be rejected: %s", protected)
	}
	if _, err := os.Stat(protected); err == nil {
		t.Fatalf("protected path must not be created during tests: %s", protected)
	}
}

// TestIsWithinTempDir 覆盖临时目录判定（Windows 大小写与相对路径）。
func TestIsWithinTempDir(t *testing.T) {
	temp := t.TempDir()
	if !isWithinTempDir(filepath.Join(temp, "nested", "mcp.yaml")) {
		t.Fatalf("expected %s to be treated as temp", temp)
	}
	if isWithinTempDir(filepath.Join(os.TempDir(), "..", "aicli-not-temp", "mcp.yaml")) {
		t.Fatal("expected parent of temp dir to be treated as non-temp")
	}
}
