package team

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelrouting"
)

// LeadPlanner coordinates task decomposition and summary generation.
type LeadPlanner struct {
	Sessions      SessionClient
	Store         Store
	Mailbox       *MailboxService
	ContextBudget int
	AutoPersist   bool
}

type planTask struct {
	ID                  string   `json:"id"`
	Title               string   `json:"title"`
	Goal                string   `json:"goal"`
	Difficulty          string   `json:"difficulty,omitempty"`
	DifficultyRationale string   `json:"difficulty_rationale,omitempty"`
	TaskType            string   `json:"task_type,omitempty"`
	TaskSubject         string   `json:"task_subject,omitempty"`
	Inputs              []string `json:"inputs,omitempty"`
	ReadPaths           []string `json:"read_paths,omitempty"`
	WritePaths          []string `json:"write_paths,omitempty"`
	Deliverables        []string `json:"deliverables,omitempty"`
	Priority            int      `json:"priority,omitempty"`
	Assignee            string   `json:"assignee,omitempty"`
}

type planDependency struct {
	Task      string `json:"task"`
	DependsOn string `json:"depends_on"`
}

type planPayload struct {
	Tasks        []planTask       `json:"tasks"`
	Dependencies []planDependency `json:"dependencies,omitempty"`
	Summary      string           `json:"summary,omitempty"`
}

// PlanResult captures planning output.
type PlanResult struct {
	Tasks        []Task
	Dependencies []TaskDependency
	Summary      string
}

// atomicTaskPlanStore is implemented by stores that can materialize a
// replan and fence it against the failed task version in one transaction.
// Stores without this capability must not be used for a versioned replan.
type atomicTaskPlanStore interface {
	PersistTaskPlanWithVersion(
		ctx context.Context,
		failedTaskID string,
		expectedVersion int64,
		tasks []Task,
		dependencies []TaskDependency,
	) error
}

// InitialPlan asks the lead to produce an initial DAG plan.
func (p *LeadPlanner) InitialPlan(ctx context.Context, team Team, goal string) (*PlanResult, error) {
	if p == nil || p.Sessions == nil {
		return nil, fmt.Errorf("lead planner sessions are not configured")
	}
	teamID := strings.TrimSpace(team.ID)
	if teamID == "" {
		return nil, fmt.Errorf("team id is required")
	}
	leadSession := strings.TrimSpace(team.LeadSessionID)
	if leadSession == "" {
		return nil, fmt.Errorf("lead session id is required")
	}

	prompt := buildPlanPrompt(goal, nil, p.buildTeamContext(ctx, teamID, ""))
	result, err := p.Sessions.SubmitPrompt(ctx, leadSession, prompt, buildLeadRunMeta(teamID, ""))
	if err != nil {
		return nil, WrapSessionExecutionError(err, result)
	}
	payload, err := parsePlanPayload(result)
	if err != nil {
		return nil, err
	}
	return p.materializePlan(ctx, teamID, payload)
}

// ReplanOnFailure asks the lead to generate additional tasks after a failure.
func (p *LeadPlanner) ReplanOnFailure(ctx context.Context, team Team, failed Task) (*PlanResult, error) {
	return p.replanOnFailure(ctx, team, failed, 0)
}

// ReplanOnFailureWithVersion asks the lead to generate additional tasks after a
// failure and atomically persists them only when the failed task still has the
// caller's expected version.
func (p *LeadPlanner) ReplanOnFailureWithVersion(ctx context.Context, team Team, failed Task, expectedVersion int64) (*PlanResult, error) {
	if expectedVersion <= 0 {
		return nil, fmt.Errorf("expected task version is required for versioned replan")
	}
	return p.replanOnFailure(ctx, team, failed, expectedVersion)
}

func (p *LeadPlanner) replanOnFailure(ctx context.Context, team Team, failed Task, expectedVersion int64) (*PlanResult, error) {
	if p == nil || p.Sessions == nil {
		return nil, fmt.Errorf("lead planner sessions are not configured")
	}
	if p.AutoPersist && p.Store != nil && expectedVersion <= 0 {
		return nil, fmt.Errorf("expected task version is required for auto-persisted replan")
	}
	teamID := strings.TrimSpace(team.ID)
	if teamID == "" {
		teamID = strings.TrimSpace(failed.TeamID)
	}
	if teamID == "" {
		return nil, fmt.Errorf("team id is required")
	}
	if failedTeamID := strings.TrimSpace(failed.TeamID); failedTeamID != "" &&
		!strings.EqualFold(failedTeamID, teamID) {
		return nil, fmt.Errorf("failed task does not belong to team: %s", strings.TrimSpace(failed.ID))
	}
	leadSession := strings.TrimSpace(team.LeadSessionID)
	if leadSession == "" {
		return nil, fmt.Errorf("lead session id is required")
	}

	prompt := buildPlanPrompt("", &failed, p.buildTeamContext(ctx, teamID, failed.ID))
	result, err := p.Sessions.SubmitPrompt(ctx, leadSession, prompt, buildLeadRunMeta(teamID, failed.ID))
	if err != nil {
		return nil, WrapSessionExecutionError(err, result)
	}
	payload, err := parsePlanPayload(result)
	if err != nil {
		return nil, err
	}
	return p.materializePlanWithFence(ctx, teamID, payload, failed.ID, expectedVersion)
}

// FinalSummary returns a team summary, optionally using the lead session.
func (p *LeadPlanner) FinalSummary(ctx context.Context, teamID string) (string, error) {
	result, err := p.FinalSummaryDetailed(ctx, teamID)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", nil
	}
	return strings.TrimSpace(result.Summary), nil
}

// FinalSummaryDetailed returns the final summary together with whether the lead
// summary path succeeded or a structured fallback path was used instead.
func (p *LeadPlanner) FinalSummaryDetailed(ctx context.Context, teamID string) (*FinalSummaryResult, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, fmt.Errorf("team id is required")
	}
	if p == nil {
		return nil, fmt.Errorf("lead planner is nil")
	}
	var (
		team  *Team
		tasks []Task
	)
	if p.Store != nil {
		loaded, err := p.Store.GetTeam(ctx, teamID)
		if err != nil {
			return nil, err
		}
		team = loaded
		list, err := p.Store.ListTasks(ctx, TaskFilter{TeamID: teamID})
		if err != nil {
			return nil, err
		}
		tasks = list
	}

	fallback := buildSummaryFallback(teamID, tasks)
	if p.Sessions == nil {
		return &FinalSummaryResult{
			Summary:        fallback,
			SummarySource:  FinalSummarySourceFallback,
			UsedFallback:   true,
			FallbackReason: FinalSummaryFallbackSessionsNotConfigured,
		}, nil
	}
	if team == nil {
		return &FinalSummaryResult{
			Summary:        fallback,
			SummarySource:  FinalSummarySourceFallback,
			UsedFallback:   true,
			FallbackReason: FinalSummaryFallbackTeamNotAvailable,
		}, nil
	}
	leadSessionID := strings.TrimSpace(team.LeadSessionID)
	if leadSessionID == "" {
		return &FinalSummaryResult{
			Summary:        fallback,
			SummarySource:  FinalSummarySourceFallback,
			UsedFallback:   true,
			FallbackReason: FinalSummaryFallbackLeadSessionMissing,
		}, nil
	}

	prompt := buildSummaryPrompt(teamID, tasks, p.buildTeamContext(ctx, teamID, ""))
	result, err := p.Sessions.SubmitPrompt(ctx, leadSessionID, prompt, buildLeadRunMeta(teamID, ""))
	if err != nil {
		detailed := &FinalSummaryResult{
			Summary:        fallback,
			SummarySource:  FinalSummarySourceFallback,
			UsedFallback:   true,
			FallbackReason: FinalSummaryFallbackLeadSessionError,
		}
		applyFinalSummarySessionError(detailed, buildFinalSummarySessionError(err, result))
		return detailed, nil
	}
	if result != nil && strings.TrimSpace(result.Output) != "" {
		return &FinalSummaryResult{
			Summary:       strings.TrimSpace(result.Output),
			SummarySource: FinalSummarySourceLead,
			TraceID:       strings.TrimSpace(result.TraceID),
		}, nil
	}
	detailed := &FinalSummaryResult{
		Summary:        fallback,
		SummarySource:  FinalSummarySourceFallback,
		UsedFallback:   true,
		FallbackReason: FinalSummaryFallbackLeadOutputEmpty,
		TraceID:        strings.TrimSpace(result.TraceID),
	}
	applyFinalSummarySessionError(detailed, buildFinalSummarySessionError(nil, result))
	return detailed, nil
}

func buildFinalSummarySessionError(err error, result *SessionResult) error {
	if err != nil {
		return WrapSessionExecutionError(err, result)
	}
	if result == nil {
		return nil
	}
	if strings.TrimSpace(result.Error) == "" && strings.TrimSpace(result.ErrorType) == "" && len(result.ErrorMetadata) == 0 {
		return nil
	}
	return WrapSessionExecutionError(fmt.Errorf("%s", firstNonEmptyString(strings.TrimSpace(result.Error), "lead summary generation failed")), result)
}

func applyFinalSummarySessionError(target *FinalSummaryResult, err error) {
	if target == nil || err == nil {
		return
	}
	target.SessionError = err
	if sessionErr, ok := AsSessionExecutionError(err); ok && sessionErr != nil {
		target.TraceID = strings.TrimSpace(sessionErr.TraceID)
		target.ErrorType = strings.TrimSpace(sessionErr.ErrorType)
		target.ErrorMetadata = sessionErr.CloneMetadata()
		return
	}
	target.TraceID = ""
	target.ErrorType = ""
	target.ErrorMetadata = nil
}

func (p *LeadPlanner) materializePlan(ctx context.Context, teamID string, payload *planPayload) (*PlanResult, error) {
	return p.materializePlanWithFence(ctx, teamID, payload, "", 0)
}

func (p *LeadPlanner) materializePlanWithFence(ctx context.Context, teamID string, payload *planPayload, failedTaskID string, expectedVersion int64) (*PlanResult, error) {
	if payload == nil || len(payload.Tasks) == 0 {
		return nil, fmt.Errorf("plan payload is empty")
	}
	planIDs := make(map[string]string, len(payload.Tasks))
	tasks := make([]Task, 0, len(payload.Tasks))
	for i, spec := range payload.Tasks {
		id := strings.TrimSpace(spec.ID)
		if id == "" {
			id = fmt.Sprintf("task_%d", i+1)
		}
		spec.Title = strings.TrimSpace(spec.Title)
		spec.Goal = strings.TrimSpace(spec.Goal)
		if spec.Goal == "" {
			spec.Goal = spec.Title
		}
		difficulty, ok := NormalizeTaskDifficulty(spec.Difficulty)
		if !ok {
			return nil, fmt.Errorf("invalid task difficulty: %s", strings.TrimSpace(spec.Difficulty))
		}
		if _, exists := planIDs[id]; exists {
			return nil, fmt.Errorf("duplicate plan task id: %s", id)
		}
		difficultyRationale := strings.TrimSpace(spec.DifficultyRationale)
		taskType := modelrouting.NormalizeTaskType(spec.TaskType)
		taskSubject := strings.TrimSpace(spec.TaskSubject)
		task := Task{
			ID:                  id,
			TeamID:              teamID,
			Title:               spec.Title,
			Goal:                spec.Goal,
			Difficulty:          difficulty,
			DifficultyRationale: difficultyRationale,
			TaskType:            taskType,
			TaskSubject:         taskSubject,
			Status:              TaskStatusPending,
			Priority:            spec.Priority,
			Inputs:              cleanStringSlice(spec.Inputs),
			ReadPaths:           cleanStringSlice(spec.ReadPaths),
			WritePaths:          cleanStringSlice(spec.WritePaths),
			Deliverables:        cleanStringSlice(spec.Deliverables),
		}
		if strings.TrimSpace(spec.Assignee) != "" {
			assignee := strings.TrimSpace(spec.Assignee)
			task.Assignee = &assignee
		}
		planIDs[id] = id
		tasks = append(tasks, task)
	}

	dependencies := make([]TaskDependency, 0, len(payload.Dependencies))
	for _, dep := range payload.Dependencies {
		rawTaskID := strings.TrimSpace(dep.Task)
		rawDependsOnID := strings.TrimSpace(dep.DependsOn)
		if rawTaskID == "" || rawDependsOnID == "" {
			return nil, fmt.Errorf("plan dependency task and depends_on ids are required")
		}
		taskID := resolvePlanID(rawTaskID, planIDs)
		if taskID == "" {
			return nil, fmt.Errorf("plan dependency task is not in the generated plan: %s", rawTaskID)
		}
		depID := resolvePlanID(rawDependsOnID, planIDs)
		if depID == "" {
			return nil, fmt.Errorf("plan dependency prerequisite is not in the generated plan: %s", rawDependsOnID)
		}
		dependencies = append(dependencies, TaskDependency{
			TaskID:      taskID,
			DependsOnID: depID,
		})
	}

	if p.Store != nil && p.AutoPersist && expectedVersion > 0 {
		if atomicStore, ok := p.Store.(atomicTaskPlanStore); ok {
			if strings.TrimSpace(failedTaskID) == "" {
				return nil, fmt.Errorf("failed task id is required for versioned replan")
			}
			if err := atomicStore.PersistTaskPlanWithVersion(
				ctx,
				strings.TrimSpace(failedTaskID),
				expectedVersion,
				tasks,
				dependencies,
			); err != nil {
				return nil, err
			}
			for i := range tasks {
				persisted, err := p.Store.GetTask(ctx, tasks[i].ID)
				if err != nil {
					return nil, fmt.Errorf("reload persisted replan task %s: %w", tasks[i].ID, err)
				}
				if persisted == nil {
					return nil, fmt.Errorf("persisted replan task not found: %s", tasks[i].ID)
				}
				tasks[i] = *persisted
			}
			return &PlanResult{
				Tasks:        tasks,
				Dependencies: dependencies,
				Summary:      strings.TrimSpace(payload.Summary),
			}, nil
		}
		return nil, agentcontrol.ErrTaskVersionUnsupported
	}

	if p.Store != nil && p.AutoPersist {
		taskWriter := NewAgentControlTaskRegistry(p.Store)
		for i := range tasks {
			assignee := ""
			if tasks[i].Assignee != nil {
				assignee = strings.TrimSpace(*tasks[i].Assignee)
			}
			created, err := taskWriter.CreateAgentControlTask(ctx, agentcontrol.TaskCreateRequest{
				ID:                  tasks[i].ID,
				Workflow:            agentcontrol.WorkflowSpawnTeam,
				TeamID:              tasks[i].TeamID,
				ParentTaskID:        derefString(tasks[i].ParentTaskID),
				Title:               tasks[i].Title,
				Goal:                tasks[i].Goal,
				Difficulty:          tasks[i].Difficulty,
				DifficultyRationale: tasks[i].DifficultyRationale,
				TaskType:            tasks[i].TaskType,
				TaskSubject:         tasks[i].TaskSubject,
				Status:              string(tasks[i].Status),
				Priority:            tasks[i].Priority,
				Assignee:            assignee,
				Inputs:              tasks[i].Inputs,
				ReadPaths:           tasks[i].ReadPaths,
				WritePaths:          tasks[i].WritePaths,
				Deliverables:        tasks[i].Deliverables,
				Summary:             tasks[i].Summary,
			})
			if err != nil {
				return nil, err
			}
			if created == nil {
				return nil, fmt.Errorf("agent control task create returned nil record")
			}
			tasks[i].ID = created.ID
		}
	}

	var dependencyWriter AgentControlTaskRegistry
	if p.Store != nil && p.AutoPersist {
		dependencyWriter = NewAgentControlTaskRegistry(p.Store)
	}
	for i := range dependencies {
		if p.Store != nil && p.AutoPersist {
			if err := dependencyWriter.CreateAgentControlTaskDependency(ctx, agentcontrol.TaskDependencyCreateRequest{
				Workflow:    agentcontrol.WorkflowSpawnTeam,
				TeamID:      teamID,
				TaskID:      dependencies[i].TaskID,
				DependsOnID: dependencies[i].DependsOnID,
			}); err != nil {
				return nil, fmt.Errorf("persist task dependency %s -> %s: %w", dependencies[i].TaskID, dependencies[i].DependsOnID, err)
			}
		}
	}
	return &PlanResult{
		Tasks:        tasks,
		Dependencies: dependencies,
		Summary:      strings.TrimSpace(payload.Summary),
	}, nil
}

func buildPlanPrompt(goal string, failed *Task, teamContext string) string {
	lines := []string{
		"You are the team lead. Decompose the goal into a DAG plan.",
		"Return JSON only with the following schema:",
		`{"tasks":[{"id":"task-1","title":"...","goal":"...","difficulty":"normal","difficulty_rationale":"...","task_type":"implement","task_subject":"...","inputs":[],"read_paths":[],"write_paths":[],"deliverables":[],"priority":0,"assignee":""}],"dependencies":[{"task":"task-2","depends_on":"task-1"}]}`,
		"Rules:",
		"- Use stable task ids within the JSON.",
		"- Keep tasks atomic and outcome-focused.",
		"- Set difficulty to one of easy, normal, hard, expert for every task.",
		"- Keep difficulty_rationale short and focused on routing/audit context.",
		"- Set task_type to one of " + strings.Join(modelrouting.TaskTypes(), ", ") + " for every task; omit it only if none applies.",
		"- Classify task_type by the standard a competent engineer would apply; never misreport the category to obtain a different model.",
		"- Keep task_subject short (a few words) and focused on audit context.",
		"- Leave assignee empty unless a teammate is explicitly required.",
	}
	if strings.TrimSpace(goal) != "" {
		lines = append(lines, "", "Goal:", strings.TrimSpace(goal))
	}
	if failed != nil {
		lines = append(lines, "", "Failed task context:")
		lines = append(lines, fmt.Sprintf("- Title: %s", firstNonEmptyString(failed.Title, failed.Goal, failed.ID)))
		if failed.Summary != "" {
			lines = append(lines, fmt.Sprintf("- Summary: %s", failed.Summary))
		}
		if failed.RetryCount > 0 {
			lines = append(lines, fmt.Sprintf("- Retry count: %d", failed.RetryCount))
		}
	}
	if strings.TrimSpace(teamContext) != "" {
		lines = append(lines, "", teamContext)
	}
	return strings.Join(lines, "\n")
}

func (p *LeadPlanner) buildTeamContext(ctx context.Context, teamID, taskID string) string {
	if p == nil || p.Store == nil {
		return ""
	}
	budget := p.ContextBudget
	if budget <= 0 {
		budget = 6
	}
	digest, err := NewContextBuilder(p.Store).Build(ctx, teamID, taskID, budget)
	if err != nil || digest == nil {
		return ""
	}
	return strings.TrimSpace(digest.Summary)
}

func buildSummaryPrompt(teamID string, tasks []Task, teamContext string) string {
	lines := []string{
		"You are the team lead. Provide a concise final summary for the user.",
		"Highlight completed tasks, unresolved blockers, and next steps.",
		fmt.Sprintf("Team ID: %s", teamID),
		"",
		"Tasks:",
	}
	for _, task := range tasks {
		title := firstNonEmptyString(task.Title, task.Goal, task.ID)
		status := string(task.Status)
		summary := truncateLine(task.Summary, 160)
		if summary != "" {
			lines = append(lines, fmt.Sprintf("- [%s] %s: %s", status, title, summary))
		} else {
			lines = append(lines, fmt.Sprintf("- [%s] %s", status, title))
		}
	}
	if strings.TrimSpace(teamContext) != "" {
		lines = append(lines, "", teamContext)
	}
	return strings.Join(lines, "\n")
}

func buildLeadRunMeta(teamID, taskID string) *RunMeta {
	teamID = strings.TrimSpace(teamID)
	taskID = strings.TrimSpace(taskID)
	if teamID == "" && taskID == "" {
		return nil
	}
	return &RunMeta{
		PermissionMode:        "bypass_permissions",
		CompletionRequirement: "none",
		Team: &TeamRunMeta{
			TeamID:        teamID,
			CurrentTaskID: taskID,
		},
	}
}

func buildSummaryFallback(teamID string, tasks []Task) string {
	if len(tasks) == 0 {
		return fmt.Sprintf("Team %s completed. No task summary available.", teamID)
	}
	lines := []string{fmt.Sprintf("Team %s summary:", teamID)}
	for _, task := range tasks {
		title := firstNonEmptyString(task.Title, task.Goal, task.ID)
		status := string(task.Status)
		summary := truncateLine(task.Summary, 140)
		if summary != "" {
			lines = append(lines, fmt.Sprintf("- [%s] %s: %s", status, title, summary))
		} else {
			lines = append(lines, fmt.Sprintf("- [%s] %s", status, title))
		}
	}
	return strings.Join(lines, "\n")
}

func parsePlanPayload(result *SessionResult) (*planPayload, error) {
	if result == nil {
		return nil, fmt.Errorf("session result is nil")
	}
	raw := strings.TrimSpace(result.Output)
	if raw == "" {
		return nil, fmt.Errorf("plan output is empty")
	}
	raw = extractJSONBlock(raw)
	var payload planPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, fmt.Errorf("parse plan output: %w", err)
	}
	if len(payload.Tasks) == 0 {
		return nil, fmt.Errorf("plan payload contains no tasks")
	}
	return &payload, nil
}

func extractJSONBlock(raw string) string {
	lower := strings.ToLower(raw)
	if start := strings.Index(lower, "```json"); start >= 0 {
		trimmed := raw[start+7:]
		if end := strings.Index(trimmed, "```"); end >= 0 {
			return strings.TrimSpace(trimmed[:end])
		}
	}
	if start := strings.Index(raw, "{"); start >= 0 {
		if end := strings.LastIndex(raw, "}"); end > start {
			return strings.TrimSpace(raw[start : end+1])
		}
	}
	return raw
}

func cleanStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		cleaned = append(cleaned, value)
	}
	return cleaned
}

func resolvePlanID(planID string, mapIDs map[string]string) string {
	if planID == "" {
		return ""
	}
	if len(mapIDs) == 0 {
		return planID
	}
	if actual, ok := mapIDs[planID]; ok {
		return actual
	}
	return ""
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
