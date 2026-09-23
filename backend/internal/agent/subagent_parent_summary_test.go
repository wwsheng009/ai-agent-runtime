package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
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
			// Distinct conclusions: identical ones would be folded by the P2-1
			// dedup pass and this test is about budget omission, not merging.
			Summary: "report " + strconv.Itoa(index) + " " + strings.Repeat("large summary ", 100),
		})
	}

	summary := summarizeSubagentReportsForParent(reports, 1024)
	encoded, err := json.Marshal(summary.Reports)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), 1024)
	require.True(t, summary.Truncated)
	require.Positive(t, summary.Omitted)
	// H4: reports that do not fit are represented by a pointer, so the parent
	// keeps a recovery path instead of losing the child entirely.
	require.NotEmpty(t, summary.OmittedRefs)
	require.LessOrEqual(t, len(summary.OmittedRefs), maxSubagentParentOmittedRefs)
	for _, ref := range summary.OmittedRefs {
		require.Contains(t, ref, "read_agent_result(id=")
	}
}

// TestSummarizeSubagentReportsForParentKeepsEveryReportReachable pins the H4
// invariant: within the pointer cap, each report is either present as a stub or
// has an omitted-ref pointer — never silently absent from both.
func TestSummarizeSubagentReportsForParentKeepsEveryReportReachable(t *testing.T) {
	reports := make([]SubagentResult, 0, 12)
	for index := 0; index < 12; index++ {
		reports = append(reports, SubagentResult{
			ID:        "child-" + strconv.Itoa(index),
			SessionID: "session-" + strconv.Itoa(index),
			Success:   true,
			Summary:   "report " + strconv.Itoa(index) + " " + strings.Repeat("large summary ", 200),
		})
	}

	summary := summarizeSubagentReportsForParent(reports, 2048)
	require.True(t, summary.Truncated)
	require.Positive(t, summary.Omitted)
	require.LessOrEqual(t, summary.Omitted, maxSubagentParentOmittedRefs)
	// Every input report is projected, pointed to, or folded into an identical
	// sibling — never silently absent from all three (H4 + P2-1).
	require.Equal(t, len(reports), len(summary.Reports)+len(summary.OmittedRefs)+summary.Deduplicated)
}

// TestSummarizeSubagentReportsForParentFoldsIdenticalConclusions pins the P2-1
// dedup half: children that reached the same conclusion occupy one projection
// and the merged sources stay named.
func TestSummarizeSubagentReportsForParentFoldsIdenticalConclusions(t *testing.T) {
	conclusion := "The failing test is caused by a stale golden file in the fixture directory."
	reports := []SubagentResult{
		{ID: "child-a", SessionID: "session-a", Success: true, Summary: conclusion},
		{ID: "child-b", SessionID: "session-b", Success: true, Summary: conclusion},
		{ID: "child-c", SessionID: "session-c", Success: true, Summary: "A different conclusion about an unrelated subsystem."},
	}

	summary := summarizeSubagentReportsForParent(reports, 8192)
	require.Equal(t, 1, summary.Deduplicated)
	require.Zero(t, summary.Conflicts)
	require.Len(t, summary.Reports, 2)
	require.Equal(t, "child-a", summary.Reports[0]["id"])
	require.Equal(t, []string{"session-b"}, summary.Reports[0]["duplicate_sources"])
	require.Equal(t, 1, summary.Reports[0]["duplicate_count"])
}

// TestSummarizeSubagentReportsForParentFlagsConflictingConclusions pins the
// other half: same subject, different conclusion → both stay side by side in
// declaration order, cross-referenced and never arbitrated.
func TestSummarizeSubagentReportsForParentFlagsConflictingConclusions(t *testing.T) {
	reports := []SubagentResult{
		{ID: "child-a", SessionID: "session-a", Success: true,
			Summary: "The migration is safe to land.\nAll 42 fixtures pass on the branch."},
		{ID: "child-b", SessionID: "session-b", Success: true,
			Summary: "The migration is safe to land.\nBut two fixtures fail once the index is rebuilt."},
	}

	summary := summarizeSubagentReportsForParent(reports, 8192)
	require.Zero(t, summary.Deduplicated)
	require.Equal(t, 2, summary.Conflicts)
	require.Len(t, summary.Reports, 2)
	require.Equal(t, "child-a", summary.Reports[0]["id"])
	require.Equal(t, "child-b", summary.Reports[1]["id"])
	require.Equal(t, true, summary.Reports[0]["conflict"])
	require.Equal(t, true, summary.Reports[1]["conflict"])
	require.Equal(t, []string{"session-b"}, summary.Reports[0]["conflict_sources"])
	require.Equal(t, []string{"session-a"}, summary.Reports[1]["conflict_sources"])
}

// TestSummarizeSubagentReportsForParentKeepsDeclarationOrder pins the P2-1
// ordering half: projections follow the input (task declaration) order instead
// of any completion order, so the same batch renders identically every time.
func TestSummarizeSubagentReportsForParentKeepsDeclarationOrder(t *testing.T) {
	reports := []SubagentResult{
		{ID: "task-1", SessionID: "session-1", Success: true, Summary: "First task concluded that the cache is cold."},
		{ID: "task-2", SessionID: "session-2", Success: true, Summary: "Second task concluded that the cache is warm."},
		{ID: "task-3", SessionID: "session-3", Success: true, Summary: "Third task concluded that the cache is unmeasured."},
	}

	summary := summarizeSubagentReportsForParent(reports, 8192)
	require.Len(t, summary.Reports, 3)
	for index, want := range []string{"task-1", "task-2", "task-3"} {
		require.Equal(t, want, summary.Reports[index]["id"])
	}
}

// TestSummarizeSubagentReportsForParentTruncatedSummaryKeepsDereferencePointer
// pins the H4 fix: a summary cut by the per-report budget must expose the read
// tool paging pointer instead of silently losing the tail.
func TestSummarizeSubagentReportsForParentTruncatedSummaryKeepsDereferencePointer(t *testing.T) {
	reports := []SubagentResult{{
		ID:        "reader-1",
		SessionID: "child-session-1",
		Success:   true,
		Summary:   strings.Repeat("deliverable body ", 500),
	}}

	summary := summarizeSubagentReportsForParent(reports, 4096)
	require.True(t, summary.Truncated)
	require.Len(t, summary.Reports, 1)
	projection := summary.Reports[0]
	require.Contains(t, projection["summary_next_action"], "read_agent_result(id=child-session-1")
	require.Greater(t, projection["summary_runes"], 0)
}

// C4-1: spawn_subagents 只有异步语义后，父会话拿到的是 batch 句柄，子任务的大段
// 输出不再经父工具消息回流（旧断言"父工具消息被截断 + artifact 归档"随内联阻塞
// 分支一起删除）。子报告回到父会话时的预算裁剪由
// summarizeSubagentReportsForParent 负责，见本文件上半部分的单元用例。
func TestSpawnSubagentsLargeResultStaysOutOfParentContext(t *testing.T) {
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
	batchStore, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "batches.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = batchStore.Close() })
	terminal := make(chan BatchTerminalLifecycle, 1)
	agent.SetSubagentBatchCoordinator(NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{
		Store:     batchStore,
		Scheduler: agent.GetSubagentScheduler(),
	}))
	agent.SetBatchLifecycleProjector(func(_ context.Context, event BatchTerminalLifecycle) error {
		terminal <- event
		return nil
	})

	largeChildOutput := "CHILD_OUTPUT_START\n" + strings.Repeat("evidence line with detailed context\n", 900) + "CHILD_OUTPUT_END"
	runtime := llm.NewLLMRuntime(nil)
	baseProvider := &SequenceLLMProvider{
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
			{Content: "Parent final.", Model: "test-model"},
		},
	}
	provider := &roleRoutingLLMProvider{
		SequenceLLMProvider: baseProvider,
		childResponses: []*llm.LLMResponse{
			{Content: largeChildOutput, Model: "test-model"},
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
	// 父 turn 不等子任务：派发 + 收尾两次父请求。
	require.Len(t, provider.requests, 2)

	var toolMessage *types.Message
	for index := range provider.requests[1].Messages {
		message := &provider.requests[1].Messages[index]
		if message.Role == "tool" {
			toolMessage = message
			break
		}
	}
	require.NotNil(t, toolMessage)
	// 父上下文只留句柄：大输出既不进父消息，也不进父 metadata。
	require.LessOrEqual(t, len(toolMessage.Content), 12*1024)
	require.Contains(t, toolMessage.Content, `"batch_id"`)
	require.Contains(t, toolMessage.Content, "continue_parent_turn")
	require.NotContains(t, toolMessage.Content, "CHILD_OUTPUT_END")
	require.NotContains(t, toolMessage.Content, "evidence line with detailed context")
	require.NotContains(t, toolMessage.Metadata, "subagent_reports")

	// 子任务照常跑完：大输出由子会话自己的输出通道处理，父侧只留句柄。
	select {
	case event := <-terminal:
		require.Equal(t, subagentbatch.BatchCompleted, event.Status)
		require.Equal(t, 1, event.CompletedCount)
	case <-time.After(5 * time.Second):
		t.Fatal("expected the detached batch to reach a terminal projection")
	}
	require.Len(t, provider.childRequestSnapshot(), 1)
}
