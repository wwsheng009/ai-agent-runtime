package commands

import (
	"fmt"
	"strings"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// acpReasoningEffortDefaultValue is the synthetic value id ACP clients send to
// clear the session override so the provider default applies again. ACP select
// options always need a currentValue drawn from the advertised choices, so the
// "provider default" state must be selectable explicitly.
const acpReasoningEffortDefaultValue = "default"

// applyRuntimeReasoningEffortSwitch 把活动会话的 reasoning_effort 切换为 raw，
// 复用交互式 /reasoning_effort 相同的解析与落地路径
// （resolveChatReasoningEffort + syncRuntimeSessionFromChat +
// refreshLocalRuntimeAfterModelSelection），从下一个 turn 生效。
//
// 与 applyRuntimeModelSwitch / applyRuntimeProviderSwitch 一致，这里不写回全局
// 偏好（避免 ACP 客户端会话隐式修改用户配置文件），由调用方决定是否持久化。
// raw 为空或 acpReasoningEffortDefaultValue 表示清除会话覆盖，交由 provider 默认值。
//
// 返回值是落地后的 effort（清除语义下为空字符串）。
func applyRuntimeReasoningEffortSwitch(session *ChatSession, raw string) (string, error) {
	if session == nil {
		return "", fmt.Errorf("当前没有活动会话")
	}
	requested := strings.TrimSpace(raw)
	if strings.EqualFold(requested, acpReasoningEffortDefaultValue) {
		requested = ""
	}

	resolved := runtimetypes.NormalizeReasoningEffort(requested)
	if resolved != "" {
		// resolveChatReasoningEffort keeps the model-card catalog out of
		// validation, so it currently never reports a warning; there is nothing
		// to forward to the terminal here.
		value, _, err := resolveChatReasoningEffort(session.Provider, effectiveRuntimeModel(session), resolved, false)
		if err != nil {
			return "", err
		}
		resolved = value
	}

	session.ReasoningEffort = resolved
	session.RequestedReasoningEffort = resolved
	session.EffectiveReasoningEffort = resolved
	warnIfChatSessionSyncFails(session, "switch reasoning_effort", syncRuntimeSessionFromChat(session))
	if err := refreshLocalRuntimeAfterModelSelection(session); err != nil {
		warnIfChatSessionSyncFails(session, "refresh local runtime after reasoning_effort switch", err)
	}
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
	return resolved, nil
}
