package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
)

// Phase 5 变更源 2：turn 边界的知识层外部变更校正接线
// （shell/exec 写盘、外部写盘、git checkout 的兜底）。

func TestSyncKnowledgeExternalChangesCallsLayer(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	seed := filepath.Join(root, "demo", "a.go")
	if err := os.MkdirAll(filepath.Dir(seed), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(seed, []byte("package demo\n\nfunc Alpha() int { return 1 }\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cfg := knowledge.DefaultConfig().WithWorkspace(root)
	cfg.Mode = knowledge.ModeShadow
	act, err := knowledge.Activate(ctx, cfg, root, knowledge.ActivationOptions{SkipInitialIndex: true})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	t.Cleanup(func() { _ = act.Close() })
	if _, err := knowledge.RunIndex(ctx, act.Layer().Store(), cfg); err != nil {
		t.Fatalf("RunIndex: %v", err)
	}

	agent := &Agent{config: &Config{Options: map[string]interface{}{
		"context_knowledge_layer": act.Layer(),
	}}}
	loop := &ReActLoop{agent: agent}
	loop.syncKnowledgeExternalChanges(ctx)

	// 临时工作区不是 git 仓库：git 源应被跳过，但 stat 校正必须已执行。
	syncs, _, _ := act.Layer().ExternalSyncStats()
	if syncs == 0 {
		t.Fatalf("turn 边界必须触发校正: syncs=%d", syncs)
	}
}

func TestSyncKnowledgeExternalChangesWithoutLayerIsNoop(t *testing.T) {
	// 未挂载知识层 / 无 agent：不得 panic、不得产生任何副作用。
	(&ReActLoop{}).syncKnowledgeExternalChanges(context.Background())
	(&ReActLoop{agent: &Agent{config: &Config{}}}).syncKnowledgeExternalChanges(context.Background())
	(&ReActLoop{agent: &Agent{config: &Config{Options: map[string]interface{}{
		"context_knowledge_layer": "not-a-layer",
	}}}}).syncKnowledgeExternalChanges(context.Background())
}
