package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestSummarizeSubagentReportsForParentBoundsMetadataAndDropsRawDiff(t *testing.T) {
	reports := []SubagentResult{
		{
			ID:        "reader-1",
			Role:      "researcher",
			SessionID: "child-session",
			ReadOnly:  true,
			Success:   true,
			Summary:   strings.Repeat("summary payload ", 1200),
			Findings: []string{
				strings.Repeat("finding one ", 400),
				strings.Repeat("finding two ", 400),
			},
			Patches: []FilePatch{
				{
					Path:         "backend/internal/agent/loop.go",
					Summary:      "bounded parent projection",
					Diff:         "RAW_DIFF_MUST_NOT_ENTER_PARENT_METADATA\n" + strings.Repeat("+line\n", 2000),
					ApplyStatus:  "applied",
					ArtifactRefs: []string{"art_patch_1"},
				},
			},
			Contract: &agentresult.Result{
				Status:  agentresult.StatusSucceeded,
				Summary: strings.Repeat("contract duplicate ", 1000),
			},
		},
	}

	summary := summarizeSubagentReportsForParent(reports, 2048)
	require.True(t, summary.Truncated)
	require.Zero(t, summary.Omitted)
	require.NotEmpty(t, summary.SHA256)
	require.Greater(t, summary.ByteCount, 2048)
	require.Len(t, summary.Reports, 1)

	encoded, err := json.Marshal(summary.Reports)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), 2048)
	require.NotContains(t, string(encoded), "RAW_DIFF_MUST_NOT_ENTER_PARENT_METADATA")
	require.Contains(t, string(encoded), "art_patch_1")
	require.Contains(t, string(encoded), `"truncated":true`)
}

func TestSummarizeSubagentReportsForParentOmitsEntriesBeyondBatchBudget(t *testing.T) {
	reports := make([]SubagentResult, 0, 40)
	for index := 0; index < 40; index++ {
		reports = append(reports, SubagentResult{
			ID:        strings.Repeat("child-id-", 30),
			Role:      "researcher",
			SessionID: strings.Repeat("session-id-", 20),
			Success:   true,
			Summary:   strings.Repeat("large summary ", 100),
		})
	}

	summary := summarizeSubagentReportsForParent(reports, 1024)
	encoded, err := json.Marshal(summary.Reports)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), 1024)
	require.True(t, summary.Truncated)
	require.Positive(t, summary.Omitted)
	require.Less(t, len(summary.Reports), len(reports))
}

func TestSpawnSubagentsLargeResultUsesBoundedParentMetadataAndArtifact(t *testing.T) {
	artifactPath := filepath.Join(t.TempDir(), "artifacts.db")
	agent := &Agent{
		config: &Config{
			Name:              "parent-agent",
			Model:             "test-provider",
			MaxSteps:          4,
			SystemPrompt:      "Parent system prompt.",
			ArtifactStorePath: artifactPath,
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		mcpManager:  &MockMCPManager{},
	}
	agent.SetSubagentScheduler(NewSubagentScheduler(agent, SubagentSchedulerConfig{
		MaxConcurrent: 1,
		MaxDepth:      1,
	}))
	t.Cleanup(func() {
		require.NoError(t, agent.Close())
	})

	largeChildOutput := "CHILD_OUTPUT_START\n" + strings.Repeat("evidence line with detailed context\n", 900) + "CHILD_OUTPUT_END"
	runtime := llm.NewLLMRuntime(nil)
	provider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "Delegate.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{
						ID:   "spawn-call",
						Name: "spawn_subagents",
						Args: map[string]interface{}{
							"agents": []interface{}{
								map[string]interface{}{
									"id":        "large-child",
									"goal":      "Inspect a large result.",
									"read_only": true,
								},
							},
						},
					},
				},
			},
			{Content: largeChildOutput, Model: "test-model"},
			{Content: "Parent final.", Model: "test-model"},
		},
	}
	require.NoError(t, runtime.RegisterProvider("test-provider", provider))

	loop := NewReActLoop(agent, runtime, &LoopReActConfig{
		MaxSteps:        4,
		EnableThought:   true,
		EnableToolCalls: true,
	})
	result, err := loop.Run(context.Background(), "Inspect the large child result.")
	require.NoError(t, err)
	require.True(t, result.Success)
	require.Len(t, provider.requests, 3)

	var toolMessage *types.Message
	for index := range provider.requests[2].Messages {
		message := &provider.requests[2].Messages[index]
		if message.Role == "tool" {
			toolMessage = message
			break
		}
	}
	require.NotNil(t, toolMessage)
	require.Contains(t, toolMessage.Content, "output truncated for history safety")
	require.LessOrEqual(t, len(toolMessage.Content), 12*1024)
	require.Contains(t, toolMessage.Content, "Full raw output artifact_id: art_")

	compactReports, err := json.Marshal(toolMessage.Metadata["subagent_reports"])
	require.NoError(t, err)
	require.LessOrEqual(t, len(compactReports), defaultSubagentParentMetadataBudgetBytes)
	require.NotContains(t, string(compactReports), "CHILD_OUTPUT_END")
	require.Equal(t, true, toolMessage.Metadata["subagent_reports_truncated"])

	artifactID, _ := toolMessage.Metadata["subagent_reports_artifact_id"].(string)
	require.NotEmpty(t, artifactID)
	record, err := agent.GetArtifactStore().Get(context.Background(), artifactID)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.Contains(t, record.Content, "CHILD_OUTPUT_END")
}
