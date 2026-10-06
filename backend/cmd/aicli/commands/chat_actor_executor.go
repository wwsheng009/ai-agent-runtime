package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/cell"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolnames"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type aicliActorChatExecutor struct{}

const aicliActorReadyPollInterval = 20 * time.Millisecond

// aicliActorReadyWaitTimeout bounds how long a busy state that no in-process run
// backs may block a prompt. Such a state is the leftover of a previous process
// (resume after a crash/kill), e.g. Status=SessionRunning with a turn that will
// never finish, and it never converges on its own. A busy state backed by a live
// run is followed instead of failed, and a parked managed turn (idle +
// SuspendedTurnID) is admitted as a new episode instead of waiting; see
// waitForAICLIActorReady.
var aicliActorReadyWaitTimeout = 30 * time.Second

// chatActorBuildBudget 限制一次提交里的 actor 构建（warmup / 驱逐后重建）。
// 0 表示不设上限（测试或极慢冷启动）。刻意只覆盖构建阶段：turn gate 等待与
// 具体 run 有自己的 ctx/生命周期语义，不属于"预跑卡死"的范畴。
// 见 docs/plan/aicli-chat-submit-run-epoch-wedge-hardening.md（P0-2）。
var chatActorBuildBudget = 5 * time.Minute

// chatActorBuildContext 为 actor 构建派生有界上下文；预算关闭时原样返回父 ctx
// （返回值恒非 nil 可供调用方安全使用）。
func chatActorBuildContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if chatActorBuildBudget <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, chatActorBuildBudget)
}

// chatActorForSessionBounded 给 actor 构建套上构建预算：超时返回可诊断错误
// （提示重试 / 检查 runtime、MCP 配置），避免提交静默卡在构建阶段而用户只看到
// 空转或什么都没有。
func chatActorForSessionBounded(ctx context.Context, session *ChatSession) (*runtimechat.SessionActor, error) {
	buildCtx, cancel := chatActorBuildContext(ctx)
	defer cancel()
	actor, err := chatActorForSession(buildCtx, session)
	if err != nil && errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("runtime actor 构建超过 %s 未就绪（可重试；若持续出现请检查 runtime/MCP 配置）: %w",
			chatActorBuildBudget, err)
	}
	return actor, err
}

// submitAICLIActorPrompt serializes an interactive user turn behind an
// internally-triggered parent turn (for example a supervision auto-wake).
// SessionActor still rejects concurrent control-plane submissions; only the
// primary interactive path opts into wait-and-retry semantics.
func submitAICLIActorPrompt(
	ctx context.Context,
	actor *runtimechat.SessionActor,
	prompt string,
	runMeta *team.RunMeta,
	opt runtimechat.SubmitPromptOption,
) (*agent.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		result, err := actor.SubmitPrompt(ctx, prompt, runMeta, opt)
		if !errors.Is(err, runtimechat.ErrSessionBusy) {
			return result, err
		}
		if err := waitForAICLIActorReady(ctx, actor); err != nil {
			return nil, err
		}
	}
}

func continueAICLIActorWhenReady(
	ctx context.Context,
	actor *runtimechat.SessionActor,
	runMeta *team.RunMeta,
	opt runtimechat.ContinueOption,
) (*agent.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		result, err := actor.Continue(ctx, runMeta, opt)
		if !errors.Is(err, runtimechat.ErrSessionBusy) {
			return result, err
		}
		if err := waitForAICLIActorReady(ctx, actor); err != nil {
			return nil, err
		}
	}
}

// waitForAICLIActorReady waits until the actor can take the caller's next
// operation. The readiness predicate mirrors the actor's own admission gate
// (SessionActor.ensureReady): only a turn that is executing or blocked on
// input/approval in this process keeps a caller waiting.
func waitForAICLIActorReady(ctx context.Context, actor *runtimechat.SessionActor) error {
	if actor == nil {
		return fmt.Errorf("session actor is nil")
	}
	ticker := time.NewTicker(aicliActorReadyPollInterval)
	defer ticker.Stop()
	staleTimeout := time.NewTimer(aicliActorReadyWaitTimeout)
	defer staleTimeout.Stop()
	for {
		state, ok := actor.StateSummary()
		if !ok || state.AcceptsResume() {
			// A parked managed turn (§6.12: idle + SuspendedTurnID) has no run of
			// its own and will never converge to "not busy" on its own, but it is
			// admissible: a new submission becomes a new episode of the same turn
			// (AC-P2-7a). Waiting on it would burn the stale-state budget for a
			// state that the following SubmitPrompt/Continue accepts.
			return nil
		}
		// A live run in this process owns the busy state (a resumed turn, a long
		// tool call, a pending approval). It converges on its own, so keep
		// following it instead of failing the prompt on the stale-state budget:
		// the run stall watchdog still bounds a wedged run, and ctx cancellation
		// is honored below.
		if actor.RunInFlight() {
			resetAICLIActorReadyTimer(staleTimeout, aicliActorReadyWaitTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-staleTimeout.C:
			state, _ := actor.StateSummary()
			msg := fmt.Sprintf(
				"actor 等待就绪超时（%v）：status=%s turn=%s suspended_turn=%s pending_tool=%v pending_approval=%v pending_question=%v active_jobs=%d；"+
					"该忙碌状态没有本进程的运行在支撑，可能是上一进程遗留的 turn，可先 Ctrl+C 结束当前轮次再重新 resume，或使用 /team 清理",
				aicliActorReadyWaitTimeout,
				state.Status, state.CurrentTurnID, state.SuspendedTurnID,
				state.PendingToolName, state.PendingApproval, state.PendingQuestion, state.ActiveJobCount,
			)
			if state.PendingQuestion || state.PendingApproval {
				msg += "；会话有未处理的提问/审批，可用 /agents 查看或重新 resume"
			}
			return errors.New(msg)
		case <-ticker.C:
		}
	}
}

// resetAICLIActorReadyTimer re-arms the stale-state budget without leaking the
// previous firing of the timer.
func resetAICLIActorReadyTimer(timer *time.Timer, d time.Duration) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}

// prepareAICLIActorRuntimeContext keeps the actor path's output ownership in
// the runtime event bridge. Agent tool execution binds a stable
// tool_call_id-scoped progress reporter before invoking shell-like tools; the
// bridge projects those tool.progress events into ActiveBand and suppresses
// their timeline cells. Installing an additional raw OutputMirror here would
// write the same bytes directly into FixedBottomSurface history, after which
// tool.completed commits a second normalized result cell. The actor path
// therefore carries only execution context; direct/legacy tool execution uses
// withLiveChatToolOutput's fenced ActiveBand-only writer instead.
func prepareAICLIActorRuntimeContext(ctx context.Context, session *ChatSession) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return generatedImageToolContext(ctx, session)
}

func newAICLIActorChatExecutor() aicliChatExecutor {
	return &aicliActorChatExecutor{}
}

func (e *aicliActorChatExecutor) RuntimeDescriptor() aicliRuntimeExecutorDescriptor {
	return newAICLIActorRuntimeDescriptor(aicliRuntimeTransportInProcess)
}

func (e *aicliActorChatExecutor) Execute(ctx context.Context, session *ChatSession, prompt string) (string, error) {
	if session == nil {
		return "", fmt.Errorf("chat session is nil")
	}
	// mesh：一个 turn 的边界（S2）——翻转节点档案的 session.busy。
	releaseMeshTurn := beginChatMeshTurn(session)
	defer releaseMeshTurn()
	if session.LocalRuntimeHost == nil || session.LocalRuntimeHost.SessionHub == nil {
		return "", fmt.Errorf("local runtime host is not configured")
	}
	if session.RuntimeSession == nil {
		return "", fmt.Errorf("runtime session is not configured")
	}
	if err := ensureSessionDurableBeforeActor(session); err != nil {
		return "", fmt.Errorf("persist runtime session: %w", err)
	}
	// 上一轮切换撞上在途 turn 时留下的延迟重建：actor 此刻已空闲，先驱逐再
	// GetOrCreate，否则本回合仍会用到旧 agent 的工具策略（A1 的假开关失效模式）。
	reconcilePendingChatActorRebuild(session)
	// P2：从此刻到本回合返回，本会话存在"在途提交"；运行期刷新必须让路（登记
	// 延迟重建），否则可能把 actor 从提交脚下驱逐，形成静默撕裂。
	releaseSubmitClaim := beginChatActorSubmitClaim(session)
	defer releaseSubmitClaim()
	ctx = prepareAICLIActorRuntimeContext(ctx, session)

	actor, err := chatActorForSessionBounded(ctx, session)
	if err != nil {
		return "", err
	}
	releaseTurn, err := session.LocalRuntimeHost.acquireActorTurnGate(ctx, session.RuntimeSession.ID)
	if err != nil {
		return "", err
	}
	defer releaseTurn()
	// A non-gated control-plane caller may already own the actor. Wait before
	// BeginRun so its lifecycle cannot be mistaken for this foreground turn.
	if err := waitForAICLIActorReady(ctx, actor); err != nil {
		return "", err
	}
	previousAssistant := latestAssistantResponseText(session)
	previousTeamID := activeTeamID(session)
	// 阶段 C：actor/executor 回合（不限于本地 sendMessage）同样持有 ESC 消费者；
	// 引用计数与本地 watcher 共享同一消费者，重叠回合不会互相 disarm。
	stopEscapeConsumer := startChatEscapeInterruptWatcher(session)
	defer stopEscapeConsumer()
	if bridge := ensureChatRuntimeEventBridge(session); bridge != nil {
		bridge.PrepareRunPrompt(prompt)
		bridge.BeginRun()
		defer bridge.EndRun()
	}
	// P0-1（docs/plan/aicli-chat-submit-run-epoch-wedge-hardening.md）：
	// run 协议（BeginRun）已开启，才把前台 UI 切到等待态；预跑阶段
	// （chatActorForSession / acquireActorTurnGate / waitForAICLIActorReady）
	// 的任何失败或停滞都不会留下幽灵 "Analyzing"。等待态的清理仍由
	// sendMessage 的 defer（CompleteWaiting/ClearWaiting）负责。
	if session.Interaction != nil {
		session.Interaction.StartWaiting()
	}
	if session.runtimeHTTPCapture != nil {
		session.runtimeHTTPCapture.Reset()
	}
	if reporter := newRuntimeHTTPDebugReporter(session); reporter != nil {
		ctx = runtimellm.WithHTTPDebugReporter(ctx, reporter)
	}
	submitOption := runtimechat.SubmitPromptOption{
		ImagePaths:       session.ImagePaths,
		ImageArtifactDir: chatSessionImageArtifactDir(session),
	}
	// `/skill` 默认路径的一次性 pin：消费即焚，只随本回合的 SubmitPromptOption
	// 跨包传递（guide → 回合 system 消息；pinnedTools → 稳定工具面冻结后叠加）。
	skillPin := consumeSkillTurnPin(session)
	var turnMessages []runtimetypes.Message
	// P1 常驻 catalog（§4.5/§5 P1 行 1）：session-scope 抽象指令置于 turn 消息
	// 首位；没有 pin/mention 时也单独生效。loop 的 composeInitialHistory 会按
	// scope 把它重定位到 leading 稳定前缀，落盘前剥离（prompt-only）。
	if resident := buildResidentSkillCatalogMessage(session); resident != nil {
		turnMessages = append(turnMessages, *resident)
	}
	if skillPin != nil {
		if guide := strings.TrimSpace(skillPin.Guide); guide != "" {
			turnMessages = append(turnMessages, *runtimetypes.NewSystemMessage(guide))
		}
		submitOption.TurnPinnedTools = skillPin.PinnedTools
	}
	// P0 mention 注入：仅用户发起回合；guide 在前、mention 片段在后（§4.4 注入点）。
	// §4.11：本地预算裁决先于 preflight，超限只降级注入，不触发历史压缩。
	if skillMentionTurnEligible(session, prompt, chatSkillMentionInteractiveTurn(session)) {
		if mentionFragments, _ := buildSkillMentionTurnMessages(ctx, session, skillMentionTurnInput{
			Prompt:          prompt,
			Interactive:     chatSkillMentionInteractiveTurn(session),
			SystemGenerated: chatSkillMentionSystemGeneratedPrompt(prompt),
			Pin:             skillPin,
			UsedTokens:      countSharedChatMessagesTokens(session.Messages) + skillMentionGuideTokens(skillPin),
			BudgetTokens:    resolveSharedChatPromptBudget(session).ActiveTurnMaxTokens,
		}); len(mentionFragments) > 0 {
			turnMessages = append(turnMessages, mentionFragments...)
		}
	}
	if len(turnMessages) > 0 {
		submitOption.TurnSystemMessages = turnMessages
	}
	result, err := submitAICLIActorPrompt(withLivePermissionModeSource(ctx, session), actor, prompt, currentRunMetaForSession(session), submitOption)
	if err != nil {
		logActorExecutorFailureIfUnrecorded(session, prompt, err)
		warnIfChatSessionSyncFails(session, "actor error sync", syncRuntimeSessionBackIntoCLIAfterFailure(session))
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
		if !bridge.WaitForCurrentEvents(chatRuntimeEventDrainTimeout(session, waitTimeout)) {
			writeSessionDebugInfo(session, "[runtime-event] actor executor drain timeout before prompt result", false)
			writeSessionDebugInfo(session, fmt.Sprintf("[runtime-event] drain snapshot before prompt result: %s", bridge.drainSnapshot().describe()), false)
		}
		if runErr := bridge.RunError(); runErr != nil {
			logActorExecutorFailureIfUnrecorded(session, prompt, runErr)
			warnIfChatSessionSyncFails(session, "actor runtime error sync", syncRuntimeSessionBackIntoCLIAfterFailure(session))
			warnIfChatSessionSyncFails(session, "actor runtime error team lifecycle sync", syncAmbientTeamLifecycleState(session))
			return "", humanizeActorExecutorError(session, runErr)
		}
	}
	if err := syncRuntimeSessionBackIntoCLI(session); err != nil {
		return "", err
	}
	warnIfChatSessionSyncFails(session, "actor post-turn reconcile", runPostTurnReconcilersAndSync(session))
	if result != nil {
		applyChatTokenUsage(session, result.Usage)
	}
	warnIfChatSessionSyncFails(session, "actor usage sync", syncRuntimeSessionFromChat(session))
	warnIfChatSessionSyncFails(session, "actor team lifecycle sync", syncAmbientTeamLifecycleState(session))
	if result == nil {
		return "", nil
	}
	if bridge := session.RuntimeEventBridge; bridge != nil {
		bridge.BindExecutorTurn(result.TurnID)
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

func (e *aicliActorChatExecutor) ContinueGoal(ctx context.Context, session *ChatSession) (string, error) {
	if session == nil {
		return "", fmt.Errorf("chat session is nil")
	}
	// mesh：goal 续写同样是一个 turn（S2）。
	releaseMeshTurn := beginChatMeshTurn(session)
	defer releaseMeshTurn()
	if session.RuntimeSession == nil {
		return "", fmt.Errorf("runtime session is not configured")
	}
	if err := ensureSessionDurableBeforeActor(session); err != nil {
		return "", fmt.Errorf("persist runtime session: %w", err)
	}
	if session.LocalRuntimeHost == nil || session.LocalRuntimeHost.SessionHub == nil {
		return "", fmt.Errorf("local runtime host is not configured")
	}
	reconcilePendingChatActorRebuild(session)
	releaseSubmitClaim := beginChatActorSubmitClaim(session)
	defer releaseSubmitClaim()
	ctx = prepareAICLIActorRuntimeContext(ctx, session)

	actor, err := chatActorForSessionBounded(ctx, session)
	if err != nil {
		return "", err
	}
	releaseTurn, err := session.LocalRuntimeHost.acquireActorTurnGate(ctx, session.RuntimeSession.ID)
	if err != nil {
		return "", err
	}
	defer releaseTurn()
	if err := waitForAICLIActorReady(ctx, actor); err != nil {
		return "", err
	}
	previousAssistant := latestAssistantResponseText(session)
	previousTeamID := activeTeamID(session)
	// 阶段 C：goal 续跑同样由 actor 驱动，必须与前台回合一样可被 ESC 中断。
	stopEscapeConsumer := startChatEscapeInterruptWatcher(session)
	defer stopEscapeConsumer()
	if bridge := ensureChatRuntimeEventBridge(session); bridge != nil {
		bridge.PrepareRunPrompt("")
		bridge.BeginRun()
		defer bridge.EndRun()
	}
	if session.runtimeHTTPCapture != nil {
		session.runtimeHTTPCapture.Reset()
	}
	if reporter := newRuntimeHTTPDebugReporter(session); reporter != nil {
		ctx = runtimellm.WithHTTPDebugReporter(ctx, reporter)
	}
	result, err := continueAICLIActorWhenReady(withLivePermissionModeSource(ctx, session), actor, currentRunMetaForSession(session), runtimechat.ContinueOption{
		ContinuationPrompt: goalAutoContinuationPrompt,
		ContinuationMetadata: map[string]interface{}{
			goalContinuationMetadataKey: true,
		},
		StripMetadataKeys: []string{goalContinuationMetadataKey},
	})
	if err != nil {
		warnIfChatSessionSyncFails(session, "actor goal continuation error sync", syncRuntimeSessionBackIntoCLIAfterFailure(session))
		warnIfChatSessionSyncFails(session, "actor goal continuation team lifecycle sync", syncAmbientTeamLifecycleState(session))
		return "", humanizeActorExecutorError(session, err)
	}
	if bridge := session.RuntimeEventBridge; bridge != nil {
		waitTimeout := 8 * time.Second
		if session.Interaction != nil && result != nil {
			waitTimeout = session.Interaction.EstimateStreamFlushTimeout(result.Output)
			if waitTimeout < 8*time.Second {
				waitTimeout = 8 * time.Second
			}
		}
		if !bridge.WaitForCurrentEvents(chatRuntimeEventDrainTimeout(session, waitTimeout)) {
			writeSessionDebugInfo(session, "[runtime-event] actor executor drain timeout before goal continuation result", false)
			writeSessionDebugInfo(session, fmt.Sprintf("[runtime-event] drain snapshot before goal continuation result: %s", bridge.drainSnapshot().describe()), false)
		}
		if runErr := bridge.RunError(); runErr != nil {
			warnIfChatSessionSyncFails(session, "actor goal continuation runtime error sync", syncRuntimeSessionBackIntoCLIAfterFailure(session))
			warnIfChatSessionSyncFails(session, "actor goal continuation team lifecycle sync", syncAmbientTeamLifecycleState(session))
			return "", humanizeActorExecutorError(session, runErr)
		}
	}
	if err := syncRuntimeSessionBackIntoCLI(session); err != nil {
		return "", err
	}
	warnIfChatSessionSyncFails(session, "actor goal continuation post-turn reconcile", runPostTurnReconcilersAndSync(session))
	if result != nil {
		applyChatTokenUsage(session, result.Usage)
	}
	warnIfChatSessionSyncFails(session, "actor goal continuation usage sync", syncRuntimeSessionFromChat(session))
	warnIfChatSessionSyncFails(session, "actor goal continuation team lifecycle sync", syncAmbientTeamLifecycleState(session))
	if result == nil {
		return "", nil
	}
	if bridge := session.RuntimeEventBridge; bridge != nil {
		bridge.BindExecutorTurn(result.TurnID)
	}
	renderAsyncTeamLaunchNotice(session, previousTeamID)
	response := resolveActorExecutorResponse(result.Output, session, previousAssistant)
	if response == "" {
		response = fallbackActorExecutorResponse(result, session)
	}
	return response, nil
}

func humanizeActorExecutorError(session *ChatSession, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, runtimechat.ErrSessionActorStopped) {
		return fmt.Errorf("本地会话执行器已停止，本次请求未被处理；请重新发送，系统会自动创建新的执行器")
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
	if permissionMode := chatSessionPermissionMode(session); permissionMode != "" {
		runMeta.PermissionMode = string(permissionMode)
	}
	if session.RuntimeSession != nil {
		// Ordinary interactive/child sessions never own a Team completion
		// contract via session context alone. Force none so a forked or
		// legacy complete_task value cannot re-enter RunMeta outside a bound
		// Team assignment (which TeammateRunner injects directly).
		if requirement := strings.TrimSpace(runtimeSessionContextString(session.RuntimeSession, toolbroker.AgentSessionContextCompletionRequirement)); requirement != "" {
			runMeta.CompletionRequirement = "none"
		}
	}
	binding := resolvedInteractiveTeamBinding(session)
	if binding != nil && strings.TrimSpace(binding.TeamID) != "" && shouldPropagateTeamRunMeta(session, binding) {
		runMeta.Team = &team.TeamRunMeta{
			TeamID:        strings.TrimSpace(binding.TeamID),
			AgentID:       firstNonEmptyChatValue(binding.AgentID, "lead"),
			CurrentTaskID: strings.TrimSpace(binding.TaskID),
		}
	}
	if strings.TrimSpace(runMeta.PermissionMode) == "" && strings.TrimSpace(runMeta.CompletionRequirement) == "" && runMeta.Team == nil {
		return nil
	}
	return runMeta
}

// withLivePermissionModeSource lets an in-flight actor turn observe a
// session-scoped permission-mode switch (for example the ACP
// session/set_config_option "mode" option) from the next tool evaluation
// instead of waiting for the next turn. RunMeta.PermissionMode is frozen at
// submit time, so the resolver reads the session state under its own lock.
//
// The resolver only reports a mode when the session value actually changed
// after the turn was submitted. Reporting the unchanged submit-time value
// would shadow the actor's own live channel: RunMeta carries every in-turn
// plan-mode transition (enter_plan_mode / exit_plan_mode republish it through
// syncLivePermissionMode, and the durable plan state pins it at turn start),
// while plan transitions are rejected for in-flight control-plane switches.
// An unchanged value therefore returns "" so permissionModeFromContext falls
// back to RunMeta / the engine mode, which is what keeps a mid-turn
// exit_plan_mode from leaving the rest of the turn frozen in plan mode (and a
// mid-turn enter_plan_mode from being bypassed by a stale CLI mode).
func withLivePermissionModeSource(ctx context.Context, session *ChatSession) context.Context {
	if session == nil {
		return ctx
	}
	submitted := chatSessionPermissionMode(session)
	return team.WithPermissionModeSource(ctx, func() string {
		current := chatSessionPermissionMode(session)
		if current == submitted {
			return ""
		}
		return string(current)
	})
}

func shouldPropagateTeamRunMeta(session *ChatSession, binding *chatTeamBinding) bool {
	if binding == nil || strings.TrimSpace(binding.TeamID) == "" {
		return false
	}
	if session != nil && session.LocalRuntimeHost != nil && session.LocalRuntimeHost.TeamStore != nil {
		record, err := session.LocalRuntimeHost.TeamStore.GetTeam(context.Background(), strings.TrimSpace(binding.TeamID))
		if err == nil && record != nil && team.IsTerminalTeamStatus(record.Status) {
			return false
		}
	}
	if interactiveTeamPendingByTeamID(session, binding.TeamID) {
		return true
	}
	activeTeam := chatSessionActiveTeam(session)
	if session == nil || activeTeam == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(activeTeam.TeamID), strings.TrimSpace(binding.TeamID)) {
		return false
	}
	return session.LocalRuntimeHost == nil || session.LocalRuntimeHost.TeamStore == nil
}

func syncRuntimeSessionBackIntoCLI(session *ChatSession) error {
	return syncRuntimeSessionBackIntoCLIWithOptions(session, true)
}

func syncRuntimeSessionBackIntoCLIAfterFailure(session *ChatSession) error {
	return syncRuntimeSessionBackIntoCLIWithOptions(session, false)
}

func syncRuntimeSessionBackIntoCLIWithOptions(session *ChatSession, refreshContextFromHistory bool) error {
	if session == nil || session.SessionManager == nil || session.RuntimeSession == nil {
		return nil
	}
	previousContextWindowTokens := session.ContextWindowTokenCount
	previousContextTokens := session.ContextTokenCount
	providerContextTokens := session.providerContextTokenCount
	providerContextWindowTokens := session.providerContextWindowTokenCount
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
	// Prefer provider last-turn snapshot > history estimate > previous.
	// Do not re-raise ContextTokenCount via previous>history high-water after
	// compact/history shrink when the provider snapshot is empty.
	if refreshContextFromHistory {
		_ = refreshChatContextTokenSnapshotFromMessages(session, previousContextWindowTokens, true)
	} else if previousContextTokens > 0 || previousContextWindowTokens > 0 {
		applyChatContextTokens(session, previousContextTokens, previousContextWindowTokens, true)
	}
	if providerContextTokens > 0 {
		if providerContextWindowTokens <= 0 {
			providerContextWindowTokens = previousContextWindowTokens
		}
		applyChatContextTokensFromUsage(session, &runtimetypes.TokenUsage{TotalTokens: providerContextTokens}, providerContextWindowTokens, true)
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
	output, metadata, err := catalog.executeRegisteredFunctionWithMeta(ctx, resolvedName, args)
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
	rendered := typedChatRuntimeTimelineEvent(cell.TimelineEvent{
		Kind:   cell.TimelineTeam,
		Status: cell.StatusRunning,
		Marker: "• ",
		Title:  fmt.Sprintf("%s 已在后台开始执行；我会继续接收进展，并在完成后自动总结结果。", currentTeamID),
	}, "team.started.notice:"+currentTeamID)
	if session.RuntimeEventBridge.shouldRenderTimelineEvent(rendered) {
		session.RuntimeEventBridge.emitTimelineEvent(rendered)
	}
}

func activeTeamID(session *ChatSession) string {
	activeTeam := chatSessionActiveTeam(session)
	if activeTeam == nil {
		return ""
	}
	return strings.TrimSpace(activeTeam.TeamID)
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
