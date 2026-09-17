package skills

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// P0-4 改动 1/2 的 runtime-server（HTTP）宿主验收。CLI 宿主的同契约用例在
// cmd/aicli/commands/chat_supervision_result_tools_test.go；两个宿主都把结果
// 读取委托给 supervision.ResultSource + supervision.BuildReadResultPayload，
// 这里钉住 HTTP 侧的 wiring：
//   - Controller 只把 scope 交给 provider（模型不能指定 root scope）；
//   - include_results 必须透传到 SnapshotRequest（默认路径不触发结果读取）；
//   - 没有结果通道/没有记录时返回 no_result_recorded 而不是工具失败。
//
// 真实 batch store → provider 的读取链路由 internal/runtimeserver 的
// supervision_results_test.go 覆盖（api/skills 不能 import runtimeserver：
// runtimeserver 反向依赖本包，测试会形成 import cycle）。

// fakeResultDescendantProvider implements both supervision.DescendantProvider
// and supervision.ResultSource, mirroring the decorated provider the runtime
// wires in main.go.
type fakeResultDescendantProvider struct {
	states           []supervision.DescendantState
	record           supervision.AgentResultRecord
	found            bool
	loadErr          error
	plainCalls       int
	withResultsCalls int
	loadScopes       []supervision.Scope
	loadTargets      []string
}

func (p *fakeResultDescendantProvider) ListDescendants(_ context.Context, _ supervision.Scope) ([]supervision.DescendantState, error) {
	p.plainCalls++
	return p.states, nil
}

func (p *fakeResultDescendantProvider) ListDescendantsWithResults(_ context.Context, _ supervision.Scope) ([]supervision.DescendantState, error) {
	p.withResultsCalls++
	return p.states, nil
}

func (p *fakeResultDescendantProvider) ListDescendantResults(_ context.Context, _ supervision.Scope) (map[string]supervision.DescendantResult, error) {
	return nil, nil
}

func (p *fakeResultDescendantProvider) LoadAgentResult(_ context.Context, scope supervision.Scope, sessionID, taskID string) (supervision.AgentResultRecord, bool, error) {
	p.loadScopes = append(p.loadScopes, scope)
	p.loadTargets = append(p.loadTargets, sessionID+"|"+taskID)
	return p.record, p.found, p.loadErr
}

func TestHandlerSupervisionToolController_ReadAgentResult(t *testing.T) {
	handler, _ := newAPISupervisionToolTestHandler(t, "api-supervision-read-result")
	provider := &fakeResultDescendantProvider{
		found: true,
		record: supervision.AgentResultRecord{
			Source:    supervision.ResultSourceTaskResult,
			TaskID:    "task-1",
			SessionID: "worker-1",
			Status:    "succeeded",
			Success:   true,
			Summary:   "patched and verified",
			Findings:  []string{"finding one", "finding two"},
			Changes: []supervision.AgentResultChange{
				{Path: "backend/x.go", Summary: "fix", Status: "applied", ArtifactRefs: []string{"artifact://patch"}},
			},
			Artifacts: []string{"artifact://api"},
			Usage:     supervision.AgentResultUsage{TotalTokens: 99},
		},
	}
	handler.SetSupervisionDescendantProvider(provider)

	controller := newHandlerSupervisionToolController(handler)
	require.NotNil(t, controller)

	payload, err := controller.ReadAgentResult(context.Background(), "api-parent", toolbroker.ReadAgentResultArgs{SessionID: "worker-1", TaskID: "task-1"})
	require.NoError(t, err)
	require.Equal(t, supervision.ResultSourceTaskResult, payload.Source)
	require.Equal(t, "succeeded", payload.Status)
	require.Equal(t, "patched and verified", payload.Summary)
	require.Equal(t, []string{"finding one", "finding two"}, payload.Findings)
	require.Len(t, payload.Changes, 1)
	require.Equal(t, "applied", payload.Changes[0].Status)
	require.Contains(t, payload.Artifacts, "artifact://api")
	require.NotNil(t, payload.Usage)
	require.Equal(t, 99, payload.Usage.TotalTokens)

	require.Len(t, provider.loadScopes, 1)
	require.Equal(t, supervision.Scope{RootSessionID: "api-parent"}, provider.loadScopes[0],
		"scope comes from the host; the model cannot name it")
	require.Equal(t, []string{"worker-1|task-1"}, provider.loadTargets)

	// Missing record: the actionable contract, not a tool failure.
	provider.found = false
	missing, err := controller.ReadAgentResult(context.Background(), "api-parent", toolbroker.ReadAgentResultArgs{SessionID: "worker-missing"})
	require.NoError(t, err)
	require.Equal(t, supervision.ResultSourceNone, missing.Source)
	require.Equal(t, "no_result_recorded", missing.ErrorCode)
	require.Contains(t, missing.NextAction, "read_agent_events")
}

// TestHandlerSupervisionToolController_ReadAgentResultWithoutResultProvider:
// a host whose provider is not result-aware still returns the actionable
// no_result_recorded contract and never calls into a nil source.
func TestHandlerSupervisionToolController_ReadAgentResultWithoutResultProvider(t *testing.T) {
	handler, _ := newAPISupervisionToolTestHandler(t, "api-supervision-read-result-plain")
	handler.SetSupervisionDescendantProvider(&recordingAPIDescendantProvider{})
	controller := newHandlerSupervisionToolController(handler)
	require.NotNil(t, controller)

	payload, err := controller.ReadAgentResult(context.Background(), "api-parent", toolbroker.ReadAgentResultArgs{SessionID: "worker-1"})
	require.NoError(t, err)
	require.Equal(t, supervision.ResultSourceNone, payload.Source)
	require.Equal(t, "no_result_recorded", payload.ErrorCode)
	require.Contains(t, payload.NextAction, "wait_agent")
}

// TestHandlerSupervisionToolController_DescendantsIncludeResultsPassthrough
// pins the gate: include_results=false never asks the provider for result rows
// (byte-compat), true switches to the result-aware read.
func TestHandlerSupervisionToolController_DescendantsIncludeResultsPassthrough(t *testing.T) {
	handler, _ := newAPISupervisionToolTestHandler(t, "api-supervision-include-results")
	provider := &fakeResultDescendantProvider{states: []supervision.DescendantState{
		{
			Kind:             supervision.SubjectAgentSession,
			ID:               "worker-1",
			ExecutionStatus:  "succeeded",
			SupervisionState: supervision.SupervisionTerminated,
			ResultStatus:     "succeeded",
			ResultSummary:    "done",
			ArtifactRefs:     []string{"artifact://api"},
		},
	}}
	handler.SetSupervisionDescendantProvider(provider)
	controller := newHandlerSupervisionToolController(handler)

	plain, err := controller.SupervisionDescendants(context.Background(), "api-parent", toolbroker.SupervisionDescendantsArgs{})
	require.NoError(t, err)
	require.Len(t, plain.Descendants, 1)
	require.Empty(t, plain.Descendants[0].ResultStatus)
	require.Equal(t, 1, provider.plainCalls)
	require.Zero(t, provider.withResultsCalls)

	withResults, err := controller.SupervisionDescendants(context.Background(), "api-parent", toolbroker.SupervisionDescendantsArgs{IncludeResults: true})
	require.NoError(t, err)
	require.Equal(t, 1, provider.withResultsCalls, "include_results must reach the provider")
	require.Len(t, withResults.Descendants, 1)
	require.Equal(t, "succeeded", withResults.Descendants[0].ResultStatus)
	require.Equal(t, "done", withResults.Descendants[0].ResultSummary)
	require.Contains(t, withResults.Descendants[0].ArtifactRefs, "artifact://api")
}
