package tools

import (
	"fmt"
	"strconv"
	"testing"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

func TestMemoryToolRegistrationConditions(t *testing.T) {
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Memory.Mode = "on"
	cfg.Memory.Tools.Enabled = true
	cfg.Memory.Tools.AllowCorrect = true
	cfg.Memory.Tools.AllowForget = true
	cfg.Memory.Tools.AllowRollback = true
	if !ShouldRegisterMemoryTools(cfg) || !ShouldRegisterMemoryForgetTool(cfg) ||
		!ShouldRegisterMemoryCorrectTool(cfg) || !ShouldRegisterMemoryRollbackTool(cfg) ||
		!ShouldRegisterMemoryAddTool(cfg) {
		t.Fatal("enabled memory tools should register")
	}
	cfg.Memory.Tools.Enabled = false
	if ShouldRegisterMemoryTools(cfg) || ShouldRegisterMemoryForgetTool(cfg) ||
		ShouldRegisterMemoryCorrectTool(cfg) || ShouldRegisterMemoryRollbackTool(cfg) ||
		ShouldRegisterMemoryAddTool(cfg) {
		t.Fatal("disabled memory tools must not register")
	}
	cfg.Memory.Mode = "off"
	cfg.Memory.Tools.Enabled = true
	if ShouldRegisterMemoryTools(cfg) || ShouldRegisterMemoryRollbackTool(cfg) {
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
		ShouldRegisterMemoryCorrectTool(cfg) || ShouldRegisterMemoryRollbackTool(cfg) ||
		ShouldRegisterMemoryAddTool(cfg) {
		t.Fatal("mode=shadow must not register memory tools")
	}
}

func TestMemoryHistoryToolRegistrationAndSchema(t *testing.T) {
	root := t.TempDir()
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Workspace.Root = root
	cfg.Memory.Mode = "on"
	cfg.Memory.Tools.Enabled = true

	registry := toolkit.NewRegistry()
	registerBuiltinToolkitTools(registry, nil, root, cfg, nil)

	history, ok := registry.Get("memory_history")
	if !ok {
		t.Fatal("memory_history must be registered when memory tools are enabled")
	}
	if core, ok := registry.Get("memory_core_get"); !ok || core.Name() != "memory_core_get" {
		t.Fatal("memory_core_get must be registered when memory tools are enabled")
	}
	if _, ok := registry.Get("memory_rollback"); ok {
		t.Fatal("memory_rollback must not be exposed as a regular agent tool")
	}
	schema, ok := registry.ParameterSchema(history.Name())
	if !ok {
		t.Fatal("memory_history schema must be captured by the registry")
	}
	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("memory_history properties = %#v", schema["properties"])
	}
	id, ok := properties["id"].(map[string]interface{})
	if !ok || id["type"] != "string" {
		t.Fatalf("memory_history id schema = %#v", properties["id"])
	}
	limit, ok := properties["limit"].(map[string]interface{})
	minimum, minErr := strconv.Atoi(fmt.Sprint(limit["minimum"]))
	maximum, maxErr := strconv.Atoi(fmt.Sprint(limit["maximum"]))
	if !ok || limit["type"] != "integer" || minErr != nil || maxErr != nil ||
		minimum != 1 || maximum != 100 {
		t.Fatalf("memory_history limit schema = %#v", properties["limit"])
	}
	required, ok := schema["required"].([]interface{})
	if !ok || len(required) != 1 || required[0] != "id" {
		t.Fatalf("memory_history required = %#v", schema["required"])
	}

	cfg.Memory.Tools.AllowRollback = true
	registry = toolkit.NewRegistry()
	registerBuiltinToolkitTools(registry, nil, root, cfg, nil)
	rollback, ok := registry.Get("memory_rollback")
	if !ok {
		t.Fatal("memory_rollback must register only when memory.tools.allowRollback is enabled")
	}
	rollbackSchema, ok := registry.ParameterSchema(rollback.Name())
	if !ok {
		t.Fatal("memory_rollback schema must be captured by the registry")
	}
	rollbackProperties, ok := rollbackSchema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("memory_rollback properties = %#v", rollbackSchema["properties"])
	}
	for _, name := range []string{"id", "target_version", "expected_revision", "reason", "confirm", "revert_supersede"} {
		if _, ok := rollbackProperties[name]; !ok {
			t.Fatalf("memory_rollback missing %q schema property: %#v", name, rollbackProperties)
		}
	}
	rollbackRequired, ok := rollbackSchema["required"].([]interface{})
	if !ok || len(rollbackRequired) != 5 {
		t.Fatalf("memory_rollback required = %#v", rollbackSchema["required"])
	}
}
