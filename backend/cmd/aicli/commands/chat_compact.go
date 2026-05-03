package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/compactruntime"
)

type chatCompactReport struct {
	RequestedMode string
	Result        *compactruntime.Result
	Status        compactruntime.Status
}

const compactTokenSourceObservedUsage = "observed_usage"

var runManualChatCompact = executeManualChatCompact

func executeManualChatCompact(session *ChatSession, requestedMode string) (*chatCompactReport, error) {
	if session == nil {
		return nil, fmt.Errorf("当前没有活动会话")
	}
	if session.RuntimeSession == nil {
		return nil, fmt.Errorf("当前没有可压缩的持久化会话")
	}
	if session.LocalRuntimeHost == nil || session.LocalRuntimeHost.SessionHub == nil {
		return nil, fmt.Errorf("当前会话未初始化本地 runtime host，无法执行 /compact")
	}

	actor, err := session.LocalRuntimeHost.SessionHub.GetOrCreate(session.RuntimeSession.ID)
	if err != nil {
		return nil, err
	}

	ctx := session.cancelCtx
	if ctx == nil {
		ctx = context.Background()
	}
	result, status, err := actor.Compact(ctx, requestedMode)
	if err != nil {
		return &chatCompactReport{
			RequestedMode: requestedMode,
			Result:        result,
			Status:        status,
		}, err
	}
	if syncErr := syncRuntimeSessionBackIntoCLI(session); syncErr != nil {
		return &chatCompactReport{
			RequestedMode: requestedMode,
			Result:        result,
			Status:        status,
		}, syncErr
	}
	applyChatCompactContextUsage(session, result, status, true)
	if syncErr := syncRuntimeSessionFromChat(session); syncErr != nil {
		return &chatCompactReport{
			RequestedMode: requestedMode,
			Result:        result,
			Status:        status,
		}, syncErr
	}
	return &chatCompactReport{
		RequestedMode: requestedMode,
		Result:        result,
		Status:        status,
	}, nil
}

func normalizeChatCompactMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", compactruntime.ModeAuto:
		return compactruntime.ModeAuto, nil
	case compactruntime.ModeLocal:
		return compactruntime.ModeLocal, nil
	case compactruntime.ModeRemote:
		return compactruntime.ModeRemote, nil
	default:
		return "", fmt.Errorf("无效的 compact mode: %s（可选值: auto|local|remote）", value)
	}
}

func formatChatCompactReport(report *chatCompactReport) string {
	if report == nil {
		return "压缩未执行"
	}
	mode := strings.TrimSpace(report.Status.Mode)
	if mode == "" {
		mode = strings.TrimSpace(report.RequestedMode)
	}
	if mode == "" {
		mode = compactruntime.ModeAuto
	}

	if report.Result == nil {
		parts := []string{
			fmt.Sprintf("压缩未执行: %s", formatCompactSkipReason(report.Status)),
			fmt.Sprintf("mode=%s", mode),
		}
		if report.Status.ResolvedProvider != "" {
			parts = append(parts, "provider="+report.Status.ResolvedProvider)
		}
		if report.Status.ResolvedModel != "" {
			parts = append(parts, "model="+report.Status.ResolvedModel)
		}
		if report.Status.TokenBefore > 0 {
			parts = append(parts, fmt.Sprintf("token_before=%d", report.Status.TokenBefore))
			parts = append(parts, "token_source="+compactTokenSourceObservedUsage)
		}
		if report.Status.TriggerTokenLimit > 0 {
			parts = append(parts, fmt.Sprintf("trigger_token_limit=%d", report.Status.TriggerTokenLimit))
		}
		return strings.Join(parts, " | ")
	}

	parts := []string{
		"压缩完成",
		fmt.Sprintf("mode=%s", report.Result.Mode),
	}
	if report.Result.ResolvedProvider != "" {
		parts = append(parts, "provider="+report.Result.ResolvedProvider)
	}
	if report.Result.ResolvedModel != "" {
		parts = append(parts, "model="+report.Result.ResolvedModel)
	}
	parts = append(parts,
		fmt.Sprintf("token_before=%d", report.Result.TokenBefore),
		fmt.Sprintf("token_after=%d", report.Result.TokenAfter),
		"token_source="+compactTokenSourceObservedUsage,
		fmt.Sprintf("compacted_messages=%d", report.Result.CompactedMessages),
		fmt.Sprintf("history_messages=%d", len(report.Result.ReplacementHistory)),
	)
	if len(report.Result.CheckpointIDs) > 0 {
		parts = append(parts, "checkpoint_id="+report.Result.CheckpointIDs[len(report.Result.CheckpointIDs)-1])
	}
	return strings.Join(parts, " | ")
}

func formatCompactSkipReason(status compactruntime.Status) string {
	switch strings.TrimSpace(status.Reason) {
	case "missing_model_capability":
		return "reason=missing_model_capability; " + compactCapabilityHint(status)
	default:
		return "reason=" + blankToDash(status.Reason)
	}
}

func compactCapabilityHint(status compactruntime.Status) string {
	provider := strings.TrimSpace(status.ResolvedProvider)
	model := strings.TrimSpace(status.ResolvedModel)

	targetPath := "`providers.items.<provider>.model_capabilities.<model>`"
	wildcardPath := "`providers.items.<provider>.model_capabilities.*`"
	switch {
	case provider != "" && model != "":
		targetPath = fmt.Sprintf("`providers.items.%s.model_capabilities.%s`", provider, model)
		wildcardPath = fmt.Sprintf("`providers.items.%s.model_capabilities.*`", provider)
	case provider != "":
		targetPath = fmt.Sprintf("`providers.items.%s.model_capabilities.<model>`", provider)
		wildcardPath = fmt.Sprintf("`providers.items.%s.model_capabilities.*`", provider)
	}

	hint := fmt.Sprintf("需要配置 %s 或 %s，至少补 `max_context_tokens` / `auto_compact_token_limit`", targetPath, wildcardPath)
	if strings.EqualFold(strings.TrimSpace(status.Mode), compactruntime.ModeRemote) {
		hint += "；如需远端压缩，再补 `supports_remote_compact: true` 或 `auto_compact_mode: remote`"
	}
	return hint
}
