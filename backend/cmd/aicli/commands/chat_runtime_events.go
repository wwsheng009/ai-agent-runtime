package commands

import (
	"context"
	"encoding/json"
	"fmt"
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
	runActive              bool
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
			text, err := chatInteractiveReadPriorityLine(session, context.Background())
			if err != nil {
				return false, err
			}
			text = strings.ToLower(strings.TrimSpace(normalizeQueuedInputLine(text)))
			return text == "y" || text == "yes", nil
		},
		askQuestion: func(prompt string, suggestions []string, required bool) (string, error) {
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
			text, err := chatInteractiveReadPriorityLine(session, context.Background())
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
	b.runActive = true
	b.renderMu.Unlock()
	b.progressMu.Lock()
	b.enqueuedEvents = 0
	b.processedEvents = 0
	b.progressMu.Unlock()
}

func (b *chatRuntimeEventBridge) EndRun() {
	if b == nil {
		return
	}
	b.renderMu.Lock()
	defer b.renderMu.Unlock()
	b.runActive = false
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

func (b *chatRuntimeEventBridge) handleEvent(event runtimeevents.Event) {
	if b == nil || b.session == nil {
		return
	}
	if b.handleAssistantReasoning(event) {
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
	if b == nil || b.writePrompt == nil || b.session == nil || b.session.RuntimeSession == nil || b.session.LocalRuntimeHost == nil || b.session.LocalRuntimeHost.RuntimeStore == nil {
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
	state, err := b.session.LocalRuntimeHost.RuntimeStore.LoadState(context.Background(), strings.TrimSpace(b.session.RuntimeSession.ID))
	if err != nil || state == nil {
		return
	}
	if state.Status != runtimechat.SessionIdle {
		return
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
		if payloadBoolValue(event.Payload, "success") {
			return chatRuntimeTimelineEvent{}
		}
		return chatRuntimeTimelineEvent{
			Line:     fmt.Sprintf("[thinking] model error %s", payloadStringValue(event.Payload["error"])),
			DedupKey: llmRequestDedupKey(event, "llm.request.finished"),
		}
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
		}
		return chatRuntimeTimelineEvent{Line: strings.Join(lines, "\n"), DedupKey: fmt.Sprintf("%s:%s:%s", strings.TrimSpace(event.Type), teamID, taskID)}
	case "team.completed":
		status := firstNonEmptyChatValue(payloadStringValue(event.Payload["status"]), "done")
		return chatRuntimeTimelineEvent{
			Line:     fmt.Sprintf("[team] completed %s status=%s", firstNonEmptyChatValue(teamID, "?"), status),
			DedupKey: fmt.Sprintf("team.completed:%s:%s", teamID, status),
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
		return chatRuntimeTimelineEvent{
			Line:     fmt.Sprintf("[context] session compact started mode=%s phase=%s %s", mode, phase, sessionCompactBudgetSummary(payload)),
			DedupKey: dedupKeyBase,
		}
	case runtimechat.EventSessionCompactCompleted:
		line := fmt.Sprintf(
			"[context] session compact completed mode=%s phase=%s token %d -> %d compacted_messages=%d history_messages=%d",
			mode,
			phase,
			intPayloadValue(payload, "token_before"),
			intPayloadValue(payload, "token_after"),
			intPayloadValue(payload, "compacted_messages"),
			intPayloadValue(payload, "message_count_after"),
		)
		if checkpointID := truncateChatRuntimeText(payloadStringValue(payload["checkpoint_id"]), 80); checkpointID != "" {
			line += " checkpoint_id=" + checkpointID
		}
		return chatRuntimeTimelineEvent{
			Line:     line,
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
	triggerLimit := intPayloadValue(payload, "trigger_token_limit")
	maxContext := intPayloadValue(payload, "max_context_tokens")
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
	source := strings.TrimSpace(payloadStringValue(payload["budget_source"]))
	switch source {
	case "default_context_max_prompt_tokens":
		return "默认 context prompt 预算"
	case "context_max_prompt_tokens":
		return "context manager 的 max_prompt_tokens"
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
	case "":
		return truncateChatRuntimeText(payloadStringValue(payload["budget_source_detail"]), 120)
	default:
		return source
	}
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
	if workdir := truncateChatRuntimeText(payloadStringValue(payload["workdir"]), 160); workdir != "" {
		return line + "\n  workdir: " + workdir
	}
	if cwd := truncateChatRuntimeText(payloadStringValue(payload["cwd"]), 160); cwd != "" {
		return line + "\n  cwd: " + cwd
	}
	return line
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
	if payload == nil {
		return ""
	}
	summary := payloadStringValue(payload["prompt_layout_summary"])
	if summary == "" {
		summary = payloadStringValue(payload["prompt_layout"])
	}
	if summary == "" {
		return ""
	}
	line := "[prompt] " + summary
	instructionTokens := intPayloadValue(payload, "instruction_tokens")
	totalTokens := intPayloadValue(payload, "total_tokens")
	instructionChars := intPayloadValue(payload, "prompt_layout_length")
	totalChars := intPayloadValue(payload, "total_message_chars")
	if instructionTokens > 0 && totalTokens > 0 && totalTokens != instructionTokens {
		line += fmt.Sprintf(" (instruction %d / total %d tokens", instructionTokens, totalTokens)
		if instructionChars > 0 && totalChars > 0 {
			line += fmt.Sprintf(", %d / %d chars", instructionChars, totalChars)
		}
		line += ")"
	} else if instructionTokens > 0 {
		line += fmt.Sprintf(" (%d tokens", instructionTokens)
		if instructionChars > 0 {
			line += fmt.Sprintf(", %d chars", instructionChars)
		}
		line += ")"
	} else if totalTokens > 0 {
		line += fmt.Sprintf(" (total %d tokens", totalTokens)
		if totalChars > 0 {
			line += fmt.Sprintf(", %d chars", totalChars)
		}
		line += ")"
	} else if instructionChars > 0 && totalChars > 0 && totalChars != instructionChars {
		line += fmt.Sprintf(" (instruction %d / total %d chars)", instructionChars, totalChars)
	} else if instructionChars > 0 {
		line += fmt.Sprintf(" (%d chars)", instructionChars)
	} else if totalChars > 0 {
		line += fmt.Sprintf(" (total %d chars)", totalChars)
	}
	return truncateChatRuntimeText(line, 200)
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
