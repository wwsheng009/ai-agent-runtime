package runtimeserver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	skillsapi "github.com/wwsheng009/ai-agent-runtime/internal/api/skills"
)

func TestLocalConfigDocumentServicePreviewAgentRouteUsesDraftWithoutPersisting(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initial := "aicli:\n  subagents:\n    routing:\n      enabled: false\n"
	require.NoError(t, os.WriteFile(configPath, []byte(initial), 0o644))

	draft := `
providers:
  default_provider: parent
  items:
    parent:
      enabled: true
      default_model: parent-model
      supported_models: [parent-model]
    strong:
      enabled: true
      default_model: strong-model
      supported_models: [strong-model]
aicli:
  chat:
    default_provider: parent
    default_model: parent-model
    reasoning_effort: medium
  subagents:
    routing:
      enabled: true
      default_difficulty: normal
      inherit_parent_when_missing: true
      levels:
        hard:
          provider: strong
          model: strong-model
          reasoning_effort: high
`
	service := NewLocalConfigDocumentService(configPath)
	result, err := service.PreviewAgentRoute(skillsapi.AgentRoutePreviewRequest{
		Document: skillsapi.ConfigDocumentSaveRequest{Mode: "raw", Raw: &draft},
		Scope:    "subagent",
		Task:     skillsapi.AgentRoutePreviewTask{Difficulty: "hard"},
	})

	require.NoError(t, err)
	require.Equal(t, "subagent", result.Scope)
	require.Equal(t, "subagent", result.RoutingSource)
	require.True(t, result.RoutingEnabled)
	require.Equal(t, "parent", result.Parent.Provider)
	require.Equal(t, "parent-model", result.Parent.Model)
	require.Equal(t, "strong", result.Decision.Provider)
	require.Equal(t, "strong-model", result.Decision.Model)
	require.Equal(t, "high", result.Decision.ReasoningEffort)
	require.Equal(t, "difficulty_level", result.Decision.Source)

	raw, readErr := os.ReadFile(configPath)
	require.NoError(t, readErr)
	require.Equal(t, initial, string(raw))
}

func TestLocalConfigDocumentServicePreviewAgentRouteUsesInheritedTeamPolicy(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	draft := `
providers:
  default_provider: parent
  items:
    parent:
      enabled: true
      default_model: parent-model
      supported_models: [parent-model]
aicli:
  subagents:
    routing:
      enabled: true
      levels: {}
`
	require.NoError(t, os.WriteFile(configPath, []byte(draft), 0o644))
	service := NewLocalConfigDocumentService(configPath)

	result, err := service.PreviewAgentRoute(skillsapi.AgentRoutePreviewRequest{
		Document: skillsapi.ConfigDocumentSaveRequest{Mode: "raw", Raw: &draft},
		Scope:    "team",
		Task:     skillsapi.AgentRoutePreviewTask{Difficulty: "normal"},
	})

	require.NoError(t, err)
	require.Equal(t, "team", result.Scope)
	require.Equal(t, "subagent_inherited", result.RoutingSource)
}
