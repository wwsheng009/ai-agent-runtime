package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ai-gateway/ai-agent-runtime/internal/agent"
	"github.com/ai-gateway/ai-agent-runtime/internal/artifact"
	"github.com/ai-gateway/ai-agent-runtime/internal/checkpoint"
	runtimeevents "github.com/ai-gateway/ai-agent-runtime/internal/events"
	runtimehooks "github.com/ai-gateway/ai-agent-runtime/internal/hooks"
	"github.com/ai-gateway/ai-agent-runtime/internal/llm"
	runtimeoutput "github.com/ai-gateway/ai-agent-runtime/internal/output"
	runtimepolicy "github.com/ai-gateway/ai-agent-runtime/internal/policy"
	"github.com/ai-gateway/ai-agent-runtime/internal/toolbroker"
	runtimetypes "github.com/ai-gateway/ai-agent-runtime/internal/types"
	"github.com/ai-gateway/ai-agent-runtime/internal/team"
	"github.com/google/uuid"
)

// SessionActorConfig configures a SessionActor instance.
type SessionActorConfig struct {
	Agent        *agent.Agent
	LLMRuntime   *llm.LLMRuntime
	SessionStore SessionStorage
	StateStore   RuntimeStateStore
	EventStore   EventStore
	EventBus     *runtimeevents.Bus
	LoopConfig   *agent.LoopReActConfig
}

// SessionActor serializes session commands and manages execution state.
type SessionActor struct {
	id           string
	agent        *agent.Agent
	llmRuntime   *llm.LLMRuntime
	loopConfig   *agent.LoopReActConfig
	sessionStore SessionStorage
	stateStore   RuntimeStateStore
	eventStore   EventStore
	eventBus     *runtimeevents.Bus

	cmdCh chan Command
	stop  chan struct{}
	done  chan struct{}

	startOnce sync.Once
	stopOnce  sync.Once

	mu           sync.RWMutex
	state        *RuntimeState
	activeCancel context.CancelFunc
	interrupted  bool

	waiterMu        sync.Mutex
	approvalWaiters map[string]chan runtimepolicy.ApprovalResponse
	questionWaiters map[string]chan string
	activeRunWG     sync.WaitGroup
}

// NewSessionActor creates a new session actor.
func NewSessionActor(sessionID string, cfg SessionActorConfig) (*SessionActor, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("session id is required")
	}
	if cfg.Agent == nil {
		return nil, fmt.Errorf("agent is required")
	}
	bus := cfg.EventBus
	if bus == nil {
		bus = cfg.Agent.GetEventBus()
		if bus == nil {
			bus = runtimeevents.NewBus()
		}
	}
	actor := &SessionActor{
		id:              sessionID,
		agent:           cfg.Agent,
		llmRuntime:      cfg.LLMRuntime,
		loopConfig:      cfg.LoopConfig,
		sessionStore:    cfg.SessionStore,
		stateStore:      cfg.StateStore,
		eventStore:      cfg.EventStore,
		eventBus:        bus,
		cmdCh:           make(chan Command, 32),
		stop:            make(chan struct{}),
		done:            make(chan struct{}),
		approvalWaiters: make(map[string]chan runtimepolicy.ApprovalResponse),
		questionWaiters: make(map[string]chan string),
	}
	if err := actor.loadState(context.Background()); err != nil {
		return nil, err
	}
	actor.configureRuntime()
	return actor, nil
}

// Start launches the actor goroutine.
func (a *SessionActor) Start() {
	if a == nil {
		return
	}
	a.startOnce.Do(func() {
		go a.run()
	})
}

// Stop terminates the actor loop.
func (a *SessionActor) Stop() {
	if a == nil {
		return
	}
	a.Start()
	a.stopOnce.Do(func() {
		close(a.stop)
		a.cancelActive()
	})
	<-a.done
}

// SubmitPrompt submits a prompt and waits for the result.
func (a *SessionActor) SubmitPrompt(ctx context.Context, prompt string, runMeta *team.RunMeta) (*agent.Result, error) {
	if a == nil {
		return nil, fmt.Errorf("session actor is nil")
	}
	a.Start()
	reply := make(chan SubmitResult, 1)
	cmd := SubmitPrompt{
		Ctx:     ctx,
		Prompt:  prompt,
		RunMeta: runMeta.Clone(),
		Reply:   reply,
	}
	if err := a.send(ctx, cmd); err != nil {
		return nil, err
	}
	select {
	case res := <-reply:
		return res.Result, res.Err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// SubmitPromptAsync submits a prompt without waiting for the final result.
func (a *SessionActor) SubmitPromptAsync(ctx context.Context, prompt string, runMeta *team.RunMeta) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	a.Start()
	reply := make(chan SubmitResult, 1)
	cmd := SubmitPrompt{
		Ctx:     ctx,
		Prompt:  prompt,
		RunMeta: runMeta.Clone(),
		Reply:   reply,
	}
	if err := a.send(ctx, cmd); err != nil {
		return err
	}
	go func() {
		<-reply
	}()
	return nil
}

// ApproveTool resolves a pending approval request.
func (a *SessionActor) ApproveTool(ctx context.Context, requestID string, allow bool) error {
	return a.ApproveToolWithArgs(ctx, requestID, allow, nil)
}

// ApproveToolWithArgs resolves a pending approval request with optional patched args.
func (a *SessionActor) ApproveToolWithArgs(ctx context.Context, requestID string, allow bool, patchedArgs json.RawMessage) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	a.Start()
	reply := make(chan error, 1)
	cmd := ApproveTool{
		Ctx:       ctx,
		RequestID: requestID,
		Allow:     allow,
		PatchedArgs: func() json.RawMessage {
			if len(patchedArgs) == 0 {
				return nil
			}
			return append(json.RawMessage(nil), patchedArgs...)
		}(),
		Reply: reply,
	}
	if err := a.send(ctx, cmd); err != nil {
		return err
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// AnswerQuestion resolves a pending question request.
func (a *SessionActor) AnswerQuestion(ctx context.Context, questionID, answer string) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	a.Start()
	reply := make(chan error, 1)
	cmd := AnswerQuestion{
		Ctx:        ctx,
		QuestionID: questionID,
		Answer:     answer,
		Reply:      reply,
	}
	if err := a.send(ctx, cmd); err != nil {
		return err
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Interrupt cancels an active execution.
func (a *SessionActor) Interrupt(ctx context.Context) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	a.Start()
	reply := make(chan error, 1)
	cmd := Interrupt{Ctx: ctx, Reply: reply}
	if err := a.send(ctx, cmd); err != nil {
		return err
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RewindTo requests a rewind to a checkpoint.
func (a *SessionActor) RewindTo(ctx context.Context, checkpointID, mode string) error {
	_, err := a.Rewind(ctx, checkpointID, mode)
	return err
}

// Rewind requests a rewind to a checkpoint and returns the restore result.
func (a *SessionActor) Rewind(ctx context.Context, checkpointID, mode string) (*checkpoint.RestoreResult, error) {
	if a == nil {
		return nil, fmt.Errorf("session actor is nil")
	}
	a.Start()
	reply := make(chan RewindResult, 1)
	cmd := RewindTo{Ctx: ctx, CheckpointID: checkpointID, Mode: mode, Reply: reply}
	if err := a.send(ctx, cmd); err != nil {
		return nil, err
	}
	select {
	case res := <-reply:
		return res.Result, res.Err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// PreviewCheckpoint returns a checkpoint restore preview without applying it.
func (a *SessionActor) PreviewCheckpoint(ctx context.Context, checkpointID, mode string) (*checkpoint.RestoreResult, error) {
	if a == nil {
		return nil, fmt.Errorf("session actor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	checkpointID = strings.TrimSpace(checkpointID)
	if checkpointID == "" {
		return nil, fmt.Errorf("checkpoint id is required")
	}
	checkpointMgr := a.agent.GetCheckpointManager()
	if checkpointMgr == nil {
		return nil, fmt.Errorf("checkpoint manager is not configured")
	}
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = string(checkpoint.RestoreCode)
	}
	return checkpointMgr.Restore(ctx, checkpoint.RestoreRequest{
		SessionID:    a.id,
		CheckpointID: checkpointID,
		Mode:         checkpoint.RestoreMode(mode),
		PreviewOnly:  true,
	})
}

// ListCheckpoints returns checkpoints associated with this session.
func (a *SessionActor) ListCheckpoints(ctx context.Context, limit, offset int) ([]artifact.Checkpoint, error) {
	if a == nil {
		return nil, fmt.Errorf("session actor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	store := a.agent.GetArtifactStore()
	if store == nil {
		return nil, fmt.Errorf("artifact store is not configured")
	}
	return store.ListCheckpoints(ctx, a.id, limit, offset)
}

// GetCheckpointFiles returns file metadata for a checkpoint.
func (a *SessionActor) GetCheckpointFiles(ctx context.Context, checkpointID string) ([]artifact.CheckpointFile, error) {
	if a == nil {
		return nil, fmt.Errorf("session actor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	checkpointID = strings.TrimSpace(checkpointID)
	if checkpointID == "" {
		return nil, fmt.Errorf("checkpoint id is required")
	}
	store := a.agent.GetArtifactStore()
	if store == nil {
		return nil, fmt.Errorf("artifact store is not configured")
	}
	checkpoint, err := store.GetCheckpoint(ctx, checkpointID)
	if err != nil {
		return nil, err
	}
	if checkpoint == nil {
		return nil, fmt.Errorf("checkpoint not found: %s", checkpointID)
	}
	if strings.TrimSpace(checkpoint.SessionID) != "" && strings.TrimSpace(checkpoint.SessionID) != strings.TrimSpace(a.id) {
		return nil, fmt.Errorf("checkpoint does not belong to session")
	}
	return store.GetCheckpointFiles(ctx, checkpointID)
}

// DeliverMailboxMessage notifies the actor of a mailbox message.
func (a *SessionActor) DeliverMailboxMessage(ctx context.Context, message team.MailMessage) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	a.Start()
	reply := make(chan error, 1)
	cmd := DeliverMailboxMessage{Ctx: ctx, Message: message, Reply: reply}
	if err := a.send(ctx, cmd); err != nil {
		return err
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SubscribeEvents wires a channel to the actor's event bus.
func (a *SessionActor) SubscribeEvents(ctx context.Context, eventType string, ch chan runtimeevents.Event) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	a.Start()
	reply := make(chan error, 1)
	cmd := SubscribeEvents{Ctx: ctx, EventType: eventType, Ch: ch, Reply: reply}
	if err := a.send(ctx, cmd); err != nil {
		return err
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// State returns the current runtime state snapshot.
func (a *SessionActor) State() *RuntimeState {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.state == nil {
		return nil
	}
	return a.state.Clone()
}

func (a *SessionActor) run() {
	defer close(a.done)
	for {
		select {
		case cmd := <-a.cmdCh:
			a.handle(cmd)
		case <-a.stop:
			a.cancelActive()
			a.activeRunWG.Wait()
			return
		}
	}
}

func (a *SessionActor) handle(cmd Command) {
	switch payload := cmd.(type) {
	case SubmitPrompt:
		a.handleSubmitPrompt(payload)
	case ApproveTool:
		a.handleApproveTool(payload)
	case AnswerQuestion:
		a.handleAnswerQuestion(payload)
	case Interrupt:
		a.handleInterrupt(payload)
	case RewindTo:
		a.handleRewindTo(payload)
	case DeliverMailboxMessage:
		a.handleDeliverMailboxMessage(payload)
	case SubscribeEvents:
		a.handleSubscribeEvents(payload)
	}
}

func (a *SessionActor) handleSubmitPrompt(cmd SubmitPrompt) {
	reply := cmd.Reply
	if reply == nil {
		return
	}
	ctx := cmd.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	prompt := strings.TrimSpace(cmd.Prompt)
	if prompt == "" {
		reply <- SubmitResult{Err: fmt.Errorf("prompt is empty")}
		return
	}
	if err := a.ensureReady(); err != nil {
		reply <- SubmitResult{Err: err}
		return
	}
	session, err := a.loadSession(ctx)
	if err != nil {
		reply <- SubmitResult{Err: err}
		return
	}
	if last := session.LastMessage(); last == nil || last.Role != "user" || last.Content != prompt {
		session.AddMessage(*runtimetypes.NewUserMessage(prompt))
		if err := a.persistSession(ctx, session); err != nil {
			reply <- SubmitResult{Err: err}
			return
		}
	}

	turnID := "turn_" + uuid.NewString()
	_ = a.updateState(ctx, func(state *RuntimeState) error {
		state.Status = SessionRunning
		state.CurrentTurnID = turnID
		state.CurrentRunMeta = cmd.RunMeta.Clone()
		state.UpdatedAt = time.Now().UTC()
		return nil
	})
	a.startSessionRun(ctx, session, prompt, false, turnID, cmd.RunMeta, reply)
}

func (a *SessionActor) handleApproveTool(cmd ApproveTool) {
	if cmd.Reply == nil {
		return
	}
	ctx := cmd.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	state := a.State()
	if state == nil || state.PendingApproval == nil || state.PendingApproval.ID != cmd.RequestID {
		cmd.Reply <- fmt.Errorf("approval request not found")
		return
	}
	if a.resolveApproval(cmd.RequestID, runtimepolicy.ApprovalResponse{
		Allowed:     cmd.Allow,
		PatchedArgs: cmd.PatchedArgs,
	}) {
		if err := a.updateState(ctx, func(state *RuntimeState) error {
			if state.PendingApproval == nil || state.PendingApproval.ID != cmd.RequestID {
				return fmt.Errorf("approval request not found")
			}
			state.PendingApproval = nil
			state.PendingTool = nil
			state.Status = SessionRunning
			state.UpdatedAt = time.Now().UTC()
			return nil
		}); err != nil {
			cmd.Reply <- err
			return
		}
	} else if !cmd.Allow {
		if err := a.resumePendingToolWithResult(ctx, state, nil, "approval_denied"); err != nil {
			cmd.Reply <- err
			return
		}
	} else {
		if err := a.resumeApprovedPendingTool(ctx, state, cmd.PatchedArgs); err != nil {
			cmd.Reply <- err
			return
		}
	}
	a.publish(runtimeevents.Event{
		Type:      EventApprovalResolved,
		SessionID: a.id,
		Payload: map[string]interface{}{
			"request_id": cmd.RequestID,
			"allowed":    cmd.Allow,
		},
	})
	cmd.Reply <- nil
}

func (a *SessionActor) handleAnswerQuestion(cmd AnswerQuestion) {
	if cmd.Reply == nil {
		return
	}
	ctx := cmd.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	state := a.State()
	if state == nil || state.PendingQuestion == nil || state.PendingQuestion.ID != cmd.QuestionID {
		cmd.Reply <- fmt.Errorf("question request not found")
		return
	}
	if a.resolveQuestion(cmd.QuestionID, cmd.Answer) {
		if err := a.updateState(ctx, func(state *RuntimeState) error {
			if state.PendingQuestion == nil || state.PendingQuestion.ID != cmd.QuestionID {
				return fmt.Errorf("question request not found")
			}
			state.PendingQuestion = nil
			state.PendingTool = nil
			state.Status = SessionRunning
			state.UpdatedAt = time.Now().UTC()
			return nil
		}); err != nil {
			cmd.Reply <- err
			return
		}
	} else {
		result := toolbroker.AskUserQuestionResult{
			QuestionID: cmd.QuestionID,
			Answer:     cmd.Answer,
		}
		if err := a.resumePendingToolWithResult(ctx, state, result, ""); err != nil {
			cmd.Reply <- err
			return
		}
	}
	a.publish(runtimeevents.Event{
		Type:      EventQuestionAnswered,
		SessionID: a.id,
		Payload: map[string]interface{}{
			"question_id": cmd.QuestionID,
			"answer":      cmd.Answer,
		},
	})
	cmd.Reply <- nil
}

func (a *SessionActor) handleInterrupt(cmd Interrupt) {
	if cmd.Reply == nil {
		return
	}
	a.markInterrupted()
	a.cancelActive()
	_ = a.updateState(context.Background(), func(state *RuntimeState) error {
		state.Status = SessionStopped
		state.UpdatedAt = time.Now().UTC()
		return nil
	})
	a.publish(runtimeevents.Event{
		Type:      EventSessionInterrupted,
		SessionID: a.id,
		Payload: map[string]interface{}{
			"reason": "interrupt",
		},
	})
	cmd.Reply <- nil
}

func (a *SessionActor) markInterrupted() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.interrupted = true
}

func (a *SessionActor) consumeInterrupted() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.interrupted {
		return false
	}
	a.interrupted = false
	return true
}

func (a *SessionActor) handleRewindTo(cmd RewindTo) {
	if cmd.Reply == nil {
		return
	}
	ctx := cmd.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	_ = a.updateState(ctx, func(state *RuntimeState) error {
		state.Status = SessionRewinding
		state.CurrentCheckpointID = cmd.CheckpointID
		state.UpdatedAt = time.Now().UTC()
		return nil
	})
	a.publish(runtimeevents.Event{
		Type:      EventRewindStarted,
		SessionID: a.id,
		Payload: map[string]interface{}{
			"checkpoint_id": cmd.CheckpointID,
			"mode":          cmd.Mode,
		},
	})

	checkpointMgr := a.agent.GetCheckpointManager()
	if checkpointMgr == nil {
		cmd.Reply <- RewindResult{Err: fmt.Errorf("checkpoint manager is not configured")}
		return
	}

	mode := strings.ToLower(strings.TrimSpace(cmd.Mode))
	var (
		result *checkpoint.RestoreResult
		err    error
	)
	restoreMode := checkpoint.RestoreMode(mode)
	if mode == "" {
		restoreMode = checkpoint.RestoreCode
	}
	switch restoreMode {
	case checkpoint.RestoreCode, checkpoint.RestoreConversation, checkpoint.RestoreBoth:
		result, err = checkpointMgr.Restore(ctx, checkpoint.RestoreRequest{
			SessionID:    a.id,
			CheckpointID: cmd.CheckpointID,
			Mode:         restoreMode,
		})
		if err == nil && result != nil && result.ConversationChanged {
			err = a.applyConversationRestore(ctx, result)
		}
	default:
		err = fmt.Errorf("unsupported rewind mode: %s", cmd.Mode)
	}
	status := SessionIdle
	if err != nil {
		status = SessionStopped
	}
	_ = a.updateState(ctx, func(state *RuntimeState) error {
		state.Status = status
		state.UpdatedAt = time.Now().UTC()
		return nil
	})
	payload := map[string]interface{}{
		"checkpoint_id": cmd.CheckpointID,
		"mode":          cmd.Mode,
		"error":         errorString(err),
	}
	if result != nil {
		payload["applied_paths"] = result.AppliedPaths
		payload["errors"] = result.Errors
		payload["conversation_changed"] = result.ConversationChanged
		payload["conversation_head"] = result.ConversationHead
		payload["conversation_exact"] = result.ConversationExact
	}
	a.publish(runtimeevents.Event{
		Type:      EventRewindFinished,
		SessionID: a.id,
		Payload:   payload,
	})
	if hookMgr := a.agent.GetHookManager(); hookMgr != nil {
		hookPayload := map[string]interface{}{
			"session_id":    a.id,
			"checkpoint_id": cmd.CheckpointID,
			"mode":          cmd.Mode,
			"error":         errorString(err),
		}
		if result != nil {
			hookPayload["applied_paths"] = result.AppliedPaths
			hookPayload["errors"] = result.Errors
			hookPayload["conversation_changed"] = result.ConversationChanged
			hookPayload["conversation_head"] = result.ConversationHead
			hookPayload["conversation_exact"] = result.ConversationExact
		}
		hookMgr.DispatchAsync(ctx, runtimehooks.EventRewindCompleted, hookPayload)
	}
	cmd.Reply <- RewindResult{Result: result, Err: err}
}

func (a *SessionActor) applyConversationRestore(ctx context.Context, restore *checkpoint.RestoreResult) error {
	if restore == nil {
		return fmt.Errorf("restore result is nil")
	}
	if restore.ConversationExact {
		return a.applyConversationSnapshot(ctx, restore.ConversationMessages)
	}
	return a.applyConversationPrefix(ctx, restore.ConversationHead)
}

func (a *SessionActor) applyConversationSnapshot(ctx context.Context, messages []runtimetypes.Message) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := a.loadSession(ctx)
	if err != nil {
		return err
	}
	session.ReplaceHistory(messages)
	session.SetHeadOffset(0)
	if err := a.persistSession(ctx, session); err != nil {
		return err
	}
	_ = a.updateState(ctx, func(state *RuntimeState) error {
		state.HeadOffset = 0
		state.UpdatedAt = time.Now().UTC()
		return nil
	})
	return nil
}

func (a *SessionActor) applyConversationPrefix(ctx context.Context, targetCount int) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := a.loadSession(ctx)
	if err != nil {
		return err
	}
	if targetCount < 0 {
		targetCount = 0
	}
	if targetCount > len(session.History) {
		targetCount = len(session.History)
	}
	cloned := make([]runtimetypes.Message, targetCount)
	for i := 0; i < targetCount; i++ {
		cloned[i] = *session.History[i].Clone()
	}
	session.ReplaceHistory(cloned)
	session.SetHeadOffset(0)
	if err := a.persistSession(ctx, session); err != nil {
		return err
	}
	_ = a.updateState(ctx, func(state *RuntimeState) error {
		state.HeadOffset = 0
		state.UpdatedAt = time.Now().UTC()
		return nil
	})
	return nil
}

func (a *SessionActor) handleDeliverMailboxMessage(cmd DeliverMailboxMessage) {
	if cmd.Reply == nil {
		return
	}
	message := cmd.Message
	payload := map[string]interface{}{
		"team_id":    strings.TrimSpace(message.TeamID),
		"message_id": strings.TrimSpace(message.ID),
		"from_agent": strings.TrimSpace(message.FromAgent),
		"to_agent":   strings.TrimSpace(message.ToAgent),
		"kind":       strings.TrimSpace(message.Kind),
		"body":       strings.TrimSpace(message.Body),
	}
	if payload["to_agent"] == "" {
		payload["to_agent"] = "*"
	}
	if payload["kind"] == "" {
		payload["kind"] = "info"
	}
	if message.TaskID != nil && strings.TrimSpace(*message.TaskID) != "" {
		payload["task_id"] = strings.TrimSpace(*message.TaskID)
	}
	if !message.CreatedAt.IsZero() {
		payload["created_at"] = message.CreatedAt.UTC()
	}
	if len(message.Metadata) > 0 {
		metadata := make(map[string]interface{}, len(message.Metadata))
		for key, value := range message.Metadata {
			metadata[key] = value
		}
		payload["metadata"] = metadata
	}
	a.publish(runtimeevents.Event{
		Type:      EventMailboxReceived,
		SessionID: a.id,
		Payload:   payload,
	})
	cmd.Reply <- nil
}

func (a *SessionActor) handleSubscribeEvents(cmd SubscribeEvents) {
	if cmd.Reply == nil {
		return
	}
	if a.eventBus == nil {
		cmd.Reply <- fmt.Errorf("event bus is not configured")
		return
	}
	if cmd.Ch == nil {
		cmd.Reply <- fmt.Errorf("event channel is required")
		return
	}
	handler := func(event runtimeevents.Event) {
		select {
		case cmd.Ch <- event:
		default:
		}
	}
	a.eventBus.Subscribe(cmd.EventType, handler)
	cmd.Reply <- nil
}

func (a *SessionActor) send(ctx context.Context, cmd Command) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case a.cmdCh <- cmd:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *SessionActor) ensureReady() error {
	state := a.State()
	if state == nil {
		return nil
	}
	switch state.Status {
	case SessionRunning, SessionWaitingApproval, SessionWaitingInput, SessionRewinding:
		return fmt.Errorf("session is busy (%s)", state.Status)
	default:
		return nil
	}
}

func (a *SessionActor) runLoop(ctx context.Context, prompt string, session *Session) (*agent.Result, error) {
	if a.agent == nil {
		return nil, fmt.Errorf("agent is not configured")
	}
	if result, matched, err := a.tryRouteSkill(ctx, prompt, session); matched || err != nil {
		return result, err
	}
	if a.llmRuntime == nil {
		return nil, fmt.Errorf("llm runtime is not configured")
	}
	loop := agent.NewReActLoop(a.agent, a.llmRuntime, a.loopConfig)
	return loop.RunWithSession(ctx, prompt, session)
}

func (a *SessionActor) continueLoop(ctx context.Context, session *Session) (*agent.Result, error) {
	if a.agent == nil {
		return nil, fmt.Errorf("agent is not configured")
	}
	if a.llmRuntime == nil {
		return nil, fmt.Errorf("llm runtime is not configured")
	}
	loop := agent.NewReActLoop(a.agent, a.llmRuntime, a.loopConfig)
	return loop.ContinueWithSession(ctx, session)
}

func (a *SessionActor) tryRouteSkill(ctx context.Context, prompt string, session *Session) (*agent.Result, bool, error) {
	if a == nil || a.agent == nil {
		return nil, false, nil
	}
	if runMeta, ok := team.GetRunMeta(ctx); ok && runMeta != nil && runMeta.Team != nil && strings.TrimSpace(runMeta.Team.TeamID) != "" {
		return nil, false, nil
	}
	router := a.agent.GetSkillRouter()
	executor := a.agent.GetSkillExecutor()
	if router == nil || executor == nil {
		return nil, false, nil
	}
	routes := router.Route(ctx, prompt)
	if len(routes) == 0 || routes[0] == nil || routes[0].Skill == nil {
		return nil, false, nil
	}
	req := runtimetypes.NewRequest(prompt)
	req.History = routeHistoryForSkillPrompt(session, prompt)
	req.Metadata.Set("permissions", []string{"*"})
	skillResult, err := executor.Execute(ctx, routes[0].Skill, req)
	if err != nil {
		return &agent.Result{
			Success:  false,
			Output:   "",
			Skill:    routes[0].Skill.Name,
			Duration: req.Duration,
			Error:    err.Error(),
		}, true, err
	}
	if skillResult == nil {
		return &agent.Result{
			Success:  false,
			Output:   "",
			Skill:    routes[0].Skill.Name,
			Duration: req.Duration,
			Error:    "skill execution returned nil result",
		}, true, fmt.Errorf("skill execution returned nil result")
	}
	if skillResult.Success {
		session.AddMessage(*runtimetypes.NewAssistantMessage(skillResult.Output))
	}
	req.MarkCompleted()
	return &agent.Result{
		Success:      skillResult.Success,
		Output:       skillResult.Output,
		Observations: skillResult.Observations,
		Skill:        routes[0].Skill.Name,
		Usage:        skillResult.Usage,
		Duration:     req.Duration,
		Error:        skillResult.Error,
	}, true, nil
}

func routeHistoryForSkillPrompt(session *Session, prompt string) []runtimetypes.Message {
	if session == nil {
		return nil
	}
	history := session.GetMessages()
	if len(history) == 0 {
		return nil
	}
	last := history[len(history)-1]
	if last.Role == "user" && last.Content == prompt {
		history = history[:len(history)-1]
	}
	cloned := make([]runtimetypes.Message, len(history))
	for i := range history {
		cloned[i] = *history[i].Clone()
	}
	return cloned
}

func (a *SessionActor) startSessionRun(ctx context.Context, session *Session, prompt string, resume bool, turnID string, runMeta *team.RunMeta, reply chan SubmitResult) {
	if a == nil || session == nil {
		if reply != nil {
			reply <- SubmitResult{Err: fmt.Errorf("session is nil")}
		}
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(turnID) == "" {
		turnID = "turn_" + uuid.NewString()
	}

	if hookMgr := a.agent.GetHookManager(); hookMgr != nil {
		hookPayload := map[string]interface{}{
			"session_id": a.id,
			"turn_id":    turnID,
			"resume":     resume,
		}
		if !resume {
			hookPayload["prompt"] = prompt
			hookMgr.DispatchAsync(ctx, runtimehooks.EventUserPromptSubmit, hookPayload)
		}
		hookMgr.DispatchAsync(ctx, runtimehooks.EventSessionStart, hookPayload)
	}
	startPayload := map[string]interface{}{
		"turn_id": turnID,
		"resume":  resume,
	}
	if !resume {
		startPayload["prompt_length"] = len(prompt)
	}
	a.publish(runtimeevents.Event{
		Type:      EventSessionStart,
		SessionID: a.id,
		TraceID:   turnID,
		Payload:   startPayload,
	})

	runCtx, cancel := context.WithCancel(ctx)
	runCtx = team.WithRunMeta(runCtx, runMeta)
	a.setActiveCancel(cancel)
	a.activeRunWG.Add(1)
	go func() {
		defer a.activeRunWG.Done()
		var (
			result  *agent.Result
			execErr error
		)
		if resume {
			result, execErr = a.continueLoop(runCtx, session)
		} else {
			result, execErr = a.runLoop(runCtx, prompt, session)
		}
		cancel()
		a.clearActiveCancel()

		status := SessionIdle
		if ctx.Err() != nil || a.consumeInterrupted() {
			status = SessionStopped
		}
		_ = a.updateState(context.Background(), func(state *RuntimeState) error {
			state.Status = status
			state.CurrentTurnID = ""
			state.CurrentRunMeta = nil
			state.PendingTool = nil
			state.UpdatedAt = time.Now().UTC()
			return nil
		})
		if persistErr := a.persistSession(context.Background(), session); persistErr != nil && execErr == nil {
			execErr = persistErr
		}

		payload := map[string]interface{}{
			"turn_id":  turnID,
			"resume":   resume,
			"success":  execErr == nil && result != nil && result.Success,
			"steps":    resultSteps(result),
			"error":    errorString(execErr),
			"duration": durationMillis(result),
		}
		if result != nil {
			payload["trace_id"] = result.TraceID
		}
		if hookMgr := a.agent.GetHookManager(); hookMgr != nil {
			hookPayload := map[string]interface{}{
				"session_id": a.id,
				"turn_id":    turnID,
				"resume":     resume,
				"success":    execErr == nil && result != nil && result.Success,
				"error":      errorString(execErr),
			}
			if result != nil {
				hookPayload["trace_id"] = result.TraceID
			}
			hookMgr.DispatchAsync(ctx, runtimehooks.EventSessionEnd, hookPayload)
		}
		a.publish(runtimeevents.Event{
			Type:      EventSessionEnd,
			SessionID: a.id,
			TraceID:   resultTraceID(result, turnID),
			Payload:   payload,
		})
		if result != nil && strings.TrimSpace(result.Output) != "" {
			a.publish(runtimeevents.Event{
				Type:      EventAssistantMessage,
				SessionID: a.id,
				TraceID:   resultTraceID(result, turnID),
				Payload: map[string]interface{}{
					"turn_id": turnID,
					"content": result.Output,
				},
			})
		}
		if reply != nil {
			reply <- SubmitResult{Result: result, Err: execErr}
		}
	}()
}

func (a *SessionActor) loadSession(ctx context.Context) (*Session, error) {
	if a.sessionStore == nil {
		return nil, fmt.Errorf("session store is not configured")
	}
	session, err := a.sessionStore.Load(ctx, a.id)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, fmt.Errorf("session not found: %s", a.id)
	}
	return session, nil
}

func (a *SessionActor) persistSession(ctx context.Context, session *Session) error {
	if session == nil || a.sessionStore == nil {
		return nil
	}
	return a.sessionStore.Update(ctx, session)
}

func (a *SessionActor) loadState(ctx context.Context) error {
	if a.stateStore == nil {
		if a.state == nil {
			a.state = &RuntimeState{
				SessionID: a.id,
				Status:    SessionIdle,
				UpdatedAt: time.Now().UTC(),
			}
		}
		return nil
	}
	state, err := a.stateStore.LoadState(ctx, a.id)
	if err != nil {
		return err
	}
	if state == nil {
		state = &RuntimeState{
			SessionID: a.id,
			Status:    SessionIdle,
			UpdatedAt: time.Now().UTC(),
		}
		_ = a.stateStore.SaveState(ctx, state)
	}
	a.state = state
	return nil
}

func (a *SessionActor) updateState(ctx context.Context, mutate func(*RuntimeState) error) error {
	if a == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.state == nil {
		a.state = &RuntimeState{SessionID: a.id, Status: SessionIdle}
	}
	if mutate != nil {
		if err := mutate(a.state); err != nil {
			return err
		}
	}
	if a.state.UpdatedAt.IsZero() {
		a.state.UpdatedAt = time.Now().UTC()
	}
	if a.stateStore != nil {
		return a.stateStore.SaveState(ctx, a.state)
	}
	return nil
}

func (a *SessionActor) recordPendingToolCall(ctx context.Context, pending *PendingToolInvocation) error {
	if a == nil || pending == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := a.loadSession(ctx)
	if err != nil {
		return err
	}
	if sessionHasToolCall(session, pending.ToolCallID) {
		return nil
	}
	assistant := runtimetypes.NewAssistantMessage("")
	assistant.ToolCalls = []runtimetypes.ToolCall{{
		ID:   pending.ToolCallID,
		Name: pending.ToolName,
		Args: decodePendingToolArgs(pending.ArgsJSON),
	}}
	session.AddMessage(*assistant)
	return a.persistSession(ctx, session)
}

func (a *SessionActor) resumePendingToolWithResult(ctx context.Context, state *RuntimeState, content interface{}, toolErr string) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	if state == nil || state.PendingTool == nil {
		return fmt.Errorf("pending tool not found")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := a.loadSession(ctx)
	if err != nil {
		return err
	}
	if !sessionHasToolCall(session, state.PendingTool.ToolCallID) {
		if err := a.recordPendingToolCall(ctx, state.PendingTool); err != nil {
			return err
		}
		session, err = a.loadSession(ctx)
		if err != nil {
			return err
		}
	}
	if sessionHasToolResult(session, state.PendingTool.ToolCallID) {
		return a.resumeFromPersistedToolResult(ctx, state, session)
	}
	toolMessage, err := a.buildPendingToolResultMessage(ctx, state.PendingTool, content, toolErr)
	if err != nil {
		return err
	}
	session.AddMessage(*toolMessage)
	if err := a.persistSession(ctx, session); err != nil {
		return err
	}
	runMeta := state.CurrentRunMeta.Clone()
	turnID := strings.TrimSpace(state.CurrentTurnID)
	if err := a.updateState(ctx, func(runtimeState *RuntimeState) error {
		if runtimeState.PendingTool == nil || runtimeState.PendingTool.ToolCallID != state.PendingTool.ToolCallID {
			return fmt.Errorf("pending tool changed while resuming")
		}
		runtimeState.PendingTool = nil
		runtimeState.PendingApproval = nil
		runtimeState.PendingQuestion = nil
		runtimeState.Status = SessionRunning
		runtimeState.UpdatedAt = time.Now().UTC()
		return nil
	}); err != nil {
		return err
	}
	a.startSessionRun(ctx, session, "", true, turnID, runMeta, nil)
	return nil
}

func (a *SessionActor) resumeFromPersistedToolResult(ctx context.Context, state *RuntimeState, session *Session) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	if state == nil || state.PendingTool == nil {
		return fmt.Errorf("pending tool not found")
	}
	if session == nil {
		return fmt.Errorf("session is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runMeta := state.CurrentRunMeta.Clone()
	turnID := strings.TrimSpace(state.CurrentTurnID)
	if err := a.updateState(ctx, func(runtimeState *RuntimeState) error {
		if runtimeState.PendingTool == nil || runtimeState.PendingTool.ToolCallID != state.PendingTool.ToolCallID {
			return fmt.Errorf("pending tool changed while resuming")
		}
		runtimeState.PendingTool = nil
		runtimeState.PendingApproval = nil
		runtimeState.PendingQuestion = nil
		runtimeState.Status = SessionRunning
		runtimeState.UpdatedAt = time.Now().UTC()
		return nil
	}); err != nil {
		return err
	}
	a.deleteStoredToolReceipt(context.Background(), a.id, state.PendingTool.ToolCallID)
	a.startSessionRun(ctx, session, "", true, turnID, runMeta, nil)
	return nil
}

func (a *SessionActor) appendPendingToolReceiptToSession(ctx context.Context, pending *PendingToolInvocation, session *Session) (*Session, error) {
	if a == nil {
		return nil, fmt.Errorf("session actor is nil")
	}
	if pending == nil {
		return nil, fmt.Errorf("pending tool not found")
	}
	if session == nil {
		return nil, fmt.Errorf("session is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if sessionHasToolResult(session, pending.ToolCallID) {
		return session, nil
	}
	message, ok := decodePendingToolResultMessage(pending.ResultMessageJSON)
	if !ok || message == nil {
		return nil, fmt.Errorf("pending tool result receipt not found")
	}
	session.AddMessage(*message)
	if err := a.persistSession(ctx, session); err != nil {
		return nil, err
	}
	return session, nil
}

func (a *SessionActor) resumeApprovedPendingTool(ctx context.Context, state *RuntimeState, patchedArgs json.RawMessage) error {
	if a == nil {
		return fmt.Errorf("session actor is nil")
	}
	if state == nil || state.PendingTool == nil {
		return fmt.Errorf("pending tool not found")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, err := a.loadSession(ctx)
	if err != nil {
		return err
	}
	pending := *state.PendingTool
	if len(patchedArgs) > 0 {
		pending.ArgsJSON = append(json.RawMessage(nil), patchedArgs...)
	}
	if !sessionHasToolCall(session, pending.ToolCallID) {
		if err := a.recordPendingToolCall(ctx, &pending); err != nil {
			return err
		}
		session, err = a.loadSession(ctx)
		if err != nil {
			return err
		}
	}
	if sessionHasToolResult(session, pending.ToolCallID) {
		return a.resumeFromPersistedToolResult(ctx, state, session)
	}
	if receipt, message, ok, err := a.loadStoredToolReceipt(ctx, a.id, pending.ToolCallID); err != nil {
		return err
	} else if ok && message != nil {
		session.AddMessage(*message)
		if err := a.persistSession(ctx, session); err != nil {
			return err
		}
		a.publishToolReceiptEvent(EventToolReceiptReplayed, strings.TrimSpace(state.CurrentTurnID), "receipt_store", *receipt)
		return a.resumeFromPersistedToolResult(ctx, state, session)
	}
	if strings.TrimSpace(pending.ExecutionState) == PendingToolExecutionCompleted {
		session, err = a.appendPendingToolReceiptToSession(ctx, &pending, session)
		if err != nil {
			return err
		}
		a.publishToolReceiptEvent(EventToolReceiptReplayed, strings.TrimSpace(state.CurrentTurnID), "runtime_state", toolExecutionReceiptFromPending(a.id, &pending))
		return a.resumeFromPersistedToolResult(ctx, state, session)
	}
	if strings.TrimSpace(pending.ExecutionState) == PendingToolExecutionStarted {
		toolMessage, err := a.buildPendingToolResultMessage(ctx, &pending, nil, "approved tool execution may have completed before interruption; verify side effects before retrying")
		if err != nil {
			return err
		}
		session.AddMessage(*toolMessage)
		if err := a.persistSession(ctx, session); err != nil {
			return err
		}
		return a.resumeFromPersistedToolResult(ctx, state, session)
	}
	if err := a.updateState(ctx, func(runtimeState *RuntimeState) error {
		if runtimeState.PendingTool == nil || runtimeState.PendingTool.ToolCallID != state.PendingTool.ToolCallID {
			return fmt.Errorf("pending tool changed while resuming")
		}
		if len(patchedArgs) > 0 {
			runtimeState.PendingTool.ArgsJSON = append(json.RawMessage(nil), patchedArgs...)
		}
		runtimeState.PendingTool.ExecutionState = PendingToolExecutionStarted
		runtimeState.PendingTool.ExecutionStartedAt = time.Now().UTC()
		runtimeState.UpdatedAt = time.Now().UTC()
		return nil
	}); err != nil {
		return err
	}
	runMeta := state.CurrentRunMeta.Clone()
	runCtx := team.WithRunMeta(ctx, runMeta)
	message, err := a.agent.ExecuteApprovedToolCall(runCtx, a.id, runtimetypes.ToolCall{
		ID:   pending.ToolCallID,
		Name: pending.ToolName,
		Args: decodePendingToolArgs(pending.ArgsJSON),
	}, session.GetMessages())
	if err != nil {
		return err
	}
	receipt, err := encodePendingToolResultMessage(message)
	if err != nil {
		return err
	}
	storedReceipt := newToolExecutionReceipt(a.id, pending.ToolCallID, pending.ToolName, receipt, time.Now().UTC())
	if err := a.saveStoredToolReceipt(ctx, storedReceipt); err != nil {
		return err
	}
	a.publishToolReceiptEvent(EventToolReceiptRecorded, strings.TrimSpace(state.CurrentTurnID), "receipt_store", storedReceipt)
	if err := a.updateState(ctx, func(runtimeState *RuntimeState) error {
		if runtimeState.PendingTool == nil || runtimeState.PendingTool.ToolCallID != state.PendingTool.ToolCallID {
			return fmt.Errorf("pending tool changed while storing receipt")
		}
		runtimeState.PendingTool.ResultMessageJSON = receipt
		runtimeState.PendingTool.ExecutionState = PendingToolExecutionCompleted
		runtimeState.PendingTool.ExecutionCompletedAt = time.Now().UTC()
		runtimeState.UpdatedAt = time.Now().UTC()
		return nil
	}); err != nil {
		return err
	}
	session.AddMessage(*message)
	if err := a.persistSession(ctx, session); err != nil {
		return err
	}
	turnID := strings.TrimSpace(state.CurrentTurnID)
	if err := a.updateState(ctx, func(runtimeState *RuntimeState) error {
		if runtimeState.PendingTool == nil || runtimeState.PendingTool.ToolCallID != state.PendingTool.ToolCallID {
			return fmt.Errorf("pending tool changed while resuming")
		}
		runtimeState.PendingTool = nil
		runtimeState.PendingApproval = nil
		runtimeState.PendingQuestion = nil
		runtimeState.Status = SessionRunning
		runtimeState.UpdatedAt = time.Now().UTC()
		return nil
	}); err != nil {
		return err
	}
	a.deleteStoredToolReceipt(context.Background(), a.id, pending.ToolCallID)
	a.startSessionRun(ctx, session, "", true, turnID, runMeta, nil)
	return nil
}

func (a *SessionActor) buildPendingToolResultMessage(ctx context.Context, pending *PendingToolInvocation, content interface{}, toolErr string) (*runtimetypes.Message, error) {
	if a == nil || pending == nil {
		return nil, fmt.Errorf("pending tool is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	gateway := a.agent.GetOutputGateway()
	envelope, gatewayErr := gateway.Process(ctx, runtimeoutput.RawToolResult{
		SessionID:  a.id,
		ToolName:   pending.ToolName,
		ToolCallID: pending.ToolCallID,
		Content:    content,
		Error:      toolErr,
	})
	message := runtimetypes.NewToolMessage(pending.ToolCallID, "")
	if envelope != nil {
		message.Content = envelope.Render()
		if len(envelope.Metadata) > 0 {
			message.Metadata = runtimetypes.NewMetadata()
			for key, value := range envelope.Metadata {
				message.Metadata[key] = value
			}
		}
	}
	if strings.TrimSpace(message.Content) == "" && strings.TrimSpace(toolErr) != "" {
		message.Content = "Tool execution failed: " + strings.TrimSpace(toolErr)
	}
	if gatewayErr != nil && message.Metadata != nil {
		message.Metadata["gateway_error"] = gatewayErr.Error()
	}
	return message, nil
}

func (a *SessionActor) setActiveCancel(cancel context.CancelFunc) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.activeCancel = cancel
}

func (a *SessionActor) clearActiveCancel() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.activeCancel = nil
}

func (a *SessionActor) cancelActive() {
	a.mu.Lock()
	cancel := a.activeCancel
	a.activeCancel = nil
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// RequestApproval implements runtimepolicy.ApprovalHandler for interactive approvals.
func (a *SessionActor) RequestApproval(ctx context.Context, req runtimepolicy.ApprovalRequest) (runtimepolicy.ApprovalResponse, error) {
	if a == nil {
		return runtimepolicy.ApprovalResponse{}, fmt.Errorf("session actor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(req.ID) == "" {
		req.ID = "approval_" + uuid.NewString()
	}
	if strings.TrimSpace(req.SessionID) == "" {
		req.SessionID = a.id
	}
	pendingTool, err := newPendingToolInvocation(req.ToolCallID, req.ToolName, req.ArgsJSON)
	if err != nil {
		return runtimepolicy.ApprovalResponse{}, err
	}
	if err := a.recordPendingToolCall(ctx, pendingTool); err != nil {
		return runtimepolicy.ApprovalResponse{}, err
	}
	pending := &ApprovalRequest{
		ID:         req.ID,
		SessionID:  req.SessionID,
		ToolCallID: req.ToolCallID,
		ToolName:   req.ToolName,
		ArgsJSON:   req.ArgsJSON,
		Reason:     req.Reason,
		RiskLevel:  req.RiskLevel,
		ExpiresAt:  req.ExpiresAt,
	}
	if err := a.updateState(ctx, func(state *RuntimeState) error {
		state.PendingTool = pendingTool
		state.PendingApproval = pending
		state.Status = SessionWaitingApproval
		state.UpdatedAt = time.Now().UTC()
		return nil
	}); err != nil {
		return runtimepolicy.ApprovalResponse{}, err
	}
	waiter := a.registerApprovalWaiter(req.ID)
	a.publish(runtimeevents.Event{
		Type:      EventApprovalRequested,
		SessionID: a.id,
		Payload: map[string]interface{}{
			"request_id": req.ID,
			"tool_name":  req.ToolName,
			"reason":     req.Reason,
			"risk_level": req.RiskLevel,
		},
	})
	select {
	case resp := <-waiter:
		return resp, nil
	case <-ctx.Done():
		a.unregisterApprovalWaiter(req.ID)
		_ = a.updateState(context.Background(), func(state *RuntimeState) error {
			if state.PendingApproval != nil && state.PendingApproval.ID == req.ID {
				state.PendingApproval = nil
				state.PendingTool = nil
				state.Status = SessionIdle
				state.UpdatedAt = time.Now().UTC()
			}
			return nil
		})
		return runtimepolicy.ApprovalResponse{}, ctx.Err()
	}
}

// AskUserQuestion implements toolbroker.UserInputHandler.
func (a *SessionActor) AskUserQuestion(ctx context.Context, req toolbroker.UserQuestionRequest) (string, error) {
	if a == nil {
		return "", fmt.Errorf("session actor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(req.ID) == "" {
		req.ID = "question_" + uuid.NewString()
	}
	if strings.TrimSpace(req.SessionID) == "" {
		req.SessionID = a.id
	}
	pendingTool, err := newPendingToolInvocation(req.ToolCallID, toolbroker.ToolAskUserQuestion, askUserQuestionArgsJSON(req))
	if err != nil {
		return "", err
	}
	if err := a.recordPendingToolCall(ctx, pendingTool); err != nil {
		return "", err
	}
	pending := &UserQuestionRequest{
		ID:          req.ID,
		SessionID:   req.SessionID,
		Prompt:      req.Prompt,
		Suggestions: append([]string(nil), req.Suggestions...),
		Required:    req.Required,
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   req.ExpiresAt,
	}
	if err := a.updateState(ctx, func(state *RuntimeState) error {
		state.PendingTool = pendingTool
		state.PendingQuestion = pending
		state.Status = SessionWaitingInput
		state.UpdatedAt = time.Now().UTC()
		return nil
	}); err != nil {
		return "", err
	}
	waiter := a.registerQuestionWaiter(req.ID)
	a.publish(runtimeevents.Event{
		Type:      EventQuestionAsked,
		SessionID: a.id,
		Payload: map[string]interface{}{
			"question_id": req.ID,
			"prompt":      req.Prompt,
			"required":    req.Required,
			"suggestions": req.Suggestions,
		},
	})
	select {
	case answer := <-waiter:
		return answer, nil
	case <-ctx.Done():
		a.unregisterQuestionWaiter(req.ID)
		_ = a.updateState(context.Background(), func(state *RuntimeState) error {
			if state.PendingQuestion != nil && state.PendingQuestion.ID == req.ID {
				state.PendingQuestion = nil
				state.PendingTool = nil
				state.Status = SessionIdle
				state.UpdatedAt = time.Now().UTC()
			}
			return nil
		})
		return "", ctx.Err()
	}
}

func (a *SessionActor) registerApprovalWaiter(requestID string) chan runtimepolicy.ApprovalResponse {
	a.waiterMu.Lock()
	defer a.waiterMu.Unlock()
	ch := make(chan runtimepolicy.ApprovalResponse, 1)
	a.approvalWaiters[requestID] = ch
	return ch
}

func (a *SessionActor) unregisterApprovalWaiter(requestID string) {
	a.waiterMu.Lock()
	defer a.waiterMu.Unlock()
	delete(a.approvalWaiters, requestID)
}

func (a *SessionActor) resolveApproval(requestID string, resp runtimepolicy.ApprovalResponse) bool {
	a.waiterMu.Lock()
	ch := a.approvalWaiters[requestID]
	delete(a.approvalWaiters, requestID)
	a.waiterMu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- resp:
	default:
	}
	return true
}

func (a *SessionActor) registerQuestionWaiter(questionID string) chan string {
	a.waiterMu.Lock()
	defer a.waiterMu.Unlock()
	ch := make(chan string, 1)
	a.questionWaiters[questionID] = ch
	return ch
}

func (a *SessionActor) unregisterQuestionWaiter(questionID string) {
	a.waiterMu.Lock()
	defer a.waiterMu.Unlock()
	delete(a.questionWaiters, questionID)
}

func (a *SessionActor) resolveQuestion(questionID, answer string) bool {
	a.waiterMu.Lock()
	ch := a.questionWaiters[questionID]
	delete(a.questionWaiters, questionID)
	a.waiterMu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- answer:
	default:
	}
	return true
}

func newPendingToolInvocation(toolCallID, toolName string, argsJSON json.RawMessage) (*PendingToolInvocation, error) {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return nil, fmt.Errorf("tool name is required")
	}
	toolCallID = strings.TrimSpace(toolCallID)
	if toolCallID == "" {
		toolCallID = "toolcall_pending_" + uuid.NewString()
	}
	pending := &PendingToolInvocation{
		ToolCallID: toolCallID,
		ToolName:   toolName,
		CreatedAt:  time.Now().UTC(),
	}
	if len(argsJSON) > 0 {
		pending.ArgsJSON = append(json.RawMessage(nil), argsJSON...)
	}
	return pending, nil
}

func askUserQuestionArgsJSON(req toolbroker.UserQuestionRequest) json.RawMessage {
	payload, err := json.Marshal(toolbroker.AskUserQuestionArgs{
		Prompt:      req.Prompt,
		Suggestions: append([]string(nil), req.Suggestions...),
		Required:    req.Required,
	})
	if err != nil {
		return nil
	}
	return payload
}

func decodePendingToolArgs(argsJSON json.RawMessage) map[string]interface{} {
	if len(argsJSON) == 0 {
		return map[string]interface{}{}
	}
	decoded := map[string]interface{}{}
	if err := json.Unmarshal(argsJSON, &decoded); err != nil {
		return map[string]interface{}{}
	}
	return decoded
}

func encodePendingToolResultMessage(message *runtimetypes.Message) (json.RawMessage, error) {
	if message == nil {
		return nil, fmt.Errorf("message is nil")
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func decodePendingToolResultMessage(payload json.RawMessage) (*runtimetypes.Message, bool) {
	if len(payload) == 0 {
		return nil, false
	}
	var message runtimetypes.Message
	if err := json.Unmarshal(payload, &message); err != nil {
		return nil, false
	}
	return &message, true
}

func (a *SessionActor) toolReceiptStore() ToolReceiptStore {
	if a == nil || a.stateStore == nil {
		return nil
	}
	store, _ := a.stateStore.(ToolReceiptStore)
	return store
}

func (a *SessionActor) saveStoredToolReceipt(ctx context.Context, receipt ToolExecutionReceipt) error {
	store := a.toolReceiptStore()
	if store == nil {
		return nil
	}
	return store.SaveToolReceipt(ctx, receipt)
}

func (a *SessionActor) loadStoredToolReceipt(ctx context.Context, sessionID, toolCallID string) (*ToolExecutionReceipt, *runtimetypes.Message, bool, error) {
	store := a.toolReceiptStore()
	if store == nil {
		return nil, nil, false, nil
	}
	receipt, err := store.GetToolReceipt(ctx, sessionID, toolCallID)
	if err != nil || receipt == nil {
		return nil, nil, false, err
	}
	message, ok := decodePendingToolResultMessage(receipt.MessageJSON)
	if !ok {
		return nil, nil, false, fmt.Errorf("failed to decode stored tool receipt for %s", toolCallID)
	}
	return receipt, message, true, nil
}

func (a *SessionActor) deleteStoredToolReceipt(ctx context.Context, sessionID, toolCallID string) {
	store := a.toolReceiptStore()
	if store == nil {
		return
	}
	_ = store.DeleteToolReceipt(ctx, sessionID, toolCallID)
}

func newToolExecutionReceipt(sessionID, toolCallID, toolName string, messageJSON json.RawMessage, createdAt time.Time) ToolExecutionReceipt {
	receipt := ToolExecutionReceipt{
		SessionID:  strings.TrimSpace(sessionID),
		ToolCallID: strings.TrimSpace(toolCallID),
		ToolName:   strings.TrimSpace(toolName),
		CreatedAt:  createdAt.UTC(),
	}
	if receipt.CreatedAt.IsZero() {
		receipt.CreatedAt = time.Now().UTC()
	}
	if len(messageJSON) > 0 {
		receipt.MessageJSON = append(json.RawMessage(nil), messageJSON...)
	}
	return receipt
}

func toolExecutionReceiptFromPending(sessionID string, pending *PendingToolInvocation) ToolExecutionReceipt {
	if pending == nil {
		return ToolExecutionReceipt{SessionID: strings.TrimSpace(sessionID)}
	}
	createdAt := pending.ExecutionCompletedAt
	if createdAt.IsZero() {
		createdAt = pending.CreatedAt
	}
	return newToolExecutionReceipt(sessionID, pending.ToolCallID, pending.ToolName, pending.ResultMessageJSON, createdAt)
}

func (a *SessionActor) publishToolReceiptEvent(eventType, traceID, source string, receipt ToolExecutionReceipt) {
	if a == nil {
		return
	}
	payload := map[string]interface{}{
		"tool_call_id": receipt.ToolCallID,
		"source":       strings.TrimSpace(source),
		"receipt": map[string]interface{}{
			"session_id":   receipt.SessionID,
			"tool_call_id": receipt.ToolCallID,
			"message_json": append(json.RawMessage(nil), receipt.MessageJSON...),
			"created_at":   receipt.CreatedAt,
		},
	}
	if strings.TrimSpace(receipt.ToolName) != "" {
		payload["tool_name"] = receipt.ToolName
		payload["receipt"].(map[string]interface{})["tool_name"] = receipt.ToolName
	}
	a.publish(runtimeevents.Event{
		Type:      eventType,
		SessionID: a.id,
		TraceID:   strings.TrimSpace(traceID),
		ToolName:  receipt.ToolName,
		Payload:   payload,
	})
}

func sessionHasToolCall(session *Session, toolCallID string) bool {
	if session == nil || strings.TrimSpace(toolCallID) == "" {
		return false
	}
	for _, message := range session.History {
		if toolCall, ok := message.GetToolCall(toolCallID); ok && toolCall != nil {
			return true
		}
	}
	return false
}

func sessionHasToolResult(session *Session, toolCallID string) bool {
	if session == nil || strings.TrimSpace(toolCallID) == "" {
		return false
	}
	for _, message := range session.History {
		if message.Role == "tool" && strings.TrimSpace(message.ToolCallID) == strings.TrimSpace(toolCallID) {
			return true
		}
	}
	return false
}

func (a *SessionActor) configureRuntime() {
	if a == nil || a.agent == nil {
		return
	}

	engine := a.agent.GetPermissionEngine()
	if engine == nil {
		engine = agent.NewPermissionEngine()
		engine.AskHandler = a
		if hookMgr := a.agent.GetHookManager(); hookMgr != nil {
			engine.Hooks = hookMgr
		}
		a.agent.SetPermissionEngine(engine)
	} else if engine.AskHandler == nil {
		engine.AskHandler = a
	}

	broker := a.agent.GetToolBroker()
	if broker == nil {
		a.agent.SetToolBroker(&toolbroker.Broker{UserInput: a})
	} else if broker.UserInput == nil {
		broker.UserInput = a
	}
}

func (a *SessionActor) publish(event runtimeevents.Event) {
	if a == nil {
		return
	}
	if event.SessionID == "" {
		event.SessionID = a.id
	}
	if event.AgentName == "" && a.agent != nil && a.agent.GetConfig() != nil {
		event.AgentName = a.agent.GetConfig().Name
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if a.eventStore != nil {
		seq, err := a.eventStore.AppendEvent(context.Background(), event)
		if err == nil {
			if event.Payload == nil {
				event.Payload = map[string]interface{}{}
			}
			event.Payload["seq"] = seq
		}
	}
	if a.eventBus != nil {
		a.eventBus.Publish(event)
	}
}

func resultTraceID(result *agent.Result, fallback string) string {
	if result != nil && strings.TrimSpace(result.TraceID) != "" {
		return result.TraceID
	}
	return fallback
}

func resultSteps(result *agent.Result) int {
	if result == nil {
		return 0
	}
	return result.Steps
}

func durationMillis(result *agent.Result) int64 {
	if result == nil {
		return 0
	}
	return result.Duration.GetDuration().Milliseconds()
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
