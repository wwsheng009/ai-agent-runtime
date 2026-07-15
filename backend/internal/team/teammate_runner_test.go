package team

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeexecution "github.com/wwsheng009/ai-agent-runtime/internal/execution"
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

type contextDeadlineSessionClient struct {
	requestedTimeout time.Duration
}

func (c *contextDeadlineSessionClient) SubmitPrompt(ctx context.Context, sessionID, prompt string, runMeta *RunMeta) (*SessionResult, error) {
	budget := runtimeexecution.ResolveTimeout(ctx, c.requestedTimeout)
	<-ctx.Done()
	timeoutErr := runtimeexecution.TimeoutError(budget)
	metadata := budget.Metadata()
	errorCode := runtimeerrors.ErrToolTimeout
	if structured, ok := timeoutErr.(*runtimeerrors.RuntimeError); ok {
		metadata = structured.GetContext()
		errorCode = structured.Code
	}
	metadata["error_code"] = string(errorCode)
	metadata["retryable"] = true
	return &SessionResult{
		Success:       false,
		Error:         timeoutErr.Error(),
		TraceID:       "trace-member-context-deadline",
		ErrorType:     "timeout",
		ErrorMetadata: metadata,
	}, timeoutErr
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

type capturingTaskTriggerClient struct {
	result  *SessionResult
	err     error
	request TaskTriggerRequest
	called  bool
}

func (c *capturingTaskTriggerClient) TriggerTask(ctx context.Context, request TaskTriggerRequest) (*SessionResult, error) {
	c.called = true
	c.request = request
	return c.result, c.err
}

type staticTaskRouteResolver struct {
	resolution *TaskRouteResolution
	err        error
	request    TaskRouteRequest
	called     bool
}

func (r *staticTaskRouteResolver) ResolveTaskRoute(ctx context.Context, request TaskRouteRequest) (*TaskRouteResolution, error) {
	r.called = true
	r.request = request
	return r.resolution, r.err
}

type capturingTaskRouteAuditSink struct {
	audits []TaskRouteAudit
}

func (s *capturingTaskRouteAuditSink) RecordTaskRouteAudit(ctx context.Context, audit TaskRouteAudit) error {
	s.audits = append(s.audits, audit.Clone())
	return nil
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

func TestTeammateRunnerPrefersAgentControlTriggerTask(t *testing.T) {
	fallback := &staticSessionClient{
		result: &SessionResult{
			Success: true,
			Output:  "```json\n{\"task_status\":\"done\",\"summary\":\"fallback\"}\n```",
		},
	}
	control := &capturingTaskTriggerClient{
		result: &SessionResult{
			Success: true,
			Output:  "```json\n{\"task_status\":\"done\",\"summary\":\"triggered\"}\n```",
		},
	}
	runner := &TeammateRunner{
		Sessions:     fallback,
		AgentControl: control,
	}

	result, err := runner.StartTask(context.Background(), Team{ID: "team-1"}, Teammate{
		ID:        "mate-1",
		Name:      "Mate",
		SessionID: "session-1",
	}, Task{
		ID:                  "task-1",
		TeamID:              "team-1",
		Title:               "Implement change",
		Goal:                "Implement change",
		Difficulty:          TaskDifficultyExpert,
		DifficultyRationale: "Needs architecture review.",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, control.called)
	assert.Equal(t, "session-1", control.request.SessionID)
	assert.Equal(t, "team-1", control.request.TeamID)
	assert.Equal(t, "mate-1", control.request.AgentID)
	assert.Equal(t, "task-1", control.request.TaskID)
	assert.Equal(t, TaskDifficultyExpert, control.request.Difficulty)
	assert.Equal(t, "Needs architecture review.", control.request.DifficultyRationale)
	assert.Contains(t, control.request.Prompt, "Implement change")
	assert.Contains(t, control.request.Prompt, "- Difficulty: expert")
	assert.Contains(t, control.request.Prompt, "- Difficulty rationale: Needs architecture review.")
	require.NotNil(t, control.request.RunMeta)
	require.NotNil(t, control.request.RunMeta.Team)
	assert.Equal(t, "team-1", control.request.RunMeta.Team.TeamID)
	assert.Equal(t, "mate-1", control.request.RunMeta.Team.AgentID)
	assert.Equal(t, "task-1", control.request.RunMeta.Team.CurrentTaskID)
	assert.Equal(t, "bypass_permissions", control.request.RunMeta.PermissionMode)
	assert.Equal(t, "", fallback.prompt)
	assert.Equal(t, "triggered", result.Summary)
}

func TestTeammateRunnerPassesResolvedRouteIntoTriggerRunMetaAndPrompt(t *testing.T) {
	control := &capturingTaskTriggerClient{
		result: &SessionResult{
			Success: true,
			Output:  "```json\n{\"task_status\":\"done\",\"summary\":\"triggered\"}\n```",
		},
	}
	resolver := &staticTaskRouteResolver{
		resolution: &TaskRouteResolution{
			Route: &TaskExecutionRoute{
				Difficulty:          TaskDifficultyHard,
				DifficultySource:    "task",
				DifficultyRationale: "Touches shared execution state.",
				Provider:            "remote-strong",
				Model:               "strong-model",
				ReasoningEffort:     "high",
				Source:              "difficulty_level",
				Warnings:            []string{"fallback checked"},
				FallbackUsed:        true,
				FallbackReason:      "parent model unavailable",
			},
		},
	}
	audit := &capturingTaskRouteAuditSink{}
	runner := &TeammateRunner{
		AgentControl:  control,
		RouteResolver: resolver,
		RouteAudit:    audit,
	}

	result, err := runner.StartTask(context.Background(), Team{ID: "team-1"}, Teammate{
		ID:        "mate-1",
		Name:      "Mate",
		SessionID: "session-1",
	}, Task{
		ID:                  "task-1",
		TeamID:              "team-1",
		Title:               "Implement change",
		Goal:                "Implement change",
		Difficulty:          TaskDifficultyHard,
		DifficultyRationale: "Touches shared execution state.",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, control.called)
	require.True(t, resolver.called)
	assert.Equal(t, 1, resolver.request.Attempt)
	require.NotNil(t, control.request.Route)
	assert.Equal(t, "remote-strong", control.request.Route.Provider)
	assert.Equal(t, "strong-model", control.request.Route.Model)
	assert.Equal(t, "high", control.request.Route.ReasoningEffort)
	assert.Equal(t, "difficulty_level", control.request.Route.Source)
	assert.Equal(t, []string{"fallback checked"}, control.request.Route.Warnings)
	require.NotNil(t, control.request.RunMeta)
	require.NotNil(t, control.request.RunMeta.Team)
	assert.Equal(t, "bypass_permissions", control.request.RunMeta.PermissionMode)
	assert.Equal(t, "remote-strong", control.request.RunMeta.Team.RouteProvider)
	assert.Equal(t, "strong-model", control.request.RunMeta.Team.RouteModel)
	assert.Equal(t, "high", control.request.RunMeta.Team.RouteReasoningEffort)
	assert.Equal(t, "difficulty_level", control.request.RunMeta.Team.RouteSource)
	assert.Equal(t, []string{"fallback checked"}, control.request.RunMeta.Team.RouteWarnings)
	assert.True(t, control.request.RunMeta.Team.RouteFallbackUsed)
	assert.Contains(t, control.request.Prompt, "Runtime routing:")
	assert.Contains(t, control.request.Prompt, "- Provider: remote-strong")
	assert.Contains(t, control.request.Prompt, "- Model: strong-model")
	require.NotNil(t, result.Route)
	assert.Equal(t, "remote-strong", result.Route.Provider)
	require.Len(t, audit.audits, 1)
	require.NotNil(t, audit.audits[0].Route)
	assert.Equal(t, "team-1", audit.audits[0].TeamID)
	assert.Equal(t, "mate-1", audit.audits[0].AgentID)
	assert.Equal(t, "task-1", audit.audits[0].TaskID)
}

func TestTeammateRunnerDisabledRouteKeepsLegacyRequest(t *testing.T) {
	control := &capturingTaskTriggerClient{
		result: &SessionResult{
			Success: true,
			Output:  "```json\n{\"task_status\":\"done\",\"summary\":\"triggered\"}\n```",
		},
	}
	resolver := &staticTaskRouteResolver{
		resolution: &TaskRouteResolution{
			Disabled: true,
			Route: &TaskExecutionRoute{
				Provider: "should-not-apply",
				Model:    "should-not-apply",
			},
		},
	}
	runner := &TeammateRunner{
		AgentControl:  control,
		RouteResolver: resolver,
	}

	result, err := runner.StartTask(context.Background(), Team{ID: "team-1"}, Teammate{
		ID:        "mate-1",
		SessionID: "session-1",
	}, Task{
		ID:     "task-1",
		TeamID: "team-1",
		Title:  "Implement change",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, control.called)
	assert.Nil(t, control.request.Route)
	assert.Nil(t, result.Route)
	assert.NotContains(t, control.request.Prompt, "Runtime routing:")
	require.NotNil(t, control.request.RunMeta)
	assert.Equal(t, "bypass_permissions", control.request.RunMeta.PermissionMode)
	assert.Empty(t, control.request.RunMeta.Team.RouteProvider)
}

func TestTeammateRunnerSessionsFallbackRejectsRouteOverride(t *testing.T) {
	session := &staticSessionClient{
		result: &SessionResult{
			Success: true,
			Output:  "```json\n{\"task_status\":\"done\",\"summary\":\"should not run\"}\n```",
		},
	}
	runner := &TeammateRunner{
		Sessions: session,
		RouteResolver: &staticTaskRouteResolver{
			resolution: &TaskRouteResolution{
				Route: &TaskExecutionRoute{
					Provider:        "remote-strong",
					Model:           "strong-model",
					ReasoningEffort: "high",
					Source:          "difficulty_level",
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
		Title:  "Implement change",
	})
	require.Error(t, err)
	require.NotNil(t, result)
	assert.Contains(t, err.Error(), "route override requires agent control trigger support")
	assert.Equal(t, "", session.prompt)
	assert.False(t, result.Success)
	assert.NotNil(t, result.Route)
}

func TestTeammateRunnerStrictRouteFailureBlocksWithoutTriggeringSession(t *testing.T) {
	control := &capturingTaskTriggerClient{
		result: &SessionResult{
			Success: true,
			Output:  "```json\n{\"task_status\":\"done\",\"summary\":\"should not run\"}\n```",
		},
	}
	resolver := &staticTaskRouteResolver{
		resolution: &TaskRouteResolution{
			Strict: true,
			Route: &TaskExecutionRoute{
				Difficulty: TaskDifficultyExpert,
				Source:     "difficulty_level",
			},
		},
		err: assert.AnError,
	}
	runner := &TeammateRunner{
		AgentControl:  control,
		RouteResolver: resolver,
	}

	result, err := runner.StartTask(context.Background(), Team{ID: "team-1"}, Teammate{
		ID:        "mate-1",
		SessionID: "session-1",
	}, Task{
		ID:         "task-1",
		TeamID:     "team-1",
		Title:      "Expert change",
		Difficulty: TaskDifficultyExpert,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, control.called)
	assert.True(t, result.Success)
	assert.True(t, result.Blocked)
	assert.False(t, result.OutcomeApplied)
	assert.True(t, result.Structured)
	assert.Equal(t, TaskOutcomeBlocked, result.Outcome)
	assert.Equal(t, "task_route_resolution", result.ErrorType)
	assert.Contains(t, result.Blocker, assert.AnError.Error())
	require.NotNil(t, result.Route)
	assert.Contains(t, result.Route.Error, assert.AnError.Error())
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

func TestTeammateRunnerPreservesStructuredSessionErrorMetadata(t *testing.T) {
	runner := &TeammateRunner{
		Sessions: &staticSessionClient{
			result: &SessionResult{
				Success:   false,
				Error:     "prompt preflight budget exceeded",
				TraceID:   "trace-task-preflight",
				ErrorType: "prompt_preflight",
				ErrorMetadata: map[string]interface{}{
					"failure_reason_code":           "prompt_still_exceeds_budget_after_compaction",
					"replacement_history_applied":   true,
					"replacement_history_available": true,
				},
			},
			err: assert.AnError,
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
	require.Error(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Success)
	assert.Equal(t, "trace-task-preflight", result.TraceID)
	assert.Equal(t, "prompt_preflight", result.ErrorType)
	assert.Equal(t, "prompt_still_exceeds_budget_after_compaction", result.ErrorMetadata["failure_reason_code"])
	assert.Equal(t, true, result.ErrorMetadata["replacement_history_applied"])
	assert.Contains(t, result.Summary, "prompt preflight")
}

func TestReliabilityEvalTeammateTimeoutPreservesStructuredFailure(t *testing.T) {
	const requestedTimeout = 5 * time.Second
	ctx, cancel := runtimeexecution.WithTimeoutSource(
		context.Background(),
		250*time.Millisecond,
		runtimeexecution.TimeoutSourceAgentRunDeadline,
	)
	defer cancel()

	runner := &TeammateRunner{
		Sessions: &contextDeadlineSessionClient{requestedTimeout: requestedTimeout},
	}

	startedAt := time.Now()
	result, err := runner.StartTask(ctx, Team{ID: "team-timeout"}, Teammate{
		ID: "mate-timeout", SessionID: "session-timeout",
	}, Task{ID: "task-timeout", TeamID: "team-timeout", Title: "slow member task"})
	elapsed := time.Since(startedAt)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.True(t, runtimeerrors.Is(err, runtimeerrors.ErrToolTimeout))
	require.NotNil(t, result)
	assert.False(t, result.Success)
	assert.Equal(t, "trace-member-context-deadline", result.TraceID)
	assert.Equal(t, "timeout", result.ErrorType)
	assert.Equal(t, "TOOL_TIMEOUT", result.ErrorMetadata["error_code"])
	effectiveMs, ok := result.ErrorMetadata["timeout_effective_ms"].(int64)
	require.True(t, ok, "timeout_effective_ms should be int64: %#v", result.ErrorMetadata)
	assert.Positive(t, effectiveMs)
	assert.Less(t, effectiveMs, requestedTimeout.Milliseconds())
	timeoutSource, ok := result.ErrorMetadata["timeout_source"].(string)
	require.True(t, ok, "timeout_source should be string: %#v", result.ErrorMetadata)
	assert.Equal(t, runtimeexecution.TimeoutSourceAgentRunDeadline, runtimeexecution.TimeoutSource(timeoutSource))
	assert.Equal(t, true, result.ErrorMetadata["retryable"])
	assert.Contains(t, result.Summary, "execution timed out")
	assert.GreaterOrEqual(t, elapsed, 100*time.Millisecond)
	assert.Less(t, elapsed, 2*time.Second)
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
		ID:                  "task-1",
		TeamID:              teamID,
		Title:               "task-1",
		Goal:                "finish the task",
		Difficulty:          TaskDifficultyHard,
		DifficultyRationale: "Requires shared context review.",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)
	assert.True(t, result.Structured)
	assert.Contains(t, client.prompt, "Mailbox digest:")
	assert.Contains(t, client.prompt, "check the latest task context")
	assert.Contains(t, client.prompt, "- Difficulty: hard")
	assert.Contains(t, client.prompt, "- Difficulty rationale: Requires shared context review.")
	assert.Contains(t, client.prompt, "read_task_spec or read_task_context")
	assert.Contains(t, client.prompt, "protocol error")
	assert.Contains(t, client.prompt, "prefer direct read-only tools such as ls, glob, grep, and view")
	assert.Contains(t, client.prompt, "The local grep tool prefers ripgrep (rg) when it is available")
	assert.Contains(t, client.prompt, "it accepts common rg-style options/mental-model mappings")
	assert.Contains(t, client.prompt, "-o")
	assert.Contains(t, client.prompt, "iglob/--iglob")
	assert.Contains(t, client.prompt, "glob_case_insensitive/--glob-case-insensitive")
	assert.Contains(t, client.prompt, "pattern_file/pattern_files/-f/--file")
	assert.Contains(t, client.prompt, "ignore_file/--ignore-file")
	assert.Contains(t, client.prompt, "ignore_file_case_insensitive/--ignore-file-case-insensitive")
	assert.Contains(t, client.prompt, "no_ignore_files/--no-ignore-files")
	assert.Contains(t, client.prompt, "no_ignore_parent/vcs/global/dot")
	assert.Contains(t, client.prompt, "hidden/no_hidden/--hidden/--no-hidden")
	assert.Contains(t, client.prompt, "-u/-uu/-uuu/--unrestricted")
	assert.Contains(t, client.prompt, "no_config/--no-config")
	assert.Contains(t, client.prompt, "no_messages/--no-messages")
	assert.Contains(t, client.prompt, "one_file_system/--one-file-system")
	assert.Contains(t, client.prompt, "pcre2/-P/--pcre2")
	assert.Contains(t, client.prompt, "engine/--engine")
	assert.Contains(t, client.prompt, "multiline/-U/--multiline")
	assert.Contains(t, client.prompt, "multiline_dotall/--multiline-dotall")
	assert.Contains(t, client.prompt, "replace/-r/--replace")
	assert.Contains(t, client.prompt, "passthru/--passthru")
	assert.Contains(t, client.prompt, "crlf/--crlf")
	assert.Contains(t, client.prompt, "auto_hybrid_regex/--auto-hybrid-regex")
	assert.Contains(t, client.prompt, "column/--column")
	assert.Contains(t, client.prompt, "trim/--trim")
	assert.Contains(t, client.prompt, "pretty/--pretty")
	assert.Contains(t, client.prompt, "line_buffered/--line-buffered")
	assert.Contains(t, client.prompt, "block_buffered/--block-buffered")
	assert.Contains(t, client.prompt, "null/null_data/--null/--null-data")
	assert.Contains(t, client.prompt, "field_context_separator/--field-context-separator")
	assert.Contains(t, client.prompt, "path_separator/--path-separator")
	assert.Contains(t, client.prompt, "context_separator/--context-separator")
	assert.Contains(t, client.prompt, "max_columns/-M/--max-columns")
	assert.Contains(t, client.prompt, "max_columns_preview/--max-columns-preview")
	assert.Contains(t, client.prompt, "count_matches/--count-matches")
	assert.Contains(t, client.prompt, "stats/--stats")
	assert.Contains(t, client.prompt, "json/--json")
	assert.Contains(t, client.prompt, "follow/-L/--follow")
	assert.Contains(t, client.prompt, "rg优先级：structured>rg_args；no_hidden>hidden；no_ignore_files>ignore_file；no_ignore_files drops ignore_file_case_insensitive；follow+one_file_system=rg-only")
	assert.Contains(t, client.prompt, "sort/sortr/--sort/--sortr")
	assert.Contains(t, client.prompt, "sort_files/--sort-files")
	assert.Contains(t, client.prompt, "type_add/type_clear/--type-add/--type-clear")
	assert.Contains(t, client.prompt, "single-file path searches")
	assert.Contains(t, client.prompt, "multi-path searches")
	assert.Contains(t, client.prompt, "src/**/*.go")
	assert.Contains(t, client.prompt, "-iwg*.go")
	assert.Contains(t, client.prompt, "--max-depth 0 / -m 0")
	assert.Contains(t, client.prompt, "max_filesize/--max-filesize")
	assert.Contains(t, client.prompt, "path:line[:column]: content")
	assert.Contains(t, client.prompt, "JSON Lines")
	assert.Contains(t, client.prompt, "--files-without-match")
	assert.Contains(t, client.prompt, "-n/-H/-N/--no-filename/--color")
	assert.Contains(t, client.prompt, "line_number/heading/no_heading/with_filename/no_filename/color")
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

func TestTeammateRunnerRecoversStructuredOutcomeFromStoreAfterSessionCancel(t *testing.T) {
	store, err := NewSQLiteStore(&StoreConfig{
		DSN: "file:teammate-runner-recover-cancel-outcome-test?mode=memory&cache=shared",
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
			_, applyErr := ApplyTerminalTaskOutcome(ctx, TaskOutcomeApplyServices{
				Store: store,
			}, TerminalTaskOutcomeRequest{
				Task: Task{
					ID:       taskID,
					TeamID:   teamID,
					Title:    "task-1",
					Status:   TaskStatusRunning,
					Assignee: stringPtr("mate-1"),
				},
				TeammateID:    "mate-1",
				DefaultStatus: TaskOutcomeDone,
				Outcome: TaskOutcomeContract{
					Status:  TaskOutcomeDone,
					Summary: "completed before cancellation",
				},
			})
			require.NoError(t, applyErr)
		},
		err: context.Canceled,
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
	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result)
	assert.True(t, result.Success)
	assert.True(t, result.Structured)
	assert.True(t, result.OutcomeApplied)
	assert.Equal(t, TaskOutcomeDone, result.Outcome)
	assert.Equal(t, "completed before cancellation", result.Summary)
	assert.Empty(t, result.ProtocolError)
}

func TestReliabilityEvalTeammateRuntimeRestartRecoversStructuredOutcome(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "team-runtime-restart.db")
	storeBeforeRestart, err := NewSQLiteStore(&StoreConfig{Path: databasePath})
	require.NoError(t, err)
	storeBeforeRestartClosed := false
	t.Cleanup(func() {
		if !storeBeforeRestartClosed {
			_ = storeBeforeRestart.Close()
		}
	})

	teamID, err := storeBeforeRestart.CreateTeam(ctx, Team{})
	require.NoError(t, err)
	taskID, err := storeBeforeRestart.CreateTask(ctx, Task{
		ID:      "task-runtime-restart",
		TeamID:  teamID,
		Title:   "durable task",
		Status:  TaskStatusRunning,
		Summary: "",
	})
	require.NoError(t, err)
	_, err = storeBeforeRestart.UpsertTeammate(ctx, Teammate{
		ID:        "mate-runtime-restart",
		TeamID:    teamID,
		SessionID: "session-runtime-restart",
		State:     TeammateStateBusy,
	})
	require.NoError(t, err)

	_, err = ApplyTerminalTaskOutcome(ctx, TaskOutcomeApplyServices{
		Store: storeBeforeRestart,
	}, TerminalTaskOutcomeRequest{
		Task: Task{
			ID:       taskID,
			TeamID:   teamID,
			Title:    "durable task",
			Status:   TaskStatusRunning,
			Assignee: stringPtr("mate-runtime-restart"),
		},
		TeammateID:    "mate-runtime-restart",
		DefaultStatus: TaskOutcomeDone,
		Outcome: TaskOutcomeContract{
			Status:  TaskOutcomeDone,
			Summary: "completed before runtime restart",
		},
	})
	require.NoError(t, err)
	require.NoError(t, storeBeforeRestart.Close())
	storeBeforeRestartClosed = true

	storeAfterRestart, err := NewSQLiteStore(&StoreConfig{Path: databasePath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = storeAfterRestart.Close() })

	persistedTask, err := storeAfterRestart.GetTask(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, persistedTask)
	require.Equal(t, TaskStatusDone, persistedTask.Status)
	require.Equal(t, "completed before runtime restart", persistedTask.Summary)

	runner := &TeammateRunner{
		Sessions: &staticSessionClient{err: context.Canceled},
		Context:  NewContextBuilder(storeAfterRestart),
	}
	result, err := runner.StartTask(ctx, Team{ID: teamID}, Teammate{
		ID:        "mate-runtime-restart",
		SessionID: "session-runtime-restart",
	}, Task{
		ID:     taskID,
		TeamID: teamID,
		Title:  "durable task",
	})

	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, result)
	assert.True(t, result.Success)
	assert.True(t, result.Structured)
	assert.True(t, result.OutcomeApplied)
	assert.Equal(t, TaskOutcomeDone, result.Outcome)
	assert.Equal(t, "completed before runtime restart", result.Summary)
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
