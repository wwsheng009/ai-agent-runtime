package runtimeapi

import (
	"testing"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// TestAPISubagentCapabilitiesMapsConfig 是 runtime-server 宿主能力位接线的
// 守卫（P1-1'）：与 CLI 宿主（localSubagentCapabilities）同口径——现状请求
// 位恒开、新能力位默认关；两个宿主不允许出现配置解释漂移。
func TestAPISubagentCapabilitiesMapsConfig(t *testing.T) {
	caps := apiSubagentCapabilities(runtimecfg.AgentsConfig{})
	if caps == nil {
		t.Fatal("apiSubagentCapabilities must never return nil")
	}
	if !caps.AgentOptions || !caps.ToolFilter || !caps.Persona {
		t.Fatalf("current-behavior bits must stay granted by default, got %+v", *caps)
	}
	if caps.ForkContext || caps.OutputSchema {
		t.Fatalf("new capability bits must default off (G4), got %+v", *caps)
	}
	caps = apiSubagentCapabilities(runtimecfg.AgentsConfig{
		ForkContextEnabled:  true,
		OutputSchemaEnabled: true,
	})
	if !caps.ForkContext || !caps.OutputSchema {
		t.Fatalf("configured capability bits must be granted, got %+v", *caps)
	}
}