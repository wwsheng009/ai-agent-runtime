package remotetools

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// TestRemoteCoreSurfaceComesFromSharedHandlers pins the Phase 6b invariant:
// every non-shell remote tool (read, mutation, artifact) is registered from a
// toolcore Handler through RuntimeTool. The adapter stamps contract_version;
// surface-local toolkit wrappers do not.
func TestRemoteCoreSurfaceComesFromSharedHandlers(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Workspace = t.TempDir()
	cfg.Profile = ProfileOps
	cfg.ToolPrefix = ""
	cfg.Artifacts.Root = t.TempDir()
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	workspace, err := NewWorkspace(cfg.Workspace, cfg.AllowedRoots)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	built, err := buildRegistry(cfg, workspace)
	if err != nil {
		t.Fatalf("buildRegistry: %v", err)
	}
	defer func() {
		if built.jobs != nil {
			built.jobs.Close()
		}
	}()

	coreNames := map[string]bool{
		ToolView:         true,
		ToolLs:           true,
		ToolGlob:         true,
		ToolGrep:         true,
		ToolWrite:        true,
		ToolAppend:       true,
		ToolEdit:         true,
		ToolApplyPatch:   true,
		ToolArtifactRead: true,
	}
	found := map[string]bool{}
	for _, tool := range built.registry.List() {
		if !coreNames[tool.Name()] {
			continue
		}
		provider, ok := tool.(toolkit.ToolDefinitionMetadataProvider)
		if !ok {
			t.Fatalf("tool %q does not expose definition metadata", tool.Name())
		}
		metadata := provider.DefinitionMetadata()
		if metadata["contract_version"] != toolcore.ContractVersion {
			t.Fatalf("tool %q is not a RuntimeTool-backed core Handler: metadata=%+v", tool.Name(), metadata)
		}
		found[tool.Name()] = true
	}
	for name := range coreNames {
		if !found[name] {
			t.Fatalf("core-backed remote tool %q is missing from the registry", name)
		}
	}
}
