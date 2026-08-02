package toolbroker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentdef"
	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/isolation/worktree"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelrouting"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolargs"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	ToolAskUserQuestion      = "ask_user_question"
	ToolEnterPlanMode        = "enter_plan_mode"
	ToolExitPlanMode         = "exit_plan_mode"
	ToolBackgroundTask       = "background_task"
	ToolTaskOutput           = "task_output"
	ToolSpawnAgent           = "spawn_agent"
	ToolListAgents           = "list_agents"
	ToolSendMessage          = "send_message"
	ToolFollowupTask         = "followup_task"
	ToolSendInput            = "send_input"
	ToolResolveAgentApproval = "resolve_agent_approval"
	ToolWaitAgent            = "wait_agent"
	ToolReadAgentEvents      = "read_agent_events"
	ToolCloseAgent           = "close_agent"
	ToolResumeAgent          = "resume_agent"
	ToolApplyAgentWorktree   = "apply_agent_worktree"
	ToolDiscardAgentWorktree = "discard_agent_worktree"
	ToolSpawnTeam            = "spawn_team"
	ToolWaitTeam             = "wait_team"
	ToolSendTeamMessage      = "send_team_message"
	ToolReadMailboxDigest    = "read_mailbox_digest"
	ToolReadTaskSpec         = "read_task_spec"
	ToolReadTaskContext      = "read_task_context"
	ToolReportTaskOutcome    = "report_task_outcome"
	ToolBlockCurrentTask     = "block_current_task"
)

// Broker provides synthetic tools backed by runtime services.
type Broker struct {
	UserInput            UserInputHandler
	PlanMode             PlanModeController
	Background           *background.Manager
	AgentSessions        AgentSessionController
	SessionContextStore  SessionContextStore
	TeamStore            team.Store
	TeamClaims           *team.PathClaimManager
	TeamPlanner          *team.LeadPlanner
	TeamDispatcher       TeamMailboxDispatcher
	TeamEvents           *team.TeamEventBus
	TeamLifecycleChanged func()
	// ExecutionSupervisor registers durable execution runs for spawned child
	// sessions (P3). Optional: nil keeps spawn behavior unchanged.
	ExecutionSupervisor *supervision.ExecutionSupervisor
}

func withBrokerSourceMetadata(metadata map[string]interface{}) map[string]interface{} {
	return toolresult.WithSource(metadata, toolresult.SourceBroker)
}

func withVolatileEmptyReplayMetadata(metadata map[string]interface{}) map[string]interface{} {
	if metadata == nil {
		metadata = map[string]interface{}{}
	} else {
		cloned := make(map[string]interface{}, len(metadata)+1)
		for key, value := range metadata {
			cloned[key] = value
		}
		metadata = cloned
	}
	// Polling / state tools can be empty now and non-empty later under the same
	// arguments. Opt them out of the run-scoped empty negative cache.
	metadata[types.ToolMetadataEmptyReplayCacheKey] = false
	return metadata
}

func withBrokerSourceDefinitions(definitions []types.ToolDefinition) []types.ToolDefinition {
	if len(definitions) == 0 {
		return nil
	}
	out := make([]types.ToolDefinition, len(definitions))
	copy(out, definitions)
	for index := range out {
		out[index].Metadata = withBrokerSourceMetadata(out[index].Metadata)
		if isVolatileEmptyReplayTool(out[index].Name) {
			out[index].Metadata = withVolatileEmptyReplayMetadata(out[index].Metadata)
		}
	}
	return out
}

func isVolatileEmptyReplayTool(name string) bool {
	switch normalizeToolName(name) {
	case ToolTaskOutput, ToolListAgents, ToolWaitAgent, ToolReadAgentEvents, ToolWaitTeam, ToolReadMailboxDigest, ToolReadTaskSpec, ToolReadTaskContext:
		return true
	default:
		return false
	}
}

// IsBrokerTool returns true if the tool is handled by the broker.
func (b *Broker) IsBrokerTool(name string) bool {
	switch normalizeToolName(name) {
	case ToolAskUserQuestion, ToolEnterPlanMode, ToolExitPlanMode, ToolBackgroundTask, ToolTaskOutput, ToolSpawnAgent, ToolListAgents, ToolSendMessage, ToolFollowupTask, ToolSendInput, ToolResolveAgentApproval, ToolWaitAgent, ToolReadAgentEvents, ToolCloseAgent, ToolResumeAgent, ToolApplyAgentWorktree, ToolDiscardAgentWorktree, ToolSpawnTeam, ToolWaitTeam, ToolSendTeamMessage, ToolReadMailboxDigest, ToolReadTaskSpec, ToolReadTaskContext, ToolReportTaskOutcome, ToolBlockCurrentTask:
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
	}
	if b != nil && b.PlanMode != nil {
		definitions = append(definitions,
			types.ToolDefinition{
				Name:        ToolEnterPlanMode,
				Description: "Enter plan mode for the current session. Restricts writes to the plan artifact (default plan.md) until exit_plan_mode. Prefer this before large implementation work when a plan should be reviewed first. Nested re-enter keeps the original previous permission mode.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"plan_path": map[string]interface{}{
							"type":        "string",
							"description": "Optional plan file path to allow writes (default plan.md).",
						},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolExitPlanMode,
				Description: "Exit plan mode with an explicit decision. decision=approve restores the previous permission mode to execute; request_changes stays in plan mode for revisions; quit restores previous mode without executing. Completion does not auto-exit plan mode.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"decision": map[string]interface{}{
							"type":        "string",
							"enum":        []string{"approve", "request_changes", "quit"},
							"description": "Required exit decision: approve | request_changes | quit.",
						},
						"notes": map[string]interface{}{
							"type":        "string",
							"description": "Optional notes recorded with the exit decision.",
						},
					},
					"required": []string{"decision"},
				},
			},
		)
	}
	definitions = append(definitions,
		types.ToolDefinition{
			Name:        ToolBackgroundTask,
			Description: "Run a long-running task in the background and return a job id. Pass that exact job_id to task_output; never guess or synthesize an id. Commands execute through the detected user shell; prefer the cwd parameter for directory changes instead of embedding cd in the command. Use restart_policy=rerun only when automatic infrastructure-failure recovery is safe for the command. A finished process with a non-zero exit code completes the job (status completed + exit_code); only launch/timeout/cancel/healthcheck failures hard-fail.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": map[string]interface{}{
						"type":        "string",
						"description": "Shell command to execute through the detected user shell. Prefer the cwd parameter for directory changes; use pwd/Get-Location on PowerShell/pwsh or cd/echo %cd% on cmd only when printing the current directory.",
					},
					"cwd": map[string]interface{}{
						"type":        "string",
						"description": "Working directory.",
					},
					"timeout_sec": map[string]interface{}{
						"type":        "integer",
						"description": "Optional command timeout in seconds. Omit or use zero for no execution deadline.",
					},
					"priority": map[string]interface{}{
						"type":        "integer",
						"description": "Queue priority.",
					},
					"restart_policy": map[string]interface{}{
						"type":        "string",
						"description": "Infrastructure-failure recovery policy: fail (default) or rerun. Rerun automatically requeues after heartbeat stalls, zombie detection, process loss, PID reuse, or repeated launch failure, and may repeat command side effects.",
					},
					"startup_acceptance": map[string]interface{}{
						"type":        "object",
						"description": "Optional generic startup acceptance gate. Defaults to a process-liveness check after a short grace period. TCP and HTTP probes require caller-supplied targets; the runtime does not infer application endpoints.",
						"properties": map[string]interface{}{
							"probe": map[string]interface{}{
								"type":        "string",
								"enum":        []string{"none", "process", "tcp", "http"},
								"description": "Acceptance probe type.",
							},
							"grace_period_ms": map[string]interface{}{
								"type":        "integer",
								"minimum":     0,
								"description": "Delay before evaluating the probe.",
							},
							"timeout_ms": map[string]interface{}{
								"type":        "integer",
								"minimum":     0,
								"description": "Maximum time allowed for acceptance.",
							},
							"address": map[string]interface{}{
								"type":        "string",
								"description": "TCP host:port supplied by the caller.",
							},
							"url": map[string]interface{}{
								"type":        "string",
								"description": "Absolute HTTP or HTTPS URL supplied by the caller.",
							},
						},
					},
				},
				"required": []string{"command"},
			},
		},
		types.ToolDefinition{
			Name:        ToolTaskOutput,
			Description: "Read background task output, process/heartbeat health, quiet duration, and automatic recovery state by offset. Inspect status and exit_code in the structured result; non-zero exit with status completed is a content result, not a tool crash. error_code is set only for hard failures.",
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
	)
	if b == nil || b.TeamStore == nil {
		if b == nil || b.AgentSessions == nil {
			return withBrokerSourceDefinitions(definitions)
		}
		return withBrokerSourceDefinitions(append(definitions,
			types.ToolDefinition{
				Name:        ToolSpawnAgent,
				Description: "Create a child session for bounded, independent work that can run in parallel and whose result is needed. Avoid delegation when the parent can finish the same work with fewer calls. Depth limit errors are not retryable: complete locally, reuse an existing child, or use spawn_team. This is separate from spawn_team teammates.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":                     map[string]interface{}{"type": "string", "description": "Optional explicit child session id."},
						"session_id":             map[string]interface{}{"type": "string", "description": "Alias for id."},
						"message":                map[string]interface{}{"type": "string", "description": "Initial prompt for the child agent."},
						"agent_type":             map[string]interface{}{"type": "string", "description": "Optional role hint for the child agent."},
						"difficulty":             map[string]interface{}{"type": "string", "enum": []string{"easy", "normal", "hard", "expert"}, "description": "Optional task difficulty hint for local child routing."},
						"difficulty_rationale":   map[string]interface{}{"type": "string", "description": "Optional short rationale for the selected task difficulty."},
						"provider":               map[string]interface{}{"type": "string", "description": "Optional provider override hint. Runtime policy may deny or ignore it."},
						"model":                  map[string]interface{}{"type": "string", "description": "Optional model hint stored on the child session."},
						"reasoning_effort":       map[string]interface{}{"type": "string", "description": "Optional reasoning effort hint for the child session."},
						"thinking_effort":        map[string]interface{}{"type": "string", "description": "Compatibility alias for reasoning_effort."},
						"permission_mode":        map[string]interface{}{"type": "string", "enum": []string{"default", "accept_edits", "plan", "bypass_permissions"}, "description": "Optional permission mode for the child agent run. Use bypass_permissions only when the child task is trusted and bounded; otherwise default may wait for approval."},
						"completion_requirement": map[string]interface{}{"type": "string", "enum": []string{"none"}, "description": "Optional child completion contract. Ordinary spawn_agent sessions only support none; use spawn_team for complete_task Team workers."},
						"completionRequirement":  map[string]interface{}{"type": "string", "enum": []string{"none"}, "description": "Compatibility alias for completion_requirement. Ordinary children only support none."},
						"isolation":              map[string]interface{}{"type": "string", "enum": []string{"none", "worktree"}, "description": "Optional workspace isolation for the child. worktree creates a dedicated git worktree under .aicli/agent-worktrees; fails closed with no main-tree fallback."},
						"read_only":              map[string]interface{}{"type": "boolean", "description": "Restrict the child to read-only, non-shell tools. Defaults permission_mode to plan when omitted."},
						"fork_context":           map[string]interface{}{"type": "boolean", "description": "Whether to copy the parent session history into the child session."},
						"fork_turns":             map[string]interface{}{"type": "string", "description": "Optional fork mode: none, all, or a positive integer. Overrides fork_context when provided."},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolListAgents,
				Description: "List lightweight spawn_agent child sessions for the current root session. This is separate from spawn_team task progress.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"parent_session_id": map[string]interface{}{"type": "string", "description": "Optional parent/root session id. Defaults to the current session."},
						"path_prefix":       map[string]interface{}{"type": "string", "description": "Optional agent path prefix filter, for example /root."},
						"include_closed":    map[string]interface{}{"type": "boolean", "description": "Whether to include closed/archived child sessions."},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolSendMessage,
				Description: "Queue a plain message for a spawn_agent child session without interrupting or starting a new turn.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"target":     map[string]interface{}{"type": "string", "description": "Target child agent session id or path such as /root/worker."},
						"id":         map[string]interface{}{"type": "string", "description": "Alias for target."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for target."},
						"message":    map[string]interface{}{"type": "string", "description": "Message to deliver."},
					},
					"required": []string{"message"},
				},
			},
			types.ToolDefinition{
				Name:        ToolFollowupTask,
				Description: "Send a follow-up task to a spawn_agent child session. If the child is busy, the message is delivered without interrupting the active run.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"target":     map[string]interface{}{"type": "string", "description": "Target child agent session id or path such as /root/worker."},
						"id":         map[string]interface{}{"type": "string", "description": "Alias for target."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for target."},
						"message":    map[string]interface{}{"type": "string", "description": "Follow-up task prompt."},
					},
					"required": []string{"message"},
				},
			},
			types.ToolDefinition{
				Name:        ToolSendInput,
				Description: "Send a follow-up prompt to an existing spawn_agent child session. This does not address spawn_team teammates.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":         map[string]interface{}{"type": "string", "description": "Child agent session id or path such as /root/worker."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for id."},
						"message":    map[string]interface{}{"type": "string", "description": "Prompt to send to the child agent."},
						"interrupt":  map[string]interface{}{"type": "boolean", "description": "Whether to interrupt an active child run before submitting the new prompt."},
					},
					"required": []string{"message"},
				},
			},
			types.ToolDefinition{
				Name:        ToolResolveAgentApproval,
				Description: "Approve or deny a pending tool approval request in a spawn_agent child session. Use this when wait_agent/read_agent_events reports waiting_approval; do not repeatedly wait for the same approval.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":           map[string]interface{}{"type": "string", "description": "Child agent session id or path such as /root/worker."},
						"session_id":   map[string]interface{}{"type": "string", "description": "Alias for id."},
						"request_id":   map[string]interface{}{"type": "string", "description": "Pending approval request id from wait_agent status or approval_requested event."},
						"allow":        map[string]interface{}{"type": "boolean", "description": "true to approve the child tool call, false to deny it."},
						"patched_args": map[string]interface{}{"type": "object", "description": "Optional replacement tool arguments to use when approving."},
					},
					"required": []string{"request_id", "allow"},
				},
			},
			types.ToolDefinition{
				Name:        ToolWaitAgent,
				Description: "Wait for spawn_agent progress or, without ids, for a parent mailbox event. Batch results include every current snapshot and each ready agent's output once. Consume ready outputs directly. If timed_out, follow next_action and do not immediately repeat the same wait while independent work remains. waiting_approval requires approval handling. Do not use this for spawn_team teammate ids.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":          map[string]interface{}{"type": "string", "description": "Child agent session id or path such as /root/worker."},
						"session_id":  map[string]interface{}{"type": "string", "description": "Alias for id."},
						"ids":         map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Child ids or paths. Returns when any becomes ready and reports all current ready/pending ids."},
						"session_ids": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Alias for ids."},
						"after_seq":   map[string]interface{}{"type": "integer", "description": "When no id is provided, wait only for parent mailbox/collab events after this session event sequence."},
						"timeout_ms":  map[string]interface{}{"type": "integer", "description": "Optional wait timeout in milliseconds."},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolReadAgentEvents,
				Description: "Read collaboration events. With id, reads recent runtime events for a spawn_agent child session and optionally waits for new events. If the child is waiting_approval, inspect the returned approval_requested event/status instead of polling repeatedly. When count=0 or timed_out, follow next_action and do not immediately re-call with the same id/after_seq; prefer wait_agent for readiness. Without id, reads the current parent session mailbox/collab events after after_seq. Do not use this for spawn_team teammate ids such as member-1.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":         map[string]interface{}{"type": "string", "description": "Child agent session id or path such as /root/worker. Omit to read parent mailbox/collab events."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for id."},
						"after_seq":  map[string]interface{}{"type": "integer", "description": "Only return events after this sequence number."},
						"limit":      map[string]interface{}{"type": "integer", "description": "Maximum number of events to return."},
						"wait_ms":    map[string]interface{}{"type": "integer", "description": "Optional wait timeout while waiting for new events to arrive."},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolCloseAgent,
				Description: "Stop and close a child agent session or agent path. Closing a parent path also closes its descendant child sessions.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":         map[string]interface{}{"type": "string", "description": "Child agent session id or path such as /root/worker."},
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
						"id":         map[string]interface{}{"type": "string", "description": "Child agent session id or path such as /root/worker."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for id."},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolApplyAgentWorktree,
				Description: "Apply a spawn_agent child's worktree isolation changes into the main repository. Default removes the worktree after apply; set keep=true to preserve it. Call this from the parent after reviewing child output; completion does not auto-apply.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":         map[string]interface{}{"type": "string", "description": "Child agent session id or path such as /root/worker."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for id."},
						"paths":      map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Optional relative paths to apply. Empty applies all tracked changes from the isolation branch."},
						"keep":       map[string]interface{}{"type": "boolean", "description": "When true, keep the worktree after apply (default false removes it)."},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolDiscardAgentWorktree,
				Description: "Discard a spawn_agent child's worktree isolation without applying changes to the main repository. Use when rejecting isolated work; close_agent also cleans up remaining worktrees.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":         map[string]interface{}{"type": "string", "description": "Child agent session id or path such as /root/worker."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for id."},
					},
				},
			},
		))
	}
	if b.AgentSessions != nil {
		definitions = append(definitions,
			types.ToolDefinition{
				Name:        ToolSpawnAgent,
				Description: "Create a child session for bounded, independent work that can run in parallel and whose result is needed. Avoid delegation when the parent can finish the same work with fewer calls. Depth limit errors are not retryable: complete locally, reuse an existing child, or use spawn_team. This is separate from spawn_team teammates.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":                     map[string]interface{}{"type": "string", "description": "Optional explicit child session id."},
						"session_id":             map[string]interface{}{"type": "string", "description": "Alias for id."},
						"message":                map[string]interface{}{"type": "string", "description": "Initial prompt for the child agent."},
						"agent_type":             map[string]interface{}{"type": "string", "description": "Optional role hint for the child agent."},
						"difficulty":             map[string]interface{}{"type": "string", "enum": []string{"easy", "normal", "hard", "expert"}, "description": "Optional task difficulty hint for local child routing."},
						"difficulty_rationale":   map[string]interface{}{"type": "string", "description": "Optional short rationale for the selected task difficulty."},
						"provider":               map[string]interface{}{"type": "string", "description": "Optional provider override hint. Runtime policy may deny or ignore it."},
						"model":                  map[string]interface{}{"type": "string", "description": "Optional model hint stored on the child session."},
						"reasoning_effort":       map[string]interface{}{"type": "string", "description": "Optional reasoning effort hint for the child session."},
						"thinking_effort":        map[string]interface{}{"type": "string", "description": "Compatibility alias for reasoning_effort."},
						"permission_mode":        map[string]interface{}{"type": "string", "enum": []string{"default", "accept_edits", "plan", "bypass_permissions"}, "description": "Optional permission mode for the child agent run. Use bypass_permissions only when the child task is trusted and bounded; otherwise default may wait for approval."},
						"completion_requirement": map[string]interface{}{"type": "string", "enum": []string{"none"}, "description": "Optional child completion contract. Ordinary spawn_agent sessions only support none; use spawn_team for complete_task Team workers."},
						"completionRequirement":  map[string]interface{}{"type": "string", "enum": []string{"none"}, "description": "Compatibility alias for completion_requirement. Ordinary children only support none."},
						"isolation":              map[string]interface{}{"type": "string", "enum": []string{"none", "worktree"}, "description": "Optional workspace isolation for the child. worktree creates a dedicated git worktree under .aicli/agent-worktrees; fails closed with no main-tree fallback."},
						"read_only":              map[string]interface{}{"type": "boolean", "description": "Restrict the child to read-only, non-shell tools. Defaults permission_mode to plan when omitted."},
						"fork_context":           map[string]interface{}{"type": "boolean", "description": "Whether to copy the parent session history into the child session."},
						"fork_turns":             map[string]interface{}{"type": "string", "description": "Optional fork mode: none, all, or a positive integer. Overrides fork_context when provided."},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolListAgents,
				Description: "List lightweight spawn_agent child sessions for the current root session. This is separate from spawn_team task progress.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"parent_session_id": map[string]interface{}{"type": "string", "description": "Optional parent/root session id. Defaults to the current session."},
						"path_prefix":       map[string]interface{}{"type": "string", "description": "Optional agent path prefix filter, for example /root."},
						"include_closed":    map[string]interface{}{"type": "boolean", "description": "Whether to include closed/archived child sessions."},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolSendMessage,
				Description: "Queue a plain message for a spawn_agent child session without interrupting or starting a new turn.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"target":     map[string]interface{}{"type": "string", "description": "Target child agent session id or path such as /root/worker."},
						"id":         map[string]interface{}{"type": "string", "description": "Alias for target."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for target."},
						"message":    map[string]interface{}{"type": "string", "description": "Message to deliver."},
					},
					"required": []string{"message"},
				},
			},
			types.ToolDefinition{
				Name:        ToolFollowupTask,
				Description: "Send a follow-up task to a spawn_agent child session. If the child is busy, the message is delivered without interrupting the active run.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"target":     map[string]interface{}{"type": "string", "description": "Target child agent session id or path such as /root/worker."},
						"id":         map[string]interface{}{"type": "string", "description": "Alias for target."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for target."},
						"message":    map[string]interface{}{"type": "string", "description": "Follow-up task prompt."},
					},
					"required": []string{"message"},
				},
			},
			types.ToolDefinition{
				Name:        ToolSendInput,
				Description: "Send a follow-up prompt to an existing spawn_agent child session. This does not address spawn_team teammates.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":         map[string]interface{}{"type": "string", "description": "Child agent session id or path such as /root/worker."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for id."},
						"message":    map[string]interface{}{"type": "string", "description": "Prompt to send to the child agent."},
						"interrupt":  map[string]interface{}{"type": "boolean", "description": "Whether to interrupt an active child run before submitting the new prompt."},
					},
					"required": []string{"message"},
				},
			},
			types.ToolDefinition{
				Name:        ToolResolveAgentApproval,
				Description: "Approve or deny a pending tool approval request in a spawn_agent child session. Use this when wait_agent/read_agent_events reports waiting_approval; do not repeatedly wait for the same approval.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":           map[string]interface{}{"type": "string", "description": "Child agent session id or path such as /root/worker."},
						"session_id":   map[string]interface{}{"type": "string", "description": "Alias for id."},
						"request_id":   map[string]interface{}{"type": "string", "description": "Pending approval request id from wait_agent status or approval_requested event."},
						"allow":        map[string]interface{}{"type": "boolean", "description": "true to approve the child tool call, false to deny it."},
						"patched_args": map[string]interface{}{"type": "object", "description": "Optional replacement tool arguments to use when approving."},
					},
					"required": []string{"request_id", "allow"},
				},
			},
			types.ToolDefinition{
				Name:        ToolWaitAgent,
				Description: "Wait for spawn_agent children to become idle, blocked, failed, or waiting_approval. Batch results include every current snapshot and each ready output once. Consume ready outputs directly. If timed_out, follow next_action and do not immediately repeat the same wait while independent work remains. Use wait_team for spawn_team teammates.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":          map[string]interface{}{"type": "string", "description": "Child agent session id or path such as /root/worker."},
						"session_id":  map[string]interface{}{"type": "string", "description": "Alias for id."},
						"ids":         map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Child ids or paths. Returns when any becomes ready and reports all current ready/pending ids."},
						"session_ids": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Alias for ids."},
						"timeout_ms":  map[string]interface{}{"type": "integer", "description": "Optional wait timeout in milliseconds."},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolReadAgentEvents,
				Description: "Read collaboration events. With id, reads recent runtime events for a spawn_agent child session and optionally waits for new events. If the child is waiting_approval, inspect the returned approval_requested event/status instead of polling repeatedly. When count=0 or timed_out, follow next_action and do not immediately re-call with the same id/after_seq; prefer wait_agent for readiness. Without id, reads the current parent session mailbox/collab events after after_seq. Do not use this for spawn_team teammate ids such as member-1; call wait_team with the spawn_team team_id for team lifecycle events.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":         map[string]interface{}{"type": "string", "description": "Child agent session id or path such as /root/worker. Omit to read parent mailbox/collab events."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for id."},
						"after_seq":  map[string]interface{}{"type": "integer", "description": "Only return events after this sequence number."},
						"limit":      map[string]interface{}{"type": "integer", "description": "Maximum number of events to return."},
						"wait_ms":    map[string]interface{}{"type": "integer", "description": "Optional wait timeout while waiting for new events to arrive."},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolCloseAgent,
				Description: "Stop and close a child agent session or agent path. Closing a parent path also closes its descendant child sessions.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":         map[string]interface{}{"type": "string", "description": "Child agent session id or path such as /root/worker."},
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
						"id":         map[string]interface{}{"type": "string", "description": "Child agent session id or path such as /root/worker."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for id."},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolApplyAgentWorktree,
				Description: "Apply a spawn_agent child's worktree isolation changes into the main repository. Default removes the worktree after apply; set keep=true to preserve it. Call this from the parent after reviewing child output; completion does not auto-apply.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":         map[string]interface{}{"type": "string", "description": "Child agent session id or path such as /root/worker."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for id."},
						"paths":      map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}, "description": "Optional relative paths to apply. Empty applies all tracked changes from the isolation branch."},
						"keep":       map[string]interface{}{"type": "boolean", "description": "When true, keep the worktree after apply (default false removes it)."},
					},
				},
			},
			types.ToolDefinition{
				Name:        ToolDiscardAgentWorktree,
				Description: "Discard a spawn_agent child's worktree isolation without applying changes to the main repository. Use when rejecting isolated work; close_agent also cleans up remaining worktrees.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"id":         map[string]interface{}{"type": "string", "description": "Child agent session id or path such as /root/worker."},
						"session_id": map[string]interface{}{"type": "string", "description": "Alias for id."},
					},
				},
			},
		)
	}
	if b == nil || b.TeamStore == nil {
		return withBrokerSourceDefinitions(definitions)
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
						"description": "Optional team status: active, paused, done, failed, partially_completed, canceled. Defaults to active.",
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
								"difficulty": map[string]interface{}{
									"type":        "string",
									"enum":        []string{"easy", "normal", "hard", "expert"},
									"description": "Optional local routing/audit metadata. When routing is enabled, difficulty may affect this teammate task's provider/model/reasoning through local policy; task payloads cannot directly set provider, model, permission mode, or tool policy.",
								},
								"difficulty_rationale": map[string]interface{}{
									"type":        "string",
									"description": "Optional short rationale for the selected task difficulty.",
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
			Name:        ToolWaitTeam,
			Description: "Wait for a spawn_team run to reach durable team.completed/team.summary state and return recent persisted team lifecycle events. Use this after spawn_team auto_start=true instead of wait_agent/read_agent_events; those are only for spawn_agent child sessions.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"team_id": map[string]interface{}{
						"type":        "string",
						"description": "Team id returned by spawn_team. If omitted, defaults to the current active team run when one is bound in context.",
					},
					"after_seq": map[string]interface{}{
						"type":        "integer",
						"description": "Only include persisted team events after this team event sequence.",
					},
					"timeout_ms": map[string]interface{}{
						"type":        "integer",
						"description": "Optional wait timeout in milliseconds. Defaults to 30000.",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum recent team events to return. Defaults to 24.",
					},
					"require_summary": map[string]interface{}{
						"type":        "boolean",
						"description": "Whether a done team should wait for team.summary. Defaults to true when a lead planner is configured, otherwise false.",
					},
				},
			},
		},
		types.ToolDefinition{
			Name:        ToolSendTeamMessage,
			Description: describeRequiresActiveTeamRun("Send a direct or broadcast mailbox message within the current team."),
			Metadata:    metadataRequiresActiveTeamRun(nil),
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
			Description: describeRequiresActiveTeamRun("Read unread mailbox context for the current teammate, including broadcast messages."),
			Metadata:    metadataRequiresActiveTeamRun(nil),
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
			Description: describeRequiresActiveTeamRun("Read the current task specification for the team run."),
			Metadata:    metadataRequiresActiveTeamRun(nil),
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
			Description: describeRequiresActiveTeamRun("Read the current task specification plus richer team context for the active task."),
			Metadata:    metadataRequiresActiveTeamRun(nil),
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
			Description: describeRequiresActiveTeamRun("Report a structured done, failed, blocked, or handoff outcome for the current team task."),
			Metadata: metadataRequiresActiveTeamRun(map[string]interface{}{
				"canonical": true,
				"replaces":  []string{ToolBlockCurrentTask},
			}),
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
			Description: describeRequiresActiveTeamRun("Compatibility alias for report_task_outcome when reporting blocked or handoff outcomes."),
			Metadata: metadataRequiresActiveTeamRun(map[string]interface{}{
				"compatibility_alias": true,
				"canonical_tool":      ToolReportTaskOutcome,
			}),
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
	return withBrokerSourceDefinitions(definitions)
}

// Execute runs a broker tool without an originating tool call id.
func (b *Broker) Execute(ctx context.Context, sessionID, toolName string, args map[string]interface{}) (interface{}, map[string]interface{}, error) {
	result, metadata, err := b.execute(ctx, sessionID, toolName, args, "")
	return result, withBrokerSourceMetadata(metadata), classifyBrokerExecutionError(toolName, err)
}

// ExecuteToolCall runs a broker tool for a concrete tool call.
func (b *Broker) ExecuteToolCall(ctx context.Context, sessionID string, call types.ToolCall) (interface{}, map[string]interface{}, error) {
	result, metadata, err := b.execute(ctx, sessionID, call.Name, call.Args, call.ID)
	return result, withBrokerSourceMetadata(metadata), classifyBrokerExecutionError(call.Name, err)
}

func classifyBrokerExecutionError(toolName string, err error) error {
	if err == nil {
		return nil
	}
	var runtimeErr *runtimeerrors.RuntimeError
	if stderrors.As(err, &runtimeErr) {
		return err
	}
	lower := strings.ToLower(strings.TrimSpace(err.Error()))
	code := runtimeerrors.ErrToolBrokerFailure
	switch {
	case strings.Contains(lower, "session already exists"):
		code = runtimeerrors.ErrAgentAlreadyExists
	case strings.Contains(lower, "session is busy"):
		code = runtimeerrors.ErrAgentBusy
	case strings.Contains(lower, "agent session reference not found"),
		strings.Contains(lower, "unknown agent session reference"),
		strings.Contains(lower, "agent session not found"):
		code = runtimeerrors.ErrAgentSessionNotFound
	case strings.Contains(lower, "sqlite3: interrupted"),
		strings.Contains(lower, "database operation interrupted"):
		code = runtimeerrors.ErrStreamInterrupted
	case strings.Contains(lower, "not found"):
		code = runtimeerrors.ErrToolPathNotFound
	case strings.Contains(lower, "required"), strings.Contains(lower, "invalid "), strings.Contains(lower, "unknown broker tool"):
		code = runtimeerrors.ErrToolInvalidArgs
	}
	return runtimeerrors.WrapWithContext(code, "broker tool execution failed", err, map[string]interface{}{
		"tool": strings.TrimSpace(toolName),
	})
}

func brokerIntArg(value interface{}) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func decorateBackgroundTaskResult(job background.Job) BackgroundTaskResult {
	result := BackgroundTaskResult{
		JobID: job.ID, Status: string(job.Status), Message: job.Message, RestartPolicy: job.RestartPolicy,
	}
	if value, ok := job.Metadata["launch_state"].(string); ok {
		result.LaunchState = value
	}
	if value, ok := job.Metadata["healthcheck_state"].(string); ok {
		result.HealthcheckState = value
	}
	if value, ok := job.Metadata["startup_probe"].(string); ok {
		result.StartupProbe = value
	}
	result.StartupGraceMs = int64(brokerIntArg(job.Metadata["startup_grace_ms"]))
	return result
}

// execute runs a broker tool.
func (b *Broker) execute(ctx context.Context, sessionID, toolName string, args map[string]interface{}, toolCallID string) (interface{}, map[string]interface{}, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	args = toolargs.Normalize(args)
	handleAliases, err := b.loadSessionHandleAliases(ctx, sessionID)
	if err != nil {
		return nil, nil, err
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
		questionID := deterministicQuestionID(toolCallID, request)
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
		return AskUserQuestionResult{QuestionID: questionID, Answer: answer}, map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindStructured,
		}, nil

	case ToolEnterPlanMode:
		if b.PlanMode == nil {
			return nil, nil, fmt.Errorf("plan mode controller is not configured")
		}
		req := EnterPlanModeArgs{}
		if value, ok := args["plan_path"].(string); ok {
			req.PlanPath = strings.TrimSpace(value)
		}
		result, err := b.PlanMode.EnterPlanMode(ctx, sessionID, req)
		if err != nil {
			return nil, nil, err
		}
		meta := map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindStructured,
		}
		if result != nil {
			meta["active"] = result.Active
			meta["permission_mode"] = result.PermissionMode
			meta["plan_path"] = result.PlanPath
		}
		return result, meta, nil

	case ToolExitPlanMode:
		if b.PlanMode == nil {
			return nil, nil, fmt.Errorf("plan mode controller is not configured")
		}
		req := ExitPlanModeArgs{}
		if value, ok := args["decision"].(string); ok {
			req.Decision = strings.TrimSpace(value)
		}
		if value, ok := args["notes"].(string); ok {
			req.Notes = strings.TrimSpace(value)
		}
		if req.Decision == "" {
			return nil, nil, fmt.Errorf("decision is required (approve|request_changes|quit)")
		}
		result, err := b.PlanMode.ExitPlanMode(ctx, sessionID, req)
		if err != nil {
			return nil, nil, err
		}
		meta := map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindStructured,
		}
		if result != nil {
			meta["active"] = result.Active
			meta["permission_mode"] = result.PermissionMode
			meta["exit_decision"] = result.ExitDecision
		}
		return result, meta, nil

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
		if value, ok := args["startup_acceptance"].(map[string]interface{}); ok {
			startup := &background.StartupAcceptance{}
			if probe, ok := value["probe"].(string); ok {
				startup.Probe = background.StartupProbeType(strings.TrimSpace(probe))
			}
			startup.GracePeriodMs = brokerIntArg(value["grace_period_ms"])
			startup.TimeoutMs = brokerIntArg(value["timeout_ms"])
			startup.Address, _ = value["address"].(string)
			startup.URL, _ = value["url"].(string)
			req.Startup = startup
		}
		job, err := b.Background.SubmitShell(ctx, sessionID, req)
		if err != nil {
			return nil, nil, err
		}
		jobAlias := strings.TrimSpace(job.ID)
		if handleAliases != nil {
			jobAlias = handleAliases.Jobs.register(job.ID, deterministicHandleAlias(backgroundJobAliasPrefix, toolCallID, ToolBackgroundTask, args))
			if err := b.saveSessionHandleAliases(ctx, sessionID, handleAliases); err != nil {
				return nil, nil, err
			}
		}
		resultMetadata := map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindStructured,
			"job_id":               strings.TrimSpace(job.ID),
			"job_alias":            jobAlias,
			"status":               string(job.Status),
			"restart_policy":       job.RestartPolicy,
		}
		for _, key := range []string{
			"timeout_requested_ms", "timeout_effective_ms", "timeout_ms", "timeout_source",
			"launch_state", "healthcheck_state", "startup_probe", "startup_grace_ms",
		} {
			if value, ok := job.Metadata[key]; ok {
				resultMetadata[key] = value
			}
		}
		outputSummary := decorateBackgroundTaskResult(*job)
		outputSummary.JobID = jobAlias
		return outputSummary, resultMetadata, nil

	case ToolTaskOutput:
		if b.Background == nil {
			return nil, nil, fmt.Errorf("background manager is not configured")
		}
		jobID, _ := args["job_id"].(string)
		jobID = strings.TrimSpace(jobID)
		if jobID == "" {
			return nil, nil, fmt.Errorf("job_id is required")
		}
		resolvedJobID := jobID
		jobAlias := ""
		if handleAliases != nil {
			resolvedJobID, jobAlias, err = handleAliases.Jobs.resolve(jobID, backgroundJobAliasPrefix, "background job")
			if err != nil {
				return nil, nil, err
			}
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
			JobID:  resolvedJobID,
			Offset: offset,
			Limit:  limit,
		})
		if err != nil {
			if runtimeerrors.Is(err, runtimeerrors.ErrJobNotFound) {
				return nil, nil, fmt.Errorf("%w; use the exact job_id returned by background_task instead of guessing an id", err)
			}
			return nil, nil, err
		}
		displayJobID := strings.TrimSpace(output.JobID)
		if handleAliases != nil {
			displayJobID = handleAliases.Jobs.aliasFor(output.JobID)
			if displayJobID == strings.TrimSpace(output.JobID) && jobAlias != "" {
				displayJobID = jobAlias
			}
		}
		outputMetadata := map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindStructured,
			"job_id":               strings.TrimSpace(output.JobID),
			"job_alias":            displayJobID,
			"status":               output.Status,
			"next_offset":          output.NextOffset,
		}
		for key, value := range map[string]interface{}{
			"error_code":           output.ErrorCode,
			"timeout_requested_ms": output.TimeoutRequestedMs,
			"timeout_effective_ms": output.TimeoutEffectiveMs,
			"timeout_source":       output.TimeoutSource,
			"cancel_source":        output.CancelSource,
			"launch_state":         output.LaunchState,
			"process_started":      output.ProcessStarted,
			"process_alive":        output.ProcessAlive,
			"startup_probe":        output.StartupProbe,
			"startup_grace_ms":     output.StartupGraceMs,
			"startup_accepted_at":  output.StartupAcceptedAt,
			"healthcheck_state":    output.HealthcheckState,
			"healthcheck_error":    output.HealthcheckError,
			"queue_position":       int64(output.QueuePosition),
			"active_jobs":          int64(output.ActiveJobs),
			"max_concurrent":       int64(output.MaxConcurrent),
			"scheduler_state":      output.SchedulerState,
			"next_action":          output.NextAction,
		} {
			if textValue, ok := value.(string); ok && strings.TrimSpace(textValue) == "" {
				continue
			}
			if intValue, ok := value.(int64); ok && intValue == 0 {
				continue
			}
			outputMetadata[key] = value
		}
		return TaskOutputResult{
			JobID: displayJobID, Status: output.Status, Output: output.Output,
			NextOffset: output.NextOffset, ExitCode: output.ExitCode, Message: output.Message,
			ErrorCode: output.ErrorCode, TimeoutRequestedMs: output.TimeoutRequestedMs,
			TimeoutEffectiveMs: output.TimeoutEffectiveMs, TimeoutSource: output.TimeoutSource,
			CancelSource: output.CancelSource, WatchdogState: output.WatchdogState,
			WatchdogErrorCode: output.WatchdogErrorCode, LaunchAttempt: output.LaunchAttempt,
			LaunchMaxAttempts: output.LaunchMaxAttempts, ProcessState: output.ProcessState,
			HeartbeatAgeMs: output.HeartbeatAgeMs, LastOutputAt: output.LastOutputAt,
			QuietForMs: output.QuietForMs, RecoveryAttempt: output.RecoveryAttempt,
			RecoveryMaxAttempts: output.RecoveryMaxAttempts, NextRecoveryAt: output.NextRecoveryAt,
			LaunchState: output.LaunchState, ProcessStarted: output.ProcessStarted,
			ProcessAlive: output.ProcessAlive, StartupProbe: output.StartupProbe,
			StartupGraceMs: output.StartupGraceMs, StartupAcceptedAt: output.StartupAcceptedAt,
			HealthcheckState: output.HealthcheckState, HealthcheckError: output.HealthcheckError,
			QueuePosition: output.QueuePosition, ActiveJobs: output.ActiveJobs,
			MaxConcurrent: output.MaxConcurrent, SchedulerState: output.SchedulerState,
			NextAction: output.NextAction,
		}, outputMetadata, nil

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
		if value, ok := args["difficulty"].(string); ok {
			if strings.TrimSpace(value) != "" {
				difficulty, ok := modelrouting.NormalizeDifficulty(value)
				if !ok {
					return nil, nil, fmt.Errorf("invalid agent difficulty: %s", strings.TrimSpace(value))
				}
				request.Difficulty = difficulty
			}
		}
		if value, ok := args["difficulty_rationale"].(string); ok {
			request.DifficultyRationale = strings.TrimSpace(value)
		}
		if value, ok := args["provider"].(string); ok {
			request.Provider = strings.TrimSpace(value)
		}
		if value, ok := args["model"].(string); ok {
			request.Model = strings.TrimSpace(value)
		}
		if value, ok := args["reasoning_effort"].(string); ok {
			request.ReasoningEffort = strings.TrimSpace(value)
		}
		if value, ok := args["thinking_effort"].(string); ok {
			request.ThinkingEffort = strings.TrimSpace(value)
			if request.ReasoningEffort == "" {
				request.ReasoningEffort = request.ThinkingEffort
				if request.ThinkingEffort != "" {
					request.RouteWarnings = append(request.RouteWarnings, "thinking_effort_alias_used")
				}
			}
		}
		permissionModeExplicit := false
		if value, ok := args["permission_mode"].(string); ok {
			permissionMode, err := normalizeSpawnAgentPermissionMode(value)
			if err != nil {
				return nil, nil, err
			}
			request.PermissionMode = permissionMode
			request.RequestedPermissionMode = strings.TrimSpace(value)
			permissionModeExplicit = strings.TrimSpace(value) != ""
		}
		if value, ok := args["completion_requirement"].(string); ok {
			request.CompletionRequirement = strings.TrimSpace(value)
		}
		if request.CompletionRequirement == "" {
			if value, ok := args["completionRequirement"].(string); ok {
				request.CompletionRequirement = strings.TrimSpace(value)
			}
		}
		normalizedCompletionRequirement, err := normalizeSpawnAgentCompletionRequirement(request.CompletionRequirement)
		if err != nil {
			return nil, nil, err
		}
		request.CompletionRequirement = normalizedCompletionRequirement
		if value, ok := args["isolation"].(string); ok {
			isolation, err := worktree.NormalizeMode(value)
			if err != nil {
				return nil, nil, err
			}
			request.Isolation = isolation
		}
		readOnlyExplicit := false
		if value, ok := args["read_only"].(bool); ok {
			request.ReadOnly = value
			readOnlyExplicit = true
		}
		if request.ReadOnly && !permissionModeExplicit {
			request.PermissionMode = string(runtimepolicy.ModePlan)
		}
		if strings.TrimSpace(request.PermissionMode) == "" {
			if runMeta, ok := team.GetRunMeta(ctx); ok && runMeta != nil {
				if permissionMode, err := normalizeSpawnAgentPermissionMode(runMeta.PermissionMode); err == nil {
					request.PermissionMode = permissionMode
				}
			}
		}
		// completion_requirement belongs to the spawned child and must never be
		// copied from the parent run's Team task contract.
		if strings.TrimSpace(request.CompletionRequirement) == "" && strings.TrimSpace(request.AgentType) != "" {
			requirement, err := resolveSpawnAgentCompletionRequirement(request.AgentType)
			if err != nil {
				return nil, nil, err
			}
			if requirement != "" {
				request.CompletionRequirement = requirement
			}
		}
		request.RequestedProvider = strings.TrimSpace(request.Provider)
		request.RequestedModel = strings.TrimSpace(request.Model)
		request.RequestedReasoningEffort = strings.TrimSpace(request.ReasoningEffort)
		request.RequestedRouteCaptured = true
		if strings.TrimSpace(request.AgentType) != "" {
			applySpawnAgentAgentdefDefaults(&request, permissionModeExplicit, readOnlyExplicit)
		}
		request.EffectivePermissionMode = request.PermissionMode
		if value, ok := args["fork_context"].(bool); ok {
			request.ForkContext = &value
		}
		if value, ok := args["fork_turns"].(string); ok {
			request.ForkTurns = strings.TrimSpace(value)
		}
		if value, ok := args["timeout_sec"].(int64); ok {
			request.TimeoutSec = value
		}
		if value, ok := args["progress_timeout_sec"].(int64); ok {
			request.ProgressTimeoutSec = value
		}
		if value, ok := args["approval_timeout_sec"].(int64); ok {
			request.ApprovalTimeoutSec = value
		}
		if value, ok := args["cancel_grace_sec"].(int64); ok {
			request.CancelGraceSec = value
		}
		if request.TimeoutSec < 0 || request.ProgressTimeoutSec < 0 || request.ApprovalTimeoutSec < 0 || request.CancelGraceSec < 0 {
			return nil, nil, fmt.Errorf("supervision timeout values must be non-negative")
		}
		explicitSessionID := strings.TrimSpace(firstNonEmptyToolValue(request.ID, request.SessionID)) != ""
		result, err := b.AgentSessions.Spawn(ctx, strings.TrimSpace(sessionID), request)
		if err != nil {
			return nil, nil, err
		}
		actualSessionID := ""
		if result != nil {
			actualSessionID = firstNonEmptyToolValue(result.SessionID, result.ID)
		}
		if handleAliases != nil && !explicitSessionID && actualSessionID != "" {
			handleAliases.Sessions.register(actualSessionID, deterministicHandleAlias(agentSessionAliasPrefix, toolCallID, ToolSpawnAgent, args))
			if err := b.saveSessionHandleAliases(ctx, sessionID, handleAliases); err != nil {
				return nil, nil, err
			}
		}
		aliasedResult := aliasAgentStatusResult(result, handleAliases)
		aliasedSessionID := actualSessionID
		if aliasedResult != nil {
			aliasedSessionID = firstNonEmptyToolValue(aliasedResult.SessionID, aliasedResult.ID)
		}
		metadata := map[string]interface{}{
			"session_id":    actualSessionID,
			"session_alias": aliasedSessionID,
			"status":        valueOrEmptyAgentStatus(result),
			"created":       result != nil && result.Created,
			"queued":        result != nil && result.Queued,
		}
		if b.ExecutionSupervisor != nil && result != nil && result.Created {
			runID, deadlineAt, runErr := startSpawnExecutionRun(ctx, b.ExecutionSupervisor, strings.TrimSpace(sessionID), request, result)
			if runErr != nil {
				metadata["supervision_error"] = runErr.Error()
			} else if runID != "" {
				metadata["run_id"] = runID
				if deadlineAt != "" {
					metadata["execution_deadline_at"] = deadlineAt
				}
				metadata["supervision_policy"] = spawnSupervisionPolicyEnforce
			}
		}
		if result != nil {
			if provider := strings.TrimSpace(result.Provider); provider != "" {
				metadata["provider"] = provider
			}
			if model := strings.TrimSpace(result.Model); model != "" {
				metadata["model"] = model
			}
			if effort := strings.TrimSpace(result.ReasoningEffort); effort != "" {
				metadata["reasoning_effort"] = effort
			}
			if permissionMode := strings.TrimSpace(result.PermissionMode); permissionMode != "" {
				metadata["permission_mode"] = permissionMode
			}
			if requirement := strings.TrimSpace(request.CompletionRequirement); requirement != "" {
				metadata["completion_requirement"] = requirement
			}
			if isolation := strings.TrimSpace(result.Isolation); isolation != "" {
				metadata["isolation"] = isolation
			} else if isolation := strings.TrimSpace(request.Isolation); isolation != "" {
				metadata["isolation"] = isolation
			}
			if path := strings.TrimSpace(result.WorktreePath); path != "" {
				metadata["worktree_path"] = path
			}
			if branch := strings.TrimSpace(result.WorktreeBranch); branch != "" {
				metadata["worktree_branch"] = branch
			}
			if repoRoot := strings.TrimSpace(result.WorktreeRepoRoot); repoRoot != "" {
				metadata["worktree_repo_root"] = repoRoot
			}
			if result.ReadOnly {
				metadata["read_only"] = true
			}
			if difficulty := strings.TrimSpace(result.Difficulty); difficulty != "" {
				metadata["difficulty"] = difficulty
			}
			if source := strings.TrimSpace(result.RouteSource); source != "" {
				metadata["route_source"] = source
			}
			if result.FallbackUsed {
				metadata["fallback_used"] = true
			}
			if reason := strings.TrimSpace(result.FallbackReason); reason != "" {
				metadata["fallback_reason"] = reason
			}
			for key, value := range map[string]string{
				"requested_provider":         result.RequestedProvider,
				"effective_provider":         firstNonEmptyToolValue(result.EffectiveProvider, result.Provider),
				"requested_model":            result.RequestedModel,
				"effective_model":            firstNonEmptyToolValue(result.EffectiveModel, result.Model),
				"requested_reasoning_effort": result.RequestedReasoningEffort,
				"effective_reasoning_effort": firstNonEmptyToolValue(result.EffectiveReasoningEffort, result.ReasoningEffort),
				"requested_permission_mode":  result.RequestedPermissionMode,
				"effective_permission_mode":  firstNonEmptyToolValue(result.EffectivePermissionMode, result.PermissionMode),
			} {
				if value = strings.TrimSpace(value); value != "" {
					metadata[key] = value
				}
			}
		}
		return aliasedResult, attachCacheSafeSummary(metadata, agentStatusCacheSafeSummary(aliasedResult)), nil

	case ToolListAgents:
		if b.AgentSessions == nil {
			return nil, nil, fmt.Errorf("agent session controller is not configured")
		}
		request := ListAgentsArgs{}
		if value, ok := args["parent_session_id"].(string); ok {
			request.ParentSessionID = strings.TrimSpace(value)
		}
		if value, ok := args["path_prefix"].(string); ok {
			request.PathPrefix = strings.TrimSpace(value)
		}
		if value, ok := args["include_closed"].(bool); ok {
			request.IncludeClosed = value
		}
		parentSessionID := firstNonEmptyToolValue(request.ParentSessionID, strings.TrimSpace(sessionID))
		result, err := b.AgentSessions.List(ctx, parentSessionID, request)
		if err != nil {
			return nil, nil, err
		}
		aliasedResult := aliasAgentListResult(result, handleAliases)
		return aliasedResult, attachCacheSafeSummary(map[string]interface{}{
			"count": valueOrZeroAgentListCount(result),
		}, agentListCacheSafeSummary(aliasedResult)), nil

	case ToolSendMessage, ToolFollowupTask:
		if b.AgentSessions == nil {
			return nil, nil, fmt.Errorf("agent session controller is not configured")
		}
		request := AgentMessageArgs{}
		if value, ok := args["target"].(string); ok {
			request.Target = strings.TrimSpace(value)
		}
		if value, ok := args["id"].(string); ok {
			request.ID = strings.TrimSpace(value)
		}
		if value, ok := args["session_id"].(string); ok {
			request.SessionID = strings.TrimSpace(value)
		}
		if value, ok := args["message"].(string); ok {
			request.Message = strings.TrimSpace(value)
		}
		sessionRef := strings.TrimSpace(firstNonEmptyToolValue(request.Target, request.ID, request.SessionID))
		actualSessionID := sessionRef
		if handleAliases != nil {
			actualSessionID, _, err = handleAliases.Sessions.resolve(sessionRef, agentSessionAliasPrefix, "agent session")
			if err != nil {
				return nil, nil, err
			}
		}
		request.Target = actualSessionID
		request.ID = ""
		request.SessionID = actualSessionID
		if err := b.rejectTeamTeammateAgentRefs(ctx, sessionID, toolName, actualSessionID); err != nil {
			return nil, nil, err
		}
		var result *AgentMessageResult
		if toolName == ToolFollowupTask {
			if strings.TrimSpace(actualSessionID) == strings.TrimSpace(sessionID) {
				return nil, nil, fmt.Errorf("followup_task target cannot be the current/root session")
			}
			result, err = b.AgentSessions.FollowupTask(ctx, strings.TrimSpace(sessionID), request)
		} else {
			result, err = b.AgentSessions.SendMessage(ctx, strings.TrimSpace(sessionID), request)
		}
		if err != nil {
			return nil, nil, err
		}
		aliasedResult := aliasAgentMessageResult(result, handleAliases)
		return aliasedResult, attachCacheSafeSummary(map[string]interface{}{
			"session_id":    strings.TrimSpace(actualSessionID),
			"session_alias": aliasSessionValue(actualSessionID, handleAliases),
			"delivered":     result != nil && result.Delivered,
			"triggered":     result != nil && result.Triggered,
		}, agentMessageCacheSafeSummary(aliasedResult)), nil

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
		sessionRef := strings.TrimSpace(firstNonEmptyToolValue(request.ID, request.SessionID))
		actualSessionID := sessionRef
		if handleAliases != nil {
			actualSessionID, _, err = handleAliases.Sessions.resolve(sessionRef, agentSessionAliasPrefix, "agent session")
			if err != nil {
				return nil, nil, err
			}
		}
		if strings.TrimSpace(request.ID) != "" {
			request.ID = actualSessionID
		} else {
			request.SessionID = actualSessionID
		}
		result, err := b.AgentSessions.SendInput(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		aliasedResult := aliasAgentStatusResult(result, handleAliases)
		aliasedSessionID := actualSessionID
		if aliasedResult != nil {
			aliasedSessionID = firstNonEmptyToolValue(aliasedResult.SessionID, aliasedResult.ID)
		}
		return aliasedResult, attachCacheSafeSummary(map[string]interface{}{
			"session_id":    actualSessionID,
			"session_alias": aliasedSessionID,
			"status":        valueOrEmptyAgentStatus(result),
			"queued":        result != nil && result.Queued,
		}, agentStatusCacheSafeSummary(aliasedResult)), nil

	case ToolResolveAgentApproval:
		if b.AgentSessions == nil {
			return nil, nil, fmt.Errorf("agent session controller is not configured")
		}
		request := ResolveAgentApprovalArgs{}
		if value, ok := args["id"].(string); ok {
			request.ID = strings.TrimSpace(value)
		}
		if value, ok := args["session_id"].(string); ok && strings.TrimSpace(request.ID) == "" {
			request.SessionID = strings.TrimSpace(value)
		}
		if value, ok := args["request_id"].(string); ok {
			request.RequestID = strings.TrimSpace(value)
		}
		allowValue, hasAllow := args["allow"].(bool)
		if !hasAllow {
			return nil, nil, fmt.Errorf("allow is required")
		}
		request.Allow = allowValue
		if patched, ok := args["patched_args"]; ok {
			raw, err := marshalToolJSONArg(patched)
			if err != nil {
				return nil, nil, fmt.Errorf("patched_args: %w", err)
			}
			request.PatchedArgs = raw
		}
		sessionRef := strings.TrimSpace(firstNonEmptyToolValue(request.ID, request.SessionID))
		if sessionRef == "" {
			return nil, nil, fmt.Errorf("id is required")
		}
		if request.RequestID == "" {
			return nil, nil, fmt.Errorf("request_id is required")
		}
		actualSessionID := sessionRef
		if handleAliases != nil {
			actualSessionID, _, err = handleAliases.Sessions.resolve(sessionRef, agentSessionAliasPrefix, "agent session")
			if err != nil {
				return nil, nil, err
			}
		}
		if err := b.rejectTeamTeammateAgentRefs(ctx, sessionID, ToolResolveAgentApproval, actualSessionID); err != nil {
			return nil, nil, err
		}
		if strings.TrimSpace(request.ID) != "" {
			request.ID = actualSessionID
		} else {
			request.SessionID = actualSessionID
		}
		result, err := b.AgentSessions.ResolveApproval(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		aliasedResult := aliasAgentApprovalResult(result, handleAliases)
		aliasedSessionID := actualSessionID
		if aliasedResult != nil {
			aliasedSessionID = strings.TrimSpace(aliasedResult.SessionID)
		}
		status := ""
		if result != nil && result.Status != nil {
			status = strings.TrimSpace(result.Status.Status)
		}
		return aliasedResult, attachCacheSafeSummary(map[string]interface{}{
			"session_id":    actualSessionID,
			"session_alias": aliasedSessionID,
			"request_id":    request.RequestID,
			"allowed":       result != nil && result.Allowed,
			"resolved":      result != nil && result.Resolved,
			"status":        status,
		}, agentApprovalCacheSafeSummary(aliasedResult)), nil

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
		if value, ok := args["after_seq"].(float64); ok {
			request.AfterSeq = int64(value)
		} else if value, ok := args["after_seq"].(int64); ok {
			request.AfterSeq = value
		} else if value, ok := args["after_seq"].(int); ok {
			request.AfterSeq = int64(value)
		}
		request.IDs = coerceStringSlice(args["ids"])
		request.SessionIDs = coerceStringSlice(args["session_ids"])
		if strings.TrimSpace(request.ID) == "" && strings.TrimSpace(request.SessionID) == "" && len(request.IDs) == 0 && len(request.SessionIDs) == 0 {
			request.SessionID = strings.TrimSpace(sessionID)
			request.MailboxOnly = true
		}
		if handleAliases != nil {
			if request.ID, _, err = handleAliases.Sessions.resolve(request.ID, agentSessionAliasPrefix, "agent session"); err != nil {
				return nil, nil, err
			}
			if request.SessionID, _, err = handleAliases.Sessions.resolve(request.SessionID, agentSessionAliasPrefix, "agent session"); err != nil {
				return nil, nil, err
			}
			for index, value := range request.IDs {
				if request.IDs[index], _, err = handleAliases.Sessions.resolve(value, agentSessionAliasPrefix, "agent session"); err != nil {
					return nil, nil, err
				}
			}
			for index, value := range request.SessionIDs {
				if request.SessionIDs[index], _, err = handleAliases.Sessions.resolve(value, agentSessionAliasPrefix, "agent session"); err != nil {
					return nil, nil, err
				}
			}
		}
		if err := b.rejectTeamTeammateAgentRefs(ctx, sessionID, ToolWaitAgent, request.ID, request.SessionID); err != nil {
			return nil, nil, err
		}
		if err := b.rejectTeamTeammateAgentRefs(ctx, sessionID, ToolWaitAgent, append(request.IDs, request.SessionIDs...)...); err != nil {
			return nil, nil, err
		}
		result, err := b.AgentSessions.Wait(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		aliasedResult := aliasAgentWaitResult(result, handleAliases)
		aliasedMatchedSessionID := ""
		if aliasedResult != nil {
			aliasedMatchedSessionID = strings.TrimSpace(aliasedResult.MatchedSessionID)
		}
		return aliasedResult, attachCacheSafeSummary(map[string]interface{}{
			"session_id":    valueOrEmptyWaitMatchedSession(result),
			"session_alias": aliasedMatchedSessionID,
			"status":        waitResultStatus(result),
			"timed_out":     result != nil && result.TimedOut,
			"ready_count":   valueOrZeroWaitReadyCount(result),
			"pending_count": valueOrZeroWaitPendingCount(result),
			"waited_ms":     valueOrZeroWaitedMs(result),
			"next_action":   valueOrEmptyWaitNextAction(result),
			"latest_seq":    valueOrZeroWaitSeq(result),
		}, agentWaitCacheSafeSummary(aliasedResult)), nil

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
		noTarget := strings.TrimSpace(request.ID) == "" && strings.TrimSpace(request.SessionID) == ""
		if noTarget {
			request.SessionID = strings.TrimSpace(sessionID)
			request.MailboxOnly = true
		}
		sessionRef := strings.TrimSpace(firstNonEmptyToolValue(request.ID, request.SessionID))
		actualSessionID := sessionRef
		if handleAliases != nil {
			actualSessionID, _, err = handleAliases.Sessions.resolve(sessionRef, agentSessionAliasPrefix, "agent session")
			if err != nil {
				return nil, nil, err
			}
		}
		if !request.MailboxOnly {
			if err := b.rejectTeamTeammateAgentRefs(ctx, sessionID, ToolReadAgentEvents, actualSessionID); err != nil {
				return nil, nil, err
			}
		}
		if strings.TrimSpace(request.ID) != "" {
			request.ID = actualSessionID
		} else {
			request.SessionID = actualSessionID
		}
		if strings.TrimSpace(actualSessionID) == "" {
			return nil, nil, fmt.Errorf("id is required")
		}
		result, err := b.AgentSessions.ReadEvents(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		result = FinalizeAgentEventsResult(result)
		aliasedResult := aliasAgentEventsResult(result, handleAliases)
		aliasedSessionID := actualSessionID
		if aliasedResult != nil {
			aliasedSessionID = strings.TrimSpace(aliasedResult.SessionID)
		}
		return aliasedResult, attachCacheSafeSummary(map[string]interface{}{
			"session_id":    valueOrEmptyEventsSession(result),
			"session_alias": aliasedSessionID,
			"count":         valueOrZeroEventsCount(result),
			"latest_seq":    valueOrZeroEventsSeq(result),
			"timed_out":     result != nil && result.TimedOut,
			"next_action":   valueOrEmptyEventsNextAction(result),
		}, agentEventsCacheSafeSummary(aliasedResult)), nil

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
		actualSessionID := sessionKey
		if handleAliases != nil {
			actualSessionID, _, err = handleAliases.Sessions.resolve(sessionKey, agentSessionAliasPrefix, "agent session")
			if err != nil {
				return nil, nil, err
			}
		}
		result, err := b.AgentSessions.Close(ctx, actualSessionID)
		if err != nil {
			return nil, nil, err
		}
		aliasedResult := aliasAgentStatusResult(result, handleAliases)
		aliasedSessionID := actualSessionID
		if aliasedResult != nil {
			aliasedSessionID = firstNonEmptyToolValue(aliasedResult.SessionID, aliasedResult.ID)
		}
		return aliasedResult, attachCacheSafeSummary(map[string]interface{}{
			"session_id":    actualSessionID,
			"session_alias": aliasedSessionID,
			"status":        valueOrEmptyAgentStatus(result),
		}, agentStatusCacheSafeSummary(aliasedResult)), nil

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
		actualSessionID := sessionKey
		if handleAliases != nil {
			actualSessionID, _, err = handleAliases.Sessions.resolve(sessionKey, agentSessionAliasPrefix, "agent session")
			if err != nil {
				return nil, nil, err
			}
		}
		result, err := b.AgentSessions.Resume(ctx, actualSessionID)
		if err != nil {
			return nil, nil, err
		}
		aliasedResult := aliasAgentStatusResult(result, handleAliases)
		aliasedSessionID := actualSessionID
		if aliasedResult != nil {
			aliasedSessionID = firstNonEmptyToolValue(aliasedResult.SessionID, aliasedResult.ID)
		}
		return aliasedResult, attachCacheSafeSummary(map[string]interface{}{
			"session_id":    actualSessionID,
			"session_alias": aliasedSessionID,
			"status":        valueOrEmptyAgentStatus(result),
		}, agentStatusCacheSafeSummary(aliasedResult)), nil

	case ToolApplyAgentWorktree:
		if b.AgentSessions == nil {
			return nil, nil, fmt.Errorf("agent session controller is not configured")
		}
		request := ApplyAgentWorktreeArgs{}
		if value, ok := args["id"].(string); ok {
			request.ID = strings.TrimSpace(value)
		}
		if value, ok := args["session_id"].(string); ok && strings.TrimSpace(request.ID) == "" {
			request.SessionID = strings.TrimSpace(value)
		}
		request.Paths = coerceStringSlice(args["paths"])
		if value, ok := args["keep"].(bool); ok {
			request.Keep = value
		}
		sessionRef := strings.TrimSpace(firstNonEmptyToolValue(request.ID, request.SessionID))
		if sessionRef == "" {
			return nil, nil, fmt.Errorf("id is required")
		}
		actualSessionID := sessionRef
		if handleAliases != nil {
			actualSessionID, _, err = handleAliases.Sessions.resolve(sessionRef, agentSessionAliasPrefix, "agent session")
			if err != nil {
				return nil, nil, err
			}
		}
		if err := b.rejectTeamTeammateAgentRefs(ctx, sessionID, ToolApplyAgentWorktree, actualSessionID); err != nil {
			return nil, nil, err
		}
		if strings.TrimSpace(request.ID) != "" {
			request.ID = actualSessionID
		} else {
			request.SessionID = actualSessionID
		}
		result, err := b.AgentSessions.ApplyWorktree(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		aliasedResult := aliasAgentWorktreeResult(result, handleAliases)
		aliasedSessionID := actualSessionID
		if aliasedResult != nil {
			aliasedSessionID = firstNonEmptyToolValue(aliasedResult.SessionID, aliasedResult.ID)
		}
		return aliasedResult, attachCacheSafeSummary(map[string]interface{}{
			"session_id":    actualSessionID,
			"session_alias": aliasedSessionID,
			"action":        valueOrEmptyWorktreeAction(result),
			"applied":       result != nil && result.Applied,
			"removed":       result != nil && result.Removed,
			"kept":          result != nil && result.Kept,
			"worktree_path": valueOrEmptyWorktreePath(result),
		}, agentWorktreeCacheSafeSummary(aliasedResult)), nil

	case ToolDiscardAgentWorktree:
		if b.AgentSessions == nil {
			return nil, nil, fmt.Errorf("agent session controller is not configured")
		}
		request := DiscardAgentWorktreeArgs{}
		if value, ok := args["id"].(string); ok {
			request.ID = strings.TrimSpace(value)
		}
		if value, ok := args["session_id"].(string); ok && strings.TrimSpace(request.ID) == "" {
			request.SessionID = strings.TrimSpace(value)
		}
		sessionRef := strings.TrimSpace(firstNonEmptyToolValue(request.ID, request.SessionID))
		if sessionRef == "" {
			return nil, nil, fmt.Errorf("id is required")
		}
		actualSessionID := sessionRef
		if handleAliases != nil {
			actualSessionID, _, err = handleAliases.Sessions.resolve(sessionRef, agentSessionAliasPrefix, "agent session")
			if err != nil {
				return nil, nil, err
			}
		}
		if err := b.rejectTeamTeammateAgentRefs(ctx, sessionID, ToolDiscardAgentWorktree, actualSessionID); err != nil {
			return nil, nil, err
		}
		if strings.TrimSpace(request.ID) != "" {
			request.ID = actualSessionID
		} else {
			request.SessionID = actualSessionID
		}
		result, err := b.AgentSessions.DiscardWorktree(ctx, request)
		if err != nil {
			return nil, nil, err
		}
		aliasedResult := aliasAgentWorktreeResult(result, handleAliases)
		aliasedSessionID := actualSessionID
		if aliasedResult != nil {
			aliasedSessionID = firstNonEmptyToolValue(aliasedResult.SessionID, aliasedResult.ID)
		}
		return aliasedResult, attachCacheSafeSummary(map[string]interface{}{
			"session_id":    actualSessionID,
			"session_alias": aliasedSessionID,
			"action":        valueOrEmptyWorktreeAction(result),
			"discarded":     result != nil && result.Discarded,
			"removed":       result != nil && result.Removed,
			"worktree_path": valueOrEmptyWorktreePath(result),
		}, agentWorktreeCacheSafeSummary(aliasedResult)), nil

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
			entries, err := coerceObjectArray(raw, "teammates")
			if err != nil {
				return nil, nil, err
			}
			for _, entry := range entries {
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
		if raw, ok := args["tasks"]; ok {
			entries, err := coerceObjectArray(raw, "tasks")
			if err != nil {
				return nil, nil, err
			}
			for _, entry := range entries {
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
				if value, ok := entry["difficulty"].(string); ok {
					difficulty, ok := team.NormalizeTaskDifficulty(value)
					if !ok {
						return nil, nil, fmt.Errorf("invalid task difficulty: %s", strings.TrimSpace(value))
					}
					spec.Difficulty = difficulty
				}
				if value, ok := entry["difficulty_rationale"].(string); ok {
					spec.DifficultyRationale = strings.TrimSpace(value)
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
				if team.IsTerminalTeamStatus(existing.Status) ||
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

		request.Teammates, err = b.ensureAutoStartTeammates(ctx, teamID, request, autoStart)
		if err != nil {
			return nil, nil, err
		}
		teammateIDs := make([]string, 0, len(request.Teammates))
		if allocator, ok := b.TeamDispatcher.(interface {
			EnsureTeammateSessionIDs(teamID string, specs []SpawnTeammateSpec) []SpawnTeammateSpec
		}); ok {
			request.Teammates = allocator.EnsureTeammateSessionIDs(teamID, request.Teammates)
		}
		var teammateProjector TeamTeammateAgentProjector
		if projector, ok := b.TeamDispatcher.(TeamTeammateAgentProjector); ok {
			teammateProjector = projector
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
			var previous *team.Teammate
			if teammateProjector != nil && strings.TrimSpace(teammate.ID) != "" {
				previous, err = b.TeamStore.GetTeammate(ctx, teammate.ID)
				if err != nil {
					return nil, nil, err
				}
			}
			id, err := b.TeamStore.UpsertTeammate(ctx, teammate)
			if err != nil {
				return nil, nil, err
			}
			if teammateProjector != nil {
				updated, err := b.TeamStore.GetTeammate(ctx, id)
				if err != nil {
					return nil, nil, err
				}
				if updated == nil {
					teammate.ID = id
					updated = &teammate
				}
				if err := teammateProjector.SyncTeamTeammateAgent(ctx, previous, *updated); err != nil {
					return nil, nil, err
				}
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
		taskRegistry := team.NewAgentControlTaskRegistry(b.TeamStore)
		for _, spec := range resolvedTaskSpecs {
			taskID := strings.TrimSpace(spec.ID)
			if taskID != "" {
				if existingTask, err := b.TeamStore.GetTask(ctx, taskID); err == nil && existingTask != nil && strings.TrimSpace(existingTask.TeamID) == strings.TrimSpace(teamID) {
					taskIDs = append(taskIDs, existingTask.ID)
					if strings.TrimSpace(spec.ID) != "" {
						taskIndex[strings.TrimSpace(spec.ID)] = existingTask.ID
					}
					continue
				} else if err != nil {
					return nil, nil, err
				}
			}
			created, err := taskRegistry.CreateAgentControlTask(ctx, agentcontrol.TaskCreateRequest{
				ID:                  taskID,
				Workflow:            agentcontrol.WorkflowSpawnTeam,
				TeamID:              teamID,
				Title:               strings.TrimSpace(spec.Title),
				Goal:                strings.TrimSpace(spec.Goal),
				Difficulty:          strings.TrimSpace(spec.Difficulty),
				DifficultyRationale: strings.TrimSpace(spec.DifficultyRationale),
				Priority:            spec.Priority,
				Assignee:            strings.TrimSpace(spec.Assignee),
				Inputs:              append([]string(nil), spec.Inputs...),
				ReadPaths:           append([]string(nil), spec.ReadPaths...),
				WritePaths:          append([]string(nil), spec.WritePaths...),
				Deliverables:        append([]string(nil), spec.Deliverables...),
			})
			if err != nil {
				return nil, nil, err
			}
			if created == nil || strings.TrimSpace(created.ID) == "" {
				return nil, nil, fmt.Errorf("task creation did not return a task id")
			}
			createdID := strings.TrimSpace(created.ID)
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
			dependencyWriter := team.NewAgentControlTaskRegistry(b.TeamStore)
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
				if err := dependencyWriter.CreateAgentControlTaskDependency(ctx, agentcontrol.TaskDependencyCreateRequest{
					Workflow:    agentcontrol.WorkflowSpawnTeam,
					TeamID:      teamID,
					TaskID:      taskID,
					DependsOnID: depID,
				}); err != nil {
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
		result := SpawnTeamResult{
			TeamID:        teamID,
			CreatedTeam:   createdTeam,
			AutoStarted:   autoStarted,
			TeammateIDs:   teammateIDs,
			TaskIDs:       taskIDs,
			TeammateCount: len(teammateIDs),
			TaskCount:     len(taskIDs),
		}
		return result, attachCacheSafeSummary(rawMeta, spawnTeamCacheSafeSummary(result)), nil

	case ToolWaitTeam:
		request := WaitTeamArgs{}
		if value, ok := args["team_id"].(string); ok {
			request.TeamID = strings.TrimSpace(value)
		}
		if value, ok := args["after_seq"].(float64); ok {
			request.AfterSeq = int64(value)
		} else if value, ok := args["after_seq"].(int64); ok {
			request.AfterSeq = value
		} else if value, ok := args["after_seq"].(int); ok {
			request.AfterSeq = int64(value)
		}
		if value, ok := args["timeout_ms"].(float64); ok {
			request.TimeoutMs = int(value)
		} else if value, ok := args["timeout_ms"].(int); ok {
			request.TimeoutMs = value
		}
		if value, ok := args["limit"].(float64); ok {
			request.Limit = int(value)
		} else if value, ok := args["limit"].(int); ok {
			request.Limit = value
		}
		if value, ok := args["require_summary"].(bool); ok {
			request.RequireSummary = &value
		}
		result, err := b.executeWaitTeam(ctx, sessionID, request)
		if err != nil {
			return nil, nil, err
		}
		return result, attachCacheSafeSummary(map[string]interface{}{
			"team_id":             result.TeamID,
			"status":              result.Status,
			"terminal":            result.Terminal,
			"summary_ready":       result.SummaryReady,
			"timed_out":           result.TimedOut,
			"wait_timeout_ms":     result.WaitTimeoutMs,
			"execution_continues": result.ExecutionContinues,
			"next_action":         result.NextAction,
			"latest_seq":          result.LatestSeq,
		}, waitTeamCacheSafeSummary(result)), nil

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
				result := SendTeamMessageResult{
					MessageID: messageID,
					TeamID:    teamID,
					FromAgent: agentID,
					ToAgent:   message.ToAgent,
					Kind:      message.Kind,
					TaskID:    taskID,
				}
				return result, attachCacheSafeSummary(rawMeta, sendTeamMessageCacheSafeSummary(result)), nil
			}
		}
		result := SendTeamMessageResult{
			MessageID: messageID,
			TeamID:    teamID,
			FromAgent: agentID,
			ToAgent:   message.ToAgent,
			Kind:      message.Kind,
			TaskID:    taskID,
		}
		return result, attachCacheSafeSummary(map[string]interface{}{
			"team_id":    teamID,
			"from_agent": agentID,
			"to_agent":   message.ToAgent,
		}, sendTeamMessageCacheSafeSummary(result)), nil

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
		result := ReadMailboxDigestResult{
			TeamID:       teamID,
			AgentID:      agentID,
			Digest:       digestResult.Digest,
			MessageIDs:   append([]string(nil), digestResult.MessageIDs...),
			MessageCount: digestResult.MessageCount,
			MarkedRead:   digestResult.MarkedRead,
		}
		return result, attachCacheSafeSummary(map[string]interface{}{
			"team_id":       teamID,
			"agent_id":      agentID,
			"message_count": digestResult.MessageCount,
			"marked_read":   digestResult.MarkedRead,
		}, readMailboxDigestCacheSafeSummary(result)), nil

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
		return result, attachCacheSafeSummary(map[string]interface{}{
			"team_id": task.TeamID,
			"task_id": task.ID,
			"status":  string(task.Status),
		}, readTaskSpecCacheSafeSummary(result)), nil

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

		return result, attachCacheSafeSummary(map[string]interface{}{
			"team_id":             teamID,
			"task_id":             task.ID,
			"message_count":       result.MessageCount,
			"dependency_count":    len(result.Dependencies),
			"dependent_count":     len(result.Dependents),
			"mailbox_marked_read": result.MarkedRead,
		}, readTaskContextCacheSafeSummary(result)), nil

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
		// Team outcome execution always resolves through an active scoped task.
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
		// Team outcome execution always resolves through an active scoped task.
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

func (b *Broker) ensureAutoStartTeammates(ctx context.Context, teamID string, request SpawnTeamArgs, autoStart bool) ([]SpawnTeammateSpec, error) {
	specs := append([]SpawnTeammateSpec(nil), request.Teammates...)
	if !autoStart || len(request.Tasks) == 0 || len(specs) > 0 {
		return specs, nil
	}
	if b == nil || b.TeamStore == nil {
		return specs, nil
	}
	existing, err := b.TeamStore.ListTeammates(ctx, strings.TrimSpace(teamID))
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		return specs, nil
	}
	return synthesizeAutoStartTeammates(request.Tasks, request.MaxTeammates), nil
}

func synthesizeAutoStartTeammates(tasks []SpawnTaskSpec, maxTeammates int) []SpawnTeammateSpec {
	seen := make(map[string]struct{}, len(tasks))
	specs := make([]SpawnTeammateSpec, 0, len(tasks))
	for _, task := range tasks {
		assignee := strings.TrimSpace(task.Assignee)
		if assignee == "" {
			continue
		}
		key := strings.ToLower(assignee)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		specs = append(specs, SpawnTeammateSpec{
			ID:   assignee,
			Name: assignee,
		})
	}
	if len(specs) > 0 {
		return specs
	}

	count := len(tasks)
	if maxTeammates > 0 && count > maxTeammates {
		count = maxTeammates
	}
	if count <= 0 {
		return nil
	}
	specs = make([]SpawnTeammateSpec, 0, count)
	for index := 0; index < count; index++ {
		specs = append(specs, SpawnTeammateSpec{
			ID:   fmt.Sprintf("mate-%d", index+1),
			Name: fmt.Sprintf("Teammate %d", index+1),
		})
	}
	return specs
}

func (b *Broker) prepareSpawnTaskSpecs(specs []SpawnTaskSpec) ([]SpawnTaskSpec, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	root := strings.TrimSpace(b.workspaceRoot())
	resolved := make([]SpawnTaskSpec, len(specs))
	copy(resolved, specs)
	seenIDs := make(map[string]struct{}, len(resolved))
	for _, spec := range resolved {
		id := strings.TrimSpace(spec.ID)
		if id == "" {
			continue
		}
		if _, ok := seenIDs[id]; ok {
			return nil, fmt.Errorf("duplicate spawn_team task id %q", id)
		}
		seenIDs[id] = struct{}{}
	}
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
	case "enterplanmode":
		return ToolEnterPlanMode
	case "exitplanmode":
		return ToolExitPlanMode
	case "backgroundtask":
		return ToolBackgroundTask
	case "taskoutput":
		return ToolTaskOutput
	case "spawnagent":
		return ToolSpawnAgent
	case "listagents":
		return ToolListAgents
	case "sendmessage":
		return ToolSendMessage
	case "followuptask":
		return ToolFollowupTask
	case "sendinput":
		return ToolSendInput
	case "resolveagentapproval", "approve_agent_tool", "approveagenttool", "approve_child_tool", "approvechildtool":
		return ToolResolveAgentApproval
	case "waitagent":
		return ToolWaitAgent
	case "readagentevents":
		return ToolReadAgentEvents
	case "closeagent":
		return ToolCloseAgent
	case "resumeagent":
		return ToolResumeAgent
	case "applyagentworktree", "apply_worktree", "applyworktree":
		return ToolApplyAgentWorktree
	case "discardagentworktree", "discard_worktree", "discardworktree":
		return ToolDiscardAgentWorktree
	case "spawnteam":
		return ToolSpawnTeam
	case "waitteam":
		return ToolWaitTeam
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
	for index := range result.Agents {
		if strings.EqualFold(strings.TrimSpace(result.Agents[index].SessionID), strings.TrimSpace(result.MatchedSessionID)) && strings.TrimSpace(result.Agents[index].Status) != "" {
			return strings.TrimSpace(result.Agents[index].Status)
		}
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
	case string(team.TeamStatusPartiallyCompleted):
		return team.TeamStatusPartiallyCompleted, nil
	case string(team.TeamStatusCanceled):
		return team.TeamStatusCanceled, nil
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

func marshalToolJSONArg(value interface{}) (json.RawMessage, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case json.RawMessage:
		if len(typed) == 0 {
			return nil, nil
		}
		if !json.Valid(typed) {
			return nil, fmt.Errorf("invalid JSON")
		}
		return append(json.RawMessage(nil), typed...), nil
	case []byte:
		if len(typed) == 0 {
			return nil, nil
		}
		if !json.Valid(typed) {
			return nil, fmt.Errorf("invalid JSON")
		}
		return append(json.RawMessage(nil), typed...), nil
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return nil, nil
		}
		if !json.Valid([]byte(text)) {
			return nil, fmt.Errorf("invalid JSON string")
		}
		return json.RawMessage(text), nil
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return nil, err
		}
		if string(raw) == "null" {
			return nil, nil
		}
		return raw, nil
	}
}

func coerceObjectArray(value interface{}, field string) ([]map[string]interface{}, error) {
	switch typed := value.(type) {
	case []map[string]interface{}:
		clone := make([]map[string]interface{}, 0, len(typed))
		for _, item := range typed {
			if item != nil {
				clone = append(clone, item)
			}
		}
		return clone, nil
	case []interface{}:
		clone := make([]map[string]interface{}, 0, len(typed))
		for _, item := range typed {
			entry, ok := item.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("%s must be an array of objects", field)
			}
			clone = append(clone, entry)
		}
		return clone, nil
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return nil, nil
		}
		var items []map[string]interface{}
		if err := json.Unmarshal([]byte(text), &items); err == nil {
			return coerceObjectArray(items, field)
		}
		var generic []interface{}
		if err := json.Unmarshal([]byte(text), &generic); err != nil {
			return nil, fmt.Errorf("%s must be an array of objects or a JSON array string: %w", field, err)
		}
		return coerceObjectArray(generic, field)
	default:
		if value == nil {
			return nil, nil
		}
		return nil, fmt.Errorf("%s must be an array of objects", field)
	}
}

func (b *Broker) resolveTeamScope(ctx context.Context, sessionID, explicitTeamID string) (teamID string, agentID string, taskID string, err error) {
	runMeta, ok := team.GetRunMeta(ctx)
	if !ok || runMeta == nil || runMeta.Team == nil || strings.TrimSpace(runMeta.Team.TeamID) == "" {
		return "", "", "", fmt.Errorf("team tools require an active team run; call spawn_team first from the lead chat or continue within an active team task")
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

func (b *Broker) resolveWaitTeamID(ctx context.Context, sessionID, explicitTeamID string) (string, error) {
	if b == nil || b.TeamStore == nil {
		return "", fmt.Errorf("team store is not configured")
	}
	teamID := strings.TrimSpace(explicitTeamID)
	if teamID != "" {
		return teamID, nil
	}
	if runMeta, ok := team.GetRunMeta(ctx); ok && runMeta != nil && runMeta.Team != nil {
		if scoped := strings.TrimSpace(runMeta.Team.TeamID); scoped != "" {
			return scoped, nil
		}
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", fmt.Errorf("team_id is required")
	}
	teams, err := b.TeamStore.ListTeams(ctx, team.TeamFilter{Limit: 64})
	if err != nil {
		return "", err
	}
	var matches []team.Team
	for _, item := range teams {
		if strings.EqualFold(strings.TrimSpace(item.LeadSessionID), sessionID) {
			matches = append(matches, item)
		}
	}
	if len(matches) == 1 {
		return strings.TrimSpace(matches[0].ID), nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("team_id is required because multiple teams belong to this lead session")
	}
	return "", fmt.Errorf("team_id is required")
}

func (b *Broker) executeWaitTeam(ctx context.Context, sessionID string, request WaitTeamArgs) (WaitTeamResult, error) {
	if b == nil || b.TeamStore == nil {
		return WaitTeamResult{}, fmt.Errorf("team store is not configured")
	}
	teamID, err := b.resolveWaitTeamID(ctx, sessionID, request.TeamID)
	if err != nil {
		return WaitTeamResult{}, err
	}
	b.notifyTeamLifecycleChanged()
	if _, err := team.ReconcileTerminalTeamState(ctx, team.TerminalTeamServices{
		Store:   b.TeamStore,
		Planner: b.TeamPlanner,
		Events:  b.TeamEvents,
	}, teamID); err != nil {
		return WaitTeamResult{}, err
	}
	if request.Limit <= 0 {
		request.Limit = 24
	}
	if request.Limit > 100 {
		request.Limit = 100
	}
	if request.TimeoutMs <= 0 {
		request.TimeoutMs = 30000
	}
	waitCtx, cancel := context.WithTimeout(ctx, time.Duration(request.TimeoutMs)*time.Millisecond)
	defer cancel()

	// Event-driven path: subscribe to the team lifecycle wake registry so a
	// durable team event (team.completed / team.summary / task.*) wakes the
	// wait immediately. A slow fallback ticker remains as a catch-up for
	// stores without a wake source or for missed notifications.
	wakeFilter := agentcontrol.WakeFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
		Kind:     agentcontrol.WakeKindTeam,
		TeamID:   teamID,
	}
	var wakeCh <-chan agentcontrol.WakeEvent
	var lastWakeSeq int64
	if ws, ok := b.TeamStore.(agentcontrol.WakeSource); ok {
		wakeCh, _ = ws.WatchAgentControlWake(waitCtx, wakeFilter)
		lastWakeSeq, _ = ws.LastAgentControlWakeSeq(waitCtx, wakeFilter)
	}

	// Fallback poll interval is intentionally long: the event-driven path
	// covers the normal case, so this ticker only guards against missed wake
	// notifications or stores without a wake source.
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return WaitTeamResult{}, ctx.Err()
			}
			result, snapshotErr := b.readWaitTeamSnapshot(ctx, teamID, request)
			if snapshotErr != nil {
				result = WaitTeamResult{TeamID: teamID}
			}
			return finalizeWaitTeamTimeout(result, request.TimeoutMs), nil
		default:
		}
		result, err := b.readWaitTeamSnapshot(ctx, teamID, request)
		if err != nil {
			return WaitTeamResult{}, err
		}
		if result.Terminal && (!b.waitTeamRequiresSummary(request) || result.SummaryReady) {
			result.WaitTimeoutMs = request.TimeoutMs
			return result, nil
		}
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return WaitTeamResult{}, ctx.Err()
			}
			return finalizeWaitTeamTimeout(result, request.TimeoutMs), nil
		case wake, ok := <-wakeCh:
			if !ok {
				wakeCh = nil
				continue
			}
			if wake.Seq > lastWakeSeq {
				lastWakeSeq = wake.Seq
			}
			// Re-read the snapshot immediately with fresh durable data.
		case <-ticker.C:
		}
	}
}

func finalizeWaitTeamTimeout(result WaitTeamResult, timeoutMs int) WaitTeamResult {
	result.TimedOut = true
	result.WaitTimeoutMs = timeoutMs
	result.ExecutionContinues = !result.Terminal
	if result.ExecutionContinues {
		result.NextAction = "team execution continues; wait timeout only ended this observation. Continue independent work or inspect current team status before waiting again"
	} else if result.Terminal && !result.SummaryReady {
		result.NextAction = "team execution is terminal but summary is not ready; wait again only if the summary is still required"
	} else {
		result.NextAction = "consume the terminal team result"
	}
	return result
}

func (b *Broker) waitTeamRequiresSummary(request WaitTeamArgs) bool {
	if request.RequireSummary != nil {
		return *request.RequireSummary
	}
	return true
}

func (b *Broker) readWaitTeamSnapshot(ctx context.Context, teamID string, request WaitTeamArgs) (WaitTeamResult, error) {
	record, err := b.TeamStore.GetTeam(ctx, teamID)
	if err != nil {
		return WaitTeamResult{}, err
	}
	if record == nil {
		return WaitTeamResult{}, fmt.Errorf("team not found: %s", teamID)
	}
	events, err := b.TeamStore.ListTeamEvents(ctx, team.TeamEventFilter{
		TeamID:   teamID,
		AfterSeq: request.AfterSeq,
		Limit:    request.Limit,
	})
	if err != nil {
		return WaitTeamResult{}, err
	}
	result := WaitTeamResult{
		TeamID:   strings.TrimSpace(record.ID),
		Status:   string(record.Status),
		Terminal: team.IsTerminalTeamStatus(record.Status),
		Events:   make([]WaitTeamEventResult, 0, len(events)),
	}
	for _, event := range events {
		item := WaitTeamEventResult{
			Seq:       event.Seq,
			Type:      strings.TrimSpace(event.Type),
			TeamID:    strings.TrimSpace(event.TeamID),
			Payload:   cloneAliasPayloadMap(event.Payload),
			CreatedAt: event.Timestamp,
		}
		result.Events = append(result.Events, item)
		if item.Seq > result.LatestSeq {
			result.LatestSeq = item.Seq
		}
		if item.Type == "team.summary" {
			result.SummaryReady = true
			result.SummaryEventSeq = item.Seq
			result.Summary = firstNonEmptyString(payloadString(item.Payload, "summary"), result.Summary)
			result.SummarySource = firstNonEmptyString(payloadString(item.Payload, "summary_source"), result.SummarySource)
			result.SummaryPayload = cloneAliasPayloadMap(item.Payload)
		}
	}
	result.EventCount = len(result.Events)
	if !result.SummaryReady {
		if summaryEvent, summaryErr := b.latestTeamSummaryEvent(ctx, teamID); summaryErr != nil {
			return WaitTeamResult{}, summaryErr
		} else if summaryEvent != nil {
			result.SummaryReady = true
			result.SummaryEventSeq = summaryEvent.Seq
			result.Summary = payloadString(summaryEvent.Payload, "summary")
			result.SummarySource = payloadString(summaryEvent.Payload, "summary_source")
			result.SummaryPayload = cloneAliasPayloadMap(summaryEvent.Payload)
			if summaryEvent.Seq > result.LatestSeq {
				result.LatestSeq = summaryEvent.Seq
			}
		}
	}
	return result, nil
}

func (b *Broker) latestTeamSummaryEvent(ctx context.Context, teamID string) (*team.TeamEventRecord, error) {
	events, err := b.TeamStore.ListTeamEvents(ctx, team.TeamEventFilter{
		TeamID:    strings.TrimSpace(teamID),
		EventType: "team.summary",
		Limit:     100,
	})
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}
	latest := events[len(events)-1]
	return &latest, nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func normalizeSpawnAgentPermissionMode(raw string) (string, error) {
	mode := runtimepolicy.Mode(strings.ToLower(strings.TrimSpace(raw)))
	switch mode {
	case "":
		return "", nil
	case runtimepolicy.ModeDefault, runtimepolicy.ModeAcceptEdits, runtimepolicy.ModePlan, runtimepolicy.ModeBypassPermissions:
		return string(mode), nil
	default:
		return "", fmt.Errorf("invalid agent permission_mode: %s", strings.TrimSpace(raw))
	}
}

// normalizeSpawnAgentCompletionRequirement validates the public spawn_agent
// contract. Ordinary children have no Team task identity, so complete_task can
// only be introduced later by TeammateRunner for a bound Team assignment.
func normalizeSpawnAgentCompletionRequirement(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	switch strings.ToLower(trimmed) {
	case "":
		return "", nil
	case string(agentdef.CompletionNone):
		return string(agentdef.CompletionNone), nil
	case string(agentdef.CompletionCompleteTask), "complete-task", "completetask":
		return "", runtimeerrors.Newf(runtimeerrors.ErrValidationFailed, "spawn_agent completion_requirement %q is not supported: ordinary child sessions have no Team task identity; use spawn_team or a Team assignment for complete_task workers", trimmed)
	default:
		return "", runtimeerrors.Newf(runtimeerrors.ErrValidationFailed, "invalid spawn_agent completion_requirement %q: ordinary child sessions only support none", trimmed)
	}
}

// resolveSpawnAgentCompletionRequirement fills completion from agentdef when
// spawn_agent only provided agent_type. Missing definitions are not errors.
func resolveSpawnAgentCompletionRequirement(agentType string) (string, error) {
	agentType = strings.TrimSpace(agentType)
	if agentType == "" {
		return "", nil
	}
	def, err := agentdef.Resolve(agentType, agentdef.DiscoverOptions{})
	if err != nil || def == nil {
		return "", nil
	}
	return normalizeSpawnAgentCompletionRequirement(string(def.CompletionRequirement))
}

// applySpawnAgentAgentdefDefaults fills permission_mode / read_only / model /
// provider from the portable agent definition when spawn_agent only provided
// agent_type (or left those fields empty). Explicit spawn args win.
func applySpawnAgentAgentdefDefaults(request *SpawnAgentArgs, permissionModeExplicit, readOnlyExplicit bool) {
	if request == nil {
		return
	}
	agentType := strings.TrimSpace(request.AgentType)
	if agentType == "" {
		return
	}
	def, err := agentdef.Resolve(agentType, agentdef.DiscoverOptions{})
	if err != nil || def == nil {
		return
	}
	binding, err := agentdef.BuildBinding(def)
	if err != nil || binding == nil {
		return
	}

	if !readOnlyExplicit && binding.ReadOnly != nil && *binding.ReadOnly {
		request.ReadOnly = true
	}
	if !permissionModeExplicit && strings.TrimSpace(request.PermissionMode) == "" {
		if mode := strings.TrimSpace(string(binding.PermissionMode)); mode != "" {
			if normalized, err := normalizeSpawnAgentPermissionMode(mode); err == nil && normalized != "" {
				request.PermissionMode = normalized
			}
		}
		// sandbox: read-only agents default to plan mode when mode still empty.
		if request.ReadOnly && strings.TrimSpace(request.PermissionMode) == "" {
			request.PermissionMode = string(runtimepolicy.ModePlan)
		}
	}
	if strings.TrimSpace(request.Provider) == "" {
		request.Provider = strings.TrimSpace(binding.Provider)
	}
	if strings.TrimSpace(request.Model) == "" {
		request.Model = strings.TrimSpace(binding.Model)
	}
}

func buildTaskSpecResult(task *team.Task) ReadTaskSpecResult {
	if task == nil {
		return ReadTaskSpecResult{}
	}
	result := ReadTaskSpecResult{
		TaskID:              task.ID,
		TeamID:              task.TeamID,
		Title:               task.Title,
		Goal:                task.Goal,
		Difficulty:          task.Difficulty,
		DifficultyRationale: task.DifficultyRationale,
		Inputs:              append([]string(nil), task.Inputs...),
		Status:              string(task.Status),
		Priority:            task.Priority,
		ReadPaths:           append([]string(nil), task.ReadPaths...),
		WritePaths:          append([]string(nil), task.WritePaths...),
		Deliverables:        append([]string(nil), task.Deliverables...),
		Summary:             task.Summary,
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
	route := team.TaskExecutionRouteFromRunMeta(activeRunMeta(ctx))

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
			Events: b.TeamEvents,
		}, team.TerminalTaskOutcomeRequest{
			Task:            *task,
			TeammateID:      agentID,
			Outcome:         applyOutcome,
			ResultRef:       resultRef,
			Route:           route.Clone(),
			DefaultStatus:   outcome.Status,
			SkipStateUpdate: true,
		})
		if err != nil {
			return ReportTaskOutcomeResult{}, nil, err
		}
		if b.TeamLifecycleChanged != nil {
			b.notifyTeamLifecycleChanged()
		} else if _, err := team.ReconcileTerminalTeamState(ctx, team.TerminalTeamServices{
			Store:               b.TeamStore,
			Planner:             b.TeamPlanner,
			Mailbox:             team.NewMailboxService(b.TeamStore),
			Events:              b.TeamEvents,
			IgnoreBusyTeammates: true,
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
		resultPayload := ReportTaskOutcomeResult{
			TaskID:    task.ID,
			TeamID:    teamID,
			Status:    string(result.Status),
			Outcome:   string(result.Outcome.Status),
			Summary:   result.Summary,
			Blocker:   strings.TrimSpace(result.Outcome.Blocker),
			ResultRef: firstNonEmptyString(payloadString(payload, "result_ref")),
			BlockedBy: agentID,
		}
		return resultPayload, attachCacheSafeSummary(payload, reportTaskOutcomeCacheSafeSummary(resultPayload)), nil

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
			Route:           route.Clone(),
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
		resultPayload := ReportTaskOutcomeResult{
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
		}
		return resultPayload, attachCacheSafeSummary(payload, reportTaskOutcomeCacheSafeSummary(resultPayload)), nil
	default:
		return ReportTaskOutcomeResult{}, nil, fmt.Errorf("unsupported task outcome: %s", outcome.Status)
	}
}

func activeRunMeta(ctx context.Context) *team.RunMeta {
	runMeta, ok := team.GetRunMeta(ctx)
	if !ok {
		return nil
	}
	return runMeta
}

func (b *Broker) notifyTeamLifecycleChanged() {
	if b == nil || b.TeamLifecycleChanged == nil {
		return
	}
	b.TeamLifecycleChanged()
}

func (b *Broker) rejectTeamTeammateAgentRefs(ctx context.Context, parentSessionID, toolName string, refs ...string) error {
	if b == nil || b.TeamStore == nil {
		return nil
	}
	spawnAgentRefs := b.currentSpawnAgentRefs(ctx, parentSessionID)
	seen := map[string]struct{}{}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		if _, ok := spawnAgentRefs[normalizeToolRefKey(ref)]; ok {
			continue
		}
		teammate, err := b.TeamStore.GetTeammate(ctx, ref)
		if err != nil {
			return err
		}
		if teammate == nil {
			continue
		}
		return fmt.Errorf("%s is a spawn_agent child-session tool, but %q is a spawn_team teammate id in team %q; use spawn_agent child session ids/paths with this tool, or call wait_team with team_id %q for spawn_team progress", toolName, ref, strings.TrimSpace(teammate.TeamID), strings.TrimSpace(teammate.TeamID))
	}
	return nil
}

func (b *Broker) currentSpawnAgentRefs(ctx context.Context, parentSessionID string) map[string]struct{} {
	if b == nil || b.AgentSessions == nil || strings.TrimSpace(parentSessionID) == "" {
		return nil
	}
	result, err := b.AgentSessions.List(ctx, strings.TrimSpace(parentSessionID), ListAgentsArgs{IncludeClosed: true})
	if err != nil || result == nil || len(result.Agents) == 0 {
		return nil
	}
	refs := make(map[string]struct{}, len(result.Agents)*3)
	for _, agent := range result.Agents {
		if !agentStatusLooksLikeSpawnAgentChild(agent) {
			continue
		}
		addToolRefKey(refs, agent.ID)
		addToolRefKey(refs, agent.SessionID)
		addToolRefKey(refs, agent.Path)
	}
	return refs
}

func agentStatusLooksLikeSpawnAgentChild(agent AgentStatusResult) bool {
	if strings.EqualFold(strings.TrimSpace(agent.AgentType), agentcontrol.AgentTypeChild) {
		return true
	}
	if strings.TrimSpace(agent.AgentType) == "" && strings.TrimSpace(agent.TeamID) == "" && strings.TrimSpace(agent.TeammateID) == "" {
		return true
	}
	return false
}

func addToolRefKey(refs map[string]struct{}, ref string) {
	if refs == nil {
		return
	}
	if key := normalizeToolRefKey(ref); key != "" {
		refs[key] = struct{}{}
	}
}

func normalizeToolRefKey(ref string) string {
	return strings.ToLower(strings.TrimSpace(ref))
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

func deterministicQuestionID(toolCallID string, request AskUserQuestionArgs) string {
	payload := struct {
		ToolCallID  string   `json:"tool_call_id,omitempty"`
		Prompt      string   `json:"prompt"`
		Suggestions []string `json:"suggestions,omitempty"`
		Required    bool     `json:"required"`
	}{
		ToolCallID:  strings.TrimSpace(toolCallID),
		Prompt:      strings.TrimSpace(request.Prompt),
		Suggestions: append([]string(nil), request.Suggestions...),
		Required:    request.Required,
	}
	encoded, err := json.Marshal(payload)
	if err != nil || len(encoded) == 0 {
		encoded = []byte(strings.TrimSpace(request.Prompt))
	}
	sum := sha256.Sum256(encoded)
	return "q_" + hex.EncodeToString(sum[:8])
}
