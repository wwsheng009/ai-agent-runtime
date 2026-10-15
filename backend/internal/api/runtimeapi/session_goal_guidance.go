package runtimeapi

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// applyRuntimeServerGoalGuidance 在每次 run 前把当前会话的 active goal 渲染成
// 逐轮指引写入 agent options（key = runtimegoal.GuidanceOptionKey），与 CLI 本地
// 路径（cmd/aicli/commands 的 renderActiveGoalGuidance + localChatPrepareRunHook）
// 同口径。
//
// 关键点：goal 从 **run 时的实时会话** 读取（run 前钩子传入的 *chat.Session），
// 因此 /goal 的 set/pause/resume/clear/complete 无需重建 actor 即可在下一轮
// 生效；而 agent 只在 actor 构建期装配一次，所以注入必须发生在这里，而不是
// buildSessionAgentForContext。
//
// 失败语义是 fail-open：无 goal / 非 active / objective 为空时清除该 option，
// goal 元数据损坏时只记日志并降级为无指引，绝不让 goal 读取失败终止 turn。
func (h *Handler) applyRuntimeServerGoalGuidance(apiAgent *agent.Agent, session *chat.Session) {
	if apiAgent == nil || session == nil {
		return
	}
	cfg := apiAgent.GetConfig()
	if cfg == nil {
		return
	}

	guidance := ""
	goal, ok, err := runtimegoal.NewMetadataStore().Get(session)
	if err != nil {
		logger.Warnf("runtime api: read session goal for turn guidance: %s", err)
	} else if ok && goal != nil {
		canUpdateGoal := h.runtimeServerGoalToolsAvailable(context.Background(), session.ID)
		guidance = strings.TrimSpace(runtimegoal.RenderTurnGuidance(goal, canUpdateGoal))
	}

	if guidance == "" {
		if cfg.Options != nil {
			delete(cfg.Options, runtimegoal.GuidanceOptionKey)
		}
		return
	}
	if cfg.Options == nil {
		cfg.Options = make(map[string]interface{})
	}
	cfg.Options[runtimegoal.GuidanceOptionKey] = guidance
}
