package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolnames"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type aicliActorChatExecutor struct{}

func newAICLIActorChatExecutor() aicliChatExecutor {
	return &aicliActorChatExecutor{}
}

func (e *aicliActorChatExecutor) Execute(ctx context.Context, session *ChatSession, prompt string) (string, error) {
	if session == nil {
		return "", fmt.Errorf("chat session is nil")
	}
	if session.LocalRuntimeHost == nil || session.LocalRuntimeHost.SessionHub == nil {
		return "", fmt.Errorf("local runtime host is not configured")
	}
	if session.RuntimeSession == nil {
		return "", fmt.Errorf("runtime session is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = generatedImageToolContext(ctx, session)

	actor, err := session.LocalRuntimeHost.SessionHub.GetOrCreate(session.RuntimeSession.ID)
	if err != nil {
		return "", err
	}
	previousAssistant := latestAssistantResponseText(session)
	previousTeamID := activeTeamID(session)
	if bridge := ensureChatRuntimeEventBridge(session); bridge != nil {
		bridge.PrepareRunPrompt(prompt)
		bridge.BeginRun()
		defer bridge.EndRun()
	}
	if session.runtimeHTTPCapture != nil {
		session.runtimeHTTPCapture.Reset()
	}
	if reporter := newRuntimeHTTPDebugReporter(session); reporter != nil {
		ctx = runtimellm.WithHTTPDebugReporter(ctx, reporter)
	}
	result, err := actor.SubmitPrompt(ctx, prompt, currentRunMetaForSession(session), runtimechat.SubmitPromptOption{
		ImagePaths:       session.ImagePaths,
		ImageArtifactDir: chatSessionImageArtifactDir(session),
	})
	if err != nil {
		warnIfChatSessionSyncFails(session, "actor error sync", syncRuntimeSessionBackIntoCLI(session))
		warnIfChatSessionSyncFails(session, "actor error team lifecycle sync", syncAmbientTeamLifecycleState(session))
		if response, ok := attemptDirectImageGenerationFallback(ctx, session, prompt, err.Error()); ok {
			return response, nil
		}
		return "", humanizeActorExecutorError(session, err)
	}
	if bridge := session.RuntimeEventBridge; bridge != nil {
		waitTimeout := 500 * time.Millisecond
		if session.Interaction != nil && result != nil {
			waitTimeout = session.Interaction.EstimateStreamFlushTimeout(result.Output)
		}
		if waitTimeout < 8*time.Second {
			waitTimeout = 8 * time.Second
		}
		bridge.WaitForCurrentEvents(waitTimeout)
		if runErr := bridge.RunError(); runErr != nil {
			warnIfChatSessionSyncFails(session, "actor runtime error sync", syncRuntimeSessionBackIntoCLI(session))
			warnIfChatSessionSyncFails(session, "actor runtime error team lifecycle sync", syncAmbientTeamLifecycleState(session))
			return "", humanizeActorExecutorError(session, runErr)
		}
	}
	if err := syncRuntimeSessionBackIntoCLI(session); err != nil {
		return "", err
	}
	if result != nil {
		applyChatTokenUsage(session, result.Usage)
	}
	warnIfChatSessionSyncFails(session, "actor usage sync", syncRuntimeSessionFromChat(session))
	warnIfChatSessionSyncFails(session, "actor team lifecycle sync", syncAmbientTeamLifecycleState(session))
	if result == nil {
		return "", nil
	}
	renderAsyncTeamLaunchNotice(session, previousTeamID)
	response := resolveActorExecutorResponse(result.Output, session, previousAssistant)
	if response == "" {
		if fallbackResponse, ok := attemptDirectImageGenerationFallback(ctx, session, prompt, result.Error); ok {
			return fallbackResponse, nil
		}
		response = fallbackActorExecutorResponse(result, session)
	}
	return response, nil
}

func humanizeActorExecutorError(session *ChatSession, err error) error {
	if err == nil {
		return nil
	}
	if preflightErr, ok := agent.AsPromptPreflightError(err); ok && preflightErr != nil {
		message := fmt.Sprintf(
			"本次请求在发送给模型前已被本地拦截：上下文过大（prompt tokens=%d，budget=%d）。",
			preflightErr.PromptTokens,
			preflightErr.PromptBudget,
		)
		if strings.TrimSpace(preflightErr.Reason) != "" {
			message += " 原因：" + strings.TrimSpace(preflightErr.Reason) + "。"
		}
		if strings.TrimSpace(preflightErr.SuggestedAction) != "" {
			message += " 建议：" + strings.TrimSpace(preflightErr.SuggestedAction)
		}
		if preflightErr.ReplacementHistoryApplied {
			message += " 当前会话已自动保存压缩后的上下文，可直接继续下一轮。"
		} else if len(preflightErr.CloneReplacementHistory()) > 0 {
			message += " 当前失败已生成一份更紧凑的恢复上下文。"
		}
		meta := make([]string, 0, 3)
		if strings.TrimSpace(preflightErr.ResolvedProvider) != "" {
			meta = append(meta, "provider="+strings.TrimSpace(preflightErr.ResolvedProvider))
		}
		if strings.TrimSpace(preflightErr.ResolvedModel) != "" {
			meta = append(meta, "model="+strings.TrimSpace(preflightErr.ResolvedModel))
		}
		if strings.TrimSpace(preflightErr.BudgetSource) != "" {
			meta = append(meta, "budget_source="+strings.TrimSpace(preflightErr.BudgetSource))
		}
		if len(meta) > 0 {
			message += " [" + strings.Join(meta, " ") + "]"
		}
		return fmt.Errorf("%s", message)
	}
	if strings.Contains(err.Error(), "upstream model returned an empty reply: no text and no tool calls") {
		lower := strings.ToLower(err.Error())
		message := "上游模型返回了空回复：既没有文本，也没有发起工具调用；请重试，或调整提示词/切换模型后再试"
		switch {
		case strings.Contains(lower, "content_inspection_failed"):
			message = "上游内容审核拦截了这次请求：输入或输出包含不符合策略的内容；请改写为更安全的表达后再试"
		case strings.Contains(lower, "quota_exhausted"):
			message = "上游额度已用尽或配额不足，当前请求无法继续；请检查模型额度后再试"
		case strings.Contains(lower, "rate_limit"):
			message = "上游触发了限流，当前请求暂时无法稳定完成；请稍后重试"
		case strings.Contains(lower, "stream_interrupted"):
			message = "上游流式响应中断，可能是网络波动或服务端临时异常；请稍后重试"
		case strings.Contains(lower, "reasoning_only_empty_reply"):
			message = "上游只返回了思考过程，没有最终正文；请重试或调整提示词"
		case strings.Contains(lower, "empty_reply"):
			message = "上游模型返回了真正的空回复：没有正文，也没有工具调用；请重试或切换模型"
		}
		if session != nil && session.runtimeHTTPCapture != nil {
			snapshot := session.runtimeHTTPCapture.Snapshot()
			details := make([]string, 0, 4)
			if snapshot.Source != "" || snapshot.Provider != "" || snapshot.Protocol != "" || snapshot.Model != "" {
				meta := []string{}
				if snapshot.Source != "" {
					meta = append(meta, "source="+snapshot.Source)
				}
				if snapshot.Provider != "" {
					meta = append(meta, "provider="+snapshot.Provider)
				}
				if snapshot.Protocol != "" {
					meta = append(meta, "protocol="+snapshot.Protocol)
				}
				if snapshot.Model != "" {
					meta = append(meta, "model="+snapshot.Model)
				}
				if len(meta) > 0 {
					details = append(details, strings.Join(meta, " "))
				}
			}
			if snapshot.ResponseStatus > 0 {
				details = append(details, fmt.Sprintf("status=%d", snapshot.ResponseStatus))
			}
			if snapshot.ErrorText != "" {
				details = append(details, "http_error="+snapshot.ErrorText)
			}
			if snapshot.RequestArtifactPath != "" {
				details = append(details, "request_artifact="+resolveAbsoluteChatPath(snapshot.RequestArtifactPath))
			}
			if snapshot.ResponseArtifactPath != "" {
				details = append(details, "response_artifact="+resolveAbsoluteChatPath(snapshot.ResponseArtifactPath))
			}
			if snapshot.ResponsePreview != "" {
				details = append(details, "response_preview="+truncateUTF8Bytes(strings.TrimSpace(snapshot.ResponsePreview), 512))
			}
			if len(details) > 0 {
				message += " 最近一次响应诊断：" + strings.Join(details, " | ")
			}
		}
		return fmt.Errorf("%s", message)
	}
	return err
}

func currentRunMetaForSession(session *ChatSession) *team.RunMeta {
	if session == nil {
		return nil
	}
	runMeta := &team.RunMeta{}
	if session.PermissionMode != "" {
		runMeta.PermissionMode = string(session.PermissionMode)
	}
	binding := resolvedInteractiveTeamBinding(session)
	if binding != nil && strings.TrimSpace(binding.TeamID) != "" && shouldPropagateTeamRunMeta(session, binding) {
		runMeta.Team = &team.TeamRunMeta{
			TeamID:        strings.TrimSpace(binding.TeamID),
			AgentID:       firstNonEmptyChatValue(binding.AgentID, "lead"),
			CurrentTaskID: strings.TrimSpace(binding.TaskID),
		}
	}
	if strings.TrimSpace(runMeta.PermissionMode) == "" && runMeta.Team == nil {
		return nil
	}
	return runMeta
}

func shouldPropagateTeamRunMeta(session *ChatSession, binding *chatTeamBinding) bool {
	if binding == nil || strings.TrimSpace(binding.TeamID) == "" {
		return false
	}
	if interactiveTeamPendingByTeamID(session, binding.TeamID) {
		return true
	}
	if session == nil || session.ActiveTeam == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(session.ActiveTeam.TeamID), strings.TrimSpace(binding.TeamID)) {
		return false
	}
	return session.LocalRuntimeHost == nil || session.LocalRuntimeHost.TeamStore == nil
}

func syncRuntimeSessionBackIntoCLI(session *ChatSession) error {
	if session == nil || session.SessionManager == nil || session.RuntimeSession == nil {
		return nil
	}
	previousContextWindowTokens := session.ContextWindowTokenCount
	runtimeSession, err := session.SessionManager.Get(context.Background(), session.RuntimeSession.ID)
	if err != nil {
		return err
	}
	if runtimeSession == nil {
		return nil
	}
	if err := restoreChatStateFromRuntimeSession(session, runtimeSession); err != nil {
		return err
	}
	inferAmbientTeamBinding(session, runtimeSession)
	if session.LocalRuntimeHost != nil {
		validateAmbientTeamBinding(session, session.LocalRuntimeHost.TeamStore)
	}
	refreshChatContextTokenSnapshotFromMessages(session, previousContextWindowTokens, true)
	return syncRuntimeSessionFromChat(session)
}

func resolveActorExecutorResponse(output string, session *ChatSession, previousAssistant string) string {
	output = strings.TrimSpace(output)
	if output != "" {
		return output
	}
	current := latestAssistantResponseText(session)
	if current == "" || current == strings.TrimSpace(previousAssistant) {
		return ""
	}
	return current
}

func fallbackActorExecutorResponse(result *agent.Result, session *ChatSession) string {
	if result == nil {
		return ""
	}
	if result.Success || strings.TrimSpace(result.Output) != "" {
		return ""
	}
	errText := strings.TrimSpace(result.Error)
	lines := []string{"这次处理没有生成后续回复。"}
	if paths := recentGeneratedImageArtifactPaths(session); len(paths) > 0 {
		if len(paths) == 1 {
			lines = append(lines, "但已检测到生成图片已保存: "+paths[0])
		} else {
			lines = append(lines, fmt.Sprintf("但已检测到 %d 张生成图片已保存，最新一张: %s", len(paths), paths[0]))
		}
	}
	if errText == "" {
		lines = append(lines, "请根据上面的信息重试，或调整请求后再试。")
		return strings.Join(lines, "\n")
	}
	lines = append(lines, "原因: "+truncateChatRuntimeText(errText, 240))
	lines = append(lines, "请根据上面的信息重试，或调整请求后再试。")
	return strings.Join(lines, "\n")
}

func attemptDirectImageGenerationFallback(ctx context.Context, session *ChatSession, prompt string, failure string) (string, bool) {
	if !shouldAttemptDirectImageGenerationFallback(session, prompt, failure) {
		return "", false
	}
	report, err := executeDirectImageGenerationFallback(ctx, session, prompt)
	if err != nil {
		writeSessionDebugInfo(session, fmt.Sprintf("[image-fallback] direct image generation failed after actor failure: %v", err), true)
		return "", false
	}
	persistDirectImageGenerationFallbackAssistant(session, report)
	output := strings.TrimSpace(report.Output)
	if output == "" {
		output = formatDirectFunctionInvokeReport(report, false)
	}
	if output == "" {
		return "", false
	}
	return strings.Join([]string{
		"主聊天模型响应失败，已直接改用图片生成工具完成请求。",
		output,
	}, "\n\n"), true
}

func shouldAttemptDirectImageGenerationFallback(session *ChatSession, prompt string, failure string) bool {
	if strings.TrimSpace(prompt) == "" || !promptLooksLikeImageGenerationIntent(prompt) {
		return false
	}
	if len(recentGeneratedImageArtifactPaths(session)) > 0 {
		return false
	}
	normalizedFailure := strings.ToLower(strings.TrimSpace(failure))
	if normalizedFailure == "" {
		return true
	}
	for _, blocked := range []string{
		"content_inspection",
		"content_filter",
		"moderation",
		"policy",
		"safety",
		"forbidden",
		"http 400",
		"http 401",
		"http 403",
	} {
		if strings.Contains(normalizedFailure, blocked) {
			return false
		}
	}
	for _, transient := range []string{
		"http 5",
		"502",
		"503",
		"504",
		"timeout",
		"timed out",
		"stream disconnected",
		"stream_interrupted",
		"internal_server_error",
		"server_error",
		"cdn",
		"源服务器超时",
	} {
		if strings.Contains(normalizedFailure, transient) {
			return true
		}
	}
	return false
}

func executeDirectImageGenerationFallback(ctx context.Context, session *ChatSession, prompt string) (*directFunctionInvokeReport, error) {
	resolvedName, _, err := resolveDirectCallableFunctionName(session, toolnames.OpenAIImageGenerateToolName, false)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = generatedImageToolContext(ctx, session)
	args := map[string]interface{}{"prompt": prompt}
	if session != nil && session.Logger != nil {
		session.Logger.LogToolCall(aicliLogScope{TurnID: "image-fallback", RequestID: "image-fallback-req-01"}, "direct-image-fallback", resolvedName, args)
	}
	catalog := ensureFunctionCatalog(session)
	if catalog == nil || catalog.Registry() == nil {
		return nil, fmt.Errorf("function registry 未初始化")
	}
	output, metadata, err := catalog.Registry().ExecuteFunctionWithMeta(ctx, resolvedName, args)
	if session != nil && session.Logger != nil {
		session.Logger.LogToolResult(aicliLogScope{TurnID: "image-fallback", RequestID: "image-fallback-req-01"}, "direct-image-fallback", resolvedName, toolExecutionLogPayload(output, metadata), err)
	}
	if err != nil {
		return nil, err
	}
	return &directFunctionInvokeReport{
		RequestedName: toolnames.OpenAIImageGenerateToolName,
		FunctionName:  resolvedName,
		Output:        output,
		Metadata:      metadata,
	}, nil
}

func persistDirectImageGenerationFallbackAssistant(session *ChatSession, report *directFunctionInvokeReport) {
	if session == nil || report == nil || strings.TrimSpace(report.Output) == "" {
		return
	}
	message := runtimetypes.NewAssistantMessage(strings.TrimSpace(report.Output))
	for key, value := range report.Metadata {
		message.Metadata[key] = value
	}
	message.Metadata["direct_image_generation_fallback"] = true
	message.Metadata["fallback_function"] = report.FunctionName
	if session.RuntimeSession != nil {
		session.RuntimeSession.AddMessage(*message)
		_ = replaceRuntimeMessages(session, session.RuntimeSession.History)
		warnIfChatSessionSyncFails(session, "direct image fallback sync", syncRuntimeSessionFromChat(session))
		return
	}
	appendRuntimeMessage(session, *message)
	warnIfChatSessionSyncFails(session, "direct image fallback sync", syncRuntimeSessionFromChat(session))
}

type generatedImageArtifactInfo struct {
	path    string
	modTime time.Time
}

func recentGeneratedImageArtifactPaths(session *ChatSession) []string {
	dir := strings.TrimSpace(currentGeneratedImageArtifactDir(session))
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	images := make([]generatedImageArtifactInfo, 0, len(entries))
	for _, entry := range entries {
		if entry == nil || entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		switch ext {
		case ".png", ".jpg", ".jpeg", ".webp":
		default:
			continue
		}
		info, err := entry.Info()
		if err != nil || info.Size() <= 0 {
			continue
		}
		images = append(images, generatedImageArtifactInfo{
			path:    resolveAbsoluteChatPath(filepath.Join(dir, entry.Name())),
			modTime: info.ModTime(),
		})
	}
	sort.Slice(images, func(i, j int) bool {
		if images[i].modTime.Equal(images[j].modTime) {
			return images[i].path < images[j].path
		}
		return images[i].modTime.After(images[j].modTime)
	})
	paths := make([]string, 0, len(images))
	for _, image := range images {
		paths = append(paths, image.path)
	}
	return paths
}

func renderAsyncTeamLaunchNotice(session *ChatSession, previousTeamID string) {
	if session == nil || session.LocalRuntimeHost == nil || session.RuntimeEventBridge == nil {
		return
	}
	currentTeamID := activeTeamID(session)
	if currentTeamID == "" || currentTeamID == strings.TrimSpace(previousTeamID) {
		return
	}
	if lifecycle := session.LocalRuntimeHost.teamLifecycleService(); lifecycle == nil || !lifecycle.Pending(context.Background(), currentTeamID) {
		return
	}
	if !shouldRenderInteractiveOutput(session) {
		return
	}
	rendered := chatRuntimeTimelineEvent{
		Line:     prefixExecutionBullet(fmt.Sprintf("[team] %s 已在后台开始执行；我会继续接收进展，并在完成后自动总结结果。", currentTeamID)),
		DedupKey: "team.started.notice:" + currentTeamID,
	}
	if session.RuntimeEventBridge.shouldRenderTimelineEvent(rendered) && session.RuntimeEventBridge.writeLine != nil {
		session.RuntimeEventBridge.writeLine(rendered.Line)
	}
}

func activeTeamID(session *ChatSession) string {
	if session == nil || session.ActiveTeam == nil {
		return ""
	}
	return strings.TrimSpace(session.ActiveTeam.TeamID)
}

func latestAssistantResponseText(session *ChatSession) string {
	if session == nil || session.RuntimeSession == nil {
		return ""
	}
	history := session.RuntimeSession.History
	for index := len(history) - 1; index >= 0; index-- {
		message := history[index]
		if message.Role != "assistant" {
			continue
		}
		content := strings.TrimSpace(message.Content)
		if content != "" {
			return content
		}
	}
	return ""
}
