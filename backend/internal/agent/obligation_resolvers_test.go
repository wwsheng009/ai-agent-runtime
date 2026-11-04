package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

type resolverProbeAgentSessions struct{}

func (resolverProbeAgentSessions) AgentSessionTerminal(context.Context, string) (bool, bool, error) {
	return true, true, nil
}

type resolverProbeTeams struct{}

func (resolverProbeTeams) TeamTerminal(context.Context, string) (bool, bool, error) {
	return true, true, nil
}

type resolverProbeJobs struct{}

func (resolverProbeJobs) BackgroundJobTerminal(context.Context, string) (bool, bool, error) {
	return true, true, nil
}

type resolverProbeExplicitJobs struct{}

func (resolverProbeExplicitJobs) BackgroundJobTerminal(context.Context, string) (bool, bool, error) {
	return false, true, nil
}

func TestNewReActLoopInheritsAgentObligationResolvers(t *testing.T) {
	ag := NewAgent(&Config{Name: "resolver-parent"}, nil)
	ag.SetObligationResolvers(subagentbatch.ObligationResolvers{
		AgentSessions:  resolverProbeAgentSessions{},
		Teams:          resolverProbeTeams{},
		BackgroundJobs: resolverProbeJobs{},
	})

	loop := NewReActLoop(ag, nil, &LoopReActConfig{})
	require.IsType(t, resolverProbeAgentSessions{}, loop.config.AgentSessionObligations)
	require.IsType(t, resolverProbeTeams{}, loop.config.TeamObligations)
	require.IsType(t, resolverProbeJobs{}, loop.config.BackgroundJobObligations)

	explicit := resolverProbeExplicitJobs{}
	loop = NewReActLoop(ag, nil, &LoopReActConfig{BackgroundJobObligations: explicit})
	require.Equal(t, explicit, loop.config.BackgroundJobObligations,
		"an explicit loop config resolver must win over the agent default")
}

func TestChildAgentBuildInheritsObligationResolvers(t *testing.T) {
	parent := NewAgent(&Config{
		Name:     "parent",
		Provider: "test-provider",
		Model:    "test-model",
	}, nil)
	parent.SetSubagentScheduler(NewSubagentScheduler(parent, SubagentSchedulerConfig{}))
	parent.SetObligationResolvers(subagentbatch.ObligationResolvers{
		AgentSessions:  resolverProbeAgentSessions{},
		Teams:          resolverProbeTeams{},
		BackgroundJobs: resolverProbeJobs{},
	})

	spec, err := (ChildAgentFactory{}).Build(context.Background(), ChildBuildRequest{
		Parent:  parent,
		Task:    SubagentTask{ID: "child-inherit", Goal: "inspect"},
		Options: SubagentRunOptions{ParentSessionID: "parent-session", Depth: 1},
		Config:  SubagentSchedulerConfig{},
	})
	require.NoError(t, err)
	require.IsType(t, resolverProbeAgentSessions{}, spec.LoopConfig.AgentSessionObligations)
	require.IsType(t, resolverProbeTeams{}, spec.LoopConfig.TeamObligations)
	require.IsType(t, resolverProbeJobs{}, spec.LoopConfig.BackgroundJobObligations)
	require.IsType(t, resolverProbeJobs{}, spec.Agent.GetObligationResolvers().BackgroundJobs)
}

func TestChildAgentBuildUsesDurableBatchSessionIdentity(t *testing.T) {
	parent := NewAgent(&Config{Name: "parent", Provider: "test-provider", Model: "test-model"}, nil)
	parent.SetSubagentScheduler(NewSubagentScheduler(parent, SubagentSchedulerConfig{}))

	spec, err := (ChildAgentFactory{}).Build(context.Background(), ChildBuildRequest{
		Parent: parent,
		Task:   SubagentTask{ID: "task-1", Goal: "resume"},
		Options: SubagentRunOptions{
			ParentSessionID: "parent-session",
			BatchID:         "batch-1",
			Depth:           1,
		},
		Config: SubagentSchedulerConfig{},
	})
	require.NoError(t, err)
	require.Equal(t, buildDurableSubagentSessionID("batch-1", "task-1"), spec.SessionID)

	// The durable row wins over a recomputation: a parked task must resume in
	// exactly the child session that owns its TurnSuspension row.
	spec, err = (ChildAgentFactory{}).Build(context.Background(), ChildBuildRequest{
		Parent: parent,
		Task:   SubagentTask{ID: "task-1", ChildSessionID: "subagent_persisted", Goal: "resume"},
		Options: SubagentRunOptions{
			ParentSessionID: "parent-session",
			BatchID:         "batch-1",
			Depth:           1,
		},
		Config: SubagentSchedulerConfig{},
	})
	require.NoError(t, err)
	require.Equal(t, "subagent_persisted", spec.SessionID)
}
