package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/pkg/skillsapi"
)

func TestSummarizeResult(t *testing.T) {
	result := &skillsapi.AgentChatResult{
		Kind:      "agent",
		Source:    "agent_route",
		Success:   true,
		Output:    "done",
		Skill:     "route-skill",
		Model:     "test-model",
		Reasoning: "checked",
		Usage: map[string]interface{}{
			"prompt_tokens":     10,
			"completion_tokens": 6,
			"total_tokens":      16,
		},
		Duration: map[string]interface{}{
			"start": "2026-03-09T09:20:25Z",
			"end":   "2026-03-09T09:20:27Z",
		},
		State: map[string]interface{}{
			"currentStep": 2,
			"running":     false,
			"errors":      []string{"warn"},
		},
		Orchestration: map[string]interface{}{
			"route_attempted":     true,
			"route_matched":       true,
			"planning_attempted":  true,
			"subagent_task_count": 2,
		},
		Planning: map[string]interface{}{
			"mode":                         "planner_preferred",
			"step_count":                   1,
			"subagent_task_count":          2,
			"subagent_execution_requested": true,
			"subagent_execution_eligible":  true,
			"subagent_execution_attempted": true,
		},
		SubagentSummary: map[string]interface{}{
			"count":       2,
			"successful":  2,
			"failed":      0,
			"patch_count": 1,
			"roles":       []string{"writer", "verifier"},
		},
		ToolCalls: []map[string]interface{}{
			{"name": "search"},
			{"name": "fetch"},
		},
		Metadata: map[string]interface{}{
			"finish_reason": "stop",
			"cached":        true,
		},
	}

	lines := summarizeResult(result)
	joined := joinLines(lines)

	assert.Contains(t, joined, "kind=agent source=agent_route success=true")
	assert.Contains(t, joined, "skill=route-skill")
	assert.Contains(t, joined, "model=test-model")
	assert.Contains(t, joined, "usage prompt=10 completion=6 total=16")
	assert.Contains(t, joined, "duration elapsed=2s")
	assert.Contains(t, joined, "state step=2 running=false errors=1")
	assert.Contains(t, joined, "planning mode=planner_preferred steps=1 tasks=2 requested=true eligible=true attempted=true")
	assert.Contains(t, joined, "subagents count=2 successful=2 failed=0 patches=1 roles=writer,verifier")
	assert.Contains(t, joined, "tool_calls=search,fetch")
	assert.Contains(t, joined, "metadata.finish_reason=stop")
	assert.Contains(t, joined, "metadata.cached=true")
}

func TestStreamDemoPrinterHandleEvent(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	printer := &streamDemoPrinter{stdout: &stdout, stderr: &stderr}

	reasoningEvent := &skillsapi.DecodedStreamEvent{
		Event: "reasoning",
		Chunk: &skillsapi.StreamChunkPayload{
			Type: "reasoning",
			Reasoning: map[string]interface{}{
				"content": "thinking",
				"delta":   "thinking",
				"length":  8,
			},
		},
	}
	require.NoError(t, printer.handleEvent(reasoningEvent))
	assert.Contains(t, stderr.String(), "[reasoning] thinking")

	toolEvent := &skillsapi.DecodedStreamEvent{
		Event: "tool_start",
		Chunk: &skillsapi.StreamChunkPayload{
			Type: "tool_start",
			Tool: map[string]interface{}{
				"name":    "search",
				"args":    map[string]interface{}{"query": "weather"},
				"status":  "tool_start",
				"content": "search",
			},
			Metadata: map[string]interface{}{
				"phase": "start",
			},
		},
	}
	require.NoError(t, printer.handleEvent(toolEvent))
	assert.Contains(t, stderr.String(), "[tool_start] search")
	assert.Contains(t, stderr.String(), "phase=start")

	textEvent := &skillsapi.DecodedStreamEvent{
		Event: "chunk",
		Chunk: &skillsapi.StreamChunkPayload{
			Type:    "text",
			Content: "hello",
		},
	}
	require.NoError(t, printer.handleEvent(textEvent))
	assert.Equal(t, "hello", stdout.String())

	require.NoError(t, printer.ensureTextNewline())
	assert.Equal(t, "hello\n", stdout.String())
}

func TestParseDemoOptions(t *testing.T) {
	opts, err := parseDemoOptions([]string{
		"-url", "http://127.0.0.1:8101",
		"-message", "hi",
		"-stream",
		"-planning-mode", "planner_preferred",
		"-timeout", "30s",
	})
	require.NoError(t, err)
	assert.Equal(t, "http://127.0.0.1:8101", opts.baseURL)
	assert.Equal(t, "hi", opts.message)
	assert.True(t, opts.stream)
	assert.Equal(t, "planner_preferred", opts.planningMode)
	assert.Equal(t, 30*time.Second, opts.timeout)
}

func TestParseDemoOptions_SessionAgentSpawnAllowsEmptyMessage(t *testing.T) {
	opts, err := parseDemoOptions([]string{
		"-mode", "session-agent",
		"-agent-action", "spawn",
		"-url", "http://127.0.0.1:8101",
	})
	require.NoError(t, err)
	assert.Equal(t, "session-agent", opts.mode)
	assert.Equal(t, "spawn", opts.agentAction)
}

func TestParseDemoOptions_SessionAgentInputRequiresMessage(t *testing.T) {
	_, err := parseDemoOptions([]string{
		"-mode", "session-agent",
		"-agent-action", "input",
		"-url", "http://127.0.0.1:8101",
		"-parent-session-id", "parent-1",
		"-agent-id", "child-1",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "message is required for session-agent input")
}

func TestParseDemoOptions_AgentControlAddDependencyRequiresDependencyID(t *testing.T) {
	_, err := parseDemoOptions([]string{
		"-mode", "agent-control",
		"-control-action", "add-dependency",
		"-task-id", "task-1",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "depends-on-id is required")
}

func TestRun_SessionAgentSpawnAutoCreatesParent(t *testing.T) {
	var requestPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPaths = append(requestPaths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/runtime/sessions":
			_, _ = io.WriteString(w, `{"session":{"id":"parent-1","userId":"demo-user","state":"active","metadata":{},"createdAt":"2026-03-18T00:00:00Z","updatedAt":"2026-03-18T00:00:00Z"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/runtime/sessions/parent-1/agents":
			_, _ = io.WriteString(w, `{"agent":{"id":"child-1","session_id":"child-1","parent_session_id":"parent-1","agent_type":"explorer","status":"idle","exists":true,"created":true}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := run([]string{
		"-mode", "session-agent",
		"-agent-action", "spawn",
		"-url", server.URL,
		"-agent-type", "explorer",
		"-user-id", "demo-user",
	}, &stdout, &stderr)
	require.NoError(t, err)

	output := stdout.String()
	assert.Contains(t, output, "created_parent_session=parent-1")
	assert.Contains(t, output, "parent_session=parent-1")
	assert.Contains(t, output, "agent_session=child-1 status=idle exists=true")
	assert.Contains(t, output, "agent_type=explorer")
	assert.Contains(t, output, "created=true")
	assert.Equal(t, []string{"/api/runtime/sessions", "/api/runtime/sessions/parent-1/agents"}, requestPaths)
}

func TestRun_SessionAgentEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		assert.Equal(t, "/api/runtime/sessions/parent-1/agents/child-1/events", r.URL.Path)
		assert.Equal(t, "after_seq=12&limit=3&wait_ms=900", r.URL.RawQuery)
		_, _ = io.WriteString(w, `{"result":{"session_id":"child-1","count":1,"latest_seq":12,"events":[{"seq":12,"type":"turn.completed","agent_name":"explorer","timestamp":"2026-03-18T00:00:00Z","payload":{"status":"ok"}}]}}`)
	}))
	t.Cleanup(server.Close)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := run([]string{
		"-mode", "session-agent",
		"-agent-action", "events",
		"-url", server.URL,
		"-parent-session-id", "parent-1",
		"-agent-id", "child-1",
		"-after-seq", "12",
		"-limit", "3",
		"-wait-ms", "900",
	}, &stdout, &stderr)
	require.NoError(t, err)

	output := stdout.String()
	assert.Contains(t, output, "parent_session=parent-1")
	assert.Contains(t, output, "agent_session=child-1 count=1 latest_seq=12 timed_out=false")
	assert.Contains(t, output, "event seq=12 type=turn.completed")
	assert.Contains(t, output, "agent=explorer")
	assert.Contains(t, output, `payload={"status":"ok"}`)
}

func TestRun_SessionAgentMailboxEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		assert.Equal(t, "/api/runtime/sessions/parent-1/agents/events", r.URL.Path)
		assert.Equal(t, "after_seq=8&limit=3&wait_ms=900", r.URL.RawQuery)
		_, _ = io.WriteString(w, `{"result":{"session_id":"parent-1","count":1,"latest_seq":9,"events":[{"seq":9,"type":"mailbox_received","session_id":"parent-1","timestamp":"2026-03-18T00:00:00Z","payload":{"body":"hello parent"}}]}}`)
	}))
	t.Cleanup(server.Close)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := run([]string{
		"-mode", "session-agent",
		"-agent-action", "events",
		"-url", server.URL,
		"-parent-session-id", "parent-1",
		"-after-seq", "8",
		"-limit", "3",
		"-wait-ms", "900",
	}, &stdout, &stderr)
	require.NoError(t, err)

	output := stdout.String()
	assert.Contains(t, output, "parent_session=parent-1")
	assert.Contains(t, output, "agent_session=parent-1 count=1 latest_seq=9 timed_out=false")
	assert.Contains(t, output, "event seq=9 type=mailbox_received session=parent-1")
	assert.Contains(t, output, "hello parent")
}

func TestRun_SessionAgentControlMailbox(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		assert.Equal(t, "/api/runtime/sessions/parent-1/agent-control/mailbox", r.URL.Path)
		assert.Equal(t, "after_seq=8&limit=3&wait_ms=900", r.URL.RawQuery)
		_, _ = io.WriteString(w, `{"result":{"session_id":"parent-1","count":1,"latest_seq":9,"source":"agent_control_mailbox","control_only":true,"messages":[{"seq":9,"kind":"agent_message","from_agent":"child-1","to_agent":"parent","body":"control hello","metadata":{"message_type":"agent_control.agent_message"}}]}}`)
	}))
	t.Cleanup(server.Close)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := run([]string{
		"-mode", "session-agent",
		"-agent-action", "control-mailbox",
		"-url", server.URL,
		"-parent-session-id", "parent-1",
		"-after-seq", "8",
		"-limit", "3",
		"-wait-ms", "900",
	}, &stdout, &stderr)
	require.NoError(t, err)

	output := stdout.String()
	assert.Contains(t, output, "parent_session=parent-1")
	assert.Contains(t, output, "agent_control_mailbox session=parent-1 count=1 latest_seq=9 timed_out=false source=agent_control_mailbox")
	assert.Contains(t, output, "control_message seq=9 kind=agent_message from=child-1 to=parent body=control hello")
	assert.Contains(t, output, "agent_control.agent_message")
}

func TestRun_SessionAgentWaitParentMailbox(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/runtime/sessions/parent-1/agents/wait", r.URL.Path)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), `"timeout_ms":900`)
		_, _ = io.WriteString(w, `{"result":{"ready_count":1,"latest_seq":9,"event":{"seq":9,"type":"mailbox_received","session_id":"parent-1","timestamp":"2026-03-18T00:00:00Z","payload":{"body":"hello parent"}},"events":[{"seq":9,"type":"mailbox_received","session_id":"parent-1","timestamp":"2026-03-18T00:00:00Z","payload":{"body":"hello parent"}}]}}`)
	}))
	t.Cleanup(server.Close)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := run([]string{
		"-mode", "session-agent",
		"-agent-action", "wait",
		"-url", server.URL,
		"-parent-session-id", "parent-1",
		"-agent-timeout-ms", "900",
	}, &stdout, &stderr)
	require.NoError(t, err)

	output := stdout.String()
	assert.Contains(t, output, "parent_session=parent-1")
	assert.Contains(t, output, "ready=1 pending=0 latest_seq=9 timed_out=false")
	assert.Contains(t, output, "mailbox_event seq=9 type=mailbox_received session=parent-1")
	assert.Contains(t, output, "hello parent")
}

func TestRun_AgentControlTasks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/runtime/agent-control/tasks", r.URL.Path)
		assert.Equal(t, "limit=5&team_id=team-1&workflow=spawn_team", r.URL.RawQuery)
		_, _ = io.WriteString(w, `{"tasks":[{"id":"task-1","workflow":"spawn_team","team_id":"team-1","assignee":"member-1","title":"Review docs","status":"running"}],"count":1}`)
	}))
	t.Cleanup(server.Close)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := run([]string{
		"-mode", "agent-control",
		"-control-action", "tasks",
		"-url", server.URL,
		"-team-id", "team-1",
		"-limit", "5",
	}, &stdout, &stderr)
	require.NoError(t, err)

	output := stdout.String()
	assert.Contains(t, output, "agent_control_tasks count=1")
	assert.Contains(t, output, "task_id=task-1 workflow=spawn_team team_id=team-1 assignee=member-1 status=running title=Review docs")
}

func TestRun_AgentControlDependencies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/api/runtime/agent-control/tasks/task%2F1/dependencies", r.URL.EscapedPath())
		assert.Equal(t, "depends_on_id=task%2F0&include_dependents=true&team_id=team-1&workflow=spawn_team", r.URL.RawQuery)
		_, _ = io.WriteString(w, `{"task_id":"task/1","dependencies":["task/0"],"dependents":["task/2"],"edges":[{"id":"dep-1","workflow":"spawn_team","team_id":"team-1","task_id":"task/1","depends_on_id":"task/0"}],"count":1,"edge_count":1}`)
	}))
	t.Cleanup(server.Close)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := run([]string{
		"-mode", "agent-control",
		"-control-action", "dependencies",
		"-url", server.URL,
		"-team-id", "team-1",
		"-task-id", "task/1",
		"-depends-on-id", "task/0",
		"-include-dependents",
	}, &stdout, &stderr)
	require.NoError(t, err)

	output := stdout.String()
	assert.Contains(t, output, "agent_control_dependencies task_id=task/1 count=1 edge_count=1")
	assert.Contains(t, output, "dependencies=task/0")
	assert.Contains(t, output, "dependents=task/2")
	assert.Contains(t, output, "edge id=dep-1 task_id=task/1 depends_on_id=task/0 workflow=spawn_team team_id=team-1")
}

func TestRun_AgentControlAddDependency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/runtime/agent-control/tasks/task%2F1/dependencies", r.URL.EscapedPath())
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), `"depends_on_id":"task/0"`)
		_, _ = io.WriteString(w, `{"task_id":"task/1","depends_on_id":"task/0"}`)
	}))
	t.Cleanup(server.Close)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := run([]string{
		"-mode", "agent-control",
		"-control-action", "add-dependency",
		"-url", server.URL,
		"-team-id", "team-1",
		"-task-id", "task/1",
		"-depends-on-id", "task/0",
	}, &stdout, &stderr)
	require.NoError(t, err)

	output := stdout.String()
	assert.Contains(t, output, "agent_control_dependency_created=true")
	assert.Contains(t, output, "task_id=task/1 depends_on_id=task/0")
}

func joinLines(lines []string) string {
	var buf bytes.Buffer
	for i, line := range lines {
		if i > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(line)
	}
	return buf.String()
}
