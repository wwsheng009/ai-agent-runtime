package commands

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/formatter"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/cell"
	uidiff "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/diff"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/compactruntime"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolprotocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type chatRuntimeEventBridge struct {
	session             *ChatSession
	primarySessionMu    sync.RWMutex
	primarySessionID    string
	startOnce           sync.Once
	processorOnce       sync.Once
	eventQueue          chan chatRuntimeQueuedEvent
	eventQueueMu        sync.Mutex
	eventQueueCond      *sync.Cond
	eventQueueBytes     int64
	eventQueueByteLimit int64
	// uiActionPostTimeout is the effective bounded wait for UI actor mailbox
	// admission. Zero falls back to uiActionPostBudget; tests may shorten it
	// to exercise the stall path without sleeping for the full budget.
	uiActionPostTimeout time.Duration
	// streamMu + pendingStreams coalesce streaming deltas (reasoning /
	// assistant text) while the consumer is behind, so a slow UI consumer
	// can never block the LLM stream callback. Merged events are emitted
	// non-blockingly as soon as the consumer catches up; ordering is
	// preserved by flushing pending streams before any non-streaming event
	// is enqueued.
	streamMu                        sync.Mutex
	pendingStreams                  []chatRuntimeQueuedEvent
	pendingStreamsBytes             int64
	runMu                           sync.Mutex
	logMu                           sync.Mutex
	renderMu                        sync.Mutex
	progressMu                      sync.Mutex
	runErr                          error
	rendered                        map[string]struct{}
	historySeedSeen                 map[string]struct{}
	approvalGrants                  map[string]time.Time
	permissionHintShown             bool
	priorityTranscriptTarget        chatRuntimePriorityTranscriptTarget
	renderedAssistantDelta          bool
	renderedAssistantDeltaFinalized bool
	renderedAssistantDeltaContent   strings.Builder
	renderedAssistantDeltaDigest    [sha256.Size]byte
	renderedAssistantDeltaLength    int
	renderedAssistantFinal          bool
	renderedAssistantFinalDigest    [sha256.Size]byte
	renderedAssistantFinalLength    int
	renderedReasoningDelta          bool
	renderedReasoningFinal          bool
	runStarted                      bool
	runActive                       bool
	runEpoch                        uint64
	activeTurnID                    string
	executorTurnID                  string
	activeAssistantStreamID         string
	assistantStreams                map[string]*chatAssistantStreamState
	retiredAssistantStreams         map[string]struct{}
	retiredTurnIDs                  map[string]struct{}
	retiredTurnOrder                []string
	acceptedAssistantFinalTurns     map[string]struct{}
	finalAssistantTurns             map[string]struct{}
	nextRunPrompt                   string
	activeRunPrompt                 string
	requestLogState                 map[string]*chatRuntimeRequestLogState
	traceLatestRequestKey           map[string]string
	latestRequestKey                string
	loggedToolCalls                 map[string]struct{}
	loggedToolResults               map[string]struct{}
	toolCallStartedAt               map[string]time.Time
	toolExecutionCalls              []aicliToolExecutionCallSummary
	toolSummaryLogged               bool
	enqueuedEvents                  uint64
	processedEvents                 uint64
	criticalPending                 uint64
	askApproval                     func(*runtimechat.ApprovalRequest, []string) (chatApprovalAnswer, error)
	askQuestion                     func(prompt string, suggestions []string, required bool) (string, error)
	approveTool                     func(ctx context.Context, sessionID, requestID string, allow bool) error
	answerQuestion                  func(ctx context.Context, sessionID, questionID, answer string) error
	// preferInteractiveApprovals keeps NoInteractive headless hosts (ACP stdio)
	// from auto-denying tools so askApproval can RPC to an external client.
	preferInteractiveApprovals bool
	writeLine                  func(string)
	writeDocument              func(render.Document) bool
	writeDelta                 func(string)
	finalizeDelta              func()
	completeDelta              func(string) bool
	writeReasoningDelta        func(*runtimetypes.ReasoningBlock)
	finalizeReasoning          func()
	completeReasoning          func(*runtimetypes.ReasoningBlock) bool
	renderResponse             func(string)
	writePrompt                func()
	renderEncoder              *encoding.EventEncoder // 统一渲染编码器（双跑模式数据面）
	renderScene                *scene.TuiScene        // 统一渲染 Scene（P3：ChangeSet 消费端）
	renderMapper               *scene.ChangeSetMapper // 绑定 renderScene 的映射器（有状态，复用）
	sceneMu                    sync.RWMutex           // 保护 renderScene/renderMapper 与 scene 统计
	sceneApplyFailures         uint64                 // ChangeSet 映射/提交失败次数（诊断）
	sceneLastError             string                 // 最近一次映射失败原因（诊断）
	scenePresenterMode         bool                   // AICLI_SCENE_PRESENTER=1：完整块可见行以 Scene 投影为权威（P3 切换）
	interactionAnchorMu        sync.Mutex
	interactionAnchor          *encoding.Tail // 最近一次用户交互触发时刻的模型尾部锚点（§5.5）
	interactionAnchorAt        time.Time
	interactionAnchorSource    string
	interactionAnchorCount     uint64
	// pendingInteraction 是"本次交互命令尚未消费"的标记：recordInteractionAnchor
	// 设置，RenderCommandDocument 提交点经 submitCommandResult 消费（转
	// 交互锚定注入）；无 cell 输出的 legacy 路径在命令返回前显式清除，
	// 防止残留污染下一条普通命令（见 chat_debug_archive.go / chat_model_command.go）。
	pendingInteractionSource string
	pendingInteractionTail   *encoding.Tail
	eventLogMu               sync.Mutex
	eventLogPathOverride     string // 测试注入：覆盖日志文件路径
	eventLogCount            uint64 // 已写入事件数
	eventLogReplayed         uint64 // 启动时重放事件数
	eventLogFailures         uint64 // 写入/重放失败次数

	// 渲染层双跑文本对照（切片 9）：coordinator 每个完整块提交后调用
	// checkTextParity，把旧路径实际写出的行序列与 Scene 快照 RenderText
	// 的对应片段逐行对照。只读旁路审计（不改变任何输出行为），统计供
	// /debug 审计段展示。
	textParityMu      sync.Mutex
	textParityBlocks  uint64 // 已对照的完整块数
	textParityMatched uint64 // 与 Scene 一致的块数
	textParityMissed  uint64 // 不一致/无法对照的块数
	textParityLastErr string // 最近一次不一致详情
	textParityCell    int    // 已对照的 Scene cell 数（每完整块对应一个 cell）
}

type chatRuntimeQueuedEvent struct {
	event runtimeevents.Event
	size  int64
	epoch uint64
}

type chatAssistantStreamState struct {
	turnID       string
	streamID     string
	nextSequence uint64
	pending      map[uint64]runtimeevents.Event
	pendingBytes int64
	tainted      bool
}

type chatRuntimeRequestLogState struct {
	Scope                   aicliLogScope
	StartedAt               time.Time
	FinishedAt              time.Time
	RequestLogged           bool
	ResponseLogged          bool
	AwaitingAssistantResult bool
	PendingResponseContent  map[string]interface{}
}

type chatApprovalAnswer struct {
	Allowed bool
	Reuse   bool
}

// chatRuntimePriorityTranscriptTarget carries the identity of the control
// event currently blocked on synchronous stdin. It is deliberately scoped to
// the bridge worker exception: it lets the retained prompt transcript finalize
// the already-encoded Scene item instead of appending a second cell.
type chatRuntimePriorityTranscriptTarget struct {
	eventType  string
	requestKey string
}

func (t chatRuntimePriorityTranscriptTarget) valid() bool {
	return strings.TrimSpace(t.eventType) != "" && strings.TrimSpace(t.requestKey) != ""
}

const chatApprovalGrantTTL = 10 * time.Minute
const chatRuntimeEventSettleWindow = 80 * time.Millisecond
const chatRuntimeEventQueueByteLimit int64 = 4 << 20
const chatRuntimeEventQueueNormalCapacity = 512
const chatRuntimeEventQueueCriticalReserve = 64
const chatRuntimeEndRunDrainTimeout = 8 * time.Second
const chatRuntimeInterruptedEndRunDrainTimeout = 250 * time.Millisecond
const chatAssistantStreamPendingLimit = 128

// uiActionPostBudget bounds how long the bridge waits for a bounded UI actor
// mailbox slot. A stalled reducer must never wedge the run forever; after the
// budget the event is dropped (and logged) and the chain continues.
const uiActionPostBudget = 5 * time.Second
const chatAssistantStreamPendingByteLimit int64 = 1 << 20

// chatStreamCoalesce* bound the coalesced stream backlog while the UI is
// behind. Count and byte caps keep a permanently stalled reducer from
// accumulating unbounded text, and the per-event cap keeps one merged delta
// from monopolizing the bounded queue.
const chatStreamCoalescePendingLimit = 128
const chatStreamCoalescePendingByteLimit int64 = 1 << 20
const chatStreamCoalesceEventByteLimit int64 = 1 << 20

// streamCoalescedFromKey records the first sequence folded into a coalesced
// stream event. The visible "sequence" field keeps the last sequence so the
// merged delta is the authoritative interval; downstream ordering must use
// this field to advance past the whole interval instead of waiting for the
// folded intermediate sequences that will never arrive separately.
const streamCoalescedFromKey = encoding.StreamCoalescedFromKey

// chatStreamFlushBudget bounds how long a non-streaming event waits for the
// coalesced stream backlog before the remainder is dropped for liveness.
const chatStreamFlushBudget = 100 * time.Millisecond
const chatRetiredTurnLimit = 64

func ensureChatRuntimeEventBridge(session *ChatSession) *chatRuntimeEventBridge {
	if session == nil {
		return nil
	}
	if session.RuntimeEventBridge == nil {
		session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	}
	// 渲染层双跑文本对照（切片 9）：bridge 就绪后把探针注入 coordinator 的
	// 完整块提交点（writeRowsLocked）。coordinator 尚未创建时由构造器接线。
	if session.Interaction != nil {
		session.Interaction.SetTextParityProbe(session.RuntimeEventBridge.checkTextParity)
		session.Interaction.SetBlockSource(session.RuntimeEventBridge.sceneBlockSource())
	}
	session.RuntimeEventBridge.start()
	return session.RuntimeEventBridge
}

// scenePresenterModeFromEnv 解析 Scene presenter 迁移开关
// （AICLI_SCENE_PRESENTER=1/true/on/yes，大小写不敏感）。默认关闭：
// 关闭时可见输出完全保持旧路径（双跑 + 对照探针）；开启时完整块可见行
// 以 Scene 投影为权威（P3 切换的可回退 feature flag）。
func scenePresenterModeFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("AICLI_SCENE_PRESENTER"))) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

func newChatRuntimeEventBridge(session *ChatSession) *chatRuntimeEventBridge {
	renderScene := scene.New()
	bridge := &chatRuntimeEventBridge{
		session:             session,
		primarySessionID:    currentRuntimeSessionID(session),
		eventQueue:          make(chan chatRuntimeQueuedEvent, chatRuntimeEventQueueNormalCapacity+chatRuntimeEventQueueCriticalReserve),
		eventQueueByteLimit: chatRuntimeEventQueueByteLimit,
		uiActionPostTimeout: uiActionPostBudget,
		rendered:            make(map[string]struct{}),
		historySeedSeen:     make(map[string]struct{}),
		renderEncoder:       encoding.NewEventEncoder(),
		renderScene:         renderScene,
		renderMapper:        scene.NewChangeSetMapper(renderScene),
		scenePresenterMode:  scenePresenterModeFromEnv(),
		writeLine: func(line string) {
			if strings.TrimSpace(line) == "" {
				return
			}
			if session != nil && session.Interaction != nil {
				session.Interaction.RenderAsyncLine(line)
				return
			}
			if unifiedInteractiveOutputMustFailClosed(session) {
				return
			}
			fmt.Println(ui.FormatAssistantSupplementBlock(line))
		},
		writeDocument: func(doc render.Document) bool {
			if session == nil {
				return false
			}
			if session.Interaction == nil {
				return unifiedInteractiveOutputMustFailClosed(session)
			}
			session.Interaction.RenderAsyncDocument(doc)
			return true
		},
		writeDelta: func(delta string) {
			if delta == "" {
				return
			}
			if session != nil && session.Interaction != nil {
				session.Interaction.RenderAssistantDelta(delta)
				return
			}
			if unifiedInteractiveOutputMustFailClosed(session) {
				return
			}
			fmt.Print(delta)
		},
		finalizeDelta: func() {
			if session != nil && session.Interaction != nil {
				session.Interaction.FinalizeAssistantDelta()
				return
			}
			if unifiedInteractiveOutputMustFailClosed(session) {
				return
			}
			fmt.Println()
		},
		completeDelta: func(content string) bool {
			if session != nil && session.Interaction != nil {
				return session.Interaction.CompleteAssistantResponse(content)
			}
			if unifiedInteractiveOutputMustFailClosed(session) {
				return true
			}
			return false
		},
		writeReasoningDelta: func(block *runtimetypes.ReasoningBlock) {
			if block == nil {
				return
			}
			if session != nil && session.Interaction != nil {
				session.Interaction.RenderReasoningDelta(block)
				return
			}
			if unifiedInteractiveOutputMustFailClosed(session) {
				return
			}
			var markdownFormatter *formatter.MarkdownFormatter
			if session != nil {
				markdownFormatter = session.Formatter
			}
			rendered := chatReasoningRenderText(block, markdownFormatter)
			if rendered == "" {
				return
			}
			fmt.Println(ui.FormatAssistantSupplementBlock(rendered))
		},
		finalizeReasoning: func() {
			if session != nil && session.Interaction != nil {
				session.Interaction.FinalizeReasoningDelta()
			}
		},
		completeReasoning: func(block *runtimetypes.ReasoningBlock) bool {
			if session != nil && session.Interaction != nil {
				return session.Interaction.CompleteReasoningResponse(block)
			}
			if unifiedInteractiveOutputMustFailClosed(session) {
				return true
			}
			return false
		},
		renderResponse: func(response string) {
			if strings.TrimSpace(response) == "" {
				return
			}
			if session == nil {
				fmt.Println(response)
				return
			}
			if session.JSONOutput || session.NoInteractive {
				if session.Interaction != nil {
					newAICLITranscriptRenderer(session).RenderAssistant(response)
					return
				}
				renderChatResponse(session, response)
				return
			}
			newAICLITranscriptRenderer(session).RenderAssistant(response)
		},
		writePrompt: func() {
			if session == nil || session.NoInteractive || session.JSONOutput {
				return
			}
			if session.Interaction != nil {
				session.Interaction.PrintPrompt()
				return
			}
			if unifiedInteractiveOutputMustFailClosed(session) {
				return
			}
			fmt.Print(ui.FormatUserPromptWithAttachments(len(session.ImagePaths)))
		},
		askApproval: func(approval *runtimechat.ApprovalRequest, contextLines []string) (chatApprovalAnswer, error) {
			if unifiedInteractiveOutputMustFailClosed(session) {
				return chatApprovalAnswer{}, fmt.Errorf("unified terminal session is unavailable")
			}
			restoreStage := pushChatComposerAgentStage(session, chatAgentStageAwaitingApproval)
			defer restoreStage()
			lines := make([]string, 0, 10)
			suspension, notice := suspendPendingInteractiveInputForPriorityPrompt(session, "审批提示")
			if suspension != nil {
				defer suspension.Restore()
			}
			if notice != "" {
				lines = append(lines, notice)
			}
			reuseScope := approvalReusePromptScope(session, approval)
			approvalLines := approvalPriorityPromptLines(approval, contextLines)
			if reuseScope != "" && len(approvalLines) > 0 {
				approvalLines[len(approvalLines)-1] = fmt.Sprintf(
					"[审批] 操作：[1] 仅本次允许  [2] 拒绝  [3] 查看完整参数  [4] 允许并在%s复用同类只读审批 10 分钟",
					reuseScope,
				)
			}
			lines = append(lines, approvalLines...)
			promptLine := approvalDecisionPromptWithReuse(reuseScope)
			detailsShown := false
			for {
				readPrompt, cleanupPrompt, transientPrompt := showChatRuntimePriorityPrompt(session, lines, promptLine)
				text, err := chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), readPrompt)
				cleanupPrompt()
				if err != nil {
					return chatApprovalAnswer{}, err
				}
				text = strings.TrimSpace(normalizeQueuedInputLine(text))
				decision := parseApprovalPromptDecisionWithReuse(text, reuseScope != "")
				if decision == approvalPromptShowDetails {
					if !detailsShown {
						lines = append(lines, approvalFullParameterLines(approval)...)
						detailsShown = true
					}
					continue
				}
				if decision == approvalPromptInvalid {
					validOptions := "1、2、3，或 y/n"
					if reuseScope != "" {
						validOptions = "1、2、3、4，或 y/n"
					}
					lines = upsertPriorityPromptValidationLine(lines, "[审批] 无效选项", "[审批] 无效选项，请输入 "+validOptions+"。")
					continue
				}
				if transientPrompt {
					renderChatRuntimePriorityPromptTranscript(session, lines, promptLine, text)
				}
				return chatApprovalAnswer{
					Allowed: decision == approvalPromptAllowOnce || decision == approvalPromptAllowReuse,
					Reuse:   decision == approvalPromptAllowReuse,
				}, nil
			}
		},
		askQuestion: func(prompt string, suggestions []string, required bool) (string, error) {
			if unifiedInteractiveOutputMustFailClosed(session) {
				return "", fmt.Errorf("unified terminal session is unavailable")
			}
			restoreStage := pushChatComposerAgentStage(session, chatAgentStageAwaitingAnswer)
			defer restoreStage()
			normalizedSuggestions := normalizedQuestionSuggestions(suggestions)
			lines := make([]string, 0, len(normalizedSuggestions)+3)
			suspension, notice := suspendPendingInteractiveInputForPriorityPrompt(session, "问题提示")
			if suspension != nil {
				defer suspension.Restore()
			}
			if notice != "" {
				lines = append(lines, notice)
			}
			lines = append(lines, questionPriorityPromptLines(prompt, normalizedSuggestions)...)
			promptLine := questionAnswerPrompt(required, len(normalizedSuggestions) > 0)
			for {
				readPrompt, cleanupPrompt, transientPrompt := showChatRuntimePriorityPrompt(session, lines, promptLine)
				text, err := chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), readPrompt)
				cleanupPrompt()
				if err != nil {
					return "", err
				}
				answer := mapQuestionSuggestionAnswer(normalizeQueuedInputLine(text), normalizedSuggestions)
				if required && answer == "" {
					lines = upsertPriorityPromptValidationLine(lines, "[提问] 此问题为必答项", "[提问] 此问题为必答项，请输入回答。")
					continue
				}
				if transientPrompt {
					renderChatRuntimePriorityPromptTranscript(session, lines, promptLine, answer)
				}
				return answer, nil
			}
		},
	}
	bridge.renderEncoder.EnableReasoningOrderingBarrier(true)
	bridge.eventQueueCond = sync.NewCond(&bridge.eventQueueMu)
	return bridge
}

func pushChatComposerAgentStage(session *ChatSession, stage chatAgentStage) func() {
	if session == nil || session.Interaction == nil {
		return func() {}
	}
	inputMode := chatInputModeChat
	switch normalizeChatAgentStage(stage) {
	case chatAgentStageAwaitingApproval:
		inputMode = chatInputModeApproval
	case chatAgentStageAwaitingAnswer:
		inputMode = chatInputModeAnswer
	}
	restoreInputMode := pushChatComposerInputMode(session, inputMode)
	interaction := session.Interaction
	previousStage := interaction.AgentStage()
	previousDetail := interaction.AgentStageDetail()
	interaction.SetAgentStage(stage)
	return func() {
		if interaction.AgentStage() == stage {
			interaction.SetAgentStageDetail(previousStage, previousDetail)
		}
		restoreInputMode()
	}
}

func (b *chatRuntimeEventBridge) start() {
	if b == nil || b.session == nil || b.session.LocalRuntimeHost == nil || b.session.LocalRuntimeHost.EventBus == nil {
		return
	}
	b.startOnce.Do(func() {
		// 事件日志重放：恢复同会话上次运行进入编码器的全部事件，幂等重建渲染模型。
		// 失败不阻塞启动：计入 failures 供 /debug 审计，进程静默降级为无重放。
		_, _ = b.replayEventLog()
		b.startProcessor()
		b.session.LocalRuntimeHost.EventBus.Subscribe("", b.Handle)
	})
}

func (b *chatRuntimeEventBridge) startProcessor() {
	if b == nil {
		return
	}
	b.processorOnce.Do(func() {
		go b.run()
	})
}

func (b *chatRuntimeEventBridge) BeginRun() {
	if b == nil {
		return
	}
	b.runMu.Lock()
	b.runErr = nil
	b.runMu.Unlock()
	// Drop any coalesced streaming events left over from the previous run
	// epoch; their text was never enqueued and must not bleed into the new
	// run. Taken BEFORE renderMu (streamMu → renderMu, the same order the
	// coalescing path uses): clearing inside the renderMu section would
	// deadlock, and clearing after it leaves a window where a new-run event
	// can be appended just before the clear and then dropped. An event
	// appended in that window still carries the OLD epoch, so it is rejected
	// by the epoch check at consume time either way — clearing first makes
	// that deterministic.
	b.streamMu.Lock()
	b.pendingStreams = nil
	b.pendingStreamsBytes = 0
	b.streamMu.Unlock()
	b.renderMu.Lock()
	b.rendered = make(map[string]struct{})
	b.pruneApprovalGrantsLocked(time.Now().UTC())
	b.renderedAssistantDelta = false
	b.renderedAssistantDeltaFinalized = false
	b.renderedAssistantDeltaContent.Reset()
	b.renderedAssistantDeltaDigest = [sha256.Size]byte{}
	b.renderedAssistantDeltaLength = 0
	b.renderedAssistantFinal = false
	b.renderedAssistantFinalDigest = [sha256.Size]byte{}
	b.renderedAssistantFinalLength = 0
	b.renderedReasoningDelta = false
	b.renderedReasoningFinal = false
	b.runStarted = true
	b.runActive = true
	b.runEpoch++
	b.activeTurnID = ""
	b.executorTurnID = ""
	b.activeAssistantStreamID = ""
	b.assistantStreams = make(map[string]*chatAssistantStreamState)
	b.retiredAssistantStreams = make(map[string]struct{})
	if b.retiredTurnIDs == nil {
		b.retiredTurnIDs = make(map[string]struct{})
	}
	b.acceptedAssistantFinalTurns = make(map[string]struct{})
	b.finalAssistantTurns = make(map[string]struct{})
	b.renderMu.Unlock()
	b.logMu.Lock()
	b.activeRunPrompt = b.nextRunPrompt
	b.nextRunPrompt = ""
	b.requestLogState = make(map[string]*chatRuntimeRequestLogState)
	b.traceLatestRequestKey = make(map[string]string)
	b.latestRequestKey = ""
	b.loggedToolCalls = make(map[string]struct{})
	b.loggedToolResults = make(map[string]struct{})
	b.toolCallStartedAt = make(map[string]time.Time)
	b.toolExecutionCalls = nil
	b.toolSummaryLogged = false
	b.logMu.Unlock()
	if b.session != nil && b.session.Interaction != nil {
		b.session.Interaction.ResetRunState()
		b.session.Interaction.SetAgentStage(chatAgentStagePlanning)
	}
	if b.session != nil && b.session.TitleNotifier != nil {
		b.session.TitleNotifier.ClearTools()
	}
}

func (b *chatRuntimeEventBridge) EndRun() {
	if b == nil {
		return
	}
	b.flushPendingStreamEventBounded(chatStreamFlushBudget)
	if !b.WaitForCurrentEvents(b.endRunDrainTimeout()) {
		b.markEndRunDrainTimeout()
	}
	// Stop the run-active log window before finalization so the run-end
	// render/log work is not double-counted. The render epoch itself stays
	// open for ambient background events (for example async team orchestration)
	// until the next BeginRun advances it.
	b.renderMu.Lock()
	if b.activeTurnID != "" {
		b.retireTurnLocked(b.activeTurnID)
	}
	b.runActive = false
	b.renderMu.Unlock()
	b.finalizeOpenUnifiedStreamsAtRunEnd()
	b.logRunEndFallback()
	if b.session != nil && b.session.TitleNotifier != nil {
		b.session.TitleNotifier.ClearTools()
	}
	if b.session != nil && b.session.Interaction != nil {
		// Codex-aligned: natural turn completion returns the composer to idle/Ready.
		// Keep Stopping only while interrupt cleanup (actor stop / lease release)
		// is still in flight. Once cleanup finishes, return to Ready even if the
		// interrupted flag remains set until the next ResetInterrupt().
		stage := chatAgentStageIdle
		if b.session.IsInterrupted() && b.session.isInterruptCleanupInFlight() {
			stage = chatAgentStageStopping
		} else if b.RunError() != nil {
			stage = chatAgentStageFailed
		}
		b.session.Interaction.SetAgentStage(stage)
	}
	flushChatSessionLog(b.session)
	b.writePromptIfIdle()
}

func (b *chatRuntimeEventBridge) markEndRunDrainTimeout() {
	if b == nil {
		return
	}
	err := fmt.Errorf("runtime event EndRun drain timeout: critical lifecycle delivery may still be pending")
	b.setRunError(err)
	if b.session != nil {
		writeSessionDebugInfo(b.session, "[runtime-event] EndRun drain timeout; finalizing run as failed while critical lifecycle delivery remains recoverable", false)
	}
}

// finalizeOpenUnifiedStreamsAtRunEnd is the fail-closed terminal boundary for
// runtimes that return through cancellation/error handling without publishing
// a final assistant.message, llm.request.finished, or session.end event. The
// encoder mutates the existing semantic item in place and the coordinator only
// receives semantic actions; no legacy surface/raw-terminal fallback exists.
func (b *chatRuntimeEventBridge) finalizeOpenUnifiedStreamsAtRunEnd() {
	if b == nil || b.renderEncoder == nil {
		return
	}
	status := encoding.StatusCompleted
	eventType := runtimechat.EventSessionEnd
	payload := map[string]interface{}{"success": true, "source": "bridge_end_run"}
	if b.session != nil && b.session.IsInterrupted() {
		status = encoding.StatusCanceled
		eventType = runtimechat.EventSessionInterrupted
		payload["success"] = false
		payload["error"] = "run interrupted"
	} else if runErr := b.RunError(); runErr != nil {
		status = encoding.StatusFailed
		payload["success"] = false
		payload["error"] = runErr.Error()
	}

	b.renderMu.Lock()
	changes := b.renderEncoder.FinalizeOpenStreams(status)
	changed := changes != nil && len(changes.Changes) > 0
	if changed {
		b.applyChangeSet(changes)
		b.appendEventLog(runtimeevents.Event{
			Type:      eventType,
			Timestamp: time.Now().UTC(),
			Payload:   payload,
		})
	}
	b.renderMu.Unlock()
	if !changed || b.session == nil || b.session.Interaction == nil || !b.session.Interaction.UnifiedRendererEnabled() {
		return
	}
	// The Scene is committed before this callback. It can therefore emit the
	// fenced FinalizeActiveCellAction, reset coordinator-local compatibility
	// bookkeeping, and never write the partial body a second time.
	b.session.Interaction.FinalizeAssistantDelta()
	b.session.Interaction.postTranscriptSnapshotFromBridge(b)
}

func (b *chatRuntimeEventBridge) retireTurnLocked(turnID string) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return
	}
	if b.retiredTurnIDs == nil {
		b.retiredTurnIDs = make(map[string]struct{})
	}
	if _, exists := b.retiredTurnIDs[turnID]; exists {
		return
	}
	b.retiredTurnIDs[turnID] = struct{}{}
	b.retiredTurnOrder = append(b.retiredTurnOrder, turnID)
	if len(b.retiredTurnOrder) <= chatRetiredTurnLimit {
		return
	}
	oldest := b.retiredTurnOrder[0]
	b.retiredTurnOrder = append([]string(nil), b.retiredTurnOrder[1:]...)
	delete(b.retiredTurnIDs, oldest)
}

func (b *chatRuntimeEventBridge) endRunDrainTimeout() time.Duration {
	if b != nil && b.session != nil && b.session.IsInterrupted() {
		return chatRuntimeInterruptedEndRunDrainTimeout
	}
	return chatRuntimeEndRunDrainTimeout
}

func chatRuntimeEventDrainTimeout(session *ChatSession, normal time.Duration) time.Duration {
	if session != nil && session.IsInterrupted() {
		return chatRuntimeInterruptedEndRunDrainTimeout
	}
	if normal <= 0 {
		return chatRuntimeEndRunDrainTimeout
	}
	return normal
}

func (b *chatRuntimeEventBridge) logRunEndFallback() {
	if b == nil || b.session == nil || b.session.Logger == nil {
		return
	}
	sessionID := b.primaryRuntimeSessionID()
	payload := map[string]interface{}{"success": true, "source": "bridge_end_run"}
	if runErr := b.RunError(); runErr != nil {
		payload["success"] = false
		payload["error"] = runErr.Error()
	} else if b.session.IsInterrupted() {
		payload["success"] = false
		payload["error"] = "run interrupted"
	}
	b.logSessionEnd(runtimeevents.Event{
		Type:      runtimechat.EventSessionEnd,
		SessionID: sessionID,
		Timestamp: time.Now().UTC(),
		Payload:   payload,
	})
}

func (b *chatRuntimeEventBridge) PrepareRunPrompt(prompt string) {
	if b == nil {
		return
	}
	b.logMu.Lock()
	defer b.logMu.Unlock()
	b.nextRunPrompt = prompt
}

func (b *chatRuntimeEventBridge) RunError() error {
	if b == nil {
		return nil
	}
	b.runMu.Lock()
	defer b.runMu.Unlock()
	return b.runErr
}

func (b *chatRuntimeEventBridge) Handle(event runtimeevents.Event) {
	if b == nil {
		return
	}
	size := runtimeevents.ApproximateEventBytes(event)
	if size < 1 {
		size = 1
	}
	if isMergeableStreamEvent(event.Type) {
		// Streaming deltas must never block the LLM callback chain. While the
		// bounded queue is full they are coalesced into one pending event and
		// flushed the moment a slot frees; a UI stall therefore degrades to
		// fewer, larger redraws instead of a dead turn.
		b.enqueueStreamEvent(event, size)
		return
	}
	if isCriticalSubagentLifecycleEvent(event.Type) {
		// Critical control-plane terminal events do not wait behind coalesced
		// assistant deltas. The queue reserves capacity for them; if even that
		// reserve is temporarily exhausted, retry asynchronously and include the
		// pending handoff in EndRun's drain barrier rather than silently dropping
		// the only parent-visible lifecycle notification.
		if !b.enqueueNonStreamEvent(event, size, 0) {
			b.enqueueCriticalRuntimeEventEventually(event, size)
		}
		return
	}
	// Non-streaming events (finals, approvals, tool boundaries) are ordered
	// behind any coalesced streaming event, then enqueue with a bounded wait:
	// they are rare and semantically should arrive, but a stalled UI must not
	// wedge the LLM callback forever. An assistant_message supersedes the
	// coalesced deltas for its turn, so stale pending text is dropped first.
	if isAssistantStreamTerminalEvent(event) {
		b.dropPendingStreamsForTerminal(event)
	} else {
		// The non-streaming event itself is allowed a 5s bounded wait below,
		// so flushing the coalesced tail with the same budget keeps ordering
		// and content intact whenever the consumer is merely slow. A truly
		// stalled UI still degrades to a bounded drop instead of wedging the
		// LLM callback forever.
		b.flushPendingStreamEventBounded(uiActionPostBudget)
	}
	if !b.enqueueNonStreamEvent(event, size, uiActionPostBudget) {
		b.logLateRuntimeEvent(event, "runtime event queue stalled; event dropped")
	}
}

// isCriticalSubagentLifecycleEvent identifies control-plane outcomes that
// must remain observable after their initiating turn has retired. Started and
// progress events intentionally remain ordinary; terminal state is the
// authoritative recovery record.
func isCriticalSubagentLifecycleEvent(eventType string) bool {
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case "subagent.completed", "subagent.denied",
		"subagent.batch.completed", "subagent.batch.failed",
		"subagent.batch.canceled", "subagent.batch.cancelled",
		"subagent.batch.timed_out", "subagent.batch.orphaned",
		"subagent.batch.circuit_open":
		return true
	default:
		return false
	}
}

func (b *chatRuntimeEventBridge) enqueueCriticalRuntimeEventEventually(event runtimeevents.Event, size int64) {
	if b == nil {
		return
	}
	b.progressMu.Lock()
	b.criticalPending++
	b.progressMu.Unlock()
	go func() {
		defer func() {
			b.progressMu.Lock()
			if b.criticalPending > 0 {
				b.criticalPending--
			}
			b.progressMu.Unlock()
		}()
		for !b.enqueueNonStreamEvent(event, size, 250*time.Millisecond) {
			// A terminal event is bounded in size and frequency. Retrying here is
			// safer than blocking the runtime publisher or losing the event; app
			// shutdown still terminates the process/goroutine naturally.
			time.Sleep(5 * time.Millisecond)
		}
	}()
}

// isMergeableStreamEvent reports whether the event carries monotonic streaming
// text that can be coalesced without loss: later deltas only extend (or, for
// snapshot-style providers, replace) earlier ones.
func isMergeableStreamEvent(eventType string) bool {
	switch eventType {
	case runtimechat.EventAssistantReasoning, runtimechat.EventAssistantDelta:
		return true
	default:
		return false
	}
}

// isAssistantStreamTerminalEvent reports whether the event is the authoritative
// snapshot that supersedes any coalesced streaming deltas for its turn.
func isAssistantStreamTerminalEvent(event runtimeevents.Event) bool {
	return event.Type == runtimechat.EventAssistantMessage
}

func streamMergeKey(event runtimeevents.Event) string {
	turnID, streamID := assistantEventIdentity(event)
	sequence, hasSequence := assistantEventSequence(event)
	key := event.Type + "\x00" + event.TraceID + "\x00" + turnID + "\x00" + streamID
	if hasSequence {
		key += "\x00" + strconv.FormatUint(sequence, 10)
	}
	return key
}

// enqueueStreamEvent coalesces streaming deltas while the consumer is
// behind, emitting them non-blockingly as soon as the consumer catches up
// (run() flushes after each consumed event). It never blocks the caller (the
// LLM stream callback).
func (b *chatRuntimeEventBridge) enqueueStreamEvent(event runtimeevents.Event, size int64) {
	b.streamMu.Lock()
	defer b.streamMu.Unlock()
	// The epoch is captured NOW (streamMu → renderMu, consistent with the
	// rest of the coalescing path) and stamped on the queued event: a delta
	// that arrives after EndRun belongs to the old run and must be rejected
	// by the consume-time epoch check, never "whitened" into the new run by
	// a later flush.
	b.renderMu.Lock()
	epoch := b.runEpoch
	b.renderMu.Unlock()
	q := chatRuntimeQueuedEvent{event: event, size: size, epoch: epoch}
	// Once any delta is waiting in pendingStreams, every later delta must
	// append to the same FIFO. The bounded queue can momentarily have room
	// while run() flushes between consumed events; sending directly then
	// would let a newer delta overtake older pending ones and corrupt
	// sequence-ordered assistant text.
	if n := len(b.pendingStreams); n > 0 {
		last := &b.pendingStreams[n-1]
		if streamMergeKey(last.event) == streamMergeKey(event) {
			// Bound the coalesced payload: a permanently stalled consumer
			// must not accumulate unbounded text. Past the byte limit the
			// new delta is dropped (logged) rather than blocking the
			// stream callback.
			if last.size+size > chatStreamCoalesceEventByteLimit ||
				b.pendingStreamsBytes+size > chatStreamCoalescePendingByteLimit {
				b.logLateRuntimeEvent(event, "coalesced stream payload exceeded byte limit; delta dropped")
				return
			}
			last.event = mergeStreamEvents(last.event, event)
			last.size += size
			if last.size < 1 {
				last.size = 1
			}
			b.pendingStreamsBytes += size
			return
		}
		// Contiguous sequence deltas of the same stream can be folded into
		// the last pending entry even though their merge keys differ by
		// sequence. Without this, a long stream behind a stalled consumer
		// exhausts the pending count budget with one entry per delta and the
		// tail is dropped, which then poisons sequence-ordered assembly.
		if contiguousStreamEvent(last.event, event) {
			if last.size+size > chatStreamCoalesceEventByteLimit ||
				b.pendingStreamsBytes+size > chatStreamCoalescePendingByteLimit {
				b.logLateRuntimeEvent(event, "coalesced stream payload exceeded byte limit; delta dropped")
				return
			}
			lastFrom, hasLastFrom := streamCoalescedFrom(last.event)
			if !hasLastFrom {
				if lastSeq, ok := assistantEventSequence(last.event); ok && lastSeq > 0 {
					lastFrom = lastSeq
					hasLastFrom = true
				}
			}
			last.event = mergeStreamEvents(last.event, event)
			if seq, ok := assistantEventSequence(event); ok {
				last.event.Payload["sequence"] = seq
			}
			if hasLastFrom {
				last.event.Payload[streamCoalescedFromKey] = lastFrom
			}
			last.size += size
			if last.size < 1 {
				last.size = 1
			}
			b.pendingStreamsBytes += size
			return
		}
		b.appendPendingStreamLocked(q)
		return
	}
	// Consumer idle and queue has room: send straight through; fall back to
	// the pending queue if a slot races away.
	if !b.streamQueueBusy() && b.trySendStreamEvent(&q) {
		return
	}
	b.appendPendingStreamLocked(q)
}

// contiguousStreamEvent reports whether incoming directly extends the pending
// entry's same stream: same type/trace/turn/stream and exactly the next
// sequence number. The two events keep distinct merge keys (sequence is part
// of the key), but coalescing them preserves order and text while using one
// bounded queue slot.
func contiguousStreamEvent(last, incoming runtimeevents.Event) bool {
	if last.Type != incoming.Type || last.TraceID != incoming.TraceID {
		return false
	}
	if !isMergeableStreamEvent(last.Type) || !isMergeableStreamEvent(incoming.Type) {
		return false
	}
	lastTurn, lastStream := assistantEventIdentity(last)
	incomingTurn, incomingStream := assistantEventIdentity(incoming)
	if lastTurn != incomingTurn || lastStream != incomingStream {
		return false
	}
	lastSeq, lastOK := assistantEventSequence(last)
	incomingSeq, incomingOK := assistantEventSequence(incoming)
	if !lastOK || !incomingOK || lastSeq == 0 || incomingSeq == 0 {
		return false
	}
	return incomingSeq > lastSeq && incomingSeq == lastSeq+1
}

// appendPendingStreamLocked bounds the coalesced backlog. Caller holds
// streamMu. Dropping under a stalled consumer is bounded degradation: the
// terminal assistant_message carries the authoritative snapshot.
func (b *chatRuntimeEventBridge) appendPendingStreamLocked(q chatRuntimeQueuedEvent) {
	if len(b.pendingStreams) >= chatStreamCoalescePendingLimit ||
		b.pendingStreamsBytes+q.size > chatStreamCoalescePendingByteLimit {
		b.logLateRuntimeEvent(q.event, "coalesced stream pending budget exceeded; delta dropped")
		return
	}
	b.pendingStreams = append(b.pendingStreams, q)
	b.pendingStreamsBytes += q.size
}

// streamQueueBusy reports whether the bounded queue or the in-flight
// consumer still holds retained events, i.e. whether the UI is behind.
func (b *chatRuntimeEventBridge) streamQueueBusy() bool {
	b.eventQueueMu.Lock()
	busy := len(b.eventQueue) > 0 || b.eventQueueBytes > 0
	b.eventQueueMu.Unlock()
	return busy
}

// flushPendingStreamEventBounded orders any coalesced streaming event before
// the next non-streaming event. It only waits a short budget: a stalled UI
// must not wedge the LLM callback behind the coalesced backlog, so remaining
// pending events are dropped (and logged) rather than blocking forever.
func (b *chatRuntimeEventBridge) flushPendingStreamEventBounded(budget time.Duration) {
	if b == nil {
		return
	}
	b.streamMu.Lock()
	defer b.streamMu.Unlock()
	b.flushPendingStreamBoundedLocked(budget)
}

// flushPendingStreamBoundedLocked drains the pending queue head while the
// bounded queue accepts events, stopping at the deadline. Caller holds
// streamMu.
func (b *chatRuntimeEventBridge) flushPendingStreamBoundedLocked(budget time.Duration) {
	if budget <= 0 {
		return
	}
	deadline := time.Now().Add(budget)
	for len(b.pendingStreams) > 0 {
		q := b.pendingStreams[0]
		if b.trySendStreamEvent(&q) {
			b.pendingStreams = b.pendingStreams[1:]
			b.pendingStreamsBytes -= q.size
			if b.pendingStreamsBytes < 0 {
				b.pendingStreamsBytes = 0
			}
			continue
		}
		if time.Now().After(deadline) {
			b.dropAllPendingStreamsLocked("coalesced stream flush budget exceeded")
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// tryFlushPendingLocked non-blockingly drains the pending queue head while
// the bounded queue accepts events. Caller holds streamMu.
func (b *chatRuntimeEventBridge) tryFlushPendingLocked() {
	for len(b.pendingStreams) > 0 {
		q := b.pendingStreams[0]
		if !b.trySendStreamEvent(&q) {
			return
		}
		b.pendingStreams = b.pendingStreams[1:]
		b.pendingStreamsBytes -= q.size
		if b.pendingStreamsBytes < 0 {
			b.pendingStreamsBytes = 0
		}
	}
}

// dropPendingStreamsForTerminal drops coalesced deltas superseded by an
// assistant_message. The terminal snapshot carries the full content, so a
// stalled UI must not wait behind deltas that would only be thrown away.
func (b *chatRuntimeEventBridge) dropPendingStreamsForTerminal(event runtimeevents.Event) {
	b.streamMu.Lock()
	defer b.streamMu.Unlock()
	turnID, streamID := assistantEventIdentity(event)
	if turnID == "" && streamID == "" {
		b.dropAllPendingStreamsLocked("assistant terminal supersedes coalesced stream")
		return
	}
	kept := b.pendingStreams[:0]
	dropped := int64(0)
	for _, q := range b.pendingStreams {
		pTurnID, pStreamID := assistantEventIdentity(q.event)
		if pTurnID == turnID && (streamID == "" || pStreamID == streamID) {
			b.logLateRuntimeEvent(q.event, "assistant terminal superseded stale coalesced stream")
			dropped += q.size
			continue
		}
		kept = append(kept, q)
	}
	b.pendingStreams = kept
	if dropped > 0 {
		b.pendingStreamsBytes -= dropped
		if b.pendingStreamsBytes < 0 {
			b.pendingStreamsBytes = 0
		}
	}
}

// dropAllPendingStreamsLocked discards the whole coalesced backlog and logs
// each dropped event. Caller holds streamMu.
func (b *chatRuntimeEventBridge) dropAllPendingStreamsLocked(reason string) {
	for _, q := range b.pendingStreams {
		b.logLateRuntimeEvent(q.event, reason)
	}
	b.pendingStreams = nil
	b.pendingStreamsBytes = 0
}

// enqueueNonStreamEvent enqueues a non-streaming event with a bounded wait.
// Streaming events are already non-blocking; this last-resort backpressure
// keeps rare terminal/control events reliable without allowing a stalled UI
// to wedge the LLM callback forever.
func (b *chatRuntimeEventBridge) enqueueNonStreamEvent(event runtimeevents.Event, size int64, wait time.Duration) bool {
	if b == nil {
		return false
	}
	if size < 1 {
		size = 1
	}
	deadline := time.Now().Add(wait)
	for {
		if b.tryReserveEventQueueBytes(size, isCriticalSubagentLifecycleEvent(event.Type)) {
			b.renderMu.Lock()
			epoch := b.runEpoch
			b.renderMu.Unlock()
			select {
			case b.eventQueue <- chatRuntimeQueuedEvent{event: event, size: size, epoch: epoch}:
				b.progressMu.Lock()
				b.enqueuedEvents++
				b.progressMu.Unlock()
				return true
			default:
				b.releaseEventQueueBytes(size)
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
}

// tryReserveEventQueueBytes is the non-blocking form of
// reserveEventQueueBytes. Returns true when the byte budget admits the event.
func (b *chatRuntimeEventBridge) tryReserveEventQueueBytes(size int64, critical bool) bool {
	b.eventQueueMu.Lock()
	defer b.eventQueueMu.Unlock()
	if size < 1 {
		size = 1
	}
	if !critical && len(b.eventQueue) >= b.normalEventQueueCapacity() {
		return false
	}
	if !critical && b.eventQueueByteLimit > 0 && b.eventQueueBytes > 0 && size > b.eventQueueByteLimit-b.eventQueueBytes {
		return false
	}
	b.eventQueueBytes += size
	return true
}

func (b *chatRuntimeEventBridge) normalEventQueueCapacity() int {
	capacity := cap(b.eventQueue)
	if capacity > chatRuntimeEventQueueCriticalReserve {
		return capacity - chatRuntimeEventQueueCriticalReserve
	}
	return capacity
}

// trySendStreamEvent attempts a non-blocking enqueue. Returns true when the
// event was accepted by the queue. The epoch on q was stamped at enqueue
// time and is preserved (see enqueueStreamEvent).
func (b *chatRuntimeEventBridge) trySendStreamEvent(q *chatRuntimeQueuedEvent) bool {
	if len(b.eventQueue) >= b.normalEventQueueCapacity() {
		return false
	}
	select {
	case b.eventQueue <- *q:
		// Streaming events are exempt from the blocking byte budget but are
		// still accounted so coalescing can detect a lagging consumer and so
		// run() releases a symmetric amount.
		b.accountEventQueueBytes(q.size)
		b.progressMu.Lock()
		b.enqueuedEvents++
		b.progressMu.Unlock()
		return true
	default:
		return false
	}
}

// accountEventQueueBytes tracks retained bytes without blocking on the byte
// budget (see trySendStreamEvent).
func (b *chatRuntimeEventBridge) accountEventQueueBytes(size int64) {
	b.eventQueueMu.Lock()
	b.eventQueueBytes += size
	b.eventQueueMu.Unlock()
}

// mergeStreamEvents combines two deltas of the same stream into one event.
// Text is merged with the same monotonic rules the encoder applies per chunk
// (snapshot providers repeat the full text; delta providers append), so the
// coalesced event renders identically to the original sequence.
func mergeStreamEvents(a, b runtimeevents.Event) runtimeevents.Event {
	if streamEventText(b) == "" {
		return a
	}
	if streamEventText(a) == "" {
		return b
	}
	merged := mergeStreamText(streamEventText(a), streamEventText(b))
	// A snapshot-style reasoning block would shadow the merged text and can
	// only represent the older snapshot; drop it so the text path wins.
	delete(a.Payload, "reasoning")
	switch a.Type {
	case runtimechat.EventAssistantReasoning:
		a.Payload["text"] = merged
	case runtimechat.EventAssistantDelta:
		a.Payload["delta"] = merged
	}
	return a
}

// streamEventText extracts the streaming text carried by a coalescible event.
// Unlike payloadStringValue it does NOT trim: leading whitespace is part of
// the delta payload and must survive coalescing byte-for-byte.
func streamEventText(event runtimeevents.Event) string {
	switch event.Type {
	case runtimechat.EventAssistantReasoning:
		if s, ok := event.Payload["text"].(string); ok {
			return s
		}
		if s, ok := event.Payload["summary"].(string); ok {
			return s
		}
	case runtimechat.EventAssistantDelta:
		if s, ok := event.Payload["delta"].(string); ok {
			return s
		}
		if s, ok := event.Payload["content"].(string); ok {
			return s
		}
	}
	return ""
}

// mergeStreamText extends existing with incoming under the same monotonic
// rules as the encoder's per-chunk append: identical/superset incoming wins,
// otherwise the incoming delta is appended.
func mergeStreamText(existing, incoming string) string {
	if incoming == "" || incoming == existing {
		return existing
	}
	if existing == "" {
		return incoming
	}
	if strings.HasPrefix(incoming, existing) {
		return incoming
	}
	return existing + incoming
}

func (b *chatRuntimeEventBridge) run() {
	for queued := range b.eventQueue {
		b.handleQueuedEvent(queued)
		b.progressMu.Lock()
		b.processedEvents++
		b.progressMu.Unlock()
		queued.event = runtimeevents.Event{}
		b.releaseEventQueueBytes(queued.size)
		// The queue just freed a slot: opportunistically flush any coalesced
		// streaming event so a stall ends as soon as the consumer catches up,
		// without waiting for the next Handle call.
		b.flushPendingStreamEventIfAble()
	}
}

// flushPendingStreamEventIfAble non-blockingly enqueues coalesced streaming
// events once the consumer has freed a slot.
func (b *chatRuntimeEventBridge) flushPendingStreamEventIfAble() {
	b.streamMu.Lock()
	defer b.streamMu.Unlock()
	b.tryFlushPendingLocked()
}

func (b *chatRuntimeEventBridge) handleQueuedEvent(queued chatRuntimeQueuedEvent) {
	if b == nil {
		return
	}
	b.renderMu.Lock()
	currentEpoch := b.runEpoch
	b.renderMu.Unlock()
	if queued.epoch != currentEpoch && !isCriticalSubagentLifecycleEvent(queued.event.Type) {
		b.logLateRuntimeEvent(queued.event, "stale local run epoch")
		return
	}
	// Keep the bridge queue as the bounded runtime ingress, but let the UI actor
	// serialize ordinary runtime-driven UI mutations with timer and surface
	// actions. Approval/question events still have to execute on this worker
	// because their legacy prompt adapters synchronously own stdin. Before that
	// exception runs, drain every earlier actor action posted by this queue. A
	// mutex alone does not preserve mailbox order: without this barrier a later
	// approval can enter the encoder/Scene while an earlier assistant/tool event
	// is still waiting in the actor mailbox.
	if runtimeEventRequiresLegacyInteraction(queued.event) {
		if coordinator := b.sessionInteraction(); coordinator != nil {
			if !coordinator.waitUIActorIdleBounded("runtime legacy interaction barrier") {
				b.logLateRuntimeEvent(queued.event, "legacy interaction barrier timeout; event dropped")
				return
			}
		}
		b.handleEvent(queued.event)
		return
	}
	if accepted, legacyOK := b.postRuntimeEventToUIActorWithEpoch(queued.event, queued.epoch); accepted {
		return
	} else if isCriticalSubagentLifecycleEvent(queued.event.Type) {
		// If the UI actor is already closed/replaced, retain the control-plane
		// terminal in the legacy/Scene path instead of silently losing it. The
		// critical actor admission path never times out merely due to a full
		// mailbox, so this fallback cannot overtake queued actor predecessors.
		b.handleEvent(queued.event)
		return
	} else if legacyOK && b.isRunEpochCurrent(queued.epoch) {
		// Only fall back to the legacy surface while the captured epoch is
		// still current. After EndRun seals the epoch, a rejected post must
		// be dropped, never rendered through a second path. A mailbox stall
		// (legacyOK=false) is likewise dropped: rendering it through the
		// legacy surface would race the queued predecessor events.
		b.handleEvent(queued.event)
	}
}

func runtimeEventRequiresLegacyInteraction(event runtimeevents.Event) bool {
	switch event.Type {
	case runtimechat.EventApprovalRequested, runtimechat.EventQuestionAsked:
		return true
	default:
		return false
	}
}

func (b *chatRuntimeEventBridge) postRuntimeEventToUIActor(event runtimeevents.Event) bool {
	if b == nil {
		return false
	}
	b.renderMu.Lock()
	epoch := b.runEpoch
	b.renderMu.Unlock()
	accepted, _ := b.postRuntimeEventToUIActorWithEpoch(event, epoch)
	return accepted
}

// postRuntimeEventToUIActorWithEpoch enqueues the event on the UI actor
// mailbox. Returns (accepted, legacyOK): accepted=true means the event was
// handed to the actor; legacyOK=true means the caller may fall back to the
// legacy render surface when the actor path is unavailable (nil session), and
// legacyOK=false means the event must be DROPPED (shutdown, stale epoch, or a
// mailbox stall) — rendering it through the legacy surface then would race
// the events already queued in the mailbox.
func (b *chatRuntimeEventBridge) postRuntimeEventToUIActorWithEpoch(event runtimeevents.Event, epoch uint64) (bool, bool) {
	if b == nil || b.session == nil || b.session.Interaction == nil {
		return false, true
	}
	coordinator := b.session.Interaction
	action := ui.RuntimeEvent{
		Kind: chatUIRuntimeEventActionKind,
		Payload: chatRuntimeEventUIAction{
			bridge: b,
			event:  event,
			epoch:  epoch,
		},
	}
	// Bounded retry with the non-blocking TryPost ingress. The bridge worker
	// may wait for a briefly full mailbox so a coalesced streaming tail is not
	// lost right before llm.finished, while the LLM callback stays decoupled:
	// while this worker waits, Handle coalesces new deltas into pendingStreams
	// instead of blocking. A permanently stalled reducer (pathological
	// markdown, terminal output, …) still drops the event after the budget and
	// the run moves on. Approval/question events never reach this loop (they
	// run through the legacy interaction barrier instead), so interactive
	// prompts are unaffected.
	timeout := b.uiActionPostTimeout
	if timeout <= 0 {
		timeout = uiActionPostBudget
	}
	waitDeadline := time.Now().Add(timeout)
	for {
		if coordinator.tryPostUIAction(action) {
			return true, true
		}
		if coordinator.uiActionRejectedAfterShutdown() {
			return false, false
		}
		if coordinator.uiActor == nil || coordinator.uiActor.Stats().Closed {
			return false, false
		}
		// A bounded mailbox is normal backpressure, but waiting for it must
		// never outlive the run that owns this event. EndRun seals the epoch
		// after its drain timeout, which releases this loop.
		if !isCriticalSubagentLifecycleEvent(event.Type) && !b.isRunEpochCurrent(epoch) {
			return false, false
		}
		if time.Now().After(waitDeadline) {
			if isCriticalSubagentLifecycleEvent(event.Type) {
				b.logLateRuntimeEvent(event, "UI actor mailbox stalled; critical lifecycle event remains pending")
				waitDeadline = time.Now().Add(timeout)
				continue
			}
			reason := "UI actor mailbox stalled; event dropped"
			if isMergeableStreamEvent(event.Type) {
				reason = "UI actor mailbox full; streaming event dropped after bounded wait"
			}
			b.logLateRuntimeEvent(event, reason)
			return false, false
		}
		// The UI actor releases a slot as soon as it finishes the current
		// action; polling keeps the wait genuinely bounded without blocking on
		// the mailbox cond.
		time.Sleep(time.Millisecond)
	}
}

// encodeRenderModelEvent 把事件送入统一渲染编码器（双跑模式数据面）。
// renderMu 与 submitUserInput 串行化编码器访问：事件循环 goroutine 与
// coordinator 渲染 goroutine 会并发调用编码器（编码器非线程安全）。
func (b *chatRuntimeEventBridge) encodeRenderModelEvent(event runtimeevents.Event) {
	if b == nil || b.renderEncoder == nil {
		return
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	b.applyChangeSet(b.renderEncoder.Encode(event))
	b.appendEventLog(event)
}

// submitUserInput 把用户输入消息注入统一渲染数据面（切片 10）：用户输入
// 没有 runtime 事件类型（事件流无 user 事件，见 encoder.SubmitUserInput），
// 由 coordinator 渲染层在用户块提交前直连调用；编码为 KindUser 终态块，
// 走 applyChangeSet 同一提交路径，并落事件日志（replay 幂等恢复）。
// renderMu 与事件循环串行化（编码器非线程安全）。
func (b *chatRuntimeEventBridge) submitUserInput(text string) {
	if b == nil || b.renderEncoder == nil || strings.TrimSpace(text) == "" {
		return
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	b.applyChangeSet(b.renderEncoder.SubmitUserInput(text))
	b.appendUserInputLog(text)
}

// submitAssistant 把没有 runtime assistant 终态事件的 direct response 接入
// Scene 数据面。runtime event path 已经 Encode 的回复不得调用该入口。
func (b *chatRuntimeEventBridge) submitAssistant(text string) {
	b.submitAssistantWithBoundaryGroup(text, newAssistantBoundaryGroupKey())
}

// submitAssistantWithBoundaryGroup keeps the direct-response Scene item and
// the legacy presentation cell on the same exact-request boundary identity.
// The identity is persisted for replay and is deliberately independent of
// CauseID/ChainKey tool ownership.
func (b *chatRuntimeEventBridge) submitAssistantWithBoundaryGroup(text, boundaryGroupKey string) {
	if b == nil || b.renderEncoder == nil || strings.TrimSpace(text) == "" {
		return
	}
	if boundaryGroupKey == "" {
		boundaryGroupKey = newAssistantBoundaryGroupKey()
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	b.applyChangeSet(b.renderEncoder.SubmitAssistantWithBoundaryGroup(text, boundaryGroupKey))
	b.appendAssistantLog(text, boundaryGroupKey)
}

// submitCommand 把本地命令执行结果注入统一渲染数据面（设计文档 §1.3 行
// 9/10）：命令执行没有 runtime 事件类型，由 coordinator 渲染层在命令结果
// cell 提交点直连调用；编码为 KindCommand 终态块，走 applyChangeSet 同一
// 提交路径，并落事件日志（replay 幂等恢复）。
func (b *chatRuntimeEventBridge) submitCommand(text string) {
	b.submitCommandDocument(text, render.Document{})
}

// submitCommandDocument preserves the structured command IR across the
// encoder/Scene boundary. The plain head remains the compatibility projection
// used by parity probes and old event-log readers.
func (b *chatRuntimeEventBridge) submitCommandDocument(text string, document render.Document) {
	if b == nil || b.renderEncoder == nil || strings.TrimSpace(text) == "" {
		return
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	b.applyChangeSet(b.renderEncoder.SubmitCommandDocument(text, document))
	b.appendCommandLogDocument(text, document)
}

// submitError 把操作错误注入统一渲染数据面（设计文档 §1.3 行 11）：本地
// 命令/工具错误没有 runtime 事件类型，由 coordinator 渲染层在错误块提交
// 点直连调用；编码为 KindSystem 终态块（会话/诊断语义），走 applyChangeSet
// 同一提交路径，并落事件日志（replay 幂等恢复）。
func (b *chatRuntimeEventBridge) submitError(text string) {
	if b == nil || b.renderEncoder == nil || strings.TrimSpace(text) == "" {
		return
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	b.applyChangeSet(b.renderEncoder.SubmitError(text))
	b.appendErrorLog(text)
}

// submitSupplement 把没有 runtime 事件对应物的本地补充接入统一 Scene
// 数据面。runtime event bridge 已经先 Encode 的事件不得走此入口，否则会
// 在 Scene 中产生第二个 semantic cell。
func (b *chatRuntimeEventBridge) submitSupplement(text string) {
	if b == nil || b.renderEncoder == nil || strings.TrimSpace(text) == "" {
		return
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	b.applyChangeSet(b.renderEncoder.SubmitSupplement(text))
	b.appendSupplementLog(text)
}

func (b *chatRuntimeEventBridge) sessionInteractionSnapshot() {
	if b != nil && b.session != nil && b.session.Interaction != nil {
		b.session.Interaction.postTranscriptSnapshotFromBridge(b)
	}
}

// submitPriorityTranscript finalizes the mutable Scene item created by the
// matching approval/question runtime event. It returns false for a missing or
// already-finalized target; callers must then use a generic local supplement
// only when no runtime target exists.
func (b *chatRuntimeEventBridge) submitPriorityTranscript(eventType, requestKey, text string) bool {
	if b == nil || b.renderEncoder == nil || strings.TrimSpace(text) == "" {
		return false
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	cs := b.renderEncoder.SubmitPriorityPromptTranscript(eventType, requestKey, text)
	if cs == nil || len(cs.Changes) == 0 {
		return false
	}
	b.applyChangeSet(cs)
	b.appendPriorityTranscriptLog(eventType, requestKey, text)
	return true
}

func (b *chatRuntimeEventBridge) currentPriorityTranscriptTarget() chatRuntimePriorityTranscriptTarget {
	if b == nil {
		return chatRuntimePriorityTranscriptTarget{}
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	return b.priorityTranscriptTarget
}

func (b *chatRuntimeEventBridge) setPriorityTranscriptTarget(event runtimeevents.Event) chatRuntimePriorityTranscriptTarget {
	if b == nil {
		return chatRuntimePriorityTranscriptTarget{}
	}
	target := chatRuntimePriorityTranscriptTarget{
		eventType:  event.Type,
		requestKey: encoding.PriorityPromptRequestKey(event),
	}
	b.renderMu.Lock()
	b.priorityTranscriptTarget = target
	b.renderMu.Unlock()
	return target
}

func (b *chatRuntimeEventBridge) clearPriorityTranscriptTarget(target chatRuntimePriorityTranscriptTarget) {
	if b == nil {
		return
	}
	b.renderMu.Lock()
	if b.priorityTranscriptTarget == target {
		b.priorityTranscriptTarget = chatRuntimePriorityTranscriptTarget{}
	}
	b.renderMu.Unlock()
}

// submitToolRequested 将 chat-core 的 direct tool 请求接入统一编码器。
// 与 runtime event 一样，编码器负责稳定 call identity 与 mutable chain
// 生命周期；调用方不得再为同一请求调用 submitSupplement。
func (b *chatRuntimeEventBridge) submitToolRequested(toolCallID, toolName string, args map[string]interface{}) {
	if b == nil || b.renderEncoder == nil || strings.TrimSpace(toolCallID) == "" || strings.TrimSpace(toolName) == "" {
		return
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	payload := cloneRuntimeEventLogPayload(args)
	payload["tool_call_id"] = strings.TrimSpace(toolCallID)
	payload["tool_name"] = strings.TrimSpace(toolName)
	event := runtimeevents.Event{Type: "tool.requested", ToolName: toolName, Payload: payload}
	b.applyChangeSet(b.renderEncoder.SubmitToolCall(toolCallID, toolName, args))
	b.appendEventLog(event)
}

// submitToolProgress 更新 direct tool 的 mutable source。进度没有独立
// committed cell，不产生额外 transcript boundary。
func (b *chatRuntimeEventBridge) submitToolProgress(toolCallID, toolName, progress string) {
	if b == nil || b.renderEncoder == nil || strings.TrimSpace(toolCallID) == "" || strings.TrimSpace(progress) == "" {
		return
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	event := runtimeevents.Event{
		Type:     "tool.progress",
		ToolName: toolName,
		Payload:  map[string]interface{}{"tool_call_id": strings.TrimSpace(toolCallID), "tool_name": strings.TrimSpace(toolName), "progress": progress},
	}
	b.applyChangeSet(b.renderEncoder.SubmitToolProgress(toolCallID, toolName, progress))
	b.appendEventLog(event)
}

// submitToolResult 完成 direct tool chain。结果与请求共享一个 Scene cell，
// 并由 Encode 路径落入同一 runtime event log，replay 时不再另造 supplement。
func (b *chatRuntimeEventBridge) submitToolResult(toolCallID, toolName, output, toolErr string, success bool) {
	if b == nil || b.renderEncoder == nil || strings.TrimSpace(toolCallID) == "" {
		return
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	payload := map[string]interface{}{
		"tool_call_id": strings.TrimSpace(toolCallID),
		"tool_name":    strings.TrimSpace(toolName),
		"success":      success,
	}
	if strings.TrimSpace(output) != "" {
		payload["output"] = output
	}
	if strings.TrimSpace(toolErr) != "" {
		payload["error"] = toolErr
	}
	typeName := "tool.completed"
	if !success {
		typeName = "tool.failed"
	}
	event := runtimeevents.Event{Type: typeName, ToolName: toolName, Payload: payload}
	b.applyChangeSet(b.renderEncoder.SubmitToolResult(toolCallID, toolName, output, toolErr, success))
	b.appendEventLog(event)
}

// submitToolResultDisplay completes a chat-core tool from its already
// normalized transcript block. The raw result remains in the event log for
// diagnostics, while display_head is the authoritative transcript source used
// by both live Scene mapping and replay.
func (b *chatRuntimeEventBridge) submitToolResultDisplay(toolCallID, toolName, output, toolErr string, success bool, display string) {
	if b == nil || b.renderEncoder == nil || strings.TrimSpace(toolCallID) == "" || strings.TrimSpace(display) == "" {
		return
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	payload := map[string]interface{}{
		"tool_call_id": strings.TrimSpace(toolCallID),
		"tool_name":    strings.TrimSpace(toolName),
		"success":      success,
		"display_head": display,
	}
	if strings.TrimSpace(output) != "" {
		payload["output"] = output
	}
	if strings.TrimSpace(toolErr) != "" {
		payload["error"] = toolErr
	}
	typeName := "tool.completed"
	if !success {
		typeName = "tool.failed"
	}
	event := runtimeevents.Event{Type: typeName, ToolName: toolName, Payload: payload}
	b.applyChangeSet(b.renderEncoder.SubmitToolResultDisplay(toolCallID, display))
	b.appendEventLog(event)
}

// submitUserInteraction 把 /debug、/model 等用户交互输出注入统一渲染数据面
// （设计文档 §1.3 行 12 / §5.5）：以触发时刻捕获的模型尾部锚点（anchor）
// 为界插入渲染序列，不进入编码器因果链（不分配 CauseID）。编码为
// KindUserInteraction 终态块，走 applyChangeSet 同一提交路径，并落事件
// 日志（replay 幂等恢复；anchor 为值类型副本，重放时按全序重建等价位置）。
func (b *chatRuntimeEventBridge) submitUserInteraction(text string, anchor *encoding.Tail) {
	b.submitUserInteractionDocument(text, render.Document{}, anchor)
}

func (b *chatRuntimeEventBridge) submitUserInteractionDocument(text string, document render.Document, anchor *encoding.Tail) {
	if b == nil || b.renderEncoder == nil || strings.TrimSpace(text) == "" {
		return
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	b.applyChangeSet(b.renderEncoder.SubmitUserInteractionDocument(text, document, anchor))
	b.appendInteractionLogDocument(text, document, anchor)
}

// submitCommandResult 是 RenderCommandDocument 提交点的统一注入入口：
// 有 pending 交互标记（/debug、/model 等，经 recordInteractionAnchor 登记）
// 时按交互锚定语义注入；否则按普通命令结果注入（submitCommand）。
// 锚点为 nil（模型为空时触发）时由编码器退化为 append。
func (b *chatRuntimeEventBridge) submitCommandResult(text string, documents ...render.Document) {
	if b == nil || strings.TrimSpace(text) == "" {
		return
	}
	document := render.Document{}
	if len(documents) > 0 {
		document = documents[0].Clone()
	}
	if source, tail := b.consumePendingInteraction(); source != "" {
		b.submitUserInteractionDocument(text, document, tail)
		return
	}
	b.submitCommandDocument(text, document)
}

// applyChangeSet 把编码器产出的 ChangeSet 映射为 SceneTransaction 并提交
// （路线图 P3：渲染层只消费 ChangeSet）。映射器有状态（chainHeads），
// 必须按事件顺序在单 goroutine 内串行调用；失败只计数并记录最后错误，
// 不阻塞事件循环（双跑/旁路模式，旧渲染路径继续输出，切换以快照与
// 统计审计为准）。Scene 由 sceneMu 保护，/debug 等诊断读可并发取快照。
func (b *chatRuntimeEventBridge) applyChangeSet(cs *encoding.ChangeSet) {
	if b == nil || cs == nil {
		return
	}
	b.sceneMu.Lock()
	defer b.sceneMu.Unlock()
	if b.renderScene == nil || b.renderMapper == nil {
		b.renderScene = scene.New()
		b.renderMapper = scene.NewChangeSetMapper(b.renderScene)
	}
	if _, _, err := b.renderMapper.Apply(cs); err != nil {
		b.sceneApplyFailures++
		b.sceneLastError = err.Error()
	}
}

// eventLogFilePath 返回事件日志文件路径（测试可注入覆盖）。
func (b *chatRuntimeEventBridge) eventLogFilePath() string {
	if b == nil {
		return ""
	}
	if b.eventLogPathOverride != "" {
		return b.eventLogPathOverride
	}
	if b.session == nil || b.session.Logger == nil {
		return ""
	}
	sessionDir := b.session.Logger.SessionDirPath()
	if sessionDir == "" {
		return ""
	}
	return filepath.Join(sessionDir, "runtime-events.jsonl")
}

// appendEventLog 把事件 JSON 追加到事件日志（best-effort：失败只计数，不阻塞事件循环）。
// 日志为 append-only，同一会话重启后由 replayEventLog 幂等重建模型。
func (b *chatRuntimeEventBridge) appendEventLog(event runtimeevents.Event) {
	if b == nil {
		return
	}
	line, err := json.Marshal(event)
	if err != nil {
		b.eventLogMu.Lock()
		b.eventLogFailures++
		b.eventLogMu.Unlock()
		return
	}
	b.appendEventLogLine(line)
}

// eventLogInjection 是事件日志中"直连注入记录"的落盘格式。注入内容没有
// runtime 事件类型（用户输入 / direct assistant / 命令结果 / 操作错误 / 本地补充 / 用户交互输出），以
// 独立记录行与事件行区分：runtimeevents.Event 的 Type 恒非空，Type 为空
// 的行即注入记录（见 submitUserInput / submitCommand / submitError /
// submitUserInteraction）；具体类别由非空字段判别。旧日志的用户输入行
// （{"user_input": "..."}）仍可解析（字段兼容）。
type eventLogInjection struct {
	// HistoryReset is a durable projection boundary for destructive mutations
	// such as backtrack. Earlier append-only event rows remain in the file for
	// audit, but replay must discard their derived model/Scene before seeding
	// the supplied canonical history.
	HistoryReset           bool                   `json:"history_reset,omitempty"`
	History                []runtimetypes.Message `json:"history,omitempty"`
	HistoryResetHeader     string                 `json:"history_reset_header,omitempty"`
	UserInput              string                 `json:"user_input,omitempty"`
	Assistant              string                 `json:"assistant,omitempty"`
	AssistantBoundaryGroup string                 `json:"assistant_boundary_group,omitempty"`
	Command                string                 `json:"command,omitempty"`
	Error                  string                 `json:"error,omitempty"`
	Supplement             string                 `json:"supplement,omitempty"`
	PriorityKind           string                 `json:"priority_kind,omitempty"`
	PriorityKey            string                 `json:"priority_key,omitempty"`
	PriorityTranscript     string                 `json:"priority_transcript,omitempty"`
	Interaction            string                 `json:"interaction,omitempty"`        // /debug、/model 等交互输出
	InteractionAnchor      *encoding.Tail         `json:"interaction_anchor,omitempty"` // 触发时刻模型尾部锚点
	// Document is the terminal-neutral render IR for command/interaction
	// injections. It is optional for backward compatibility with old JSONL
	// records that only persisted the plain text projection.
	Document *render.Document `json:"document,omitempty"`
}

// appendHistoryResetLog persists a canonical transcript boundary. Caller must
// hold renderMu so a later command/result injection is ordered after the
// replacement marker in both the live Scene and the event log.
func (b *chatRuntimeEventBridge) appendHistoryResetLog(messages []runtimetypes.Message, header string) {
	if b == nil {
		return
	}
	history := make([]runtimetypes.Message, 0, len(messages))
	for _, message := range messages {
		history = append(history, *message.Clone())
	}
	b.appendInjectionLog(eventLogInjection{
		HistoryReset:       true,
		History:            history,
		HistoryResetHeader: header,
	})
}

// appendInjectionLog 把一条注入记录追加到事件日志（与事件行同一全序，
// 保证 replay 重建顺序与实时路径一致）。
func (b *chatRuntimeEventBridge) appendInjectionLog(rec eventLogInjection) {
	if b == nil {
		return
	}
	line, err := json.Marshal(rec)
	if err != nil {
		b.eventLogMu.Lock()
		b.eventLogFailures++
		b.eventLogMu.Unlock()
		return
	}
	b.appendEventLogLine(line)
}

// appendUserInputLog 追加用户输入注入记录。
func (b *chatRuntimeEventBridge) appendUserInputLog(text string) {
	b.appendInjectionLog(eventLogInjection{UserInput: text})
}

// appendAssistantLog 追加 direct assistant 终态注入记录及其边界身份。
func (b *chatRuntimeEventBridge) appendAssistantLog(text, boundaryGroupKey string) {
	b.appendInjectionLog(eventLogInjection{
		Assistant: text, AssistantBoundaryGroup: boundaryGroupKey,
	})
}

// appendCommandLog 追加命令结果注入记录。
func (b *chatRuntimeEventBridge) appendCommandLog(text string) {
	b.appendCommandLogDocument(text, render.Document{})
}

func (b *chatRuntimeEventBridge) appendCommandLogDocument(text string, document render.Document) {
	rec := eventLogInjection{Command: text}
	if len(document.Blocks) > 0 {
		doc := document.Clone()
		rec.Document = &doc
	}
	b.appendInjectionLog(rec)
}

// appendErrorLog 追加操作错误注入记录。
func (b *chatRuntimeEventBridge) appendErrorLog(text string) {
	b.appendInjectionLog(eventLogInjection{Error: text})
}

// appendSupplementLog 追加本地补充注入记录。
func (b *chatRuntimeEventBridge) appendSupplementLog(text string) {
	b.appendInjectionLog(eventLogInjection{Supplement: text})
}

// appendPriorityTranscriptLog records completion of the already-encoded
// approval/question item. Replay uses the request identity to update that
// item rather than append a duplicate transcript cell.
func (b *chatRuntimeEventBridge) appendPriorityTranscriptLog(eventType, requestKey, text string) {
	b.appendInjectionLog(eventLogInjection{
		PriorityKind:       eventType,
		PriorityKey:        requestKey,
		PriorityTranscript: text,
	})
}

// appendInteractionLog 追加用户交互输出注入记录（含触发时刻锚点；重放时
// 按全序重建等价插入位置）。
func (b *chatRuntimeEventBridge) appendInteractionLog(text string, anchor *encoding.Tail) {
	b.appendInteractionLogDocument(text, render.Document{}, anchor)
}

func (b *chatRuntimeEventBridge) appendInteractionLogDocument(text string, document render.Document, anchor *encoding.Tail) {
	rec := eventLogInjection{Interaction: text, InteractionAnchor: anchor}
	if len(document.Blocks) > 0 {
		doc := document.Clone()
		rec.Document = &doc
	}
	b.appendInjectionLog(rec)
}

// appendEventLogLine 写入单行日志记录（open→append→close）。
func (b *chatRuntimeEventBridge) appendEventLogLine(line []byte) {
	if b == nil {
		return
	}
	path := b.eventLogFilePath()
	if path == "" {
		return
	}
	b.eventLogMu.Lock()
	defer b.eventLogMu.Unlock()
	// 每次 open→append→close：不持有长生命周期句柄，避免文件占用
	// （事件频率低，open/close 开销可忽略）。
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		b.eventLogFailures++
		return
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		b.eventLogFailures++
		return
	}
	b.eventLogCount++
}

// replayEventLog 从事件日志重放全部记录（事件 + 用户输入，保持全序），
// 幂等重建渲染模型与 Scene 数据面（重放前模型必须为空，即新 bridge；
// Scene 同样从空重建，保证恢复后的 Scene 与实时路径等价）。返回重放
// 记录数；日志不存在时静默返回 0。
func (b *chatRuntimeEventBridge) replayEventLog() (uint64, error) {
	if b == nil || b.renderEncoder == nil {
		return 0, nil
	}
	path := b.eventLogFilePath()
	if path == "" {
		return 0, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		b.eventLogMu.Lock()
		b.eventLogFailures++
		b.eventLogMu.Unlock()
		return 0, err
	}
	type entry struct {
		historyReset           bool
		history                []runtimetypes.Message
		historyResetHeader     string
		event                  runtimeevents.Event
		userInput              string          // 非空表示用户输入注入记录（无 runtime 事件类型）
		assistant              string          // 非空表示 direct assistant 终态注入记录
		assistantBoundaryGroup string          // 可选；旧日志为空时保持独立块语义
		command                string          // 非空表示命令结果注入记录
		err                    string          // 非空表示操作错误注入记录
		supplement             string          // 非空表示本地补充注入记录
		priorityKind           string          // approval_requested/question_asked
		priorityKey            string          // request_id/question_id-derived key
		priorityTranscript     string          // retained prompt + answer
		interaction            string          // 非空表示用户交互输出注入记录
		interactionAnchor      *encoding.Tail  // 交互输出触发时刻锚点
		document               render.Document // 可选结构化 command/interaction IR
	}
	var entries []entry
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	lineIndex := 0
	for scanner.Scan() {
		lineIndex++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var ev runtimeevents.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			b.eventLogMu.Lock()
			b.eventLogFailures++
			b.eventLogMu.Unlock()
			return uint64(lineIndex - 1), fmt.Errorf("event log line %d: %w", lineIndex, err)
		}
		if ev.Type == "" {
			// 注入记录行（runtimeevents.Event 的 Type 恒非空）：按非空
			// 字段判别类别（用户输入 / 命令结果 / 操作错误 / 交互输出）。
			var inj eventLogInjection
			if err := json.Unmarshal([]byte(line), &inj); err != nil {
				b.eventLogMu.Lock()
				b.eventLogFailures++
				b.eventLogMu.Unlock()
				return uint64(lineIndex - 1), fmt.Errorf("event log line %d: invalid injection record", lineIndex)
			}
			switch {
			case inj.HistoryReset:
				entries = append(entries, entry{
					historyReset:       true,
					history:            inj.History,
					historyResetHeader: inj.HistoryResetHeader,
				})
			case inj.UserInput != "":
				entries = append(entries, entry{userInput: inj.UserInput})
			case inj.Assistant != "":
				entries = append(entries, entry{
					assistant: inj.Assistant, assistantBoundaryGroup: inj.AssistantBoundaryGroup,
				})
			case inj.Command != "":
				en := entry{command: inj.Command}
				if inj.Document != nil {
					en.document = inj.Document.Clone()
				}
				entries = append(entries, en)
			case inj.Error != "":
				entries = append(entries, entry{err: inj.Error})
			case inj.Supplement != "":
				entries = append(entries, entry{supplement: inj.Supplement})
			case inj.PriorityKind != "" && inj.PriorityKey != "" && inj.PriorityTranscript != "":
				entries = append(entries, entry{
					priorityKind: inj.PriorityKind, priorityKey: inj.PriorityKey,
					priorityTranscript: inj.PriorityTranscript,
				})
			case inj.Interaction != "":
				en := entry{interaction: inj.Interaction, interactionAnchor: inj.InteractionAnchor}
				if inj.Document != nil {
					en.document = inj.Document.Clone()
				}
				entries = append(entries, en)
			default:
				b.eventLogMu.Lock()
				b.eventLogFailures++
				b.eventLogMu.Unlock()
				return uint64(lineIndex - 1), fmt.Errorf("event log line %d: empty injection record", lineIndex)
			}
			continue
		}
		entries = append(entries, entry{event: ev})
	}
	if err := scanner.Err(); err != nil {
		b.eventLogMu.Lock()
		b.eventLogFailures++
		b.eventLogMu.Unlock()
		return uint64(len(entries)), err
	}
	// 重建 Scene 数据面：与实时路径走同一入口（Encode → ChangeSet →
	// ChangeSetMapper.Apply）。Replay 内部即逐事件 Encode，但丢弃
	// ChangeSet，因此这里显式循环以同步驱动 Scene；语义等价。
	// 注入记录同样经 SubmitUserInput / SubmitCommand / SubmitError →
	// applyChangeSet 恢复，顺序与实时路径一致（同一日志全序）。
	b.renderMu.Lock()
	b.resetCanonicalHistoryProjectionLocked()
	for _, en := range entries {
		switch {
		case en.historyReset:
			b.resetCanonicalHistoryProjectionLocked()
			b.seedPersistedHistoryLocked(buildPersistedHistorySeedUnits(en.history), en.historyResetHeader)
		case en.userInput != "":
			b.applyChangeSet(b.renderEncoder.SubmitUserInput(en.userInput))
		case en.assistant != "":
			b.applyChangeSet(b.renderEncoder.SubmitAssistantWithBoundaryGroup(
				en.assistant, en.assistantBoundaryGroup,
			))
		case en.command != "":
			b.applyChangeSet(b.renderEncoder.SubmitCommandDocument(en.command, en.document))
		case en.err != "":
			b.applyChangeSet(b.renderEncoder.SubmitError(en.err))
		case en.supplement != "":
			b.applyChangeSet(b.renderEncoder.SubmitSupplement(en.supplement))
		case en.priorityKind != "" && en.priorityKey != "" && en.priorityTranscript != "":
			b.applyChangeSet(b.renderEncoder.SubmitPriorityPromptTranscript(
				en.priorityKind, en.priorityKey, en.priorityTranscript,
			))
		case en.interaction != "":
			// 交互输出：按全序重建到该行时模型状态与实时注入时刻等价，
			// 锚点 ItemID 必然存在（实时时 Tail 指向模型尾部项）；锚点
			// nil（空模型触发）退化为 append，与实时路径一致。
			b.applyChangeSet(b.renderEncoder.SubmitUserInteractionDocument(en.interaction, en.document, en.interactionAnchor))
		default:
			b.applyChangeSet(b.renderEncoder.Encode(en.event))
		}
	}
	b.renderMu.Unlock()
	b.eventLogMu.Lock()
	b.eventLogReplayed = uint64(len(entries))
	b.eventLogMu.Unlock()
	// Replay rebuilds the same semantic Scene used by live runtime events. Once
	// the reconstruction is complete, publish one immutable snapshot through
	// the UI actor so AppState does not retain a pre-replay transcript. This is
	// a data-plane bridge only: the legacy replay presenter remains unchanged
	// until the later Compose/TerminalSession migration.
	if b.session != nil && b.session.Interaction != nil {
		b.session.Interaction.postTranscriptSnapshotFromBridge(b)
	}
	return uint64(len(entries)), nil
}

// eventLogStats 返回事件日志状态（/debug 诊断用）。
func (b *chatRuntimeEventBridge) eventLogStats() (path string, count, replayed, failures uint64) {
	if b == nil {
		return "", 0, 0, 0
	}
	b.eventLogMu.Lock()
	defer b.eventLogMu.Unlock()
	return b.eventLogFilePath(), b.eventLogCount, b.eventLogReplayed, b.eventLogFailures
}

// renderModelSnapshot 返回编码器当前渲染模型快照（/debug 诊断用）。
func (b *chatRuntimeEventBridge) renderModelSnapshot() *encoding.RenderModel {
	if b == nil || b.renderEncoder == nil {
		return nil
	}
	return b.renderEncoder.Snapshot()
}

// renderEncoderStats 返回编码器运行统计（/debug 诊断用）。
func (b *chatRuntimeEventBridge) renderEncoderStats() encoding.Stats {
	if b == nil || b.renderEncoder == nil {
		return encoding.Stats{}
	}
	return b.renderEncoder.Stats()
}

// sceneSnapshot 返回 Scene 不可变快照（/debug 诊断与 presenter 消费前置）。
// 与 renderModelSnapshot 互补：模型是数据面权威，Scene 是渲染面权威；
// 双跑模式下二者应逐项一致（身份/顺序/终态）。
func (b *chatRuntimeEventBridge) sceneSnapshot() *scene.Snapshot {
	if b == nil {
		return nil
	}
	b.sceneMu.RLock()
	defer b.sceneMu.RUnlock()
	if b.renderScene == nil {
		return nil
	}
	return b.renderScene.Snapshot()
}

// sceneStats 返回 Scene 映射统计（/debug 诊断用）。
func (b *chatRuntimeEventBridge) sceneStats() (cells, revision, failures uint64, lastErr string) {
	if b == nil {
		return 0, 0, 0, ""
	}
	b.sceneMu.RLock()
	defer b.sceneMu.RUnlock()
	if b.renderScene == nil {
		return 0, 0, b.sceneApplyFailures, b.sceneLastError
	}
	return uint64(b.renderScene.Len()), b.renderScene.Revision(), b.sceneApplyFailures, b.sceneLastError
}

// checkTextParity 是渲染层双跑文本对照探针（切片 9）的 bridge 侧实现：
// coordinator 每提交一个完整块（writeRowsLocked）即回调本方法，传入旧路径
// 实际写出的行序列（含跨块 gap 空行）。本方法按 cell 逐块对照（切片 10）：
// LayoutTranscript 的 gap 行归属后继 cell，每个完整块对应一个已完成 cell：
//
//   - 已对照 cell 数越界（Scene 落后或旧路径超前）→ mismatch；
//   - 行数不等或逐行不相等 → mismatch；
//   - 全部相等 → 推进到下一 cell，matched++。
//
// user cell 的 legacy 行先剥离样式前缀 "> "（RenderText 只投影语义内容，
// 样式属于 presenter 层，见 scene.RenderText 约束）；错误块以 KindSystem
// 呈现，legacy 行剥离错误图标前缀（theme.ErrorIcon + 空格，message.go
// MessageError chrome），与 user 前缀同理；assistant/tool 等块无样式前缀，
// 原样对照。
//
// 只读旁路审计：不改变任何输出行为；统计经 textParityStats 供 /debug 展示。
// 对照假设：同会话事件序列下，旧路径完整块序列与 Scene cell 序列一一对应
// （切片 7/8/10 已固化该等价，含用户输入注入）；流式中间态不经过
// writeRowsLocked，不会误对照。
func (b *chatRuntimeEventBridge) checkTextParity(blockRows []string) {
	if b == nil || len(blockRows) == 0 {
		return
	}
	b.textParityMu.Lock()
	defer b.textParityMu.Unlock()
	b.textParityBlocks++
	snap := b.sceneSnapshot()
	if snap == nil || len(snap.Cells) == 0 {
		b.textParityMissed++
		b.textParityLastErr = fmt.Sprintf("block %d: scene snapshot empty", b.textParityBlocks)
		return
	}
	groups := sceneBlockGroups(snap)
	if b.textParityCell >= len(groups) {
		b.textParityMissed++
		b.textParityLastErr = fmt.Sprintf("block %d: scene cells=%d consumed=%d block=%d (overflow)",
			b.textParityBlocks, len(groups), b.textParityCell, len(blockRows))
		return
	}
	g := groups[b.textParityCell]
	wantLines := g.lines
	if g.kind == scene.KindUser {
		// 旧路径 user 块的前导 gap 由 prompt 重绘（writePromptGapLocked）
		// 输出，不在块行内；数据面把该 gap 归属后继 user cell。对照时
		// 忽略前导 gap 行（gap 行本身不属于 writeRowsLocked 对照范围）。
		for len(wantLines) > 0 && wantLines[0] == "" {
			wantLines = wantLines[1:]
		}
	}
	if len(wantLines) != len(blockRows) {
		b.textParityMissed++
		b.textParityLastErr = fmt.Sprintf("block %d: cell rows=%d block=%d (row count mismatch)",
			b.textParityBlocks, len(wantLines), len(blockRows))
		return
	}
	for i, r := range blockRows {
		if g.kind == scene.KindUser {
			r = strings.TrimPrefix(r, "> ")
		}
		if g.kind == scene.KindSystem {
			r = strings.TrimPrefix(r, ui.GetTheme(ui.ThemeAuto).ErrorIcon+"  ")
		}
		if wantLines[i] != r {
			b.textParityMissed++
			b.textParityLastErr = fmt.Sprintf("block %d row %d: legacy=%q scene=%q",
				b.textParityBlocks, i, r, wantLines[i])
			return
		}
	}
	b.textParityCell++
	b.textParityMatched++
}

// sceneBlockGroup 是 Scene 投影按 cell 分组后的一个完整块行序列
// （LayoutTranscript gap 行归属后继 cell，§7.4）。
type sceneBlockGroup struct {
	kind  scene.CellKind
	lines []string
}

// sceneBlockGroups 按 cell 分组 LayoutTranscript 行：gap 行归属后继 cell。
// 供 checkTextParity（双跑对照探针）与 sceneBlockSource（Scene presenter
// 模式的完整块行源）共用同一分组语义，保证两态下"Scene 侧块行"定义一致。
func sceneBlockGroups(snap *scene.Snapshot) []sceneBlockGroup {
	if snap == nil {
		return nil
	}
	rows := scene.LayoutTranscript(snap.Cells, snap.Revision)
	var groups []sceneBlockGroup
	groupByID := make(map[scene.CellID]int)
	cellKindOf := func(id scene.CellID) scene.CellKind {
		for _, c := range snap.Cells {
			if c != nil && c.ID == id {
				return c.Kind
			}
		}
		return scene.KindSystem
	}
	for _, row := range rows {
		idx, ok := groupByID[row.CellID]
		if !ok {
			idx = len(groups)
			groupByID[row.CellID] = idx
			groups = append(groups, sceneBlockGroup{kind: cellKindOf(row.CellID)})
		}
		groups[idx].lines = append(groups[idx].lines, row.Text)
	}
	return groups
}

// sceneBlockSource 返回 Scene presenter 模式的完整块行源（P3 切换的
// feature flag 出口）：flag（AICLI_SCENE_PRESENTER）关闭时返回 nil
// （coordinator 保持旧路径，行为完全不变）；开启时返回按块顺序消费
// Scene 投影的闭包——writeRowsLocked 的每个完整块可见行 = Scene 对应
// cell 组的 LayoutTranscript 行（含跨块 gap 空行，user 前导 gap 由
// prompt 重绘输出故剥离）+ 样式 chrome（user "> "、system ErrorIcon）。
// 消费前做内容对应校验：入参块行（剥 chrome 与前导 gap 空行）必须与
// 某个未消费分组的行（剥前导 gap 空行）完全一致才消费该分组——完整块
// 语义（整 cell 提交）。流式残差/部分提交（内容仅为分组前缀或后缀）与
// 不相关块一律不消费、不推进，回退旧行；这样部分块不会被整组内容
// 顶替，后续完整块仍能按内容找到自己的分组。闭包返回空表示 Scene 侧
// 尚无对应分组（快照缺失/无匹配/分组耗尽），调用方回退旧行并靠探针
// 报告 mismatch。
func (b *chatRuntimeEventBridge) sceneBlockSource() func(blockRows []string) []string {
	if b == nil || !b.scenePresenterMode {
		return nil
	}
	var mu sync.Mutex
	nextGroup := 0
	normalizeBlockRows := func(rows []string) []string {
		if len(rows) == 0 {
			return nil
		}
		out := make([]string, 0, len(rows))
		for i, r := range rows {
			if i == 0 && r == "" {
				// 跨块 gap 空行：Scene 分组与旧路径块行都可能带，对照时忽略。
				continue
			}
			r = strings.TrimPrefix(r, "> ")
			r = strings.TrimPrefix(r, ui.GetTheme(ui.ThemeAuto).ErrorIcon+"  ")
			out = append(out, r)
		}
		return out
	}
	rowsEqual := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	return func(blockRows []string) []string {
		mu.Lock()
		defer mu.Unlock()
		snap := b.sceneSnapshot()
		groups := sceneBlockGroups(snap)
		want := normalizeBlockRows(blockRows)
		// 内容对应：从 nextGroup 起找第一个行内容与本次块完全一致的分组。
		// 找不到（残差/部分提交/快照滞后）→ 不消费不推进，回退旧行。
		idx := -1
		for i := nextGroup; i < len(groups); i++ {
			if rowsEqual(want, normalizeBlockRows(groups[i].lines)) {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil
		}
		g := groups[idx]
		nextGroup = idx + 1
		out := make([]string, 0, len(g.lines))
		for i, line := range g.lines {
			if i == 0 && g.kind == scene.KindUser && line == "" {
				// 与 checkTextParity 对称：旧路径 user 块的前导 gap 由
				// prompt 重绘（writePromptGapLocked）输出，不在块行内。
				continue
			}
			switch g.kind {
			case scene.KindUser:
				out = append(out, "> "+line)
			case scene.KindSystem:
				if line == "" {
					// gap/空白行不加 chrome：旧路径中跨块 gap 由
					// writeRowsLocked 的 gapBlank 输出为空行，探针
					// （checkTextParity）对照时 gap 行同样不参与 chrome。
					out = append(out, line)
				} else {
					out = append(out, ui.GetTheme(ui.ThemeAuto).ErrorIcon+"  "+line)
				}
			default:
				out = append(out, line)
			}
		}
		return out
	}
}

// textParityStats 返回渲染层文本对照统计（/debug 审计段展示用）。
func (b *chatRuntimeEventBridge) textParityStats() (blocks, matched, missed uint64, lastErr string) {
	if b == nil {
		return 0, 0, 0, ""
	}
	b.textParityMu.Lock()
	defer b.textParityMu.Unlock()
	return b.textParityBlocks, b.textParityMatched, b.textParityMissed, b.textParityLastErr
}

// renderModelTail 返回编码器当前尾部锚点（ItemID/Seq）。
// /debug、/model 等用户交互输出以触发时刻的该锚点为界参与渲染总序
// （不进入编码器因果链，见统一编码器方案 §5.5）。
func (b *chatRuntimeEventBridge) renderModelTail() *encoding.Tail {
	if b == nil || b.renderEncoder == nil {
		return nil
	}
	return b.renderEncoder.Tail()
}

// recordInteractionAnchor 捕获触发时刻的模型尾部锚点（/debug、/model 等用户交互输出用）。
// 交互输出不进入编码器因果链，以该锚点为界参与渲染总序（见统一编码器方案 §5.5）。
// Tail 为值类型副本，模型后续增长不会影响已记录锚点。同时登记 pending
// 交互标记（供 RenderCommandDocument 提交点按锚定语义注入，见
// submitCommandResult / consumePendingInteraction）。
func (b *chatRuntimeEventBridge) recordInteractionAnchor(source string) *encoding.Tail {
	if b == nil {
		return nil
	}
	tail := b.renderModelTail()
	b.interactionAnchorMu.Lock()
	defer b.interactionAnchorMu.Unlock()
	if tail != nil {
		b.interactionAnchor = tail
		b.interactionAnchorAt = time.Now()
		b.interactionAnchorSource = source
		b.interactionAnchorCount++
	}
	b.pendingInteractionSource = source
	b.pendingInteractionTail = tail
	return tail
}

// consumePendingInteraction 读取并清除 pending 交互标记（RenderCommandDocument
// 提交点用：有 pending 说明本次命令是 /debug、/model 等交互输出，应按锚点
// 插入而非普通命令追加）。返回 (source, tail)；无 pending 时 source 为空。
func (b *chatRuntimeEventBridge) consumePendingInteraction() (string, *encoding.Tail) {
	if b == nil {
		return "", nil
	}
	b.interactionAnchorMu.Lock()
	defer b.interactionAnchorMu.Unlock()
	source := b.pendingInteractionSource
	tail := b.pendingInteractionTail
	b.pendingInteractionSource = ""
	b.pendingInteractionTail = nil
	return source, tail
}

// clearPendingInteraction 清除 pending 交互标记（legacy 无 cell 输出路径
// 用；防止锚点残留污染后续普通命令注入）。
func (b *chatRuntimeEventBridge) clearPendingInteraction() {
	if b == nil {
		return
	}
	b.interactionAnchorMu.Lock()
	defer b.interactionAnchorMu.Unlock()
	b.pendingInteractionSource = ""
	b.pendingInteractionTail = nil
}

// lastInteractionAnchor 返回最近一次用户交互锚点（/debug 诊断展示用）。
func (b *chatRuntimeEventBridge) lastInteractionAnchor() (tail *encoding.Tail, at time.Time, source string, count uint64) {
	if b == nil {
		return nil, time.Time{}, "", 0
	}
	b.interactionAnchorMu.Lock()
	defer b.interactionAnchorMu.Unlock()
	return b.interactionAnchor, b.interactionAnchorAt, b.interactionAnchorSource, b.interactionAnchorCount
}

func (b *chatRuntimeEventBridge) logLateRuntimeEvent(event runtimeevents.Event, reason string) {
	if b == nil || b.session == nil {
		return
	}
	if b.session.ExecEventBridge != nil {
		b.session.ExecEventBridge.HandleRuntimeEvent(event)
	}
	payload, _ := json.Marshal(event.Payload)
	writeSessionDebugInfo(
		b.session,
		fmt.Sprintf("[runtime-event] render suppressed reason=%q type=%q session_id=%q trace_id=%q payload=%s",
			reason, event.Type, event.SessionID, event.TraceID, payload),
		false,
	)
}

func (b *chatRuntimeEventBridge) reserveEventQueueBytes(size int64) {
	b.eventQueueMu.Lock()
	defer b.eventQueueMu.Unlock()
	for b.eventQueueByteLimit > 0 && b.eventQueueBytes > 0 && size > b.eventQueueByteLimit-b.eventQueueBytes {
		b.eventQueueCond.Wait()
	}
	b.eventQueueBytes += size
}

func (b *chatRuntimeEventBridge) releaseEventQueueBytes(size int64) {
	b.eventQueueMu.Lock()
	b.eventQueueBytes -= size
	if b.eventQueueBytes < 0 {
		b.eventQueueBytes = 0
	}
	b.eventQueueCond.Broadcast()
	b.eventQueueMu.Unlock()
}

func (b *chatRuntimeEventBridge) WaitForCurrentEvents(timeout time.Duration) bool {
	if b == nil || timeout <= 0 {
		return true
	}
	deadline := time.Now().Add(timeout)
	stableSince := time.Time{}
	lastSeenEnqueued := uint64(0)
	for {
		// Keep draining coalesced streams while the queue frees slots so the
		// drain window reflects the full ingress, not just already-queued work.
		b.flushPendingStreamEventIfAble()
		b.progressMu.Lock()
		enqueued := b.enqueuedEvents
		processed := b.processedEvents
		criticalPending := b.criticalPending
		b.progressMu.Unlock()
		now := time.Now()
		if processed >= enqueued && criticalPending == 0 {
			if stableSince.IsZero() || enqueued != lastSeenEnqueued {
				stableSince = now
				lastSeenEnqueued = enqueued
			}
			if now.Sub(stableSince) >= chatRuntimeEventSettleWindow {
				if coordinator := b.sessionInteraction(); coordinator != nil {
					if !coordinator.waitUIActorIdleTimeout(time.Until(deadline)) {
						return false
					}
				}
				return true
			}
		} else {
			stableSince = time.Time{}
			lastSeenEnqueued = enqueued
		}
		if now.After(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (b *chatRuntimeEventBridge) sessionInteraction() *chatInteractionCoordinator {
	if b == nil || b.session == nil {
		return nil
	}
	return b.session.Interaction
}

// isRunEpochCurrent is the actor-side epoch fence. A queued RuntimeEvent is
// only applied while its captured run epoch matches the current one. Events
// from a previous run are rejected as soon as the next BeginRun advances the
// epoch; events after EndRun remain renderable so ambient background work such
// as async team orchestration can finish its timeline.
func (b *chatRuntimeEventBridge) isRunEpochCurrent(epoch uint64) bool {
	if b == nil {
		return false
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	return epoch != 0 && epoch == b.runEpoch
}

func (b *chatRuntimeEventBridge) handleStructuredLogEvent(event runtimeevents.Event) {
	if b == nil || b.session == nil || b.session.Logger == nil {
		return
	}
	if !b.isRunActive() || !b.isPrimarySessionEvent(event) {
		return
	}
	switch event.Type {
	case runtimechat.EventLLMRequestStarted, "llm.request.started":
		b.logLLMRequestStarted(event)
	case runtimechat.EventLLMRequestFinished, "llm.request.finished":
		b.logLLMRequestFinished(event)
	case runtimechat.EventToolStarted, "tool.requested":
		b.logToolRequested(event)
	case runtimechat.EventToolFinished, "tool.completed":
		b.logToolCompleted(event)
	case runtimechat.EventAssistantMessage:
		b.logAssistantMessage(event)
	case runtimechat.EventSessionEnd:
		b.logSessionEnd(event)
	}
}

func (b *chatRuntimeEventBridge) logLLMRequestStarted(event runtimeevents.Event) {
	state, _, prompt := b.ensureRequestLogState(event, true)
	if state == nil || state.RequestLogged {
		return
	}
	state.StartedAt = runtimeEventTimestamp(event)
	state.RequestLogged = true
	b.session.Logger.LogRequest(state.Scope, buildRuntimeEventLogContent(event, prompt))
	if debugInfo := formatRuntimeLLMRequestDebugInfo(event); debugInfo != "" {
		writeSessionDebugInfo(b.session, debugInfo, false)
	}
}

func (b *chatRuntimeEventBridge) logLLMRequestFinished(event runtimeevents.Event) {
	state, _, _ := b.ensureRequestLogState(event, false)
	if state == nil {
		state, _, _ = b.ensureRequestLogState(event, true)
	}
	if state == nil {
		return
	}
	state.FinishedAt = runtimeEventTimestamp(event)
	if state.ResponseLogged {
		return
	}
	content := buildRuntimeEventLogContent(event, "")
	success := payloadBoolValue(event.Payload, "success")
	toolCallCount := intPayloadValue(event.Payload, "tool_call_count")
	durationMs := runtimeEventDurationMs(state.StartedAt, state.FinishedAt)
	if !success {
		err := runtimeEventError(event.Payload)
		b.session.Logger.LogResponse(state.Scope, content, nil, b.session.Stream, err, durationMs)
		state.ResponseLogged = true
		state.AwaitingAssistantResult = false
		state.PendingResponseContent = nil
		if debugInfo := formatRuntimeLLMRequestDebugInfo(event); debugInfo != "" {
			writeSessionDebugInfo(b.session, debugInfo, false)
		}
		return
	}
	if toolCallCount > 0 {
		b.session.Logger.LogResponse(state.Scope, content, nil, b.session.Stream, nil, durationMs)
		state.ResponseLogged = true
		state.AwaitingAssistantResult = false
		state.PendingResponseContent = nil
		if debugInfo := formatRuntimeLLMRequestDebugInfo(event); debugInfo != "" {
			writeSessionDebugInfo(b.session, debugInfo, false)
		}
		return
	}
	state.PendingResponseContent = content
	state.AwaitingAssistantResult = true
	if debugInfo := formatRuntimeLLMRequestDebugInfo(event); debugInfo != "" {
		writeSessionDebugInfo(b.session, debugInfo, false)
	}
}

func (b *chatRuntimeEventBridge) logToolRequested(event runtimeevents.Event) {
	scope, ok := b.scopeForEvent(event)
	if !ok {
		return
	}
	toolCallID := strings.TrimSpace(payloadStringValue(event.Payload["tool_call_id"]))
	if toolCallID == "" {
		return
	}
	b.logMu.Lock()
	if _, exists := b.loggedToolCalls[toolCallID]; exists {
		b.logMu.Unlock()
		return
	}
	b.loggedToolCalls[toolCallID] = struct{}{}
	b.logMu.Unlock()
	b.session.Logger.LogToolCall(scope, toolCallID, runtimeEventToolName(event), cloneRuntimeEventLogPayload(event.Payload))
}

func (b *chatRuntimeEventBridge) logToolCompleted(event runtimeevents.Event) {
	scope, ok := b.scopeForEvent(event)
	if !ok {
		return
	}
	toolCallID := strings.TrimSpace(payloadStringValue(event.Payload["tool_call_id"]))
	if toolCallID == "" {
		return
	}
	resultPayload := cloneRuntimeEventLogPayload(event.Payload)
	err := runtimeEventError(event.Payload)
	callSummary := runtimeToolExecutionSummaryCall(event)
	if callSummary.RawOutputArtifactPath != "" {
		recordLocalShellArtifactPath(b.session, callSummary.RawOutputArtifactPath)
	}
	b.logMu.Lock()
	if _, exists := b.loggedToolResults[toolCallID]; exists {
		b.logMu.Unlock()
		return
	}
	b.loggedToolResults[toolCallID] = struct{}{}
	b.toolExecutionCalls = append(b.toolExecutionCalls, callSummary)
	b.logMu.Unlock()
	b.session.Logger.LogToolResult(scope, toolCallID, runtimeEventToolName(event), resultPayload, err)
}

func (b *chatRuntimeEventBridge) logAssistantMessage(event runtimeevents.Event) {
	state, _ := b.latestRequestLogState(event)
	if state == nil || state.ResponseLogged {
		return
	}
	content := cloneRuntimeEventLogPayload(event.Payload)
	if len(state.PendingResponseContent) > 0 {
		content = mergeRuntimeEventLogContent(state.PendingResponseContent, content)
	}
	b.session.Logger.LogResponse(state.Scope, content, nil, b.session.Stream, nil, runtimeEventDurationMs(state.StartedAt, state.FinishedAt))
	state.ResponseLogged = true
	state.AwaitingAssistantResult = false
	state.PendingResponseContent = nil
}

func (b *chatRuntimeEventBridge) logSessionEnd(event runtimeevents.Event) {
	pendingStates := b.pendingAssistantRequestStates(event)
	if len(pendingStates) > 0 {
		runErr := error(nil)
		if !payloadBoolValue(event.Payload, "success") {
			runErr = runtimeEventError(event.Payload)
		}
		for _, state := range pendingStates {
			if state == nil || state.ResponseLogged {
				continue
			}
			content := cloneRuntimeEventLogPayload(event.Payload)
			if len(state.PendingResponseContent) > 0 {
				content = mergeRuntimeEventLogContent(state.PendingResponseContent, content)
			}
			b.session.Logger.LogResponse(state.Scope, content, nil, b.session.Stream, runErr, runtimeEventDurationMs(state.StartedAt, state.FinishedAt))
			state.ResponseLogged = true
			state.AwaitingAssistantResult = false
			state.PendingResponseContent = nil
		}
	}
	scope := b.toolExecutionSummaryScope(event)
	if scope.TurnID == "" && scope.RequestID == "" {
		return
	}
	b.logMu.Lock()
	if b.toolSummaryLogged || len(b.toolExecutionCalls) == 0 {
		b.logMu.Unlock()
		return
	}
	calls := append([]aicliToolExecutionCallSummary(nil), b.toolExecutionCalls...)
	b.toolSummaryLogged = true
	b.logMu.Unlock()
	successCount := 0
	errorCount := 0
	for _, call := range calls {
		if call.Success {
			successCount++
		} else {
			errorCount++
		}
	}
	b.session.Logger.LogToolExecutionSummary(scope, buildToolExecutionSummary(calls, successCount, errorCount))
}

func (b *chatRuntimeEventBridge) ensureRequestLogState(event runtimeevents.Event, allowCreate bool) (*chatRuntimeRequestLogState, string, string) {
	if b == nil {
		return nil, "", ""
	}
	key := runtimeEventRequestKey(event)
	traceID := runtimeEventTraceID(event)
	b.logMu.Lock()
	defer b.logMu.Unlock()
	if key == "" && traceID != "" {
		key = b.traceLatestRequestKey[traceID]
	}
	if key == "" {
		key = b.latestRequestKey
	}
	state := b.requestLogState[key]
	if state != nil || !allowCreate {
		return state, key, ""
	}
	if b.requestLogState == nil {
		b.requestLogState = make(map[string]*chatRuntimeRequestLogState)
	}
	prompt := b.activeRunPrompt
	state = &chatRuntimeRequestLogState{
		Scope: newRuntimeEventLogScope(b.session, prompt),
	}
	b.requestLogState[key] = state
	b.latestRequestKey = key
	if traceID != "" {
		if b.traceLatestRequestKey == nil {
			b.traceLatestRequestKey = make(map[string]string)
		}
		b.traceLatestRequestKey[traceID] = key
	}
	b.activeRunPrompt = ""
	return state, key, prompt
}

func (b *chatRuntimeEventBridge) latestRequestLogState(event runtimeevents.Event) (*chatRuntimeRequestLogState, string) {
	state, key, _ := b.ensureRequestLogState(event, false)
	if state != nil {
		return state, key
	}
	return b.latestRequestStateForTrace(runtimeEventTraceID(event))
}

func (b *chatRuntimeEventBridge) latestRequestStateForTrace(traceID string) (*chatRuntimeRequestLogState, string) {
	if b == nil {
		return nil, ""
	}
	b.logMu.Lock()
	defer b.logMu.Unlock()
	key := ""
	if traceID != "" && b.traceLatestRequestKey != nil {
		key = b.traceLatestRequestKey[traceID]
	}
	if key == "" {
		key = b.latestRequestKey
	}
	if key == "" {
		return nil, ""
	}
	return b.requestLogState[key], key
}

func (b *chatRuntimeEventBridge) scopeForEvent(event runtimeevents.Event) (aicliLogScope, bool) {
	state, _ := b.latestRequestLogState(event)
	if state == nil {
		return aicliLogScope{}, false
	}
	return state.Scope, true
}

func (b *chatRuntimeEventBridge) pendingAssistantRequestStates(event runtimeevents.Event) []*chatRuntimeRequestLogState {
	if b == nil {
		return nil
	}
	traceID := runtimeEventTraceID(event)
	b.logMu.Lock()
	defer b.logMu.Unlock()
	states := make([]*chatRuntimeRequestLogState, 0, 1)
	if traceID != "" && b.traceLatestRequestKey != nil {
		if key := b.traceLatestRequestKey[traceID]; key != "" {
			if state := b.requestLogState[key]; state != nil && state.AwaitingAssistantResult && !state.ResponseLogged {
				states = append(states, state)
				return states
			}
		}
	}
	for _, state := range b.requestLogState {
		if state != nil && state.AwaitingAssistantResult && !state.ResponseLogged {
			states = append(states, state)
		}
	}
	return states
}

func (b *chatRuntimeEventBridge) toolExecutionSummaryScope(event runtimeevents.Event) aicliLogScope {
	if scope, ok := b.scopeForEvent(event); ok {
		return scope
	}
	return aicliLogScope{}
}

func (b *chatRuntimeEventBridge) isRunActive() bool {
	if b == nil {
		return false
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	return b.runActive
}

func newRuntimeEventLogScope(session *ChatSession, prompt string) aicliLogScope {
	if session == nil {
		return aicliLogScope{}
	}
	return nextLogScope(session, prompt)
}

func buildRuntimeEventLogContent(event runtimeevents.Event, prompt string) map[string]interface{} {
	content := cloneRuntimeEventLogPayload(event.Payload)
	content["source"] = "actor_runtime_event"
	content["event_type"] = strings.TrimSpace(event.Type)
	if traceID := runtimeEventTraceID(event); traceID != "" {
		content["trace_id"] = traceID
	}
	if toolName := runtimeEventToolName(event); toolName != "" {
		content["tool_name"] = toolName
	}
	if strings.TrimSpace(prompt) != "" {
		content["user_message"] = prompt
	}
	return content
}

func cloneRuntimeEventLogPayload(payload map[string]interface{}) map[string]interface{} {
	if len(payload) == 0 {
		return map[string]interface{}{}
	}
	cloned := make(map[string]interface{}, len(payload))
	for key, value := range payload {
		cloned[key] = value
	}
	return cloned
}

func mergeRuntimeEventLogContent(base map[string]interface{}, extra map[string]interface{}) map[string]interface{} {
	if len(base) == 0 && len(extra) == 0 {
		return map[string]interface{}{}
	}
	merged := cloneRuntimeEventLogPayload(base)
	for key, value := range extra {
		merged[key] = value
	}
	return merged
}

func runtimeEventTraceID(event runtimeevents.Event) string {
	return firstNonEmptyChatValue(strings.TrimSpace(event.TraceID), payloadStringValue(event.Payload["trace_id"]))
}

func runtimeEventRequestKey(event runtimeevents.Event) string {
	traceID := runtimeEventTraceID(event)
	stepLabel := payloadStringValue(event.Payload["step"])
	if traceID == "" && stepLabel == "" {
		return ""
	}
	if stepLabel == "" {
		stepLabel = "step"
	}
	return traceID + ":" + stepLabel
}

func runtimeEventTimestamp(event runtimeevents.Event) time.Time {
	if !event.Timestamp.IsZero() {
		return event.Timestamp
	}
	return time.Now().UTC()
}

func runtimeEventExplicitTimestamp(event runtimeevents.Event) time.Time {
	return event.Timestamp
}

func runtimeEventDurationMs(start time.Time, end time.Time) int64 {
	if start.IsZero() {
		return 0
	}
	if end.IsZero() {
		end = time.Now().UTC()
	}
	if end.Before(start) {
		return 0
	}
	return end.Sub(start).Milliseconds()
}

func runtimeEventError(payload map[string]interface{}) error {
	errText := strings.TrimSpace(payloadStringValue(payload["error"]))
	if errText == "" {
		return nil
	}
	return fmt.Errorf("%s", errText)
}

func runtimeEventToolName(event runtimeevents.Event) string {
	return firstNonEmptyChatValue(strings.TrimSpace(event.ToolName), payloadStringValue(event.Payload["tool_name"]))
}

// runtimeEventHasToolCallIdentity gates mutable tool projections. A tool name
// alone is not a safe owner because concurrent calls may share it; incomplete
// legacy events remain visible through the system/timeline fallback instead.
func runtimeEventHasToolCallIdentity(event runtimeevents.Event) bool {
	return strings.TrimSpace(payloadStringValue(event.Payload["tool_call_id"])) != ""
}

func runtimeToolTimelinePayload(event runtimeevents.Event) map[string]interface{} {
	toolName := runtimeEventToolName(event)
	payload := cloneRuntimeEventLogPayload(event.Payload)
	if strings.TrimSpace(toolName) != "" && strings.TrimSpace(payloadStringValue(payload["tool_name"])) == "" {
		payload["tool_name"] = toolName
	}
	return payload
}

func runtimeToolExecutionSummaryCall(event runtimeevents.Event) aicliToolExecutionCallSummary {
	toolCallID := strings.TrimSpace(payloadStringValue(event.Payload["tool_call_id"]))
	function := runtimeEventToolName(event)
	success := runtimeEventError(event.Payload) == nil
	summaryText := strings.Join(chatToolSummaryLines(event.Payload), "\n")
	summary := aicliToolExecutionCallSummary{
		ToolCallID:    toolCallID,
		Function:      function,
		Success:       success,
		Error:         payloadStringValue(event.Payload["error"]),
		ToolSource:    strings.TrimSpace(payloadStringValue(event.Payload[toolresult.SourceKey])),
		OutputKind:    strings.TrimSpace(payloadStringValue(event.Payload[toolresult.MetadataKey])),
		ResultPreview: summaryText,
		ResultBytes:   len(summaryText),
	}
	applyToolExecutionOutputCaptureMetadata(&summary, event.Payload)
	applyToolExecutionShellMetadata(&summary, event.Payload)
	return summary
}

// isRuntimeToolTerminalEventType 判断事件是否属于工具调用终态（完成/失败/
// 取消），这些事件会携带调用时长并触发调用后渲染。
func isRuntimeToolTerminalEventType(eventType string) bool {
	switch eventType {
	case runtimechat.EventToolFinished, "tool.completed", "tool.failed", "tool.cancelled", "tool.canceled":
		return true
	}
	return false
}

// enrichRuntimeToolDuration 在 Scene 数据面编码之前为工具终态事件注入
// duration_ms（从 toolCallStartedAt 记录的调用起始时刻与事件时间戳之差）。
// enrichTimelineEvent 也在计算该值，但发生在 encodeRenderModelEvent 之后，
// 只服务 timeline 渲染；本方法复用同一 startedAt 账本，使 encoder 的
// toolCallCompletedTitle 也能恢复 "in 5ms" 时长后缀。消费后删除 startedAt，
// 与 enrichTimelineEvent 的消费语义保持一致（避免 map 残留）。
func (b *chatRuntimeEventBridge) enrichRuntimeToolDuration(event runtimeevents.Event) runtimeevents.Event {
	if b == nil {
		return event
	}
	switch event.Type {
	case runtimechat.EventToolFinished, "tool.completed", "tool.failed", "tool.cancelled", "tool.canceled":
	default:
		return event
	}
	payload := cloneRuntimeEventLogPayload(runtimeToolTimelinePayload(event))
	if intPayloadValue(payload, "duration_ms") > 0 {
		return event
	}
	toolCallKey := runtimeToolCallTimelineKey(event, payload)
	timestamp := runtimeEventExplicitTimestamp(event)
	if toolCallKey == "" || timestamp.IsZero() {
		return event
	}
	var startedAt time.Time
	b.renderMu.Lock()
	if b.toolCallStartedAt != nil {
		startedAt = b.toolCallStartedAt[toolCallKey]
		delete(b.toolCallStartedAt, toolCallKey)
	}
	b.renderMu.Unlock()
	if startedAt.IsZero() {
		return event
	}
	if durationMs := runtimeEventDurationMs(startedAt, timestamp); durationMs > 0 {
		enriched := cloneRuntimeEventLogPayload(event.Payload)
		enriched["duration_ms"] = durationMs
		event.Payload = enriched
	}
	return event
}

func runtimeToolCallTimelineKey(event runtimeevents.Event, payload map[string]interface{}) string {
	toolCallID := strings.TrimSpace(payloadStringValue(payload["tool_call_id"]))
	if toolCallID == "" {
		return ""
	}
	sessionID := firstNonEmptyChatValue(strings.TrimSpace(event.SessionID), payloadStringValue(payload["session_id"]))
	traceID := firstNonEmptyChatValue(strings.TrimSpace(event.TraceID), payloadStringValue(payload["trace_id"]))
	return strings.Join([]string{sessionID, traceID, toolCallID}, "\x00")
}

func (b *chatRuntimeEventBridge) enrichTimelineEvent(event runtimeevents.Event) runtimeevents.Event {
	if b == nil {
		return event
	}
	switch event.Type {
	case runtimechat.EventToolStarted, "tool.requested",
		runtimechat.EventToolFinished, "tool.completed", "tool.failed", "tool.cancelled", "tool.canceled":
	default:
		return event
	}
	payload := cloneRuntimeEventLogPayload(runtimeToolTimelinePayload(event))
	if payload == nil {
		payload = make(map[string]interface{})
	}
	toolCallKey := runtimeToolCallTimelineKey(event, payload)
	timestamp := runtimeEventExplicitTimestamp(event)
	switch event.Type {
	case runtimechat.EventToolStarted, "tool.requested":
		if toolCallKey != "" && !timestamp.IsZero() {
			b.renderMu.Lock()
			if b.toolCallStartedAt == nil {
				b.toolCallStartedAt = make(map[string]time.Time)
			}
			b.toolCallStartedAt[toolCallKey] = timestamp
			b.renderMu.Unlock()
		}
	case runtimechat.EventToolFinished, "tool.completed", "tool.failed", "tool.cancelled", "tool.canceled":
		if toolCallKey != "" && intPayloadValue(payload, "duration_ms") <= 0 && !timestamp.IsZero() {
			var startedAt time.Time
			b.renderMu.Lock()
			if b.toolCallStartedAt != nil {
				startedAt = b.toolCallStartedAt[toolCallKey]
				delete(b.toolCallStartedAt, toolCallKey)
			}
			b.renderMu.Unlock()
			if !startedAt.IsZero() {
				if durationMs := runtimeEventDurationMs(startedAt, timestamp); durationMs > 0 {
					payload["duration_ms"] = durationMs
				}
			}
		}
	}
	event.Payload = payload
	return event
}

func (b *chatRuntimeEventBridge) handleEvent(event runtimeevents.Event) {
	if b == nil || b.session == nil {
		return
	}
	b.observePrimaryRunTurn(event)
	// Logging is append-only observability and may retain stale events. All
	// mutable UI/transcript paths below remain guarded by turn ownership.
	b.handleStructuredLogEvent(event)
	if b.shouldSuppressMismatchedPrimaryTurnEvent(event) {
		payload, _ := json.Marshal(event.Payload)
		writeSessionDebugInfo(
			b.session,
			fmt.Sprintf("[runtime-event] render suppressed reason=%q type=%q session_id=%q trace_id=%q payload=%s",
				"event turn does not match active run", event.Type, event.SessionID, event.TraceID, payload),
			false,
		)
		return
	}
	if b.session.ExecEventBridge != nil {
		b.session.ExecEventBridge.HandleRuntimeEvent(event)
	}
	b.applyLLMRequestStatus(event)
	b.applySessionCompactStatus(event)
	if b.shouldSuppressLatePrimaryRunEvent(event) {
		return
	}
	// 编码必须在 ownership/suppression 校验之后：数据面只接收旧 UI 真正
	// 会渲染的事件，保证 Scene 与可见输出一致、事件日志重放等价。
	// 工具调用时长（duration_ms）由 enrichTimelineEvent 在编码之后才计算
	// （服务 timeline 渲染），导致 Scene 数据面拿不到调用后标题的
	// "in 5ms" 时长细节；这里在编码前为 tool 终态事件先行注入，对齐旧
	// compactToolCompletionTitle 的完整调用后渲染。
	if isRuntimeToolTerminalEventType(event.Type) {
		event = b.enrichRuntimeToolDuration(event)
	}
	b.encodeRenderModelEvent(event)
	b.updateComposerAgentStageForRuntimeEvent(event)
	if isTeamLifecycleRuntimeEvent(event.Type) && strings.TrimSpace(event.SessionID) != "" && !b.shouldAcceptTeamLifecycleRuntimeEvent(event) {
		return
	}
	b.updateChatTitleForRuntimeEvent(event)
	if b.handleAssistantReasoning(event) {
		return
	}
	if event.Type == runtimechat.EventAssistantReasoning || event.Type == "assistant.reasoning" {
		return
	}
	if b.handleAssistantDelta(event) {
		return
	}
	if b.handlePrimaryAssistantMessage(event) {
		b.writePromptIfIdle()
		return
	}
	flushedReasoning := b.shouldFlushReasoningOnSessionEnd(event)
	flushedAssistant := b.shouldFinalizeAssistantDeltaOnTerminalEvent(event)
	if (flushedReasoning || flushedAssistant) && !isPromptPreflightSessionEndEvent(event) {
		return
	}
	if b.shouldSuppressTimelineDuringAssistantStream(event) {
		return
	}
	suppressApprovalTimeline := false
	if event.Type == runtimechat.EventApprovalRequested {
		suppressApprovalTimeline = b.shouldSuppressApprovalTimeline(event)
	}
	renderedSomething := false
	rendered := chatRuntimeTimelineEvent{}
	timelineEvent := b.enrichTimelineEvent(event)
	if !suppressApprovalTimeline {
		rendered = renderChatRuntimeTimelineEvent(timelineEvent)
		if rendered.Line == "" {
			rendered = b.renderAsyncTeamSummaryFallback(event)
		}
	}
	// llm.retry：重试信息渲染在动态数据状态区域（composer 上方状态行），
	// 不进入 timeline/transcript（重试是过程状态，非持久历史）。格式复用
	// 旧 renderLLMRetryTimelineEvent 的重试详情（step/attempt/reason 等），
	// 由结构化 SetRetrying 呈现（detail 仅为展示数据，不参与语义判断）。
	if event.Type == "llm.retry" {
		// Retry is live process state, only meaningful while the run is active.
		// A late retry event (for example published without a turn_id by an
		// async LLM retry callback after the run settled) must not re-arm the
		// status line: it would wipe the frozen "Worked for ..." completion
		// summary and flip the state icon back to running.
		if parts := chatLLMRetryParts(event); len(parts) > 0 &&
			shouldRenderInteractiveOutput(b.session) && b.session.Interaction != nil {
			b.session.Interaction.SetRetrying(strings.Join(parts, " "))
		}
		rendered = chatRuntimeTimelineEvent{}
	}
	if rendered.DebugOnly && !isSessionDebugModeEnabled(b.session) {
		rendered = chatRuntimeTimelineEvent{}
	}
	if isRuntimeToolRequestedEventType(event.Type) &&
		b.isPrimarySessionEvent(event) &&
		runtimeEventHasToolCallIdentity(event) &&
		rendered.Line != "" &&
		shouldRenderInteractiveOutput(b.session) &&
		b.session.Interaction != nil {
		b.session.Interaction.SetToolAgentStageDisplay(
			payloadStringValue(event.Payload["tool_call_id"]),
			runtimeEventToolName(event),
			rendered.Line,
		)
		// Running belongs exclusively to ActiveBand. The matching final event
		// remains the single durable history cell.
		rendered = chatRuntimeTimelineEvent{}
		renderedSomething = true
	}
	if event.Type == "tool.progress" &&
		b.isPrimarySessionEvent(event) &&
		runtimeEventHasToolCallIdentity(event) &&
		shouldRenderInteractiveOutput(b.session) &&
		b.session.Interaction != nil {
		// A progress event with stable call identity is already represented by
		// the mutable Scene tool cell and the ActiveBand stage. Emitting its
		// compact timeline line would create a second visible rendering path and
		// make progress look like durable history. Identity-less progress keeps
		// the system fallback above so incomplete events remain observable.
		rendered = chatRuntimeTimelineEvent{}
		renderedSomething = true
	}
	if rendered.Line != "" && shouldRenderInteractiveOutput(b.session) && b.shouldRenderTimelineEvent(rendered) {
		b.emitTimelineEvent(rendered)
		renderedSomething = true
	}
	if response := b.asyncTeamAssistantResponse(event); response != "" && shouldRenderInteractiveOutput(b.session) {
		b.renderResponse(response)
		renderedSomething = true
	}
	if renderedSomething {
		b.writePromptIfIdle()
	}

	if event.Type != runtimechat.EventApprovalRequested && event.Type != runtimechat.EventQuestionAsked {
		return
	}
	switch event.Type {
	case runtimechat.EventApprovalRequested:
		requestID, _ := event.Payload["request_id"].(string)
		approval := b.approvalRequestForEvent(event)
		approvalContextLines := approvalRequestContextLines(event.Payload)
		if grantKey := b.autoApprovalGrantKey(event.SessionID, approval); grantKey != "" && b.hasApprovalGrant(grantKey) {
			if err := b.resolveApproval(context.Background(), event.SessionID, requestID, true); err != nil {
				b.setRunError(err)
			}
			return
		}
		if b.session.NoInteractive && !b.preferInteractiveApprovals {
			b.setRunError(b.nonInteractiveApprovalError(approval))
			_ = b.resolveApproval(context.Background(), event.SessionID, requestID, false)
			return
		}
		reason, _ := event.Payload["reason"].(string)
		b.maybeRenderPermissionModeHint(reason)
		if hint := b.approvalPromptHint(event.SessionID, approval); hint != "" {
			b.renderLocalApprovalSupplement(hint)
		}
		priorityTarget := b.setPriorityTranscriptTarget(event)
		answer, askErr := func() (chatApprovalAnswer, error) {
			endAction := beginChatTitleAction(b.session, "Approval Required")
			defer endAction()
			return b.askApproval(approval, approvalContextLines)
		}()
		b.clearPriorityTranscriptTarget(priorityTarget)
		if askErr != nil {
			b.setRunError(askErr)
			_ = b.resolveApproval(context.Background(), event.SessionID, requestID, false)
			return
		}
		b.renderApprovalDecision(approval, answer.Allowed)
		if answer.Allowed && answer.Reuse {
			b.rememberApprovalGrant(b.autoApprovalGrantKey(event.SessionID, approval))
		}
		if err := b.resolveApproval(context.Background(), event.SessionID, requestID, answer.Allowed); err != nil {
			b.setRunError(err)
		}
	case runtimechat.EventQuestionAsked:
		questionID, _ := event.Payload["question_id"].(string)
		prompt, _ := event.Payload["prompt"].(string)
		required, _ := event.Payload["required"].(bool)
		suggestions := interfaceSliceToStrings(event.Payload["suggestions"])
		if b.session.NoInteractive {
			b.setRunError(b.nonInteractiveQuestionError(prompt))
			_ = b.resolveQuestion(context.Background(), event.SessionID, questionID, "")
			return
		}
		priorityTarget := b.setPriorityTranscriptTarget(event)
		answer, askErr := func() (string, error) {
			endAction := beginChatTitleAction(b.session, "Input Required")
			defer endAction()
			return b.askQuestion(prompt, suggestions, required)
		}()
		b.clearPriorityTranscriptTarget(priorityTarget)
		if askErr != nil {
			b.setRunError(askErr)
			_ = b.resolveQuestion(context.Background(), event.SessionID, questionID, "")
			return
		}
		if err := b.resolveQuestion(context.Background(), event.SessionID, questionID, answer); err != nil {
			b.setRunError(err)
		}
	}
}

func (b *chatRuntimeEventBridge) observePrimaryRunTurn(event runtimeevents.Event) {
	if b == nil || event.Type != runtimechat.EventSessionStart || !b.isPrimarySessionEvent(event) {
		return
	}
	turnID := strings.TrimSpace(payloadStringValue(event.Payload["turn_id"]))
	if turnID == "" {
		return
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	if !b.runActive {
		return
	}
	if b.retiredTurnIDs == nil {
		b.retiredTurnIDs = make(map[string]struct{})
	}
	if _, retired := b.retiredTurnIDs[turnID]; retired {
		return
	}
	if b.activeTurnID == "" {
		b.activeTurnID = turnID
	}
}

func (b *chatRuntimeEventBridge) shouldSuppressMismatchedPrimaryTurnEvent(event runtimeevents.Event) bool {
	if b == nil || !b.isPrimarySessionEvent(event) {
		return false
	}
	// Team/task/mailbox events have their own durable team/task/message
	// identities and may legitimately arrive after the initiating chat turn.
	if isTeamLifecycleRuntimeEvent(event.Type) || event.Type == runtimechat.EventMailboxReceived || isCriticalSubagentLifecycleEvent(event.Type) {
		return false
	}
	turnID := strings.TrimSpace(payloadStringValue(event.Payload["turn_id"]))
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	// Once the actor has identified the current run, an identityless primary
	// event cannot prove ownership. Keep it in runtime logs, but never let it
	// mutate that run's viewport or transcript. Outside an identified run,
	// retain compatibility with legacy status events and persisted fixtures.
	if turnID == "" {
		return b.runActive && b.activeTurnID != ""
	}
	if !b.runActive {
		return true
	}
	if _, retired := b.retiredTurnIDs[turnID]; retired {
		return true
	}
	return b.activeTurnID == "" || b.activeTurnID != turnID
}

func (b *chatRuntimeEventBridge) updateComposerAgentStageForRuntimeEvent(event runtimeevents.Event) {
	if b == nil || b.session == nil || b.session.Interaction == nil || !b.isRunActive() || !b.isPrimarySessionEvent(event) {
		return
	}
	switch event.Type {
	case runtimechat.EventLLMRequestStarted, "llm.request.started":
		b.session.Interaction.SetAgentStage(chatAgentStagePlanning)
	case runtimechat.EventToolStarted, "tool.requested":
		if !runtimeEventHasToolCallIdentity(event) {
			return
		}
		b.session.Interaction.SetToolAgentStage(
			payloadStringValue(event.Payload["tool_call_id"]),
			runtimeEventToolName(event),
		)
	case "tool.progress":
		if !runtimeEventHasToolCallIdentity(event) {
			return
		}
		// Light stage detail only — progress is high-frequency and must not
		// hard-block the composer the way late tool start/finish does.
		if detail := chatToolProgressStageDetail(event); detail != "" {
			b.session.Interaction.SetToolAgentStage(payloadStringValue(event.Payload["tool_call_id"]), detail)
		}
	case runtimechat.EventToolFinished, "tool.completed", "tool.failed", "tool.cancelled", "tool.canceled":
		if !runtimeEventHasToolCallIdentity(event) {
			return
		}
		b.session.Interaction.FinishToolAgentStage(
			payloadStringValue(event.Payload["tool_call_id"]),
			runtimeEventToolName(event),
		)
	case runtimechat.EventSessionEnd, runtimechat.EventSessionInterrupted:
		b.session.Interaction.ClearRuntimeToolAgentStages()
	}
}

func (b *chatRuntimeEventBridge) updateChatTitleForRuntimeEvent(event runtimeevents.Event) {
	if b == nil || b.session == nil || b.session.TitleNotifier == nil {
		return
	}
	switch event.Type {
	case runtimechat.EventToolStarted, "tool.requested":
		if !runtimeEventHasToolCallIdentity(event) {
			return
		}
		key := runtimeToolCallTimelineKey(event, runtimeToolTimelinePayload(event))
		b.session.TitleNotifier.SetToolRunning(key, true)
	case runtimechat.EventToolFinished, "tool.completed", "tool.failed", "tool.cancelled", "tool.canceled":
		if !runtimeEventHasToolCallIdentity(event) {
			return
		}
		key := runtimeToolCallTimelineKey(event, runtimeToolTimelinePayload(event))
		b.session.TitleNotifier.SetToolRunning(key, false)
	case runtimechat.EventSessionEnd:
		sessionID := firstNonEmptyChatValue(strings.TrimSpace(event.SessionID), payloadStringValue(event.Payload["session_id"]))
		if sessionID == "" || b.isPrimarySessionEvent(event) {
			b.session.TitleNotifier.ClearTools()
		} else {
			b.session.TitleNotifier.ClearToolsForSession(sessionID)
		}
	}
}

func (b *chatRuntimeEventBridge) shouldSuppressLatePrimaryRunEvent(event runtimeevents.Event) bool {
	if b == nil || b.session == nil || !b.runStarted || b.isRunActive() || !b.isPrimarySessionEvent(event) {
		return false
	}
	switch event.Type {
	case runtimechat.EventLLMRequestStarted, "llm.request.started":
		return true
	case runtimechat.EventLLMRequestFinished, "llm.request.finished":
		return true
	case runtimechat.EventAssistantReasoning, "assistant.reasoning":
		return true
	case runtimechat.EventAssistantDelta:
		return true
	case runtimechat.EventAssistantMessage:
		return true
	case runtimechat.EventApprovalRequested:
		return true
	case runtimechat.EventQuestionAsked:
		return true
	case runtimechat.EventToolStarted, "tool.requested":
		return true
	case runtimechat.EventToolFinished, "tool.completed":
		return true
	case "tool.progress":
		// Live-only mid-tool updates: drop after the primary run settles so the
		// timeline is not spammed by late progress from finishing tools.
		return true
	default:
		return false
	}
}

func (b *chatRuntimeEventBridge) applyLLMRequestStatus(event runtimeevents.Event) {
	if b == nil || b.session == nil || !b.isRunActive() || !b.isPrimarySessionEvent(event) {
		return
	}
	switch event.Type {
	case runtimechat.EventLLMRequestStarted, "llm.request.started":
		if messageCount := firstPositivePayloadInt(event.Payload, "message_count"); messageCount > 0 {
			applyChatStatusMessageCount(b.session, messageCount, true)
		}
		promptTokens := firstPositivePayloadInt(event.Payload, "context_prompt_tokens", "total_tokens")
		windowTokens := firstPositivePayloadInt(event.Payload, "context_window_tokens", "max_context_tokens", "model_capability_max_context_tokens", "provider_context_limit")
		applyChatTurnContextTokens(b.session, promptTokens, windowTokens, true)
	case runtimechat.EventLLMRequestFinished, "llm.request.finished":
		windowTokens := firstPositivePayloadInt(event.Payload, "context_window_tokens", "max_context_tokens", "model_capability_max_context_tokens", "provider_context_limit")
		applyChatTurnContextTokens(b.session, 0, windowTokens, true)
		cacheReadTokens := firstPositivePayloadInt(event.Payload, "usage_cache_read_tokens", "usage_cached_tokens", "cached_tokens", "cache_read_input_tokens")
		cacheCreationTokens := firstPositivePayloadInt(event.Payload, "usage_cache_creation_tokens", "cache_creation_input_tokens")
		usage := &runtimetypes.TokenUsage{
			PromptTokens:        firstPositivePayloadInt(event.Payload, "usage_prompt_tokens", "prompt_tokens", "input_tokens"),
			CompletionTokens:    firstPositivePayloadInt(event.Payload, "usage_completion_tokens", "completion_tokens", "output_tokens"),
			TotalTokens:         firstPositivePayloadInt(event.Payload, "usage_total_tokens"),
			CachedTokens:        cacheReadTokens,
			CacheReadTokens:     cacheReadTokens,
			CacheCreationTokens: cacheCreationTokens,
			CacheReadReported:   payloadBoolValue(event.Payload, "usage_cache_read_reported") || cacheReadTokens > 0,
			ReasoningTokens:     firstPositivePayloadInt(event.Payload, "usage_reasoning_tokens", "reasoning_tokens", "thinking_tokens"),
		}
		if applied := applyChatContextTokensFromUsage(b.session, usage, windowTokens, true); applied <= 0 {
			if estimateTokens := firstPositivePayloadInt(event.Payload, "context_prompt_tokens", "total_tokens"); estimateTokens > 0 {
				applyChatContextTokens(b.session, estimateTokens, windowTokens, true)
			}
		}
	default:
		return
	}
}

func (b *chatRuntimeEventBridge) applySessionCompactStatus(event runtimeevents.Event) {
	if b == nil || b.session == nil || !b.isPrimarySessionEvent(event) {
		return
	}
	switch event.Type {
	case runtimechat.EventSessionCompactCompleted:
		contextTokens := firstPositivePayloadInt(event.Payload, "token_after", "context_prompt_tokens", "prompt_tokens_after")
		windowTokens := firstPositivePayloadInt(event.Payload, "max_context_tokens", "context_window_tokens")
		b.session.TurnContextTokenCount = 0
		if contextTokens > 0 {
			applyChatContextTokensReset(b.session, contextTokens, windowTokens, true)
		} else if b.session.Interaction != nil {
			b.session.Interaction.RefreshStatus("")
		}
		// Auto-compact updates title lineage on the runtime session; pull the
		// new title into the terminal notifier without waiting for the next turn.
		if err := syncRuntimeSessionBackIntoCLI(b.session); err != nil {
			logpkg.Debugf("failed to sync session after compact completed: %v", err)
		}
		refreshChatTitleMetadata(b.session)
	case runtimechat.EventSessionCompactStarted, runtimechat.EventSessionCompactSkipped, runtimechat.EventSessionCompactFailed:
		windowTokens := firstPositivePayloadInt(event.Payload, "max_context_tokens", "context_window_tokens")
		if windowTokens > 0 && b.session.ContextWindowTokenCount != windowTokens {
			b.session.ContextWindowTokenCount = windowTokens
			if b.session.Interaction != nil {
				b.session.Interaction.RefreshStatus("")
			}
		}
	}
}

func (b *chatRuntimeEventBridge) approvalRequestForEvent(event runtimeevents.Event) *runtimechat.ApprovalRequest {
	if b == nil {
		return nil
	}
	approval := b.pendingApprovalForSession(event.SessionID)
	if approval != nil {
		return approval
	}
	toolName, _ := event.Payload["tool_name"].(string)
	reason, _ := event.Payload["reason"].(string)
	return &runtimechat.ApprovalRequest{
		ToolName: strings.TrimSpace(toolName),
		Reason:   strings.TrimSpace(reason),
	}
}

func approvalRequestContextLines(payload map[string]interface{}) []string {
	if payload == nil {
		return nil
	}
	lines := make([]string, 0, 2)
	scopeParts := make([]string, 0, 4)
	if teamID := truncateChatRuntimeText(payloadStringValue(payload["team_id"]), 80); teamID != "" {
		scopeParts = append(scopeParts, "team="+teamID)
	}
	if taskID := truncateChatRuntimeText(payloadStringValue(payload["task_id"]), 80); taskID != "" {
		scopeParts = append(scopeParts, "task="+taskID)
	}
	if teammateID := truncateChatRuntimeText(payloadStringValue(payload["teammate_id"]), 80); teammateID != "" {
		scopeParts = append(scopeParts, "teammate="+teammateID)
	} else if agentID := truncateChatRuntimeText(payloadStringValue(payload["agent_id"]), 80); agentID != "" {
		scopeParts = append(scopeParts, "agent="+agentID)
	}
	if permissionMode := truncateChatRuntimeText(payloadStringValue(payload["permission_mode"]), 80); permissionMode != "" {
		scopeParts = append(scopeParts, "permission_mode="+permissionMode)
	}
	if len(scopeParts) > 0 {
		lines = append(lines, strings.Join(scopeParts, " "))
	}

	routeParts := make([]string, 0, 6)
	if provider := truncateChatRuntimeText(payloadStringValue(payload["route_provider"]), 80); provider != "" {
		routeParts = append(routeParts, "provider="+provider)
	}
	if model := truncateChatRuntimeText(payloadStringValue(payload["route_model"]), 80); model != "" {
		routeParts = append(routeParts, "model="+model)
	}
	if effort := truncateChatRuntimeText(payloadStringValue(payload["route_reasoning_effort"]), 80); effort != "" {
		routeParts = append(routeParts, "reasoning="+effort)
	}
	if source := truncateChatRuntimeText(payloadStringValue(payload["route_source"]), 80); source != "" {
		routeParts = append(routeParts, "route_source="+source)
	}
	if payloadBoolValue(payload, "fallback_used") {
		routeParts = append(routeParts, "fallback=true")
	}
	if fallbackReason := truncateChatRuntimeText(payloadStringValue(payload["fallback_reason"]), 80); fallbackReason != "" {
		routeParts = append(routeParts, "fallback_reason="+fallbackReason)
	}
	if warnings := stringSliceValueAny(payload["route_warnings"]); len(warnings) > 0 {
		routeParts = append(routeParts, "warnings="+strings.Join(warnings, ","))
	}
	if len(routeParts) > 0 {
		lines = append(lines, strings.Join(routeParts, " "))
	}
	return lines
}

type approvalPromptDecision uint8

const (
	approvalPromptInvalid approvalPromptDecision = iota
	approvalPromptAllowOnce
	approvalPromptAllowReuse
	approvalPromptDeny
	approvalPromptShowDetails
)

func approvalDecisionPrompt() string {
	return "[审批] 请选择 [1] 仅本次允许  [2] 拒绝  [3] 查看完整参数（兼容 y/n）： "
}

func approvalDecisionPromptWithReuse(scope string) string {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return approvalDecisionPrompt()
	}
	return fmt.Sprintf("[审批] 请选择 [1] 仅本次允许  [2] 拒绝  [3] 查看完整参数  [4] 允许并在%s复用同类只读审批 10 分钟： ", scope)
}

func parseApprovalPromptDecision(input string) approvalPromptDecision {
	return parseApprovalPromptDecisionWithReuse(input, false)
}

func parseApprovalPromptDecisionWithReuse(input string, reuseAvailable bool) approvalPromptDecision {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "1", "y", "yes", "允许", "同意":
		return approvalPromptAllowOnce
	case "4":
		if reuseAvailable {
			return approvalPromptAllowReuse
		}
		return approvalPromptInvalid
	case "", "2", "n", "no", "拒绝", "deny":
		return approvalPromptDeny
	case "3":
		return approvalPromptShowDetails
	default:
		return approvalPromptInvalid
	}
}

func approvalReusePromptScope(session *ChatSession, approval *runtimechat.ApprovalRequest) string {
	if session == nil || approval == nil || approvalGrantFamily(strings.TrimSpace(approval.ToolName), approval.ArgsJSON) == "" {
		return ""
	}
	ctx := snapshotChatRuntimeContext(session)
	switch ctx.ApprovalReuseMode {
	case chatApprovalReuseSessionReadOnlyShell:
		return "当前会话内"
	case chatApprovalReuseTeamReadOnlyShell:
		if ctx.ActiveTeam != nil && strings.TrimSpace(ctx.ActiveTeam.TeamID) != "" {
			return "当前团队内"
		}
	}
	return ""
}

func upsertPriorityPromptValidationLine(lines []string, prefix, line string) []string {
	prefix = strings.TrimSpace(prefix)
	filtered := make([]string, 0, len(lines)+1)
	for _, existing := range lines {
		if prefix != "" && strings.HasPrefix(strings.TrimSpace(existing), prefix) {
			continue
		}
		filtered = append(filtered, existing)
	}
	return append(filtered, line)
}

func approvalPriorityPromptLines(approval *runtimechat.ApprovalRequest, contextLines []string) []string {
	lines := []string{"[审批] Agent 请求执行需要授权的操作"}
	toolName := "未知工具"
	if approval != nil {
		if value := strings.TrimSpace(approval.ToolName); value != "" {
			toolName = value
		}
	}
	lines = append(lines, "[审批] 工具："+toolName)

	if approval != nil {
		if reason := strings.TrimSpace(approval.Reason); reason != "" {
			lines = append(lines, "[审批] 原因："+humanApprovalReason(reason))
		} else {
			lines = append(lines, "[审批] 原因：工具策略要求确认")
		}
		if risk := strings.TrimSpace(approval.RiskLevel); risk != "" {
			lines = append(lines, "[审批] 风险等级："+humanApprovalRiskLevel(risk))
		}
	}
	for _, line := range contextLines {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, "[审批] 上下文："+line)
		}
	}

	previewLines := approvalRequestPreviewLines(approval)
	if len(previewLines) == 0 {
		lines = append(lines, "[审批] 参数摘要：（无参数）")
	} else {
		for _, line := range previewLines {
			lines = append(lines, localizeApprovalPreviewLine(line))
		}
	}
	lines = append(lines, "[审批] 操作：[1] 仅本次允许  [2] 拒绝  [3] 查看完整参数")
	return lines
}

func humanApprovalReason(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "permission_mode_requires_approval":
		return "当前权限模式要求在执行前获得确认（permission_mode_requires_approval）"
	case "manual approval":
		return "当前工具策略要求人工审批"
	case "approval_required":
		return "当前操作需要审批"
	default:
		return strings.TrimSpace(reason)
	}
}

func humanApprovalRiskLevel(risk string) string {
	switch strings.ToLower(strings.TrimSpace(risk)) {
	case "low":
		return "低（low）"
	case "medium":
		return "中（medium）"
	case "high":
		return "高（high）"
	case "critical":
		return "严重（critical）"
	default:
		return strings.TrimSpace(risk)
	}
}

func localizeApprovalPreviewLine(line string) string {
	line = strings.TrimSpace(line)
	labels := []struct {
		prefix string
		label  string
	}{
		{"command=", "命令"},
		{"workdir=", "工作目录"},
		{"cwd=", "工作目录"},
		{"url=", "网址"},
		{"path=", "路径"},
		{"query=", "查询"},
		{"prompt=", "提示"},
		{"args=", "参数摘要"},
	}
	for _, item := range labels {
		if strings.HasPrefix(line, item.prefix) {
			return "[审批] " + item.label + "：" + strings.TrimSpace(strings.TrimPrefix(line, item.prefix))
		}
	}
	return "[审批] 参数摘要：" + line
}

func approvalFullParameterLines(approval *runtimechat.ApprovalRequest) []string {
	const maxVisibleParameterLines = 12
	const maxVisibleParameterLineRunes = 240

	lines := []string{"[审批] 完整参数："}
	if approval == nil || len(approval.ArgsJSON) == 0 {
		return append(lines, "[审批]   （无参数）", "[审批] 查看完毕，请继续选择。")
	}

	formatted := strings.TrimSpace(string(approval.ArgsJSON))
	var payload interface{}
	if json.Unmarshal(approval.ArgsJSON, &payload) == nil {
		if indented, err := json.MarshalIndent(payload, "", "  "); err == nil {
			formatted = string(indented)
		}
	}
	formatted = strings.ReplaceAll(formatted, "\r\n", "\n")
	parameterLines := strings.Split(formatted, "\n")
	visibleCount := len(parameterLines)
	if visibleCount > maxVisibleParameterLines {
		visibleCount = maxVisibleParameterLines
	}
	for _, line := range parameterLines[:visibleCount] {
		lines = append(lines, "[审批]   "+truncateChatRuntimeText(line, maxVisibleParameterLineRunes))
	}
	if omitted := len(parameterLines) - visibleCount; omitted > 0 {
		lines = append(lines, fmt.Sprintf("[审批]   已省略 %d 行；可从日志或调试输出查看完整参数。", omitted))
	}
	return append(lines, "[审批] 查看完毕，请继续选择。")
}

func normalizedQuestionSuggestions(suggestions []string) []string {
	normalized := make([]string, 0, len(suggestions))
	for _, suggestion := range suggestions {
		if suggestion = strings.TrimSpace(suggestion); suggestion != "" {
			normalized = append(normalized, suggestion)
		}
	}
	return normalized
}

func questionPriorityPromptLines(prompt string, suggestions []string) []string {
	lines := []string{"[提问] Agent 需要你的补充信息", "[提问] 问题：" + strings.TrimSpace(prompt)}
	for index, suggestion := range normalizedQuestionSuggestions(suggestions) {
		lines = append(lines, fmt.Sprintf("[提问] %d. %s", index+1, suggestion))
	}
	return lines
}

func questionAnswerPrompt(required bool, hasSuggestions bool) string {
	choiceHint := ""
	if hasSuggestions {
		choiceHint = "，可输入建议编号"
	}
	if required {
		return "[提问] 请输入回答" + choiceHint + "（必答）： "
	}
	return "[提问] 请输入回答" + choiceHint + "（可选，直接 Enter 跳过）： "
}

func mapQuestionSuggestionAnswer(input string, suggestions []string) string {
	answer := strings.TrimSpace(input)
	if answer == "" {
		return ""
	}
	normalized := normalizedQuestionSuggestions(suggestions)
	choice, err := strconv.Atoi(answer)
	if err == nil && choice >= 1 && choice <= len(normalized) {
		return normalized[choice-1]
	}
	return answer
}

func showChatRuntimePriorityPrompt(session *ChatSession, lines []string, prompt string) (string, func(), bool) {
	return newChatPromptOverlay(session).showPriorityPrompt(lines, prompt)
}

func renderChatRuntimePriorityPromptTranscript(session *ChatSession, lines []string, prompt string, answer string) {
	if session == nil || session.Interaction == nil {
		return
	}
	transcriptLines := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimRight(strings.ReplaceAll(line, "\r\n", "\n"), "\n"))
		if line != "" {
			transcriptLines = append(transcriptLines, line)
		}
	}
	prompt = strings.TrimRight(strings.ReplaceAll(prompt, "\r\n", "\n"), "\n")
	if strings.TrimSpace(prompt) != "" {
		if answer = strings.TrimSpace(answer); answer != "" {
			prompt += answer
		}
		transcriptLines = append(transcriptLines, prompt)
	}
	if len(transcriptLines) == 0 {
		return
	}
	transcript := strings.Join(transcriptLines, "\n")
	if bridge := session.RuntimeEventBridge; bridge != nil {
		if target := bridge.currentPriorityTranscriptTarget(); target.valid() {
			session.Interaction.RenderPriorityPromptTranscript(transcript, target)
			return
		}
	}
	// Slash-command confirmations also reuse this display helper but do not
	// have a runtime approval/question event. They are genuine direct output
	// and therefore require their own typed supplement injection.
	session.Interaction.RenderLocalSupplement(transcript)
}

func (b *chatRuntimeEventBridge) shouldSuppressApprovalTimeline(event runtimeevents.Event) bool {
	if b == nil || event.Type != runtimechat.EventApprovalRequested {
		return false
	}
	approval := b.approvalRequestForEvent(event)
	if approval == nil {
		return false
	}
	if b.session != nil && !b.session.NoInteractive {
		return true
	}
	grantKey := b.autoApprovalGrantKey(event.SessionID, approval)
	return grantKey != "" && b.hasApprovalGrant(grantKey)
}

func (b *chatRuntimeEventBridge) shouldSuppressTimelineDuringAssistantStream(event runtimeevents.Event) bool {
	if b == nil || !b.HasRenderedAssistantDelta() || b.HasRenderedAssistantFinal() {
		return false
	}
	switch event.Type {
	case runtimechat.EventLLMRequestFinished, "llm.request.finished":
		return true
	default:
		return false
	}
}

func (b *chatRuntimeEventBridge) shouldFlushReasoningOnSessionEnd(event runtimeevents.Event) bool {
	if b == nil || b.session == nil || event.Type != runtimechat.EventSessionEnd {
		return false
	}
	if !b.hasRenderedReasoningDelta() || b.hasRenderedReasoningFinal() {
		return false
	}
	if b.finalizeReasoning != nil {
		b.finalizeReasoning()
	}
	b.renderMu.Lock()
	b.renderedReasoningFinal = true
	b.renderMu.Unlock()
	return true
}

// shouldFinalizeAssistantDeltaOnTerminalEvent closes coordinator-local stream
// bookkeeping only after the encoder has committed the same Scene cell.
// A successful llm.request.finished is merely a transport boundary: production
// publishes the authoritative assistant.message afterward. Resetting the
// coordinator there makes that final unable to emit its fenced
// FinalizeActiveCellAction, leaving the last coalesced delta tail outside native
// history. Failed requests and session termination do close the Scene cell and
// may therefore finalize the coordinator immediately.
func (b *chatRuntimeEventBridge) shouldFinalizeAssistantDeltaOnTerminalEvent(event runtimeevents.Event) bool {
	if b == nil || b.session == nil {
		return false
	}
	switch event.Type {
	case runtimechat.EventLLMRequestFinished, "llm.request.finished":
		if _, reported := event.Payload["success"]; reported && !payloadBoolValue(event.Payload, "success") {
			break
		}
		if strings.TrimSpace(payloadStringValue(event.Payload["error"])) == "" {
			return false
		}
	case runtimechat.EventSessionEnd, runtimechat.EventSessionInterrupted:
	default:
		return false
	}
	if !b.HasRenderedAssistantDelta() || b.HasRenderedAssistantFinal() || b.hasFinalizedAssistantDelta() {
		return false
	}
	if b.finalizeDelta != nil {
		b.finalizeDelta()
	}
	b.markAssistantDeltaFinalized()
	return true
}

func (b *chatRuntimeEventBridge) handleAssistantReasoning(event runtimeevents.Event) bool {
	if b == nil || b.session == nil {
		return false
	}
	if event.Type != runtimechat.EventAssistantReasoning && event.Type != "assistant.reasoning" {
		return false
	}
	if !shouldRenderInteractiveOutput(b.session) || !b.isPrimarySessionEvent(event) {
		return false
	}
	if !chatReasoningOutputEnabled(b.session) {
		return true
	}
	block := runtimetypes.ReasoningBlockFromMap(event.Payload["reasoning"])
	if block == nil {
		return false
	}
	display := block.RawDisplayText()
	if b.hasRenderedReasoningFinal() {
		return true
	}
	if b.hasRenderedReasoningDelta() && !isReasoningStreamDeltaBlock(block) && b.completeReasoning != nil {
		if b.completeReasoning(block) {
			b.renderMu.Lock()
			b.renderedReasoningFinal = true
			b.renderMu.Unlock()
			return true
		}
		// completeReasoning 失败（reasoning 已提前 reset/flush）：增量已渲染，
		// 不得重放完整内容当新 delta 或再走 timeline，避免双份输出。
		if b.hasRenderedReasoningDelta() {
			return true
		}
	}
	if block.Streamable && display != "" && b.writeReasoningDelta != nil && b.session.Interaction != nil && b.session.Interaction.SupportsLiveStream() {
		b.renderMu.Lock()
		b.renderedReasoningDelta = true
		b.renderMu.Unlock()
		b.writeReasoningDelta(block)
		return true
	}
	rendered := chatReasoningTimelineEvent(strings.TrimSpace(event.TraceID), payloadStringValue(event.Payload["step"]), block)
	if rendered.Line == "" {
		return false
	}
	if b.shouldRenderTimelineEvent(rendered) {
		b.emitTimelineEvent(rendered)
	}
	b.renderMu.Lock()
	b.renderedReasoningFinal = true
	b.renderMu.Unlock()
	return true
}

func (b *chatRuntimeEventBridge) handleAssistantDelta(event runtimeevents.Event) bool {
	if b == nil || b.session == nil || event.Type != runtimechat.EventAssistantDelta {
		return false
	}
	ordered, handled := b.orderAssistantDelta(event)
	if handled && len(ordered) == 0 {
		return true
	}
	if !handled {
		ordered = []runtimeevents.Event{event}
	}
	for _, orderedEvent := range ordered {
		b.renderAssistantDelta(orderedEvent)
	}
	return true
}

func (b *chatRuntimeEventBridge) renderAssistantDelta(event runtimeevents.Event) {
	if b.HasRenderedAssistantFinal() {
		return
	}
	if !shouldRenderInteractiveOutput(b.session) || !b.isPrimarySessionEvent(event) {
		return
	}
	delta, _ := event.Payload["delta"].(string)
	if delta == "" {
		delta, _ = event.Payload["content"].(string)
	}
	if delta == "" {
		return
	}
	b.markAssistantDeltaRendered(delta)
	if b.writeDelta != nil {
		b.writeDelta(delta)
	}
}

// orderAssistantDelta enforces append-stream ordering before bytes reach the
// terminal. Events without the new identity fields retain the legacy path so
// older providers and persisted test fixtures remain compatible.
func (b *chatRuntimeEventBridge) orderAssistantDelta(event runtimeevents.Event) ([]runtimeevents.Event, bool) {
	turnID, streamID := assistantEventIdentity(event)
	sequence, hasSequence := assistantEventSequence(event)
	if turnID == "" || streamID == "" || !hasSequence || sequence == 0 {
		return nil, false
	}

	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	if b.assistantStreams == nil {
		b.assistantStreams = make(map[string]*chatAssistantStreamState)
	}
	if b.retiredAssistantStreams == nil {
		b.retiredAssistantStreams = make(map[string]struct{})
	}
	if b.retiredTurnIDs == nil {
		b.retiredTurnIDs = make(map[string]struct{})
	}
	if !b.acceptAssistantTurnLocked(turnID) {
		return nil, true
	}
	if _, retired := b.retiredAssistantStreams[streamID]; retired {
		return nil, true
	}
	if b.activeAssistantStreamID != "" && b.activeAssistantStreamID != streamID {
		b.retiredAssistantStreams[b.activeAssistantStreamID] = struct{}{}
		delete(b.assistantStreams, b.activeAssistantStreamID)
	}
	b.activeAssistantStreamID = streamID
	state := b.assistantStreams[streamID]
	if state == nil {
		state = &chatAssistantStreamState{
			turnID:       turnID,
			streamID:     streamID,
			nextSequence: 1,
			pending:      make(map[uint64]runtimeevents.Event),
		}
		b.assistantStreams[streamID] = state
	}
	if state.turnID != turnID || state.tainted || sequence < state.nextSequence {
		return nil, true
	}
	from, coalesced := streamCoalescedFrom(event)
	if coalesced && from < state.nextSequence {
		return nil, true
	}
	if sequence > state.nextSequence {
		if !(coalesced && from == state.nextSequence) {
			cacheKey := sequence
			if coalesced {
				cacheKey = from
			}
			if _, exists := state.pending[cacheKey]; exists {
				return nil, true
			}
			size := runtimeevents.ApproximateEventBytes(event)
			if len(state.pending) >= chatAssistantStreamPendingLimit || size > chatAssistantStreamPendingByteLimit-state.pendingBytes {
				state.pending = make(map[uint64]runtimeevents.Event)
				state.pendingBytes = 0
				state.tainted = true
				return nil, true
			}
			state.pending[cacheKey] = event
			state.pendingBytes += size
			return nil, true
		}
	}

	ordered := make([]runtimeevents.Event, 0, 1)
	ordered = append(ordered, event)
	if coalesced && from == state.nextSequence && sequence >= from {
		state.nextSequence = sequence + 1
	} else {
		state.nextSequence++
	}
	for {
		pending, ok := state.pending[state.nextSequence]
		if !ok {
			break
		}
		delete(state.pending, state.nextSequence)
		state.pendingBytes -= runtimeevents.ApproximateEventBytes(pending)
		if state.pendingBytes < 0 {
			state.pendingBytes = 0
		}
		ordered = append(ordered, pending)
		if pendingFrom, pendingCoalesced := streamCoalescedFrom(pending); pendingCoalesced && pendingFrom == state.nextSequence {
			if pendingSeq, pendingOK := assistantEventSequence(pending); pendingOK {
				state.nextSequence = pendingSeq + 1
				continue
			}
		}
		state.nextSequence++
	}
	return ordered, true
}

func assistantEventIdentity(event runtimeevents.Event) (turnID, streamID string) {
	if event.Payload == nil {
		return "", ""
	}
	return strings.TrimSpace(payloadStringValue(event.Payload["turn_id"])), strings.TrimSpace(payloadStringValue(event.Payload["stream_id"]))
}

func assistantEventSequence(event runtimeevents.Event) (uint64, bool) {
	if event.Payload == nil {
		return 0, false
	}
	return assistantPayloadSequence(event.Payload["sequence"])
}

func assistantPayloadSequence(value interface{}) (uint64, bool) {
	switch value := value.(type) {
	case uint64:
		return value, true
	case uint:
		return uint64(value), true
	case int:
		if value < 0 {
			return 0, true
		}
		return uint64(value), true
	case int64:
		if value < 0 {
			return 0, true
		}
		return uint64(value), true
	case float64:
		if value < 0 {
			return 0, true
		}
		return uint64(value), true
	case json.Number:
		parsed, err := strconv.ParseUint(string(value), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

// streamCoalescedFrom returns the first sequence folded into a coalesced
// stream event. The event's visible sequence is the interval end.
func streamCoalescedFrom(event runtimeevents.Event) (uint64, bool) {
	if event.Payload == nil {
		return 0, false
	}
	from, ok := assistantPayloadSequence(event.Payload[streamCoalescedFromKey])
	return from, ok && from > 0
}

func (b *chatRuntimeEventBridge) acceptAssistantTurnLocked(turnID string) bool {
	if turnID == "" {
		return true
	}
	if _, retired := b.retiredTurnIDs[turnID]; retired {
		return false
	}
	return b.activeTurnID != "" && b.activeTurnID == turnID
}

func (b *chatRuntimeEventBridge) finalizeAssistantDelta(event runtimeevents.Event) bool {
	if b == nil || b.session == nil || event.Type != runtimechat.EventAssistantMessage {
		return false
	}
	if !b.isPrimarySessionEvent(event) || !b.HasRenderedAssistantDelta() {
		return false
	}
	if b.finalizeDelta != nil {
		b.finalizeDelta()
	}
	b.markAssistantDeltaFinalized()
	content, _ := event.Payload["content"].(string)
	if b.hasRenderedAssistantContent(content) {
		b.markAssistantFinalRendered(content)
	}
	return true
}

func (b *chatRuntimeEventBridge) handlePrimaryAssistantMessage(event runtimeevents.Event) bool {
	if b == nil || b.session == nil || event.Type != runtimechat.EventAssistantMessage {
		return false
	}
	if !shouldRenderInteractiveOutput(b.session) || !b.isPrimarySessionEvent(event) {
		return false
	}
	if b.HasRenderedAssistantFinal() {
		return b.handleAsyncTeamAssistantMessage(event)
	}
	if !b.acceptAssistantFinal(event) {
		return true
	}
	if block := runtimetypes.ReasoningBlockFromMap(event.Payload["reasoning"]); block != nil {
		b.renderReasoningFromAssistantMessage(event, block)
	}
	renderedSummary := false
	if rendered := b.renderAsyncTeamSummaryFallback(event); rendered.Line != "" && b.shouldRenderTimelineEvent(rendered) {
		b.emitTimelineEvent(rendered)
		renderedSummary = true
	}
	if b.HasRenderedAssistantDelta() {
		content, _ := event.Payload["content"].(string)
		if strings.TrimSpace(content) != "" && b.completeDelta != nil && b.completeDelta(content) {
			b.markAssistantFinalRendered(content)
			b.commitAssistantFinal(event)
			return true
		}
		if strings.TrimSpace(content) != "" && b.hasFinalizedAssistantDelta() {
			content = sanitizeInteractiveAsyncTeamLaunchResponse(content)
			// 防重：finalize 已把 delta 内容渲染（文本直播已写屏 / markdown
			// 收尾已整块提交）时，迟到的终态消息不应整段重渲染——同一正文
			// 只 commit 最终 ownership（exactly-once），不重复显示。
			if b.renderResponse != nil && !b.hasRenderedAssistantContent(content) {
				b.renderResponse(content)
			}
			b.markAssistantFinalRendered(content)
			b.commitAssistantFinal(event)
			return true
		}
		rendered := b.finalizeAssistantDelta(event)
		if rendered && b.HasRenderedAssistantFinal() {
			b.commitAssistantFinal(event)
		}
		return rendered || renderedSummary
	}
	content, _ := event.Payload["content"].(string)
	if strings.TrimSpace(content) == "" {
		return renderedSummary
	}
	content = sanitizeInteractiveAsyncTeamLaunchResponse(content)
	if b.renderResponse != nil {
		b.renderResponse(content)
	}
	b.markAssistantFinalRendered(content)
	b.commitAssistantFinal(event)
	return true
}

// acceptAssistantFinal makes turn identity, not response text, the authority
// for exactly-once final commit. The final snapshot supersedes any buffered
// out-of-order deltas for its stream.
func (b *chatRuntimeEventBridge) acceptAssistantFinal(event runtimeevents.Event) bool {
	turnID, streamID := assistantEventIdentity(event)
	if turnID == "" {
		return true
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	if b.retiredTurnIDs == nil {
		b.retiredTurnIDs = make(map[string]struct{})
	}
	if b.acceptedAssistantFinalTurns == nil {
		b.acceptedAssistantFinalTurns = make(map[string]struct{})
	}
	if b.assistantStreams == nil {
		b.assistantStreams = make(map[string]*chatAssistantStreamState)
	}
	if !b.acceptAssistantTurnLocked(turnID) {
		return false
	}
	if _, accepted := b.acceptedAssistantFinalTurns[turnID]; accepted {
		return false
	}
	if streamID != "" {
		if b.retiredAssistantStreams == nil {
			b.retiredAssistantStreams = make(map[string]struct{})
		}
		if _, retired := b.retiredAssistantStreams[streamID]; retired {
			return false
		}
		if b.activeAssistantStreamID != "" && b.activeAssistantStreamID != streamID {
			b.retiredAssistantStreams[b.activeAssistantStreamID] = struct{}{}
			delete(b.assistantStreams, b.activeAssistantStreamID)
		}
		b.activeAssistantStreamID = streamID
		b.retiredAssistantStreams[streamID] = struct{}{}
		if state := b.assistantStreams[streamID]; state != nil {
			state.pending = make(map[uint64]runtimeevents.Event)
			state.pendingBytes = 0
			delete(b.assistantStreams, streamID)
		}
	}
	b.acceptedAssistantFinalTurns[turnID] = struct{}{}
	return true
}

// commitAssistantFinal records durable final ownership only after the terminal
// renderer has successfully committed the authoritative snapshot. Acceptance
// alone must not suppress the executor fallback.
func (b *chatRuntimeEventBridge) commitAssistantFinal(event runtimeevents.Event) {
	if b == nil {
		return
	}
	turnID, _ := assistantEventIdentity(event)
	if turnID == "" {
		return
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	if b.finalAssistantTurns == nil {
		b.finalAssistantTurns = make(map[string]struct{})
	}
	b.finalAssistantTurns[turnID] = struct{}{}
}

func (b *chatRuntimeEventBridge) handleAsyncTeamAssistantMessage(event runtimeevents.Event) bool {
	if b == nil || b.session == nil || event.Type != runtimechat.EventAssistantMessage {
		return false
	}
	if !shouldRenderInteractiveOutput(b.session) || !b.isPrimarySessionEvent(event) {
		return false
	}
	renderedSomething := false
	if rendered := b.renderAsyncTeamSummaryFallback(event); rendered.Line != "" && b.shouldRenderTimelineEvent(rendered) {
		b.emitTimelineEvent(rendered)
		renderedSomething = true
	}
	if response := b.asyncTeamAssistantResponse(event); response != "" {
		if b.renderResponse != nil {
			b.renderResponse(response)
		}
		renderedSomething = true
	}
	return renderedSomething
}

func (b *chatRuntimeEventBridge) renderReasoningFromAssistantMessage(event runtimeevents.Event, block *runtimetypes.ReasoningBlock) {
	if b == nil || block == nil || b.hasRenderedReasoningFinal() {
		return
	}
	if !chatReasoningOutputEnabled(b.session) {
		return
	}
	if b.hasRenderedReasoningDelta() && !b.hasRenderedReasoningFinal() && b.completeReasoning != nil {
		if b.completeReasoning(block) {
			b.renderMu.Lock()
			b.renderedReasoningFinal = true
			b.renderMu.Unlock()
			return
		}
		// completeReasoning 失败（如 reasoning 已提前 reset/flush）：该内容
		// 已经以流式增量或 flush 完整渲染过，不得再走 timeline 完整渲染
		// 造成同一 reasoning 双份输出。
		if b.hasRenderedReasoningDelta() {
			return
		}
	}
	rendered := chatReasoningTimelineEvent(strings.TrimSpace(event.TraceID), "", block)
	if rendered.Line == "" {
		return
	}
	if b.shouldRenderTimelineEvent(rendered) {
		b.emitTimelineEvent(rendered)
	}
	b.renderMu.Lock()
	b.renderedReasoningFinal = true
	b.renderMu.Unlock()
}

func (b *chatRuntimeEventBridge) shouldRenderTimelineEvent(rendered chatRuntimeTimelineEvent) bool {
	if b == nil || strings.TrimSpace(rendered.Line) == "" {
		return false
	}
	key := strings.TrimSpace(rendered.DedupKey)
	if key == "" {
		return true
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	if b.rendered == nil {
		b.rendered = make(map[string]struct{})
	}
	if _, exists := b.rendered[key]; exists {
		return false
	}
	b.rendered[key] = struct{}{}
	return true
}

func (b *chatRuntimeEventBridge) renderAsyncTeamSummaryFallback(event runtimeevents.Event) chatRuntimeTimelineEvent {
	if b == nil || b.session == nil || event.Type != runtimechat.EventAssistantMessage {
		return chatRuntimeTimelineEvent{}
	}
	if !b.matchesPrimarySessionID(event.SessionID) {
		return chatRuntimeTimelineEvent{}
	}
	ctx := snapshotChatRuntimeContext(b.session)
	if ctx.ActiveTeam == nil || strings.TrimSpace(ctx.ActiveTeam.TeamID) == "" {
		return chatRuntimeTimelineEvent{}
	}
	teamID := strings.TrimSpace(ctx.ActiveTeam.TeamID)
	if !b.hasRenderedTimelineKey("team.completed:"+teamID+":done") &&
		!b.hasRenderedTimelineKey("team.completed:"+teamID+":failed") &&
		!b.hasRenderedTimelineKey("team.completed:"+teamID+":partially_completed") &&
		!b.hasRenderedTimelineKey("team.completed:"+teamID+":canceled") &&
		!b.isTerminalTeam(teamID) {
		return chatRuntimeTimelineEvent{}
	}
	content := truncateChatRuntimeText(payloadStringValue(event.Payload["content"]), 200)
	if content == "" {
		return chatRuntimeTimelineEvent{}
	}
	return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
		Kind:   cell.TimelineTeam,
		Status: cell.StatusSuccess,
		Tag:    "[team summary]",
		Title:  teamID + " " + content,
	}, "team.summary:"+teamID)
}

func (b *chatRuntimeEventBridge) hasRenderedTimelineKey(key string) bool {
	if b == nil || strings.TrimSpace(key) == "" {
		return false
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	if b.rendered == nil {
		return false
	}
	_, exists := b.rendered[strings.TrimSpace(key)]
	return exists
}

func (b *chatRuntimeEventBridge) pendingApprovalForSession(sessionID string) *runtimechat.ApprovalRequest {
	if b == nil || b.session == nil || b.session.LocalRuntimeHost == nil || b.session.LocalRuntimeHost.SessionHub == nil {
		return nil
	}
	actor, err := b.session.LocalRuntimeHost.SessionHub.GetOrCreate(strings.TrimSpace(sessionID))
	if err != nil || actor == nil {
		return nil
	}
	return actor.PendingApproval()
}

func (b *chatRuntimeEventBridge) autoApprovalGrantKey(sessionID string, approval *runtimechat.ApprovalRequest) string {
	if approval == nil {
		return ""
	}
	family := approvalGrantFamily(strings.TrimSpace(approval.ToolName), approval.ArgsJSON)
	if family == "" {
		return ""
	}
	scope := b.autoApprovalScope(sessionID)
	if scope == "" {
		return ""
	}
	return scope + "|" + family
}

func (b *chatRuntimeEventBridge) autoApprovalScope(sessionID string) string {
	if b == nil || b.session == nil {
		return ""
	}
	ctx := snapshotChatRuntimeContext(b.session)
	switch ctx.ApprovalReuseMode {
	case chatApprovalReuseSessionReadOnlyShell:
		if sid := strings.TrimSpace(sessionID); sid != "" {
			return "session:" + sid
		}
		return ""
	case chatApprovalReuseTeamReadOnlyShell:
		if ctx.ActiveTeam != nil {
			if teamID := strings.TrimSpace(ctx.ActiveTeam.TeamID); teamID != "" {
				return "team:" + teamID
			}
		}
		return ""
	default:
		return ""
	}
}

func (b *chatRuntimeEventBridge) hasApprovalGrant(key string) bool {
	if b == nil || strings.TrimSpace(key) == "" {
		return false
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	if b.approvalGrants == nil {
		return false
	}
	key = strings.TrimSpace(key)
	expiresAt, exists := b.approvalGrants[key]
	if !exists {
		return false
	}
	if !expiresAt.IsZero() && time.Now().UTC().After(expiresAt) {
		delete(b.approvalGrants, key)
		return false
	}
	return true
}

func (b *chatRuntimeEventBridge) rememberApprovalGrant(key string) {
	if b == nil || strings.TrimSpace(key) == "" {
		return
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	if b.approvalGrants == nil {
		b.approvalGrants = make(map[string]time.Time)
	}
	b.approvalGrants[strings.TrimSpace(key)] = time.Now().UTC().Add(chatApprovalGrantTTL)
}

func (b *chatRuntimeEventBridge) approvalGrantStatusLines(now time.Time) []string {
	if b == nil {
		return nil
	}
	now = now.UTC()
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	b.pruneApprovalGrantsLocked(now)
	if len(b.approvalGrants) == 0 {
		return nil
	}
	keys := make([]string, 0, len(b.approvalGrants))
	for key := range b.approvalGrants {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		expiresAt := b.approvalGrants[key]
		scope, family, _ := strings.Cut(key, "|")
		remaining := expiresAt.Sub(now)
		if remaining < 0 {
			remaining = 0
		}
		remaining = remaining.Round(time.Second)
		lines = append(lines, fmt.Sprintf(
			"  scope=%s family=%s expires_in=%s",
			approvalGrantScopeLabel(scope),
			firstNonEmptyChatValue(strings.TrimSpace(family), "unknown"),
			remaining,
		))
	}
	return lines
}

func (b *chatRuntimeEventBridge) clearApprovalGrants() int {
	if b == nil {
		return 0
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	count := len(b.approvalGrants)
	b.approvalGrants = nil
	return count
}

func (b *chatRuntimeEventBridge) nonInteractiveApprovalError(approval *runtimechat.ApprovalRequest) error {
	mode := chatRuntimePermissionModeLabel(b.session)
	toolName := "unknown"
	reason := ""
	if approval != nil {
		if name := strings.TrimSpace(approval.ToolName); name != "" {
			toolName = name
		}
		reason = strings.TrimSpace(approval.Reason)
	}

	parts := []string{
		fmt.Sprintf("非交互模式（--no-interactive）无法审批工具调用 tool=%s", toolName),
		fmt.Sprintf("permission-mode=%s", mode),
	}
	if reason != "" {
		parts = append(parts, fmt.Sprintf("reason=%s", truncateChatRuntimeText(reason, 160)))
	}
	parts = append(parts, "建议：纯文本问答使用 `aicli exec --disable-tools \"...\"`；信任当前工作区工具执行时使用 `aicli exec --yolo \"...\"`；需要人工确认时使用 `aicli chat`")
	return fmt.Errorf("%s", strings.Join(parts, "；"))
}

func (b *chatRuntimeEventBridge) nonInteractiveQuestionError(prompt string) error {
	parts := []string{
		"非交互模式（--no-interactive）无法回答运行时提问",
	}
	if prompt = strings.TrimSpace(prompt); prompt != "" {
		parts = append(parts, fmt.Sprintf("prompt=%s", truncateChatRuntimeText(prompt, 160)))
	}
	parts = append(parts, "建议：把必要信息直接写进 exec 输入；纯文本问答使用 `aicli exec --disable-tools \"...\"`；需要交互追问时使用 `aicli chat`")
	return fmt.Errorf("%s", strings.Join(parts, "；"))
}

func chatRuntimePermissionModeLabel(session *ChatSession) string {
	ctx := snapshotChatRuntimeContext(session)
	mode := strings.TrimSpace(string(ctx.PermissionMode))
	if mode == "" {
		return string(runtimepolicy.ModeDefault)
	}
	return mode
}

func (b *chatRuntimeEventBridge) maybeRenderPermissionModeHint(reason string) {
	if b == nil || b.session == nil || b.writeLine == nil {
		return
	}
	if strings.TrimSpace(reason) != "permission_mode_requires_approval" {
		return
	}
	ctx := snapshotChatRuntimeContext(b.session)
	b.renderMu.Lock()
	if b.permissionHintShown {
		b.renderMu.Unlock()
		return
	}
	b.permissionHintShown = true
	b.renderMu.Unlock()

	mode := strings.TrimSpace(string(ctx.PermissionMode))
	if mode == "" {
		mode = string(runtimepolicy.ModeDefault)
	}
	b.renderLocalApprovalSupplement(fmt.Sprintf(
		"[tip] 当前 permission-mode=%s。若你信任当前会话，可用 --yolo（等价于 --permission-mode bypass_permissions）关闭审批；--approval-reuse=%s 可减少重复只读审批（shell/网络搜索等）。",
		mode,
		formatChatApprovalReuseMode(ctx.ApprovalReuseMode),
	))
}

func (b *chatRuntimeEventBridge) approvalPromptHint(sessionID string, approval *runtimechat.ApprovalRequest) string {
	if b == nil || b.session == nil || approval == nil {
		return ""
	}
	ctx := snapshotChatRuntimeContext(b.session)
	if ctx.ApprovalReuseMode == chatApprovalReuseOff {
		return ""
	}
	scope := b.autoApprovalScope(sessionID)
	if scope == "" {
		if ctx.ApprovalReuseMode == chatApprovalReuseTeamReadOnlyShell {
			return "[tip] 当前没有 active team，team_readonly_shell 不会缓存这次审批。"
		}
		return ""
	}
	family := approvalGrantFamily(strings.TrimSpace(approval.ToolName), approval.ArgsJSON)
	switch family {
	case "readonly_shell":
		return fmt.Sprintf("[tip] 本次命令属于 readonly_shell；%s 里还没有该家族的审批缓存，所以这次仍需审批。", approvalGrantScopeLabel(scope))
	case "approved_shell":
		return fmt.Sprintf("[tip] 本次命令属于 approved_shell；首次仍需审批，后续同一%s内的同家族命令可自动复用。", approvalGrantScopeLabel(scope))
	case "readonly_network":
		return fmt.Sprintf("[tip] 本次命令属于 readonly_network；%s 里还没有该家族的审批缓存，所以这次仍需审批。", approvalGrantScopeLabel(scope))
	}

	if details := approvalGrantExclusionReason(strings.TrimSpace(approval.ToolName), approval.ArgsJSON); details != "" {
		return "[tip] " + details
	}
	return ""
}

func approvalGrantScopeLabel(scope string) string {
	scope = strings.TrimSpace(scope)
	switch {
	case strings.HasPrefix(scope, "session:"):
		return "当前会话"
	case strings.HasPrefix(scope, "team:"):
		return "当前 team"
	default:
		return "当前作用域"
	}
}

func approvalGrantExclusionReason(toolName string, argsJSON json.RawMessage) string {
	normalized := strings.ToLower(strings.TrimSpace(toolName))
	if normalized == "" {
		return ""
	}
	if runtimepolicy.IsWriteLikeToolName(normalized) {
		return "本次工具属于写操作，不参与 approval-reuse。"
	}
	if !runtimepolicy.IsShellLikeToolName(normalized) || len(argsJSON) == 0 {
		return ""
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(argsJSON, &payload); err != nil {
		return ""
	}
	if mutated := extractApprovalStringSlice(payload["mutated_paths"]); len(mutated) > 0 {
		return "本次命令声明了 mutated_paths，按写操作处理，不参与 approval-reuse。"
	}
	command := payloadStringValue(payload["command"])
	if command != "" && isDangerousShellCommand(command) {
		return "本次 shell 命令包含写入或外部副作用风险，不参与 approval-reuse。"
	}
	return ""
}

func (b *chatRuntimeEventBridge) pruneApprovalGrantsLocked(now time.Time) {
	if b == nil || b.approvalGrants == nil {
		return
	}
	for key, expiresAt := range b.approvalGrants {
		if !expiresAt.IsZero() && now.After(expiresAt) {
			delete(b.approvalGrants, key)
		}
	}
}

func normalizeRenderedAssistantContent(content string) string {
	content = sanitizeInteractiveAsyncTeamLaunchResponse(content)
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	return strings.TrimSpace(content)
}

func (b *chatRuntimeEventBridge) markAssistantDeltaRendered(delta string) {
	if b == nil {
		return
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	b.renderedAssistantDelta = true
	b.renderedAssistantDeltaFinalized = false
	b.renderedAssistantDeltaContent.WriteString(delta)
}

func (b *chatRuntimeEventBridge) markAssistantDeltaFinalized() {
	if b == nil {
		return
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	b.renderedAssistantDeltaFinalized = true
	normalized := normalizeRenderedAssistantContent(b.renderedAssistantDeltaContent.String())
	b.renderedAssistantDeltaDigest, b.renderedAssistantDeltaLength = digestRenderedAssistantContent(normalized)
	b.renderedAssistantDeltaContent = strings.Builder{}
}

func (b *chatRuntimeEventBridge) hasFinalizedAssistantDelta() bool {
	if b == nil {
		return false
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	return b.renderedAssistantDeltaFinalized
}

func (b *chatRuntimeEventBridge) markAssistantFinalRendered(content string) {
	if b == nil {
		return
	}
	normalized := normalizeRenderedAssistantContent(content)
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	b.renderedAssistantFinal = true
	if normalized != "" {
		b.renderedAssistantFinalDigest, b.renderedAssistantFinalLength = digestRenderedAssistantContent(normalized)
		b.renderedAssistantDeltaContent = strings.Builder{}
	}
}

func (b *chatRuntimeEventBridge) hasRenderedAssistantContent(content string) bool {
	if b == nil {
		return false
	}
	normalized := normalizeRenderedAssistantContent(content)
	if normalized == "" {
		return false
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	digest, length := digestRenderedAssistantContent(normalized)
	if b.renderedAssistantFinalLength == length && b.renderedAssistantFinalDigest == digest {
		return true
	}
	if b.renderedAssistantDeltaFinalized && b.renderedAssistantDeltaLength == length && b.renderedAssistantDeltaDigest == digest {
		return true
	}
	return false
}

func digestRenderedAssistantContent(content string) ([sha256.Size]byte, int) {
	if content == "" {
		return [sha256.Size]byte{}, 0
	}
	return sha256.Sum256([]byte(content)), len(content)
}

func (b *chatRuntimeEventBridge) HasRenderedAssistantDelta() bool {
	if b == nil {
		return false
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	return b.renderedAssistantDelta
}

func (b *chatRuntimeEventBridge) HasRenderedAssistantFinal() bool {
	if b == nil {
		return false
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	return b.renderedAssistantFinal
}

func (b *chatRuntimeEventBridge) MarkAssistantFinalRendered() {
	b.markAssistantFinalRendered("")
}

func (b *chatRuntimeEventBridge) MarkAssistantFinalResponseRendered(content string) {
	if b == nil {
		return
	}
	b.renderMu.Lock()
	if turnID := strings.TrimSpace(b.executorTurnID); turnID != "" {
		if b.finalAssistantTurns == nil {
			b.finalAssistantTurns = make(map[string]struct{})
		}
		b.finalAssistantTurns[turnID] = struct{}{}
	}
	b.renderMu.Unlock()
	b.markAssistantFinalRendered(content)
}

func (b *chatRuntimeEventBridge) HasRenderedAssistantFinalResponse(content string) bool {
	if b.HasCommittedExecutorTurnFinal() {
		return true
	}
	return b.hasRenderedAssistantContent(content)
}

// BindExecutorTurn associates the string returned by the actor executor with
// the same durable turn ID carried by assistant_message.
func (b *chatRuntimeEventBridge) BindExecutorTurn(turnID string) {
	if b == nil {
		return
	}
	turnID = strings.TrimSpace(turnID)
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	b.executorTurnID = turnID
	if turnID == "" || b.activeTurnID != "" || !b.runActive {
		return
	}
	if _, retired := b.retiredTurnIDs[turnID]; !retired {
		b.activeTurnID = turnID
	}
}

func (b *chatRuntimeEventBridge) HasCommittedExecutorTurnFinal() bool {
	if b == nil {
		return false
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	turnID := strings.TrimSpace(b.executorTurnID)
	if turnID == "" {
		return false
	}
	_, committed := b.finalAssistantTurns[turnID]
	return committed
}

func (b *chatRuntimeEventBridge) hasRenderedReasoningDelta() bool {
	if b == nil {
		return false
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	return b.renderedReasoningDelta
}

func (b *chatRuntimeEventBridge) hasRenderedReasoningFinal() bool {
	if b == nil {
		return false
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	return b.renderedReasoningFinal
}

func (b *chatRuntimeEventBridge) isPrimarySessionEvent(event runtimeevents.Event) bool {
	if b == nil {
		return false
	}
	return b.matchesPrimarySessionID(event.SessionID)
}

// setPrimarySessionID updates the event bridge's immutable routing identity
// when the mutable ChatSession runtime projection is replaced. Runtime events
// are processed asynchronously, so they must never read RuntimeSession
// directly while a command is restoring or switching the session.
func (b *chatRuntimeEventBridge) setPrimarySessionID(sessionID string) {
	if b == nil {
		return
	}
	b.primarySessionMu.Lock()
	b.primarySessionID = strings.TrimSpace(sessionID)
	b.primarySessionMu.Unlock()
}

func (b *chatRuntimeEventBridge) matchesPrimarySessionID(sessionID string) bool {
	if b == nil {
		return false
	}
	b.primarySessionMu.RLock()
	primarySessionID := b.primarySessionID
	b.primarySessionMu.RUnlock()
	return primarySessionID != "" && strings.TrimSpace(sessionID) == primarySessionID
}

func (b *chatRuntimeEventBridge) primaryRuntimeSessionID() string {
	if b == nil {
		return ""
	}
	b.primarySessionMu.RLock()
	primarySessionID := b.primarySessionID
	b.primarySessionMu.RUnlock()
	return primarySessionID
}

func isTeamLifecycleRuntimeEvent(eventType string) bool {
	eventType = strings.TrimSpace(eventType)
	return strings.HasPrefix(eventType, "team.") || strings.HasPrefix(eventType, "task.")
}

func isTaskRouteResolvedRuntimeEvent(eventType string) bool {
	return strings.EqualFold(strings.TrimSpace(eventType), team.TaskRouteResolvedEvent)
}

func (b *chatRuntimeEventBridge) shouldAcceptTeamLifecycleRuntimeEvent(event runtimeevents.Event) bool {
	if b == nil {
		return false
	}
	if b.isPrimarySessionEvent(event) {
		return true
	}
	if !isTaskRouteResolvedRuntimeEvent(event.Type) {
		return false
	}
	if b.session == nil {
		return false
	}
	ctx := snapshotChatRuntimeContext(b.session)
	if ctx.ActiveTeam == nil {
		return false
	}
	activeTeamID := strings.TrimSpace(ctx.ActiveTeam.TeamID)
	eventTeamID := strings.TrimSpace(payloadStringValue(event.Payload["team_id"]))
	return activeTeamID != "" && eventTeamID == activeTeamID
}

func (b *chatRuntimeEventBridge) isTerminalTeam(teamID string) bool {
	if b == nil || b.session == nil || b.session.LocalRuntimeHost == nil || b.session.LocalRuntimeHost.TeamStore == nil {
		return false
	}
	record, err := b.session.LocalRuntimeHost.TeamStore.GetTeam(context.Background(), strings.TrimSpace(teamID))
	if err != nil || record == nil {
		return false
	}
	return team.IsTerminalTeamStatus(record.Status)
}

func (b *chatRuntimeEventBridge) asyncTeamAssistantResponse(event runtimeevents.Event) string {
	if b == nil || b.session == nil || event.Type != runtimechat.EventAssistantMessage {
		return ""
	}
	if !b.matchesPrimarySessionID(event.SessionID) {
		return ""
	}
	ctx := snapshotChatRuntimeContext(b.session)
	if ctx.ActiveTeam == nil || strings.TrimSpace(ctx.ActiveTeam.TeamID) == "" {
		return ""
	}
	teamID := strings.TrimSpace(ctx.ActiveTeam.TeamID)
	if !b.hasRenderedTimelineKey("team.completed:"+teamID+":done") &&
		!b.hasRenderedTimelineKey("team.completed:"+teamID+":failed") &&
		!b.hasRenderedTimelineKey("team.completed:"+teamID+":partially_completed") &&
		!b.hasRenderedTimelineKey("team.completed:"+teamID+":canceled") &&
		!b.isTerminalTeam(teamID) {
		return ""
	}
	content := strings.TrimSpace(payloadStringValue(event.Payload["content"]))
	if content == "" {
		return ""
	}
	key := "team.async_response:" + teamID
	if b.hasRenderedTimelineKey(key) {
		return ""
	}
	b.renderMu.Lock()
	if b.rendered == nil {
		b.rendered = make(map[string]struct{})
	}
	b.rendered[key] = struct{}{}
	b.renderMu.Unlock()
	b.markAssistantFinalRendered(content)
	return content
}

func (b *chatRuntimeEventBridge) writePromptIfIdle() {
	if b == nil || b.writePrompt == nil || b.session == nil {
		return
	}
	b.renderMu.Lock()
	runActive := b.runActive
	b.renderMu.Unlock()
	if runActive {
		return
	}
	if !shouldDisplayInteractivePrompt(b.session) {
		return
	}
	if sessionID := b.primaryRuntimeSessionID(); sessionID != "" && b.session.LocalRuntimeHost != nil && b.session.LocalRuntimeHost.RuntimeStore != nil {
		state, err := b.session.LocalRuntimeHost.RuntimeStore.LoadState(context.Background(), sessionID)
		if err == nil && state != nil && state.Status != runtimechat.SessionIdle {
			return
		}
	}
	if b.session.Interaction != nil {
		b.session.Interaction.SchedulePromptRedraw()
		return
	}
	b.writePrompt()
}

func (b *chatRuntimeEventBridge) lookupActor(sessionID string) (*runtimechat.SessionActor, error) {
	if b == nil || b.session == nil || b.session.LocalRuntimeHost == nil || b.session.LocalRuntimeHost.SessionHub == nil {
		return nil, fmt.Errorf("session hub not configured")
	}
	return b.session.LocalRuntimeHost.SessionHub.GetOrCreate(strings.TrimSpace(sessionID))
}

func (b *chatRuntimeEventBridge) resolveApproval(ctx context.Context, sessionID, requestID string, allow bool) error {
	if b == nil {
		return nil
	}
	if b.approveTool != nil {
		return b.approveTool(ctx, sessionID, requestID, allow)
	}
	actor, err := b.lookupActor(sessionID)
	if err != nil {
		return err
	}
	return actor.ApproveTool(ctx, requestID, allow)
}

func (b *chatRuntimeEventBridge) renderApprovalDecision(approval *runtimechat.ApprovalRequest, allowed bool) {
	if b == nil || approval == nil {
		return
	}
	status := "denied"
	if allowed {
		status = "approved"
	}
	toolName := strings.TrimSpace(approval.ToolName)
	if toolName == "" {
		toolName = "tool"
	}
	b.renderLocalApprovalSupplement(fmt.Sprintf("[approval] %s: %s", status, toolName))
}

// renderLocalApprovalSupplement is for visible local consequences of a
// runtime approval that have no distinct runtime event source: permission
// guidance, reuse hints, and the locally chosen decision. It must not use
// RenderAsyncLine because the request event has already been encoded.
func (b *chatRuntimeEventBridge) renderLocalApprovalSupplement(line string) {
	if b == nil || strings.TrimSpace(line) == "" {
		return
	}
	if b.session != nil && b.session.Interaction != nil && !b.session.NoInteractive && !b.session.JSONOutput {
		b.session.Interaction.RenderLocalSupplement(line)
		return
	}
	if b.writeLine != nil {
		b.writeLine(line)
	}
}

func (b *chatRuntimeEventBridge) resolveQuestion(ctx context.Context, sessionID, questionID, answer string) error {
	if b == nil {
		return nil
	}
	if b.answerQuestion != nil {
		return b.answerQuestion(ctx, sessionID, questionID, answer)
	}
	actor, err := b.lookupActor(sessionID)
	if err != nil {
		return err
	}
	return actor.AnswerQuestion(ctx, questionID, answer)
}

func (b *chatRuntimeEventBridge) setRunError(err error) {
	if b == nil || err == nil {
		return
	}
	b.runMu.Lock()
	defer b.runMu.Unlock()
	if b.runErr == nil {
		b.runErr = err
	}
}

type chatRuntimeTimelineEvent struct {
	Line      string
	DedupKey  string
	DebugOnly bool
	Timeline  *cell.TimelineEvent
	Document  *render.Document
}

func typedChatRuntimeTimelineEvent(event cell.TimelineEvent, dedupKey string) chatRuntimeTimelineEvent {
	doc := event.Document()
	return chatRuntimeTimelineEvent{
		Line:     event.FormatPlain(),
		DedupKey: dedupKey,
		Timeline: &event,
		Document: &doc,
	}
}

func documentedChatRuntimeTimelineEvent(doc render.Document, dedupKey string) chatRuntimeTimelineEvent {
	return chatRuntimeTimelineEvent{
		Line:     render.PlainBackend{}.Render(doc),
		DedupKey: dedupKey,
		Document: &doc,
	}
}

func chatToolCompletedTimelineEvent(line string, dedupKeys ...string) chatRuntimeTimelineEvent {
	line = strings.TrimRight(strings.ReplaceAll(line, "\r\n", "\n"), "\n")
	if strings.TrimSpace(line) == "" {
		return chatRuntimeTimelineEvent{}
	}
	dedupKey := ""
	if len(dedupKeys) > 0 {
		dedupKey = strings.TrimSpace(dedupKeys[0])
	}
	// Edited and read-only Diff supplements have a dedicated structured renderer.
	// Keep this compatibility envelope until the tool result carries FileDiff.
	if len(uidiff.ParseSupplementBlocks(line)) > 0 {
		return chatRuntimeTimelineEvent{Line: line, DedupKey: dedupKey}
	}
	lines := strings.Split(line, "\n")
	title := strings.TrimPrefix(lines[0], "• ")
	if title == lines[0] {
		return chatRuntimeTimelineEvent{Line: line, DedupKey: dedupKey}
	}
	status := cell.StatusSuccess
	if strings.HasPrefix(title, "Failed ") {
		status = cell.StatusError
	}
	return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
		Kind:               cell.TimelineTool,
		Status:             status,
		Marker:             "• ",
		SuppressKindPrefix: true,
		Title:              title,
		Details:            lines[1:],
	}, dedupKey)
}

func isRuntimeToolRequestedEventType(eventType string) bool {
	return eventType == "tool.requested" || eventType == runtimechat.EventToolStarted
}

func (b *chatRuntimeEventBridge) emitTimelineEvent(rendered chatRuntimeTimelineEvent) {
	if b == nil || strings.TrimSpace(rendered.Line) == "" {
		return
	}
	if rendered.Document != nil && b.writeDocument != nil && b.writeDocument(*rendered.Document) {
		return
	}
	if b.writeLine != nil {
		b.writeLine(rendered.Line)
	}
}

func renderChatRuntimeEvent(event runtimeevents.Event) string {
	return renderChatRuntimeTimelineEvent(event).Line
}

func renderChatRuntimeTimelineEvent(event runtimeevents.Event) chatRuntimeTimelineEvent {
	teamID := payloadStringValue(event.Payload["team_id"])
	switch event.Type {
	case runtimechat.EventLLMRequestStarted, "llm.request.started":
		return chatRuntimeTimelineEvent{}
	case runtimechat.EventLLMRequestFinished, "llm.request.finished":
		return renderLLMRequestFinishedTimelineEvent(event)
	case "llm.retry":
		return renderLLMRetryTimelineEvent(event)
	case runtimechat.EventAssistantReasoning, "assistant.reasoning":
		if rendered := renderChatReasoningTimelineEvent(event); rendered.Line != "" {
			return rendered
		}
		return chatRuntimeTimelineEvent{}
	case runtimechat.EventSessionCompactStarted, runtimechat.EventSessionCompactCompleted, runtimechat.EventSessionCompactSkipped, runtimechat.EventSessionCompactFailed:
		if rendered := renderSessionCompactTimelineEvent(event); rendered.Line != "" {
			return rendered
		}
		return chatRuntimeTimelineEvent{}
	case runtimechat.EventSessionEnd:
		if rendered := renderPromptPreflightSessionEndTimelineEvent(event); rendered.Line != "" {
			return rendered
		}
		return chatRuntimeTimelineEvent{}
	case "planning.started":
		return chatRuntimeTimelineEvent{}
	case "planning.completed":
		return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
			Kind: cell.TimelinePlanning, Status: cell.StatusSuccess, Title: "completed",
		}, "")
	case "subagent.batch.started":
		return chatRuntimeTimelineEvent{}
	case "subagent.batch.completed":
		return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
			Kind: cell.TimelineTeam, Status: cell.StatusSuccess, Tag: "[subagents]", Title: "completed",
		}, "")
	case "subagent.batch.failed", "subagent.batch.canceled", "subagent.batch.cancelled", "subagent.batch.timed_out", "subagent.batch.orphaned", "subagent.batch.circuit_open":
		status := strings.TrimPrefix(event.Type, "subagent.batch.")
		details := []string{}
		if reason := strings.TrimSpace(payloadStringValue(event.Payload["error"])); reason != "" {
			details = append(details, reason)
		}
		return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
			Kind: cell.TimelineTeam, Status: cell.StatusError, Tag: "[subagents]", Title: status, Details: details,
		}, firstNonEmptyChatValue(payloadStringValue(event.Payload["batch_id"]), event.Type))
	case "subagent.started":
		return chatRuntimeTimelineEvent{}
	case "subagent.completed":
		return typedChatRuntimeTimelineEvent(renderSubagentCompletedTimelineEvent(event), "")
	case "subagent.denied":
		return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
			Kind:   cell.TimelineTeam,
			Status: cell.StatusDenied,
			Tag:    "[subagent]",
			Title:  fmt.Sprintf("denied %s", payloadStringValue(event.Payload["reason"])),
		}, "")
	case "tool.requested", runtimechat.EventToolStarted:
		payload := runtimeToolTimelinePayload(event)
		display := compactToolDisplayTextWithSource(
			firstNonEmptyChatValue(strings.TrimSpace(event.ToolName), payloadStringValue(payload["tool_name"])),
			"",
			payloadStringValue(payload["command_text"]),
			payloadStringValue(payload["arg_preview"]),
			payloadStringValue(payload[toolresult.SourceKey]),
		)
		if display == "" {
			return chatRuntimeTimelineEvent{}
		}
		return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
			Kind:               cell.TimelineTool,
			Status:             cell.StatusRunning,
			Marker:             "• ",
			SuppressKindPrefix: true,
			Title:              "Running " + display,
			Details:            compactToolContextLines(payload),
		}, "")
	case "tool.completed", "tool.failed", "tool.cancelled", "tool.canceled", runtimechat.EventToolFinished:
		payload := runtimeToolTimelinePayload(event)
		if (event.Type == "tool.failed" || event.Type == "tool.cancelled" || event.Type == "tool.canceled") &&
			strings.TrimSpace(payloadStringValue(payload["error"])) == "" {
			payload["error"] = firstNonEmptyChatValue(payloadStringValue(payload["reason"]), event.Type)
		}
		line := renderCompactToolCompletedWithPayload(firstNonEmptyChatValue(strings.TrimSpace(event.ToolName), payloadStringValue(payload["tool_name"])), "", payloadStringValue(payload["command_text"]), payloadStringValue(payload["arg_preview"]), payloadStringValue(payload[toolresult.SourceKey]), chatToolSummaryLines(payload), payload)
		rendered := []string{line}
		if waitingLine := chatToolPostCommandHint(payload); waitingLine != "" {
			rendered = append(rendered, waitingLine)
		}
		line = strings.Join(rendered, "\n")
		dedupKey := runtimeToolCallTimelineKey(event, payload)
		if dedupKey != "" {
			dedupKey = "tool-final:" + dedupKey
		}
		return chatToolCompletedTimelineEvent(line, dedupKey)
	case "tool.denied":
		return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
			Kind:    cell.TimelineTool,
			Status:  cell.StatusDenied,
			Title:   payloadStringValue(event.Payload["reason"]),
			Details: compactToolContextLines(event.Payload),
		}, "")
	case "tool.progress":
		return renderToolProgressTimelineEvent(event)
	case runtimechat.EventApprovalRequested:
		return renderApprovalRequestedTimelineEvent(event)
	case runtimechat.EventQuestionAsked:
		return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
			Kind: cell.TimelineQuestion, Status: cell.StatusPending, Title: payloadStringValue(event.Payload["prompt"]),
		}, "")
	case runtimechat.EventMailboxReceived:
		messageID := firstNonEmptyChatValue(payloadStringValue(event.Payload["message_id"]), "?")
		fromAgent := firstNonEmptyChatValue(payloadStringValue(event.Payload["from_agent"]), "?")
		toAgent := firstNonEmptyChatValue(payloadStringValue(event.Payload["to_agent"]), "*")
		kind := firstNonEmptyChatValue(payloadStringValue(event.Payload["kind"]), "info")
		body := truncateChatRuntimeText(payloadStringValue(event.Payload["body"]), 160)
		taskID := payloadStringValue(event.Payload["task_id"])
		if body == "" && taskID == "" && fromAgent == "?" && toAgent == "*" && kind == "info" {
			return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
				Kind: cell.TimelineNotice, Status: cell.StatusInfo, Tag: "[mailbox]",
				Title: fmt.Sprintf("%s %s", firstNonEmptyChatValue(teamID, "?"), messageID),
			}, "mailbox:"+messageID)
		}
		title := fmt.Sprintf("%s -> %s", fromAgent, toAgent)
		if taskID != "" {
			title += " " + taskID
		}
		if body != "" {
			title += " " + body
		}
		status := cell.StatusInfo
		if strings.EqualFold(kind, "error") || strings.EqualFold(kind, "failed") {
			status = cell.StatusError
		}
		return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
			Kind: cell.TimelineNotice, Status: status, Tag: "[" + kind + "]", Title: title,
		}, "mailbox:"+messageID)
	case team.TaskRouteResolvedEvent:
		return renderTaskRouteResolvedTimelineEvent(event, teamID)
	case "task.started", "task.completed", "task.failed", "task.blocked", "team.task.completed", "team.task.failed", "team.task.blocked":
		action := chatRuntimeTaskAction(event.Type)
		if action == "started" {
			return chatRuntimeTimelineEvent{}
		}
		taskID := firstNonEmptyChatValue(payloadStringValue(event.Payload["task_id"]), "?")
		assignee := payloadStringValue(event.Payload["assignee"])
		summary := truncateChatRuntimeText(payloadStringValue(event.Payload["summary"]), 160)
		title := fmt.Sprintf("%s %s", action, taskID)
		if assignee != "" {
			title += fmt.Sprintf(" @%s", assignee)
		}
		if summary != "" {
			title += " " + summary
		}
		details := make([]string, 0, 8)
		if action == "failed" && strings.EqualFold(strings.TrimSpace(payloadStringValue(event.Payload["error_type"])), "prompt_preflight") {
			title += " [prompt preflight]"
			if reason := promptPreflightReasonSummary(event.Payload); reason != "" {
				details = append(details, "  原因: "+reason)
			}
			if model := promptPreflightModelSummary(event.Payload); model != "" {
				details = append(details, "  模型: "+model)
			}
			if budget := promptPreflightBudgetSourceSummary(event.Payload); budget != "" {
				details = append(details, "  预算: "+budget)
			}
			if recovery := promptPreflightRecoverySummary(event.Payload); recovery != "" {
				details = append(details, "  恢复: "+recovery)
			}
			if extras := runtimeContextSummaryLines(event.Payload, false); len(extras) > 0 {
				details = append(details, extras...)
			}
		} else if action == "blocked" && strings.EqualFold(strings.TrimSpace(payloadStringValue(event.Payload["replan_error_type"])), "prompt_preflight") {
			replanPayload := extractPrefixedRuntimePayload(event.Payload, "replan")
			if reason := promptPreflightReasonSummary(replanPayload); reason != "" {
				details = append(details, "  replan: [prompt preflight] "+reason)
			}
			if model := promptPreflightModelSummary(replanPayload); model != "" {
				details = append(details, "  replan 模型: "+model)
			}
			if budget := promptPreflightBudgetSourceSummary(replanPayload); budget != "" {
				details = append(details, "  replan 预算: "+budget)
			}
			if recovery := promptPreflightRecoverySummary(replanPayload); recovery != "" {
				details = append(details, "  replan 恢复: "+recovery)
			}
			if extras := runtimeContextSummaryLines(replanPayload, false); len(extras) > 0 {
				details = append(details, extras...)
			}
		}
		eventStatus := cell.StatusSuccess
		if action == "failed" {
			eventStatus = cell.StatusError
		} else if action == "blocked" {
			eventStatus = cell.StatusDenied
		}
		return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
			Kind: cell.TimelineTask, Status: eventStatus, Title: title, Details: details,
		}, fmt.Sprintf("%s:%s:%s", strings.TrimSpace(event.Type), teamID, taskID))
	case "team.completed":
		status := firstNonEmptyChatValue(payloadStringValue(event.Payload["status"]), "done")
		eventStatus := cell.StatusSuccess
		if status == "failed" || status == "canceled" {
			eventStatus = cell.StatusError
		}
		return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
			Kind:   cell.TimelineTeam,
			Status: eventStatus,
			Title:  fmt.Sprintf("completed %s status=%s", firstNonEmptyChatValue(teamID, "?"), status),
		}, fmt.Sprintf("team.completed:%s:%s", teamID, status))
	case "team.interrupted":
		status := firstNonEmptyChatValue(payloadStringValue(event.Payload["status"]), "paused")
		return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
			Kind:   cell.TimelineTeam,
			Status: cell.StatusInfo,
			Title:  fmt.Sprintf("interrupted %s status=%s", firstNonEmptyChatValue(teamID, "?"), status),
		}, fmt.Sprintf("team.interrupted:%s:%s", teamID, status))
	case "team.plan.failed", "team.plan.replan_failed", "team.summary.failed":
		action := strings.TrimSpace(event.Type)
		tag := "[team]"
		title := "failed"
		switch action {
		case "team.plan.failed":
			tag = "[team plan]"
			title = fmt.Sprintf("failed %s", firstNonEmptyChatValue(teamID, "?"))
		case "team.plan.replan_failed":
			tag = "[team replan]"
			title = fmt.Sprintf("failed %s", firstNonEmptyChatValue(teamID, "?"))
			if taskID := strings.TrimSpace(payloadStringValue(event.Payload["task_id"])); taskID != "" {
				title += " " + taskID
			}
		case "team.summary.failed":
			tag = "[team summary]"
			title = fmt.Sprintf("failed %s", firstNonEmptyChatValue(teamID, "?"))
		}
		details := make([]string, 0, 8)
		if strings.EqualFold(strings.TrimSpace(payloadStringValue(event.Payload["error_type"])), "prompt_preflight") {
			title += " [prompt preflight]"
			if reason := promptPreflightReasonSummary(event.Payload); reason != "" {
				details = append(details, "  原因: "+reason)
			}
			if model := promptPreflightModelSummary(event.Payload); model != "" {
				details = append(details, "  模型: "+model)
			}
			if budget := promptPreflightBudgetSourceSummary(event.Payload); budget != "" {
				details = append(details, "  预算: "+budget)
			}
			if recovery := promptPreflightRecoverySummary(event.Payload); recovery != "" {
				details = append(details, "  恢复: "+recovery)
			}
			if extras := runtimeContextSummaryLines(event.Payload, false); len(extras) > 0 {
				details = append(details, extras...)
			}
		} else if summary := truncateChatRuntimeText(payloadStringValue(event.Payload["error"]), 160); summary != "" {
			details = append(details, "  错误: "+summary)
		}
		return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
			Kind: cell.TimelineTeam, Status: cell.StatusError, Tag: tag, Title: title, Details: details,
		}, fmt.Sprintf("%s:%s:%s", action, teamID, payloadStringValue(event.Payload["task_id"])))
	case "team.summary", "team.summary.generated":
		return renderTeamSummaryTimelineEvent(event, teamID)
	case chatEventInputQueueDetected:
		count := intPayloadValue(event.Payload, "queued_input_count")
		source := firstNonEmptyChatValue(payloadStringValue(event.Payload["source"]), "stdin")
		return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
			Kind: cell.TimelineInput, Status: cell.StatusInfo,
			Title: fmt.Sprintf("queued %d line(s) from %s", count, source),
		}, fmt.Sprintf("input.queue.detected:%s:%d", strings.TrimSpace(event.SessionID), count))
	case chatEventInputQueueDrained:
		return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
			Kind: cell.TimelineInput, Status: cell.StatusSuccess, Title: "queued input drained",
		}, fmt.Sprintf("input.queue.drained:%s", strings.TrimSpace(event.SessionID)))
	case chatEventInputQueueDiscarded:
		count := intPayloadValue(event.Payload, "discarded_count")
		promptKind := payloadStringValue(event.Payload["prompt_kind"])
		title := fmt.Sprintf("discarded %d queued line(s)", count)
		if promptKind != "" {
			title += " before " + promptKind
		}
		return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
			Kind: cell.TimelineInput, Status: cell.StatusInfo,
			Title: title,
		}, fmt.Sprintf("input.queue.discarded:%s:%s:%d", strings.TrimSpace(event.SessionID), promptKind, count))
	default:
		return chatRuntimeTimelineEvent{}
	}
}

func llmRequestDedupKey(event runtimeevents.Event, eventType string) string {
	traceID := firstNonEmptyChatValue(strings.TrimSpace(event.TraceID), payloadStringValue(event.Payload["trace_id"]))
	stepLabel := payloadStringValue(event.Payload["step"])
	if stepLabel == "" {
		return fmt.Sprintf("%s:%s", strings.TrimSpace(eventType), traceID)
	}
	return fmt.Sprintf("%s:%s:%s", strings.TrimSpace(eventType), traceID, stepLabel)
}

func renderLLMRetryTimelineEvent(event runtimeevents.Event) chatRuntimeTimelineEvent {
	parts := chatLLMRetryParts(event)
	if len(parts) == 0 {
		return chatRuntimeTimelineEvent{}
	}
	payload := event.Payload
	traceID := firstNonEmptyChatValue(strings.TrimSpace(event.TraceID), payloadStringValue(payload["trace_id"]))
	stepLabel := payloadStringValue(payload["step"])
	if stepLabel == "" {
		if step := intPayloadValue(payload, "step"); step > 0 {
			stepLabel = fmt.Sprintf("%d", step)
		}
	}
	attempt := intPayloadValue(payload, "attempt")
	reason := strings.TrimSpace(payloadStringValue(payload["retry_reason"]))
	source := strings.TrimSpace(payloadStringValue(payload["source"]))
	return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
		Kind:   cell.TimelineProgress,
		Status: cell.StatusPending,
		Tag:    "[retry]",
		Title:  strings.Join(parts, " "),
	}, fmt.Sprintf("llm.retry:%s:%s:%d:%s:%s", traceID, stepLabel, attempt, reason, source))
}

// chatLLMRetryParts 提取 llm.retry 事件的重试详情片段（step/provider/
// model/attempt/reason/delay/source/error）。timeline 标题与动态状态行的
// 重试信息共用同一构造，保证两处格式一致。
func chatLLMRetryParts(event runtimeevents.Event) []string {
	payload := event.Payload
	if payload == nil {
		return nil
	}
	stepLabel := payloadStringValue(payload["step"])
	if stepLabel == "" {
		if step := intPayloadValue(payload, "step"); step > 0 {
			stepLabel = fmt.Sprintf("%d", step)
		}
	}
	attempt := intPayloadValue(payload, "attempt")
	maxAttempts := intPayloadValue(payload, "max_attempts")
	reason := strings.TrimSpace(payloadStringValue(payload["retry_reason"]))
	delay := intPayloadValue(payload, "retry_delay_ms")
	source := strings.TrimSpace(payloadStringValue(payload["source"]))
	targetParts := make([]string, 0, 3)
	if provider := strings.TrimSpace(payloadStringValue(payload["provider"])); provider != "" {
		targetParts = append(targetParts, provider)
	}
	if protocol := strings.TrimSpace(payloadStringValue(payload["protocol"])); protocol != "" {
		targetParts = append(targetParts, protocol)
	}
	if model := strings.TrimSpace(payloadStringValue(payload["model"])); model != "" {
		targetParts = append(targetParts, model)
	}

	parts := make([]string, 0, 6)
	if stepLabel != "" {
		parts = append(parts, "step="+stepLabel)
	}
	if len(targetParts) > 0 {
		parts = append(parts, strings.Join(targetParts, " / "))
	}
	switch {
	case attempt > 0 && maxAttempts > 0:
		parts = append(parts, fmt.Sprintf("attempt=%d/%d", attempt, maxAttempts))
	case attempt > 0:
		parts = append(parts, fmt.Sprintf("attempt=%d", attempt))
	}
	if reason != "" {
		parts = append(parts, "reason="+reason)
	}
	if delay > 0 {
		parts = append(parts, fmt.Sprintf("delay=%dms", delay))
	}
	if source != "" {
		parts = append(parts, "source="+source)
	}
	if errText := truncateChatRuntimeText(payloadStringValue(payload["error"]), 120); errText != "" {
		parts = append(parts, "error="+errText)
	}
	return parts
}

func renderLLMRequestFinishedTimelineEvent(event runtimeevents.Event) chatRuntimeTimelineEvent {
	payload := event.Payload
	if payload == nil {
		return chatRuntimeTimelineEvent{}
	}
	if !payloadBoolValue(payload, "success") {
		title := "model error"
		attributes := make([]string, 0, 2)
		if code := strings.TrimSpace(payloadStringValue(payload["error_code"])); code != "" {
			attributes = append(attributes, code)
		}
		if _, ok := payload["retryable"]; ok {
			attributes = append(attributes, fmt.Sprintf("retryable=%t", payloadBoolValue(payload, "retryable")))
		}
		if len(attributes) > 0 {
			title += " [" + strings.Join(attributes, ", ") + "]"
		}
		if errText := truncateChatRuntimeText(payloadStringValue(payload["error"]), 240); errText != "" {
			title += " " + errText
		}
		details := make([]string, 0, 1)
		if nextAction := truncateChatRuntimeText(payloadStringValue(payload["next_action"]), 240); nextAction != "" {
			details = append(details, "[action] "+nextAction)
		}
		return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
			Kind: cell.TimelineThinking, Status: cell.StatusError, Title: title, Details: details,
		}, llmRequestDedupKey(event, "llm.request.finished"))
	}
	title := "request finished"
	if target := strings.TrimSpace(chatLLMRequestTargetSummary(payload)); target != "" {
		title += " " + target
	}
	details := runtimeContextSummaryLines(payload, true)
	if len(details) == 0 {
		return chatRuntimeTimelineEvent{}
	}
	rendered := typedChatRuntimeTimelineEvent(cell.TimelineEvent{
		Kind: cell.TimelineThinking, Status: cell.StatusSuccess, Title: title, Details: details,
	}, llmRequestDedupKey(event, "llm.request.finished"))
	rendered.DebugOnly = true
	return rendered
}

func runtimeContextSummaryLines(payload map[string]interface{}, includeUsage bool) []string {
	if payload == nil {
		return nil
	}
	lines := make([]string, 0, 8)

	contextParts := make([]string, 0, 4)
	if current := firstPositivePayloadInt(payload, "context_prompt_tokens", "prompt_tokens_after", "token_after", "prompt_tokens", "token_before", "prompt_tokens_before"); current > 0 {
		contextParts = append(contextParts, fmt.Sprintf("prompt=%d", current))
	}
	if budget := firstPositivePayloadInt(payload, "prompt_budget", "trigger_token_limit"); budget > 0 {
		contextParts = append(contextParts, fmt.Sprintf("budget=%d", budget))
	}
	if window := firstPositivePayloadInt(payload, "context_window_tokens", "max_context_tokens"); window > 0 {
		contextParts = append(contextParts, fmt.Sprintf("window=%d", window))
	}
	if len(contextParts) > 0 {
		lines = append(lines, formatRuntimePanelLine("context", strings.Join(contextParts, " ")))
	}

	if includeUsage {
		usageParts := make([]string, 0, 4)
		if prompt := firstPositivePayloadInt(payload, "usage_prompt_tokens"); prompt > 0 {
			usageParts = append(usageParts, fmt.Sprintf("in=%d", prompt))
		}
		if completion := firstPositivePayloadInt(payload, "usage_completion_tokens"); completion > 0 {
			usageParts = append(usageParts, fmt.Sprintf("out=%d", completion))
		}
		if total := firstPositivePayloadInt(payload, "usage_total_tokens"); total > 0 {
			usageParts = append(usageParts, fmt.Sprintf("total=%d", total))
		}
		if cached := firstPositivePayloadInt(payload, "usage_cache_read_tokens", "usage_cached_tokens"); cached > 0 {
			usageParts = append(usageParts, fmt.Sprintf("cached=%d", cached))
		}
		if created := firstPositivePayloadInt(payload, "usage_cache_creation_tokens"); created > 0 {
			usageParts = append(usageParts, fmt.Sprintf("cache_write=%d", created))
		}
		if ratio, ok := payloadFloatValue(payload, "usage_cache_hit_ratio"); ok {
			usageParts = append(usageParts, fmt.Sprintf("cache_hit=%.1f%%", ratio*100))
		} else if prompt := firstPositivePayloadInt(payload, "usage_prompt_tokens"); prompt > 0 {
			if cached := firstPositivePayloadInt(payload, "usage_cache_read_tokens", "usage_cached_tokens"); cached > 0 {
				usageParts = append(usageParts, fmt.Sprintf("cache_hit=%.1f%%", float64(cached)/float64(prompt)*100))
			}
		}
		if status := strings.TrimSpace(payloadStringValue(payload["usage_cache_status"])); status != "" && status != "hit" {
			usageParts = append(usageParts, "cache_status="+status)
		}
		if reasoning := firstPositivePayloadInt(payload, "usage_reasoning_tokens"); reasoning > 0 {
			usageParts = append(usageParts, fmt.Sprintf("reasoning=%d", reasoning))
		}
		if source := strings.TrimSpace(payloadStringValue(payload["usage_source"])); source != "" {
			usageParts = append(usageParts, "source="+source)
		}
		if len(usageParts) > 0 {
			lines = append(lines, formatRuntimePanelLine("usage", strings.Join(usageParts, " ")))
		}
	}
	if budgetLines := budgetSummaryLines(payload); len(budgetLines) > 0 {
		lines = append(lines, budgetLines...)
	}

	return lines
}

func formatRuntimePanelLine(label string, value string) string {
	label = strings.TrimSpace(label)
	value = strings.TrimSpace(value)
	if label == "" {
		return "  " + value
	}
	return fmt.Sprintf("  %-7s: %s", label, value)
}

func formatRuntimePanelSubLine(label string, value string) string {
	label = strings.TrimSpace(label)
	value = strings.TrimSpace(value)
	if label == "" {
		return "           " + value
	}
	return fmt.Sprintf("           %-10s: %s", label, value)
}

func formatRuntimePanelBullet(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return "             - " + value
}

func formatRuntimePanelWrappedLines(prefix, text string, limit int) []string {
	text = strings.TrimSpace(strings.Join(strings.Fields(text), " "))
	if text == "" {
		return nil
	}
	if limit <= 0 || len([]rune(text)) <= limit {
		return []string{prefix + text}
	}

	indent := strings.Repeat(" ", len(prefix))
	words := strings.Fields(text)
	lines := make([]string, 0, len(words))
	current := ""
	firstLine := true
	appendCurrent := func() {
		if current == "" {
			return
		}
		if firstLine {
			lines = append(lines, prefix+current)
			firstLine = false
		} else {
			lines = append(lines, indent+current)
		}
		current = ""
	}
	flush := func() {
		appendCurrent()
	}
	for _, word := range words {
		wordRunes := []rune(word)
		if len(wordRunes) > limit {
			flush()
			for len(wordRunes) > limit {
				chunk := string(wordRunes[:limit])
				if firstLine {
					lines = append(lines, prefix+chunk)
					firstLine = false
				} else {
					lines = append(lines, indent+chunk)
				}
				wordRunes = wordRunes[limit:]
			}
			if len(wordRunes) > 0 {
				current = string(wordRunes)
			}
			continue
		}
		candidate := word
		if current != "" {
			candidate = current + " " + word
		}
		if len([]rune(candidate)) <= limit {
			current = candidate
			continue
		}
		flush()
		current = word
	}
	flush()
	if len(lines) == 0 {
		return nil
	}
	return lines
}

func budgetSummaryLines(payload map[string]interface{}) []string {
	if payload == nil {
		return nil
	}
	lines := make([]string, 0, 6)
	sourceKey := strings.TrimSpace(payloadStringValue(payload["budget_source"]))
	detail := strings.TrimSpace(payloadStringValue(payload["budget_source_detail"]))
	if source := budgetSourceSummary(sourceKey, detail); source != "" {
		lines = append(lines, formatRuntimePanelLine("budget", "source="+source))
	}
	if sourceKey != "" && detail != "" {
		lines = append(lines, budgetDetailLines(detail)...)
	}
	if candidateLines := budgetCandidateLines(payload); len(candidateLines) > 0 {
		lines = append(lines, candidateLines...)
	}
	return lines
}

func budgetDetailLines(detail string) []string {
	lines := formatRuntimePanelWrappedLines("           detail    : ", detail, 96)
	if len(lines) == 0 {
		return nil
	}
	return lines
}

func budgetCandidateLines(payload map[string]interface{}) []string {
	if payload == nil {
		return nil
	}
	raw, ok := payload["budget_candidates"].(map[string]interface{})
	if !ok || len(raw) == 0 {
		return nil
	}
	selectedSource := strings.TrimSpace(payloadStringValue(payload["budget_source"]))
	keys := make([]string, 0, len(raw))
	for key := range raw {
		key = strings.TrimSpace(key)
		if key != "" {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys)+1)
	lines = append(lines, formatRuntimePanelSubLine("candidates", fmt.Sprintf("%d option(s)", len(keys))))
	for _, key := range keys {
		value := raw[key]
		if line := budgetCandidateLine(key, value, selectedSource); line != "" {
			lines = append(lines, formatRuntimePanelWrappedLines("             - ", line, 88)...)
		}
	}
	return lines
}

func budgetCandidateLine(key string, value interface{}, selectedSource string) string {
	label := budgetCandidateLabel(key)
	selectedSuffix := ""
	if selectedSource != "" && strings.TrimSpace(key) == selectedSource {
		selectedSuffix = "（选中）"
	}
	switch num := value.(type) {
	case int:
		if num > 0 {
			return fmt.Sprintf("%s=%d%s", label, num, selectedSuffix)
		}
	case int64:
		if num > 0 {
			return fmt.Sprintf("%s=%d%s", label, num, selectedSuffix)
		}
	case float64:
		if num > 0 {
			return fmt.Sprintf("%s=%d%s", label, int(num), selectedSuffix)
		}
	}
	if text := strings.TrimSpace(payloadStringValue(value)); text != "" {
		return fmt.Sprintf("%s=%s%s", label, truncateChatRuntimeText(text, 60), selectedSuffix)
	}
	return fmt.Sprintf("%s=%v%s", label, value, selectedSuffix)
}

func budgetCandidateLabel(key string) string {
	switch strings.TrimSpace(key) {
	case "default_context_max_prompt_tokens":
		return "默认 prompt 预算"
	case "default_context_fallback_max_prompt_tokens":
		return "默认 context fallback prompt 预算"
	case "context_max_prompt_tokens":
		return "context manager prompt 预算"
	case "context_fallback_max_prompt_tokens":
		return "context fallback prompt 预算"
	case "model_capability_auto_compact_token_limit":
		return "模型能力 auto-compact token limit"
	case "model_capability_context_ratio":
		return "模型能力 auto-compact ratio"
	case "provider_context_limit_default_ratio":
		return "provider context limit 默认比例"
	case "default_context_window_default_ratio":
		return "默认 context window 比例"
	case "remaining_budget":
		return "当前轮剩余预算"
	default:
		return key
	}
}

func budgetSourceSummary(source, detail string) string {
	switch strings.TrimSpace(source) {
	case "default_context_max_prompt_tokens":
		return "默认 context prompt 预算"
	case "default_context_fallback_max_prompt_tokens":
		return "默认 runtime context fallbackMaxPromptTokens"
	case "context_max_prompt_tokens":
		return "context manager 的 max_prompt_tokens"
	case "context_fallback_max_prompt_tokens":
		return "runtime context fallbackMaxPromptTokens"
	case "remaining_budget":
		return "本轮剩余 prompt 预算"
	case "model_capability_auto_compact_token_limit":
		return "模型能力 auto-compact token limit"
	case "model_capability_auto_compact_ratio":
		return "模型能力 auto-compact ratio"
	case "model_capability_max_context_tokens":
		return "模型能力 max_context_tokens"
	case "provider_context_limit":
		return "provider context limit"
	case "default_context_window_default_ratio":
		return "默认 context window 的自动压缩比例"
	case "":
		return truncateChatRuntimeText(detail, 120)
	default:
		return source
	}
}

func chatLLMRequestTargetSummary(payload map[string]interface{}) string {
	if payload == nil {
		return ""
	}
	provider := strings.TrimSpace(payloadStringValue(payload["provider"]))
	model := strings.TrimSpace(payloadStringValue(payload["model"]))
	switch {
	case provider != "" && model != "":
		return provider + "/" + model
	case provider != "":
		return provider
	default:
		return model
	}
}

func renderSessionCompactTimelineEvent(event runtimeevents.Event) chatRuntimeTimelineEvent {
	payload := event.Payload
	if payload == nil {
		return chatRuntimeTimelineEvent{}
	}
	mode := firstNonEmptyChatValue(payloadStringValue(payload["mode"]), compactruntime.ModeLocal)
	phase := firstNonEmptyChatValue(payloadStringValue(payload["phase"]), compactruntime.PhasePreTurn)
	traceID := firstNonEmptyChatValue(strings.TrimSpace(event.TraceID), payloadStringValue(payload["trace_id"]))
	sessionID := firstNonEmptyChatValue(strings.TrimSpace(event.SessionID), payloadStringValue(payload["session_id"]))
	dedupKeyBase := fmt.Sprintf("%s:%s:%s:%s", strings.TrimSpace(event.Type), sessionID, traceID, phase)

	switch event.Type {
	case runtimechat.EventSessionCompactStarted:
		return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
			Kind:    cell.TimelinePlanning,
			Status:  cell.StatusRunning,
			Tag:     "[context]",
			Title:   fmt.Sprintf("session compact started mode=%s phase=%s %s", mode, phase, sessionCompactBudgetSummary(payload)),
			Details: runtimeContextSummaryLines(payload, false),
		}, dedupKeyBase)
	case runtimechat.EventSessionCompactCompleted:
		title := fmt.Sprintf(
			"session compact completed mode=%s phase=%s token %d -> %d compacted_messages=%d history_messages=%d",
			mode,
			phase,
			intPayloadValue(payload, "token_before"),
			intPayloadValue(payload, "token_after"),
			intPayloadValue(payload, "compacted_messages"),
			intPayloadValue(payload, "message_count_after"),
		)
		if checkpointID := truncateChatRuntimeText(payloadStringValue(payload["checkpoint_id"]), 80); checkpointID != "" {
			title += " checkpoint_id=" + checkpointID
		}
		if generation := intPayloadValue(payload, "compact_generation"); generation > 0 {
			title += fmt.Sprintf(" generation=%d", generation)
		}
		if rootTitle := truncateChatRuntimeText(payloadStringValue(payload["compact_root_title"]), 80); rootTitle != "" {
			title += " root_title=" + rootTitle
		}
		return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
			Kind: cell.TimelinePlanning, Status: cell.StatusSuccess, Tag: "[context]",
			Title: title, Details: runtimeContextSummaryLines(payload, true),
		}, dedupKeyBase)
	case runtimechat.EventSessionCompactSkipped:
		rendered := typedChatRuntimeTimelineEvent(cell.TimelineEvent{
			Kind: cell.TimelinePlanning, Status: cell.StatusInfo, Tag: "[context]",
			Title: fmt.Sprintf("session compact skipped mode=%s phase=%s reason=%s", mode, phase, sessionCompactReasonSummary(payload)),
		}, dedupKeyBase+":reason="+sessionCompactReasonSummary(payload))
		rendered.DebugOnly = true
		return rendered
	case runtimechat.EventSessionCompactFailed:
		title := fmt.Sprintf("session compact failed mode=%s phase=%s reason=%s", mode, phase, sessionCompactReasonSummary(payload))
		if errText := truncateChatRuntimeText(payloadStringValue(payload["error"]), 160); errText != "" {
			title += " error=" + errText
		}
		return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
			Kind: cell.TimelinePlanning, Status: cell.StatusError, Tag: "[context]", Title: title,
		}, dedupKeyBase+":failed")
	default:
		return chatRuntimeTimelineEvent{}
	}
}

func sessionCompactBudgetSummary(payload map[string]interface{}) string {
	if payload == nil {
		return ""
	}
	tokenBefore := intPayloadValue(payload, "token_before")
	triggerLimit := firstPositivePayloadInt(payload, "trigger_token_limit", "prompt_budget")
	maxContext := firstPositivePayloadInt(payload, "max_context_tokens", "context_window_tokens", "model_capability_max_context_tokens", "provider_context_limit")
	model := sessionCompactModelSummary(payload)
	parts := make([]string, 0, 4)
	if tokenBefore > 0 {
		parts = append(parts, fmt.Sprintf("token_before=%d", tokenBefore))
	}
	if triggerLimit > 0 {
		parts = append(parts, fmt.Sprintf("trigger_token_limit=%d", triggerLimit))
	}
	if maxContext > 0 {
		parts = append(parts, fmt.Sprintf("max_context_tokens=%d", maxContext))
	}
	if model != "" {
		parts = append(parts, "target="+model)
	}
	return strings.Join(parts, " ")
}

func sessionCompactReasonSummary(payload map[string]interface{}) string {
	reason := strings.TrimSpace(payloadStringValue(payload["reason"]))
	switch reason {
	case "":
		return "unknown"
	case "below_limit":
		return "below_limit"
	case "missing_model_capability":
		return "missing_model_capability"
	case "history_empty":
		return "history_empty"
	case "pending_tool":
		return "pending_tool"
	case "pending_approval":
		return "pending_approval"
	case "pending_question":
		return "pending_question"
	case "resume_run":
		return "resume_run"
	default:
		return reason
	}
}

func sessionCompactModelSummary(payload map[string]interface{}) string {
	provider := strings.TrimSpace(payloadStringValue(payload["provider"]))
	model := strings.TrimSpace(payloadStringValue(payload["model"]))
	switch {
	case provider != "" && model != "":
		return provider + "/" + model
	case provider != "":
		return provider
	default:
		return model
	}
}

func isPromptPreflightSessionEndEvent(event runtimeevents.Event) bool {
	if strings.TrimSpace(event.Type) != runtimechat.EventSessionEnd {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(payloadStringValue(event.Payload["error_type"])), "prompt_preflight")
}

func renderPromptPreflightSessionEndTimelineEvent(event runtimeevents.Event) chatRuntimeTimelineEvent {
	if !isPromptPreflightSessionEndEvent(event) {
		return chatRuntimeTimelineEvent{}
	}
	payload := event.Payload
	promptTokens := intPayloadValue(payload, "prompt_tokens")
	promptBudget := intPayloadValue(payload, "prompt_budget")

	title := "本地拦截：请求在发送给模型前因上下文预算超限而终止"
	if promptTokens > 0 && promptBudget > 0 {
		title = fmt.Sprintf("本地拦截：prompt %d > budget %d", promptTokens, promptBudget)
	} else if promptTokens > 0 {
		title = fmt.Sprintf("本地拦截：prompt=%d", promptTokens)
	}

	details := make([]string, 0, 8)
	if reason := promptPreflightReasonSummary(payload); reason != "" {
		details = append(details, "  原因: "+reason)
	}
	if suggestion := truncateChatRuntimeText(payloadStringValue(payload["suggested_action"]), 160); suggestion != "" {
		details = append(details, "  建议: "+suggestion)
	}
	if model := promptPreflightModelSummary(payload); model != "" {
		details = append(details, "  模型: "+model)
	}
	if budget := promptPreflightBudgetSourceSummary(payload); budget != "" {
		details = append(details, "  预算: "+budget)
	}
	if activeTurn := promptPreflightActiveTurnSummary(payload); activeTurn != "" {
		details = append(details, "  active-turn: "+activeTurn)
	}
	if recovery := promptPreflightRecoverySummary(payload); recovery != "" {
		details = append(details, "  恢复: "+recovery)
	}
	if extras := runtimeContextSummaryLines(payload, false); len(extras) > 0 {
		details = append(details, extras...)
	}

	traceID := firstNonEmptyChatValue(strings.TrimSpace(event.TraceID), payloadStringValue(payload["trace_id"]))
	failureCode := firstNonEmptyChatValue(payloadStringValue(payload["failure_reason_code"]), "prompt_preflight")
	return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
		Kind: cell.TimelineNotice, Status: cell.StatusError, Tag: "[prompt preflight]", Title: title, Details: details,
	}, fmt.Sprintf("session_end.prompt_preflight:%s:%s:%s", strings.TrimSpace(event.SessionID), traceID, failureCode))
}

func promptPreflightReasonSummary(payload map[string]interface{}) string {
	switch strings.TrimSpace(payloadStringValue(payload["failure_reason_code"])) {
	case "active_turn_not_compactable":
		return "当前轮次里的 active-turn replay 已无法继续压缩"
	case "prompt_still_exceeds_budget_after_compaction":
		return "active-turn 已压缩，但 prompt 仍然超出预算"
	}
	if reason := truncateChatRuntimeText(payloadStringValue(payload["failure_reason"]), 160); reason != "" {
		return reason
	}
	return truncateChatRuntimeText(payloadStringValue(payload["failure_reason_detail"]), 160)
}

func promptPreflightModelSummary(payload map[string]interface{}) string {
	provider := strings.TrimSpace(payloadStringValue(payload["resolved_provider"]))
	model := strings.TrimSpace(payloadStringValue(payload["resolved_model"]))
	switch {
	case provider != "" && model != "":
		return provider + " / " + model
	case model != "":
		return model
	default:
		return provider
	}
}

func promptPreflightBudgetSourceSummary(payload map[string]interface{}) string {
	return budgetSourceSummary(
		payloadStringValue(payload["budget_source"]),
		payloadStringValue(payload["budget_source_detail"]),
	)
}

func promptPreflightActiveTurnSummary(payload map[string]interface{}) string {
	if payload == nil {
		return ""
	}
	messageCount := intPayloadValue(payload, "active_turn_message_count")
	replayBlockCount := intPayloadValue(payload, "latest_replay_block_message_count")
	compacted := payloadBoolValue(payload, "active_turn_compacted")
	if messageCount <= 0 && replayBlockCount <= 0 && !compacted {
		return ""
	}
	parts := make([]string, 0, 3)
	if messageCount > 0 {
		parts = append(parts, fmt.Sprintf("messages=%d", messageCount))
	}
	if replayBlockCount > 0 {
		parts = append(parts, fmt.Sprintf("latest_replay_block=%d", replayBlockCount))
	}
	if compacted {
		parts = append(parts, "compacted=true")
	} else if messageCount > 0 || replayBlockCount > 0 {
		parts = append(parts, "compacted=false")
	}
	return strings.Join(parts, ", ")
}

func promptPreflightRecoverySummary(payload map[string]interface{}) string {
	if payload == nil {
		return ""
	}
	available := payloadBoolValue(payload, "replacement_history_available")
	applied := payloadBoolValue(payload, "replacement_history_applied")
	messageCount := intPayloadValue(payload, "replacement_history_message_count")
	if !available && !applied && messageCount <= 0 {
		return ""
	}
	parts := make([]string, 0, 2)
	switch {
	case applied:
		parts = append(parts, "已自动保存压缩后的上下文，可直接继续下一轮")
	case available:
		parts = append(parts, "已生成压缩后的恢复上下文")
	}
	if messageCount > 0 {
		parts = append(parts, fmt.Sprintf("history_messages=%d", messageCount))
	}
	return strings.Join(parts, " | ")
}

func teamSummaryFallbackReasonSummary(reason string) string {
	switch strings.TrimSpace(reason) {
	case "sessions_not_configured":
		return "lead summary 会话不可用，改用任务列表回退总结"
	case "team_not_available":
		return "未能加载 team 记录，改用任务列表回退总结"
	case "lead_session_missing":
		return "lead session 缺失，改用任务列表回退总结"
	case "lead_session_error":
		return "lead summary 执行失败，改用任务列表回退总结"
	case "lead_output_empty":
		return "lead 未返回总结内容，改用任务列表回退总结"
	default:
		return truncateChatRuntimeText(strings.TrimSpace(reason), 160)
	}
}

func renderTeamSummaryTimelineEvent(event runtimeevents.Event, teamID string) chatRuntimeTimelineEvent {
	summary := truncateChatRuntimeText(payloadStringValue(event.Payload["summary"]), 200)
	title := firstNonEmptyChatValue(teamID, "?")
	details := make([]string, 0, 8)
	fallback := strings.EqualFold(strings.TrimSpace(payloadStringValue(event.Payload["summary_source"])), "fallback")
	status := cell.StatusSuccess
	if fallback {
		title += " [fallback]"
		status = cell.StatusInfo
		if strings.EqualFold(strings.TrimSpace(payloadStringValue(event.Payload["error_type"])), "prompt_preflight") {
			title += " [prompt preflight]"
			status = cell.StatusError
		}
	}
	if summary != "" {
		title += " " + summary
	}
	if fallback {
		if strings.EqualFold(strings.TrimSpace(payloadStringValue(event.Payload["error_type"])), "prompt_preflight") {
			if reason := promptPreflightReasonSummary(event.Payload); reason != "" {
				details = append(details, "  原因: "+reason)
			}
			if model := promptPreflightModelSummary(event.Payload); model != "" {
				details = append(details, "  模型: "+model)
			}
			if budget := promptPreflightBudgetSourceSummary(event.Payload); budget != "" {
				details = append(details, "  预算: "+budget)
			}
			if recovery := promptPreflightRecoverySummary(event.Payload); recovery != "" {
				details = append(details, "  恢复: "+recovery)
			}
			if extras := runtimeContextSummaryLines(event.Payload, false); len(extras) > 0 {
				details = append(details, extras...)
			}
		} else if reason := teamSummaryFallbackReasonSummary(payloadStringValue(event.Payload["fallback_reason"])); reason != "" {
			details = append(details, "  fallback: "+reason)
		}
	}
	return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
		Kind: cell.TimelineTeam, Status: status, Tag: "[team summary]", Title: title, Details: details,
	}, fmt.Sprintf("team.summary:%s", teamID))
}

func extractPrefixedRuntimePayload(payload map[string]interface{}, prefix string) map[string]interface{} {
	if len(payload) == 0 {
		return nil
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil
	}
	normalizedPrefix := prefix + "_"
	out := map[string]interface{}{}
	for key, value := range payload {
		key = strings.TrimSpace(key)
		if !strings.HasPrefix(key, normalizedPrefix) {
			continue
		}
		trimmed := strings.TrimPrefix(key, normalizedPrefix)
		if trimmed == "" {
			continue
		}
		out[trimmed] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func chatRuntimeTaskAction(eventType string) string {
	switch strings.TrimSpace(eventType) {
	case "task.started":
		return "started"
	case "task.completed", "team.task.completed":
		return "completed"
	case "task.failed", "team.task.failed":
		return "failed"
	case "task.blocked", "team.task.blocked":
		return "blocked"
	default:
		return strings.TrimSpace(eventType)
	}
}

func truncateChatRuntimeText(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if limit <= 0 || len([]rune(text)) <= limit {
		return text
	}
	runes := []rune(text)
	return strings.TrimSpace(string(runes[:limit])) + "..."
}

func intPayloadValue(payload map[string]interface{}, key string) int {
	if payload == nil {
		return 0
	}
	switch value := payload[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func firstPositivePayloadInt(payload map[string]interface{}, keys ...string) int {
	if payload == nil {
		return 0
	}
	for _, key := range keys {
		if value := intPayloadValue(payload, key); value > 0 {
			return value
		}
	}
	return 0
}

func payloadFloatValue(payload map[string]interface{}, key string) (float64, bool) {
	if payload == nil {
		return 0, false
	}
	switch value := payload[key].(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case json.Number:
		parsed, err := value.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func sanitizeInteractiveAsyncTeamLaunchResponse(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return content
	}
	if !containsAnyChatMarker(trimmed,
		"后台开始工作",
		"后台开始执行",
		"我会在他们完成后",
		"完成后为你汇总",
		"完成后自动总结",
	) {
		return content
	}
	if !containsAnyChatMarker(trimmed,
		"如果你愿意",
		"你要我继续哪一种",
		"下一步可以继续",
	) {
		return content
	}

	lines := strings.Split(trimmed, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		current := strings.TrimSpace(line)
		if current == "" && len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
			continue
		}
		if containsAnyChatMarker(current,
			"如果你愿意",
			"你要我继续哪一种",
			"下一步可以继续",
		) {
			break
		}
		if len(kept) > 0 && isChatOptionLine(current) {
			break
		}
		kept = append(kept, line)
	}

	sanitized := strings.TrimSpace(strings.Join(kept, "\n"))
	if sanitized == "" {
		sanitized = trimmed
	}
	if !containsAnyChatMarker(sanitized, "自动总结", "自动给你总结", "完成后为你汇总", "完成后自动给你总结") {
		sanitized += "\n\n我会继续跟踪团队进展，并在完成后自动给你总结结果。"
	}
	return sanitized
}

func containsAnyChatMarker(text string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func isChatOptionLine(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	for _, prefix := range []string{"1.", "1..", "2.", "2..", "3.", "3..", "•"} {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

// approvalGrantFamily returns a family key for approval reuse. Tools in the
// same family share a single approval grant, so that once a user approves one
// call, subsequent calls of the same family are auto-approved within the
// configured scope (session or team).
//
// The family is derived from the tool's capabilities rather than a hardcoded
// name list, so that new tools are automatically covered as long as the
// capability resolver can classify them.
//
// Current families:
//   - "readonly_shell":   shell-like tools (shell, bash, execute_shell_command, …)
//     whose command is clearly read-only (whitelist match).
//   - "approved_shell":   shell-like tools whose command is not in the
//     read-only whitelist but also not in the dangerous
//     blacklist. The first call still requires manual
//     approval, but subsequent calls are auto-approved.
//   - "readonly_network": read-only tools that require network access
//     (web_search, sourcegraph, fetch, …).
func approvalGrantFamily(toolName string, argsJSON json.RawMessage) string {
	normalized := strings.ToLower(strings.TrimSpace(toolName))
	if normalized == "" {
		return ""
	}

	// Shell-like tools need special handling: we must inspect the actual
	// command to determine the risk level.
	if runtimepolicy.IsShellLikeToolName(normalized) {
		if isReadOnlyShellApprovalArgs(argsJSON) {
			return "readonly_shell"
		}
		if isApprovedShellApprovalArgs(argsJSON) {
			return "approved_shell"
		}
		return "" // dangerous shell command → no reuse
	}

	// Write-like tools never qualify for automatic approval reuse.
	if runtimepolicy.IsWriteLikeToolName(normalized) {
		return ""
	}

	// For all other tools, derive the family from capabilities.
	caps := runtimepolicy.DefaultCapabilityResolver{}.Resolve(
		runtimepolicy.EvalRequest{ToolName: normalized},
	)
	hasNetwork := false
	for _, cap := range caps {
		switch cap {
		case runtimepolicy.CapWriteFS, runtimepolicy.CapExecShell,
			runtimepolicy.CapExternalSideEffect, runtimepolicy.CapBackgroundTask:
			// Capabilities that imply mutation or side effects disqualify
			// the tool from automatic approval reuse.
			return ""
		case runtimepolicy.CapNetwork:
			hasNetwork = true
		}
	}

	if hasNetwork {
		return "readonly_network"
	}

	// Pure read-only tools without network access (e.g. view, grep, glob)
	// don't require approval in default mode, so no family is needed.
	return ""
}

func isReadOnlyShellApprovalArgs(argsJSON json.RawMessage) bool {
	if len(argsJSON) == 0 {
		return false
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(argsJSON, &payload); err != nil {
		return false
	}
	if mutated := extractApprovalStringSlice(payload["mutated_paths"]); len(mutated) > 0 {
		return false
	}
	command := payloadStringValue(payload["command"])
	if command == "" {
		return false
	}
	return isReadOnlyShellCommand(command)
}

// isApprovedShellApprovalArgs returns true for shell commands that are not
// clearly dangerous (not in the blacklist) but also not in the read-only
// whitelist. Such commands still require manual approval the first time, but
// once approved the grant is cached so subsequent calls are auto-approved.
func isApprovedShellApprovalArgs(argsJSON json.RawMessage) bool {
	if len(argsJSON) == 0 {
		return false
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(argsJSON, &payload); err != nil {
		return false
	}
	if mutated := extractApprovalStringSlice(payload["mutated_paths"]); len(mutated) > 0 {
		return false
	}
	command := payloadStringValue(payload["command"])
	if command == "" {
		return false
	}
	return !isDangerousShellCommand(command)
}

func extractApprovalStringSlice(value interface{}) []string {
	items, ok := value.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text := strings.TrimSpace(fmt.Sprintf("%v", item))
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

func isReadOnlyShellCommand(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	if isDangerousShellCommand(command) {
		return false
	}

	lower := strings.ToLower(command)
	// Split on &&, ||, and ; to check each segment independently.
	// Each segment (after splitting on | for pipes) must be a read-only command.
	segments := splitShellChainSegments(lower)
	for _, segment := range segments {
		if !isReadOnlyShellSegment(segment) {
			return false
		}
	}
	return true
}

// isDangerousShellCommand returns true for commands that are clearly
// destructive or write-like. These commands are never eligible for
// automatic approval reuse.
func isDangerousShellCommand(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return true
	}
	lower := strings.ToLower(command)

	// Block clearly dangerous write operations, regardless of &&/||/; structure.
	for _, marker := range []string{">>", "out-file", "set-content", "add-content", "copy-item", "move-item", "remove-item", "new-item", "rename-item", "invoke-webrequest", "curl ", "wget ", " start-process", "taskkill", " sed -i", " perl -pi", "git apply", "git commit", "git push"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	// Redirect operators are always write-like.
	for _, marker := range []string{">", "<"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	// Broad destructive keywords — dangerous regardless of chaining.
	// Match both in the middle (e.g. "&& rm …") and at the start (e.g. "rm …").
	for _, marker := range []string{"rm ", "del ", "move ", "copy ", "mkdir ", "rmdir "} {
		if strings.HasPrefix(lower, marker) || strings.Contains(lower, " "+marker) {
			return true
		}
	}
	return false
}

// splitShellChainSegments splits a command string on chain operators &&, ||, and ;.
// It handles the case where these appear inside quoted strings incorrectly by doing
// a simple split — this is acceptable because the read-only whitelist is conservative.
func splitShellChainSegments(command string) []string {
	var segments []string
	current := ""
	i := 0
	for i < len(command) {
		if i+1 < len(command) && command[i] == '&' && command[i+1] == '&' {
			if trimmed := strings.TrimSpace(current); trimmed != "" {
				segments = append(segments, trimmed)
			}
			current = ""
			i += 2
			continue
		}
		if i+1 < len(command) && command[i] == '|' && command[i+1] == '|' {
			if trimmed := strings.TrimSpace(current); trimmed != "" {
				segments = append(segments, trimmed)
			}
			current = ""
			i += 2
			continue
		}
		if command[i] == ';' {
			if trimmed := strings.TrimSpace(current); trimmed != "" {
				segments = append(segments, trimmed)
			}
			current = ""
			i++
			continue
		}
		current += string(command[i])
		i++
	}
	if trimmed := strings.TrimSpace(current); trimmed != "" {
		segments = append(segments, trimmed)
	}
	return segments
}

// isReadOnlyShellSegment checks whether a single chain segment (no &&, ||, ;)
// is read-only. A segment may still contain pipes (|), each pipe stage is checked.
func isReadOnlyShellSegment(segment string) bool {
	pipeStages := strings.Split(segment, "|")
	for _, stage := range pipeStages {
		stage = strings.TrimSpace(stage)
		if stage == "" {
			return false
		}
		fields := strings.Fields(stage)
		if len(fields) == 0 {
			return false
		}
		cmd := normalizeApprovalCommandName(fields[0])
		switch cmd {
		case "ls", "dir", "pwd", "cat", "type", "find", "findstr", "grep", "rg", "tree", "stat", "head", "tail", "wc",
			"get-childitem", "gci", "get-content", "gc", "select-string", "sls", "where-object", "sort-object", "measure-object",
			"format-table", "ft", "format-list", "fl", "resolve-path", "test-path", "cd", "chdir", "pushd", "popd", "echo", "printf",
			// Common always-read-only commands
			"which", "where", "command", "env", "printenv", "whoami", "hostname", "uname":
			continue
		case "git":
			if len(fields) < 2 {
				return false
			}
			switch strings.TrimSpace(fields[1]) {
			case "status", "diff", "log", "show", "branch", "stash", "remote", "config", "tag", "blame", "describe", "rev-parse", "ls-files", "ls-tree":
				continue
			default:
				return false
			}
		default:
			return false
		}
	}
	return true
}

func normalizeApprovalCommandName(command string) string {
	command = strings.TrimSpace(strings.Trim(command, `"'`))
	command = strings.TrimSuffix(command, ".exe")
	return strings.ToLower(command)
}

func approvalRequestPreviewLines(approval *runtimechat.ApprovalRequest) []string {
	if approval == nil || len(approval.ArgsJSON) == 0 {
		return nil
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(approval.ArgsJSON, &payload); err != nil {
		args := truncateChatRuntimeText(strings.TrimSpace(string(approval.ArgsJSON)), 200)
		if args == "" {
			return nil
		}
		return []string{"args=" + args}
	}
	lines := make([]string, 0, 3)
	if command := truncateChatRuntimeText(payloadStringValue(payload["command"]), 200); command != "" {
		lines = append(lines, "command="+command)
	}
	if workdir := truncateChatRuntimeText(payloadStringValue(payload["workdir"]), 120); workdir != "" {
		lines = append(lines, "workdir="+workdir)
	}
	if cwd := truncateChatRuntimeText(payloadStringValue(payload["cwd"]), 120); cwd != "" {
		lines = append(lines, "cwd="+cwd)
	}
	if len(lines) > 0 {
		return lines
	}
	for _, key := range []string{"url", "path", "query", "prompt"} {
		if value := truncateChatRuntimeText(payloadStringValue(payload[key]), 160); value != "" {
			lines = append(lines, key+"="+value)
			return lines
		}
	}
	args := truncateChatRuntimeText(strings.TrimSpace(string(approval.ArgsJSON)), 200)
	if args != "" {
		lines = append(lines, "args="+args)
	}
	return lines
}

func interfaceSliceToStrings(value interface{}) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	default:
		return nil
	}
}

func chatToolArgPreview(payload map[string]interface{}) string {
	if payload == nil {
		return ""
	}
	return truncateChatRuntimeText(payloadStringValue(payload["arg_preview"]), 72)
}

func appendCompactToolDirectory(line string, payload map[string]interface{}) string {
	line = strings.TrimRight(line, "\n")
	if line == "" || payload == nil {
		return line
	}
	extras := compactToolContextLines(payload)
	if len(extras) == 0 {
		return line
	}
	return line + "\n" + strings.Join(extras, "\n")
}

func compactToolContextLines(payload map[string]interface{}) []string {
	if payload == nil {
		return nil
	}
	extras := make([]string, 0, 5)
	if filePath := compactToolDisplaySegment(payloadStringValue(payload["display_file_path"])); filePath != "" {
		extras = append(extras, "  file_path: "+filePath)
	}
	if workdir := truncateChatRuntimeText(payloadStringValue(payload["workdir"]), 160); workdir != "" {
		extras = append(extras, "  workdir: "+workdir)
	} else if cwd := truncateChatRuntimeText(payloadStringValue(payload["cwd"]), 160); cwd != "" {
		extras = append(extras, "  cwd: "+cwd)
	}
	if shell := truncateChatRuntimeText(chatToolShellDisplay(payload), 180); shell != "" {
		extras = append(extras, "  shell: "+shell)
	}
	if code := strings.TrimSpace(payloadStringValue(payload["error_code"])); code != "" {
		diagnostic := code
		if _, ok := payload["retryable"]; ok {
			diagnostic += fmt.Sprintf(" (retryable=%t)", payloadBoolValue(payload, "retryable"))
		}
		extras = append(extras, "  diagnostic: "+diagnostic)
	}
	if action := truncateChatRuntimeText(payloadStringValue(payload["next_action"]), 220); action != "" {
		extras = append(extras, "  action: "+action)
	}
	if len(extras) == 0 {
		return nil
	}
	return extras
}

func chatToolShellDisplay(payload map[string]interface{}) string {
	if payload == nil {
		return ""
	}
	if display := strings.TrimSpace(payloadStringValue(payload["shell_display"])); display != "" {
		return display
	}
	shellType := strings.TrimSpace(payloadStringValue(payload["shell_type"]))
	shellPath := strings.TrimSpace(payloadStringValue(payload["shell_path"]))
	switch {
	case shellType != "" && shellPath != "":
		return shellType + " (" + shellPath + ")"
	case shellType != "":
		return shellType
	case shellPath != "":
		return shellPath
	default:
		return ""
	}
}

func chatToolDivider(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return strings.Repeat("─", 72)
	}
	content := " " + label + " "
	width := 72
	runeCount := len([]rune(content))
	if runeCount >= width {
		return content
	}
	left := (width - runeCount) / 2
	right := width - runeCount - left
	return strings.Repeat("─", left) + content + strings.Repeat("─", right)
}

func chatToolSummaryLines(payload map[string]interface{}) []string {
	if payload == nil {
		return nil
	}
	errText := payloadStringValue(payload["error"])
	maxLines := chatToolSummaryLineLimit(payload)
	if maxLines <= 0 {
		maxLines = 3
	}

	lines := interfaceSliceToStrings(payload["summary_lines"])
	if len(lines) == 0 {
		summary := payloadStringValue(payload["summary"])
		if summary != "" {
			lines = strings.Split(strings.ReplaceAll(summary, "\r\n", "\n"), "\n")
		}
	}

	maxChars := chatToolSummaryTextLimit(payload)
	out := make([]string, 0, maxLines)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		out = append(out, truncateChatRuntimeText(trimmed, maxChars))
		if len(out) == maxLines {
			return out
		}
	}
	if len(out) > 0 {
		if errText != "" && isGenericChatToolFailureSummary(out) {
			return []string{truncateChatRuntimeText("failed: "+errText, maxChars)}
		}
		return out
	}

	if errText != "" {
		return []string{truncateChatRuntimeText("failed: "+errText, maxChars)}
	}
	return nil
}

func chatToolSummaryLineLimit(payload map[string]interface{}) int {
	if strings.EqualFold(strings.TrimSpace(payloadStringValue(payload["tool_name"])), "todos") {
		return 32
	}
	switch toolresult.NormalizeSource(payloadStringValue(payload[toolresult.SourceKey])) {
	case toolresult.SourceMeta, toolresult.SourceMCP, toolresult.SourceBroker:
		return 2
	default:
		return 3
	}
}

func chatToolSummaryTextLimit(payload map[string]interface{}) int {
	if strings.EqualFold(strings.TrimSpace(payloadStringValue(payload["tool_name"])), "todos") {
		return 240
	}
	return 120
}

func chatLLMRequestToolAvailabilityHint(payload map[string]interface{}) string {
	if payload == nil {
		return ""
	}
	raw, ok := payload["tool_availability"].(map[string]interface{})
	if !ok || len(raw) == 0 {
		return ""
	}
	names := interfaceSliceToStrings(raw["requires_active_team_run"])
	if len(names) == 0 {
		return ""
	}
	preview := names
	extraCount := 0
	if len(preview) > 4 {
		extraCount = len(preview) - 4
		preview = preview[:4]
	}
	line := fmt.Sprintf("[tools] %d team-run tool(s) require spawn_team first", len(names))
	if len(preview) > 0 {
		line += ": " + strings.Join(preview, ", ")
		if extraCount > 0 {
			line += fmt.Sprintf(", +%d more", extraCount)
		}
	}
	return truncateChatRuntimeText(line, 200)
}

func formatRuntimeLLMRequestDebugInfo(event runtimeevents.Event) string {
	switch event.Type {
	case runtimechat.EventLLMRequestStarted, "llm.request.started":
		return formatRuntimeLLMRequestStartedDebugInfo(event)
	case runtimechat.EventLLMRequestFinished, "llm.request.finished":
		return formatRuntimeLLMRequestFinishedDebugInfo(event)
	default:
		return ""
	}
}

func formatRuntimeLLMRequestStartedDebugInfo(event runtimeevents.Event) string {
	if len(event.Payload) == 0 {
		return ""
	}
	parts := make([]string, 0, 8)
	if traceID := strings.TrimSpace(firstNonEmptyChatValue(event.TraceID, payloadStringValue(event.Payload["trace_id"]))); traceID != "" {
		parts = append(parts, "trace_id="+traceID)
	}
	if step := strings.TrimSpace(payloadStringValue(event.Payload["step"])); step != "" {
		parts = append(parts, "step="+step)
	}
	if summary := firstNonEmptyChatValue(
		strings.TrimSpace(payloadStringValue(event.Payload["prompt_layout_summary"])),
		strings.TrimSpace(payloadStringValue(event.Payload["prompt_layout"])),
	); summary != "" {
		parts = append(parts, "prompt_layout_summary="+truncateChatRuntimeText(summary, 200))
	}
	if instructionTokens := intPayloadValue(event.Payload, "instruction_tokens"); instructionTokens > 0 {
		parts = append(parts, fmt.Sprintf("prompt_layout_instruction_tokens=%d", instructionTokens))
	}
	if totalTokens := intPayloadValue(event.Payload, "total_tokens"); totalTokens > 0 {
		parts = append(parts, fmt.Sprintf("prompt_layout_total_tokens=%d", totalTokens))
	}
	if layoutLength := intPayloadValue(event.Payload, "prompt_layout_length"); layoutLength > 0 {
		parts = append(parts, fmt.Sprintf("prompt_layout_length=%d", layoutLength))
	}
	if totalChars := intPayloadValue(event.Payload, "total_message_chars"); totalChars > 0 {
		parts = append(parts, fmt.Sprintf("total_message_chars=%d", totalChars))
	}
	if budget := intPayloadValue(event.Payload, "prompt_budget"); budget > 0 {
		parts = append(parts, fmt.Sprintf("prompt_budget=%d", budget))
	}
	if window := intPayloadValue(event.Payload, "context_window_tokens"); window > 0 {
		parts = append(parts, fmt.Sprintf("context_window_tokens=%d", window))
	}
	if source := strings.TrimSpace(payloadStringValue(event.Payload["budget_source"])); source != "" {
		parts = append(parts, "budget_source="+truncateChatRuntimeText(source, 80))
	}
	if fingerprint := strings.TrimSpace(payloadStringValue(event.Payload["tool_surface_fingerprint"])); fingerprint != "" {
		parts = append(parts, "tool_surface_fingerprint="+truncateChatRuntimeText(fingerprint, 80))
	}
	if len(parts) == 0 {
		return ""
	}
	return "[llm-debug] request_started " + strings.Join(parts, " ")
}

func formatRuntimeLLMRequestFinishedDebugInfo(event runtimeevents.Event) string {
	if len(event.Payload) == 0 {
		return ""
	}
	parts := make([]string, 0, 8)
	if traceID := strings.TrimSpace(firstNonEmptyChatValue(event.TraceID, payloadStringValue(event.Payload["trace_id"]))); traceID != "" {
		parts = append(parts, "trace_id="+traceID)
	}
	if step := strings.TrimSpace(payloadStringValue(event.Payload["step"])); step != "" {
		parts = append(parts, "step="+step)
	}
	if _, ok := event.Payload["success"]; ok {
		parts = append(parts, fmt.Sprintf("success=%t", payloadBoolValue(event.Payload, "success")))
	}
	if promptTokens := intPayloadValue(event.Payload, "usage_prompt_tokens"); promptTokens > 0 {
		parts = append(parts, fmt.Sprintf("usage_prompt_tokens=%d", promptTokens))
	}
	if completionTokens := intPayloadValue(event.Payload, "usage_completion_tokens"); completionTokens > 0 {
		parts = append(parts, fmt.Sprintf("usage_completion_tokens=%d", completionTokens))
	}
	if totalTokens := intPayloadValue(event.Payload, "usage_total_tokens"); totalTokens > 0 {
		parts = append(parts, fmt.Sprintf("usage_total_tokens=%d", totalTokens))
	}
	if cachedTokens := intPayloadValue(event.Payload, "usage_cached_tokens"); cachedTokens > 0 {
		parts = append(parts, fmt.Sprintf("usage_cached_tokens=%d", cachedTokens))
	}
	if cacheReadTokens := intPayloadValue(event.Payload, "usage_cache_read_tokens"); cacheReadTokens > 0 {
		parts = append(parts, fmt.Sprintf("usage_cache_read_tokens=%d", cacheReadTokens))
	}
	if cacheCreationTokens := intPayloadValue(event.Payload, "usage_cache_creation_tokens"); cacheCreationTokens > 0 {
		parts = append(parts, fmt.Sprintf("usage_cache_creation_tokens=%d", cacheCreationTokens))
	}
	if _, ok := event.Payload["usage_cache_read_reported"]; ok {
		parts = append(parts, fmt.Sprintf("usage_cache_read_reported=%t", payloadBoolValue(event.Payload, "usage_cache_read_reported")))
	}
	if ratio, ok := payloadFloatValue(event.Payload, "usage_cache_hit_ratio"); ok {
		parts = append(parts, fmt.Sprintf("usage_cache_hit_ratio=%.4f", ratio))
	} else if promptTokens := intPayloadValue(event.Payload, "usage_prompt_tokens"); promptTokens > 0 {
		if cachedTokens := firstPositivePayloadInt(event.Payload, "usage_cache_read_tokens", "usage_cached_tokens"); cachedTokens > 0 {
			parts = append(parts, fmt.Sprintf("usage_cache_hit_ratio=%.4f", float64(cachedTokens)/float64(promptTokens)))
		}
	}
	if status := strings.TrimSpace(payloadStringValue(event.Payload["usage_cache_status"])); status != "" {
		parts = append(parts, "usage_cache_status="+truncateChatRuntimeText(status, 80))
	}
	if reasoningTokens := intPayloadValue(event.Payload, "usage_reasoning_tokens"); reasoningTokens > 0 {
		parts = append(parts, fmt.Sprintf("usage_reasoning_tokens=%d", reasoningTokens))
	}
	if source := strings.TrimSpace(payloadStringValue(event.Payload["usage_source"])); source != "" {
		parts = append(parts, "usage_source="+truncateChatRuntimeText(source, 80))
	}
	if errText := strings.TrimSpace(payloadStringValue(event.Payload["error"])); errText != "" {
		parts = append(parts, "error="+strconv.Quote(truncateChatRuntimeText(errText, 240)))
	}
	if len(parts) == 0 {
		return ""
	}
	return "[llm-debug] request_finished " + strings.Join(parts, " ")
}

// renderToolProgressTimelineEvent formats live tool.progress notifications.
// Progress is high-frequency and live-only; keep the line compact.
func renderToolProgressTimelineEvent(event runtimeevents.Event) chatRuntimeTimelineEvent {
	toolName := firstNonEmptyChatValue(runtimeEventToolName(event), "tool")
	message := truncateChatRuntimeText(payloadStringValue(event.Payload["message"]), 120)
	partialRaw := payloadStringValue(event.Payload["partial"])
	partial := truncateChatRuntimeText(partialRaw, 80)
	percent, hasPercent := payloadFloatValue(event.Payload, "percent")
	isStream := payloadBoolValue(event.Payload, "stream")
	if isStream && payloadBoolValue(event.Payload, toolprotocol.MetadataOutputMirrored) {
		// The local output mirror already rendered these bytes. Keep the event for
		// ACP/SSE consumers and stage updates, but avoid a duplicate chat line.
		return chatRuntimeTimelineEvent{}
	}
	channel := payloadStringValue(event.Payload["stream_channel"])
	phase := payloadStringValue(event.Payload["phase"])
	chunkIndex, hasChunk := payloadFloatValue(event.Payload, "stream_chunk_index")

	label := "Progress"
	if isStream {
		label = "Stream"
	} else if phase == "start" {
		label = "Start"
	} else if phase == "finish" {
		label = "Finish"
	}
	parts := []string{label, toolName}
	if isStream && channel != "" && channel != "combined" {
		parts = append(parts, channel)
	}
	if hasPercent && percent > 0 {
		if percent == float64(int(percent)) {
			parts = append(parts, fmt.Sprintf("%d%%", int(percent)))
		} else {
			parts = append(parts, fmt.Sprintf("%.1f%%", percent))
		}
	}
	if message != "" && !isStream {
		parts = append(parts, message)
	} else if message != "" && isStream && partial == "" {
		parts = append(parts, message)
	}
	if partial != "" {
		// Stream chunks are the primary content; keep them readable.
		if isStream {
			parts = append(parts, strings.TrimRight(partial, "\r\n"))
		} else {
			parts = append(parts, "("+partial+")")
		}
	}
	if len(parts) == 2 && message == "" && partial == "" && !(hasPercent && percent > 0) {
		// Bare tool name with no details is noise; skip.
		return chatRuntimeTimelineEvent{}
	}
	callID := payloadStringValue(event.Payload["tool_call_id"])
	dedup := firstNonEmptyChatValue(callID, toolName) + ":" + message + ":" + partial
	if isStream {
		// Prefer chunk index so successive stream lines are not over-deduped.
		if hasChunk {
			dedup = firstNonEmptyChatValue(callID, toolName) + ":stream:" + fmt.Sprintf("%.0f", chunkIndex)
		} else {
			dedup = firstNonEmptyChatValue(callID, toolName) + ":stream:" + partial
		}
	} else if hasPercent && percent > 0 {
		dedup += fmt.Sprintf(":%.0f", percent)
	} else if phase != "" {
		dedup += ":" + phase
	}
	status := cell.StatusRunning
	if phase == "finish" {
		status = cell.StatusSuccess
	}
	return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
		Kind:               cell.TimelineProgress,
		Status:             status,
		Marker:             "• ",
		SuppressKindPrefix: true,
		Title:              strings.Join(parts, " "),
	}, "tool.progress:"+dedup)
}

// chatToolProgressStageDetail builds a short composer stage label from progress.
func chatToolProgressStageDetail(event runtimeevents.Event) string {
	toolName := runtimeEventToolName(event)
	if toolName == "" {
		return ""
	}
	if payloadBoolValue(event.Payload, "stream") {
		partial := truncateChatRuntimeText(payloadStringValue(event.Payload["partial"]), 40)
		if partial != "" {
			return toolName + " " + strings.TrimRight(partial, "\r\n")
		}
		if channel := payloadStringValue(event.Payload["stream_channel"]); channel != "" {
			return toolName + " stream:" + channel
		}
		return toolName + " stream"
	}
	message := truncateChatRuntimeText(payloadStringValue(event.Payload["message"]), 48)
	percent, hasPercent := payloadFloatValue(event.Payload, "percent")
	if hasPercent && percent > 0 {
		if message != "" {
			return fmt.Sprintf("%s %d%% %s", toolName, int(percent), message)
		}
		return fmt.Sprintf("%s %d%%", toolName, int(percent))
	}
	if message != "" {
		return toolName + " " + message
	}
	if phase := payloadStringValue(event.Payload["phase"]); phase != "" {
		return toolName + " " + phase
	}
	return toolName
}

func renderSubagentCompletedTimelineEvent(event runtimeevents.Event) cell.TimelineEvent {
	payload := event.Payload
	agentID := firstNonEmptyChatValue(payloadStringValue(payload["agent_id"]), payloadStringValue(payload["role"]), strings.TrimSpace(event.SessionID))
	parts := []string{fmt.Sprintf("completed %s", agentID)}
	if difficulty := strings.TrimSpace(payloadStringValue(payload["difficulty"])); difficulty != "" {
		parts = append(parts, "difficulty="+difficulty)
	}
	if provider := strings.TrimSpace(payloadStringValue(payload["route_provider"])); provider != "" {
		parts = append(parts, "provider="+provider)
	}
	if model := strings.TrimSpace(payloadStringValue(payload["route_model"])); model != "" {
		parts = append(parts, "model="+model)
	}
	if effort := strings.TrimSpace(payloadStringValue(payload["route_reasoning_effort"])); effort != "" {
		parts = append(parts, "reasoning="+effort)
	}
	if permissionMode := strings.TrimSpace(payloadStringValue(payload["permission_mode"])); permissionMode != "" {
		parts = append(parts, "permission_mode="+permissionMode)
	}
	if source := strings.TrimSpace(payloadStringValue(payload["route_source"])); source != "" {
		parts = append(parts, "route_source="+source)
	}
	if totalTokens := intPayloadValue(payload, "usage_total_tokens"); totalTokens > 0 {
		parts = append(parts, fmt.Sprintf("usage_total_tokens=%d", totalTokens))
	}
	if warnings := stringSliceValueAny(payload["route_warnings"]); len(warnings) > 0 {
		parts = append(parts, "warnings="+strings.Join(warnings, ","))
	}
	return cell.TimelineEvent{
		Kind: cell.TimelineTask, Status: cell.StatusSuccess, Tag: "[subagent]", Title: strings.Join(parts, " "),
	}
}

func renderTaskRouteResolvedTimelineEvent(event runtimeevents.Event, teamID string) chatRuntimeTimelineEvent {
	payload := event.Payload
	taskID := firstNonEmptyChatValue(payloadStringValue(payload["task_id"]), "?")
	assignee := firstNonEmptyChatValue(payloadStringValue(payload["assignee"]), payloadStringValue(payload["agent_id"]))
	parts := []string{fmt.Sprintf("resolved %s", taskID)}
	if assignee != "" {
		parts = append(parts, "@"+assignee)
	}
	if difficulty := strings.TrimSpace(payloadStringValue(payload["difficulty"])); difficulty != "" {
		parts = append(parts, "difficulty="+difficulty)
	}
	if provider := strings.TrimSpace(payloadStringValue(payload["route_provider"])); provider != "" {
		parts = append(parts, "provider="+provider)
	}
	if model := strings.TrimSpace(payloadStringValue(payload["route_model"])); model != "" {
		parts = append(parts, "model="+model)
	}
	if effort := strings.TrimSpace(payloadStringValue(payload["route_reasoning_effort"])); effort != "" {
		parts = append(parts, "reasoning="+effort)
	}
	if source := strings.TrimSpace(payloadStringValue(payload["route_source"])); source != "" {
		parts = append(parts, "route_source="+source)
	}
	attempt := intPayloadValue(payload, "route_attempt")
	if attempt > 0 {
		parts = append(parts, fmt.Sprintf("attempt=%d", attempt))
	}
	if payloadBoolValue(payload, "disabled") {
		parts = append(parts, "disabled=true")
	}
	if payloadBoolValue(payload, "strict") {
		parts = append(parts, "strict=true")
	}
	if payloadBoolValue(payload, "fallback_used") || payloadBoolValue(payload, "fallback") {
		parts = append(parts, "fallback=true")
	}
	if reason := strings.TrimSpace(payloadStringValue(payload["fallback_reason"])); reason != "" {
		parts = append(parts, "fallback_reason="+truncateChatRuntimeText(reason, 80))
	}
	if warnings := stringSliceValueAny(payload["route_warnings"]); len(warnings) > 0 {
		parts = append(parts, "warnings="+strings.Join(warnings, ","))
	}
	eventStatus := cell.StatusSuccess
	if routeError := strings.TrimSpace(payloadStringValue(payload["route_error"])); routeError != "" {
		parts = append(parts, "error="+truncateChatRuntimeText(routeError, 120))
		eventStatus = cell.StatusError
	}
	return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
		Kind: cell.TimelineTask, Status: eventStatus, Tag: "[task route]", Title: strings.Join(parts, " "),
	}, fmt.Sprintf("%s:%s:%s:%d", team.TaskRouteResolvedEvent, teamID, taskID, attempt))
}

func renderApprovalRequestedTimelineEvent(event runtimeevents.Event) chatRuntimeTimelineEvent {
	payload := event.Payload
	toolName := firstNonEmptyChatValue(payloadStringValue(payload["tool_name"]), "tool")
	details := make([]string, 0, 4)
	for _, line := range approvalRequestContextLines(payload) {
		if line = strings.TrimSpace(line); line != "" {
			details = append(details, "  "+line)
		}
	}
	return typedChatRuntimeTimelineEvent(cell.TimelineEvent{
		Kind:    cell.TimelineApproval,
		Status:  cell.StatusPending,
		Title:   toolName,
		Details: details,
	}, "")
}

func chatToolPostCommandHint(payload map[string]interface{}) string {
	return ""
}

func prefixExecutionBullet(line string) string {
	line = strings.TrimRight(line, "\n")
	if strings.TrimSpace(line) == "" {
		return ""
	}
	if strings.HasPrefix(strings.TrimSpace(line), "• ") {
		return line
	}
	return "• " + line
}

func renderChatReasoningTimelineEvent(event runtimeevents.Event) chatRuntimeTimelineEvent {
	block := runtimetypes.ReasoningBlockFromMap(event.Payload["reasoning"])
	if block == nil {
		return chatRuntimeTimelineEvent{}
	}
	stepLabel := payloadStringValue(event.Payload["step"])
	if stepLabel == "" {
		if stepValue, ok := event.Payload["step"].(int); ok && stepValue > 0 {
			stepLabel = fmt.Sprintf("%d", stepValue)
		}
	}
	return chatReasoningTimelineEvent(strings.TrimSpace(event.TraceID), stepLabel, block)
}

func chatReasoningTimelineEvent(traceID, stepLabel string, block *runtimetypes.ReasoningBlock) chatRuntimeTimelineEvent {
	if block == nil {
		return chatRuntimeTimelineEvent{}
	}
	lines := chatReasoningLines(block)
	if len(lines) == 0 {
		return chatRuntimeTimelineEvent{}
	}
	stepLabel = strings.TrimSpace(stepLabel)
	keyParts := []string{"assistant.reasoning", strings.TrimSpace(traceID), stepLabel, strings.TrimSpace(block.DisplayText())}
	return documentedChatRuntimeTimelineEvent(chatReasoningTimelineDocument(lines), strings.Join(keyParts, ":"))
}

func chatReasoningTimelineDocument(lines []string) render.Document {
	rendered := make([]render.Line, 0, len(lines))
	for index, line := range lines {
		if index == 0 || index == len(lines)-1 {
			rendered = append(rendered, render.Line{Spans: []render.Span{{
				Text: line, Style: render.Style{Role: string(style.RoleReasoning)},
			}}})
			continue
		}
		if strings.HasPrefix(line, "[reasoning]") {
			rendered = append(rendered, render.Line{Spans: []render.Span{
				{Text: "[reasoning]", Style: render.Style{Role: string(style.RoleReasoning), Bold: true}},
				{Text: strings.TrimPrefix(line, "[reasoning]"), Style: render.Style{Role: string(style.RoleTextMuted)}},
			}})
			continue
		}
		rendered = append(rendered, render.Line{Spans: []render.Span{{
			Text: line, Style: render.Style{Role: string(style.RoleTextSecondary)},
		}}})
	}
	return render.LinesDoc(rendered...)
}

func chatReasoningLines(block *runtimetypes.ReasoningBlock) []string {
	rendered := chatReasoningRenderText(block, nil)
	if rendered == "" {
		return nil
	}
	return strings.Split(rendered, "\n")
}

// chatReasoningRenderText renders a complete reasoning supplement block
// (divider + meta + content + end divider). When the block's visible text
// looks like markdown, it is parsed and styled via the provided formatter
// instead of being emitted as trimmed plain-text lines.
func chatReasoningRenderText(block *runtimetypes.ReasoningBlock, formatter *formatter.MarkdownFormatter) string {
	if block == nil || !chatReasoningHasVisibleContent(block) {
		return ""
	}
	lines := []string{chatToolDivider("reasoning")}
	if meta := chatReasoningMetaLine(block); meta != "" {
		lines = append(lines, meta)
	}
	if content := chatReasoningRenderContent(block.DisplayText(), formatter, "  "); content != "" {
		lines = append(lines, content)
	} else if strings.TrimSpace(block.OpaqueState) != "" {
		lines = append(lines, "  provider 返回了不可显示的 reasoning state，已保留续接信息。")
	}
	if len(lines) == 1 {
		return ""
	}
	lines = append(lines, chatToolDivider("end reasoning"))
	return strings.Join(lines, "\n")
}

// chatReasoningRenderContent renders the reasoning block's visible text.
// When the content looks like markdown it is parsed and styled with the
// provided formatter; otherwise it falls back to the legacy trimmed
// plain-text lines. Every non-empty output line receives the caller-chosen
// indent so the markdown document keeps the same visual alignment as the
// plain-text path. Markdown rendering needs the whole document (fenced code
// blocks, multi-line spans), so the ANSI output is not re-split, trimmed or
// truncated per line.
func chatReasoningRenderContent(display string, formatter *formatter.MarkdownFormatter, indent string) string {
	display = strings.TrimSpace(display)
	if display == "" {
		return ""
	}
	if formatter != nil && formatter.IsMarkdown(display) {
		rendered := strings.TrimRight(formatter.Format(display), "\n")
		if rendered == "" {
			return ""
		}
		lines := strings.Split(rendered, "\n")
		var builder strings.Builder
		for i, line := range lines {
			if i > 0 {
				builder.WriteString("\n")
			}
			if line != "" {
				builder.WriteString(indent)
				builder.WriteString(line)
			}
		}
		return builder.String()
	}
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(display, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lines = append(lines, indent+truncateChatRuntimeText(trimmed, 160))
	}
	return strings.Join(lines, "\n")
}

func chatReasoningHasVisibleContent(block *runtimetypes.ReasoningBlock) bool {
	if block == nil {
		return false
	}
	if strings.TrimSpace(block.DisplayText()) != "" {
		return true
	}
	if strings.TrimSpace(block.OpaqueState) != "" {
		return true
	}
	return false
}

func isReasoningStreamDeltaBlock(block *runtimetypes.ReasoningBlock) bool {
	if block == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(block.Format), "stream_delta")
}

func chatReasoningMetaLine(block *runtimetypes.ReasoningBlock) string {
	if block == nil {
		return ""
	}
	parts := make([]string, 0, 2)
	if block.ReplayRequired {
		parts = append(parts, "replay=required")
	}
	if strings.TrimSpace(block.DisplayText()) == "" && strings.TrimSpace(block.OpaqueState) != "" {
		parts = append(parts, "visibility=opaque")
	}
	if len(parts) == 0 {
		return ""
	}
	return "[reasoning] " + strings.Join(parts, " ")
}

func isGenericChatToolFailureSummary(lines []string) bool {
	if len(lines) == 0 {
		return false
	}
	normalized := strings.ToLower(strings.Join(lines, " "))
	normalized = strings.Join(strings.Fields(normalized), " ")
	if normalized == "tool returned no output." {
		return true
	}
	return strings.Contains(normalized, "failed before producing output.")
}

func payloadBoolValue(payload map[string]interface{}, key string) bool {
	if payload == nil {
		return false
	}
	value, ok := payload[key]
	if !ok {
		return false
	}
	boolean, _ := value.(bool)
	return boolean
}
