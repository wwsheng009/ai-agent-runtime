package skill

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRouterRouteDirectSkipsToolWorkflowByDefault(t *testing.T) {
	registry := NewRegistry(nil)
	workflow := &Skill{
		Name:        "shell_workflow",
		Description: "execute a shell workflow",
		Triggers:    []Trigger{{Type: "keyword", Values: []string{"shell command"}, Weight: 1}},
		Workflow: &Workflow{Steps: []WorkflowStep{{
			ID:   "run",
			Tool: "shell",
			Args: map[string]interface{}{"command": "{{prompt}}"},
		}}},
	}
	require.NoError(t, registry.Register(workflow))
	router := NewRouter(registry)
	prompt := "Running shell commands in a captured transcript"

	require.Len(t, router.Route(context.Background(), prompt), 1)
	require.Empty(t, router.RouteDirect(context.Background(), prompt))
}

func TestRouterRouteDirectKeepsPromptSkillsAndExplicitOptIn(t *testing.T) {
	registry := NewRegistry(nil)
	require.NoError(t, registry.Register(&Skill{
		Name:         "prompt_skill",
		Description:  "prompt-only skill",
		Triggers:     []Trigger{{Type: "keyword", Values: []string{"prompt route"}, Weight: 1}},
		SystemPrompt: "Handle the request.",
	}))
	direct := true
	require.NoError(t, registry.Register(&Skill{
		Name:        "opted_in_workflow",
		Description: "explicit direct workflow",
		Triggers:    []Trigger{{Type: "keyword", Values: []string{"direct workflow"}, Weight: 1}},
		DirectRoute: &direct,
		Workflow: &Workflow{Steps: []WorkflowStep{{
			ID:   "run",
			Tool: "echo",
		}}},
	}))
	router := NewRouter(registry)

	promptRoutes := router.RouteDirect(context.Background(), "use prompt route")
	require.Len(t, promptRoutes, 1)
	require.Equal(t, "prompt_skill", promptRoutes[0].Skill.Name)

	workflowRoutes := router.RouteDirect(context.Background(), "use direct workflow")
	require.Len(t, workflowRoutes, 1)
	require.Equal(t, "opted_in_workflow", workflowRoutes[0].Skill.Name)
}

func TestSkillSummaryPreservesDirectRoutePolicy(t *testing.T) {
	direct := true
	summary := SummaryFromSkill(&Skill{
		Name:        "summary_direct",
		Description: "summary policy",
		DirectRoute: &direct,
		Workflow:    &Workflow{Steps: []WorkflowStep{{ID: "run", Tool: "echo"}}},
	})
	require.NotNil(t, summary)
	require.NotNil(t, summary.DirectRoute)
	require.True(t, *summary.DirectRoute)

	stub := summary.ToSkillStub()
	require.NotNil(t, stub)
	require.True(t, stub.AllowsDirectRoute())
	*summary.DirectRoute = false
	require.True(t, stub.AllowsDirectRoute(), "stub must own an independent policy value")
}
