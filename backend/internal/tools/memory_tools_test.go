package tools

import (
	"testing"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

func TestMemoryToolRegistrationConditions(t *testing.T) {
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Memory.Mode = "on"
	cfg.Memory.Tools.Enabled = true
	cfg.Memory.Tools.AllowCorrect = true
	cfg.Memory.Tools.AllowForget = true
	if !ShouldRegisterMemoryTools(cfg) || !ShouldRegisterMemoryForgetTool(cfg) ||
		!ShouldRegisterMemoryCorrectTool(cfg) || !ShouldRegisterMemoryAddTool(cfg) {
		t.Fatal("enabled memory tools should register")
	}
	cfg.Memory.Tools.Enabled = false
	if ShouldRegisterMemoryTools(cfg) || ShouldRegisterMemoryForgetTool(cfg) ||
		ShouldRegisterMemoryCorrectTool(cfg) || ShouldRegisterMemoryAddTool(cfg) {
		t.Fatal("disabled memory tools must not register")
	}
	cfg.Memory.Mode = "off"
	cfg.Memory.Tools.Enabled = true
	if ShouldRegisterMemoryTools(cfg) {
		t.Fatal("mode=off must not register memory tools")
	}
	cfg.Memory.Mode = "on"
	cfg.Memory.Tools.Enabled = true
	cfg.Memory.Manual.Enabled = false
	if ShouldRegisterMemoryAddTool(cfg) {
		t.Fatal("manual.enabled=false must not register memory_add")
	}
	cfg.Memory.Mode = "shadow"
	cfg.Memory.Manual.Enabled = true
	if ShouldRegisterMemoryTools(cfg) || ShouldRegisterMemoryForgetTool(cfg) ||
		ShouldRegisterMemoryCorrectTool(cfg) || ShouldRegisterMemoryAddTool(cfg) {
		t.Fatal("mode=shadow must not register memory tools")
	}
}
