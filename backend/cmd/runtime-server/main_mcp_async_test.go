package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

const mcpStallHelperEnv = "AICLI_TEST_MCP_STALL_HELPER"

// TestMCPStallHelperProcess 是 buildSkillsMCPManager 后台建连测试的替身 MCP 进程：
// 握手阶段直接挂起，用于模拟慢/不可达的 MCP server。
func TestMCPStallHelperProcess(t *testing.T) {
	if os.Getenv(mcpStallHelperEnv) != "1" {
		return
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

// TestBuildSkillsMCPManager_ReturnsBeforeSlowMCPConnects 验证 runtime-server 启动路径
// 不再等待 MCP 建连：慢 server 仍在后台连接时，bootstrap 也应立即返回。
func TestBuildSkillsMCPManager_ReturnsBeforeSlowMCPConnects(t *testing.T) {
	mcpConfigPath := filepath.Join(t.TempDir(), "mcp.yaml")
	content := fmt.Sprintf(`mcpServers:
  slow:
    name: slow
    type: stdio
    command: %q
    args:
      - -test.run=TestMCPStallHelperProcess
    env:
      %s: "1"
    enabled: true
    timeout: 3s
global:
  connectTimeout: 3s
  healthCheckInterval: 0s
`, os.Args[0], mcpStallHelperEnv)
	if err := os.WriteFile(mcpConfigPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write mcp config: %v", err)
	}

	cfg := &config.Config{
		AICLI: &config.AICLIConfig{
			MCP: &config.AICLIMCPConfig{ConfigFile: mcpConfigPath},
		},
	}
	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Workspace.Root = t.TempDir()

	start := time.Now()
	adapter, manager, err := buildSkillsMCPManager(context.Background(), cfg, runtimeConfig)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("buildSkillsMCPManager failed: %v", err)
	}
	if manager == nil {
		t.Fatal("expected external MCP manager")
	}
	if adapter == nil {
		t.Fatal("expected runtime tool adapter")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("runtime-server bootstrap blocked on MCP connect: %s", elapsed)
	}
	if tools := manager.ListTools(); len(tools) != 0 {
		t.Fatalf("slow MCP tools must not be published before connect: %d", len(tools))
	}

	stopStart := time.Now()
	if err := manager.Stop(); err != nil {
		t.Fatalf("stop MCP manager: %v", err)
	}
	t.Logf("bootstrap=%s stop=%s", elapsed, time.Since(stopStart))
}
