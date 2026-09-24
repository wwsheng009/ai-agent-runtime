package commands

import (
	"strings"
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeprompt "github.com/wwsheng009/ai-agent-runtime/internal/prompt"
)

const profilePromptMarker = "PROFILE-SYSTEM-PROMPT-MARKER"

func newProfilePromptTestSession(mode string) *ChatSession {
	session := &ChatSession{
		SystemPromptText:  profilePromptMarker,
		ProfilePromptMode: mode,
		RuntimeSession:    runtimechat.NewSession("tester"),
	}
	storeSessionEnvironmentSnapshot(session, runtimeprompt.EnvironmentSnapshot{
		ContextBlock:       "<environment_context>\n  <cwd>profile-test</cwd>\n</environment_context>",
		CapabilityGuidance: "frozen capability",
	})
	return session
}

// FR-7 默认 replace：profile 文本保持既有位置（内置环境上下文之前），
// 且「未设置模式」与「显式 replace」必须逐字节一致（NFR-1 零变化）。
func TestComposeDurablePrompt_ProfileReplaceKeepsLegacyOrder(t *testing.T) {
	legacy := newProfilePromptTestSession("")
	explicit := newProfilePromptTestSession("replace")

	legacyPrompt := composeDurableChatSystemPromptWithGuidanceForCWD(legacy, `E:\projects\demo`)
	explicitPrompt := composeDurableChatSystemPromptWithGuidanceForCWD(explicit, `E:\projects\demo`)

	if legacyPrompt != explicitPrompt {
		t.Fatalf("empty mode must be byte-identical to explicit replace mode\nempty:\n%s\nexplicit:\n%s", legacyPrompt, explicitPrompt)
	}
	profileAt := strings.Index(legacyPrompt, profilePromptMarker)
	envAt := strings.Index(legacyPrompt, "Environment context:")
	if profileAt < 0 || envAt < 0 || profileAt > envAt {
		t.Fatalf("replace mode must keep profile prompt before built-in guidance, got:\n%s", legacyPrompt)
	}
}

// FR-7 append：profile 文本叠加在内置基础提示（agentguidance 等）之后。
func TestComposeDurablePrompt_ProfileAppendRunsAfterBuiltInGuidance(t *testing.T) {
	replacePrompt := composeDurableChatSystemPromptWithGuidanceForCWD(newProfilePromptTestSession(""), `E:\projects\demo`)
	appendPrompt := composeDurableChatSystemPromptWithGuidanceForCWD(newProfilePromptTestSession("append"), `E:\projects\demo`)

	profileAt := strings.Index(appendPrompt, profilePromptMarker)
	envAt := strings.Index(appendPrompt, "Environment context:")
	if envAt < 0 || profileAt < 0 || profileAt < envAt {
		t.Fatalf("append mode must place profile prompt after built-in guidance, got:\n%s", appendPrompt)
	}
	if !strings.HasSuffix(appendPrompt, profilePromptMarker) {
		t.Fatalf("append mode must place profile prompt last, got:\n%s", appendPrompt)
	}
	if strings.Count(appendPrompt, profilePromptMarker) != 1 {
		t.Fatalf("profile prompt must appear exactly once, got:\n%s", appendPrompt)
	}
	// 两种模式的差异只允许是 profile 文本的位置：剥离 profile 段后内容一致。
	stripped := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(appendPrompt), profilePromptMarker))
	expected := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(replacePrompt), profilePromptMarker))
	if stripped != expected {
		t.Fatalf("append mode must only relocate the profile prompt\nappend-minus-profile:\n%s\nreplace-minus-profile:\n%s", stripped, expected)
	}
}

// 未知模式不得猜测：归一化后按 replace 处理（错误不猜 + 只收窄）。
func TestComposeDurablePrompt_UnknownModeFallsBackToReplace(t *testing.T) {
	replacePrompt := composeDurableChatSystemPromptWithGuidanceForCWD(newProfilePromptTestSession("replace"), `E:\projects\demo`)
	unknownPrompt := composeDurableChatSystemPromptWithGuidanceForCWD(newProfilePromptTestSession("bogus-mode"), `E:\projects\demo`)
	if unknownPrompt != replacePrompt {
		t.Fatalf("unknown prompt mode must behave as replace\nunknown:\n%s\nreplace:\n%s", unknownPrompt, replacePrompt)
	}
}
