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
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
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
	runActive              bool
	enqueuedEvents         uint64
	processedEvents        uint64
	askApproval            func(toolName, reason string) (bool, error)
	askQuestion            func(prompt string, suggestions []string, required bool) (string, error)
	writeLine              func(string)
	writeDelta             func(string)
	finalizeDelta          func()
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
		eventQueue: make(chan runtimeevents.Event, 128),
		rendered:   make(map[string]struct{}),
		writeLine: func(line string) {
			if strings.TrimSpace(line) == "" {
				return
			}
			if session != nil && session.Interaction != nil {
				session.Interaction.RenderAsyncLine(line)
				return
			}
			fmt.Println(line)
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
			fmt.Print(ui.FormatUserPrompt())
		},
		askApproval: func(toolName, reason string) (bool, error) {
			if notice := discardPendingInteractiveInputForPriorityPrompt(session, "审批提示"); notice != "" {
				fmt.Printf("\n%s\n", notice)
			}
			fmt.Printf("\n[approval] allow %s", strings.TrimSpace(toolName))
			if strings.TrimSpace(reason) != "" {
				fmt.Printf(" (%s)", strings.TrimSpace(reason))
			}
			fmt.Print("? [y/N]: ")
			text, err := chatInteractiveReadPriorityLine(session, context.Background())
			if err != nil {
				return false, err
			}
			text = strings.ToLower(strings.TrimSpace(normalizeQueuedInputLine(text)))
			return text == "y" || text == "yes", nil
		},
		askQuestion: func(prompt string, suggestions []string, required bool) (string, error) {
			if notice := discardPendingInteractiveInputForPriorityPrompt(session, "问题提示"); notice != "" {
				fmt.Printf("\n%s\n", notice)
			}
			fmt.Printf("\n[question] %s\n", strings.TrimSpace(prompt))
			if len(suggestions) > 0 {
				fmt.Printf("Suggestions: %s\n", strings.Join(suggestions, ", "))
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
	select {
	case b.eventQueue <- event:
		b.progressMu.Lock()
		b.enqueuedEvents++
		b.progressMu.Unlock()
	default:
	}
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
	if b.handleAssistantDelta(event) {
		return
	}
	if b.handlePrimaryAssistantMessage(event) {
		b.writePromptIfIdle()
		return
	}
	if b.shouldSuppressTimelineDuringAssistantStream(event) {
		return
	}
	renderedSomething := false
	rendered := renderChatRuntimeTimelineEvent(event)
	if rendered.Line == "" {
		rendered = b.renderAsyncTeamSummaryFallback(event)
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
		toolName, _ := event.Payload["tool_name"].(string)
		reason, _ := event.Payload["reason"].(string)
		b.maybeRenderPermissionModeHint(reason)
		approval := b.pendingApprovalForSession(event.SessionID)
		if grantKey := b.autoApprovalGrantKey(event.SessionID, approval); grantKey != "" && b.hasApprovalGrant(grantKey) {
			if b.writeLine != nil {
				b.writeLine(fmt.Sprintf("[approval] auto-approved %s", strings.TrimSpace(toolName)))
			}
			if err := actor.ApproveTool(context.Background(), requestID, true); err != nil {
				b.setRunError(err)
			}
			return
		}
		if b.session.NoInteractive {
			b.setRunError(fmt.Errorf("interactive approval required in --no-interactive mode"))
			_ = actor.ApproveTool(context.Background(), requestID, false)
			return
		}
		allowed, askErr := b.askApproval(toolName, reason)
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
	renderedSummary := false
	if rendered := b.renderAsyncTeamSummaryFallback(event); rendered.Line != "" && b.shouldRenderTimelineEvent(rendered) {
		b.writeLine(rendered.Line)
		renderedSummary = true
	}
	if b.HasRenderedAssistantDelta() {
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
	if b == nil {
		return ""
	}
	if b.session == nil || b.session.ApprovalReuseMode != chatApprovalReuseTeamReadOnlyShell {
		return ""
	}
	if b.session != nil && b.session.ActiveTeam != nil {
		if teamID := strings.TrimSpace(b.session.ActiveTeam.TeamID); teamID != "" {
			return "team:" + teamID
		}
	}
	return ""
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
		"[tip] 当前 permission-mode=%s。若你信任当前会话，可用 --yolo（等价于 --permission-mode bypass_permissions）关闭审批；--approval-reuse=%s 仅减少同一团队里的重复只读 shell 审批。",
		mode,
		formatChatApprovalReuseMode(b.session.ApprovalReuseMode),
	))
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
		model := firstNonEmptyChatValue(payloadStringValue(event.Payload["model"]), "?")
		return chatRuntimeTimelineEvent{
			Line:     fmt.Sprintf("[thinking] contacting model=%s", model),
			DedupKey: fmt.Sprintf("llm.request.started:%s", strings.TrimSpace(event.TraceID)),
		}
	case runtimechat.EventLLMRequestFinished, "llm.request.finished":
		if payloadBoolValue(event.Payload, "success") {
			return chatRuntimeTimelineEvent{
				Line:     "[thinking] model responded",
				DedupKey: fmt.Sprintf("llm.request.finished:%s", strings.TrimSpace(event.TraceID)),
			}
		}
		return chatRuntimeTimelineEvent{
			Line:     fmt.Sprintf("[thinking] model error %s", payloadStringValue(event.Payload["error"])),
			DedupKey: fmt.Sprintf("llm.request.finished:%s", strings.TrimSpace(event.TraceID)),
		}
	case "planning.started":
		return chatRuntimeTimelineEvent{Line: "[planning] started"}
	case "planning.completed":
		return chatRuntimeTimelineEvent{Line: "[planning] completed"}
	case "subagent.batch.started":
		return chatRuntimeTimelineEvent{Line: "[subagents] started"}
	case "subagent.batch.completed":
		return chatRuntimeTimelineEvent{Line: "[subagents] completed"}
	case "subagent.started":
		return chatRuntimeTimelineEvent{Line: fmt.Sprintf("[subagent] started %s", firstNonEmptyChatValue(payloadStringValue(event.Payload["agent_id"]), payloadStringValue(event.Payload["role"]), strings.TrimSpace(event.SessionID)))}
	case "subagent.completed":
		return chatRuntimeTimelineEvent{Line: fmt.Sprintf("[subagent] completed %s", firstNonEmptyChatValue(payloadStringValue(event.Payload["agent_id"]), payloadStringValue(event.Payload["role"]), strings.TrimSpace(event.SessionID)))}
	case "subagent.denied":
		return chatRuntimeTimelineEvent{Line: fmt.Sprintf("[subagent] denied %s", payloadStringValue(event.Payload["reason"]))}
	case "tool.requested":
		return chatRuntimeTimelineEvent{Line: fmt.Sprintf("[tool] %s", firstNonEmptyChatValue(strings.TrimSpace(event.ToolName), payloadStringValue(event.Payload["tool_name"])))}
	case "tool.completed":
		return chatRuntimeTimelineEvent{Line: fmt.Sprintf("[tool done] %s", firstNonEmptyChatValue(strings.TrimSpace(event.ToolName), payloadStringValue(event.Payload["tool_name"])))}
	case "tool.denied":
		return chatRuntimeTimelineEvent{Line: fmt.Sprintf("[tool denied] %s", payloadStringValue(event.Payload["reason"]))}
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
		taskID := firstNonEmptyChatValue(payloadStringValue(event.Payload["task_id"]), "?")
		assignee := payloadStringValue(event.Payload["assignee"])
		summary := truncateChatRuntimeText(payloadStringValue(event.Payload["summary"]), 160)
		line := fmt.Sprintf("[task] %s %s", chatRuntimeTaskAction(event.Type), taskID)
		if assignee != "" {
			line += fmt.Sprintf(" @%s", assignee)
		}
		if summary != "" {
			line += " " + summary
		}
		return chatRuntimeTimelineEvent{Line: line, DedupKey: fmt.Sprintf("%s:%s:%s", strings.TrimSpace(event.Type), teamID, taskID)}
	case "team.completed":
		status := firstNonEmptyChatValue(payloadStringValue(event.Payload["status"]), "done")
		return chatRuntimeTimelineEvent{
			Line:     fmt.Sprintf("[team] completed %s status=%s", firstNonEmptyChatValue(teamID, "?"), status),
			DedupKey: fmt.Sprintf("team.completed:%s:%s", teamID, status),
		}
	case "team.summary":
		summary := truncateChatRuntimeText(payloadStringValue(event.Payload["summary"]), 200)
		line := fmt.Sprintf("[team summary] %s", firstNonEmptyChatValue(teamID, "?"))
		if summary != "" {
			line += " " + summary
		}
		return chatRuntimeTimelineEvent{
			Line:     line,
			DedupKey: fmt.Sprintf("team.summary:%s", teamID),
		}
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

func approvalGrantFamily(toolName string, argsJSON json.RawMessage) string {
	switch strings.TrimSpace(toolName) {
	case "bash", "execute_shell_command":
		if isReadOnlyShellApprovalArgs(argsJSON) {
			return "readonly_shell"
		}
	}
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
	lower := strings.ToLower(command)
	for _, marker := range []string{"&&", "||", ";", ">>", "out-file", "set-content", "add-content", "copy-item", "move-item", "remove-item", "new-item", "rename-item", "invoke-webrequest", "curl ", "wget ", " start-process", "taskkill", " rm ", " del ", " move ", " copy ", " mkdir ", " rmdir ", " sed -i", " perl -pi", "git apply", "git commit", "git push"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	for _, marker := range []string{">", "<"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}

	segments := strings.Split(lower, "|")
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			return false
		}
		fields := strings.Fields(segment)
		if len(fields) == 0 {
			return false
		}
		cmd := normalizeApprovalCommandName(fields[0])
		switch cmd {
		case "ls", "dir", "pwd", "cat", "type", "find", "findstr", "grep", "rg", "tree", "stat", "head", "tail", "wc",
			"get-childitem", "gci", "get-content", "gc", "select-string", "sls", "where-object", "sort-object", "measure-object",
			"format-table", "ft", "format-list", "fl", "resolve-path", "test-path":
			continue
		case "git":
			if len(fields) < 2 {
				return false
			}
			switch strings.TrimSpace(fields[1]) {
			case "status", "diff", "log", "show", "branch":
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
