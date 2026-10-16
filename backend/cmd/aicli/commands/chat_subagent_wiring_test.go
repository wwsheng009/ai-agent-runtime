package commands

import (
	"testing"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// TestLocalSubagentCapabilitiesMapsConfig 是 CLI 宿主能力位接线的守卫
//（P1-1'）：config → SubagentCapabilities 的映射必须保持"现状请求位恒开、
// 新能力位默认关"的口径，否则灰度开关会静默失效或意外放宽。
func TestLocalSubagentCapabilitiesMapsConfig(t *testing.T) {
	// 默认（未配置）：现状已支持的请求位开，新能力位关（G4）。
	caps := localSubagentCapabilities(runtimecfg.AgentsConfig{})
	if caps == nil {
		t.Fatal("localSubagentCapabilities must never return nil")
	}
	if !caps.AgentOptions || !caps.ToolFilter || !caps.Persona {
		t.Fatalf("current-behavior bits must stay granted by default, got %+v", *caps)
	}
	if caps.ForkContext || caps.OutputSchema {
		t.Fatalf("new capability bits must default off (G4), got %+v", *caps)
	}
	// 配置显式开启：对应位随配置打开，未配置的位不受影响。
	caps = localSubagentCapabilities(runtimecfg.AgentsConfig{
		ForkContextEnabled:  true,
		OutputSchemaEnabled: true,
	})
	if !caps.ForkContext || !caps.OutputSchema {
		t.Fatalf("configured capability bits must be granted, got %+v", *caps)
	}
	if !caps.AgentOptions || !caps.ToolFilter || !caps.Persona {
		t.Fatalf("enabling new bits must not reset current-behavior bits, got %+v", *caps)
	}
}