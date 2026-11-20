// Package scheduleexec adapts the shared runtime execution core to the
// schedule.SessionExecutor / schedule.HeadlessExecutor seams.
//
// The scheduler leaders (runtime-server, aicli, and the standalone daemon) all
// use the same session actor core. Keeping this adapter in one internal package
// prevents a host from advertising scheduler capability while silently lacking
// the execution half of that capability.
package scheduleexec

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// TurnExecutor is the narrow execution seam needed by automation jobs.
// runtimehost.Executor and host-specific adapters both satisfy it.
type TurnExecutor interface {
	SubmitTurn(ctx context.Context, req runtimehost.TurnRequest) (runtimehost.TurnResult, error)
}

// SessionManager is the subset of chat.SessionManager used to provision the
// temporary session for headless/workspace jobs.
type SessionManager interface {
	Create(ctx context.Context, userID string) (*chat.Session, error)
	Get(ctx context.Context, sessionID string) (*chat.Session, error)
	Update(ctx context.Context, session *chat.Session) error
	Archive(ctx context.Context, sessionID string) error
}

// Options configures the shared adapter.
type Options struct {
	// Executor is the host's session execution core.
	Executor TurnExecutor
	// Sessions provisions temporary headless sessions. It is required for
	// headless/workspace jobs but not for session-target reminders.
	Sessions SessionManager
	// Profile is used when a job does not specify target_profile.
	Profile string
	// HeadlessUserID is the synthetic owner for temporary headless sessions.
	// Defaults to "scheduled" when empty.
	HeadlessUserID string
}

// Executor implements both schedule.SessionExecutor and
// schedule.HeadlessExecutor.
type Executor struct {
	executor       TurnExecutor
	sessions       SessionManager
	profile        string
	headlessUserID string
}

var (
	_ schedule.SessionExecutor  = (*Executor)(nil)
	_ schedule.HeadlessExecutor = (*Executor)(nil)
)

// New creates a shared schedule executor. The session manager may be nil when
// the host only needs session-target execution; headless/workspace jobs will
// then fail with an actionable error instead of silently blocking.
func New(opts Options) (*Executor, error) {
	if opts.Executor == nil {
		return nil, fmt.Errorf("scheduleexec: executor is required")
	}
	userID := strings.TrimSpace(opts.HeadlessUserID)
	if userID == "" {
		userID = "scheduled"
	}
	return &Executor{
		executor:       opts.Executor,
		sessions:       opts.Sessions,
		profile:        strings.TrimSpace(opts.Profile),
		headlessUserID: userID,
	}, nil
}

// ExecuteTurn executes one embedded session-target reminder.
func (e *Executor) ExecuteTurn(ctx context.Context, sessionID, prompt string) error {
	if e == nil || e.executor == nil {
		return fmt.Errorf("scheduleexec: executor is not configured")
	}
	_, err := e.executor.SubmitTurn(ctx, runtimehost.TurnRequest{
		SessionID: sessionID,
		Prompt:    prompt,
		RunMeta:   scheduledRunMeta(),
		Profile:   e.profile,
	})
	return err
}

// ExecuteHeadlessTurn creates a temporary session, executes the job, and
// archives the session after a completed turn.
func (e *Executor) ExecuteHeadlessTurn(ctx context.Context, req schedule.HeadlessTurnRequest) (schedule.HeadlessTurnResult, error) {
	if e == nil || e.executor == nil {
		return schedule.HeadlessTurnResult{}, fmt.Errorf("scheduleexec: executor is not configured")
	}
	if e.sessions == nil {
		return schedule.HeadlessTurnResult{}, fmt.Errorf("scheduleexec: session manager is not configured")
	}
	sessionID, err := e.createSession(ctx)
	if err != nil {
		return schedule.HeadlessTurnResult{}, fmt.Errorf("scheduleexec: create headless session: %w", err)
	}
	result := schedule.HeadlessTurnResult{SessionID: sessionID}
	if err := e.initializeSession(ctx, sessionID, req); err != nil {
		return result, fmt.Errorf("scheduleexec: initialize headless session: %w", err)
	}
	turn, err := e.submitHeadlessTurn(ctx, runtimehost.TurnRequest{
		SessionID:       sessionID,
		Prompt:          BuildHeadlessPrompt(req),
		RunMeta:         scheduledRunMeta(),
		Profile:         firstNonEmpty(strings.TrimSpace(req.Profile), e.profile),
		Provider:        strings.TrimSpace(req.Provider),
		Model:           strings.TrimSpace(req.Model),
		ReasoningEffort: strings.TrimSpace(req.ReasoningEffort),
		Workspace:       strings.TrimSpace(req.Workspace),
	})
	result.Output = turn.Output
	if err != nil {
		return result, err
	}
	if !turn.Completed {
		return result, fmt.Errorf("scheduleexec: headless turn did not complete")
	}
	if !turn.Success {
		message := strings.TrimSpace(turn.Error)
		if message == "" {
			message = "headless turn reported failure"
		}
		return result, fmt.Errorf("scheduleexec: %s", message)
	}
	// Best effort: a failed archive must not turn a successful job into a
	// failed one. The session remains visible for cleanup.
	_ = e.sessions.Archive(ctx, sessionID)
	return result, nil
}

func (e *Executor) submitHeadlessTurn(ctx context.Context, req runtimehost.TurnRequest) (runtimehost.TurnResult, error) {
	if waiter, ok := e.executor.(runtimehost.TurnWaiter); ok {
		return waiter.SubmitTurnWait(ctx, req)
	}
	return e.executor.SubmitTurn(ctx, req)
}

func (e *Executor) createSession(ctx context.Context) (string, error) {
	session, err := e.sessions.Create(ctx, e.headlessUserID)
	if err != nil {
		return "", err
	}
	if session == nil || strings.TrimSpace(session.ID) == "" {
		return "", fmt.Errorf("session manager returned an empty session")
	}
	return strings.TrimSpace(session.ID), nil
}

func (e *Executor) initializeSession(ctx context.Context, sessionID string, req schedule.HeadlessTurnRequest) error {
	session, err := e.sessions.Get(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("load session: %w", err)
	}
	if session == nil {
		return fmt.Errorf("session not found")
	}

	workspace := ""
	if req.TargetKind == schedule.TargetWorkspace {
		workspace = strings.TrimSpace(req.Workspace)
	}
	model := strings.TrimSpace(req.Model)
	session.SetContext(sessionmeta.WorkspacePath, workspace)
	session.SetContext(sessionmeta.ProviderName, strings.TrimSpace(req.Provider))
	session.SetContext(sessionmeta.Model, model)
	session.SetContext(toolbroker.AgentSessionContextRequestedModel, model)
	session.SetContext(sessionmeta.ReasoningEffort, strings.TrimSpace(req.ReasoningEffort))
	return e.sessions.Update(ctx, session)
}

func scheduledRunMeta() *team.RunMeta {
	return &team.RunMeta{
		Source:         "schedule",
		PermissionMode: string(policy.ModeDontAsk),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// BuildHeadlessPrompt renders a job's goal and declarative plan into one turn
// prompt. With no goal/plan it preserves the plain reminder semantics.
func BuildHeadlessPrompt(req schedule.HeadlessTurnRequest) string {
	goal := strings.TrimSpace(req.Goal)
	prompt := strings.TrimSpace(req.Prompt)
	if goal == "" && len(req.Plan) == 0 {
		return prompt
	}
	var b strings.Builder
	if goal != "" {
		b.WriteString("[SCHEDULE JOB] goal: ")
		b.WriteString(goal)
		b.WriteString("\n")
	}
	if len(req.Plan) > 0 {
		b.WriteString("plan:\n")
		for i, step := range req.Plan {
			if trimmed := strings.TrimSpace(step); trimmed != "" {
				fmt.Fprintf(&b, "%d. %s\n", i+1, trimmed)
			}
		}
	}
	if prompt != "" {
		b.WriteString(prompt)
	}
	return strings.TrimSpace(b.String())
}
