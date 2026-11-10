package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

func memoryToolTestConfig(t *testing.T) *runtimecfg.RuntimeConfig {
	t.Helper()
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Workspace.Root = t.TempDir()
	cfg.Memory.Mode = "on"
	cfg.Memory.Injection.Mode = "hybrid"
	cfg.Memory.Tools.Enabled = true
	cfg.Memory.Tools.AllowCorrect = true
	cfg.Memory.Tools.AllowForget = true
	return cfg
}

func TestMemoryToolsLifecycle(t *testing.T) {
	cfg := memoryToolTestConfig(t)
	ctx := toolctx.WithSessionID(context.Background(), "session-tools")
	add := NewMemoryAddTool(cfg)
	result, err := add.Execute(ctx, map[string]interface{}{
		"text": "Use revisioned memory writes.", "kind": "decision", "tags": []interface{}{"memory"},
	})
	if err != nil || !result.Success {
		t.Fatalf("memory_add = %+v, %v", result, err)
	}
	var note struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(result.Content), &note); err != nil || note.ID == "" {
		t.Fatalf("memory_add content = %q, %v", result.Content, err)
	}

	searchResult, err := NewMemorySearchTool(cfg).Execute(ctx, map[string]interface{}{"query": "revisioned"})
	if err != nil || !searchResult.Success || !strings.Contains(searchResult.Content, note.ID) {
		t.Fatalf("memory_search = %+v, %v", searchResult, err)
	}
	readResult, err := NewMemoryReadTool(cfg).Execute(ctx, map[string]interface{}{"id": note.ID})
	if err != nil || !readResult.Success {
		t.Fatalf("memory_read = %+v, %v", readResult, err)
	}
	correctResult, err := NewMemoryCorrectTool(cfg).Execute(ctx, map[string]interface{}{
		"id": note.ID, "text": "Use correction revisions.",
	})
	if err != nil || !correctResult.Success {
		t.Fatalf("memory_correct = %+v, %v", correctResult, err)
	}

	statsResult, err := NewMemoryStatsTool(cfg).Execute(ctx, nil)
	if err != nil || !statsResult.Success || !strings.Contains(statsResult.Content, `"Mode": "on"`) {
		t.Fatalf("memory_stats = %+v, %v", statsResult, err)
	}
	forgetResult, err := NewMemoryForgetTool(cfg).Execute(ctx, map[string]interface{}{
		"id": note.ID, "confirm": true,
	})
	if err != nil || !forgetResult.Success {
		t.Fatalf("memory_forget = %+v, %v", forgetResult, err)
	}
}

func TestMemoryToolsForgetRequiresConfirmAndConfigGate(t *testing.T) {
	cfg := memoryToolTestConfig(t)
	ctx := context.Background()
	addResult, err := NewMemoryAddTool(cfg).Execute(ctx, map[string]interface{}{"text": "forget gate"})
	if err != nil || !addResult.Success {
		t.Fatalf("memory_add = %+v, %v", addResult, err)
	}
	var note struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(addResult.Content), &note); err != nil {
		t.Fatalf("decode note: %v", err)
	}
	withoutConfirm, err := NewMemoryForgetTool(cfg).Execute(ctx, map[string]interface{}{"id": note.ID})
	if err != nil || withoutConfirm.Success {
		t.Fatalf("memory_forget without confirm = %+v, %v", withoutConfirm, err)
	}
	cfg.Memory.Tools.AllowForget = false
	disabled, err := NewMemoryForgetTool(cfg).Execute(ctx, map[string]interface{}{"id": note.ID, "confirm": true})
	if err != nil || disabled.Success {
		t.Fatalf("memory_forget disabled = %+v, %v", disabled, err)
	}
}
