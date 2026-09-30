package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// W6：KnowledgeWrite 置位测试。
//   - 只读策略恒为读语义；写意图短语 → true；只读核验措辞 → false；
//     无法判定 → true（保守：更严阈值 + 强制验证读取）。
//   - 透传正/反例：循环的判定经 contextmgr.BuildInput 到达 Planner；off 零调用。

type fakeKnowledgeWritePlanner struct {
	calls     int
	lastInput knowledge.PlanInput
}

func (f *fakeKnowledgeWritePlanner) Plan(_ context.Context, in knowledge.PlanInput) (knowledge.Plan, error) {
	f.calls++
	f.lastInput = in
	return knowledge.Plan{Reason: knowledge.PlanReasonNoCandidates}, nil
}

func knowledgeWriteTestLoop(t *testing.T, readOnly bool) *ReActLoop {
	t.Helper()
	agent := &Agent{config: &Config{Name: "knowledge-write-test"}}
	if readOnly {
		agent.SetToolExecutionPolicy(NewToolExecutionPolicy([]string{"grep", "view"}, true))
	} else {
		agent.SetToolExecutionPolicy(NewToolExecutionPolicy([]string{"grep", "view", "write", "edit"}, false))
	}
	return NewReActLoop(agent, nil, &LoopReActConfig{})
}

func TestKnowledgeWriteIntent(t *testing.T) {
	writable := knowledgeWriteTestLoop(t, false)
	readOnly := knowledgeWriteTestLoop(t, true)

	cases := []struct {
		name string
		loop *ReActLoop
		goal string
		want bool
	}{
		{"write_intent_english", writable, "modify the config file", true},
		{"write_intent_chinese", writable, "创建文件并写入结果", true},
		{"strong_write_with_check", writable, "检查并修改 X 的代码", true},
		{"read_only_goal_english", writable, "inspect the config file and summarize", false},
		{"read_only_goal_chinese", writable, "复查工作区未提交改动，列出可疑点", false},
		{"unknown_goal_defaults_write", writable, "locate the runtime agent loop entry", true},
		{"read_only_policy_never_write", readOnly, "modify the config file", false},
		{"read_only_policy_read_goal", readOnly, "analyze the loop entry", false},
		{"nil_loop_defaults_write", nil, "anything", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.loop.knowledgeWriteIntent(tc.goal))
		})
	}
}

// 透传正/反例：loop 判定 → BuildInput.KnowledgeWrite → Planner.PlanInput.Write；
// off 档位零调用（无论写意图如何）。
func TestKnowledgeWriteIntentPassThrough(t *testing.T) {
	ctx := context.Background()
	input := contextmgr.BuildInput{
		TraceID:     "trace-kw",
		WorkspaceID: "ws-1",
		SessionID:   "session-kw",
		TaskID:      "task-1",
		Goal:        "modify the config file",
		History: []types.Message{
			*types.NewSystemMessage("system prompt"),
			*types.NewUserMessage("modify the config file"),
		},
	}

	// 正例：写意图 + 可写策略 → Planner 收到 Write=true。
	planner := &fakeKnowledgeWritePlanner{}
	manager := contextmgr.NewManager(contextmgr.DefaultBudget(), nil)
	manager.Knowledge = planner
	manager.Strategy.KnowledgeMode = contextmgr.KnowledgeModeSignals
	manager.Strategy.MinKnowledgeQueryLength = 4

	writable := knowledgeWriteTestLoop(t, false)
	input.KnowledgeWrite = writable.knowledgeWriteIntent(input.Goal)
	manager.Build(ctx, input)
	require.Equal(t, 1, planner.calls)
	require.True(t, planner.lastInput.Write, "写意图必须透传 Write=true")

	// 反例：只读策略 → Write=false（只读 run 无写能力）。
	readOnly := knowledgeWriteTestLoop(t, true)
	input.KnowledgeWrite = readOnly.knowledgeWriteIntent(input.Goal)
	manager.Build(ctx, input)
	require.Equal(t, 2, planner.calls)
	require.False(t, planner.lastInput.Write, "只读策略必须透传 Write=false")

	// off：零调用（门禁之外的基线不变式）。
	manager.Strategy.KnowledgeMode = contextmgr.KnowledgeModeOff
	input.KnowledgeWrite = writable.knowledgeWriteIntent(input.Goal)
	manager.Build(ctx, input)
	require.Equal(t, 2, planner.calls, "off 档位不得触达 Planner")
}
