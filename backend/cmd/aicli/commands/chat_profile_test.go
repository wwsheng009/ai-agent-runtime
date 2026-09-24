package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestResolveChatProfileState_AppliesDefaultsPromptAndPolicy(t *testing.T) {
	profilesRoot := t.TempDir()
	globalSkills := t.TempDir()
	profileRoot := filepath.Join(profilesRoot, "dev")

	writeTestFile(t, filepath.Join(profileRoot, "profile.yaml"), `profile:
  name: dev
  default_agent: coder
providers:
  default_provider: nvidia
agents:
  coder:
    model: z-ai/glm4.7
`)
	writeTestFile(t, filepath.Join(profileRoot, "runtime.yaml"), "agent:\n  defaultModel: custom\n")
	writeTestFile(t, filepath.Join(profileRoot, "mcp.yaml"), "mcp_servers: {}\n")
	writeTestFile(t, filepath.Join(profileRoot, "agents", "coder", "prompts", "system.md"), "System prompt.")
	writeTestFile(t, filepath.Join(profileRoot, "agents", "coder", "prompts", "tools.md"), "Use tools carefully.")
	writeTestFile(t, filepath.Join(profileRoot, "agents", "coder", "memory", "memory.json"), `{"summary":"cached profile memory"}`)
	writeTestFile(t, filepath.Join(profileRoot, "agents", "coder", "context", "notes.md"), "Profile investigation notes.")
	writeTestFile(t, filepath.Join(profileRoot, "agents", "coder", "tools", "policy.yaml"), `allowlist: [read_file]
denylist: [write_file]
read_only: true
sandbox:
  allowedPaths: ["."]`)
	mustMkdir(t, filepath.Join(profileRoot, "agents", "coder", "sessions"))
	mustMkdir(t, filepath.Join(profileRoot, "skills"))
	mustMkdir(t, filepath.Join(profileRoot, "agents", "coder", "workspace", "skills"))

	cfg := &config.Config{
		Profiles: &config.ProfilesConfig{Root: profilesRoot},
		AICLI: &config.AICLIConfig{
			MCP: &config.AICLIMCPConfig{ConfigFile: filepath.Join("configs", "mcp.yaml")},
		},
		SkillsRuntime: &config.SkillsRuntimeConfig{
			Enabled:  true,
			SkillDir: globalSkills,
		},
	}

	state, err := resolveChatProfileState(cfg, &chatCommandOptions{ProfileFlag: "dev"})
	if err != nil {
		t.Fatalf("resolveChatProfileState: %v", err)
	}
	if !state.Active() {
		t.Fatal("expected active profile state")
	}
	if state.Resolved.ProfileName != "dev" || state.Resolved.AgentID != "coder" {
		t.Fatalf("unexpected resolved profile: %+v", state.Resolved)
	}
	if state.Resolved.RuntimeConfig != filepath.Join(profileRoot, "runtime.yaml") {
		t.Fatalf("unexpected runtime config path: %q", state.Resolved.RuntimeConfig)
	}
	if state.Resolved.MCPConfig != filepath.Join(profileRoot, "mcp.yaml") {
		t.Fatalf("unexpected mcp config path: %q", state.Resolved.MCPConfig)
	}
	if len(state.Resolved.SkillDirs) != 3 {
		t.Fatalf("unexpected skill dirs: %#v", state.Resolved.SkillDirs)
	}
	if !strings.Contains(state.PromptText, "# System\nSystem prompt.") {
		t.Fatalf("expected composed system prompt, got:\n%s", state.PromptText)
	}
	if !strings.Contains(state.PromptText, "cached profile memory") {
		t.Fatalf("expected profile memory in prompt, got:\n%s", state.PromptText)
	}
	if !strings.Contains(state.PromptText, "Profile investigation notes.") {
		t.Fatalf("expected profile notes in prompt, got:\n%s", state.PromptText)
	}
	if state.ContextValues == nil {
		t.Fatal("expected profile context values")
	}
	if state.ContextValues["profile_memory_path"] != filepath.Join(profileRoot, "agents", "coder", "memory", "memory.json") {
		t.Fatalf("unexpected profile memory path: %#v", state.ContextValues["profile_memory_path"])
	}
	if state.ToolPolicy == nil || !state.ToolPolicy.ReadOnly {
		t.Fatalf("expected read-only tool policy, got %#v", state.ToolPolicy)
	}
	if state.AgentSource != "profile" {
		t.Fatalf("expected profile agent source, got %q", state.AgentSource)
	}
	if strings.TrimSpace(state.AgentSourcePath) == "" {
		t.Fatal("expected profile agent source path (agent config or agent dir)")
	}
	if !strings.Contains(state.AgentSourcePath, filepath.Join("agents", "coder")) {
		t.Fatalf("expected agent source path under agents/coder, got %q", state.AgentSourcePath)
	}
	if err := state.ToolPolicy.AllowTool("read_file"); err != nil {
		t.Fatalf("expected read_file to be allowed, got %v", err)
	}
	if err := state.ToolPolicy.AllowTool("write_file"); err == nil {
		t.Fatal("expected write_file to be blocked")
	}

	opts := &chatCommandOptions{}
	applyProfileDefaultsToChatOptions(opts, state)
	if opts.ProviderFlag != "nvidia" {
		t.Fatalf("expected provider default nvidia, got %q", opts.ProviderFlag)
	}
	if opts.ModelFlag != "z-ai/glm4.7" {
		t.Fatalf("expected model default z-ai/glm4.7, got %q", opts.ModelFlag)
	}
	if opts.SessionDirFlag != filepath.Join(profileRoot, "agents", "coder", "sessions") {
		t.Fatalf("unexpected session dir default: %q", opts.SessionDirFlag)
	}
	if !opts.SessionFeaturesRequested {
		t.Fatal("expected session features requested to be enabled for profile mode")
	}
}

func TestResolveChatProfileState_AgentWithoutProfileUsesAgentdef(t *testing.T) {
	state, err := resolveChatProfileState(nil, &chatCommandOptions{AgentFlag: "explore"})
	if err != nil {
		t.Fatalf("resolveChatProfileState: %v", err)
	}
	if !state.Active() {
		t.Fatal("expected active agentdef state")
	}
	if state.Resolved.ProfileName != "agentdef" || state.Resolved.AgentID != "explore" {
		t.Fatalf("unexpected resolved agent: %+v", state.Resolved)
	}
	if state.Reference != "agentdef:explore" {
		t.Fatalf("unexpected reference: %q", state.Reference)
	}
	if strings.TrimSpace(state.AgentSourcePath) == "" {
		t.Fatal("expected agent source path from agentdef resolve")
	}
	if state.AgentSource != "builtin" && state.AgentSource != "project" {
		t.Fatalf("expected builtin or project agent source, got %q", state.AgentSource)
	}
	if !strings.Contains(state.PromptText, "read-only") && !strings.Contains(strings.ToLower(state.PromptText), "explorer") {
		t.Fatalf("expected explorer role prompt, got:\n%s", state.PromptText)
	}
	if state.ToolPolicy == nil || !state.ToolPolicy.ReadOnly {
		t.Fatalf("expected read-only explore tool policy, got %#v", state.ToolPolicy)
	}
	if err := state.ToolPolicy.AllowTool("view"); err != nil {
		t.Fatalf("expected view allowed: %v", err)
	}
	if err := state.ToolPolicy.AllowTool("write"); err == nil {
		t.Fatal("expected write blocked by explore denylist/read-only")
	}
	if state.PermissionMode != runtimepolicy.ModePlan {
		t.Fatalf("expected plan permission mode from explore, got %q", state.PermissionMode)
	}

	opts := &chatCommandOptions{PermissionMode: runtimepolicy.ModeDefault}
	applyProfileDefaultsToChatOptions(opts, state)
	if opts.PermissionMode != runtimepolicy.ModePlan {
		t.Fatalf("expected agentdef permission mode applied, got %q", opts.PermissionMode)
	}
	if !opts.SessionFeaturesRequested {
		t.Fatal("expected session features requested for agentdef mode")
	}

	// Explicit CLI permission mode wins.
	optsExplicit := &chatCommandOptions{
		PermissionMode:        runtimepolicy.ModeBypassPermissions,
		PermissionModeChanged: true,
	}
	applyProfileDefaultsToChatOptions(optsExplicit, state)
	if optsExplicit.PermissionMode != runtimepolicy.ModeBypassPermissions {
		t.Fatalf("expected explicit permission mode preserved, got %q", optsExplicit.PermissionMode)
	}
}

func TestResolveChatProfileState_UnknownAgentWithoutProfileErrors(t *testing.T) {
	_, err := resolveChatProfileState(nil, &chatCommandOptions{AgentFlag: "does-not-exist-agent"})
	if err == nil {
		t.Fatal("expected unknown agent to fail")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestResolveGlobalRuntimeConfigPath_NoLayerResolvesEmpty pins the policy: with
// no .aicli layer on disk chat uses the built-in defaults, so resolution must
// report "no runtime config" instead of handing callers a path inside the
// development directory.
func TestResolveGlobalRuntimeConfigPath_NoLayerResolvesEmpty(t *testing.T) {
	home := isolateInitHome(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(t.TempDir())

	if got := resolveGlobalRuntimeConfigPath(nil); got != "" {
		t.Fatalf("no-layer resolution = %q, want empty", got)
	}
	conventionCfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{ConfigFile: aiclipaths.DefaultRuntimeConfigRelativePath},
	}
	if got := resolveGlobalRuntimeConfigPath(conventionCfg); got != "" {
		t.Fatalf("convention value resolution = %q, want empty", got)
	}

	// A real override that does not exist keeps its path so the caller can still
	// report the misconfiguration.
	override := filepath.Join(t.TempDir(), "custom-runtime.yaml")
	overrideCfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{ConfigFile: override},
	}
	if got := resolveGlobalRuntimeConfigPath(overrideCfg); got != override {
		t.Fatalf("missing override = %q, want %q", got, override)
	}
}

// TestResolveGlobalRuntimeConfigPath_IgnoresDevelopmentDirectory pins the
// "backend/configs is a development directory" rule: a runtime.yaml there is
// never used implicitly, from the CWD upward search or from the executable
// directory, and a real .aicli layer still wins.
func TestResolveGlobalRuntimeConfigPath_IgnoresDevelopmentDirectory(t *testing.T) {
	home := isolateInitHome(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	repoDir := t.TempDir()
	writeTestFile(t, filepath.Join(repoDir, "backend", "configs", aiclipaths.DefaultRuntimeConfigFileName), "agent:\n  defaultModel: dev-only\n")
	writeTestFile(t, filepath.Join(repoDir, "configs", aiclipaths.DefaultRuntimeConfigFileName), "agent:\n  defaultModel: dev-only\n")
	t.Chdir(repoDir)

	// Even an executable sitting inside the development directory must not make
	// resolution walk up into it.
	originalExe := executablePathForTest
	executablePathForTest = filepath.Join(repoDir, "backend", "aicli-test-bin")
	writeTestFile(t, executablePathForTest, "#!/bin/sh\n")
	t.Cleanup(func() { executablePathForTest = originalExe })

	for name, cfg := range map[string]*config.Config{
		"backend/configs convention value": {SkillsRuntime: &config.SkillsRuntimeConfig{ConfigFile: "backend/configs/runtime.yaml"}},
		"configs convention value":         {SkillsRuntime: &config.SkillsRuntimeConfig{ConfigFile: "configs/runtime.yaml"}},
		"no skills runtime section":        nil,
	} {
		if got := resolveGlobalRuntimeConfigPath(cfg); got != "" {
			t.Fatalf("%s: development layout must not resolve, got %q", name, got)
		}
	}

	// A real .aicli layer still wins over the development file.
	projectConfig := filepath.Join(repoDir, ".aicli", aiclipaths.DefaultRuntimeConfigFileName)
	writeTestFile(t, projectConfig, "agent:\n  defaultModel: project\n")
	if got := resolveGlobalRuntimeConfigPath(nil); got != projectConfig {
		t.Fatalf("project layer = %q, want %q", got, projectConfig)
	}
}

// TestResolveGlobalRuntimeConfigPath_PrefersUserLayerOverNothing keeps the user
// layer reachable when the workspace has no .aicli directory of its own.
func TestResolveGlobalRuntimeConfigPath_PrefersUserLayerOverNothing(t *testing.T) {
	home := isolateInitHome(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	userConfig := filepath.Join(home, ".aicli", aiclipaths.DefaultRuntimeConfigFileName)
	writeTestFile(t, userConfig, "agent:\n  defaultModel: user\n")
	t.Chdir(t.TempDir())

	if got := resolveGlobalRuntimeConfigPath(nil); got != userConfig {
		t.Fatalf("user layer = %q, want %q", got, userConfig)
	}
}

func TestResolveChatSkillDirs_ResolvesUpwardRelativeSessionAndCLIPaths(t *testing.T) {
	root := t.TempDir()
	sessionSkillDir := filepath.Join(root, ".agents", "skills")
	cliSkillDir := filepath.Join(root, "team-skills")
	writeTestFile(t, filepath.Join(sessionSkillDir, "skill.yaml"), "name: test\ntriggers:\n  - type: keyword\n    values: [test]\n")
	mustMkdir(t, cliSkillDir)

	backendDir := filepath.Join(root, "backend")
	mustMkdir(t, backendDir)

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(backendDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	})

	session := &ChatSession{
		ResolvedSkillDirs: []string{"./.agents/skills"},
	}
	got := resolveChatSkillDirs(nil, session, []string{"./team-skills"})
	if len(got) != 2 {
		t.Fatalf("unexpected resolved dir count: %d (%#v)", len(got), got)
	}
	if got[0] != sessionSkillDir {
		t.Fatalf("unexpected resolved session dir: %q", got[0])
	}
	if got[1] != cliSkillDir {
		t.Fatalf("unexpected resolved cli dir: %q", got[1])
	}
}

func TestEnsureChatSystemPromptMessage_PrependsAndReplaces(t *testing.T) {
	session := &ChatSession{
		SystemPromptText: "Profile system prompt.",
	}
	replaceRuntimeMessages(session, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("hello"),
	})

	ensureChatSystemPromptMessage(session)
	if len(session.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(session.Messages))
	}
	expected := composeDurableChatSystemPromptWithGuidance(session)
	if session.Messages[0].Role != "system" || session.Messages[0].Content != expected {
		t.Fatalf("unexpected leading system message: %#v", session.Messages[0])
	}

	session.SystemPromptText = "Updated prompt."
	ensureChatSystemPromptMessage(session)
	if len(session.Messages) != 2 {
		t.Fatalf("expected no duplicate system message, got %d", len(session.Messages))
	}
	expected = composeDurableChatSystemPromptWithGuidance(session)
	if session.Messages[0].Content != expected {
		t.Fatalf("expected system prompt update, got %#v", session.Messages[0].Content)
	}
}

func TestComposeChatSystemPromptWithGuidance_IncludesParallelToolGuidance(t *testing.T) {
	session := &ChatSession{
		SystemPromptText: "Profile system prompt.",
	}

	prompt := composeChatSystemPromptWithGuidance(session)
	if !strings.Contains(prompt, "Environment context:") {
		t.Fatalf("expected environment context heading, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "<current_date>") || !strings.Contains(prompt, "<timezone>") {
		t.Fatalf("expected date and timezone context, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Parallel tool guidance:") {
		t.Fatalf("expected parallel tool guidance, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "same assistant turn") {
		t.Fatalf("expected batching guidance, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Task difficulty rating and subagent delegation policy:") {
		t.Fatalf("expected task difficulty guidance, got:\n%s", prompt)
	}
	if !strings.Contains(prompt, "include difficulty and difficulty_rationale for every child task") {
		t.Fatalf("expected subagent difficulty schema guidance, got:\n%s", prompt)
	}
}

// TestResolveGlobalRuntimeConfigPath_NeverReturnsMissingPath pins the fix for
// the misleading startup warning "未找到配置文件: backend/configs/runtime.yaml".
// Resolution must not hand callers a CWD-relative path that is then stat-ed and
// reported as a missing config. The result is either a real file from the .aicli
// layers (workspace ./.aicli > ~/.aicli) or "" meaning "no runtime.yaml, use the
// built-in defaults".
func TestResolveGlobalRuntimeConfigPath_NeverReturnsMissingPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	outside := t.TempDir()
	fakeExe := filepath.Join(outside, "bin", "aicli-test-bin")
	writeTestFile(t, fakeExe, "#!/bin/sh\n")
	t.Chdir(outside)

	originalExe := executablePathForTest
	executablePathForTest = fakeExe
	t.Cleanup(func() { executablePathForTest = originalExe })

	cfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{
			ConfigFile: filepath.FromSlash(filepath.Join("backend", "configs", aiclipaths.DefaultRuntimeConfigFileName)),
		},
	}

	for name, got := range map[string]string{
		"configured convention path": resolveGlobalRuntimeConfigPath(cfg),
		"no skills runtime section":  resolveGlobalRuntimeConfigPath(nil),
	} {
		if got == "" {
			continue
		}
		if !filepath.IsAbs(got) {
			t.Fatalf("%s: resolved runtime config must be absolute, got %q", name, got)
		}
		if _, err := os.Stat(got); err != nil {
			t.Fatalf("%s: resolved runtime config must exist, got %q: %v", name, got, err)
		}
	}
}

// TestResolveGlobalRuntimeConfigPath_PrefersWorkspaceDotAICLI keeps the layer
// order explicit: the workspace ./.aicli/runtime.yaml wins, and a
// backend/configs/runtime.yaml next to it is ignored (development directory).
func TestResolveGlobalRuntimeConfigPath_PrefersWorkspaceDotAICLI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	workspace := t.TempDir()
	workspaceConfig := filepath.Join(workspace, ".aicli", aiclipaths.DefaultRuntimeConfigFileName)
	writeTestFile(t, workspaceConfig, "agent:\n  defaultModel: workspace\n")
	writeTestFile(t, filepath.Join(workspace, "backend", "configs", aiclipaths.DefaultRuntimeConfigFileName), "agent:\n  defaultModel: portable\n")
	t.Chdir(workspace)

	cfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{ConfigFile: "backend/configs/runtime.yaml"},
	}
	if got := resolveGlobalRuntimeConfigPath(cfg); got != workspaceConfig {
		t.Fatalf("expected workspace .aicli config %q, got %q", workspaceConfig, got)
	}
}

// TestLoadRuntimeToolConfig_MissingConventionPathStaysSilent verifies a missing
// optional runtime.yaml no longer prints the runtime tools warning: chat
// silently uses the built-in defaults instead.
func TestLoadRuntimeToolConfig_MissingConventionPathStaysSilent(t *testing.T) {
	resetChatRuntimeConfigCacheForTest()
	t.Cleanup(resetChatRuntimeConfigCacheForTest)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	outside := t.TempDir()
	fakeExe := filepath.Join(outside, "bin", "aicli-test-bin")
	writeTestFile(t, fakeExe, "#!/bin/sh\n")
	t.Chdir(outside)

	originalExe := executablePathForTest
	executablePathForTest = fakeExe
	t.Cleanup(func() { executablePathForTest = originalExe })

	cfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{ConfigFile: "backend/configs/runtime.yaml"},
	}

	var cfgLoaded bool
	stderr := captureStderr(t, func() {
		cfgLoaded = loadRuntimeToolConfig(cfg, nil) != nil
	})
	if !cfgLoaded {
		t.Fatal("expected a runtime config (defaults or a real file)")
	}
	if strings.Contains(stderr, "加载 runtime tools 配置失败") {
		t.Fatalf("missing convention runtime.yaml must not warn, got %q", stderr)
	}
}

func writeTestFile(t *testing.T, path string, contents string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestResolveChatProfileState_PrecedenceFlagBeatsConfigDefault 锁定 FR-6/D10 的
// 优先级契约：--profile flag > config.profiles.default_profile（DEFAULT_PROFILE
// env 经配置层进入同一字段）> 无 profile（全量，零变化）。
func TestResolveChatProfileState_PrecedenceFlagBeatsConfigDefault(t *testing.T) {
	profilesRoot := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		profileRoot := filepath.Join(profilesRoot, name)
		writeTestFile(t, filepath.Join(profileRoot, "profile.yaml"), fmt.Sprintf(`profile:
  name: %s
  default_agent: default
`, name))
		writeTestFile(t, filepath.Join(profileRoot, "agents", "default", "agent.yaml"), "name: default\ndescription: precedence fixture\n")
	}
	cfg := &config.Config{Profiles: &config.ProfilesConfig{Root: profilesRoot, DefaultProfile: "alpha"}}

	// 1) 仅 config.default_profile 生效：解析到 alpha。
	state, err := resolveChatProfileState(cfg, &chatCommandOptions{})
	if err != nil {
		t.Fatalf("default_profile resolve: %v", err)
	}
	if !state.Active() || state.Resolved.ProfileName != "alpha" {
		t.Fatalf("expected default_profile alpha, got %+v", state)
	}

	// 2) flag 覆盖 config.default_profile：解析到 beta。
	state, err = resolveChatProfileState(cfg, &chatCommandOptions{ProfileFlag: "beta"})
	if err != nil {
		t.Fatalf("flag resolve: %v", err)
	}
	if !state.Active() || state.Resolved.ProfileName != "beta" {
		t.Fatalf("expected flag profile beta, got %+v", state)
	}

	// 3) 两者皆无：不激活（无 profile = 全量，行为零变化）。
	state, err = resolveChatProfileState(&config.Config{}, &chatCommandOptions{})
	if err != nil {
		t.Fatalf("no-profile resolve: %v", err)
	}
	if state.Active() {
		t.Fatalf("expected inactive profile state, got %+v", state)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}
