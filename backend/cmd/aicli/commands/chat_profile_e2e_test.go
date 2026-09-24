package commands

import (
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/foldertrust"
)

const e2e6PromptMarker = "E2E6-PROJECT-PROMPT-MARKER"

// E2E-6（Batch 14 / D29，P0 安全线）端到端验收：未信任仓库（含 `.aicli/profiles/`
// 与 prompts）启动 → 裁剪生效、prompts 未应用、警告出现；`/trust grant` 后同一会话
// 恢复注入（A14）。
//
// 与 slice 2 单测的差别：这里走**真实判定链**，即 foldertrust.Resolve 的 marker 扫描
// （V21 修复点：`.aicli/profiles` 必须让 RepoConfigsPresent=true，否则 Decide 第 4 步
// 直接放行=假门控）+ 真实信任存储（AICLI_HOME 隔离，无记录）+ resolveChatProfileState
// 解析与门控 + applyProfileStateToChatSession 投影 + composeDurableChatSystemPrompt
// 最终提示词组合。任何一环漏接都会让断言失败。
func TestE2E6UntrustedWorkspaceProfileGate(t *testing.T) {
	// 隔离信任存储：AICLI_HOME 指向临时目录 → 该工作区无任何信任记录（未信任）。
	t.Setenv("AICLI_HOME", t.TempDir())
	t.Setenv(foldertrust.EnvFolderTrust, "1")
	resetProcessFolderTrust()
	t.Cleanup(resetProcessFolderTrust)

	workspace := t.TempDir()
	profilesRoot := filepath.Join(workspace, ".aicli", "profiles")
	profileRoot := filepath.Join(profilesRoot, "e2e6")
	writeTestFile(t, filepath.Join(profileRoot, "profile.yaml"), "profile:\n  name: e2e6\n  default_agent: coder\n")
	writeTestFile(t, filepath.Join(profileRoot, "agents", "coder", "prompts", "system.md"), e2e6PromptMarker)
	writeTestFile(t, filepath.Join(profileRoot, "agents", "coder", "tools", "policy.yaml"), "denylist: [write_file]\n")

	// ① 真实判定链（V21）：只带 `.aicli/profiles/` 的仓库必须被判为未信任。
	res := foldertrust.Resolve(foldertrust.ResolveOptions{CWD: workspace, SkipPrompt: true})
	if !res.FeatureEnabled {
		t.Fatalf("folder trust feature must be enabled in this test, got %+v", res)
	}
	if res.Trusted || res.Outcome != foldertrust.OutcomeUntrusted {
		t.Fatalf("workspace with .aicli/profiles must resolve untrusted, got %+v", res)
	}
	setProcessFolderTrust(res)

	cfg := &config.Config{Profiles: &config.ProfilesConfig{Root: profilesRoot}}
	session := &ChatSession{Config: cfg}
	state, err := resolveChatProfileState(cfg, &chatCommandOptions{ProfileFlag: "e2e6"})
	if err != nil {
		t.Fatalf("resolveChatProfileState: %v", err)
	}
	if !applyProfileStateToChatSession(session, state) {
		t.Fatal("expected profile surface projection")
	}
	// 与 chat_setup.go:260 同序：trust 在 profile 投影之后挂载（门控读进程级结论）。
	applyChatFolderTrust(session, currentFolderTrust())

	// ② prompts 未应用（A13 半程）：最终组合的系统提示词不含 profile 文本。
	if prompt := composeDurableChatSystemPromptWithGuidanceForCWD(session, workspace); strings.Contains(prompt, e2e6PromptMarker) {
		t.Fatalf("untrusted workspace must not inject the project profile prompt:\n%s", prompt)
	}

	// ③ 裁剪生效（A13 另一半）：分级门控不误伤 tools——denylist 声明保留且真实执行
	// 策略拒绝被 deny 的工具（session.ToolPolicy 即执行器消费的策略，buildLocalChatToolPolicy
	// 以 Clone 消费）。
	if session.ToolPolicy == nil {
		t.Fatal("graded gate must keep the profile tool policy")
	}
	if err := session.ToolPolicy.AllowTool("write_file"); err == nil {
		t.Fatal("denied tool must stay blocked by the profile policy")
	}
	if err := session.ToolPolicy.AllowTool("read_file"); err != nil {
		t.Fatalf("unlisted tool must stay allowed (denylist-only policy), got %v", err)
	}

	// ④ 警告出现（E2E-6 的"检查警告"步骤）。
	if notice := profilePromptSuppressionNotice(session); !strings.Contains(notice, "未信任") {
		t.Fatalf("notice must explain the untrusted workspace, got %q", notice)
	}
	if status := chatProfileStatusText(session); !strings.Contains(status, "未应用") {
		t.Fatalf("status must warn about withheld prompts:\n%s", status)
	}

	// ⑤ A14：`/trust grant`（持久化）→ 重解析 → 同一会话重新投影 → prompts 恢复注入。
	if _, err := foldertrust.GrantTrust(workspace); err != nil {
		t.Fatalf("GrantTrust: %v", err)
	}
	res = foldertrust.Resolve(foldertrust.ResolveOptions{CWD: workspace, SkipPrompt: true})
	if !res.Trusted {
		t.Fatalf("granted workspace must resolve trusted, got %+v", res)
	}
	setProcessFolderTrust(res)

	state, err = resolveChatProfileState(cfg, &chatCommandOptions{ProfileFlag: "e2e6"})
	if err != nil {
		t.Fatalf("resolveChatProfileState (granted): %v", err)
	}
	if !applyProfileStateToChatSession(session, state) {
		t.Fatal("expected profile surface projection after grant")
	}
	if prompt := composeDurableChatSystemPromptWithGuidanceForCWD(session, workspace); !strings.Contains(prompt, e2e6PromptMarker) {
		t.Fatalf("granted workspace must inject the project profile prompt:\n%s", prompt)
	}
	if session.ProfilePromptSuppressed {
		t.Fatal("granted session must not stay marked as suppressed")
	}
}

// E2E-7（Batch 14 / R18）resume 漂移容错：会话绑定 profile X → X 被删除 → resume 该
// 会话必须给出警告且会话可用（不崩、不静默降级：绑定字段保留，下一次 sync 不会误删
// sessionmeta 中的 profile_ref）。
func TestE2E7ResumedProfileDriftKeepsSessionUsable(t *testing.T) {
	profilesRoot := filepath.Join(t.TempDir(), ".aicli", "profiles")
	cfg := &config.Config{Profiles: &config.ProfilesConfig{Root: profilesRoot}}
	session := &ChatSession{
		Config:           cfg,
		ProfileReference: "user:gone",
		ProfileName:      "gone",
		ProfileAgent:     "coder",
	}

	stderr := captureStderr(t, func() { chatReapplyResumedProfileState(session, "gone") })
	if !strings.Contains(stderr, "解析失败") || !strings.Contains(stderr, "/profile reload") {
		t.Fatalf("resume drift must warn with the recovery path, got %q", stderr)
	}
	// 会话可用 + 绑定不丢（R18）：身份字段保留、生效面退回基线而非半截状态。
	if session.ProfileReference != "user:gone" || session.ProfileName != "gone" || session.ProfileAgent != "coder" {
		t.Fatalf("resume drift must keep the binding identity, got %+v", session)
	}
	if session.SystemPromptText != "" {
		t.Fatalf("failed reapply must not half-apply a prompt, got %q", session.SystemPromptText)
	}
}
