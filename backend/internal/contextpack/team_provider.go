package contextpack

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// TeamProvider injects team task/mailbox context into the context pack.
type TeamProvider struct {
	Builder *team.ContextBuilder
	Budget  int
}

// NewTeamProvider creates a TeamProvider with an optional budget.
func NewTeamProvider(builder *team.ContextBuilder, budget int) *TeamProvider {
	if budget <= 0 {
		budget = 6
	}
	return &TeamProvider{
		Builder: builder,
		Budget:  budget,
	}
}

// Name identifies the provider in the context pack.
func (p *TeamProvider) Name() string { return "team" }

// Build returns team digest content when team/task identifiers are supplied.
func (p *TeamProvider) Build(ctx context.Context, input *Input) (map[string]interface{}, error) {
	if p == nil || p.Builder == nil || input == nil {
		return nil, nil
	}
	teamID := strings.TrimSpace(input.TeamID)
	taskID := strings.TrimSpace(input.TaskID)
	if teamID == "" && taskID == "" {
		return nil, nil
	}

	digest, err := p.Builder.Build(ctx, teamID, taskID, p.Budget)
	if err != nil {
		return nil, err
	}
	if digest == nil || strings.TrimSpace(digest.Summary) == "" {
		return nil, nil
	}
	return map[string]interface{}{
		"team_id":    digest.TeamID,
		"task_id":    digest.TaskID,
		"summary":    digest.Summary,
		"task_count": digest.TaskCount,
		"mail_count": digest.MailCount,
		"mate_count": digest.MateCount,
	}, nil
}
