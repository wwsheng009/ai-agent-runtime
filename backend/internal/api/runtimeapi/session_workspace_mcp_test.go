package runtimeapi

// 会话工作区锚定 MCP 的单测：
//   - 按「解析出的生效文件」去重复用（不同工作区解析到同一文件只建一份）；
//   - 与进程级解析相同 → 不建实例（避免配置分裂）；
//   - 会话未绑定工作区 → 回退全局 manager；
//   - LRU 淘汰与会话关闭时释放适配器与连接。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	mcpmanager "github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

type stubWorkspaceMCPAdapter struct {
	closed int
}

func (s *stubWorkspaceMCPAdapter) ListTools() []skill.ToolInfo { return nil }

func (s *stubWorkspaceMCPAdapter) FindTool(name string) (skill.ToolInfo, error) {
	return skill.ToolInfo{}, fmt.Errorf("tool %q not found", name)
}

func (s *stubWorkspaceMCPAdapter) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	return nil, fmt.Errorf("not implemented")
}

func (s *stubWorkspaceMCPAdapter) Close() error {
	s.closed++
	return nil
}

func writeWorkspaceMCPConfig(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	target := filepath.Join(base, ".aicli", "mcp.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(target, []byte("mcpServers: {}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return base
}

func TestWorkspaceMCPManagersDedupeByResolvedFile(t *testing.T) {
	baseA := writeWorkspaceMCPConfig(t)
	baseB := writeWorkspaceMCPConfig(t)
	globalPath := filepath.Join(t.TempDir(), "global-mcp.yaml")

	var created []*stubWorkspaceMCPAdapter
	store := newWorkspaceMCPManagers(WorkspaceMCPSupportConfig{
		GlobalPath: globalPath,
		NewManager: func() mcpmanager.Manager { return mcpmanager.NewManager() },
		Wrap: func(m mcpmanager.Manager) skill.MCPManager {
			adapter := &stubWorkspaceMCPAdapter{}
			created = append(created, adapter)
			return adapter
		},
		MaxEntries: 2,
	})
	t.Cleanup(store.Close)

	first := store.acquire(baseA)
	again := store.acquire(baseA)
	second := store.acquire(baseB)

	if first == nil || again == nil || second == nil {
		t.Fatalf("expected workspace managers, got %v/%v/%v", first, again, second)
	}
	if first != again {
		t.Fatal("same workspace must reuse the cached manager")
	}
	if first == second {
		t.Fatal("different resolved files must not share a manager")
	}
	if len(created) != 2 {
		t.Fatalf("expected 2 constructed adapters, got %d", len(created))
	}

	store.Close()
	for index, adapter := range created {
		if adapter.closed != 1 {
			t.Fatalf("adapter %d closed=%d, want 1", index, adapter.closed)
		}
	}
}

func TestWorkspaceMCPManagersSkipWhenSameAsProcessResolution(t *testing.T) {
	base := writeWorkspaceMCPConfig(t)
	resolved := filepath.Join(base, ".aicli", "mcp.yaml")
	wrapCalled := false

	store := newWorkspaceMCPManagers(WorkspaceMCPSupportConfig{
		GlobalPath: resolved,
		NewManager: func() mcpmanager.Manager { return mcpmanager.NewManager() },
		Wrap: func(m mcpmanager.Manager) skill.MCPManager {
			wrapCalled = true
			return &stubWorkspaceMCPAdapter{}
		},
	})
	t.Cleanup(store.Close)

	if got := store.acquire(base); got != nil {
		t.Fatalf("same-as-global resolution must reuse the process manager, got %v", got)
	}
	if wrapCalled {
		t.Fatal("same-as-global resolution must not construct a second manager")
	}
}

func TestSessionBaseMCPManagerUsesWorkspaceChain(t *testing.T) {
	globalAdapter := &stubWorkspaceMCPAdapter{}
	handler := &Handler{mcpManager: globalAdapter}
	handler.SetWorkspaceMCPSupport(WorkspaceMCPSupportConfig{
		GlobalPath: filepath.Join(t.TempDir(), "global-mcp.yaml"),
		NewManager: func() mcpmanager.Manager { return mcpmanager.NewManager() },
		Wrap: func(m mcpmanager.Manager) skill.MCPManager {
			return &stubWorkspaceMCPAdapter{}
		},
	})
	t.Cleanup(handler.CloseWorkspaceMCPSupport)

	workspace := writeWorkspaceMCPConfig(t)
	session := chat.NewSession("user")
	session.SetContext(sessionmeta.WorkspacePath, workspace)

	base := handler.sessionBaseMCPManager(context.Background(), session)
	if base == nil || base == globalAdapter {
		t.Fatalf("workspace-bound session must use the workspace manager, got %v", base)
	}

	// 未绑定工作区的会话回退进程级 manager。
	other := chat.NewSession("user")
	if got := handler.sessionBaseMCPManager(context.Background(), other); got != globalAdapter {
		t.Fatalf("workspace-less session must fall back to the global manager, got %v", got)
	}
}

func TestWorkspaceMCPManagersEvictLRU(t *testing.T) {
	baseA := writeWorkspaceMCPConfig(t)
	baseB := writeWorkspaceMCPConfig(t)
	var created []*stubWorkspaceMCPAdapter

	store := newWorkspaceMCPManagers(WorkspaceMCPSupportConfig{
		GlobalPath: filepath.Join(t.TempDir(), "global-mcp.yaml"),
		NewManager: func() mcpmanager.Manager { return mcpmanager.NewManager() },
		Wrap: func(m mcpmanager.Manager) skill.MCPManager {
			adapter := &stubWorkspaceMCPAdapter{}
			created = append(created, adapter)
			return adapter
		},
		MaxEntries: 1,
	})
	t.Cleanup(store.Close)

	first := store.acquire(baseA)
	second := store.acquire(baseB)
	if first == nil || second == nil {
		t.Fatal("expected both workspaces to acquire a manager")
	}
	if created[0].closed != 1 {
		t.Fatalf("LRU eviction must close the evicted adapter, closed=%d", created[0].closed)
	}

	recreated := store.acquire(baseA)
	if recreated == nil || recreated == first {
		t.Fatal("evicted workspace must be rebuilt on next acquire")
	}
	if len(created) != 3 {
		t.Fatalf("expected 3 adapters after eviction+rebuild, got %d", len(created))
	}
}
