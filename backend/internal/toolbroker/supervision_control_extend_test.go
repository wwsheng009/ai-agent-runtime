package toolbroker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 建议稿 §5.2 / doc 6.5：extend_deadline 在监督层早已存在——allowed_actions 对
// agent-run 行宣告它、LocalControlService.Control 接受并透传载荷、ActionService
// 在 execute 时复核 I5 预算与 I6 不可逆点。缺的只是模型面：枚举里没有它、参数
// 解析不认载荷，于是父代理面对"还在推进的长任务"只能杀或重派。本组用例钉住
// 工具面四件事：枚举可发现、载荷可解析、请求形状先判、非 extend 动作不接受载荷。
func TestControlDescendantExtendDeadlinePayload(t *testing.T) {
	parsed, err := parseControlDescendantArgs(map[string]interface{}{
		"notification_id": "n-agent_run-abc",
		"action":          "extend_deadline",
		"reason":          "child still making progress",
		"extend_by_ms":    float64(30 * 60 * 1000),
		"extend_which":    "progress",
	})
	require.NoError(t, err)
	require.Equal(t, 30*time.Minute, parsed.ExtendBy)
	require.Nil(t, parsed.NewDeadline)
	require.Equal(t, "progress", parsed.ExtendWhich)

	parsed, err = parseControlDescendantArgs(map[string]interface{}{
		"notification_id": "n-agent_run-abc",
		"action":          "extend_deadline",
		"reason":          "one more window",
		"new_deadline":    "2026-09-28T18:00:00Z",
	})
	require.NoError(t, err)
	require.Zero(t, parsed.ExtendBy)
	require.NotNil(t, parsed.NewDeadline)
	require.Equal(t, "2026-09-28T18:00:00Z", parsed.NewDeadline.UTC().Format(time.RFC3339))
	require.Empty(t, parsed.ExtendWhich, "empty extend_which means execution at the supervision layer")

	// 形状先判：两者都给 / 都不给 / extend_which 非法 / 非 extend 动作带载荷，
	// 都在解析层拒绝——一次必然被监督层拒绝的动作不应先落审计行。
	both := map[string]interface{}{
		"notification_id": "n-agent_run-abc",
		"action":          "extend_deadline",
		"reason":          "both payloads",
		"extend_by_ms":    float64(60000),
		"new_deadline":    "2026-09-28T18:00:00Z",
	}
	_, err = parseControlDescendantArgs(both)
	require.ErrorContains(t, err, "exactly one of extend_by_ms or new_deadline")

	neither := map[string]interface{}{
		"notification_id": "n-agent_run-abc",
		"action":          "extend_deadline",
		"reason":          "no payload",
	}
	_, err = parseControlDescendantArgs(neither)
	require.ErrorContains(t, err, "exactly one of extend_by_ms or new_deadline")

	badWhich := map[string]interface{}{
		"notification_id": "n-agent_run-abc",
		"action":          "extend_deadline",
		"reason":          "bad which",
		"extend_by_ms":    float64(60000),
		"extend_which":    "wall_clock",
	}
	_, err = parseControlDescendantArgs(badWhich)
	require.ErrorContains(t, err, "extend_which must be execution|progress|both")

	misplaced := map[string]interface{}{
		"notification_id": "n-agent_run-abc",
		"action":          "cancel",
		"reason":          "done",
		"extend_by_ms":    float64(60000),
	}
	_, err = parseControlDescendantArgs(misplaced)
	require.ErrorContains(t, err, "only valid with action=extend_deadline")
}

// TestControlDescendantAdvertisesExtendDeadline 钉住可发现性：模型只能从 schema
// 得知有这条路，枚举与载荷字段缺一不可（历史症状：allowed_actions 宣告了动作，
// 模型却规划不出调用）。
func TestControlDescendantAdvertisesExtendDeadline(t *testing.T) {
	var definition map[string]interface{}
	for _, def := range supervisionToolDefinitions() {
		if def.Name != ToolControlDescendant {
			continue
		}
		properties, ok := def.Parameters["properties"].(map[string]interface{})
		require.True(t, ok, "subagent_control properties missing")
		definition = properties
		break
	}
	require.NotNil(t, definition, "subagent_control definition missing")

	action, ok := definition["action"].(map[string]interface{})
	require.True(t, ok, "action property missing")
	require.Contains(t, action["enum"], "extend_deadline",
		"extend_deadline must be advertised, otherwise the parent can only kill or re-dispatch")

	for _, key := range []string{"extend_by_ms", "new_deadline", "extend_which"} {
		require.Contains(t, definition, key, "extend payload property %s must be advertised", key)
	}
}
