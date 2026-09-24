package commands

import (
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/foldertrust"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// A13（Batch 14 / D29）：项目级 profile 在未信任工作区的 prompts 不进入会话生效面
// （state.PromptText 为空），且分级门控不误伤 tools 声明；信任翻转后 prompts 恢复。
func TestResolveChatProfileStateProjectPromptGate(t *testing.T) {
	workspace := t.TempDir()
	profilesRoot := filepath.Join(workspace, ".aicli", "profiles")
	profileRoot := filepath.Join(profilesRoot, "dev")

	writeTestFile(t, filepath.Join(profileRoot, "profile.yaml"), `profile:
  name: dev
  default_agent: coder
`)
	writeTestFile(t, filepath.Join(profileRoot, "agents", "coder", "prompts", "system.md"), "Injected project system prompt.")
	writeTestFile(t, filepath.Join(profileRoot, "agents", "coder", "tools", "policy.yaml"), "allowlist: [read_file]\n")

	cfg := &config.Config{Profiles: &config.ProfilesConfig{Root: profilesRoot}}

	t.Setenv(foldertrust.EnvFolderTrust, "1")
	resetProcessFolderTrust()
	t.Cleanup(resetProcessFolderTrust)

	// 未信任 + 特性开启：prompts 扣留（分级——tools 声明仍在）。
	setProcessFolderTrust(foldertrust.Resolution{
		FeatureEnabled: true,
		Trusted:        false,
		Outcome:        foldertrust.OutcomeUntrusted,
		Source:         "headless_deny",
		WorkspaceKey:   workspace,
		ProjectRoot:    workspace,
	})
	state, err := resolveChatProfileState(cfg, &chatCommandOptions{ProfileFlag: "dev"})
	if err != nil {
		t.Fatalf("resolveChatProfileState: %v", err)
	}
	if state.PromptText != "" {
		t.Fatalf("project profile prompt must not reach the session, got %q", state.PromptText)
	}
	if !state.Resolved.PromptSuppressed || state.Resolved.PromptSuppressionReason == "" {
		t.Fatalf("suppression must be recorded for warnings, got %+v", state.Resolved)
	}
	if state.Resolved.PromptMode != profilesys.PromptModeReplace {
		t.Fatalf("prompt mode declaration must survive the gate, got %q", state.Resolved.PromptMode)
	}
	if len(state.Resolved.ToolPolicy.Allowlist) == 0 {
		t.Fatal("graded gate must keep tool policy declarations")
	}

	// 信任翻转（/trust grant 后的同会话重解析）：prompts 恢复。
	setProcessFolderTrust(foldertrust.Resolution{
		FeatureEnabled: true,
		Trusted:        true,
		Outcome:        foldertrust.OutcomeTrusted,
		Source:         "grant",
		WorkspaceKey:   workspace,
		ProjectRoot:    workspace,
	})
	state, err = resolveChatProfileState(cfg, &chatCommandOptions{ProfileFlag: "dev"})
	if err != nil {
		t.Fatalf("resolveChatProfileState (trusted): %v", err)
	}
	if !strings.Contains(state.PromptText, "Injected project system prompt.") {
		t.Fatalf("trusted workspace must apply the profile prompt, got %q", state.PromptText)
	}
	if state.Resolved.PromptSuppressed {
		t.Fatal("trusted resolution must not be marked suppressed")
	}
}

// D29 三处警告面（Batch 14 slice 3）：prompts 被扣留时，/profile status、启动摘要、
// Switch Report 必须显式呈现"内容因工作区未信任而未应用"（防"假开关"）。
func TestProfilePromptSuppressionWarnings(t *testing.T) {
	workspace := t.TempDir()
	profilesRoot := filepath.Join(workspace, ".aicli", "profiles")
	profileRoot := filepath.Join(profilesRoot, "dev")
	writeTestFile(t, filepath.Join(profileRoot, "profile.yaml"), "profile:\n  name: dev\n  default_agent: coder\n")
	writeTestFile(t, filepath.Join(profileRoot, "agents", "coder", "prompts", "system.md"), "Injected project system prompt.")
	cfg := &config.Config{Profiles: &config.ProfilesConfig{Root: profilesRoot}}

	t.Setenv(foldertrust.EnvFolderTrust, "1")
	resetProcessFolderTrust()
	t.Cleanup(resetProcessFolderTrust)
	setProcessFolderTrust(foldertrust.Resolution{
		FeatureEnabled: true,
		Trusted:        false,
		Outcome:        foldertrust.OutcomeUntrusted,
		Source:         "headless_deny",
		WorkspaceKey:   workspace,
		ProjectRoot:    workspace,
	})

	session := &ChatSession{Config: cfg}
	state, err := resolveChatProfileState(cfg, &chatCommandOptions{ProfileFlag: "dev"})
	if err != nil {
		t.Fatalf("resolveChatProfileState: %v", err)
	}
	if !applyProfileStateToChatSession(session, state) {
		t.Fatal("expected profile surface projection")
	}

	notice := profilePromptSuppressionNotice(session)
	if !strings.Contains(notice, "未信任") || !strings.Contains(notice, "/profile reload") {
		t.Fatalf("notice must explain cause and recovery, got %q", notice)
	}

	if status := chatProfileStatusText(session); !strings.Contains(status, "⚠") || !strings.Contains(status, "未应用") {
		t.Fatalf("status must warn about withheld prompts:\n%s", status)
	}

	warnedRow := false
	for _, row := range chatProfileSurfaceRows(session) {
		if row.label == "Profile Prompt:" && strings.Contains(row.value, "⚠") {
			warnedRow = true
		}
	}
	if !warnedRow {
		t.Fatalf("startup summary must warn, rows=%+v", chatProfileSurfaceRows(session))
	}

	report, err := applyRuntimeProfileSwitch(session, "dev")
	if err != nil {
		t.Fatalf("applyRuntimeProfileSwitch: %v", err)
	}
	warnedSwitch := false
	for _, warning := range report.Warnings {
		if strings.Contains(warning, "未应用") {
			warnedSwitch = true
		}
	}
	if !warnedSwitch {
		t.Fatalf("switch report must warn, warnings=%v", report.Warnings)
	}
}
