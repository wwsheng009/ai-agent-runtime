package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
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

	actor, err := session.LocalRuntimeHost.SessionHub.GetOrCreate(session.RuntimeSession.ID)
	if err != nil {
		return "", err
	}
	previousAssistant := latestAssistantResponseText(session)
	previousTeamID := activeTeamID(session)
	if bridge := ensureChatRuntimeEventBridge(session); bridge != nil {
		bridge.BeginRun()
		defer bridge.EndRun()
	}
	if session.runtimeHTTPCapture != nil {
		session.runtimeHTTPCapture.Reset()
	}
	if reporter := newRuntimeHTTPDebugReporter(session); reporter != nil {
		ctx = runtimellm.WithHTTPDebugReporter(ctx, reporter)
	}
	result, err := actor.SubmitPrompt(ctx, prompt, currentRunMetaForSession(session))
	if err != nil {
		warnIfChatSessionSyncFails(session, "actor error sync", syncRuntimeSessionBackIntoCLI(session))
		warnIfChatSessionSyncFails(session, "actor error team lifecycle sync", syncAmbientTeamLifecycleState(session))
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
	warnIfChatSessionSyncFails(session, "actor team lifecycle sync", syncAmbientTeamLifecycleState(session))
	if result == nil {
		return "", nil
	}
	renderAsyncTeamLaunchNotice(session, previousTeamID)
	response := resolveActorExecutorResponse(result.Output, session, previousAssistant)
	if response == "" {
		response = fallbackActorExecutorResponse(result)
	}
	return response, nil
}

func humanizeActorExecutorError(session *ChatSession, err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "upstream model returned an empty reply: no text and no tool calls") {
		message := "上游模型返回了空回复：既没有文本，也没有发起工具调用；请重试，或调整提示词/切换模型后再试"
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

func fallbackActorExecutorResponse(result *agent.Result) string {
	if result == nil {
		return ""
	}
	if result.Success || strings.TrimSpace(result.Output) != "" {
		return ""
	}
	errText := strings.TrimSpace(result.Error)
	if errText == "" {
		return "这次处理没有生成后续回复，请重试。"
	}
	return strings.Join([]string{
		"这次处理没有生成后续回复。",
		"原因: " + truncateChatRuntimeText(errText, 240),
		"请根据上面的信息重试，或调整请求后再试。",
	}, "\n")
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
