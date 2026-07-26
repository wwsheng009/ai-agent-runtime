package team

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentdef"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// SessionClient abstracts prompt submission for teammate execution.
type SessionClient interface {
	SubmitPrompt(ctx context.Context, sessionID, prompt string, runMeta *RunMeta) (*SessionResult, error)
}

// TaskTriggerClient dispatches a team task through the agent control surface.
type TaskTriggerClient interface {
	TriggerTask(ctx context.Context, request TaskTriggerRequest) (*SessionResult, error)
}

// TaskRouteResolver optionally resolves a per-task execution route for a teammate run.
type TaskRouteResolver interface {
	ResolveTaskRoute(ctx context.Context, request TaskRouteRequest) (*TaskRouteResolution, error)
}

// TaskRouteAuditSink optionally records route decisions and failures.
type TaskRouteAuditSink interface {
	RecordTaskRouteAudit(ctx context.Context, audit TaskRouteAudit) error
}

// TaskRouteRequest contains the inputs needed to resolve a teammate task route.
type TaskRouteRequest struct {
	Team      Team
	Teammate  Teammate
	Task      Task
	Attempt   int
	SessionID string
}

// TaskRouteResolution is the result of route resolution.
type TaskRouteResolution struct {
	Route    *TaskExecutionRoute
	Disabled bool
	Strict   bool
}

// TaskTriggerRequest describes a teammate task dispatch.
type TaskTriggerRequest struct {
	SessionID           string
	TeamID              string
	AgentID             string
	TaskID              string
	Difficulty          string
	DifficultyRationale string
	Route               *TaskExecutionRoute
	Prompt              string
	RunMeta             *RunMeta
}

// SessionResult captures the outcome of a session prompt.
type SessionResult struct {
	Success       bool
	Output        string
	Error         string
	ErrorType     string
	ErrorMetadata map[string]interface{}
	TraceID       string
	Steps         int
	Observations  []SessionObservation
}

// TaskRunResult captures the outcome of a teammate task execution.
type TaskRunResult struct {
	Success        bool
	Output         string
	Summary        string
	Error          string
	ErrorType      string
	ErrorMetadata  map[string]interface{}
	TraceID        string
	Blocked        bool
	Outcome        TaskOutcomeStatus
	OutcomeApplied bool
	Blocker        string
	HandoffTo      string
	Structured     bool
	ProtocolError  string
	Route          *TaskExecutionRoute
}

// TeammateRunner drives task execution through existing sessions.
type TeammateRunner struct {
	Sessions          SessionClient
	AgentControl      TaskTriggerClient
	Mailbox           *MailboxService
	Context           *ContextBuilder
	RouteResolver     TaskRouteResolver
	RouteAudit        TaskRouteAuditSink
	ContextBudget     int
	DigestLimit       int
	HeartbeatInterval time.Duration
}

const teammateAuxiliaryReadTimeout = 1500 * time.Millisecond

// StartTask submits a task prompt to the teammate's session and returns the result.
func (r *TeammateRunner) StartTask(ctx context.Context, team Team, mate Teammate, task Task) (*TaskRunResult, error) {
	if r == nil || (r.Sessions == nil && r.AgentControl == nil) {
		return nil, fmt.Errorf("teammate runner agent control is not configured")
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

	route, strictRouteFailure, routeErr := r.resolveTaskRoute(ctx, team, mate, task)
	if strictRouteFailure {
		run := buildStrictRouteFailureResult(route, routeErr)
		r.recoverStructuredTaskOutcome(ctx, strings.TrimSpace(task.ID), run)
		return run, nil
	}

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
	prompt := buildTaskPrompt(teamID, mate.Name, task, digest, teamContext, route)
	runMeta := &RunMeta{
		PermissionMode:        teammateRunPermissionMode(mate.Profile),
		CompletionRequirement: "complete_task",
		Team: &TeamRunMeta{
			TeamID:        teamID,
			AgentID:       strings.TrimSpace(mate.ID),
			CurrentTaskID: strings.TrimSpace(task.ID),
		},
	}
	applyRouteToTeamRunMeta(runMeta.Team, route)
	result, err := r.triggerTask(ctx, TaskTriggerRequest{
		SessionID:           strings.TrimSpace(mate.SessionID),
		TeamID:              teamID,
		AgentID:             strings.TrimSpace(mate.ID),
		TaskID:              strings.TrimSpace(task.ID),
		Difficulty:          strings.TrimSpace(task.Difficulty),
		DifficultyRationale: strings.TrimSpace(task.DifficultyRationale),
		Route:               route.Clone(),
		Prompt:              prompt,
		RunMeta:             runMeta,
	})
	if err != nil {
		run := buildTaskRunResult(result)
		if run == nil {
			run = &TaskRunResult{}
		}
		run.Success = false
		if strings.TrimSpace(run.Error) == "" {
			run.Error = strings.TrimSpace(err.Error())
		}
		if strings.TrimSpace(run.Summary) == "" {
			run.Summary = truncateLine(firstNonEmptyString(run.Error, err.Error()), 240)
		}
		run.Route = route.Clone()
		r.recoverStructuredTaskOutcome(ctx, strings.TrimSpace(task.ID), run)
		return run, err
	}
	if result == nil {
		return &TaskRunResult{
			Success: false,
			Error:   "session result is nil",
		}, fmt.Errorf("session result is nil")
	}

	run := buildTaskRunResult(result)
	run.Route = route.Clone()
	applyObservedTaskOutcome(run, result.Observations)
	if !run.OutcomeApplied {
		applyStructuredTaskOutcome(run, run.Output)
	}
	r.recoverStructuredTaskOutcome(ctx, strings.TrimSpace(task.ID), run)
	return run, nil
}

func (r *TeammateRunner) triggerTask(ctx context.Context, request TaskTriggerRequest) (*SessionResult, error) {
	if r != nil && r.AgentControl != nil {
		return r.AgentControl.TriggerTask(ctx, request)
	}
	if r == nil || r.Sessions == nil {
		return nil, fmt.Errorf("teammate runner agent control is not configured")
	}
	if request.Route != nil {
		return nil, fmt.Errorf("teammate runner route override requires agent control trigger support")
	}
	return r.Sessions.SubmitPrompt(ctx, request.SessionID, request.Prompt, request.RunMeta)
}

func (r *TeammateRunner) resolveTaskRoute(ctx context.Context, team Team, mate Teammate, task Task) (*TaskExecutionRoute, bool, error) {
	if r == nil || r.RouteResolver == nil {
		return nil, false, nil
	}
	attempt := task.RetryCount + 1
	if attempt <= 0 {
		attempt = 1
	}
	resolution, err := r.RouteResolver.ResolveTaskRoute(ctx, TaskRouteRequest{
		Team:      team,
		Teammate:  mate,
		Task:      task,
		Attempt:   attempt,
		SessionID: strings.TrimSpace(mate.SessionID),
	})
	route := (*TaskExecutionRoute)(nil)
	disabled := false
	strict := false
	if resolution != nil {
		route = normalizeTaskExecutionRoute(resolution.Route, task, attempt)
		disabled = resolution.Disabled
		strict = resolution.Strict
	}
	if disabled {
		route = nil
	}
	if err != nil && strict {
		route = normalizeTaskExecutionRoute(route, task, attempt)
		if route == nil {
			route = &TaskExecutionRoute{}
		}
		route.Error = truncateLine(sanitizeRouteAuditText(err.Error()), 240)
		if route.ResolvedAt.IsZero() {
			route.ResolvedAt = time.Now().UTC()
		}
	}
	r.recordTaskRouteAudit(ctx, team, mate, task, route, disabled, strict, err)
	return route, err != nil && strict, err
}

func normalizeTaskExecutionRoute(route *TaskExecutionRoute, task Task, attempt int) *TaskExecutionRoute {
	if route == nil {
		return nil
	}
	clone := route.Clone()
	if strings.TrimSpace(clone.Difficulty) == "" {
		clone.Difficulty = strings.TrimSpace(task.Difficulty)
	}
	if strings.TrimSpace(clone.DifficultyRationale) == "" {
		clone.DifficultyRationale = strings.TrimSpace(task.DifficultyRationale)
	}
	if clone.Attempt <= 0 {
		clone.Attempt = attempt
	}
	if clone.ResolvedAt.IsZero() {
		clone.ResolvedAt = time.Now().UTC()
	}
	return clone
}

func (r *TeammateRunner) recordTaskRouteAudit(ctx context.Context, team Team, mate Teammate, task Task, route *TaskExecutionRoute, disabled, strict bool, routeErr error) {
	if r == nil || r.RouteAudit == nil {
		return
	}
	audit := TaskRouteAudit{
		TeamID:     firstNonEmptyString(strings.TrimSpace(team.ID), strings.TrimSpace(task.TeamID)),
		AgentID:    strings.TrimSpace(mate.ID),
		TaskID:     strings.TrimSpace(task.ID),
		SessionID:  strings.TrimSpace(mate.SessionID),
		Route:      route.Clone(),
		Strict:     strict,
		Disabled:   disabled,
		RecordedAt: time.Now().UTC(),
	}
	if routeErr != nil {
		audit.Error = truncateLine(sanitizeRouteAuditText(routeErr.Error()), 240)
	}
	if err := r.RouteAudit.RecordTaskRouteAudit(ctx, audit); err != nil {
		logger.Debug("teammate runner: route audit skipped",
			logger.String("task_id", strings.TrimSpace(task.ID)),
			logger.String("error", err.Error()),
		)
	}
}

func applyRouteToTeamRunMeta(meta *TeamRunMeta, route *TaskExecutionRoute) {
	if meta == nil || route == nil {
		return
	}
	meta.Difficulty = strings.TrimSpace(route.Difficulty)
	meta.DifficultySource = strings.TrimSpace(route.DifficultySource)
	meta.DifficultyRationale = strings.TrimSpace(route.DifficultyRationale)
	meta.RouteProvider = strings.TrimSpace(route.Provider)
	meta.RouteModel = strings.TrimSpace(route.Model)
	meta.RouteReasoningEffort = strings.TrimSpace(route.ReasoningEffort)
	meta.RouteSource = strings.TrimSpace(route.Source)
	meta.RouteWarnings = append([]string(nil), route.Warnings...)
	meta.RouteFallbackUsed = route.FallbackUsed
	meta.RouteFallbackReason = strings.TrimSpace(route.FallbackReason)
}

func buildStrictRouteFailureResult(route *TaskExecutionRoute, routeErr error) *TaskRunResult {
	message := "task route resolution failed"
	if routeErr != nil && strings.TrimSpace(routeErr.Error()) != "" {
		message = truncateLine(sanitizeRouteAuditText(routeErr.Error()), 240)
	} else if route != nil && strings.TrimSpace(route.Error) != "" {
		message = truncateLine(sanitizeRouteAuditText(route.Error), 240)
	}
	return &TaskRunResult{
		Success:    true,
		Summary:    message,
		Error:      message,
		ErrorType:  "task_route_resolution",
		Blocked:    true,
		Outcome:    TaskOutcomeBlocked,
		Blocker:    message,
		Structured: true,
		Route:      route.Clone(),
	}
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

// teammateRunPermissionMode returns the RunMeta permission mode for a teammate.
// Portable agentdef profiles (e.g. explore → plan) win; otherwise keep the
// historical team-worker default of bypass_permissions so unlabeled mates stay
// unattended-capable. Lead planner stays on its own hardcoded default.
func teammateRunPermissionMode(profile string) string {
	if mode := strings.TrimSpace(agentdef.TeammatePermissionMode(profile, agentdef.DiscoverOptions{})); mode != "" {
		return mode
	}
	return "bypass_permissions"
}

func buildTaskPrompt(teamID, teammateName string, task Task, mailboxDigest string, teamContext string, route *TaskExecutionRoute) string {
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
	if difficulty := strings.TrimSpace(task.Difficulty); difficulty != "" {
		lines = append(lines, fmt.Sprintf("- Difficulty: %s", difficulty))
	}
	if rationale := strings.TrimSpace(task.DifficultyRationale); rationale != "" {
		lines = append(lines, fmt.Sprintf("- Difficulty rationale: %s", rationale))
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
	lines = append(lines, "- For directory and document exploration, prefer direct read-only tools such as ls, glob, grep, and view. The local grep tool prefers ripgrep (rg) when it is available, and it accepts common rg-style options/mental-model mappings such as glob/-g, iglob/--iglob, glob_case_insensitive/--glob-case-insensitive, pattern_file/pattern_files/-f/--file, ignore_file/--ignore-file, ignore_file_case_insensitive/--ignore-file-case-insensitive, no_ignore_files/--no-ignore-files, no_ignore_parent/vcs/global/dot/--no-ignore-parent/--no-ignore-vcs/--no-ignore-global/--no-ignore-dot, hidden/no_hidden/--hidden/--no-hidden, -u/-uu/-uuu/--unrestricted (where -u mainly relaxes ignore, -uu additionally includes hidden files/dirs, and -uuu further relaxes binary filtering; all mapped through structured no_ignore/unrestricted_level), no_config/--no-config, no_messages/--no-messages, one_file_system/--one-file-system, pcre2/-P/--pcre2, engine/--engine, multiline/-U/--multiline, multiline_dotall/--multiline-dotall, replace/-r/--replace, passthru/--passthru, crlf/--crlf, auto_hybrid_regex/--auto-hybrid-regex (all rg-only passthrough when rg exists), column/--column, trim/--trim, pretty/--pretty, line_buffered/--line-buffered, block_buffered/--block-buffered, null/null_data/--null/--null-data, field_context_separator/--field-context-separator, path_separator/--path-separator, context_separator/--context-separator, max_columns/-M/--max-columns, max_columns_preview/--max-columns-preview, count_matches/--count-matches, stats/--stats, json/--json, follow/-L/--follow, sort/sortr/--sort/--sortr, sort_files/--sort-files, type/-t, type_not/-T, type_add/type_clear/--type-add/--type-clear, -i, -w, -x, -v, -o, -C/-B/-A, -l, --files-without-match, -c, repeated -e patterns, rg_args, direct single-file path searches, multi-path searches, path-aware globs like src/**/*.go, common short-flag clusters like -iwg*.go, explicit zero forms like --max-depth 0 / -m 0, and max_filesize/--max-filesize. Grep output is normally normalized to a stable path:line[:column]: content shape, so display-only rg flags such as -n/-H/-N/--no-filename/--color and structured aliases like line_number/heading/no_heading/with_filename/no_filename/color can be used as familiar no-op hints without changing the output skeleton; if json/--json is requested, the grep tool instead passes through rg-style JSON Lines events.")
	lines = append(lines, "- rg优先级：structured>rg_args；no_hidden>hidden；no_ignore_files>ignore_file；no_ignore_files drops ignore_file_case_insensitive；follow+one_file_system=rg-only。")
	lines = append(lines, "- Do not use background_task or shell commands for basic file listing or file reading when direct tools can answer the task.")
	lines = append(lines, "- If you are unsure about the exact task boundary, allowed paths, deliverables, or team context, call read_task_spec or read_task_context before editing.")
	lines = append(lines, "- Summarize decisions and blockers.")
	lines = append(lines, "- If blocked, send a mailbox message to the lead.")
	lines = append(lines, "- Prefer report_task_outcome for done/failed/blocked/handoff outcomes; block_current_task is a compatibility alias for blocked or handoff.")
	lines = append(lines, TaskOutcomePromptLines(TaskOutcomeDone, TaskOutcomeFailed, TaskOutcomeBlocked, TaskOutcomeHandoff)...)
	if route != nil {
		lines = append(lines, "", "Runtime routing:")
		if provider := strings.TrimSpace(route.Provider); provider != "" {
			lines = append(lines, fmt.Sprintf("- Provider: %s", provider))
		}
		if model := strings.TrimSpace(route.Model); model != "" {
			lines = append(lines, fmt.Sprintf("- Model: %s", model))
		}
		if effort := strings.TrimSpace(route.ReasoningEffort); effort != "" {
			lines = append(lines, fmt.Sprintf("- Reasoning effort: %s", effort))
		}
		if source := strings.TrimSpace(route.Source); source != "" {
			lines = append(lines, fmt.Sprintf("- Route source: %s", source))
		}
		if reason := strings.TrimSpace(route.FallbackReason); reason != "" {
			lines = append(lines, fmt.Sprintf("- Fallback reason: %s", reason))
		}
		if len(route.Warnings) > 0 {
			lines = append(lines, fmt.Sprintf("- Route warnings: %s", strings.Join(route.Warnings, "; ")))
		}
		if route.FallbackUsed {
			lines = append(lines, "- Fallback used: true")
		}
	}

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

func buildTaskRunResult(result *SessionResult) *TaskRunResult {
	if result == nil {
		return nil
	}
	run := &TaskRunResult{
		Success:       result.Success,
		Output:        strings.TrimSpace(result.Output),
		Error:         strings.TrimSpace(firstNonEmptyString(result.Error)),
		ErrorType:     strings.TrimSpace(result.ErrorType),
		ErrorMetadata: cloneStructuredErrorMetadata(result.ErrorMetadata),
		TraceID:       strings.TrimSpace(result.TraceID),
	}
	run.Summary = extractTaskSummary(run.Output)
	if run.Summary == "" && run.Error != "" {
		run.Summary = truncateLine(run.Error, 240)
	}
	if run.Summary == "" {
		run.Summary = truncateLine(run.Output, 240)
	}
	return run
}

func cloneStructuredErrorMetadata(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
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
	if run == nil {
		return
	}
	// Parse even when the session reported Success=false. Worker harness
	// complete_task may fail for missing report_task_outcome / block_current_task
	// while the assistant still emitted a valid structured status JSON fallback.
	// That fallback is part of the teammate contract and must recover the run.
	outcome, err := ParseTaskOutcomeContract(output)
	if err != nil {
		// Protocol errors only apply when the session otherwise succeeded; keep
		// original session failures (preflight, cancel, etc.) when no structured
		// contract is present.
		if run.Success {
			applyTaskProtocolError(run, err)
		}
		return
	}
	run.Structured = true
	run.Outcome = outcome.Status
	run.ProtocolError = ""
	run.Blocker = strings.TrimSpace(outcome.Blocker)
	run.HandoffTo = strings.TrimSpace(outcome.HandoffTo)
	if summary := strings.TrimSpace(firstNonEmptyString(outcome.Summary, outcome.Blocker)); summary != "" {
		run.Summary = summary
	}
	// Do not set OutcomeApplied: structured text is a fallback contract. The
	// orchestrator still needs BlockTask/complete/fail store transitions (unlike
	// tool/store-applied outcomes recovered via recoverStructuredTaskOutcome).
	switch outcome.Status {
	case TaskOutcomeBlocked, TaskOutcomeHandoff:
		// Recover success so executeAssignment takes the blocked replan path
		// instead of failTaskWithRunResult (complete_task missing is not a hard
		// task failure when structured blocked/handoff is present).
		run.Success = true
		run.Blocked = true
		run.Blocker = strings.TrimSpace(firstNonEmptyString(run.Blocker, run.Summary))
		// Clear harness completion-requirement noise; blocker/summary carry state.
		if strings.Contains(strings.ToLower(run.Error), "completion requirement") {
			run.Error = ""
		}
	case TaskOutcomeFailed:
		run.Success = false
		run.Blocked = false
		run.Blocker = strings.TrimSpace(firstNonEmptyString(run.Blocker, run.Summary))
		if run.Error == "" || strings.Contains(strings.ToLower(run.Error), "completion requirement") {
			run.Error = firstNonEmptyString(run.Summary, run.Blocker, "task failed")
		}
	default: // done
		run.Success = true
		run.Blocked = false
		run.Blocker = ""
		run.HandoffTo = ""
		if strings.Contains(strings.ToLower(run.Error), "completion requirement") {
			run.Error = ""
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
	switch value := output.(type) {
	case taskOutcomeObservationPayload:
		return value, true
	case *taskOutcomeObservationPayload:
		if value == nil {
			return taskOutcomeObservationPayload{}, false
		}
		return *value, true
	case map[string]interface{}:
		return decodeObservedTaskOutcomePayloadFromJSON(value)
	case string:
		return decodeObservedTaskOutcomePayloadFromText(value)
	case []byte:
		return decodeObservedTaskOutcomePayloadFromText(string(value))
	case json.RawMessage:
		return decodeObservedTaskOutcomePayloadFromText(string(value))
	default:
		// Structured tool results are sometimes wrapped in typed structs. Prefer
		// JSON re-encoding so ReportTaskOutcomeResult and similar payloads decode.
		raw, err := json.Marshal(value)
		if err != nil {
			return taskOutcomeObservationPayload{}, false
		}
		return decodeObservedTaskOutcomePayloadFromJSONBytes(raw)
	}
}

func decodeObservedTaskOutcomePayloadFromJSON(value interface{}) (taskOutcomeObservationPayload, bool) {
	raw, err := json.Marshal(value)
	if err != nil {
		return taskOutcomeObservationPayload{}, false
	}
	return decodeObservedTaskOutcomePayloadFromJSONBytes(raw)
}

func decodeObservedTaskOutcomePayloadFromJSONBytes(raw []byte) (taskOutcomeObservationPayload, bool) {
	var payload taskOutcomeObservationPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return taskOutcomeObservationPayload{}, false
	}
	if strings.TrimSpace(payload.Status) == "" &&
		strings.TrimSpace(payload.Outcome) == "" &&
		strings.TrimSpace(payload.Summary) == "" &&
		strings.TrimSpace(payload.Blocker) == "" &&
		strings.TrimSpace(payload.HandoffTo) == "" {
		return taskOutcomeObservationPayload{}, false
	}
	return payload, true
}

func decodeObservedTaskOutcomePayloadFromText(text string) (taskOutcomeObservationPayload, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return taskOutcomeObservationPayload{}, false
	}
	// Prefer structured JSON payloads (object or JSON code block) when present.
	if strings.HasPrefix(text, "{") || strings.Contains(text, "```") {
		if payload, ok := decodeObservedTaskOutcomePayloadFromJSONBytes([]byte(text)); ok {
			return payload, true
		}
		if outcome, err := ParseTaskOutcomeContract(text); err == nil {
			return taskOutcomeObservationPayload{
				Status:    string(outcome.Status),
				Outcome:   string(outcome.Status),
				Summary:   outcome.Summary,
				Blocker:   outcome.Blocker,
				HandoffTo: outcome.HandoffTo,
			}, true
		}
	}
	// Cache-safe summary lines from report_task_outcome / block_current_task.
	if payload, ok := decodeObservedTaskOutcomePayloadFromCacheSafeSummary(text); ok {
		return payload, true
	}
	if payload, ok := decodeObservedTaskOutcomePayloadFromJSONBytes([]byte(text)); ok {
		return payload, true
	}
	return taskOutcomeObservationPayload{}, false
}

func decodeObservedTaskOutcomePayloadFromCacheSafeSummary(text string) (taskOutcomeObservationPayload, bool) {
	var payload taskOutcomeObservationPayload
	found := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		switch {
		case strings.HasPrefix(lower, "task outcome:"):
			value := strings.TrimSpace(trimmed[len("Task outcome:"):])
			payload.Outcome = value
			payload.Status = value
			found = true
		case strings.HasPrefix(lower, "summary:"):
			payload.Summary = strings.TrimSpace(trimmed[len("Summary:"):])
			found = true
		case strings.HasPrefix(lower, "blocker:"):
			payload.Blocker = strings.TrimSpace(trimmed[len("Blocker:"):])
			found = true
		case strings.HasPrefix(lower, "handoff to:"):
			payload.HandoffTo = strings.TrimSpace(trimmed[len("Handoff to:"):])
			found = true
		}
	}
	if !found {
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
