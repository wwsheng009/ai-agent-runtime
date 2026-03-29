package team

import (
	"context"
	"fmt"
	"strings"
)

// ContextDigest summarizes team state for prompt injection.
type ContextDigest struct {
	TeamID    string
	TaskID    string
	Summary   string
	TaskCount int
	MailCount int
	MateCount int
}

// ContextBuilder produces a compact digest of team state.
type ContextBuilder struct {
	Store Store
}

// NewContextBuilder creates a builder for team context.
func NewContextBuilder(store Store) *ContextBuilder {
	return &ContextBuilder{Store: store}
}

// Build returns a digest summary for the given team/task.
func (b *ContextBuilder) Build(ctx context.Context, teamID, taskID string, budget int) (*ContextDigest, error) {
	if b == nil || b.Store == nil {
		return nil, nil
	}

	teamID = strings.TrimSpace(teamID)
	taskID = strings.TrimSpace(taskID)

	var currentTask *Task
	if taskID != "" {
		task, err := b.Store.GetTask(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if task != nil {
			currentTask = task
			if teamID == "" {
				teamID = task.TeamID
			}
		}
	}

	if teamID == "" {
		return nil, nil
	}

	if budget <= 0 {
		budget = 6
	}

	teammates, err := b.Store.ListTeammates(ctx, teamID)
	if err != nil {
		return nil, err
	}
	showMateCounts := budget > 4

	statuses := []TaskStatus{TaskStatusRunning, TaskStatusReady, TaskStatusBlocked}
	tasks, err := b.Store.ListTasks(ctx, TaskFilter{
		TeamID: teamID,
		Status: statuses,
		Limit:  budget,
	})
	if err != nil {
		return nil, err
	}

	mailLimit := budget / 2
	if mailLimit < 2 {
		mailLimit = 2
	}
	if mailLimit > 4 {
		mailLimit = 4
	}
	messages, err := b.Store.ListMail(ctx, MailFilter{
		TeamID:     teamID,
		UnreadOnly: true,
		Limit:      mailLimit,
	})
	if err != nil {
		return nil, err
	}

	lines := []string{"Team context:"}
	if showMateCounts && len(teammates) > 0 {
		idleCount := 0
		busyCount := 0
		blockedCount := 0
		offlineCount := 0
		for _, mate := range teammates {
			switch mate.State {
			case TeammateStateIdle:
				idleCount++
			case TeammateStateBusy:
				busyCount++
			case TeammateStateBlocked:
				blockedCount++
			case TeammateStateOffline:
				offlineCount++
			}
		}
		lines = append(lines, fmt.Sprintf("- teammates: idle=%d busy=%d blocked=%d offline=%d total=%d", idleCount, busyCount, blockedCount, offlineCount, len(teammates)))
	}
	if currentTask != nil {
		lines = append(lines, formatTaskLine("current", *currentTask))
		if len(currentTask.Inputs) > 0 {
			lines = append(lines, fmt.Sprintf("- inputs: %s", formatInputList(currentTask.Inputs, 3)))
		}
	}
	for _, task := range tasks {
		if currentTask != nil && task.ID == currentTask.ID {
			continue
		}
		lines = append(lines, formatTaskLine("task", task))
	}
	for _, message := range messages {
		lines = append(lines, formatMailLine(message))
	}
	if len(lines) == 1 {
		return nil, nil
	}

	return &ContextDigest{
		TeamID:    teamID,
		TaskID:    taskID,
		Summary:   strings.Join(lines, "\n"),
		TaskCount: len(tasks),
		MailCount: len(messages),
		MateCount: len(teammates),
	}, nil
}

func formatTaskLine(prefix string, task Task) string {
	title := strings.TrimSpace(task.Title)
	if title == "" {
		title = task.Goal
	}
	title = truncateLine(title, 120)
	status := strings.TrimSpace(string(task.Status))
	assignee := ""
	if task.Assignee != nil && strings.TrimSpace(*task.Assignee) != "" {
		assignee = " @" + strings.TrimSpace(*task.Assignee)
	}
	if title == "" {
		title = task.ID
	}
	return fmt.Sprintf("- %s[%s]%s: %s", prefix, status, assignee, title)
}

func formatMailLine(message MailMessage) string {
	kind := strings.TrimSpace(message.Kind)
	if kind == "" {
		kind = "info"
	}
	from := strings.TrimSpace(message.FromAgent)
	to := strings.TrimSpace(message.ToAgent)
	header := fmt.Sprintf("%s", kind)
	if from != "" || to != "" {
		header = fmt.Sprintf("%s %s->%s", kind, firstNonEmptyString(from, "?"), firstNonEmptyString(to, "*"))
	}
	body := truncateLine(message.Body, 160)
	return fmt.Sprintf("- mail[%s]: %s", header, body)
}

func truncateLine(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" || limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func formatInputList(values []string, limit int) string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		cleaned = append(cleaned, value)
	}
	if len(cleaned) == 0 {
		return ""
	}
	if limit <= 0 || len(cleaned) <= limit {
		return truncateLine(strings.Join(cleaned, ", "), 160)
	}
	head := append([]string(nil), cleaned[:limit]...)
	head = append(head, "...")
	return truncateLine(strings.Join(head, ", "), 160)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// runMetaKey is the context key for RunMeta.
type runMetaKey struct{}

// WithRunMeta attaches RunMeta to the context.
func WithRunMeta(ctx context.Context, meta *RunMeta) context.Context {
	if meta == nil {
		return ctx
	}
	return context.WithValue(ctx, runMetaKey{}, meta.Clone())
}

// GetRunMeta retrieves RunMeta from the context.
func GetRunMeta(ctx context.Context) (*RunMeta, bool) {
	if ctx == nil {
		return nil, false
	}
	meta, ok := ctx.Value(runMetaKey{}).(*RunMeta)
	return meta, ok
}
