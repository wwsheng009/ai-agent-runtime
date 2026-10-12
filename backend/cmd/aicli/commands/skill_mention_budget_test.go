package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// $mention 注入通路专项：文本技能正文超过 mention_inject_max_chars 时按单技能
// 字符上限截断并打标；回合 token 预算耗尽时整条降级（skipped），而不是静默注入
// 半截正文。该通路与 /skill 的函数加载路径相互独立，本测试锁定其大小上限与
// 降级行为（此前只有代码路径、没有专项测试）。
func TestSkillMentionInjectionTruncatesAndDegrades(t *testing.T) {
	body := strings.Repeat("m", 400)
	skill := mentionTestSkill("big-text", body, `C:\skills\big-text\SKILL.md`)
	cfg := &config.SkillsRuntimeConfig{
		MentionInjection:        config.SkillMentionInjectionOn,
		MentionInjectMaxChars:   120,
		MentionInjectTotalChars: 4096,
	}
	session, _ := mentionTestSession(t, cfg, skill)

	messages, diag := buildSkillMentionTurnMessages(context.Background(), session, skillMentionTurnInput{
		Prompt: "use $big-text", Interactive: true,
	})
	require.Len(t, messages, 1)
	require.Contains(t, messages[0].Content, skillMentionSingleTruncateMarker)
	require.Len(t, diag.Injected, 1)
	require.True(t, diag.Injected[0].Truncated)
	require.Less(t, diag.Injected[0].Chars, runeCountString(body))

	// 回合 token 预算耗尽：整条降级为 skipped（不注入），而不是注入被截断的正文。
	degraded, degradedDiag := buildSkillMentionTurnMessages(context.Background(), session, skillMentionTurnInput{
		Prompt: "use $big-text", Interactive: true,
		BudgetTokens: 100, UsedTokens: 100,
	})
	require.Empty(t, degraded)
	require.Len(t, degradedDiag.Skipped, 1)
	require.Equal(t, skillMentionSkipLimit, degradedDiag.Skipped[0].Reason)
}
