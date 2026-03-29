package toolbroker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/google/uuid"
)

const (
	ToolAskUserQuestion   = "ask_user_question"
	ToolBackgroundTask    = "background_task"
	ToolTaskOutput        = "task_output"
	ToolSpawnAgent        = "spawn_agent"
	ToolSendInput         = "send_input"
	ToolWaitAgent         = "wait_agent"
	ToolReadAgentEvents   = "read_agent_events"
	ToolCloseAgent        = "close_agent"
	ToolResumeAgent       = "resume_agent"
	ToolSpawnTeam         = "spawn_team"
	ToolSendTeamMessage   = "send_team_message"
	ToolReadMailboxDigest = "read_mailbox_digest"
	ToolReadTaskSpec      = "read_task_spec"
	ToolReadTaskContext   = "read_task_context"
	ToolReportTaskOutcome = "report_task_outcome"
	ToolBlockCurrentTask  = "block_current_task"
)

// Broker provides synthetic tools backed by runtime services.
type Broker struct {
	UserInput            UserInputHandler
	Background           *background.Manager
	AgentSessions        AgentSessionController
	TeamStore            team.Store
	TeamClaims           *team.PathClaimManager
	TeamPlanner          *team.LeadPlanner
	TeamDispatcher       TeamMailboxDispatcher
	TeamLifecycleChanged func()
}

// IsBrokerTool returns true if the tool is handled by the broker.
func (b *Broker) IsBrokerTool(name string) bool {
	switch normalizeToolName(name) {
	case ToolAskUserQuestion, ToolBackgroundTask, ToolTaskOutput, ToolSpawnAgent, ToolSendInput, ToolWaitAgent, ToolReadAgentEvents, ToolCloseAgent, ToolResumeAgent, ToolSpawnTeam, ToolSendTeamMessage, ToolReadMailboxDigest, ToolReadTaskSpec, ToolReadTaskContext, ToolReportTaskOutcome, ToolBlockCurrentTask:
		return true
	default:
		return false
	}
}

// Definitions returns tool definitions exposed to the LLM.
func (b *Broker) Definitions() []types.ToolDefinition {
	definitions := []types.ToolDefinition{
		{
			Name:        ToolAskUserQuestion,
			Description: "Ask the user for required input and wait for an answer.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"prompt": map[string]interface{}{
						"type":        "string",
						"description": "Question to show the user.",
					},
					"suggestions": map[string]interface{}{
						"type":  "array",
						"items": map[string]interface{}{"type": "string"},
					},
					"required": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether the answer is required to continue.",
					},
				},
				"required": []string{"prompt"},
			},
		},
		{
			Name:        ToolBackgroundTask,
			Description: "Run a long-running task in the background and return a job id.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": map[string]interface{}{
						"type":        "string",
						"description": "Shell command to execute.",
					},
					"cwd": map[string]interface{}{
						"type":        "string",
						"description": "Working directory.",
					},
					"timeout_sec": map[string]interface{}{
						"type":        "integer",
						"description": "Command timeout in seconds.",
					},
					"priority": map[string]interface{}{
						"type":        "integer",
						"description": "Queue priority.",
					},
					"restart_policy": map[string]interface{}{
						"type":        "string",
						"description": "Optional restart handling policy: fail (default) or rerun.",
					},
				},
				"required": []string{"command"},
			},
		},
		{
			Name:        ToolTaskOutput,
			Description: "Read background task output by offset.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"job_id": map[string]interface{}{
						"type":        "string",
						"description": "Background job id.",
					},
					"offset": map[string]interface{}{
						"type":        "integer",
						"description": "Read offset.",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum bytes to return.",
					},
				},
				"required": []string{"job_id"},
			},
		},
	}
	if b == nil || b.TeamStore == nil {
		if b == nil || b.AgentSessions == nil {
			return definitions
		}
		return append(definitions,
			types.ToolDefinition{
				Name:        ToolSpawnAgent,
				Description: "Create a lightweight child agent session and optionally send its first prompt.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":           map[string]interface{}{"type": "string", "description": "Optional explicit child session id."},
						"session_id":   map[string]interface{}{"type": "string", "description": "Alias for id."},
						"message":      map[string]interface{}{"type": "string", "description": "Initial prompt for the child agent."},
						"agent_type":   map[string]interface{}{"type": "string", "description": "Optional role hint for the child agent."},
						"model":        map[string]interface{}{"type": "string", "description": "Optional model hint stored on the child session."},
						"fork_context": map[string]interface{}{"type": "boolean", "description": "Whether to copy the parent session history into the child session."},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolSendInput,
				Description: "Send a follow-up prompt to an existing child agent session.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":         map[string]interface{}{"type": "string", "description": "Child agent session id."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for id."},
						"message":    map[string]interface{}{"type": "string", "description": "Prompt to send to the child agent."},
						"interrupt":  map[string]interface{}{"type": "boolean", "description": "Whether to interrupt an active child run before submitting the new prompt."},
					},
					"required": []string{"message"},
				},
			},
			types.ToolDefinition{
				Name:        ToolWaitAgent,
				Description: "Wait for a child agent session to become idle or blocked.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":          map[string]interface{}{"type": "string", "description": "Child agent session id."},
						"session_id":  map[string]interface{}{"type": "string", "description": "Alias for id."},
						"ids":         map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Optional list of child agent ids. Returns when the first one becomes ready."},
						"session_ids": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Alias for ids."},
						"timeout_ms":  map[string]interface{}{"type": "integer", "description": "Optional wait timeout in milliseconds."},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolReadAgentEvents,
				Description: "Read recent runtime events for a child agent session and optionally wait for new events.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":         map[string]interface{}{"type": "string", "description": "Child agent session id."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for id."},
						"after_seq":  map[string]interface{}{"type": "integer", "description": "Only return events after this sequence number."},
						"limit":      map[string]interface{}{"type": "integer", "description": "Maximum number of events to return."},
						"wait_ms":    map[string]interface{}{"type": "integer", "description": "Optional wait timeout for polling until new events arrive."},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolReadAgentEvents,
				Description: "Read recent runtime events for a child agent session and optionally wait for new events.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":         map[string]interface{}{"type": "string", "description": "Child agent session id."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for id."},
						"after_seq":  map[string]interface{}{"type": "integer", "description": "Only return events after this sequence number."},
						"limit":      map[string]interface{}{"type": "integer", "description": "Maximum number of events to return."},
						"wait_ms":    map[string]interface{}{"type": "integer", "description": "Optional wait timeout for polling until new events arrive."},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolCloseAgent,
				Description: "Stop and close a child agent session.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":         map[string]interface{}{"type": "string", "description": "Child agent session id."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for id."},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolResumeAgent,
				Description: "Recreate an in-memory actor for an existing child agent session.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":         map[string]interface{}{"type": "string", "description": "Child agent session id."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for id."},
					},
				},
			},
		)
	}
	if b.AgentSessions != nil {
		definitions = append(definitions,
			types.ToolDefinition{
				Name:        ToolSpawnAgent,
				Description: "Create a lightweight child agent session and optionally send its first prompt.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":           map[string]interface{}{"type": "string", "description": "Optional explicit child session id."},
						"session_id":   map[string]interface{}{"type": "string", "description": "Alias for id."},
						"message":      map[string]interface{}{"type": "string", "description": "Initial prompt for the child agent."},
						"agent_type":   map[string]interface{}{"type": "string", "description": "Optional role hint for the child agent."},
						"model":        map[string]interface{}{"type": "string", "description": "Optional model hint stored on the child session."},
						"fork_context": map[string]interface{}{"type": "boolean", "description": "Whether to copy the parent session history into the child session."},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolSendInput,
				Description: "Send a follow-up prompt to an existing child agent session.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":         map[string]interface{}{"type": "string", "description": "Child agent session id."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for id."},
						"message":    map[string]interface{}{"type": "string", "description": "Prompt to send to the child agent."},
						"interrupt":  map[string]interface{}{"type": "boolean", "description": "Whether to interrupt an active child run before submitting the new prompt."},
					},
					"required": []string{"message"},
				},
			},
			types.ToolDefinition{
				Name:        ToolWaitAgent,
				Description: "Wait for a child agent session to become idle or blocked.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":          map[string]interface{}{"type": "string", "description": "Child agent session id."},
						"session_id":  map[string]interface{}{"type": "string", "description": "Alias for id."},
						"ids":         map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Optional list of child agent ids. Returns when the first one becomes ready."},
						"session_ids": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Alias for ids."},
						"timeout_ms":  map[string]interface{}{"type": "integer", "description": "Optional wait timeout in milliseconds."},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolReadAgentEvents,
				Description: "Read recent runtime events for a child agent session and optionally wait for new events.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":         map[string]interface{}{"type": "string", "description": "Child agent session id."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for id."},
						"after_seq":  map[string]interface{}{"type": "integer", "description": "Only return events after this sequence number."},
						"limit":      map[string]interface{}{"type": "integer", "description": "Maximum number of events to return."},
						"wait_ms":    map[string]interface{}{"type": "integer", "description": "Optional wait timeout for polling until new events arrive."},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolReadAgentEvents,
				Description: "Read recent runtime events for a child agent session and optionally wait for new events.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":         map[string]interface{}{"type": "string", "description": "Child agent session id."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for id."},
						"after_seq":  map[string]interface{}{"type": "integer", "description": "Only return events after this sequence number."},
						"limit":      map[string]interface{}{"type": "integer", "description": "Maximum number of events to return."},
						"wait_ms":    map[string]interface{}{"type": "integer", "description": "Optional wait timeout for polling until new events arrive."},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolCloseAgent,
				Description: "Stop and close a child agent session.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":         map[string]interface{}{"type": "string", "description": "Child agent session id."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for id."},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolResumeAgent,
				Description: "Recreate an in-memory actor for an existing child agent session.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":         map[string]interface{}{"type": "string", "description": "Child agent session id."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for id."},
					},
				},
			},
		)
	}
	if b == nil || b.TeamStore == nil {
		return definitions
	}
	reportOutcomeSchema := team.TaskOutcomeContractSchemaFor(team.TaskOutcomeDone, team.TaskOutcomeFailed, team.TaskOutcomeBlocked, team.TaskOutcomeHandoff)
	reportOutcomeProperties, _ := reportOutcomeSchema["properties"].(map[string]interface{})
	blockOutcomeSchema := team.TaskOutcomeContractSchemaFor(team.TaskOutcomeBlocked, team.TaskOutcomeHandoff)
	blockOutcomeProperties, _ := blockOutcomeSchema["properties"].(map[string]interface{})
	definitions = append(definitions,
		types.ToolDefinition{
			Name:        ToolSpawnTeam,
			Description: "Create a team with optional teammates and tasks. Use when the user asks to spin up a team or delegate work to multiple agents. If auto_start is true, the team starts running immediately in the background.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"team_id": map[string]interface{}{
						"type":        "string",
						"description": "Optional explicit team id. If provided and exists, defaults to reuse unless allow_existing is false.",
					},
					"workspace_id": map[string]interface{}{
						"type":        "string",
						"description": "Optional workspace id for the team.",
					},
					"lead_session_id": map[string]interface{}{
						"type":        "string",
						"description": "Optional lead session id for the team.",
					},
					"strategy": map[string]interface{}{
						"type":        "string",
						"description": "Optional scheduling strategy label.",
					},
					"status": map[string]interface{}{
						"type":        "string",
						"description": "Optional team status: active, paused, done, failed. Defaults to active.",
					},
					"max_teammates": map[string]interface{}{
						"type":        "integer",
						"description": "Optional maximum concurrent teammates.",
					},
					"max_writers": map[string]interface{}{
						"type":        "integer",
						"description": "Optional maximum concurrent writers (tasks with write paths).",
					},
					"allow_existing": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether to reuse an existing team_id if present. Defaults to true.",
					},
					"auto_start": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether to auto-start team orchestration loop if available. Defaults to true. When true, delegated work is already running, so the assistant should not ask the user to pick the next step before that background work settles.",
					},
					"teammates": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"id": map[string]interface{}{
									"type":        "string",
									"description": "Optional teammate id (stable).",
								},
								"name": map[string]interface{}{
									"type":        "string",
									"description": "Human-friendly teammate name.",
								},
								"profile": map[string]interface{}{
									"type":        "string",
									"description": "Optional profile id to use for this teammate.",
								},
								"session_id": map[string]interface{}{
									"type":        "string",
									"description": "Optional session id; needed for active execution.",
								},
								"state": map[string]interface{}{
									"type":        "string",
									"description": "Initial state: idle, busy, blocked, offline. Defaults to idle.",
								},
								"capabilities": map[string]interface{}{
									"type":  "array",
									"items": map[string]interface{}{"type": "string"},
								},
							},
						},
					},
					"tasks": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"id": map[string]interface{}{
									"type":        "string",
									"description": "Optional stable task id.",
								},
								"title": map[string]interface{}{
									"type":        "string",
									"description": "Task title.",
								},
								"goal": map[string]interface{}{
									"type":        "string",
									"description": "Task goal or objective.",
								},
								"inputs": map[string]interface{}{
									"type":  "array",
									"items": map[string]interface{}{"type": "string"},
								},
								"read_paths": map[string]interface{}{
									"type":  "array",
									"items": map[string]interface{}{"type": "string"},
								},
								"write_paths": map[string]interface{}{
									"type":  "array",
									"items": map[string]interface{}{"type": "string"},
								},
								"deliverables": map[string]interface{}{
									"type":  "array",
									"items": map[string]interface{}{"type": "string"},
								},
								"priority": map[string]interface{}{
									"type":        "integer",
									"description": "Optional priority (higher is more important).",
								},
								"assignee": map[string]interface{}{
									"type":        "string",
									"description": "Optional teammate id to assign.",
								},
								"depends_on": map[string]interface{}{
									"type":        "array",
									"items":       map[string]interface{}{"type": "string"},
									"description": "Optional list of task ids this task depends on.",
								},
							},
						},
					},
				},
			},
		},
		types.ToolDefinition{
			Name:        ToolSendTeamMessage,
			Description: "Send a direct or broadcast mailbox message within the current team.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"team_id": map[string]interface{}{
						"type":        "string",
						"description": "Optional team id override; must match the current run when present.",
					},
					"to_agent": map[string]interface{}{
						"type":        "string",
						"description": "Recipient agent id. Use * to broadcast.",
					},
					"kind": map[string]interface{}{
						"type":        "string",
						"description": "Message kind such as info, question, warning, or done.",
					},
					"body": map[string]interface{}{
						"type":        "string",
						"description": "Message body.",
					},
					"task_id": map[string]interface{}{
						"type":        "string",
						"description": "Optional related task id.",
					},
					"metadata": map[string]interface{}{
						"type":                 "object",
						"additionalProperties": true,
					},
				},
				"required": []string{"body"},
			},
		},
		types.ToolDefinition{
			Name:        ToolReadMailboxDigest,
			Description: "Read unread mailbox context for the current teammate, including broadcast messages.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"team_id": map[string]interface{}{
						"type":        "string",
						"description": "Optional team id override; must match the current run when present.",
					},
					"agent_id": map[string]interface{}{
						"type":        "string",
						"description": "Optional teammate id override.",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum unread messages to summarize.",
					},
					"mark_read": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether to mark returned messages as read. Defaults to true.",
					},
				},
			},
		},
		types.ToolDefinition{
			Name:        ToolReadTaskSpec,
			Description: "Read the current task specification for the team run.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"team_id": map[string]interface{}{
						"type":        "string",
						"description": "Optional team id override; must match the current run when present.",
					},
					"task_id": map[string]interface{}{
						"type":        "string",
						"description": "Optional task id override; defaults to the current run task.",
					},
				},
			},
		},
		types.ToolDefinition{
			Name:        ToolReadTaskContext,
			Description: "Read the current task specification plus richer team context for the active task.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"team_id": map[string]interface{}{
						"type":        "string",
						"description": "Optional team id override; must match the current run when present.",
					},
					"task_id": map[string]interface{}{
						"type":        "string",
						"description": "Optional task id override; defaults to the current run task.",
					},
					"include_dependencies": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether to include dependency and dependent task ids. Defaults to true.",
					},
					"include_mailbox": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether to include an unread mailbox digest for the current teammate. Defaults to true.",
					},
					"mailbox_limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum unread mailbox messages to summarize when mailbox context is included.",
					},
					"mark_read": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether mailbox messages included in the context should be marked read. Defaults to true.",
					},
					"context_budget": map[string]interface{}{
						"type":        "integer",
						"description": "Budget hint passed to the team context builder.",
					},
				},
			},
		},
		types.ToolDefinition{
			Name:        ToolReportTaskOutcome,
			Description: "Report a structured done, failed, blocked, or handoff outcome for the current team task.",
			Metadata: map[string]interface{}{
				"canonical": true,
				"replaces":  []string{ToolBlockCurrentTask},
			},
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"team_id": map[string]interface{}{
						"type":        "string",
						"description": "Optional team id override; must match the current run when present.",
					},
					"task_id": map[string]interface{}{
						"type":        "string",
						"description": "Optional task id override; defaults to the current run task.",
					},
					"task_status": reportOutcomeProperties["task_status"],
					"summary":     reportOutcomeProperties["summary"],
					"blocker":     reportOutcomeProperties["blocker"],
					"handoff_to":  reportOutcomeProperties["handoff_to"],
					"result_ref": map[string]interface{}{
						"type":        "string",
						"description": "Optional result reference stored on the task for done or failed outcomes.",
					},
					"notify_lead": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether blocked or handoff outcomes should notify the lead or handoff recipient. Defaults to true.",
					},
					"auto_replan": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether blocked outcomes should invoke the lead planner. Defaults to true unless handing off to a non-lead recipient.",
					},
				},
				"required": []string{"task_status", "summary"},
			},
		},
		types.ToolDefinition{
			Name:        ToolBlockCurrentTask,
			Description: "Compatibility alias for report_task_outcome when reporting blocked or handoff outcomes.",
			Metadata: map[string]interface{}{
				"compatibility_alias": true,
				"canonical_tool":      ToolReportTaskOutcome,
			},
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"team_id": map[string]interface{}{
						"type":        "string",
						"description": "Optional team id override; must match the current run when present.",
					},
					"task_id": map[string]interface{}{
						"type":        "string",
						"description": "Optional task id override; defaults to the current run task.",
					},
					"summary": map[string]interface{}{
						"type":        "string",
						"description": "Outcome summary. Legacy shorthand when no structured status fields are provided.",
					},
					"task_status": blockOutcomeProperties["task_status"],
					"blocker":     blockOutcomeProperties["blocker"],
					"handoff_to":  blockOutcomeProperties["handoff_to"],
					"notify_lead": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether to send a mailbox message to the lead or handoff target. Defaults to true.",
					},
					"auto_replan": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether to invoke the lead planner for follow-up tasks. Defaults to true unless handing off to a non-lead recipient.",
					},
				},
			},
		},
	)
	return definitions
}

// Execute runs a broker tool without an originating tool call id.
func (b *Broker) Execute(ctx context.Context, sessionID, toolName string, args map[string]interface{}) (interface{}, map[string]interface{}, error) {
	return b.execute(ctx, sessionID, toolName, args, "")
}

// ExecuteToolCall runs a broker tool for a concrete tool call.
func (b *Broker) ExecuteToolCall(ctx context.Context, sessionID string, call types.ToolCall) (interface{}, map[string]interface{}, error) {
	return b.execute(ctx, sessionID, call.Name, call.Args, call.ID)
}

// execute runs a broker tool.
func (b *Broker) execute(ctx context.Context, sessionID, toolName string, args map[string]interface{}, toolCallID string) (interface{}, map[string]interface{}, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	switch normalizeToolName(toolName) {
	case ToolAskUserQuestion:
		if b.UserInput == nil {
			return nil, nil, fmt.Errorf("user input handler is not configured")
		}
		request := AskUserQuestionArgs{}
		if value, ok := args["prompt"].(string); ok {
			request.Prompt = strings.TrimSpace(value)
		}
		if request.Prompt == "" {
			return nil, nil, fmt.Errorf("prompt is required")
		}
		request.Required = true
		if value, ok := args["required"].(bool); ok {
			request.Required = value
		}
		if raw, ok := args["suggestions"]; ok {
			switch items := raw.(type) {
			case []string:
				request.Suggestions = append([]string(nil), items...)
			case []interface{}:
				for _, item := range items {
					if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
						request.Suggestions = append(request.Suggestions, strings.TrimSpace(text))
					}
				}
			}
		}
		questionID := "q_" + uuid.NewString()
		answer, err := b.UserInput.AskUserQuestion(ctx, UserQuestionRequest{
			ID:          questionID,
			SessionID:   sessionID,
			ToolCallID:  strings.TrimSpace(toolCallID),
			Prompt:      request.Prompt,
			Suggestions: request.Suggestions,
			Required:    request.Required,
			CreatedAt:   time.Now().UTC(),
		})
		if err != nil {
			return nil, nil, err
		}
		return AskUserQuestionResult{QuestionID: questionID, Answer: answer}, nil, nil

	case ToolBackgroundTask:
		if b.Background == nil {
			b.Background = background.NewManager(background.DefaultConfig())
		}
		command, _ := args["command"].(string)
		command = strings.TrimSpace(command)
		if command == "" {
			return nil, nil, fmt.Errorf("command is required")
		}
		req := BackgroundTaskArgs{Command: command}
		if value, ok := args["cwd"].(string); ok {
			req.Cwd = strings.TrimSpace(value)
		}
		if value, ok := args["timeout_sec"].(float64); ok {
			req.TimeoutSec = int(value)
		} else if value, ok := args["timeout_sec"].(int); ok {
			req.TimeoutSec = value
		}
		if value, ok := args["priority"].(float64); ok {
			req.Priority = int(value)
		} else if value, ok := args["priority"].(int); ok {
			req.Priority = value
		}
		if value, ok := args["restart_policy"].(string); ok {
			req.RestartPolicy = background.RestartPolicy(strings.TrimSpace(value))
		}
		job, err := b.Background.SubmitShell(ctx, sessionID, background.BackgroundTaskArgs{
			Command:       req.Command,
			Cwd:           req.Cwd,
			TimeoutSec:    req.TimeoutSec,
			Priority:      req.Priority,
			RestartPolicy: req.RestartPolicy,
		})
		if err != nil {
			return nil, nil, err
		}
		return BackgroundTaskResult{
			JobID:         job.ID,
			Status:        string(job.Status),
			Message:       job.Message,
			RestartPolicy: job.RestartPolicy,
		}, nil, nil

	case ToolTaskOutput:
		if b.Background == nil {
			return nil, nil, fmt.Errorf("background manager is not configured")
		}
		jobID, _ := args["job_id"].(string)
		jobID = strings.TrimSpace(jobID)
		if jobID == "" {
			return nil, nil, fmt.Errorf("job_id is required")
		}
		offset := int64(0)
		limit := 0
		if value, ok := args["offset"].(float64); ok {
			offset = int64(value)
		} else if value, ok := args["offset"].(int); ok {
			offset = int64(value)
		}
		if value, ok := args["limit"].(float64); ok {
			limit = int(value)
		} else if value, ok := args["limit"].(int); ok {
			limit = value
		}
		output, err := b.Background.ReadOutput(ctx, background.TaskOutputArgs{
			JobID:  jobID,
			Offset: offset,
			Limit:  limit,
		})
		if err != nil {
			return nil, nil, err
		}
		return TaskOutputResult{
			JobID:      output.JobID,
			Status:     output.Status,
			Output:     output.Output,
			NextOffset: output.NextOffset,
			ExitCode:   output.ExitCode,
		}, nil, nil

	case ToolSpawnAgent:
		if b.AgentSessions == nil {
			return nil, nil, fmt.Errorf("agent session controller is not configured")
		}
		request := SpawnAgentArgs{}
		if value, ok := args["id"].(string); ok {
			request.ID = strings.TrimSpace(value)
		}
		if value, ok := args["session_id"].(string); ok && strings.TrimSpace(request.ID) == "" {
			request.SessionID = strings.TrimSpace(value)
		}
		if value, ok := args["message"].(string); ok {
			request.Message = strings.TrimSpace(value)
		}
		if value, ok := args["agent_type"].(string); ok {
			request.AgentType = strings.TrimSpace(value)
		}
		if value, ok := args["model"].(string); ok {
			request.Model = strings.TrimSpace(value)
		}
		if value, ok := args["fork_context"].(bool); ok {
			request.ForkContext = &value
		}
		result, err := b.AgentSessions.Spawn(ctx, strings.TrimSpace(sessionID), request)
		if err != nil {
			return nil, nil, err
		}
		return result, map[string]interface{}{
			"session_id": result.SessionID,
			"status":     result.Status,
			"created":    result.Created,
			"queued":     result.Queued,
		}, nil

	case ToolSendInput:
		if b.AgentSessions == nil {
			return nil, nil, fmt.Errorf("agent session controller is not configured")
		}
		request := SendAgentInputArgs{}
		if value, ok := args["id"].(string); ok {
			request.ID = strings.TrimSpace(value)
		}
		if value, ok := args["session_id"].(string); ok && strings.TrimSpace(request.ID) == "" {
			request.SessionID = strings.TrimSpace(value)
		}
		if value, ok := args["message"].(string); ok {
			request.Message = strings.TrimSpace(value)
		}
		if value, ok := args["interrupt"].(bool); ok {
			request.Interrupt = &value
		}
		result, err := b.AgentSessions.SendInput(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		return result, map[string]interface{}{
			"session_id": result.SessionID,
			"status":     result.Status,
			"queued":     result.Queued,
		}, nil

	case ToolWaitAgent:
		if b.AgentSessions == nil {
			return nil, nil, fmt.Errorf("agent session controller is not configured")
		}
		request := WaitAgentArgs{}
		if value, ok := args["id"].(string); ok {
			request.ID = strings.TrimSpace(value)
		}
		if value, ok := args["session_id"].(string); ok && strings.TrimSpace(request.ID) == "" {
			request.SessionID = strings.TrimSpace(value)
		}
		if value, ok := args["timeout_ms"].(float64); ok {
			request.TimeoutMs = int(value)
		} else if value, ok := args["timeout_ms"].(int); ok {
			request.TimeoutMs = value
		}
		request.IDs = coerceStringSlice(args["ids"])
		request.SessionIDs = coerceStringSlice(args["session_ids"])
		result, err := b.AgentSessions.Wait(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		return result, map[string]interface{}{
			"session_id":  result.MatchedSessionID,
			"status":      waitResultStatus(result),
			"timed_out":   result.TimedOut,
			"ready_count": result.ReadyCount,
		}, nil

	case ToolReadAgentEvents:
		if b.AgentSessions == nil {
			return nil, nil, fmt.Errorf("agent session controller is not configured")
		}
		request := ReadAgentEventsArgs{}
		if value, ok := args["id"].(string); ok {
			request.ID = strings.TrimSpace(value)
		}
		if value, ok := args["session_id"].(string); ok && strings.TrimSpace(request.ID) == "" {
			request.SessionID = strings.TrimSpace(value)
		}
		if value, ok := args["after_seq"].(float64); ok {
			request.AfterSeq = int64(value)
		} else if value, ok := args["after_seq"].(int64); ok {
			request.AfterSeq = value
		} else if value, ok := args["after_seq"].(int); ok {
			request.AfterSeq = int64(value)
		}
		if value, ok := args["limit"].(float64); ok {
			request.Limit = int(value)
		} else if value, ok := args["limit"].(int); ok {
			request.Limit = value
		}
		if value, ok := args["wait_ms"].(float64); ok {
			request.WaitMs = int(value)
		} else if value, ok := args["wait_ms"].(int); ok {
			request.WaitMs = value
		}
		result, err := b.AgentSessions.ReadEvents(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		return result, map[string]interface{}{
			"session_id": result.SessionID,
			"count":      result.Count,
			"latest_seq": result.LatestSeq,
			"timed_out":  result.TimedOut,
		}, nil

	case ToolCloseAgent:
		if b.AgentSessions == nil {
			return nil, nil, fmt.Errorf("agent session controller is not configured")
		}
		sessionKey := strings.TrimSpace(stringValue(args["id"]))
		if sessionKey == "" {
			sessionKey = strings.TrimSpace(stringValue(args["session_id"]))
		}
		if sessionKey == "" {
			return nil, nil, fmt.Errorf("id is required")
		}
		result, err := b.AgentSessions.Close(ctx, sessionKey)
		if err != nil {
			return nil, nil, err
		}
		return result, map[string]interface{}{
			"session_id": result.SessionID,
			"status":     result.Status,
		}, nil

	case ToolResumeAgent:
		if b.AgentSessions == nil {
			return nil, nil, fmt.Errorf("agent session controller is not configured")
		}
		sessionKey := strings.TrimSpace(stringValue(args["id"]))
		if sessionKey == "" {
			sessionKey = strings.TrimSpace(stringValue(args["session_id"]))
		}
		if sessionKey == "" {
			return nil, nil, fmt.Errorf("id is required")
		}
		result, err := b.AgentSessions.Resume(ctx, sessionKey)
		if err != nil {
			return nil, nil, err
		}
		return result, map[string]interface{}{
			"session_id": result.SessionID,
			"status":     result.Status,
		}, nil

	case ToolSpawnTeam:
		if b.TeamStore == nil {
			return nil, nil, fmt.Errorf("team store is not configured")
		}
		request := SpawnTeamArgs{}
		if value, ok := args["team_id"].(string); ok {
			request.TeamID = strings.TrimSpace(value)
		}
		if value, ok := args["workspace_id"].(string); ok {
			request.WorkspaceID = strings.TrimSpace(value)
		}
		if value, ok := args["lead_session_id"].(string); ok {
			request.LeadSessionID = strings.TrimSpace(value)
		}
		if isCurrentPlaceholder(request.LeadSessionID) {
			request.LeadSessionID = strings.TrimSpace(sessionID)
		}
		if currentSessionID := strings.TrimSpace(sessionID); currentSessionID != "" {
			// For chat-originated spawn_team calls, keep the lead bound to the
			// current session. This prevents the model from inventing a detached
			// lead_session_id that would break ambient team state and prompt gating.
			request.LeadSessionID = currentSessionID
		}
		if value, ok := args["workspace_id"].(string); ok {
			request.WorkspaceID = strings.TrimSpace(value)
			if isCurrentPlaceholder(request.WorkspaceID) {
				request.WorkspaceID = ""
			}
		}
		if value, ok := args["strategy"].(string); ok {
			request.Strategy = strings.TrimSpace(value)
		}
		if value, ok := args["status"].(string); ok {
			request.Status = strings.TrimSpace(value)
		}
		if value, ok := args["max_teammates"].(float64); ok {
			request.MaxTeammates = int(value)
		} else if value, ok := args["max_teammates"].(int); ok {
			request.MaxTeammates = value
		}
		if value, ok := args["max_writers"].(float64); ok {
			request.MaxWriters = int(value)
		} else if value, ok := args["max_writers"].(int); ok {
			request.MaxWriters = value
		}
		if value, ok := args["allow_existing"].(bool); ok {
			request.AllowExisting = &value
		}
		if value, ok := args["auto_start"].(bool); ok {
			request.AutoStart = &value
		}
		if raw, ok := args["teammates"]; ok {
			switch items := raw.(type) {
			case []interface{}:
				for _, item := range items {
					if entry, ok := item.(map[string]interface{}); ok {
						spec := SpawnTeammateSpec{}
						if value, ok := entry["id"].(string); ok {
							spec.ID = strings.TrimSpace(value)
						}
						if value, ok := entry["name"].(string); ok {
							spec.Name = strings.TrimSpace(value)
						}
						if value, ok := entry["profile"].(string); ok {
							spec.Profile = strings.TrimSpace(value)
						}
						if value, ok := entry["session_id"].(string); ok {
							spec.SessionID = strings.TrimSpace(value)
							if isCurrentPlaceholder(spec.SessionID) {
								spec.SessionID = ""
							}
						}
						if value, ok := entry["state"].(string); ok {
							spec.State = strings.TrimSpace(value)
						}
						if caps := coerceStringSlice(entry["capabilities"]); len(caps) > 0 {
							spec.Capabilities = caps
						}
						request.Teammates = append(request.Teammates, spec)
					}
				}
			}
		}
		if raw, ok := args["tasks"]; ok {
			switch items := raw.(type) {
			case []interface{}:
				for _, item := range items {
					if entry, ok := item.(map[string]interface{}); ok {
						spec := SpawnTaskSpec{}
						if value, ok := entry["id"].(string); ok {
							spec.ID = strings.TrimSpace(value)
						}
						if value, ok := entry["title"].(string); ok {
							spec.Title = strings.TrimSpace(value)
						}
						if value, ok := entry["goal"].(string); ok {
							spec.Goal = strings.TrimSpace(value)
						}
						spec.Inputs = coerceStringSlice(entry["inputs"])
						spec.ReadPaths = coerceStringSlice(entry["read_paths"])
						spec.WritePaths = coerceStringSlice(entry["write_paths"])
						spec.Deliverables = coerceStringSlice(entry["deliverables"])
						if value, ok := entry["priority"].(float64); ok {
							spec.Priority = int(value)
						} else if value, ok := entry["priority"].(int); ok {
							spec.Priority = value
						}
						if value, ok := entry["assignee"].(string); ok {
							spec.Assignee = strings.TrimSpace(value)
						}
						spec.DependsOn = coerceStringSlice(entry["depends_on"])
						request.Tasks = append(request.Tasks, spec)
					}
				}
			}
		}
		preparedTaskSpecs, err := b.prepareSpawnTaskSpecs(request.Tasks)
		if err != nil {
			return nil, nil, err
		}
		request.Tasks = preparedTaskSpecs

		allowExisting := true
		if request.AllowExisting != nil {
			allowExisting = *request.AllowExisting
		}
		autoStart := true
		if request.AutoStart != nil {
			autoStart = *request.AutoStart
		}

		var createdTeam bool
		var teamID string
		if strings.TrimSpace(request.TeamID) != "" {
			teamID = strings.TrimSpace(request.TeamID)
			existing, err := b.TeamStore.GetTeam(ctx, teamID)
			if err != nil {
				return nil, nil, err
			}
			if existing != nil {
				if !allowExisting {
					return nil, nil, fmt.Errorf("team_id already exists")
				}
				if existing.Status == team.TeamStatusDone || existing.Status == team.TeamStatusFailed ||
					(strings.TrimSpace(existing.LeadSessionID) != "" &&
						strings.TrimSpace(request.LeadSessionID) != "" &&
						!strings.EqualFold(strings.TrimSpace(existing.LeadSessionID), strings.TrimSpace(request.LeadSessionID))) {
					teamID = b.nextAvailableTeamID(ctx, teamID)
					status, err := parseTeamStatus(request.Status)
					if err != nil {
						return nil, nil, err
					}
					teamRecord := team.Team{
						ID:            teamID,
						WorkspaceID:   request.WorkspaceID,
						LeadSessionID: request.LeadSessionID,
						Status:        status,
						Strategy:      request.Strategy,
						MaxTeammates:  request.MaxTeammates,
						MaxWriters:    request.MaxWriters,
					}
					createdID, err := b.TeamStore.CreateTeam(ctx, teamRecord)
					if err != nil {
						return nil, nil, err
					}
					teamID = createdID
					createdTeam = true
				}
			} else {
				status, err := parseTeamStatus(request.Status)
				if err != nil {
					return nil, nil, err
				}
				teamRecord := team.Team{
					ID:            teamID,
					WorkspaceID:   request.WorkspaceID,
					LeadSessionID: request.LeadSessionID,
					Status:        status,
					Strategy:      request.Strategy,
					MaxTeammates:  request.MaxTeammates,
					MaxWriters:    request.MaxWriters,
				}
				createdID, err := b.TeamStore.CreateTeam(ctx, teamRecord)
				if err != nil {
					return nil, nil, err
				}
				teamID = createdID
				createdTeam = true
			}
		} else {
			status, err := parseTeamStatus(request.Status)
			if err != nil {
				return nil, nil, err
			}
			teamRecord := team.Team{
				WorkspaceID:   request.WorkspaceID,
				LeadSessionID: request.LeadSessionID,
				Status:        status,
				Strategy:      request.Strategy,
				MaxTeammates:  request.MaxTeammates,
				MaxWriters:    request.MaxWriters,
			}
			createdID, err := b.TeamStore.CreateTeam(ctx, teamRecord)
			if err != nil {
				return nil, nil, err
			}
			teamID = createdID
			createdTeam = true
		}

		teammateIDs := make([]string, 0, len(request.Teammates))
		if allocator, ok := b.TeamDispatcher.(interface {
			EnsureTeammateSessionIDs(teamID string, specs []SpawnTeammateSpec) []SpawnTeammateSpec
		}); ok {
			request.Teammates = allocator.EnsureTeammateSessionIDs(teamID, request.Teammates)
		}
		for _, spec := range request.Teammates {
			state, err := parseTeammateState(spec.State)
			if err != nil {
				return nil, nil, err
			}
			teammate := team.Teammate{
				ID:           strings.TrimSpace(spec.ID),
				TeamID:       teamID,
				Name:         strings.TrimSpace(spec.Name),
				Profile:      strings.TrimSpace(spec.Profile),
				SessionID:    strings.TrimSpace(spec.SessionID),
				State:        state,
				Capabilities: append([]string(nil), spec.Capabilities...),
			}
			id, err := b.TeamStore.UpsertTeammate(ctx, teammate)
			if err != nil {
				return nil, nil, err
			}
			teammateIDs = append(teammateIDs, id)
		}

		taskIDs := make([]string, 0, len(request.Tasks))
		taskIndex := make(map[string]string, len(request.Tasks))
		resolvedTaskSpecs := make([]SpawnTaskSpec, len(request.Tasks))
		copy(resolvedTaskSpecs, request.Tasks)
		resolvedTaskIDs := make(map[string]string, len(resolvedTaskSpecs))
		for index := range resolvedTaskSpecs {
			specID := strings.TrimSpace(resolvedTaskSpecs[index].ID)
			if specID == "" {
				continue
			}
			if existingTask, err := b.TeamStore.GetTask(ctx, specID); err == nil && existingTask != nil {
				if strings.TrimSpace(existingTask.TeamID) == strings.TrimSpace(teamID) && (existingTask.Status == team.TaskStatusPending || existingTask.Status == team.TaskStatusReady || existingTask.Status == team.TaskStatusRunning || existingTask.Status == team.TaskStatusBlocked) {
					resolvedTaskIDs[specID] = specID
					continue
				}
				resolvedTaskSpecs[index].ID = b.nextAvailableTaskID(ctx, specID)
			} else if err != nil {
				return nil, nil, err
			}
			resolvedTaskIDs[specID] = strings.TrimSpace(resolvedTaskSpecs[index].ID)
		}
		for _, spec := range resolvedTaskSpecs {
			task := team.Task{
				ID:           strings.TrimSpace(spec.ID),
				TeamID:       teamID,
				Title:        strings.TrimSpace(spec.Title),
				Goal:         strings.TrimSpace(spec.Goal),
				Inputs:       append([]string(nil), spec.Inputs...),
				ReadPaths:    append([]string(nil), spec.ReadPaths...),
				WritePaths:   append([]string(nil), spec.WritePaths...),
				Deliverables: append([]string(nil), spec.Deliverables...),
				Priority:     spec.Priority,
			}
			if assignee := strings.TrimSpace(spec.Assignee); assignee != "" {
				task.Assignee = &assignee
			}
			if existingTask, err := b.TeamStore.GetTask(ctx, task.ID); err == nil && existingTask != nil && strings.TrimSpace(existingTask.TeamID) == strings.TrimSpace(teamID) {
				taskIDs = append(taskIDs, existingTask.ID)
				if strings.TrimSpace(spec.ID) != "" {
					taskIndex[strings.TrimSpace(spec.ID)] = existingTask.ID
				}
				continue
			} else if err != nil {
				return nil, nil, err
			}
			createdID, err := b.TeamStore.CreateTask(ctx, task)
			if err != nil {
				return nil, nil, err
			}
			taskIDs = append(taskIDs, createdID)
			if strings.TrimSpace(spec.ID) != "" {
				taskIndex[strings.TrimSpace(spec.ID)] = createdID
			}
		}

		for _, spec := range resolvedTaskSpecs {
			if len(spec.DependsOn) == 0 {
				continue
			}
			if strings.TrimSpace(spec.ID) == "" {
				return nil, nil, fmt.Errorf("task depends_on requires explicit task id")
			}
			taskID := taskIndex[strings.TrimSpace(spec.ID)]
			if taskID == "" {
				return nil, nil, fmt.Errorf("task id %s was not created", strings.TrimSpace(spec.ID))
			}
			for _, dep := range spec.DependsOn {
				dep = strings.TrimSpace(dep)
				if dep == "" {
					continue
				}
				if mapped, ok := resolvedTaskIDs[dep]; ok && mapped != "" {
					dep = mapped
				}
				depID := taskIndex[dep]
				if depID == "" {
					return nil, nil, fmt.Errorf("dependency %s not found", dep)
				}
				if err := b.TeamStore.AddTaskDependency(ctx, taskID, depID); err != nil {
					return nil, nil, err
				}
			}
		}

		autoStarted := false
		if autoStart {
			b.notifyTeamLifecycleChanged()
			autoStarted = true
		}
		rawMeta := map[string]interface{}{
			"team_id":      teamID,
			"created_team": createdTeam,
			"auto_started": autoStarted,
		}
		if len(taskIDs) == 1 {
			rawMeta["task_id"] = taskIDs[0]
		}
		return SpawnTeamResult{
			TeamID:        teamID,
			CreatedTeam:   createdTeam,
			AutoStarted:   autoStarted,
			TeammateIDs:   teammateIDs,
			TaskIDs:       taskIDs,
			TeammateCount: len(teammateIDs),
			TaskCount:     len(taskIDs),
		}, rawMeta, nil

	case ToolSendTeamMessage:
		if b.TeamStore == nil {
			return nil, nil, fmt.Errorf("team store is not configured")
		}
		request := SendTeamMessageArgs{}
		if value, ok := args["team_id"].(string); ok {
			request.TeamID = strings.TrimSpace(value)
		}
		if value, ok := args["to_agent"].(string); ok {
			request.ToAgent = strings.TrimSpace(value)
		}
		if value, ok := args["kind"].(string); ok {
			request.Kind = strings.TrimSpace(value)
		}
		if value, ok := args["body"].(string); ok {
			request.Body = strings.TrimSpace(value)
		}
		if value, ok := args["task_id"].(string); ok {
			request.TaskID = strings.TrimSpace(value)
		}
		if value, ok := args["metadata"].(map[string]interface{}); ok {
			request.Metadata = value
		}
		if request.Body == "" {
			return nil, nil, fmt.Errorf("body is required")
		}
		teamID, agentID, currentTaskID, err := b.resolveTeamScope(ctx, sessionID, request.TeamID)
		if err != nil {
			return nil, nil, err
		}
		taskID := firstNonEmptyString(request.TaskID, currentTaskID)
		message := team.MailMessage{
			TeamID:    teamID,
			FromAgent: agentID,
			ToAgent:   firstNonEmptyString(request.ToAgent, "*"),
			Kind:      firstNonEmptyString(request.Kind, "info"),
			Body:      request.Body,
			Metadata:  request.Metadata,
		}
		if taskID != "" {
			message.TaskID = &taskID
		}
		messageID, err := team.NewMailboxService(b.TeamStore).Send(ctx, message)
		if err != nil {
			return nil, nil, err
		}
		message.ID = messageID
		if b.TeamDispatcher != nil {
			if dispatchErr := b.TeamDispatcher.DispatchTeamMailboxMessage(ctx, message); dispatchErr != nil {
				rawMeta := map[string]interface{}{
					"team_id":        teamID,
					"from_agent":     agentID,
					"to_agent":       message.ToAgent,
					"dispatch_error": dispatchErr.Error(),
				}
				return SendTeamMessageResult{
					MessageID: messageID,
					TeamID:    teamID,
					FromAgent: agentID,
					ToAgent:   message.ToAgent,
					Kind:      message.Kind,
					TaskID:    taskID,
				}, rawMeta, nil
			}
		}
		return SendTeamMessageResult{
				MessageID: messageID,
				TeamID:    teamID,
				FromAgent: agentID,
				ToAgent:   message.ToAgent,
				Kind:      message.Kind,
				TaskID:    taskID,
			}, map[string]interface{}{
				"team_id":    teamID,
				"from_agent": agentID,
				"to_agent":   message.ToAgent,
			}, nil

	case ToolReadMailboxDigest:
		if b.TeamStore == nil {
			return nil, nil, fmt.Errorf("team store is not configured")
		}
		request := ReadMailboxDigestArgs{}
		if value, ok := args["team_id"].(string); ok {
			request.TeamID = strings.TrimSpace(value)
		}
		if value, ok := args["agent_id"].(string); ok {
			request.AgentID = strings.TrimSpace(value)
		}
		if value, ok := args["limit"].(float64); ok {
			request.Limit = int(value)
		} else if value, ok := args["limit"].(int); ok {
			request.Limit = value
		}
		if value, ok := args["mark_read"].(bool); ok {
			request.MarkRead = &value
		}
		teamID, defaultAgentID, _, err := b.resolveTeamScope(ctx, sessionID, request.TeamID)
		if err != nil {
			return nil, nil, err
		}
		agentID := firstNonEmptyString(request.AgentID, defaultAgentID)
		if agentID == "" {
			return nil, nil, fmt.Errorf("agent id is required")
		}
		markedRead := true
		if request.MarkRead != nil {
			markedRead = *request.MarkRead
		}
		digestResult, err := team.NewMailboxService(b.TeamStore).ReadDigest(ctx, teamID, agentID, request.Limit, markedRead)
		if err != nil {
			return nil, nil, err
		}
		if digestResult == nil {
			digestResult = &team.MailboxDigest{}
		}
		return ReadMailboxDigestResult{
				TeamID:       teamID,
				AgentID:      agentID,
				Digest:       digestResult.Digest,
				MessageIDs:   append([]string(nil), digestResult.MessageIDs...),
				MessageCount: digestResult.MessageCount,
				MarkedRead:   digestResult.MarkedRead,
			}, map[string]interface{}{
				"team_id":       teamID,
				"agent_id":      agentID,
				"message_count": digestResult.MessageCount,
				"marked_read":   digestResult.MarkedRead,
			}, nil

	case ToolReadTaskSpec:
		if b.TeamStore == nil {
			return nil, nil, fmt.Errorf("team store is not configured")
		}
		request := ReadTaskSpecArgs{}
		if value, ok := args["team_id"].(string); ok {
			request.TeamID = strings.TrimSpace(value)
		}
		if value, ok := args["task_id"].(string); ok {
			request.TaskID = strings.TrimSpace(value)
		}
		_, _, task, err := b.loadScopedTask(ctx, sessionID, request.TeamID, request.TaskID)
		if err != nil {
			return nil, nil, err
		}
		result := buildTaskSpecResult(task)
		return result, map[string]interface{}{
			"team_id": task.TeamID,
			"task_id": task.ID,
			"status":  string(task.Status),
		}, nil

	case ToolReadTaskContext:
		if b.TeamStore == nil {
			return nil, nil, fmt.Errorf("team store is not configured")
		}
		request := ReadTaskContextArgs{}
		if value, ok := args["team_id"].(string); ok {
			request.TeamID = strings.TrimSpace(value)
		}
		if value, ok := args["task_id"].(string); ok {
			request.TaskID = strings.TrimSpace(value)
		}
		if value, ok := args["include_dependencies"].(bool); ok {
			request.IncludeDependencies = &value
		}
		if value, ok := args["include_mailbox"].(bool); ok {
			request.IncludeMailbox = &value
		}
		if value, ok := args["mailbox_limit"].(float64); ok {
			request.MailboxLimit = int(value)
		} else if value, ok := args["mailbox_limit"].(int); ok {
			request.MailboxLimit = value
		}
		if value, ok := args["mark_read"].(bool); ok {
			request.MarkRead = &value
		}
		if value, ok := args["context_budget"].(float64); ok {
			request.ContextBudget = int(value)
		} else if value, ok := args["context_budget"].(int); ok {
			request.ContextBudget = value
		}
		teamID, agentID, task, err := b.loadScopedTask(ctx, sessionID, request.TeamID, request.TaskID)
		if err != nil {
			return nil, nil, err
		}
		result := ReadTaskContextResult{
			Spec: buildTaskSpecResult(task),
		}

		builder := team.NewContextBuilder(b.TeamStore)
		if digest, digestErr := builder.Build(ctx, teamID, task.ID, request.ContextBudget); digestErr != nil {
			return nil, nil, digestErr
		} else if digest != nil {
			result.TeamContext = strings.TrimSpace(digest.Summary)
		}

		includeDependencies := true
		if request.IncludeDependencies != nil {
			includeDependencies = *request.IncludeDependencies
		}
		if includeDependencies {
			if deps, depsErr := b.TeamStore.ListTaskDependencies(ctx, task.ID); depsErr != nil {
				return nil, nil, depsErr
			} else if len(deps) > 0 {
				result.Dependencies = append([]string(nil), deps...)
			}
			if dependents, dependentsErr := b.TeamStore.ListTaskDependents(ctx, task.ID); dependentsErr != nil {
				return nil, nil, dependentsErr
			} else if len(dependents) > 0 {
				result.Dependents = append([]string(nil), dependents...)
			}
		}

		includeMailbox := true
		if request.IncludeMailbox != nil {
			includeMailbox = *request.IncludeMailbox
		}
		if includeMailbox && agentID != "" {
			markRead := true
			if request.MarkRead != nil {
				markRead = *request.MarkRead
			}
			mailbox := team.NewMailboxService(b.TeamStore)
			digestResult, digestErr := mailbox.ReadDigest(ctx, teamID, agentID, request.MailboxLimit, markRead)
			if digestErr != nil {
				return nil, nil, digestErr
			}
			if digestResult != nil {
				result.MailboxDigest = digestResult.Digest
				result.MessageIDs = append([]string(nil), digestResult.MessageIDs...)
				result.MessageCount = digestResult.MessageCount
				result.MarkedRead = digestResult.MarkedRead
			}
		}

		return result, map[string]interface{}{
			"team_id":             teamID,
			"task_id":             task.ID,
			"message_count":       result.MessageCount,
			"dependency_count":    len(result.Dependencies),
			"dependent_count":     len(result.Dependents),
			"mailbox_marked_read": result.MarkedRead,
		}, nil

	case ToolBlockCurrentTask:
		request := ReportTaskOutcomeArgs{}
		if value, ok := args["team_id"].(string); ok {
			request.TeamID = strings.TrimSpace(value)
		}
		if value, ok := args["task_id"].(string); ok {
			request.TaskID = strings.TrimSpace(value)
		}
		if value, ok := args["task_status"].(string); ok {
			request.TaskStatus = strings.TrimSpace(value)
		}
		if value, ok := args["summary"].(string); ok {
			request.Summary = strings.TrimSpace(value)
		}
		if value, ok := args["blocker"].(string); ok {
			request.Blocker = strings.TrimSpace(value)
		}
		if value, ok := args["handoff_to"].(string); ok {
			request.HandoffTo = strings.TrimSpace(value)
		}
		if value, ok := args["notify_lead"].(bool); ok {
			request.NotifyLead = &value
		}
		if value, ok := args["auto_replan"].(bool); ok {
			request.AutoReplan = &value
		}
		result, meta, err := b.executeReportTaskOutcome(ctx, sessionID, request, team.TaskOutcomeBlocked, false, team.TaskOutcomeBlocked, team.TaskOutcomeHandoff)
		if err != nil {
			return nil, nil, err
		}
		return BlockCurrentTaskResult(result), meta, nil

	case ToolReportTaskOutcome:
		request := ReportTaskOutcomeArgs{}
		if value, ok := args["team_id"].(string); ok {
			request.TeamID = strings.TrimSpace(value)
		}
		if value, ok := args["task_id"].(string); ok {
			request.TaskID = strings.TrimSpace(value)
		}
		if value, ok := args["task_status"].(string); ok {
			request.TaskStatus = strings.TrimSpace(value)
		}
		if value, ok := args["summary"].(string); ok {
			request.Summary = strings.TrimSpace(value)
		}
		if value, ok := args["blocker"].(string); ok {
			request.Blocker = strings.TrimSpace(value)
		}
		if value, ok := args["handoff_to"].(string); ok {
			request.HandoffTo = strings.TrimSpace(value)
		}
		if value, ok := args["result_ref"].(string); ok {
			request.ResultRef = strings.TrimSpace(value)
		}
		if value, ok := args["notify_lead"].(bool); ok {
			request.NotifyLead = &value
		}
		if value, ok := args["auto_replan"].(bool); ok {
			request.AutoReplan = &value
		}
		return b.executeReportTaskOutcome(ctx, sessionID, request, "", true, team.TaskOutcomeDone, team.TaskOutcomeFailed, team.TaskOutcomeBlocked, team.TaskOutcomeHandoff)

	default:
		return nil, nil, fmt.Errorf("unknown broker tool: %s", toolName)
	}
}

func (b *Broker) nextAvailableTeamID(ctx context.Context, base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "team"
	}
	candidate := base
	for index := 2; ; index++ {
		record, err := b.TeamStore.GetTeam(ctx, candidate)
		if err == nil && record == nil {
			return candidate
		}
		candidate = fmt.Sprintf("%s_v%d", base, index)
	}
}

func (b *Broker) nextAvailableTaskID(ctx context.Context, base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "task"
	}
	candidate := base
	for index := 2; ; index++ {
		record, err := b.TeamStore.GetTask(ctx, candidate)
		if err == nil && record == nil {
			return candidate
		}
		candidate = fmt.Sprintf("%s_v%d", base, index)
	}
}

func isCurrentPlaceholder(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), "current")
}

func (b *Broker) workspaceRoot() string {
	if b == nil || b.TeamClaims == nil {
		return ""
	}
	return b.TeamClaims.Root()
}

func (b *Broker) prepareSpawnTaskSpecs(specs []SpawnTaskSpec) ([]SpawnTaskSpec, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	root := strings.TrimSpace(b.workspaceRoot())
	resolved := make([]SpawnTaskSpec, len(specs))
	copy(resolved, specs)
	if root == "" {
		return resolved, nil
	}
	for index := range resolved {
		normalizedReads, err := b.normalizeSpawnPaths(root, resolved[index], resolved[index].ReadPaths, true)
		if err != nil {
			return nil, err
		}
		normalizedWrites, err := b.normalizeSpawnPaths(root, resolved[index], resolved[index].WritePaths, false)
		if err != nil {
			return nil, err
		}
		resolved[index].ReadPaths = normalizedReads
		resolved[index].WritePaths = normalizedWrites
	}
	return resolved, nil
}

func (b *Broker) normalizeSpawnPaths(root string, spec SpawnTaskSpec, paths []string, mustExist bool) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	normalized := make([]string, 0, len(paths))
	for _, rawPath := range paths {
		relativePath, absolutePath, err := resolveSpawnTaskPath(root, rawPath)
		if err != nil {
			return nil, fmt.Errorf("task %s path %q invalid: %w", spawnTaskLabel(spec), rawPath, err)
		}
		if mustExist {
			info, statErr := os.Stat(absolutePath)
			if statErr != nil {
				return nil, fmt.Errorf("task %s read_path %q not found under workspace root %s", spawnTaskLabel(spec), rawPath, root)
			}
			if !info.IsDir() && strings.HasSuffix(strings.TrimSpace(rawPath), string(filepath.Separator)) {
				return nil, fmt.Errorf("task %s read_path %q expected directory under workspace root %s", spawnTaskLabel(spec), rawPath, root)
			}
		}
		normalized = append(normalized, relativePath)
	}
	return normalized, nil
}

func resolveSpawnTaskPath(root string, rawPath string) (string, string, error) {
	root = strings.TrimSpace(root)
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return "", "", fmt.Errorf("path is empty")
	}
	if root == "" {
		return filepath.ToSlash(filepath.Clean(rawPath)), filepath.Clean(rawPath), nil
	}

	absolutePath := rawPath
	if !filepath.IsAbs(absolutePath) {
		absolutePath = filepath.Join(root, rawPath)
	}
	absolutePath = filepath.Clean(absolutePath)
	root = filepath.Clean(root)

	relativePath, err := filepath.Rel(root, absolutePath)
	if err != nil {
		return "", "", fmt.Errorf("resolve relative path: %w", err)
	}
	if relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("path escapes workspace root")
	}
	return filepath.ToSlash(filepath.Clean(relativePath)), absolutePath, nil
}

func spawnTaskLabel(spec SpawnTaskSpec) string {
	if title := strings.TrimSpace(spec.Title); title != "" {
		return title
	}
	if id := strings.TrimSpace(spec.ID); id != "" {
		return id
	}
	if goal := strings.TrimSpace(spec.Goal); goal != "" {
		return goal
	}
	return "unnamed task"
}

func normalizeToolName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "-", "_")
	switch name {
	case "askuserquestion":
		return ToolAskUserQuestion
	case "backgroundtask":
		return ToolBackgroundTask
	case "taskoutput":
		return ToolTaskOutput
	case "spawnagent":
		return ToolSpawnAgent
	case "sendinput":
		return ToolSendInput
	case "waitagent":
		return ToolWaitAgent
	case "readagentevents":
		return ToolReadAgentEvents
	case "closeagent":
		return ToolCloseAgent
	case "resumeagent":
		return ToolResumeAgent
	case "spawnteam":
		return ToolSpawnTeam
	case "sendteammessage":
		return ToolSendTeamMessage
	case "readmailboxdigest":
		return ToolReadMailboxDigest
	case "readtaskspec":
		return ToolReadTaskSpec
	case "readtaskcontext":
		return ToolReadTaskContext
	case "reporttaskoutcome":
		return ToolReportTaskOutcome
	case "blockcurrenttask":
		return ToolBlockCurrentTask
	default:
		return name
	}
}

func waitResultStatus(result *AgentWaitResult) string {
	if result == nil {
		return "missing"
	}
	if result.Agent != nil && strings.TrimSpace(result.Agent.Status) != "" {
		return strings.TrimSpace(result.Agent.Status)
	}
	if result.TimedOut {
		return "timeout"
	}
	return "missing"
}

func parseTeamStatus(raw string) (team.TeamStatus, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return team.TeamStatusActive, nil
	case string(team.TeamStatusActive):
		return team.TeamStatusActive, nil
	case string(team.TeamStatusPaused):
		return team.TeamStatusPaused, nil
	case string(team.TeamStatusDone):
		return team.TeamStatusDone, nil
	case string(team.TeamStatusFailed):
		return team.TeamStatusFailed, nil
	default:
		return "", fmt.Errorf("invalid team status: %s", raw)
	}
}

func parseTeammateState(raw string) (team.TeammateState, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return team.TeammateStateIdle, nil
	case string(team.TeammateStateIdle):
		return team.TeammateStateIdle, nil
	case string(team.TeammateStateBusy):
		return team.TeammateStateBusy, nil
	case string(team.TeammateStateBlocked):
		return team.TeammateStateBlocked, nil
	case string(team.TeammateStateOffline):
		return team.TeammateStateOffline, nil
	default:
		return "", fmt.Errorf("invalid teammate state: %s", raw)
	}
}

func coerceStringSlice(value interface{}) []string {
	switch typed := value.(type) {
	case []string:
		clone := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				clone = append(clone, trimmed)
			}
		}
		return clone
	case []interface{}:
		clone := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				if trimmed := strings.TrimSpace(text); trimmed != "" {
					clone = append(clone, trimmed)
				}
			}
		}
		return clone
	case string:
		if trimmed := strings.TrimSpace(typed); trimmed != "" {
			return []string{trimmed}
		}
	}
	return nil
}

func (b *Broker) resolveTeamScope(ctx context.Context, sessionID, explicitTeamID string) (teamID string, agentID string, taskID string, err error) {
	runMeta, ok := team.GetRunMeta(ctx)
	if !ok || runMeta == nil || runMeta.Team == nil || strings.TrimSpace(runMeta.Team.TeamID) == "" {
		return "", "", "", fmt.Errorf("team tools require an active team run")
	}
	teamID = strings.TrimSpace(explicitTeamID)
	runTeamID := strings.TrimSpace(runMeta.Team.TeamID)
	if teamID != "" && runTeamID != "" && teamID != runTeamID {
		if b != nil && b.TeamStore != nil {
			existing, storeErr := b.TeamStore.GetTeam(ctx, teamID)
			if storeErr != nil {
				return "", "", "", storeErr
			}
			if existing == nil {
				teamID = runTeamID
			} else {
				return "", "", "", fmt.Errorf("team_id does not match current run")
			}
		} else {
			return "", "", "", fmt.Errorf("team_id does not match current run")
		}
	}
	if teamID == "" {
		teamID = runTeamID
	}
	agentID = strings.TrimSpace(runMeta.Team.AgentID)
	taskID = strings.TrimSpace(runMeta.Team.CurrentTaskID)
	if agentID == "" {
		agentID = "lead"
	}
	return teamID, agentID, taskID, nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func buildTaskSpecResult(task *team.Task) ReadTaskSpecResult {
	if task == nil {
		return ReadTaskSpecResult{}
	}
	result := ReadTaskSpecResult{
		TaskID:       task.ID,
		TeamID:       task.TeamID,
		Title:        task.Title,
		Goal:         task.Goal,
		Inputs:       append([]string(nil), task.Inputs...),
		Status:       string(task.Status),
		Priority:     task.Priority,
		ReadPaths:    append([]string(nil), task.ReadPaths...),
		WritePaths:   append([]string(nil), task.WritePaths...),
		Deliverables: append([]string(nil), task.Deliverables...),
		Summary:      task.Summary,
	}
	if task.Assignee != nil {
		result.Assignee = strings.TrimSpace(*task.Assignee)
	}
	if task.ResultRef != nil {
		result.ResultRef = strings.TrimSpace(*task.ResultRef)
	}
	return result
}

func (b *Broker) loadScopedTask(ctx context.Context, sessionID, explicitTeamID, explicitTaskID string) (string, string, *team.Task, error) {
	if b == nil || b.TeamStore == nil {
		return "", "", nil, fmt.Errorf("team store is not configured")
	}
	teamID, agentID, currentTaskID, err := b.resolveTeamScope(ctx, sessionID, explicitTeamID)
	if err != nil {
		return "", "", nil, err
	}
	taskID := firstNonEmptyString(explicitTaskID, currentTaskID)
	if taskID == "" {
		return "", "", nil, fmt.Errorf("task_id is required")
	}
	task, err := b.TeamStore.GetTask(ctx, taskID)
	if err != nil {
		return "", "", nil, err
	}
	if task == nil {
		return "", "", nil, fmt.Errorf("task not found: %s", taskID)
	}
	if teamID != "" && strings.TrimSpace(task.TeamID) != "" && strings.TrimSpace(task.TeamID) != teamID {
		return "", "", nil, fmt.Errorf("task does not belong to team: %s", teamID)
	}
	return teamID, agentID, task, nil
}

func (b *Broker) executeReportTaskOutcome(ctx context.Context, sessionID string, request ReportTaskOutcomeArgs, defaultStatus team.TaskOutcomeStatus, requireStructured bool, allowed ...team.TaskOutcomeStatus) (ReportTaskOutcomeResult, map[string]interface{}, error) {
	if b == nil || b.TeamStore == nil {
		return ReportTaskOutcomeResult{}, nil, fmt.Errorf("team store is not configured")
	}
	teamID, agentID, task, err := b.loadScopedTask(ctx, sessionID, request.TeamID, request.TaskID)
	if err != nil {
		return ReportTaskOutcomeResult{}, nil, err
	}
	outcome, structured, err := team.NormalizeTaskOutcomeContract(defaultStatus, team.TaskOutcomeContract{
		Status:    team.TaskOutcomeStatus(request.TaskStatus),
		Summary:   request.Summary,
		Blocker:   request.Blocker,
		HandoffTo: request.HandoffTo,
	})
	if err != nil {
		return ReportTaskOutcomeResult{}, nil, err
	}
	if requireStructured && !structured {
		return ReportTaskOutcomeResult{}, nil, fmt.Errorf("task_status is required")
	}
	if err := team.ValidateAllowedTaskOutcomeStatus(outcome, allowed...); err != nil {
		return ReportTaskOutcomeResult{}, nil, err
	}

	switch outcome.Status {
	case team.TaskOutcomeDone, team.TaskOutcomeFailed:
		applyOutcome := outcome
		if !structured {
			applyOutcome.Status = ""
		}
		var resultRef *string
		if strings.TrimSpace(request.ResultRef) != "" {
			value := strings.TrimSpace(request.ResultRef)
			resultRef = &value
		}
		result, err := team.ApplyTerminalTaskOutcome(ctx, team.TaskOutcomeApplyServices{
			Store:  b.TeamStore,
			Claims: b.TeamClaims,
		}, team.TerminalTaskOutcomeRequest{
			Task:            *task,
			TeammateID:      agentID,
			Outcome:         applyOutcome,
			ResultRef:       resultRef,
			DefaultStatus:   outcome.Status,
			SkipStateUpdate: true,
		})
		if err != nil {
			return ReportTaskOutcomeResult{}, nil, err
		}
		if b.TeamLifecycleChanged != nil {
			b.notifyTeamLifecycleChanged()
		} else if _, err := team.ReconcileTerminalTeamState(ctx, team.TerminalTeamServices{
			Store:   b.TeamStore,
			Planner: b.TeamPlanner,
			Mailbox: team.NewMailboxService(b.TeamStore),
		}, teamID); err != nil {
			if !team.IsSQLiteLockError(err) {
				return ReportTaskOutcomeResult{}, nil, err
			}
		}
		payload := map[string]interface{}{
			"team_id":    teamID,
			"task_id":    task.ID,
			"status":     string(result.Status),
			"outcome":    string(result.Outcome.Status),
			"blocked_by": agentID,
		}
		if blocker := strings.TrimSpace(result.Outcome.Blocker); blocker != "" {
			payload["blocker"] = blocker
		}
		if result.ResultRef != nil {
			payload["result_ref"] = *result.ResultRef
		}
		return ReportTaskOutcomeResult{
			TaskID:    task.ID,
			TeamID:    teamID,
			Status:    string(result.Status),
			Outcome:   string(result.Outcome.Status),
			Summary:   result.Summary,
			Blocker:   strings.TrimSpace(result.Outcome.Blocker),
			ResultRef: firstNonEmptyString(payloadString(payload, "result_ref")),
			BlockedBy: agentID,
		}, payload, nil

	case team.TaskOutcomeBlocked, team.TaskOutcomeHandoff:
		applyOutcome := outcome
		if !structured {
			applyOutcome.Status = ""
		}
		teamRecord, loadErr := b.TeamStore.GetTeam(ctx, teamID)
		if loadErr != nil {
			return ReportTaskOutcomeResult{}, nil, loadErr
		}
		if teamRecord == nil {
			teamRecord = &team.Team{ID: teamID}
		}
		result, err := team.ApplyBlockedTaskOutcome(ctx, team.TaskOutcomeApplyServices{
			Store:   b.TeamStore,
			Claims:  b.TeamClaims,
			Mailbox: team.NewMailboxService(b.TeamStore),
			Planner: b.TeamPlanner,
		}, team.BlockedTaskOutcomeRequest{
			Team:            *teamRecord,
			Task:            *task,
			TeammateID:      agentID,
			Outcome:         applyOutcome,
			NotifyRecipient: request.NotifyLead,
			AutoReplan:      request.AutoReplan,
			SkipStateUpdate: true,
		})
		if err != nil {
			return ReportTaskOutcomeResult{}, nil, err
		}
		var (
			messageID       string
			plannedTaskIDs  []string
			dependencyCount int
			replanError     string
		)
		if result.Message != nil {
			messageID = result.Message.ID
			if b.TeamDispatcher != nil {
				if dispatchErr := b.TeamDispatcher.DispatchTeamMailboxMessage(ctx, *result.Message); dispatchErr != nil {
					replanError = firstNonEmptyString(replanError, dispatchErr.Error())
				}
			}
		}
		if result.ReplanError != "" {
			replanError = firstNonEmptyString(replanError, result.ReplanError)
		}
		if result.PlanResult != nil {
			for _, planned := range result.PlanResult.Tasks {
				if strings.TrimSpace(planned.ID) == "" {
					continue
				}
				plannedTaskIDs = append(plannedTaskIDs, planned.ID)
			}
			dependencyCount = len(result.PlanResult.Dependencies)
		}
		payload := map[string]interface{}{
			"team_id":      teamID,
			"task_id":      task.ID,
			"status":       string(team.TaskStatusBlocked),
			"outcome":      string(result.Outcome.Status),
			"blocker":      strings.TrimSpace(result.Outcome.Blocker),
			"blocked_by":   agentID,
			"message_id":   messageID,
			"handoff_to":   result.HandoffTo,
			"replanned":    len(plannedTaskIDs) > 0,
			"replan_error": replanError,
		}
		return ReportTaskOutcomeResult{
			TaskID:          task.ID,
			TeamID:          teamID,
			Status:          string(team.TaskStatusBlocked),
			Outcome:         string(result.Outcome.Status),
			Summary:         result.Summary,
			Blocker:         strings.TrimSpace(result.Outcome.Blocker),
			BlockedBy:       agentID,
			HandoffTo:       result.HandoffTo,
			MessageID:       messageID,
			Replanned:       len(plannedTaskIDs) > 0,
			PlannedTaskIDs:  plannedTaskIDs,
			DependencyCount: dependencyCount,
			ReplanError:     replanError,
		}, payload, nil
	default:
		return ReportTaskOutcomeResult{}, nil, fmt.Errorf("unsupported task outcome: %s", outcome.Status)
	}
}

func (b *Broker) notifyTeamLifecycleChanged() {
	if b == nil || b.TeamLifecycleChanged == nil {
		return
	}
	b.TeamLifecycleChanged()
}

func payloadString(payload map[string]interface{}, key string) string {
	if payload == nil {
		return ""
	}
	value, ok := payload[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func stringValue(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return fmt.Sprintf("%v", value)
	}
}
