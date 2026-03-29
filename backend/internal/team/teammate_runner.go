package team

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ai-gateway/ai-agent-runtime/internal/pkg/logger"
)

// SessionClient abstracts prompt submission for teammate execution.
type SessionClient interface {
	SubmitPrompt(ctx context.Context, sessionID, prompt string, runMeta *RunMeta) (*SessionResult, error)
}

// SessionResult captures the outcome of a session prompt.
type SessionResult struct {
	Success      bool
	Output       string
	Error        string
	TraceID      string
	Steps        int
	Observations []SessionObservation
}

// TaskRunResult captures the outcome of a teammate task execution.
type TaskRunResult struct {
	Success        bool
	Output         string
	Summary        string
	Error          string
	TraceID        string
	Blocked        bool
	Outcome        TaskOutcomeStatus
	OutcomeApplied bool
	Blocker        string
	HandoffTo      string
	Structured     bool
	ProtocolError  string
}

// TeammateRunner drives task execution through existing sessions.
type TeammateRunner struct {
	Sessions          SessionClient
	Mailbox           *MailboxService
	Context           *ContextBuilder
	ContextBudget     int
	DigestLimit       int
	HeartbeatInterval time.Duration
}

const teammateAuxiliaryReadTimeout = 1500 * time.Millisecond

// StartTask submits a task prompt to the teammate's session and returns the result.
func (r *TeammateRunner) StartTask(ctx context.Context, team Team, mate Teammate, task Task) (*TaskRunResult, error) {
	if r == nil || r.Sessions == nil {
		return nil, fmt.Errorf("teammate runner sessions are not configured")
	}
	teamID := strings.TrimSpace(team.ID)
	if teamID == "" {
		teamID = strings.TrimSpace(task.TeamID)
	}
	if teamID == "" {
		return nil, fmt.Errorf("team id is required")
	}
	if strings.TrimSpace(mate.SessionID) == "" {
		return nil, fmt.Errorf("teammate session id is required")
	}
	stopHeartbeat := r.startHeartbeatLoop(ctx, mate.ID)
	defer stopHeartbeat()

	digest := ""
	if r.Mailbox != nil {
		limit := r.DigestLimit
		if limit <= 0 {
			limit = 4
		}
		digestCtx, cancel := context.WithTimeout(ctx, teammateAuxiliaryReadTimeout)
		if mailboxDigest, err := r.Mailbox.ReadDigest(digestCtx, teamID, mate.ID, limit, true); err == nil && mailboxDigest != nil {
			digest = mailboxDigest.Digest
		} else if err != nil {
			logger.Debug("teammate runner: mailbox digest skipped",
				logger.String("task_id", strings.TrimSpace(task.ID)),
				logger.String("error", err.Error()),
			)
		}
		cancel()
	}
	teamContext := ""
	contextBuilder := r.Context
	if contextBuilder == nil && r.Mailbox != nil && r.Mailbox.Store != nil {
		contextBuilder = NewContextBuilder(r.Mailbox.Store)
	}
	if contextBuilder != nil {
		budget := r.ContextBudget
		if budget <= 0 {
			budget = 6
		}
		contextCtx, cancel := context.WithTimeout(ctx, teammateAuxiliaryReadTimeout)
		if digest, err := contextBuilder.Build(contextCtx, teamID, strings.TrimSpace(task.ID), budget); err == nil && digest != nil {
			teamContext = strings.TrimSpace(digest.Summary)
		} else if err != nil {
			logger.Debug("teammate runner: team context skipped",
				logger.String("task_id", strings.TrimSpace(task.ID)),
				logger.String("error", err.Error()),
			)
		}
		cancel()
	}
	prompt := buildTaskPrompt(teamID, mate.Name, task, digest, teamContext)
	result, err := r.Sessions.SubmitPrompt(ctx, mate.SessionID, prompt, &RunMeta{
		PermissionMode: "bypass_permissions",
		Team: &TeamRunMeta{
			TeamID:        teamID,
			AgentID:       strings.TrimSpace(mate.ID),
			CurrentTaskID: strings.TrimSpace(task.ID),
		},
	})
	if err != nil {
		return &TaskRunResult{
			Success: false,
			Error:   err.Error(),
		}, err
	}
	if result == nil {
		return &TaskRunResult{
			Success: false,
			Error:   "session result is nil",
		}, fmt.Errorf("session result is nil")
	}

	run := &TaskRunResult{
		Success: result.Success,
		Output:  strings.TrimSpace(result.Output),
		Error:   strings.TrimSpace(firstNonEmptyString(result.Error)),
		TraceID: result.TraceID,
	}
	run.Summary = extractTaskSummary(run.Output)
	if run.Summary == "" && run.Error != "" {
		run.Summary = truncateLine(run.Error, 240)
	}
	if run.Summary == "" {
		run.Summary = truncateLine(run.Output, 240)
	}
	applyObservedTaskOutcome(run, result.Observations)
	if !run.OutcomeApplied {
		applyStructuredTaskOutcome(run, run.Output)
	}
	r.recoverStructuredTaskOutcome(ctx, strings.TrimSpace(task.ID), run)
	return run, nil
}

func (r *TeammateRunner) startHeartbeatLoop(ctx context.Context, teammateID string) func() {
	store := r.resolveStore()
	teammateID = strings.TrimSpace(teammateID)
	if store == nil || teammateID == "" {
		return func() {}
	}

	touch := func() {
		_ = store.UpdateTeammateHeartbeat(context.Background(), teammateID, time.Now().UTC())
	}
	touch()

	interval := r.HeartbeatInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				touch()
			}
		}
	}()
	return func() {
		close(stop)
		touch()
	}
}

func (r *TeammateRunner) resolveStore() Store {
	if r == nil {
		return nil
	}
	if r.Mailbox != nil && r.Mailbox.Store != nil {
		return r.Mailbox.Store
	}
	if r.Context != nil && r.Context.Store != nil {
		return r.Context.Store
	}
	return nil
}

func buildTaskPrompt(teamID, teammateName string, task Task, mailboxDigest string, teamContext string) string {
	teammateName = strings.TrimSpace(teammateName)
	if teammateName == "" {
		teammateName = "teammate"
	}
	title := strings.TrimSpace(task.Title)
	if title == "" {
		title = task.Goal
	}
	lines := []string{
		fmt.Sprintf("You are teammate %s in team %s.", teammateName, teamID),
		"",
		"Current task:",
		fmt.Sprintf("- Task ID: %s", strings.TrimSpace(task.ID)),
		fmt.Sprintf("- Title: %s", firstNonEmptyString(title, task.ID)),
		fmt.Sprintf("- Goal: %s", strings.TrimSpace(task.Goal)),
	}
	if len(task.Inputs) > 0 {
		lines = append(lines, fmt.Sprintf("- Inputs: %s", strings.Join(task.Inputs, ", ")))
	}
	if len(task.ReadPaths) > 0 {
		lines = append(lines, fmt.Sprintf("- Read paths: %s", formatPathList(task.ReadPaths)))
	}
	if len(task.WritePaths) > 0 {
		lines = append(lines, fmt.Sprintf("- Write paths: %s", formatPathList(task.WritePaths)))
	}
	if len(task.Deliverables) > 0 {
		lines = append(lines, fmt.Sprintf("- Deliverables: %s", strings.Join(task.Deliverables, ", ")))
	}
	lines = append(lines, "", "Constraints:")
	lines = append(lines, "- Treat the read paths as the authoritative task boundary for this task.")
	if len(task.WritePaths) == 0 {
		lines = append(lines, "- Do not modify files unless explicitly allowed.")
	} else {
		lines = append(lines, "- Do not modify files outside the write paths.")
	}
	lines = append(lines, "- For directory and document exploration, prefer direct read-only tools such as ls, glob, grep, and view.")
	lines = append(lines, "- Do not use background_task or shell commands for basic file listing or file reading when direct tools can answer the task.")
	lines = append(lines, "- If you are unsure about the exact task boundary, allowed paths, deliverables, or team context, call read_task_spec or read_task_context before editing.")
	lines = append(lines, "- Summarize decisions and blockers.")
	lines = append(lines, "- If blocked, send a mailbox message to the lead.")
	lines = append(lines, "- Prefer report_task_outcome for done/failed/blocked/handoff outcomes; block_current_task is a compatibility alias for blocked or handoff.")
	lines = append(lines, TaskOutcomePromptLines(TaskOutcomeDone, TaskOutcomeFailed, TaskOutcomeBlocked, TaskOutcomeHandoff)...)

	if strings.TrimSpace(mailboxDigest) != "" {
		lines = append(lines, "", "Mailbox digest:", mailboxDigest)
	}
	if strings.TrimSpace(teamContext) != "" {
		lines = append(lines, "", teamContext)
	}

	return strings.Join(lines, "\n")
}

func (r *TeammateRunner) recoverStructuredTaskOutcome(ctx context.Context, taskID string, run *TaskRunResult) {
	if r == nil || run == nil || run.Structured || taskID == "" {
		return
	}
	store := r.resolveStore()
	if store == nil {
		return
	}
	task, err := store.GetTask(ctx, taskID)
	if err != nil || task == nil {
		return
	}

	switch task.Status {
	case TaskStatusDone:
		run.Success = true
		run.Blocked = false
		run.Outcome = TaskOutcomeDone
	case TaskStatusBlocked:
		run.Success = true
		run.Blocked = true
		run.Outcome = TaskOutcomeBlocked
		run.Blocker = strings.TrimSpace(firstNonEmptyString(task.Summary, run.Blocker))
	case TaskStatusFailed:
		run.Success = false
		run.Blocked = false
		run.Outcome = TaskOutcomeFailed
		if run.Error == "" {
			run.Error = strings.TrimSpace(firstNonEmptyString(task.Summary, run.Error, "task failed"))
		}
	default:
		return
	}

	if summary := strings.TrimSpace(firstNonEmptyString(task.Summary, run.Summary)); summary != "" {
		run.Summary = summary
	}
	run.Structured = true
	run.OutcomeApplied = true
	run.ProtocolError = ""
}

func extractTaskSummary(output string) string {
	if outcome, err := ParseTaskOutcomeContract(output); err == nil {
		return strings.TrimSpace(firstNonEmptyString(outcome.Summary, outcome.Blocker))
	}
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		switch {
		case strings.HasPrefix(lower, "blocked:"):
			return strings.TrimSpace(trimmed[len("blocked:"):])
		case strings.HasPrefix(lower, "blocker:"):
			return strings.TrimSpace(trimmed[len("blocker:"):])
		case strings.HasPrefix(lower, "summary:"):
			return strings.TrimSpace(trimmed[len("summary:"):])
		case strings.HasPrefix(lower, "result:"):
			return strings.TrimSpace(trimmed[len("result:"):])
		case strings.HasPrefix(lower, "final:"):
			return strings.TrimSpace(trimmed[len("final:"):])
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return truncateLine(trimmed, 240)
		}
	}
	return ""
}

func formatPathList(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	parts := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		parts = append(parts, path)
	}
	return strings.Join(parts, ", ")
}

func applyStructuredTaskOutcome(run *TaskRunResult, output string) {
	if run == nil || !run.Success {
		return
	}
	outcome, err := ParseTaskOutcomeContract(output)
	if err != nil {
		applyTaskProtocolError(run, err)
		return
	}
	run.Structured = true
	run.Outcome = outcome.Status
	run.Blocker = strings.TrimSpace(outcome.Blocker)
	run.HandoffTo = strings.TrimSpace(outcome.HandoffTo)
	if summary := strings.TrimSpace(firstNonEmptyString(outcome.Summary, outcome.Blocker)); summary != "" {
		run.Summary = summary
	}
	switch outcome.Status {
	case TaskOutcomeBlocked, TaskOutcomeHandoff:
		run.Blocked = true
	case TaskOutcomeFailed:
		run.Success = false
		if run.Error == "" {
			run.Error = firstNonEmptyString(run.Summary, "task failed")
		}
	}
}

type taskOutcomeObservationPayload struct {
	Status    string `json:"status"`
	Outcome   string `json:"outcome"`
	Summary   string `json:"summary"`
	Blocker   string `json:"blocker"`
	HandoffTo string `json:"handoff_to"`
}

func applyObservedTaskOutcome(run *TaskRunResult, observations []SessionObservation) {
	if run == nil || !run.Success || len(observations) == 0 {
		return
	}
	for i := len(observations) - 1; i >= 0; i-- {
		observation := observations[i]
		if !observation.Success {
			continue
		}
		switch normalizeObservedTaskOutcomeTool(observation.Tool) {
		case "report_task_outcome", "block_current_task":
		default:
			continue
		}
		payload, ok := decodeObservedTaskOutcomePayload(observation.Output)
		if !ok {
			continue
		}
		outcome := normalizeObservedTaskOutcomeStatus(payload)
		if outcome == "" {
			continue
		}
		run.Structured = true
		run.OutcomeApplied = true
		run.Outcome = outcome
		run.ProtocolError = ""
		run.HandoffTo = strings.TrimSpace(payload.HandoffTo)
		run.Blocker = strings.TrimSpace(payload.Blocker)
		if summary := strings.TrimSpace(firstNonEmptyString(payload.Summary, payload.Blocker, run.Summary)); summary != "" {
			run.Summary = summary
		}
		switch outcome {
		case TaskOutcomeBlocked, TaskOutcomeHandoff:
			run.Success = true
			run.Blocked = true
			run.Blocker = strings.TrimSpace(firstNonEmptyString(run.Blocker, run.Summary))
		case TaskOutcomeFailed:
			run.Success = false
			run.Blocked = false
			run.Blocker = strings.TrimSpace(firstNonEmptyString(run.Blocker, run.Summary))
			if run.Error == "" {
				run.Error = firstNonEmptyString(run.Summary, run.Blocker, "task failed")
			}
		default:
			run.Success = true
			run.Blocked = false
			run.Blocker = ""
			run.HandoffTo = ""
		}
		return
	}
}

func normalizeObservedTaskOutcomeTool(tool string) string {
	return strings.ToLower(strings.TrimSpace(tool))
}

func decodeObservedTaskOutcomePayload(output interface{}) (taskOutcomeObservationPayload, bool) {
	if output == nil {
		return taskOutcomeObservationPayload{}, false
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return taskOutcomeObservationPayload{}, false
	}
	var payload taskOutcomeObservationPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return taskOutcomeObservationPayload{}, false
	}
	return payload, true
}

func normalizeObservedTaskOutcomeStatus(payload taskOutcomeObservationPayload) TaskOutcomeStatus {
	outcome := TaskOutcomeStatus(strings.ToLower(strings.TrimSpace(payload.Outcome)))
	if validTaskOutcomeStatus(outcome) {
		return outcome
	}
	switch strings.ToLower(strings.TrimSpace(payload.Status)) {
	case string(TaskStatusDone):
		return TaskOutcomeDone
	case string(TaskStatusFailed):
		return TaskOutcomeFailed
	case string(TaskStatusBlocked):
		if strings.TrimSpace(payload.HandoffTo) != "" {
			return TaskOutcomeHandoff
		}
		return TaskOutcomeBlocked
	default:
		return ""
	}
}

func applyTaskProtocolError(run *TaskRunResult, err error) {
	if run == nil {
		return
	}
	message := strings.TrimSpace(firstNonEmptyString(errorString(err), "protocol error: invalid teammate task outcome"))
	if !strings.HasPrefix(strings.ToLower(message), "protocol error:") {
		message = "protocol error: " + message
	}
	run.Success = false
	run.Blocked = false
	run.Outcome = TaskOutcomeFailed
	run.Blocker = ""
	run.HandoffTo = ""
	run.Structured = false
	run.ProtocolError = message
	run.Summary = message
	run.Error = message
}
