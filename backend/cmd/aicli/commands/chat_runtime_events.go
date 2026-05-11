package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/compactruntime"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type chatRuntimeEventBridge struct {
	session                *ChatSession
	startOnce              sync.Once
	eventQueue             chan runtimeevents.Event
	runMu                  sync.Mutex
	logMu                  sync.Mutex
	renderMu               sync.Mutex
	progressMu             sync.Mutex
	runErr                 error
	rendered               map[string]struct{}
	approvalGrants         map[string]time.Time
	permissionHintShown    bool
	renderedAssistantDelta bool
	renderedAssistantFinal bool
	renderedReasoningDelta bool
	renderedReasoningFinal bool
	runStarted             bool
	runActive              bool
	nextRunPrompt          string
	activeRunPrompt        string
	requestLogState        map[string]*chatRuntimeRequestLogState
	traceLatestRequestKey  map[string]string
	latestRequestKey       string
	loggedToolCalls        map[string]struct{}
	loggedToolResults      map[string]struct{}
	toolExecutionCalls     []aicliToolExecutionCallSummary
	toolSummaryLogged      bool
	enqueuedEvents         uint64
	processedEvents        uint64
	askApproval            func(*runtimechat.ApprovalRequest) (bool, error)
	askQuestion            func(prompt string, suggestions []string, required bool) (string, error)
	writeLine              func(string)
	writeDelta             func(string)
	finalizeDelta          func()
	completeDelta          func(string) bool
	writeReasoningDelta    func(*runtimetypes.ReasoningBlock)
	finalizeReasoning      func()
	completeReasoning      func(*runtimetypes.ReasoningBlock) bool
	renderResponse         func(string)
	writePrompt            func()
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

const chatApprovalGrantTTL = 10 * time.Minute
const chatRuntimeEventSettleWindow = 80 * time.Millisecond

func ensureChatRuntimeEventBridge(session *ChatSession) *chatRuntimeEventBridge {
	if session == nil {
		return nil
	}
	if session.RuntimeEventBridge == nil {
		session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	}
	session.RuntimeEventBridge.start()
	return session.RuntimeEventBridge
}

func newChatRuntimeEventBridge(session *ChatSession) *chatRuntimeEventBridge {
	return &chatRuntimeEventBridge{
		session:    session,
		eventQueue: make(chan runtimeevents.Event, 512),
		rendered:   make(map[string]struct{}),
		writeLine: func(line string) {
			if strings.TrimSpace(line) == "" {
				return
			}
			if session != nil && session.Interaction != nil {
				session.Interaction.RenderAsyncLine(line)
				return
			}
			fmt.Println(ui.FormatAssistantSupplementBlock(line))
		},
		writeDelta: func(delta string) {
			if delta == "" {
				return
			}
			if session != nil && session.Interaction != nil {
				session.Interaction.RenderAssistantDelta(delta)
				return
			}
			fmt.Print(delta)
		},
		finalizeDelta: func() {
			if session != nil && session.Interaction != nil {
				session.Interaction.FinalizeAssistantDelta()
				return
			}
			fmt.Println()
		},
		completeDelta: func(content string) bool {
			if session != nil && session.Interaction != nil {
				return session.Interaction.CompleteAssistantResponse(content)
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
			lines := chatReasoningLines(block)
			if len(lines) == 0 {
				return
			}
			fmt.Println(ui.FormatAssistantSupplementBlock(strings.Join(lines, "\n")))
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
			return false
		},
		renderResponse: func(response string) {
			if strings.TrimSpace(response) == "" {
				return
			}
			if session != nil && session.Interaction != nil {
				session.Interaction.RenderAssistant(response)
				return
			}
			if session == nil {
				fmt.Println(response)
				return
			}
			if !session.JSONOutput && !session.NoInteractive && session.Formatter == nil {
				fmt.Println(response)
				return
			}
			renderChatResponse(session, response)
		},
		writePrompt: func() {
			if session == nil || session.NoInteractive || session.JSONOutput {
				return
			}
			if session.Interaction != nil {
				session.Interaction.PrintPrompt()
				return
			}
			fmt.Print(ui.FormatUserPromptWithAttachments(len(session.ImagePaths)))
		},
		askApproval: func(approval *runtimechat.ApprovalRequest) (bool, error) {
			beginDirectInteractiveOutput(session)
			if notice := discardPendingInteractiveInputForPriorityPrompt(session, "审批提示"); notice != "" {
				fmt.Printf("\n%s\n", formatInteractiveSupplementPromptLine(notice))
			}
			toolName := ""
			reason := ""
			if approval != nil {
				toolName = strings.TrimSpace(approval.ToolName)
				reason = strings.TrimSpace(approval.Reason)
				for _, line := range approvalRequestPreviewLines(approval) {
					fmt.Printf("\n%s", formatInteractiveSupplementPromptLine("[approval] "+line))
				}
			}
			promptLine := fmt.Sprintf("[approval] allow %s", strings.TrimSpace(toolName))
			if strings.TrimSpace(reason) != "" {
				promptLine += fmt.Sprintf(" (%s)", strings.TrimSpace(reason))
			}
			fmt.Printf("\n%s? [y/N]: ", formatInteractiveSupplementPromptLine(promptLine))
			text, err := chatInteractiveReadTransientLine(session, context.Background())
			if err != nil {
				return false, err
			}
			text = strings.ToLower(strings.TrimSpace(normalizeQueuedInputLine(text)))
			return text == "y" || text == "yes", nil
		},
		askQuestion: func(prompt string, suggestions []string, required bool) (string, error) {
			beginDirectInteractiveOutput(session)
			if notice := discardPendingInteractiveInputForPriorityPrompt(session, "问题提示"); notice != "" {
				fmt.Printf("\n%s\n", formatInteractiveSupplementPromptLine(notice))
			}
			fmt.Printf("\n%s\n", formatInteractiveSupplementPromptLine("[question] "+strings.TrimSpace(prompt)))
			if len(suggestions) > 0 {
				fmt.Printf("%s\n", formatInteractiveSupplementPromptLine("Suggestions: "+strings.Join(suggestions, ", ")))
			}
			if required {
				fmt.Print("> ")
			} else {
				fmt.Print("> (optional) ")
			}
			text, err := chatInteractiveReadTransientLine(session, context.Background())
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(normalizeQueuedInputLine(text)), nil
		},
	}
}

func (b *chatRuntimeEventBridge) start() {
	if b == nil || b.session == nil || b.session.LocalRuntimeHost == nil || b.session.LocalRuntimeHost.EventBus == nil {
		return
	}
	b.startOnce.Do(func() {
		b.session.LocalRuntimeHost.EventBus.Subscribe("", b.Handle)
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
	b.renderMu.Lock()
	b.rendered = make(map[string]struct{})
	b.pruneApprovalGrantsLocked(time.Now().UTC())
	b.renderedAssistantDelta = false
	b.renderedAssistantFinal = false
	b.renderedReasoningDelta = false
	b.renderedReasoningFinal = false
	b.runStarted = true
	b.runActive = true
	b.renderMu.Unlock()
	b.progressMu.Lock()
	b.enqueuedEvents = 0
	b.processedEvents = 0
	b.progressMu.Unlock()
	b.logMu.Lock()
	b.activeRunPrompt = b.nextRunPrompt
	b.nextRunPrompt = ""
	b.requestLogState = make(map[string]*chatRuntimeRequestLogState)
	b.traceLatestRequestKey = make(map[string]string)
	b.latestRequestKey = ""
	b.loggedToolCalls = make(map[string]struct{})
	b.loggedToolResults = make(map[string]struct{})
	b.toolExecutionCalls = nil
	b.toolSummaryLogged = false
	b.logMu.Unlock()
	if b.session != nil && b.session.Interaction != nil {
		b.session.Interaction.ResetRunState()
	}
}

func (b *chatRuntimeEventBridge) EndRun() {
	if b == nil {
		return
	}
	b.renderMu.Lock()
	b.runActive = false
	b.renderMu.Unlock()
	b.writePromptIfIdle()
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
	b.eventQueue <- event
	b.progressMu.Lock()
	b.enqueuedEvents++
	b.progressMu.Unlock()
}

func (b *chatRuntimeEventBridge) run() {
	for event := range b.eventQueue {
		b.handleEvent(event)
		b.progressMu.Lock()
		b.processedEvents++
		b.progressMu.Unlock()
	}
}

func (b *chatRuntimeEventBridge) WaitForCurrentEvents(timeout time.Duration) {
	if b == nil || timeout <= 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	stableSince := time.Time{}
	lastSeenEnqueued := uint64(0)
	for {
		b.progressMu.Lock()
		enqueued := b.enqueuedEvents
		processed := b.processedEvents
		b.progressMu.Unlock()
		now := time.Now()
		if processed >= enqueued {
			if stableSince.IsZero() || enqueued != lastSeenEnqueued {
				stableSince = now
				lastSeenEnqueued = enqueued
			}
			if now.Sub(stableSince) >= chatRuntimeEventSettleWindow {
				return
			}
		} else {
			stableSince = time.Time{}
			lastSeenEnqueued = enqueued
		}
		if now.After(deadline) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
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

func (b *chatRuntimeEventBridge) handleEvent(event runtimeevents.Event) {
	if b == nil || b.session == nil {
		return
	}
	if b.session.ExecEventBridge != nil {
		b.session.ExecEventBridge.HandleRuntimeEvent(event)
	}
	b.handleStructuredLogEvent(event)
	b.applyLLMRequestStatus(event)
	b.applySessionCompactStatus(event)
	if b.shouldSuppressLatePrimaryRunEvent(event) {
		return
	}
	if isTeamLifecycleRuntimeEvent(event.Type) && strings.TrimSpace(event.SessionID) != "" && !b.isPrimarySessionEvent(event) {
		return
	}
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
	flushedAssistant := b.shouldFlushAssistantDeltaOnSessionEnd(event)
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
	if !suppressApprovalTimeline {
		rendered = renderChatRuntimeTimelineEvent(event)
		if rendered.Line == "" {
			rendered = b.renderAsyncTeamSummaryFallback(event)
		}
	}
	if rendered.Line != "" && shouldRenderInteractiveOutput(b.session) && b.shouldRenderTimelineEvent(rendered) {
		b.writeLine(rendered.Line)
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
	actor, err := b.lookupActor(event.SessionID)
	if err != nil {
		b.setRunError(err)
		return
	}
	switch event.Type {
	case runtimechat.EventApprovalRequested:
		requestID, _ := event.Payload["request_id"].(string)
		reason, _ := event.Payload["reason"].(string)
		b.maybeRenderPermissionModeHint(reason)
		approval := b.approvalRequestForEvent(event)
		if grantKey := b.autoApprovalGrantKey(event.SessionID, approval); grantKey != "" && b.hasApprovalGrant(grantKey) {
			if err := actor.ApproveTool(context.Background(), requestID, true); err != nil {
				b.setRunError(err)
			}
			return
		}
		if hint := b.approvalPromptHint(event.SessionID, approval); hint != "" && b.writeLine != nil {
			b.writeLine(hint)
		}
		if b.session.NoInteractive {
			b.setRunError(fmt.Errorf("interactive approval required in --no-interactive mode"))
			_ = actor.ApproveTool(context.Background(), requestID, false)
			return
		}
		allowed, askErr := b.askApproval(approval)
		if askErr != nil {
			b.setRunError(askErr)
			_ = actor.ApproveTool(context.Background(), requestID, false)
			return
		}
		if allowed {
			b.rememberApprovalGrant(b.autoApprovalGrantKey(event.SessionID, approval))
		}
		if err := actor.ApproveTool(context.Background(), requestID, allowed); err != nil {
			b.setRunError(err)
		}
	case runtimechat.EventQuestionAsked:
		questionID, _ := event.Payload["question_id"].(string)
		prompt, _ := event.Payload["prompt"].(string)
		required, _ := event.Payload["required"].(bool)
		suggestions := interfaceSliceToStrings(event.Payload["suggestions"])
		if b.session.NoInteractive {
			b.setRunError(fmt.Errorf("interactive question required in --no-interactive mode"))
			_ = actor.AnswerQuestion(context.Background(), questionID, "")
			return
		}
		answer, askErr := b.askQuestion(prompt, suggestions, required)
		if askErr != nil {
			b.setRunError(askErr)
			_ = actor.AnswerQuestion(context.Background(), questionID, "")
			return
		}
		if err := actor.AnswerQuestion(context.Background(), questionID, answer); err != nil {
			b.setRunError(err)
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
		usage := &runtimetypes.TokenUsage{
			PromptTokens:     firstPositivePayloadInt(event.Payload, "usage_prompt_tokens", "prompt_tokens", "input_tokens"),
			CompletionTokens: firstPositivePayloadInt(event.Payload, "usage_completion_tokens", "completion_tokens", "output_tokens"),
			TotalTokens:      firstPositivePayloadInt(event.Payload, "usage_total_tokens"),
			CachedTokens:     firstPositivePayloadInt(event.Payload, "usage_cached_tokens", "cached_tokens", "cache_read_input_tokens", "cache_creation_input_tokens"),
			ReasoningTokens:  firstPositivePayloadInt(event.Payload, "usage_reasoning_tokens", "reasoning_tokens", "thinking_tokens"),
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

func formatInteractiveSupplementPromptLine(line string) string {
	line = strings.TrimRight(strings.ReplaceAll(line, "\r\n", "\n"), "\n")
	if strings.TrimSpace(line) == "" {
		return ""
	}
	return ui.FormatAssistantSupplementBlock(line)
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

// shouldFlushAssistantDeltaOnSessionEnd flushes any buffered streaming delta
// when the session ends (EventSessionEnd) without a preceding
// EventAssistantMessage. In a ReAct loop the runtime only emits
// EventAssistantMessage after the entire loop completes, so intermediate
// text deltas from earlier turns would otherwise be silently dropped.
func (b *chatRuntimeEventBridge) shouldFlushAssistantDeltaOnSessionEnd(event runtimeevents.Event) bool {
	if b == nil || b.session == nil || event.Type != runtimechat.EventSessionEnd {
		return false
	}
	if !b.HasRenderedAssistantDelta() || b.HasRenderedAssistantFinal() {
		return false
	}
	if b.finalizeDelta != nil {
		b.finalizeDelta()
	}
	b.renderMu.Lock()
	b.renderedAssistantFinal = true
	b.renderMu.Unlock()
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
	if b.shouldRenderTimelineEvent(rendered) && b.writeLine != nil {
		b.writeLine(rendered.Line)
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
	if b.HasRenderedAssistantFinal() {
		return true
	}
	if !shouldRenderInteractiveOutput(b.session) || !b.isPrimarySessionEvent(event) {
		return false
	}
	delta, _ := event.Payload["delta"].(string)
	if delta == "" {
		delta, _ = event.Payload["content"].(string)
	}
	if delta == "" {
		return false
	}
	b.renderMu.Lock()
	b.renderedAssistantDelta = true
	b.renderMu.Unlock()
	if b.writeDelta != nil {
		b.writeDelta(delta)
	}
	return true
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
	b.renderMu.Lock()
	b.renderedAssistantFinal = true
	b.renderMu.Unlock()
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
	if block := runtimetypes.ReasoningBlockFromMap(event.Payload["reasoning"]); block != nil {
		b.renderReasoningFromAssistantMessage(event, block)
	}
	renderedSummary := false
	if rendered := b.renderAsyncTeamSummaryFallback(event); rendered.Line != "" && b.shouldRenderTimelineEvent(rendered) {
		b.writeLine(rendered.Line)
		renderedSummary = true
	}
	if b.HasRenderedAssistantDelta() {
		content, _ := event.Payload["content"].(string)
		if strings.TrimSpace(content) != "" && b.completeDelta != nil && b.completeDelta(content) {
			b.renderMu.Lock()
			b.renderedAssistantFinal = true
			b.renderMu.Unlock()
			return true
		}
		return b.finalizeAssistantDelta(event) || renderedSummary
	}
	content, _ := event.Payload["content"].(string)
	if strings.TrimSpace(content) == "" {
		return renderedSummary
	}
	content = sanitizeInteractiveAsyncTeamLaunchResponse(content)
	if b.renderResponse != nil {
		b.renderResponse(content)
	}
	b.renderMu.Lock()
	b.renderedAssistantFinal = true
	b.renderMu.Unlock()
	return true
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
		b.writeLine(rendered.Line)
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
	if b.hasRenderedReasoningDelta() && !b.hasRenderedReasoningFinal() && b.completeReasoning != nil {
		if b.completeReasoning(block) {
			b.renderMu.Lock()
			b.renderedReasoningFinal = true
			b.renderMu.Unlock()
			return
		}
	}
	rendered := chatReasoningTimelineEvent(strings.TrimSpace(event.TraceID), "", block)
	if rendered.Line == "" {
		return
	}
	if b.shouldRenderTimelineEvent(rendered) && b.writeLine != nil {
		b.writeLine(rendered.Line)
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
	if b.session.RuntimeSession == nil || strings.TrimSpace(event.SessionID) != strings.TrimSpace(b.session.RuntimeSession.ID) {
		return chatRuntimeTimelineEvent{}
	}
	if b.session.ActiveTeam == nil || strings.TrimSpace(b.session.ActiveTeam.TeamID) == "" {
		return chatRuntimeTimelineEvent{}
	}
	teamID := strings.TrimSpace(b.session.ActiveTeam.TeamID)
	if !b.hasRenderedTimelineKey("team.completed:"+teamID+":done") &&
		!b.hasRenderedTimelineKey("team.completed:"+teamID+":failed") &&
		!b.isTerminalTeam(teamID) {
		return chatRuntimeTimelineEvent{}
	}
	content := truncateChatRuntimeText(payloadStringValue(event.Payload["content"]), 200)
	if content == "" {
		return chatRuntimeTimelineEvent{}
	}
	return chatRuntimeTimelineEvent{
		Line:     fmt.Sprintf("[team summary] %s %s", teamID, content),
		DedupKey: "team.summary:" + teamID,
	}
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
	state := actor.State()
	if state == nil || state.PendingApproval == nil {
		return nil
	}
	approval := *state.PendingApproval
	if len(approval.ArgsJSON) > 0 {
		approval.ArgsJSON = append(json.RawMessage(nil), approval.ArgsJSON...)
	}
	return &approval
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
	switch b.session.ApprovalReuseMode {
	case chatApprovalReuseSessionReadOnlyShell:
		if sid := strings.TrimSpace(sessionID); sid != "" {
			return "session:" + sid
		}
		return ""
	case chatApprovalReuseTeamReadOnlyShell:
		if b.session.ActiveTeam != nil {
			if teamID := strings.TrimSpace(b.session.ActiveTeam.TeamID); teamID != "" {
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

func (b *chatRuntimeEventBridge) maybeRenderPermissionModeHint(reason string) {
	if b == nil || b.session == nil || b.writeLine == nil {
		return
	}
	if strings.TrimSpace(reason) != "permission_mode_requires_approval" {
		return
	}
	b.renderMu.Lock()
	if b.permissionHintShown {
		b.renderMu.Unlock()
		return
	}
	b.permissionHintShown = true
	b.renderMu.Unlock()

	mode := string(b.session.PermissionMode)
	if strings.TrimSpace(mode) == "" {
		mode = string(runtimepolicy.ModeDefault)
	}
	b.writeLine(fmt.Sprintf(
		"[tip] 当前 permission-mode=%s。若你信任当前会话，可用 --yolo（等价于 --permission-mode bypass_permissions）关闭审批；--approval-reuse=%s 可减少重复只读审批（shell/网络搜索等）。",
		mode,
		formatChatApprovalReuseMode(b.session.ApprovalReuseMode),
	))
}

func (b *chatRuntimeEventBridge) approvalPromptHint(sessionID string, approval *runtimechat.ApprovalRequest) string {
	if b == nil || b.session == nil || approval == nil {
		return ""
	}
	if b.session.ApprovalReuseMode == chatApprovalReuseOff {
		return ""
	}
	scope := b.autoApprovalScope(sessionID)
	if scope == "" {
		if b.session.ApprovalReuseMode == chatApprovalReuseTeamReadOnlyShell {
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
	if b == nil {
		return
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	b.renderedAssistantFinal = true
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
	if b == nil || b.session == nil || b.session.RuntimeSession == nil {
		return false
	}
	return strings.TrimSpace(event.SessionID) == strings.TrimSpace(b.session.RuntimeSession.ID)
}

func isTeamLifecycleRuntimeEvent(eventType string) bool {
	eventType = strings.TrimSpace(eventType)
	return strings.HasPrefix(eventType, "team.") || strings.HasPrefix(eventType, "task.")
}

func (b *chatRuntimeEventBridge) isTerminalTeam(teamID string) bool {
	if b == nil || b.session == nil || b.session.LocalRuntimeHost == nil || b.session.LocalRuntimeHost.TeamStore == nil {
		return false
	}
	record, err := b.session.LocalRuntimeHost.TeamStore.GetTeam(context.Background(), strings.TrimSpace(teamID))
	if err != nil || record == nil {
		return false
	}
	return record.Status == team.TeamStatusDone || record.Status == team.TeamStatusFailed
}

func (b *chatRuntimeEventBridge) asyncTeamAssistantResponse(event runtimeevents.Event) string {
	if b == nil || b.session == nil || event.Type != runtimechat.EventAssistantMessage {
		return ""
	}
	if b.session.RuntimeSession == nil || strings.TrimSpace(event.SessionID) != strings.TrimSpace(b.session.RuntimeSession.ID) {
		return ""
	}
	if b.session.ActiveTeam == nil || strings.TrimSpace(b.session.ActiveTeam.TeamID) == "" {
		return ""
	}
	teamID := strings.TrimSpace(b.session.ActiveTeam.TeamID)
	if !b.hasRenderedTimelineKey("team.completed:"+teamID+":done") &&
		!b.hasRenderedTimelineKey("team.completed:"+teamID+":failed") &&
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
	b.renderedAssistantFinal = true
	b.renderMu.Unlock()
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
	if b.session.RuntimeSession != nil && b.session.LocalRuntimeHost != nil && b.session.LocalRuntimeHost.RuntimeStore != nil {
		state, err := b.session.LocalRuntimeHost.RuntimeStore.LoadState(context.Background(), strings.TrimSpace(b.session.RuntimeSession.ID))
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
	Line     string
	DedupKey string
}

func renderChatRuntimeEvent(event runtimeevents.Event) string {
	return renderChatRuntimeTimelineEvent(event).Line
}

func renderChatRuntimeTimelineEvent(event runtimeevents.Event) chatRuntimeTimelineEvent {
	teamID := payloadStringValue(event.Payload["team_id"])
	switch event.Type {
	case runtimechat.EventLLMRequestStarted, "llm.request.started":
		if line := chatLLMRequestPromptLayoutHint(event.Payload); line != "" {
			return chatRuntimeTimelineEvent{
				Line:     line,
				DedupKey: llmRequestDedupKey(event, "llm.request.started"),
			}
		}
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
		return chatRuntimeTimelineEvent{Line: "[planning] completed"}
	case "subagent.batch.started":
		return chatRuntimeTimelineEvent{}
	case "subagent.batch.completed":
		return chatRuntimeTimelineEvent{Line: "[subagents] completed"}
	case "subagent.started":
		return chatRuntimeTimelineEvent{}
	case "subagent.completed":
		return chatRuntimeTimelineEvent{Line: fmt.Sprintf("[subagent] completed %s", firstNonEmptyChatValue(payloadStringValue(event.Payload["agent_id"]), payloadStringValue(event.Payload["role"]), strings.TrimSpace(event.SessionID)))}
	case "subagent.denied":
		return chatRuntimeTimelineEvent{Line: fmt.Sprintf("[subagent] denied %s", payloadStringValue(event.Payload["reason"]))}
	case "tool.requested":
		return chatRuntimeTimelineEvent{Line: appendCompactToolDirectory(renderCompactToolRequestedWithSource(firstNonEmptyChatValue(strings.TrimSpace(event.ToolName), payloadStringValue(event.Payload["tool_name"])), "", payloadStringValue(event.Payload["command_text"]), payloadStringValue(event.Payload["arg_preview"]), payloadStringValue(event.Payload[toolresult.SourceKey])), event.Payload)}
	case "tool.completed":
		line := renderCompactToolCompletedWithSource(firstNonEmptyChatValue(strings.TrimSpace(event.ToolName), payloadStringValue(event.Payload["tool_name"])), "", payloadStringValue(event.Payload["command_text"]), payloadStringValue(event.Payload["arg_preview"]), payloadStringValue(event.Payload[toolresult.SourceKey]), chatToolSummaryLines(event.Payload))
		line = appendCompactToolDirectory(line, event.Payload)
		rendered := []string{line}
		if waitingLine := chatToolPostCommandHint(event.Payload); waitingLine != "" {
			rendered = append(rendered, waitingLine)
		}
		line = strings.Join(rendered, "\n")
		return chatRuntimeTimelineEvent{Line: line}
	case "tool.denied":
		line := fmt.Sprintf("[tool denied] %s", payloadStringValue(event.Payload["reason"]))
		return chatRuntimeTimelineEvent{Line: line}
	case runtimechat.EventApprovalRequested:
		return chatRuntimeTimelineEvent{Line: fmt.Sprintf("[approval] %s", payloadStringValue(event.Payload["tool_name"]))}
	case runtimechat.EventQuestionAsked:
		return chatRuntimeTimelineEvent{Line: fmt.Sprintf("[question] %s", payloadStringValue(event.Payload["prompt"]))}
	case runtimechat.EventMailboxReceived:
		messageID := firstNonEmptyChatValue(payloadStringValue(event.Payload["message_id"]), "?")
		fromAgent := firstNonEmptyChatValue(payloadStringValue(event.Payload["from_agent"]), "?")
		toAgent := firstNonEmptyChatValue(payloadStringValue(event.Payload["to_agent"]), "*")
		kind := firstNonEmptyChatValue(payloadStringValue(event.Payload["kind"]), "info")
		body := truncateChatRuntimeText(payloadStringValue(event.Payload["body"]), 160)
		taskID := payloadStringValue(event.Payload["task_id"])
		if body == "" && taskID == "" && fromAgent == "?" && toAgent == "*" && kind == "info" {
			return chatRuntimeTimelineEvent{
				Line: fmt.Sprintf("[mailbox] %s %s",
					firstNonEmptyChatValue(teamID, "?"),
					messageID),
				DedupKey: "mailbox:" + messageID,
			}
		}
		line := fmt.Sprintf("[%s] %s -> %s", kind, fromAgent, toAgent)
		if taskID != "" {
			line += " " + taskID
		}
		if body != "" {
			line += " " + body
		}
		return chatRuntimeTimelineEvent{Line: line, DedupKey: "mailbox:" + messageID}
	case "task.started", "task.completed", "task.failed", "task.blocked", "team.task.completed", "team.task.failed", "team.task.blocked":
		action := chatRuntimeTaskAction(event.Type)
		if action == "started" {
			return chatRuntimeTimelineEvent{}
		}
		taskID := firstNonEmptyChatValue(payloadStringValue(event.Payload["task_id"]), "?")
		assignee := payloadStringValue(event.Payload["assignee"])
		summary := truncateChatRuntimeText(payloadStringValue(event.Payload["summary"]), 160)
		line := fmt.Sprintf("[task] %s %s", action, taskID)
		if assignee != "" {
			line += fmt.Sprintf(" @%s", assignee)
		}
		if summary != "" {
			line += " " + summary
		}
		lines := []string{line}
		if action == "failed" && strings.EqualFold(strings.TrimSpace(payloadStringValue(event.Payload["error_type"])), "prompt_preflight") {
			lines[0] += " [prompt preflight]"
			if reason := promptPreflightReasonSummary(event.Payload); reason != "" {
				lines = append(lines, "  原因: "+reason)
			}
			if model := promptPreflightModelSummary(event.Payload); model != "" {
				lines = append(lines, "  模型: "+model)
			}
			if budget := promptPreflightBudgetSourceSummary(event.Payload); budget != "" {
				lines = append(lines, "  预算: "+budget)
			}
			if recovery := promptPreflightRecoverySummary(event.Payload); recovery != "" {
				lines = append(lines, "  恢复: "+recovery)
			}
			if extras := runtimeContextSummaryLines(event.Payload, false); len(extras) > 0 {
				lines = append(lines, extras...)
			}
		} else if action == "blocked" && strings.EqualFold(strings.TrimSpace(payloadStringValue(event.Payload["replan_error_type"])), "prompt_preflight") {
			replanPayload := extractPrefixedRuntimePayload(event.Payload, "replan")
			if reason := promptPreflightReasonSummary(replanPayload); reason != "" {
				lines = append(lines, "  replan: [prompt preflight] "+reason)
			}
			if model := promptPreflightModelSummary(replanPayload); model != "" {
				lines = append(lines, "  replan 模型: "+model)
			}
			if budget := promptPreflightBudgetSourceSummary(replanPayload); budget != "" {
				lines = append(lines, "  replan 预算: "+budget)
			}
			if recovery := promptPreflightRecoverySummary(replanPayload); recovery != "" {
				lines = append(lines, "  replan 恢复: "+recovery)
			}
			if extras := runtimeContextSummaryLines(replanPayload, false); len(extras) > 0 {
				lines = append(lines, extras...)
			}
		}
		return chatRuntimeTimelineEvent{Line: strings.Join(lines, "\n"), DedupKey: fmt.Sprintf("%s:%s:%s", strings.TrimSpace(event.Type), teamID, taskID)}
	case "team.completed":
		status := firstNonEmptyChatValue(payloadStringValue(event.Payload["status"]), "done")
		return chatRuntimeTimelineEvent{
			Line:     fmt.Sprintf("[team] completed %s status=%s", firstNonEmptyChatValue(teamID, "?"), status),
			DedupKey: fmt.Sprintf("team.completed:%s:%s", teamID, status),
		}
	case "team.interrupted":
		status := firstNonEmptyChatValue(payloadStringValue(event.Payload["status"]), "paused")
		return chatRuntimeTimelineEvent{
			Line:     fmt.Sprintf("[team] interrupted %s status=%s", firstNonEmptyChatValue(teamID, "?"), status),
			DedupKey: fmt.Sprintf("team.interrupted:%s:%s", teamID, status),
		}
	case "team.plan.failed", "team.plan.replan_failed", "team.summary.failed":
		action := strings.TrimSpace(event.Type)
		headline := "[team] failed"
		switch action {
		case "team.plan.failed":
			headline = fmt.Sprintf("[team plan] failed %s", firstNonEmptyChatValue(teamID, "?"))
		case "team.plan.replan_failed":
			headline = fmt.Sprintf("[team replan] failed %s", firstNonEmptyChatValue(teamID, "?"))
			if taskID := strings.TrimSpace(payloadStringValue(event.Payload["task_id"])); taskID != "" {
				headline += " " + taskID
			}
		case "team.summary.failed":
			headline = fmt.Sprintf("[team summary] failed %s", firstNonEmptyChatValue(teamID, "?"))
		}
		lines := []string{headline}
		if strings.EqualFold(strings.TrimSpace(payloadStringValue(event.Payload["error_type"])), "prompt_preflight") {
			lines[0] += " [prompt preflight]"
			if reason := promptPreflightReasonSummary(event.Payload); reason != "" {
				lines = append(lines, "  原因: "+reason)
			}
			if model := promptPreflightModelSummary(event.Payload); model != "" {
				lines = append(lines, "  模型: "+model)
			}
			if budget := promptPreflightBudgetSourceSummary(event.Payload); budget != "" {
				lines = append(lines, "  预算: "+budget)
			}
			if recovery := promptPreflightRecoverySummary(event.Payload); recovery != "" {
				lines = append(lines, "  恢复: "+recovery)
			}
			if extras := runtimeContextSummaryLines(event.Payload, false); len(extras) > 0 {
				lines = append(lines, extras...)
			}
		} else if summary := truncateChatRuntimeText(payloadStringValue(event.Payload["error"]), 160); summary != "" {
			lines = append(lines, "  错误: "+summary)
		}
		return chatRuntimeTimelineEvent{
			Line:     strings.Join(lines, "\n"),
			DedupKey: fmt.Sprintf("%s:%s:%s", action, teamID, payloadStringValue(event.Payload["task_id"])),
		}
	case "team.summary", "team.summary.generated":
		return renderTeamSummaryTimelineEvent(event, teamID)
	case chatEventInputQueueDetected:
		count := intPayloadValue(event.Payload, "queued_input_count")
		source := firstNonEmptyChatValue(payloadStringValue(event.Payload["source"]), "stdin")
		line := fmt.Sprintf("[input] queued %d line(s) from %s", count, source)
		return chatRuntimeTimelineEvent{
			Line:     line,
			DedupKey: fmt.Sprintf("input.queue.detected:%s:%d", strings.TrimSpace(event.SessionID), count),
		}
	case chatEventInputQueueDrained:
		return chatRuntimeTimelineEvent{
			Line:     "[input] queued input drained",
			DedupKey: fmt.Sprintf("input.queue.drained:%s", strings.TrimSpace(event.SessionID)),
		}
	case chatEventInputQueueDiscarded:
		count := intPayloadValue(event.Payload, "discarded_count")
		promptKind := payloadStringValue(event.Payload["prompt_kind"])
		line := fmt.Sprintf("[input] discarded %d queued line(s)", count)
		if promptKind != "" {
			line += " before " + promptKind
		}
		return chatRuntimeTimelineEvent{
			Line:     line,
			DedupKey: fmt.Sprintf("input.queue.discarded:%s:%s:%d", strings.TrimSpace(event.SessionID), promptKind, count),
		}
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
	payload := event.Payload
	if payload == nil {
		return chatRuntimeTimelineEvent{}
	}
	traceID := firstNonEmptyChatValue(strings.TrimSpace(event.TraceID), payloadStringValue(payload["trace_id"]))
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
	if len(parts) == 0 {
		return chatRuntimeTimelineEvent{}
	}
	return chatRuntimeTimelineEvent{
		Line:     "[retry] " + strings.Join(parts, " "),
		DedupKey: fmt.Sprintf("llm.retry:%s:%s:%d:%s:%s", traceID, stepLabel, attempt, reason, source),
	}
}

func renderLLMRequestFinishedTimelineEvent(event runtimeevents.Event) chatRuntimeTimelineEvent {
	payload := event.Payload
	if payload == nil {
		return chatRuntimeTimelineEvent{}
	}
	if !payloadBoolValue(payload, "success") {
		return chatRuntimeTimelineEvent{
			Line:     fmt.Sprintf("[thinking] model error %s", payloadStringValue(payload["error"])),
			DedupKey: llmRequestDedupKey(event, "llm.request.finished"),
		}
	}
	headline := "[thinking] request finished"
	if target := strings.TrimSpace(chatLLMRequestTargetSummary(payload)); target != "" {
		headline += " " + target
	}
	lines := []string{headline}
	if extras := runtimeContextSummaryLines(payload, true); len(extras) > 0 {
		lines = append(lines, extras...)
	} else {
		return chatRuntimeTimelineEvent{}
	}
	return chatRuntimeTimelineEvent{
		Line:     strings.Join(lines, "\n"),
		DedupKey: llmRequestDedupKey(event, "llm.request.finished"),
	}
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
		if cached := firstPositivePayloadInt(payload, "usage_cached_tokens"); cached > 0 {
			usageParts = append(usageParts, fmt.Sprintf("cached=%d", cached))
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
		lines := []string{
			fmt.Sprintf("[context] session compact started mode=%s phase=%s %s", mode, phase, sessionCompactBudgetSummary(payload)),
		}
		if extras := runtimeContextSummaryLines(payload, false); len(extras) > 0 {
			lines = append(lines, extras...)
		}
		return chatRuntimeTimelineEvent{
			Line:     strings.Join(lines, "\n"),
			DedupKey: dedupKeyBase,
		}
	case runtimechat.EventSessionCompactCompleted:
		lines := []string{fmt.Sprintf(
			"[context] session compact completed mode=%s phase=%s token %d -> %d compacted_messages=%d history_messages=%d",
			mode,
			phase,
			intPayloadValue(payload, "token_before"),
			intPayloadValue(payload, "token_after"),
			intPayloadValue(payload, "compacted_messages"),
			intPayloadValue(payload, "message_count_after"),
		)}
		if checkpointID := truncateChatRuntimeText(payloadStringValue(payload["checkpoint_id"]), 80); checkpointID != "" {
			lines[0] += " checkpoint_id=" + checkpointID
		}
		if extras := runtimeContextSummaryLines(payload, true); len(extras) > 0 {
			lines = append(lines, extras...)
		}
		return chatRuntimeTimelineEvent{
			Line:     strings.Join(lines, "\n"),
			DedupKey: dedupKeyBase,
		}
	case runtimechat.EventSessionCompactSkipped:
		return chatRuntimeTimelineEvent{
			Line:     fmt.Sprintf("[context] session compact skipped mode=%s phase=%s reason=%s", mode, phase, sessionCompactReasonSummary(payload)),
			DedupKey: dedupKeyBase + ":reason=" + sessionCompactReasonSummary(payload),
		}
	case runtimechat.EventSessionCompactFailed:
		line := fmt.Sprintf("[context] session compact failed mode=%s phase=%s reason=%s", mode, phase, sessionCompactReasonSummary(payload))
		if errText := truncateChatRuntimeText(payloadStringValue(payload["error"]), 160); errText != "" {
			line += " error=" + errText
		}
		return chatRuntimeTimelineEvent{
			Line:     line,
			DedupKey: dedupKeyBase + ":failed",
		}
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

	headline := "[prompt preflight] 本地拦截：请求在发送给模型前因上下文预算超限而终止"
	if promptTokens > 0 && promptBudget > 0 {
		headline = fmt.Sprintf("[prompt preflight] 本地拦截：prompt %d > budget %d", promptTokens, promptBudget)
	} else if promptTokens > 0 {
		headline = fmt.Sprintf("[prompt preflight] 本地拦截：prompt=%d", promptTokens)
	}

	lines := []string{headline}
	if reason := promptPreflightReasonSummary(payload); reason != "" {
		lines = append(lines, "  原因: "+reason)
	}
	if suggestion := truncateChatRuntimeText(payloadStringValue(payload["suggested_action"]), 160); suggestion != "" {
		lines = append(lines, "  建议: "+suggestion)
	}
	if model := promptPreflightModelSummary(payload); model != "" {
		lines = append(lines, "  模型: "+model)
	}
	if budget := promptPreflightBudgetSourceSummary(payload); budget != "" {
		lines = append(lines, "  预算: "+budget)
	}
	if activeTurn := promptPreflightActiveTurnSummary(payload); activeTurn != "" {
		lines = append(lines, "  active-turn: "+activeTurn)
	}
	if recovery := promptPreflightRecoverySummary(payload); recovery != "" {
		lines = append(lines, "  恢复: "+recovery)
	}
	if extras := runtimeContextSummaryLines(payload, false); len(extras) > 0 {
		lines = append(lines, extras...)
	}

	traceID := firstNonEmptyChatValue(strings.TrimSpace(event.TraceID), payloadStringValue(payload["trace_id"]))
	failureCode := firstNonEmptyChatValue(payloadStringValue(payload["failure_reason_code"]), "prompt_preflight")
	return chatRuntimeTimelineEvent{
		Line:     strings.Join(lines, "\n"),
		DedupKey: fmt.Sprintf("session_end.prompt_preflight:%s:%s:%s", strings.TrimSpace(event.SessionID), traceID, failureCode),
	}
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
	line := fmt.Sprintf("[team summary] %s", firstNonEmptyChatValue(teamID, "?"))
	lines := []string{}
	if strings.EqualFold(strings.TrimSpace(payloadStringValue(event.Payload["summary_source"])), "fallback") {
		line += " [fallback]"
		if strings.EqualFold(strings.TrimSpace(payloadStringValue(event.Payload["error_type"])), "prompt_preflight") {
			line += " [prompt preflight]"
		}
	}
	if summary != "" {
		line += " " + summary
	}
	lines = append(lines, line)
	if strings.EqualFold(strings.TrimSpace(payloadStringValue(event.Payload["summary_source"])), "fallback") {
		if strings.EqualFold(strings.TrimSpace(payloadStringValue(event.Payload["error_type"])), "prompt_preflight") {
			if reason := promptPreflightReasonSummary(event.Payload); reason != "" {
				lines = append(lines, "  原因: "+reason)
			}
			if model := promptPreflightModelSummary(event.Payload); model != "" {
				lines = append(lines, "  模型: "+model)
			}
			if budget := promptPreflightBudgetSourceSummary(event.Payload); budget != "" {
				lines = append(lines, "  预算: "+budget)
			}
			if recovery := promptPreflightRecoverySummary(event.Payload); recovery != "" {
				lines = append(lines, "  恢复: "+recovery)
			}
			if extras := runtimeContextSummaryLines(event.Payload, false); len(extras) > 0 {
				lines = append(lines, extras...)
			}
		} else if reason := teamSummaryFallbackReasonSummary(payloadStringValue(event.Payload["fallback_reason"])); reason != "" {
			lines = append(lines, "  fallback: "+reason)
		}
	}
	return chatRuntimeTimelineEvent{
		Line:     strings.Join(lines, "\n"),
		DedupKey: fmt.Sprintf("team.summary:%s", teamID),
	}
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
//   - "readonly_shell":   shell-like tools (bash, execute_shell_command, …)
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
	extras := make([]string, 0, 2)
	if workdir := truncateChatRuntimeText(payloadStringValue(payload["workdir"]), 160); workdir != "" {
		extras = append(extras, "  workdir: "+workdir)
	} else if cwd := truncateChatRuntimeText(payloadStringValue(payload["cwd"]), 160); cwd != "" {
		extras = append(extras, "  cwd: "+cwd)
	}
	if shell := truncateChatRuntimeText(chatToolShellDisplay(payload), 180); shell != "" {
		extras = append(extras, "  shell: "+shell)
	}
	if len(extras) == 0 {
		return line
	}
	return line + "\n" + strings.Join(extras, "\n")
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

	out := make([]string, 0, 3)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		out = append(out, truncateChatRuntimeText(trimmed, 120))
		if len(out) == maxLines {
			return out
		}
	}
	if len(out) > 0 {
		if errText != "" && isGenericChatToolFailureSummary(out) {
			return []string{truncateChatRuntimeText("failed: "+errText, 120)}
		}
		return out
	}

	if errText != "" {
		return []string{truncateChatRuntimeText("failed: "+errText, 120)}
	}
	return nil
}

func chatToolSummaryLineLimit(payload map[string]interface{}) int {
	switch toolresult.NormalizeSource(payloadStringValue(payload[toolresult.SourceKey])) {
	case toolresult.SourceMeta, toolresult.SourceMCP, toolresult.SourceBroker:
		return 2
	default:
		return 3
	}
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

func chatLLMRequestPromptLayoutHint(payload map[string]interface{}) string {
	return ""
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
	if reasoningTokens := intPayloadValue(event.Payload, "usage_reasoning_tokens"); reasoningTokens > 0 {
		parts = append(parts, fmt.Sprintf("usage_reasoning_tokens=%d", reasoningTokens))
	}
	if source := strings.TrimSpace(payloadStringValue(event.Payload["usage_source"])); source != "" {
		parts = append(parts, "usage_source="+truncateChatRuntimeText(source, 80))
	}
	if len(parts) == 0 {
		return ""
	}
	return "[llm-debug] request_finished " + strings.Join(parts, " ")
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
	return chatRuntimeTimelineEvent{
		Line:     strings.Join(lines, "\n"),
		DedupKey: strings.Join(keyParts, ":"),
	}
}

func chatReasoningLines(block *runtimetypes.ReasoningBlock) []string {
	if block == nil {
		return nil
	}
	if !chatReasoningHasVisibleContent(block) {
		return nil
	}
	lines := []string{chatToolDivider("reasoning")}
	meta := chatReasoningMetaLine(block)
	if meta != "" {
		lines = append(lines, meta)
	}
	display := strings.TrimSpace(block.DisplayText())
	if display != "" {
		for _, line := range strings.Split(strings.ReplaceAll(display, "\r\n", "\n"), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			lines = append(lines, "  "+truncateChatRuntimeText(trimmed, 160))
		}
	} else if strings.TrimSpace(block.OpaqueState) != "" {
		lines = append(lines, "  provider 返回了不可显示的 reasoning state，已保留续接信息。")
	}
	if len(lines) == 1 {
		return nil
	}
	lines = append(lines, chatToolDivider("end reasoning"))
	return lines
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
