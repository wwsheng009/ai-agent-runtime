package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/foldertrust"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

func writeProfileGateFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// A13/A14（Batch 14 / D29）：server 侧项目级 profile 的 prompts 分级门控。
// 未信任（特性开启 + 项目级 profile 标记 + 无信任记录）→ prompts 不进入生效面；
// 特性关闭 → foldertrust 既有语义 trusted → prompts 恢复（信任翻转的等价面）。
func TestResolveProfileSessionStateProjectPromptGate(t *testing.T) {
	workspace := t.TempDir()
	profilesRoot := filepath.Join(workspace, ".aicli", "profiles")
	profileRoot := filepath.Join(profilesRoot, "dev")
	writeProfileGateFile(t, filepath.Join(profileRoot, "profile.yaml"), "profile:\n  name: dev\n  default_agent: coder\n")
	writeProfileGateFile(t, filepath.Join(profileRoot, "agents", "coder", "prompts", "system.md"), "Injected server system prompt.")
	writeProfileGateFile(t, filepath.Join(profileRoot, "agents", "coder", "tools", "policy.yaml"), "allowlist: [read_file]\n")

	handler := &Handler{}
	handler.SetProfileSupport(ProfileSupportConfig{
		Registry: profilesys.NewRegistryFromProfilesConfig(&agentconfig.ProfilesConfig{Root: profilesRoot}),
	})

	t.Setenv(foldertrust.EnvFolderTrust, "1")
	state, err := handler.resolveProfileSessionState("dev", "coder", workspace)
	if err != nil {
		t.Fatalf("resolveProfileSessionState: %v", err)
	}
	if state == nil || state.Resolved == nil {
		t.Fatal("expected resolved profile state")
	}
	if state.PromptText != "" {
		t.Fatalf("server must not apply project profile prompt, got %q", state.PromptText)
	}
	if !state.Resolved.PromptSuppressed || state.Resolved.PromptSuppressionReason == "" {
		t.Fatalf("suppression must be recorded for warnings, got %+v", state.Resolved)
	}
	if len(state.Resolved.ToolPolicy.Allowlist) == 0 {
		t.Fatal("graded gate must keep tool policy declarations")
	}
	if notice := sessionProfilePromptSuppressionWarning(state); !strings.Contains(notice, "未应用") {
		t.Fatalf("switch report must warn about withheld prompts, got %q", notice)
	}

	t.Setenv(foldertrust.EnvFolderTrust, "")
	state, err = handler.resolveProfileSessionState("dev", "coder", workspace)
	if err != nil {
		t.Fatalf("resolveProfileSessionState (feature off): %v", err)
	}
	if !strings.Contains(state.PromptText, "Injected server system prompt.") {
		t.Fatalf("feature-off resolution must apply the profile prompt, got %q", state.PromptText)
	}
	if state.Resolved.PromptSuppressed {
		t.Fatal("feature-off resolution must not be marked suppressed")
	}
	if notice := sessionProfilePromptSuppressionWarning(state); notice != "" {
		t.Fatalf("feature-off resolution must not warn, got %q", notice)
	}
}
