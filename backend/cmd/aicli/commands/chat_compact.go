package commands

import (
	"context"
	"fmt"
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/compactruntime"
)

type chatCompactReport struct {
	RequestedMode string
	Result        *compactruntime.Result
	Status        compactruntime.Status
	// Lineage fields are filled after a successful compact when the runtime
	// session still carries compact title metadata (generation / root title).
	Generation int
	Title      string
	RootTitle  string
	// HistoryMessages 是服务端 compact 结果里替换历史的条数（server 模式）。
	// CLI 不重建消息体，仅保真展示；本地路径保持 0，展示以 Result 为准。
	HistoryMessages int
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

	ctx := session.cancelCtx
	if ctx == nil {
		ctx = context.Background()
	}
	if session.LocalRuntimeHost == nil || session.LocalRuntimeHost.SessionHub == nil {
		// runtime-server 模式：本地没有 actor，/compact 走远端 compact 命令
		//（与 interrupt 同一命令通道）。服务端返回的 status/result 会映射回
		// 本地报告结构，供 /compact 渲染与 token 统计复用。
		if exec, ok := session.ChatExecutor.(aicliRuntimeCompactExecutor); ok {
			return exec.CompactSession(ctx, session, requestedMode)
		}
		return nil, fmt.Errorf("当前会话未初始化本地 runtime host，无法执行 /compact")
	}
	actor, err := chatActorForSession(ctx, session)
	if err != nil {
		return nil, err
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
	report := &chatCompactReport{
		RequestedMode: requestedMode,
		Result:        result,
		Status:        status,
	}
	attachChatCompactLineage(report, session)
	return report, nil
}

// chatCompactLineageSnapshot 是压缩血缘在锁内取出的字段快照。
type chatCompactLineageSnapshot struct {
	generation    int
	hasGeneration bool
	rootTitle     string
	title         string
}

// readChatCompactLineageSnapshot 在 runtimeContextMapMu 读锁下执行（由
// chatRuntimeSessionRead 保证），只读。BuildPreview 依赖历史，故这里不能退化成
// chatRuntimeSessionSnapshot（无历史会静默读到空标题）。
func readChatCompactLineageSnapshot(runtimeSession *runtimechat.Session) chatCompactLineageSnapshot {
	snapshot := chatCompactLineageSnapshot{
		rootTitle: strings.TrimSpace(runtimeSessionContextString(runtimeSession, runtimechat.ContextCompactRootTitle)),
	}
	if generation, ok := runtimeSessionContextInt(runtimeSession, runtimechat.ContextCompactGeneration); ok && generation > 0 {
		snapshot.generation = generation
		snapshot.hasGeneration = true
	}
	title := strings.TrimSpace(runtimeSession.Metadata.Title)
	if title == "" {
		if preview := runtimeSession.BuildPreview(); preview != nil {
			title = strings.TrimSpace(preview.Title)
		}
	}
	snapshot.title = title
	return snapshot
}

func attachChatCompactLineage(report *chatCompactReport, session *ChatSession) {
	if report == nil || session == nil {
		return
	}
	lineage, ok := chatRuntimeSessionRead(session, readChatCompactLineageSnapshot)
	if !ok {
		return
	}
	if lineage.hasGeneration {
		report.Generation = lineage.generation
	}
	if lineage.rootTitle != "" {
		report.RootTitle = lineage.rootTitle
	}
	if lineage.title != "" {
		report.Title = lineage.title
	}
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
	historyMessages := len(report.Result.ReplacementHistory)
	if historyMessages == 0 && report.HistoryMessages > 0 {
		historyMessages = report.HistoryMessages
	}
	parts = append(parts,
		fmt.Sprintf("token_before=%d", report.Result.TokenBefore),
		fmt.Sprintf("token_after=%d", report.Result.TokenAfter),
		"token_source="+compactTokenSourceObservedUsage,
		fmt.Sprintf("compacted_messages=%d", report.Result.CompactedMessages),
		fmt.Sprintf("history_messages=%d", historyMessages),
	)
	if len(report.Result.CheckpointIDs) > 0 {
		parts = append(parts, "checkpoint_id="+report.Result.CheckpointIDs[len(report.Result.CheckpointIDs)-1])
	}
	if reconciliation := report.Result.Reconciliation; reconciliation != nil {
		parts = append(parts, fmt.Sprintf("context_drift=%d", reconciliation.DriftCount))
		if reconciliation.CorrectionMade {
			parts = append(parts, "correction=applied")
		}
	}
	if report.Generation > 0 {
		parts = append(parts, fmt.Sprintf("generation=%d", report.Generation))
	}
	if report.Title != "" {
		parts = append(parts, "title="+report.Title)
	} else if report.RootTitle != "" {
		// Fallback when title is empty but root lineage was recorded.
		parts = append(parts, "root_title="+report.RootTitle)
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

	hint := fmt.Sprintf("需要配置 %s 或 %s，至少补 `max_context_tokens` / `auto_compact_token_limit`（或 `auto_compact_ratio`，默认 0.8）", targetPath, wildcardPath)
	if strings.EqualFold(strings.TrimSpace(status.Mode), compactruntime.ModeRemote) {
		hint += "；如需远端压缩，再补 `supports_remote_compact: true` 或 `auto_compact_mode: remote`"
	}
	return hint
}
