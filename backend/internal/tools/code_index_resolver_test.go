package tools

import (
	"context"
	"testing"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// Phase 3（06 §4 Phase 3）注册门控与解析器测试：
//   - code_tools=off（默认）不注册任何 code.*（与改动前逐字节一致，回滚口径）；
//   - code_tools=on 注册 5 个 code.*；
//   - 解析器在 mode=off / 库不存在时不可用（工具 fallback），库就绪后可用。

func TestRegisterBuiltinToolkitToolsGatesCodeTools(t *testing.T) {
	root := t.TempDir()

	registry := toolkit.NewRegistry()
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Workspace.Root = root
	registerBuiltinToolkitTools(registry, nil, root, cfg)
	if _, ok := registry.Get("code_search"); ok {
		t.Fatal("code_search 不得在 code_tools=off（默认）时注册")
	}
	if _, ok := registry.Get("view"); !ok {
		t.Fatal("view 必须始终注册（P0 工具不替换）")
	}

	registry = toolkit.NewRegistry()
	cfg.Knowledge.CodeTools = knowledge.CodeToolsOn
	registerBuiltinToolkitTools(registry, nil, root, cfg)
	if _, ok := registry.Get("code_search"); ok {
		t.Fatal("mode=off 时 code_search 不得注册（ADR-0004 §4.4 全局硬闸）")
	}

	registry = toolkit.NewRegistry()
	cfg.Knowledge.Mode = knowledge.ModeOn
	registerBuiltinToolkitTools(registry, nil, root, cfg)
	for _, name := range []string{"code_search", "code_inspect", "code_navigate", "code_references", "code_callers"} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("%s 未在 mode=on + code_tools=on 时注册", name)
		}
	}
}

func TestCodeIndexResolverAvailability(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	t.Cleanup(codeIndexCacheGlobal.closeAll)

	// mode=off：永远不可用（即使库文件存在）。
	offCfg := knowledge.DefaultConfig().WithWorkspace(root)
	if _, ok := newCodeIndexResolver(offCfg, root)(ctx); ok {
		t.Fatal("mode=off 时解析器必须不可用")
	}

	// mode=on 但库不存在：不可用（工具 fallback），不是错误。
	onCfg := knowledge.DefaultConfig().WithWorkspace(root)
	onCfg.Mode = knowledge.ModeOn
	resolver := newCodeIndexResolver(onCfg, root)
	if _, ok := resolver(ctx); ok {
		t.Fatal("库不存在时解析器必须不可用")
	}

	// 建库并登记 workspace：可用，且带 Mode 与文件映射。
	store, err := knowledge.OpenStore(ctx, knowledge.StorePathFor(onCfg), false)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if _, err := store.EnsureWorkspace(ctx, knowledge.Workspace{RootPath: root}); err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	handle, ok := resolver(ctx)
	if !ok || handle == nil || handle.Mode != knowledge.ModeOn || handle.WorkspaceID == "" {
		t.Fatalf("handle = %+v ok=%v, want available", handle, ok)
	}
}

func TestCodeIndexResolverUsesContextWorkspace(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	t.Cleanup(codeIndexCacheGlobal.closeAll)
	cfg := knowledge.DefaultConfig().WithWorkspace(root)
	cfg.Mode = knowledge.ModeOn

	store, err := knowledge.OpenStore(ctx, knowledge.StorePathFor(cfg), false)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if _, err := store.EnsureWorkspace(ctx, knowledge.Workspace{RootPath: root}); err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// 管理器配置没有 workspace，但 ctx 绑定了会话 workspace：仍应可用。
	resolver := newCodeIndexResolver(cfg, "")
	handle, ok := resolver(toolctx.WithWorkspaceRoot(ctx, root))
	if !ok || handle == nil || handle.WorkspaceID == "" {
		t.Fatalf("handle = %+v ok=%v, want ctx workspace resolution", handle, ok)
	}
}
