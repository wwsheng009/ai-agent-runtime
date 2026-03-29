package team

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type staticSessionClient struct {
	result *SessionResult
	err    error
	prompt string
}

func (c *staticSessionClient) SubmitPrompt(ctx context.Context, sessionID, prompt string, runMeta *RunMeta) (*SessionResult, error) {
	c.prompt = prompt
	return c.result, c.err
}

type delayedSessionClient struct {
	delay  time.Duration
	result *SessionResult
}

func (c *delayedSessionClient) SubmitPrompt(ctx context.Context, sessionID, prompt string, runMeta *RunMeta) (*SessionResult, error) {
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	return c.result, nil
}

type updatingSessionClient struct {
	onSubmit func(ctx context.Context, sessionID, prompt string, runMeta *RunMeta)
	result   *SessionResult
	err      error
}

func (c *updatingSessionClient) SubmitPrompt(ctx context.Context, sessionID, prompt string, runMeta *RunMeta) (*SessionResult, error) {
	if c.onSubmit != nil {
		c.onSubmit(ctx, sessionID, prompt, runMeta)
	}
	return c.result, c.err
}

func TestTeammateRunnerMarksMissingStructuredOutcomeAsProtocolError(t *testing.T) {
	runner := &TeammateRunner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  "blocked: waiting on architecture review",
			},
		},
	}

	result, err := runner.StartTask(context.Background(), Team{ID: "team-1"}, Teammate{
		ID:        "mate-1",
		SessionID: "session-1",
	}, Task{
		ID:     "task-1",
		TeamID: "team-1",
		Title:  "task-1",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Blocked)
	assert.False(t, result.Structured)
	assert.False(t, result.Success)
	assert.Equal(t, TaskOutcomeFailed, result.Outcome)
	assert.Contains(t, result.ProtocolError, "missing structured task outcome")
	assert.Equal(t, result.ProtocolError, result.Summary)
	assert.False(t, result.Structured)
}

func TestTeammateRunnerParsesStructuredJSONOutcome(t *testing.T) {
	runner := &TeammateRunner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  "Work log\n```json\n{\"task_status\":\"handoff\",\"summary\":\"handoff to reviewer\",\"blocker\":\"need review\",\"handoff_to\":\"mate-2\"}\n```",
			},
		},
	}

	result, err := runner.StartTask(context.Background(), Team{ID: "team-1"}, Teammate{
		ID:        "mate-1",
		SessionID: "session-1",
	}, Task{
		ID:     "task-1",
		TeamID: "team-1",
		Title:  "task-1",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Blocked)
	assert.True(t, result.Structured)
	assert.Equal(t, TaskOutcomeHandoff, result.Outcome)
	assert.Equal(t, "handoff to reviewer", result.Summary)
	assert.Equal(t, "need review", result.Blocker)
	assert.Equal(t, "mate-2", result.HandoffTo)
}

func TestTeammateRunnerMarksMailboxDigestReadWhenInjected(t *testing.T) {
	store, err := NewSQLiteStore(&StoreConfig{
		DSN: "file:teammate-runner-mailbox-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	_, err = store.InsertMail(ctx, MailMessage{
		TeamID:    teamID,
		FromAgent: "lead",
		ToAgent:   "mate-1",
		Kind:      "info",
		Body:      "check the latest task context",
	})
	require.NoError(t, err)

	client := &staticSessionClient{
		result: &SessionResult{
			Success: true,
			Output:  "notes\n```json\n{\"task_status\":\"done\",\"summary\":\"task finished\"}\n```",
		},
	}
	runner := &TeammateRunner{
		Sessions: client,
		Mailbox:  NewMailboxService(store),
	}

	result, err := runner.StartTask(ctx, Team{ID: teamID}, Teammate{
		ID:        "mate-1",
		SessionID: "session-1",
		Name:      "mate-1",
	}, Task{
		ID:     "task-1",
		TeamID: teamID,
		Title:  "task-1",
		Goal:   "finish the task",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)
	assert.True(t, result.Structured)
	assert.Contains(t, client.prompt, "Mailbox digest:")
	assert.Contains(t, client.prompt, "check the latest task context")
	assert.Contains(t, client.prompt, "read_task_spec or read_task_context")
	assert.Contains(t, client.prompt, "protocol error")
	assert.Contains(t, client.prompt, "prefer direct read-only tools such as ls, glob, grep, and view")
	assert.Contains(t, client.prompt, "Do not use background_task or shell commands for basic file listing or file reading")
	assert.Contains(t, client.prompt, "Prefer report_task_outcome for done/failed/blocked/handoff outcomes")
	assert.Contains(t, client.prompt, "If you do not call report_task_outcome or block_current_task, end your final response with a structured status block")

	remaining, err := store.ListMail(ctx, MailFilter{
		TeamID:           teamID,
		ToAgent:          "mate-1",
		IncludeBroadcast: true,
		UnreadOnly:       true,
	})
	require.NoError(t, err)
	require.Len(t, remaining, 0)
}

func TestTeammateRunnerInjectsTeamContextWhenStoreIsAvailable(t *testing.T) {
	store, err := NewSQLiteStore(&StoreConfig{
		DSN: "file:teammate-runner-context-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: "session-1",
		State:     TeammateStateBusy,
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "mate-2",
		TeamID:    teamID,
		SessionID: "session-2",
		State:     TeammateStateIdle,
	})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "current task",
		Goal:   "finish the current task",
		Status: TaskStatusRunning,
		Inputs: []string{"spec.md"},
	})
	require.NoError(t, err)
	_, err = store.CreateTask(ctx, Task{
		TeamID: teamID,
		Title:  "follow-up task",
		Goal:   "handle follow-up work",
		Status: TaskStatusReady,
	})
	require.NoError(t, err)

	client := &staticSessionClient{
		result: &SessionResult{
			Success: true,
			Output:  "```json\n{\"task_status\":\"done\",\"summary\":\"task finished\"}\n```",
		},
	}
	runner := &TeammateRunner{
		Sessions: client,
		Context:  NewContextBuilder(store),
	}

	result, err := runner.StartTask(ctx, Team{ID: teamID}, Teammate{
		ID:        "mate-1",
		SessionID: "session-1",
		Name:      "mate-1",
	}, Task{
		ID:     taskID,
		TeamID: teamID,
		Title:  "current task",
		Goal:   "finish the current task",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)
	assert.Contains(t, client.prompt, "Team context:")
	assert.Contains(t, client.prompt, "follow-up task")
	assert.Contains(t, client.prompt, "teammates:")
	assert.Contains(t, client.prompt, "Task ID:")
}

func TestTeammateRunnerRecoversStructuredOutcomeFromStore(t *testing.T) {
	store, err := NewSQLiteStore(&StoreConfig{
		DSN: "file:teammate-runner-recover-outcome-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	taskID, err := store.CreateTask(ctx, Task{
		ID:      "task-1",
		TeamID:  teamID,
		Title:   "task-1",
		Status:  TaskStatusRunning,
		Summary: "",
	})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: "session-1",
		State:     TeammateStateBusy,
	})
	require.NoError(t, err)

	client := &updatingSessionClient{
		onSubmit: func(ctx context.Context, sessionID, prompt string, runMeta *RunMeta) {
			_, applyErr := ApplyBlockedTaskOutcome(ctx, TaskOutcomeApplyServices{
				Store: store,
			}, BlockedTaskOutcomeRequest{
				Team: Team{ID: teamID},
				Task: Task{
					ID:       taskID,
					TeamID:   teamID,
					Title:    "task-1",
					Status:   TaskStatusRunning,
					Assignee: stringPtr("mate-1"),
				},
				TeammateID: "mate-1",
				Outcome: TaskOutcomeContract{
					Status:  TaskOutcomeBlocked,
					Summary: "blocked by prior tool call",
					Blocker: "blocked by prior tool call",
				},
			})
			require.NoError(t, applyErr)
		},
		result: &SessionResult{
			Success: true,
			Output:  "已阻塞并上报给 lead。",
		},
	}

	runner := &TeammateRunner{
		Sessions: client,
		Context:  NewContextBuilder(store),
	}

	result, err := runner.StartTask(ctx, Team{ID: teamID}, Teammate{
		ID:        "mate-1",
		SessionID: "session-1",
	}, Task{
		ID:     taskID,
		TeamID: teamID,
		Title:  "task-1",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Structured)
	assert.True(t, result.OutcomeApplied)
	assert.True(t, result.Blocked)
	assert.Equal(t, TaskOutcomeBlocked, result.Outcome)
	assert.Equal(t, "blocked by prior tool call", result.Summary)
	assert.Empty(t, result.ProtocolError)
}

func TestTeammateRunnerUsesObservedReportTaskOutcomeAsCanonicalResult(t *testing.T) {
	runner := &TeammateRunner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  "completed and reported.",
				Observations: []SessionObservation{
					{
						Tool:    "report_task_outcome",
						Success: true,
						Output: map[string]interface{}{
							"status":  "done",
							"outcome": "done",
							"summary": "completed via task outcome tool",
						},
					},
				},
			},
		},
	}

	result, err := runner.StartTask(context.Background(), Team{ID: "team-1"}, Teammate{
		ID:        "mate-1",
		SessionID: "session-1",
	}, Task{
		ID:     "task-1",
		TeamID: "team-1",
		Title:  "task-1",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)
	assert.False(t, result.Blocked)
	assert.True(t, result.Structured)
	assert.True(t, result.OutcomeApplied)
	assert.Equal(t, TaskOutcomeDone, result.Outcome)
	assert.Equal(t, "completed via task outcome tool", result.Summary)
	assert.Empty(t, result.ProtocolError)
}

func TestTeammateRunnerUsesObservedBlockCurrentTaskAsCanonicalResult(t *testing.T) {
	runner := &TeammateRunner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  "handoff sent.",
				Observations: []SessionObservation{
					{
						Tool:    "block_current_task",
						Success: true,
						Output: map[string]interface{}{
							"status":     "blocked",
							"outcome":    "handoff",
							"summary":    "pass to reviewer",
							"blocker":    "need review",
							"handoff_to": "mate-2",
						},
					},
				},
			},
		},
	}

	result, err := runner.StartTask(context.Background(), Team{ID: "team-1"}, Teammate{
		ID:        "mate-1",
		SessionID: "session-1",
	}, Task{
		ID:     "task-1",
		TeamID: "team-1",
		Title:  "task-1",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)
	assert.True(t, result.Blocked)
	assert.True(t, result.Structured)
	assert.True(t, result.OutcomeApplied)
	assert.Equal(t, TaskOutcomeHandoff, result.Outcome)
	assert.Equal(t, "pass to reviewer", result.Summary)
	assert.Equal(t, "need review", result.Blocker)
	assert.Equal(t, "mate-2", result.HandoffTo)
	assert.Empty(t, result.ProtocolError)
}

func stringPtr(value string) *string {
	return &value
}

func TestTeammateRunnerUpdatesHeartbeatDuringTaskExecution(t *testing.T) {
	store, err := NewSQLiteStore(&StoreConfig{
		DSN: "file:teammate-runner-heartbeat-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	teamID, err := store.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	_, err = store.UpsertTeammate(ctx, Teammate{
		ID:        "mate-1",
		TeamID:    teamID,
		SessionID: "session-1",
		State:     TeammateStateBusy,
	})
	require.NoError(t, err)

	runner := &TeammateRunner{
		Sessions: &delayedSessionClient{
			delay: 35 * time.Millisecond,
			result: &SessionResult{
				Success: true,
				Output:  "```json\n{\"task_status\":\"done\",\"summary\":\"task finished\"}\n```",
			},
		},
		Context:           NewContextBuilder(store),
		HeartbeatInterval: 10 * time.Millisecond,
	}

	result, err := runner.StartTask(ctx, Team{ID: teamID}, Teammate{
		ID:        "mate-1",
		SessionID: "session-1",
	}, Task{
		ID:     "task-1",
		TeamID: teamID,
		Title:  "task-1",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Success)

	mate, err := store.GetTeammate(ctx, "mate-1")
	require.NoError(t, err)
	require.NotNil(t, mate)
	assert.False(t, mate.LastHeartbeat.IsZero())
}

func TestTeammateRunnerParsesStructuredLineOutcome(t *testing.T) {
	runner := &TeammateRunner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  "notes\nTASK_STATUS: failed\nTASK_SUMMARY: tests failed on auth path\nTASK_BLOCKER: nil token case",
			},
		},
	}

	result, err := runner.StartTask(context.Background(), Team{ID: "team-1"}, Teammate{
		ID:        "mate-1",
		SessionID: "session-1",
	}, Task{
		ID:     "task-1",
		TeamID: "team-1",
		Title:  "task-1",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Structured)
	assert.False(t, result.Success)
	assert.Equal(t, TaskOutcomeFailed, result.Outcome)
	assert.Equal(t, "tests failed on auth path", result.Summary)
	assert.Equal(t, "nil token case", result.Blocker)
}

func TestTeammateRunnerMarksInvalidStructuredJSONOutcomeAsProtocolError(t *testing.T) {
	runner := &TeammateRunner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  "```json\n{\"task_status\":\"blocked\",\"summary\":\"waiting on review\"}\n```",
			},
		},
	}

	result, err := runner.StartTask(context.Background(), Team{ID: "team-1"}, Teammate{
		ID:        "mate-1",
		SessionID: "session-1",
	}, Task{
		ID:     "task-1",
		TeamID: "team-1",
		Title:  "task-1",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Success)
	assert.False(t, result.Structured)
	assert.False(t, result.Blocked)
	assert.Contains(t, result.ProtocolError, "invalid JSON status block")
	assert.Contains(t, result.ProtocolError, "blocker is required")
}

func TestTeammateRunnerMarksInvalidStructuredLineOutcomeAsProtocolError(t *testing.T) {
	runner := &TeammateRunner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success: true,
				Output:  "TASK_STATUS: handoff\nTASK_SUMMARY: pass to reviewer\nTASK_BLOCKER: need security review",
			},
		},
	}

	result, err := runner.StartTask(context.Background(), Team{ID: "team-1"}, Teammate{
		ID:        "mate-1",
		SessionID: "session-1",
	}, Task{
		ID:     "task-1",
		TeamID: "team-1",
		Title:  "task-1",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Success)
	assert.False(t, result.Structured)
	assert.False(t, result.Blocked)
	assert.Contains(t, result.ProtocolError, "invalid TASK_* status block")
	assert.Contains(t, result.ProtocolError, "handoff_to is required")
}
