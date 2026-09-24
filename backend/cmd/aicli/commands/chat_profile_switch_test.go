package commands

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// writeProfileSwitchFixture 生成一个最小可解析的 profile 目录：
// <root>/<name>/profile.yaml + agents/<agent>/prompts/system.md + tools/policy.yaml。
func writeProfileSwitchFixture(t *testing.T, profilesRoot, name, profileYAML, promptText, toolPolicyYAML string) {
	t.Helper()
	profileRoot := filepath.Join(profilesRoot, name)
	writeTestFile(t, filepath.Join(profileRoot, "profile.yaml"), profileYAML)
	writeTestFile(t, filepath.Join(profileRoot, "runtime.yaml"), "agent:\n  defaultModel: fixture\n")
	writeTestFile(t, filepath.Join(profileRoot, "mcp.yaml"), "mcp_servers: {}\n")
	if strings.TrimSpace(promptText) != "" {
		writeTestFile(t, filepath.Join(profileRoot, "agents", "coder", "prompts", "system.md"), promptText)
	}
	if strings.TrimSpace(toolPolicyYAML) != "" {
		writeTestFile(t, filepath.Join(profileRoot, "agents", "coder", "tools", "policy.yaml"), toolPolicyYAML)
	}
	mustMkdir(t, filepath.Join(profileRoot, "agents", "coder", "sessions"))
}

// newProfileSwitchTestSession 构造带 Config（profile 注册表）与持久化管理器的会话。
func newProfileSwitchTestSession(t *testing.T, profilesRoot string) (*ChatSession, func()) {
	t.Helper()
	session, cleanup := newGoalCommandTestSession(t)
	session.Config = &config.Config{
		Profiles: &config.ProfilesConfig{Root: profilesRoot},
		AICLI: &config.AICLIConfig{
			MCP: &config.AICLIMCPConfig{ConfigFile: filepath.Join("configs", "mcp.yaml")},
		},
		SkillsRuntime: &config.SkillsRuntimeConfig{Enabled: true, SkillDir: t.TempDir()},
	}
	session.SessionUserID = "tester"
	return session, cleanup
}

// A1 + D23：切换后会话工具面等于新 profile 声明，且报告记录工具面差异。
func TestProfileSwitch_ToolSurfaceMatchesDeclaredPolicy(t *testing.T) {
	profilesRoot := t.TempDir()
	writeProfileSwitchFixture(t, profilesRoot, "coding", `profile:
  name: coding
  default_agent: coder
agents:
  coder:
    model: fixture
`, "Coding prompt.", `allowlist: [read_file, grep]
denylist: [write_file]
`)
	writeProfileSwitchFixture(t, profilesRoot, "minimal", `profile:
  name: minimal
  default_agent: coder
agents:
  coder:
    model: fixture
`, "Minimal prompt.", `allowlist: [read_file]
`)

	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()

	first, err := applyRuntimeProfileSwitch(session, "coding")
	if err != nil {
		t.Fatalf("switch to coding: %v", err)
	}
	if first.To == "" || first.EffectiveAt != profileSwitchEffectiveAt {
		t.Fatalf("unexpected report head: %+v", first)
	}
	if got := session.ToolPolicy.AllowedToolNames(); !reflect.DeepEqual(sortedCopy(got), []string{"grep", "read_file"}) {
		t.Fatalf("unexpected coding allowlist: %#v", got)
	}

	// 稳定工具面缓存先预热，切换后必须被清空（②）。
	session.stableSharedToolSessionID = currentRuntimeSessionID(session)
	session.stableSharedToolSelection = &aicliFunctionSelection{Mode: "fixture"}
	session.ContextWindowTokenCount = 4096

	report, err := applyRuntimeProfileSwitch(session, "minimal")
	if err != nil {
		t.Fatalf("switch to minimal: %v", err)
	}
	if got := session.ToolPolicy.AllowedToolNames(); !reflect.DeepEqual(sortedCopy(got), []string{"read_file"}) {
		t.Fatalf("unexpected minimal allowlist: %#v", got)
	}
	if !reflect.DeepEqual(report.Changed.ToolsRemoved, []string{"grep"}) {
		t.Fatalf("expected grep removal in report, got %#v", report.Changed.ToolsRemoved)
	}
	if len(report.Changed.ToolsAdded) != 0 {
		t.Fatalf("expected no tool additions, got %#v", report.Changed.ToolsAdded)
	}
	if session.stableSharedToolSelection != nil || session.stableSharedToolSessionID != "" {
		t.Fatal("expected stable shared tool surface reset")
	}
	if session.ContextWindowTokenCount != 0 || !report.ContextTokenCountReset {
		t.Fatalf("expected context token count reset, got %d", session.ContextWindowTokenCount)
	}
	// 无运行时宿主时工具面失效退化为 none（不谎报已失效）。
	if report.ToolSurfaceScope != profileSwitchSurfaceScopeNone || report.ToolSurfaceInvalidated {
		t.Fatalf("unexpected tool surface scope: %q/%v", report.ToolSurfaceScope, report.ToolSurfaceInvalidated)
	}
	if !strings.Contains(report.CacheNotice, "下一轮起生效") {
		t.Fatalf("expected cache notice, got %q", report.CacheNotice)
	}
}

// A2 + A4：切换删除 prompt 锚点（下一轮按新 profile 重组 head），身份写入
// sessionmeta 并随 sync 持久化（resume 后仍是新 profile）。
func TestProfileSwitch_SystemPromptRebuiltFromNewProfile(t *testing.T) {
	profilesRoot := t.TempDir()
	writeProfileSwitchFixture(t, profilesRoot, "dev", `profile:
  name: dev
  default_agent: coder
agents:
  coder:
    model: fixture
`, "Dev prompt body.", `allowlist: [read_file]
`)

	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()

	session.SystemPromptText = "OLD HEAD"
	storeFrozenChatSystemPrompt(session.RuntimeSession, session, "OLD HEAD")
	if got := loadFrozenChatSystemPrompt(session.RuntimeSession, session); got != "OLD HEAD" {
		t.Fatalf("anchor setup failed: %q", got)
	}

	report, err := applyRuntimeProfileSwitch(session, "dev")
	if err != nil {
		t.Fatalf("switch to dev: %v", err)
	}
	if !report.AnchorCleared {
		t.Fatal("expected frozen prompt anchor cleared")
	}
	if got := loadFrozenChatSystemPrompt(session.RuntimeSession, session); got != "" {
		t.Fatalf("expected anchor removed, got %q", got)
	}
	if !strings.Contains(session.SystemPromptText, "Dev prompt body.") {
		t.Fatalf("expected new profile prompt, got %q", session.SystemPromptText)
	}
	if !report.Changed.PromptChanged {
		t.Fatal("expected prompt_changed=true in report")
	}

	ctx := frozenChatSystemPromptContext(session.RuntimeSession, session)
	if got := sessionmeta.String(ctx, sessionmeta.ProfileName); got != "dev" {
		t.Fatalf("expected profile_name=dev, got %q", got)
	}
	if got := sessionmeta.String(ctx, sessionmeta.ProfileAgent); got != "coder" {
		t.Fatalf("expected profile_agent=coder, got %q", got)
	}

	// A4：持久化后重新加载仍是新 profile（resume 语义）。
	loaded, err := session.SessionManager.GetStorage().Load(context.Background(), session.RuntimeSession.ID)
	if err != nil {
		t.Fatalf("reload runtime session: %v", err)
	}
	if got := sessionmeta.String(loaded.Metadata.Context, sessionmeta.ProfileName); got != "dev" {
		t.Fatalf("expected persisted profile_name=dev, got %q", got)
	}
	if got := sessionmeta.String(loaded.Metadata.Context, sessionmeta.ProfileRef); got == "" {
		t.Fatal("expected persisted profile_ref")
	}
}

// A3：切换语义是"下一 turn 生效"。本层断言切换不触碰在途 turn 的既有前缀
// （消息、路由、模型一律不动）；在途 turn 的 FrozenTurnTools 由存储层在
// CurrentTurnID != "" 时保留（session_runtime_store.go:555-590，
// turn_tool_surface_snapshot_test.go 已锁定），端到端剧本在 Batch 11a 覆盖。
func TestProfileSwitch_InFlightTurnPrefixStable(t *testing.T) {
	profilesRoot := t.TempDir()
	writeProfileSwitchFixture(t, profilesRoot, "dev", `profile:
  name: dev
  default_agent: coder
agents:
  coder:
    model: fixture
`, "Dev prompt.", `allowlist: [read_file]
`)

	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()

	session.ProviderName = "openai"
	session.Model = "gpt-4o"
	session.BaseURL = "https://example.invalid/v1"
	beforeMessages := append([]runtimetypes.Message(nil), session.Messages...)
	beforeProvider, beforeModel, beforeBaseURL := session.ProviderName, session.Model, session.BaseURL

	report, err := applyRuntimeProfileSwitch(session, "dev")
	if err != nil {
		t.Fatalf("switch to dev: %v", err)
	}
	if len(session.Messages) != len(beforeMessages) {
		t.Fatalf("in-flight message window must not grow during a switch: %d → %d", len(beforeMessages), len(session.Messages))
	}
	if len(beforeMessages) > 1 && !reflect.DeepEqual(session.Messages[1:], beforeMessages[1:]) {
		t.Fatal("in-flight conversation tail must not change during a profile switch")
	}
	if session.ProviderName != beforeProvider || session.Model != beforeModel || session.BaseURL != beforeBaseURL {
		t.Fatalf("in-flight route must not change: %s/%s/%s", session.ProviderName, session.Model, session.BaseURL)
	}
	// 无运行时宿主 → 不存在在途 actor；报告必须如实标注（不谎报在途 turn）。
	if report.InFlightTurn {
		t.Fatal("expected in_flight_turn=false without a runtime host")
	}
}

// A5 + D30：profile 声明的 provider/model 不隐式覆盖会话的显式选择，只进报告告警。
func TestProfileSwitch_ExplicitSelectionWins(t *testing.T) {
	profilesRoot := t.TempDir()
	writeProfileSwitchFixture(t, profilesRoot, "nvidia-dev", `profile:
  name: nvidia-dev
  default_agent: coder
providers:
  default_provider: nvidia
agents:
  coder:
    model: z-ai/glm4.7
`, "Nvidia prompt.", `allowlist: [read_file]
`)

	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()

	session.ProviderName = "openai"
	session.Model = "gpt-4o"
	report, err := applyRuntimeProfileSwitch(session, "nvidia-dev")
	if err != nil {
		t.Fatalf("switch to nvidia-dev: %v", err)
	}
	if session.ProviderName != "openai" || session.Model != "gpt-4o" {
		t.Fatalf("explicit selection must win, got %s/%s", session.ProviderName, session.Model)
	}
	if report.Changed.ProviderChanged || report.Changed.ModelChanged {
		t.Fatalf("provider/model must not be applied implicitly: %+v", report.Changed)
	}
	joined := strings.Join(report.Warnings, "\n")
	if !strings.Contains(joined, "/provider") || !strings.Contains(joined, "/model") {
		t.Fatalf("expected deferred provider/model warnings, got %q", joined)
	}
}

// A6：切换失败时锚点、工具面、sessionmeta 全部不变（原子性）。
func TestProfileSwitch_FailureLeavesStateUntouched(t *testing.T) {
	profilesRoot := t.TempDir()
	writeProfileSwitchFixture(t, profilesRoot, "coding", `profile:
  name: coding
  default_agent: coder
agents:
  coder:
    model: fixture
`, "Coding prompt.", `allowlist: [read_file, grep]
`)

	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()

	if _, err := applyRuntimeProfileSwitch(session, "coding"); err != nil {
		t.Fatalf("switch to coding: %v", err)
	}
	storeFrozenChatSystemPrompt(session.RuntimeSession, session, "FROZEN-HEAD")
	session.stableSharedToolSessionID = currentRuntimeSessionID(session)
	session.stableSharedToolSelection = &aicliFunctionSelection{Mode: "fixture"}
	session.ContextWindowTokenCount = 1024
	beforePolicy := append([]string(nil), session.ToolPolicy.AllowedToolNames()...)
	beforePrompt := session.SystemPromptText
	beforeName := sessionmeta.String(frozenChatSystemPromptContext(session.RuntimeSession, session), sessionmeta.ProfileName)

	if _, err := applyRuntimeProfileSwitch(session, "does-not-exist"); err == nil {
		t.Fatal("expected unknown profile to fail")
	}

	if got := loadFrozenChatSystemPrompt(session.RuntimeSession, session); got != "FROZEN-HEAD" {
		t.Fatalf("anchor must survive a failed switch, got %q", got)
	}
	if session.stableSharedToolSelection == nil || session.stableSharedToolSessionID == "" {
		t.Fatal("stable tool surface must survive a failed switch")
	}
	if session.ContextWindowTokenCount != 1024 {
		t.Fatalf("context token count must survive a failed switch, got %d", session.ContextWindowTokenCount)
	}
	if !reflect.DeepEqual(sortedCopy(session.ToolPolicy.AllowedToolNames()), sortedCopy(beforePolicy)) {
		t.Fatalf("tool policy must survive a failed switch: %#v", session.ToolPolicy.AllowedToolNames())
	}
	if session.SystemPromptText != beforePrompt {
		t.Fatalf("prompt must survive a failed switch: %q", session.SystemPromptText)
	}
	if got := sessionmeta.String(frozenChatSystemPromptContext(session.RuntimeSession, session), sessionmeta.ProfileName); got != beforeName {
		t.Fatalf("profile identity must survive a failed switch: %q", got)
	}
}

// sortedCopy 返回排序后的副本，便于比较集合语义（顺序无关）。
func sortedCopy(values []string) []string {
	return normalizeStringSet(values)
}

// A8：同一 turn 内连续两次切换合并为最后一次——最终状态与"直接一次切换到目标
// profile"完全一致，中间态不残留（锚点/稳定工具面/token 计数按最后一次失效）。
func TestProfileSwitch_RapidSwitchCoalesces(t *testing.T) {
	profilesRoot := t.TempDir()
	writeProfileSwitchFixture(t, profilesRoot, "coding", `profile:
  name: coding
  default_agent: coder
agents:
  coder:
    model: fixture
`, "Coding prompt.", `allowlist: [read_file, grep]
`)
	writeProfileSwitchFixture(t, profilesRoot, "minimal", `profile:
  name: minimal
  default_agent: coder
agents:
  coder:
    model: fixture
`, "Minimal prompt.", `allowlist: [read_file]
`)

	rapid, cleanupRapid := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanupRapid()

	if _, err := applyRuntimeProfileSwitch(rapid, "coding"); err != nil {
		t.Fatalf("first switch: %v", err)
	}
	// 中间态痕迹：prompt 锚点 + 稳定工具面缓存 + token 计数；第二次切换必须按
	// "最后一次"失效，而不是把中间态当成基线。
	storeFrozenChatSystemPrompt(rapid.RuntimeSession, rapid, "MIDDLE-HEAD")
	rapid.stableSharedToolSessionID = currentRuntimeSessionID(rapid)
	rapid.stableSharedToolSelection = &aicliFunctionSelection{Mode: "middle"}
	rapid.ContextWindowTokenCount = 2048

	second, err := applyRuntimeProfileSwitch(rapid, "minimal")
	if err != nil {
		t.Fatalf("second switch: %v", err)
	}
	if second.From != "coding" || second.To != "minimal" {
		t.Fatalf("second report must chain from the first switch: %+v", second)
	}

	direct, cleanupDirect := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanupDirect()
	if _, err := applyRuntimeProfileSwitch(direct, "minimal"); err != nil {
		t.Fatalf("direct switch: %v", err)
	}

	// 合并结果 == 一次直接切换：工具面/身份/提示词/失效痕迹全部一致。
	if got, want := sortedCopy(rapid.ToolPolicy.AllowedToolNames()), sortedCopy(direct.ToolPolicy.AllowedToolNames()); !reflect.DeepEqual(got, want) {
		t.Fatalf("coalesced tool surface %#v != direct %#v", got, want)
	}
	if rapid.ProfileReference != direct.ProfileReference || rapid.ProfileName != direct.ProfileName {
		t.Fatalf("coalesced identity %q/%q != direct %q/%q",
			rapid.ProfileReference, rapid.ProfileName, direct.ProfileReference, direct.ProfileName)
	}
	if strings.Contains(rapid.SystemPromptText, "Coding prompt.") || !strings.Contains(rapid.SystemPromptText, "Minimal prompt.") {
		t.Fatalf("coalesced prompt must be the last profile's: %q", rapid.SystemPromptText)
	}
	if got := loadFrozenChatSystemPrompt(rapid.RuntimeSession, rapid); got != "" {
		t.Fatalf("anchor must be cleared by the last switch, got %q", got)
	}
	if rapid.stableSharedToolSelection != nil || rapid.stableSharedToolSessionID != "" {
		t.Fatal("stable tool surface must be invalidated by the last switch")
	}
	if rapid.ContextWindowTokenCount != 0 {
		t.Fatalf("token count must be reset by the last switch, got %d", rapid.ContextWindowTokenCount)
	}
}

// A4（resume 半程）：切换后 resume，身份仍为新 profile——`/profile status` 展示该
// 绑定、工具面按 profile 收窄（不回落基线）、提示词来自 profile，且下一次 sync
// 不会删除持久化键（否则 resume 一次就把绑定丢掉）。
func TestProfileSwitch_IdentityPersistsAcrossResume(t *testing.T) {
	profilesRoot := t.TempDir()
	writeProfileSwitchFixture(t, profilesRoot, "dev", `profile:
  name: dev
  default_agent: coder
agents:
  coder:
    model: fixture
`, "Dev prompt body.", `allowlist: [read_file]
`)

	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()
	if _, err := applyRuntimeProfileSwitch(session, "dev"); err != nil {
		t.Fatalf("switch to dev: %v", err)
	}
	loaded, err := session.SessionManager.GetStorage().Load(context.Background(), session.RuntimeSession.ID)
	if err != nil {
		t.Fatalf("reload runtime session: %v", err)
	}

	// resume：新进程只拿到持久化会话（与 chat_exit_resume_repro_test.go 同法）。
	resumed := &ChatSession{
		Config:         session.Config,
		SessionManager: session.SessionManager,
		SessionUserID:  session.SessionUserID,
	}
	if err := restoreChatStateFromRuntimeSession(resumed, loaded); err != nil {
		t.Fatalf("resume restore: %v", err)
	}
	if resumed.ProfileReference != "dev" || resumed.ProfileName != "dev" {
		t.Fatalf("resume 必须回填 profile 身份，got ref=%q name=%q", resumed.ProfileReference, resumed.ProfileName)
	}
	text, _ := chatProfileCommandText(resumed, "/profile status")
	if !strings.Contains(text, "profile: dev") {
		t.Fatalf("resume 后 status 应展示绑定的 profile，got: %s", text)
	}
	if got := sortedCopy(resumed.ToolPolicy.AllowedToolNames()); !reflect.DeepEqual(got, []string{"read_file"}) {
		t.Fatalf("resume 后工具面必须按 profile 收窄，got %#v", got)
	}
	if !strings.Contains(resumed.SystemPromptText, "Dev prompt body.") {
		t.Fatalf("resume 后提示词必须来自 profile，got %q", resumed.SystemPromptText)
	}

	if err := syncRuntimeSessionFromChat(resumed); err != nil {
		t.Fatalf("sync after resume: %v", err)
	}
	again, err := resumed.SessionManager.GetStorage().Load(context.Background(), resumed.RuntimeSession.ID)
	if err != nil {
		t.Fatalf("reload after resume sync: %v", err)
	}
	if got := sessionmeta.String(again.Metadata.Context, sessionmeta.ProfileRef); got != "dev" {
		t.Fatalf("resume 后 sync 必须保留 profile_ref，got %q", got)
	}
}
