package team

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Goal         string   `json:"goal"`
	Inputs       []string `json:"inputs,omitempty"`
	ReadPaths    []string `json:"read_paths,omitempty"`
	WritePaths   []string `json:"write_paths,omitempty"`
	Deliverables []string `json:"deliverables,omitempty"`
	Priority     int      `json:"priority,omitempty"`
	Assignee     string   `json:"assignee,omitempty"`
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
		return nil, err
	}
	payload, err := parsePlanPayload(result)
	if err != nil {
		return nil, err
	}
	return p.materializePlan(ctx, teamID, payload)
}

// ReplanOnFailure asks the lead to generate additional tasks after a failure.
func (p *LeadPlanner) ReplanOnFailure(ctx context.Context, team Team, failed Task) (*PlanResult, error) {
	if p == nil || p.Sessions == nil {
		return nil, fmt.Errorf("lead planner sessions are not configured")
	}
	teamID := strings.TrimSpace(team.ID)
	if teamID == "" {
		teamID = strings.TrimSpace(failed.TeamID)
	}
	if teamID == "" {
		return nil, fmt.Errorf("team id is required")
	}
	leadSession := strings.TrimSpace(team.LeadSessionID)
	if leadSession == "" {
		return nil, fmt.Errorf("lead session id is required")
	}

	prompt := buildPlanPrompt("", &failed, p.buildTeamContext(ctx, teamID, failed.ID))
	result, err := p.Sessions.SubmitPrompt(ctx, leadSession, prompt, buildLeadRunMeta(teamID, failed.ID))
	if err != nil {
		return nil, err
	}
	payload, err := parsePlanPayload(result)
	if err != nil {
		return nil, err
	}
	return p.materializePlan(ctx, teamID, payload)
}

// FinalSummary returns a team summary, optionally using the lead session.
func (p *LeadPlanner) FinalSummary(ctx context.Context, teamID string) (string, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return "", fmt.Errorf("team id is required")
	}
	if p == nil {
		return "", fmt.Errorf("lead planner is nil")
	}
	var (
		team  *Team
		tasks []Task
	)
	if p.Store != nil {
		loaded, err := p.Store.GetTeam(ctx, teamID)
		if err != nil {
			return "", err
		}
		team = loaded
		list, err := p.Store.ListTasks(ctx, TaskFilter{TeamID: teamID})
		if err != nil {
			return "", err
		}
		tasks = list
	}

	fallback := buildSummaryFallback(teamID, tasks)
	if p.Sessions == nil || team == nil || strings.TrimSpace(team.LeadSessionID) == "" {
		return fallback, nil
	}

	prompt := buildSummaryPrompt(teamID, tasks, p.buildTeamContext(ctx, teamID, ""))
	result, err := p.Sessions.SubmitPrompt(ctx, team.LeadSessionID, prompt, buildLeadRunMeta(teamID, ""))
	if err != nil {
		return fallback, nil
	}
	if result != nil && strings.TrimSpace(result.Output) != "" {
		return strings.TrimSpace(result.Output), nil
	}
	return fallback, nil
}

func (p *LeadPlanner) materializePlan(ctx context.Context, teamID string, payload *planPayload) (*PlanResult, error) {
	if payload == nil || len(payload.Tasks) == 0 {
		return nil, fmt.Errorf("plan payload is empty")
	}
	planIDMap := make(map[string]int)
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
		task := Task{
			TeamID:       teamID,
			Title:        spec.Title,
			Goal:         spec.Goal,
			Status:       TaskStatusPending,
			Priority:     spec.Priority,
			Inputs:       cleanStringSlice(spec.Inputs),
			ReadPaths:    cleanStringSlice(spec.ReadPaths),
			WritePaths:   cleanStringSlice(spec.WritePaths),
			Deliverables: cleanStringSlice(spec.Deliverables),
		}
		if strings.TrimSpace(spec.Assignee) != "" {
			assignee := strings.TrimSpace(spec.Assignee)
			task.Assignee = &assignee
		}
		planIDMap[id] = len(tasks)
		tasks = append(tasks, task)
	}

	idMap := make(map[string]string)
	if p.Store != nil && p.AutoPersist {
		for i := range tasks {
			id, err := p.Store.CreateTask(ctx, tasks[i])
			if err != nil {
				return nil, err
			}
			planID := planKeyAtIndex(planIDMap, i)
			if planID != "" {
				idMap[planID] = id
			}
			tasks[i].ID = id
		}
	}

	dependencies := make([]TaskDependency, 0, len(payload.Dependencies))
	for _, dep := range payload.Dependencies {
		taskKey := strings.TrimSpace(dep.Task)
		depKey := strings.TrimSpace(dep.DependsOn)
		if taskKey == "" || depKey == "" {
			continue
		}
		taskID := resolvePlanID(taskKey, idMap)
		depID := resolvePlanID(depKey, idMap)
		if taskID == "" || depID == "" {
			continue
		}
		dependencies = append(dependencies, TaskDependency{
			TaskID:      taskID,
			DependsOnID: depID,
		})
		if p.Store != nil && p.AutoPersist {
			_ = p.Store.AddTaskDependency(ctx, taskID, depID)
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
		`{"tasks":[{"id":"task-1","title":"...","goal":"...","inputs":[],"read_paths":[],"write_paths":[],"deliverables":[],"priority":0,"assignee":""}],"dependencies":[{"task":"task-2","depends_on":"task-1"}]}`,
		"Rules:",
		"- Use stable task ids within the JSON.",
		"- Keep tasks atomic and outcome-focused.",
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
		PermissionMode: "bypass_permissions",
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

func planKeyAtIndex(mapping map[string]int, index int) string {
	for key, value := range mapping {
		if value == index {
			return key
		}
	}
	return ""
}
