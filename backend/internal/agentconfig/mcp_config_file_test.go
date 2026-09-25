package agentconfig

import "testing"

// TestEffectiveAICLIMCPConfigFile 固定 MCP 选择值的优先级：MCP_CONFIG_FILE 环境变量
// 优先于 YAML 值；两者都未设置/为空时返回空串，交由路径解析器按“发现”语义处理
// 工作区层的 ./.aicli/mcp.yaml（不再需要用户先写 aicli.mcp.config_file）。
func TestEffectiveAICLIMCPConfigFile(t *testing.T) {
	t.Setenv("MCP_CONFIG_FILE", "")

	if got := EffectiveAICLIMCPConfigFile(nil); got != "" {
		t.Fatalf("nil cfg = %q, want empty", got)
	}
	empty := &Config{AICLI: &AICLIConfig{}}
	if got := EffectiveAICLIMCPConfigFile(empty); got != "" {
		t.Fatalf("missing mcp block = %q, want empty", got)
	}

	configured := &Config{AICLI: &AICLIConfig{MCP: &AICLIMCPConfig{ConfigFile: "  configs/mcp.yaml  "}}}
	if got := EffectiveAICLIMCPConfigFile(configured); got != "configs/mcp.yaml" {
		t.Fatalf("yaml value = %q, want trimmed configs/mcp.yaml", got)
	}

	// 环境变量优先于 YAML（与 install.md 的优先级 0 一致）。
	t.Setenv("MCP_CONFIG_FILE", "E:/tmp/env-mcp.yaml")
	if got := EffectiveAICLIMCPConfigFile(configured); got != "E:/tmp/env-mcp.yaml" {
		t.Fatalf("env override = %q, want the env value", got)
	}
}
