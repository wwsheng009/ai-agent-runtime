package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
	runtimeprompt "github.com/wwsheng009/ai-agent-runtime/internal/prompt"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

// 镜像字段必须完整传递，否则 server 侧三个消费点都拿不到声明（假开关）。
func TestToProfileInputResolvedAgent_CopiesSelectionAndPromptMode(t *testing.T) {
	mirrored := toProfileInputResolvedAgent(&profilesys.ResolvedAgent{
		ProfileName:  "demo",
		Skills:       profilesys.ResolvedSkillSelection{Allowlist: []string{"docs"}, Denylist: []string{"danger"}},
		MCPSelection: profilesys.ResolvedMCPSelection{UseServers: []string{"filesystem"}},
		PromptMode:   profilesys.PromptModeAppend,
	})
	if mirrored == nil {
		t.Fatal("expected mirrored agent")
	}
	if got := mirrored.Skills.Allowlist; len(got) != 1 || got[0] != "docs" {
		t.Fatalf("skills allowlist not mirrored: %v", got)
	}
	if got := mirrored.Skills.Denylist; len(got) != 1 || got[0] != "danger" {
		t.Fatalf("skills denylist not mirrored: %v", got)
	}
	if got := mirrored.MCPSelection.UseServers; len(got) != 1 || got[0] != "filesystem" {
		t.Fatalf("mcp selection not mirrored: %v", got)
	}
	if mirrored.PromptMode != profilesys.PromptModeAppend {
		t.Fatalf("prompt mode not mirrored: %q", mirrored.PromptMode)
	}
}

const profileAppendTestMarker = "PROFILE-BASE-MARKER"

func newPromptModeTestState(mode string) *profileRuntimeState {
	layers := runtimeprompt.NewLayers()
	layers.AddLayer(runtimeprompt.LayerBase, "Profile System", profileAppendTestMarker, "system.md")
	return &profileRuntimeState{
		Resolved:     &profilesys.ResolvedAgent{PromptMode: mode},
		PromptText:   profileAppendTestMarker,
		PromptLayers: layers,
	}
}

// FR-7 append：内置基础指令在前，profile 组合叠加在后，且 profile 文本仍必须
// 出现在首条 system 指令中（否则 agentConfig.SystemPrompt 会丢 profile）。
func TestBuildRuntimeInstructionMessages_AppendPlacesGuidanceBeforeProfile(t *testing.T) {
	messages := buildRuntimeInstructionMessages(newPromptModeTestState(profilesys.PromptModeAppend), "", "openai")
	if len(messages) == 0 {
		t.Fatal("expected instruction messages")
	}
	first := strings.TrimSpace(messages[0].Content)
	guidanceAt := strings.Index(first, taskDifficultyGuidanceHeader)
	profileAt := strings.Index(first, profileAppendTestMarker)
	if guidanceAt < 0 || profileAt < 0 || guidanceAt > profileAt {
		t.Fatalf("append mode must place built-in guidance before the profile text, got:\n%s", first)
	}
	if got := primarySystemInstructionContent(messages); !strings.Contains(got, profileAppendTestMarker) {
		t.Fatalf("append mode must keep the profile text in the primary system instruction, got:\n%s", got)
	}
}

// FR-7 replace（默认）：保持既有顺序（profile 在前，内置基础指令追加其后）。
func TestBuildRuntimeInstructionMessages_ReplaceKeepsProfileBeforeGuidance(t *testing.T) {
	messages := buildRuntimeInstructionMessages(newPromptModeTestState(""), "", "openai")
	if len(messages) == 0 {
		t.Fatal("expected instruction messages")
	}
	first := strings.TrimSpace(messages[0].Content)
	guidanceAt := strings.Index(first, taskDifficultyGuidanceHeader)
	profileAt := strings.Index(first, profileAppendTestMarker)
	if guidanceAt < 0 || profileAt < 0 || profileAt > guidanceAt {
		t.Fatalf("replace mode must keep the profile text before built-in guidance, got:\n%s", first)
	}
}

// 未声明模式时与显式 replace 逐字节一致（NFR-1 零变化）。
func TestBuildRuntimeInstructionMessages_UnknownModeBehavesAsReplace(t *testing.T) {
	implicit := buildRuntimeInstructionMessages(newPromptModeTestState(""), "", "openai")
	explicit := buildRuntimeInstructionMessages(newPromptModeTestState(profilesys.PromptModeReplace), "", "openai")
	if len(implicit) != len(explicit) {
		t.Fatalf("mode-less and replace mode must agree: %d vs %d messages", len(implicit), len(explicit))
	}
	for index := range implicit {
		if strings.TrimSpace(implicit[index].Content) != strings.TrimSpace(explicit[index].Content) {
			t.Fatalf("message %d differs:\n%s\n---\n%s", index, implicit[index].Content, explicit[index].Content)
		}
	}
}

// 无 profile 的纯内置路径不受影响（零变化）。
func TestBuildRuntimeInstructionMessages_WithoutProfileKeepsGuidanceOnly(t *testing.T) {
	messages := buildRuntimeInstructionMessages(nil, "", "openai")
	if len(messages) == 0 {
		t.Fatal("expected built-in guidance message")
	}
	if !strings.Contains(messages[0].Content, taskDifficultyGuidanceHeader) {
		t.Fatalf("expected built-in guidance, got:\n%s", messages[0].Content)
	}
	if strings.Contains(messages[0].Content, profileAppendTestMarker) {
		t.Fatalf("profile marker must not leak without a profile")
	}
}

func writeMCPSelectionTestConfig(t *testing.T, path string) {
	t.Helper()
	contents := strings.Join([]string{
		"mcpServers:",
		"  filesystem:",
		"    type: stdio",
		"    command: npx",
		"    disabled: true",
		"  browser:",
		"    type: stdio",
		"    command: npx",
		"    disabled: true",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write mcp config: %v", err)
	}
}

// FR-4 服务器选择：无选择时沿用共享 manager（零变化）；有选择时不得复用共享
// manager（否则共享实例会被单个 profile 收窄），且只加载允许的服务器。
func TestResolveProfileMCPAdapter_AppliesServerSelection(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "mcp.yaml")
	writeMCPSelectionTestConfig(t, configPath)

	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.Workspace.Root = root

	sharedAdapter := runtimetools.NewAgentAdapter(runtimetools.NewDefaultManagerWithRuntimeConfig(nil, runtimeConfig))
	handler := &Handler{profileGlobalMCPPath: configPath, mcpManager: sharedAdapter}

	adapter, manager, err := handler.resolveProfileMCPAdapter(context.Background(), &profilesys.ResolvedAgent{MCPConfig: configPath}, runtimeConfig)
	if err != nil {
		t.Fatalf("resolveProfileMCPAdapter: %v", err)
	}
	if manager != nil || adapter != sharedAdapter {
		t.Fatalf("empty selection must reuse the shared manager (manager==nil, adapter==shared)")
	}

	_, scoped, err := handler.resolveProfileMCPAdapter(context.Background(), &profilesys.ResolvedAgent{
		ProfileName:  "demo",
		MCPConfig:    configPath,
		MCPSelection: profilesys.ResolvedMCPSelection{UseServers: []string{"filesystem"}},
	}, runtimeConfig)
	if err != nil {
		t.Fatalf("resolveProfileMCPAdapter (selection): %v", err)
	}
	if scoped == nil {
		t.Fatal("selection must build a scoped manager instead of reusing the shared one")
	}
	defer func() { _ = scoped.Stop() }()

	names := map[string]bool{}
	for _, status := range scoped.ListMCPs() {
		if status != nil {
			names[status.Name] = true
		}
	}
	if !names["filesystem"] {
		t.Fatalf("allowed server missing from scoped manager: %v", names)
	}
	if names["browser"] {
		t.Fatalf("excluded server must not be loaded: %v", names)
	}
}
