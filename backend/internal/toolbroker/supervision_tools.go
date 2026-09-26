package toolbroker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// P2-12 方案 3：监督控制面的模型入口。
//
// 在它出现之前，父 agent 只能在回合结束后靠宿主（preflight 摘要注入 +
// /debug supervision 命令）看到 supervision 通知，模型本身既读不到快照，
// 也无法把已处理的通知收敛掉，于是 critical 行会一轮轮重复注入（N9）。
//
// 三个工具的边界按「宿主能力」门控：只有宿主装配了 Supervision 控制器时
// 才会出现在 Definitions() 里，避免出现模型看得见、调不通的悬空工具。
// scope 由宿主按父会话推导（模型不能指定 root scope），写动作一律要求
// reason/note 并支持 expected_version CAS。

// AgentSupervisionController is the host-side capability behind the
// supervision_snapshot / supervision_descendants / ack_lifecycle /
// control_descendant / read_agent_result tools. Implementations must derive the
// caller's root scope from parentSessionID and enforce that a notification or
// result outside that scope is rejected.
type AgentSupervisionController interface {
	// SupervisionSnapshot returns the scoped preflight digest (read-only).
	SupervisionSnapshot(ctx context.Context, parentSessionID string, args SupervisionSnapshotArgs) (*supervision.Digest, error)
	// SupervisionDescendants returns the scoped descendant state matrix
	// (doc 6.2, read-only). It is the inspection primitive for business
	// supervision: one call shows every child/descendant of the caller's scope
	// with its execution status, supervision state and remediation hints, so a
	// parent does not have to poll wait_agent row by row.
	SupervisionDescendants(ctx context.Context, parentSessionID string, args SupervisionDescendantsArgs) (*supervision.Snapshot, error)
	// AckLifecycle applies one decision (acknowledge / defer / resolve).
	AckLifecycle(ctx context.Context, parentSessionID string, args AckLifecycleArgs) (*supervision.Notification, error)
	// ControlDescendant requests/executes a durable control action against the
	// notification subject (cancel / close / retry / reassign).
	ControlDescendant(ctx context.Context, parentSessionID string, args ControlDescendantArgs) (supervision.ActionRecord, error)
	// ReadAgentResult returns the bounded durable result of one child session
	// or batch task inside the caller's own scope (P0-4 改动 2, read-only).
	// Scope comes from the host; the model can never widen it. A missing
	// durable record is reported as source=none + no_result_recorded with an
	// actionable next_action, not as a hard tool error.
	ReadAgentResult(ctx context.Context, parentSessionID string, args ReadAgentResultArgs) (supervision.ReadResultPayload, error)
}

// SupervisionSnapshotArgs is the parsed input of supervision_snapshot.
type SupervisionSnapshotArgs struct {
	AfterSeq        int64
	IncludeResolved bool
	Limit           int
}

// SupervisionDescendantsArgs is the parsed input of supervision_descendants.
type SupervisionDescendantsArgs struct {
	// Mode is children | descendants (empty defaults to descendants).
	Mode string
	// Health is any | abnormal | action_required (empty defaults to any).
	Health string
	// IncludeTerminal keeps terminal rows (closed/terminated) in the matrix.
	IncludeTerminal bool
	// IncludeResults asks the provider for the bounded result projection
	// (result_status/result_summary/artifact_refs/error_class/finished_at).
	// Default false: the row payload stays byte-identical to before (P0-4).
	IncludeResults bool
	// Limit caps the returned rows (unresolved/abnormal rows are prioritized by
	// the builder's ordering, never dropped silently without truncated=true).
	Limit int
	// AfterSeq is the caller's last seen sequence; terminal rows newer than it
	// are counted as terminal_unacknowledged.
	AfterSeq int64
}

// ReadAgentResultArgs is the parsed input of read_agent_result (P0-4 改动 2).
type ReadAgentResultArgs struct {
	// SessionID is the child session id or agent path (required).
	SessionID string
	// TaskID optionally narrows the read to one durable batch task.
	TaskID string
	// Sections selects summary | findings | changes | artifacts | errors |
	// usage; empty means all sections.
	Sections []string
	// Offset is the rune offset used to page a long summary; zero starts at the
	// beginning. Only meaningful together with Limit.
	Offset int
	// Limit caps the summary runes returned by this read; zero uses the
	// max_chars budget. Non-zero values let a caller walk a long result instead
	// of losing the tail to the 512-rune display cap (P0-1 H4).
	Limit int
	// MaxChars bounds the serialized output; zero uses the default 4000.
	MaxChars int
}

// AckLifecycleArgs is the parsed input of ack_lifecycle.
type AckLifecycleArgs struct {
	NotificationID     string
	Decision           string
	Note               string
	Reason             string
	Until              time.Time
	Resolution         string
	ExpectedVersion    int64
	HasExpectedVersion bool
}

// ControlDescendantArgs is the parsed input of control_descendant.
type ControlDescendantArgs struct {
	NotificationID     string
	Action             string
	Reason             string
	Cascade            string
	ExpectedVersion    int64
	HasExpectedVersion bool
}

// supervisionToolDefinitions returns the advertised control-plane tools: one
// name per capability. Callers gate on the host capability before appending
// them.
//
// The legacy names stay callable through executeSupervisionTool (they live in
// allSupervisionToolDefinitions) so prompts, hosts and stored pointers keep
// working, but they are no longer advertised to the model: each of them is
// fully covered by an advertised sibling, and two names for one capability is
// exactly the drift this consolidation removes.
func supervisionToolDefinitions() []types.ToolDefinition {
	return advertisedSupervisionToolDefinitions(allSupervisionToolDefinitions())
}

// retiredSupervisionToolNames are the pre-consolidation definition names: they
// stay dispatchable and each maps onto an advertised sibling, but they are no
// longer advertised — two names for one capability is exactly the drift this
// consolidation removes.
//
//	supervision_snapshot    -> subagent_status(include_digest=true)
//	supervision_descendants -> subagent_status
//	read_agent_result       -> subagent_inspect_task(include_status=false)
//
// The verb spellings ack_lifecycle / control_descendant are alias-only inputs to
// normalizeToolName (never definition names) and resolve to
// subagent_ack_lifecycle / subagent_control.
var retiredSupervisionToolNames = map[string]bool{
	ToolSupervisionSnapshot:    true,
	ToolSupervisionDescendants: true,
	ToolReadAgentResult:        true,
}

// advertisedSupervisionToolDefinitions drops the legacy names from the
// model-facing list. The filter is data-driven (a name set, not a slice index)
// so adding or removing a legacy name cannot silently shift the advertised
// surface.
func advertisedSupervisionToolDefinitions(all []types.ToolDefinition) []types.ToolDefinition {
	advertised := make([]types.ToolDefinition, 0, len(all))
	for _, definition := range all {
		if retiredSupervisionToolNames[definition.Name] {
			continue
		}
		advertised = append(advertised, definition)
	}
	return advertised
}

// allSupervisionToolDefinitions holds every control-plane definition the
// dispatcher accepts, advertised or not.
func allSupervisionToolDefinitions() []types.ToolDefinition {
	return []types.ToolDefinition{
		{
			Name: ToolSupervisionSnapshot,
			Description: "Read the scoped supervision digest for this session (read-only). Returns critical_unresolved / action_required counts, the lifecycle items with their notification_id + version, the rendered text and next_seq for incremental reads. Rows whose subject no longer exists are reported as stale and need no action. " +
				"Use this digest for lifecycle decisions (acknowledge/defer/resolve); use supervision_descendants when you need the child/descendant state matrix - do not poll wait_agent row by row. Call this before ack_lifecycle whenever a version conflict is reported.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"after_seq": map[string]interface{}{
						"type":        "integer",
						"description": "Only include items with event_seq greater than this cursor (use the previous next_seq).",
					},
					"include_resolved": map[string]interface{}{
						"type":        "boolean",
						"description": "Include items resolved after after_seq (default false).",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Cap the number of returned items; unresolved critical rows stay prioritized and truncated=true reports the cut.",
					},
				},
			},
		},
		{
			Name: ToolSupervisionDescendants,
			Description: "Read the scoped descendant state matrix (doc 6.2, read-only): one call lists the children/descendants of this session's own scope with execution_status, supervision_state (running/blocked/stalled/timed_out/orphaned/terminal), heartbeat_age_ms / progress_age_ms, action_required, recommended_action, allowed_actions and notification_id. " +
				"Prefer this tool for supervision inspection: it returns the whole N-row matrix (how many are still running, which one is stalled, which finished) in one call, so do not poll wait_agent / list_agents row by row; repeat calls are expected and are exempt from the anti-polling advisory because they are an observation, not a blocking wait. " +
				"Read a row as follows: action_required=true means the parent must decide; recommended_action is the host's suggested next step for that row; allowed_actions is the subset this host can actually execute (control_descendant re-validates against it); next_action explains any remediation path that was filtered out because the host has no entry point for it. " +
				"include_results=true additionally attaches the bounded per-row result projection (result_status/result_summary/artifact_refs/error_class/finished_at); it significantly increases the output, so use it only when converging finished rows. " +
				"Rows whose subject no longer exists are reported with the state the control plane last saw. Only rows inside this session's scope are returned; the model cannot widen the scope.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"mode": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"children", "descendants"},
						"description": "children = direct children only; descendants = whole subtree (default).",
					},
					"health": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"any", "abnormal", "action_required"},
						"description": "any (default) = full matrix; abnormal = only stalled/timed_out/orphaned/invalid/terminating rows; action_required = only rows asking the parent to decide (action_required=true). Unknown values are rejected instead of silently widening the read.",
					},
					"include_terminal": map[string]interface{}{
						"type":        "boolean",
						"description": "Keep terminal (closed/terminated) rows in the matrix (default false). Turn it on when converging a finished batch: terminal_unacknowledged counts finished rows not yet acknowledged or closed.",
					},
					"include_results": map[string]interface{}{
						"type":        "boolean",
						"description": "Attach the bounded result projection per row (result_status, result_summary <=512 runes with result_truncated, artifact_refs <=3, error_class, finished_at). Default false. Significantly increases the output; use it only when converging results (typically with include_terminal=true), then read details with read_agent_result.",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Cap the number of returned rows; abnormal/action-required rows stay prioritized and truncated=true reports the cut. Keep it small for routine inspections; raise it only when the full matrix is needed.",
					},
					"after_seq": map[string]interface{}{
						"type":        "integer",
						"description": "Last lifecycle sequence already seen by the caller (use the previous next_seq); terminal rows newer than it are counted as terminal_unacknowledged.",
					},
				},
			},
		},
		{
			Name: ToolReadAgentResult,
			Description: "Read the bounded durable result of one child session or batch task (read-only, P0-4). Resolves id inside this session's own scope only (the model cannot widen the scope) and reads, in priority order: the batch task TaskResult (summary/findings/changes/artifacts/errors/usage) and then the terminal mailbox completion payload (status/success/error/usage). " +
				"The output is bounded: findings <=3, changes <=8, artifacts <=8, errors <=3 and a total max_chars budget (default 4000); truncated=true reports any cut. source is task_result | completion_payload | none. " +
				"When no durable record exists the call still succeeds with source=none and error_code=no_result_recorded plus a next_action: follow it (read_agent_events / wait_agent) instead of retrying the same read. Sections let you fetch only what you need (summary/findings/changes/artifacts/errors/usage).",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{
						"type":        "string",
						"description": "Child session id or agent path (required). Must belong to this session's own scope.",
					},
					"task_id": map[string]interface{}{
						"type":        "string",
						"description": "Optional batch task id; narrows the read to that durable task record.",
					},
					"sections": map[string]interface{}{
						"type":        "array",
						"items":       map[string]interface{}{"type": "string"},
						"description": "Optional subset of summary | output | findings | changes | artifacts | errors | usage (default: all). \"output\" is accepted as an alias of summary. An unknown section is rejected instead of widening the read.",
					},
					"offset": map[string]interface{}{
						"type":        "integer",
						"description": "Optional rune offset into the summary section; use the previous next_offset to continue. Non-zero offsets page a long result instead of re-reading the head.",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Optional summary rune cap for this read (default: the max_chars budget). eof=false plus next_offset means more text remains.",
					},
					"max_chars": map[string]interface{}{
						"type":        "integer",
						"description": "Optional output budget in characters (default 4000, clamped to 256..20000). truncated=true reports any cut.",
					},
				},
				"required": []string{"id"},
			},
		},
		{
			Name: ToolAckLifecycle,
			Description: "Decide one supervision notification so it stops being re-injected: decision=acknowledge (accept the handled risk; note required), decision=defer (postpone until an RFC3339 deadline or Go duration such as 30m; reason required), decision=resolve (set the terminal resolution state closed|recovered|failed). Only notifications inside this session's own scope can be decided. " +
				"notification_id must be copied verbatim from subagent_status(include_digest=true) output - never invented or synthesized. Pass expected_version from the same read; on a version conflict re-read subagent_status(include_digest=true) instead of retrying blindly.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"notification_id": map[string]interface{}{
						"type":        "string",
						"description": "Notification id copied verbatim from subagent_status(include_digest=true); do not invent or synthesize ids.",
					},
					"decision": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"acknowledge", "defer", "resolve"},
						"description": "Required decision.",
					},
					"note": map[string]interface{}{
						"type":        "string",
						"description": "Audit note; required for decision=acknowledge.",
					},
					"reason": map[string]interface{}{
						"type":        "string",
						"description": "Audit reason; required for decision=defer.",
					},
					"until": map[string]interface{}{
						"type":        "string",
						"description": "Defer deadline: RFC3339 timestamp or Go duration (30m, 2h); required for decision=defer.",
					},
					"state": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"closed", "recovered", "failed"},
						"description": "Resolution state; required for decision=resolve.",
					},
					"expected_version": map[string]interface{}{
						"type":        "integer",
						"description": "Optional optimistic-concurrency guard taken from the subagent_status digest read.",
					},
				},
				"required": []string{"notification_id", "decision"},
			},
		},
		{
			Name: ToolControlDescendant,
			Description: "Execute a durable control action against the subject of a supervision notification: action=cancel|close|cancel_subtree|retry|reassign. Use action=close to converge a finished (terminal) child session that is no longer needed; the parent turn's progress block reminds you when a batch reached terminal. " +
				"The subject is taken from the notification, so only rows inside this session's own scope can be controlled; the requested action is validated against the row's allowed_actions. reason is required (durable audit), expected_version is optional. The action is persisted before execution; the returned action_id can be re-read even when execution fails, and a successful mutation produces an ack-able receipt notification.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"notification_id": map[string]interface{}{
						"type":        "string",
						"description": "Notification id whose subject should be controlled; copy it verbatim from subagent_status (never invent it).",
					},
					"action": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"cancel", "close", "cancel_subtree", "retry", "reassign"},
						"description": "Required control action; validated against the notification's server-computed allowed_actions (a value outside that set is rejected).",
					},
					"reason": map[string]interface{}{
						"type":        "string",
						"description": "Required audit reason.",
					},
					"cascade": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"target", "descendants"},
						"description": "cascade=descendants freezes the root and propagates to live descendants (default target).",
					},
					"expected_version": map[string]interface{}{
						"type":        "integer",
						"description": "Optional optimistic-concurrency guard taken from the subagent_status digest read.",
					},
				},
				"required": []string{"notification_id", "action", "reason"},
			},
		},
		{
			Name: ToolSubagentStatus,
			Description: "Read the scoped subagent ledger overview (read-only): one call returns every child/descendant row of this session's own scope with execution_status, supervision_state (running/blocked/stalled/timed_out/orphaned/terminal), heartbeat_age_ms / progress_age_ms, attempt/max_attempts, deadlines, action_required, recommended_action, allowed_actions[] and notification_id, plus the ledger rollup pending_count / terminal_count / terminal_delta[]. " +
				"terminal_delta[] lists the rows that finished since your after_seq cursor, so pass the next_seq of your previous read to see only what changed. Use this as the inspection primitive instead of polling wait_agent row by row; repeat calls are legitimate observation and are exempt from the anti-polling advisory. " +
				"An empty ledger returns next_action=finalize immediately (no empty wait); while any row may still be pending, next_action never claims you may finalize. allowed_actions[] is per row: a control action must be one of that row's values and is re-validated by subagent_control. include_results=true attaches the bounded per-row result projection (result_status/result_summary/artifact_refs/error_class/finished_at). " +
				"Set include_digest=true to fold the scoped lifecycle digest into the same answer (critical_unresolved / action_required counts, the notification rows with their notification_id + version, the rendered text and next_seq for incremental reads); include_resolved=true additionally lists items resolved after your after_seq cursor. A digest read failure is reported as digest_error and never hides the matrix. Use the digest for lifecycle decisions with subagent_ack_lifecycle. The scope is derived from the caller's own session and cannot be widened by the model.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"mode": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"children", "descendants"},
						"description": "children = direct children only; descendants = whole subtree (default).",
					},
					"health": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"any", "abnormal", "action_required"},
						"description": "any (default) = full matrix; abnormal = only stalled/timed_out/orphaned/invalid/terminating rows; action_required = only rows asking the parent to decide. Unknown values are rejected instead of silently widening the read.",
					},
					"include_terminal": map[string]interface{}{
						"type":        "boolean",
						"description": "Keep terminal (closed/terminated) rows in the matrix (default false). Turn it on when converging a finished batch.",
					},
					"include_results": map[string]interface{}{
						"type":        "boolean",
						"description": "Attach the bounded per-row result projection (default false). Significantly increases the output; use it only when converging finished rows.",
					},
					"include_digest": map[string]interface{}{
						"type":        "boolean",
						"description": "Fold the scoped lifecycle digest into this answer (default false). Same payload as the legacy supervision_snapshot read: critical_unresolved / action_required counts, items[] with notification_id + version, rendered text and next_seq. A digest failure degrades to digest_error instead of failing the ledger read.",
					},
					"include_resolved": map[string]interface{}{
						"type":        "boolean",
						"description": "With include_digest=true, also list items resolved after after_seq (default false).",
					},
					"after_seq": map[string]interface{}{
						"type":        "integer",
						"description": "Your last seen sequence (use the previous next_seq): terminal rows newer than it are reported in terminal_delta[] and counted as terminal_unacknowledged; with include_digest=true it is also the digest cursor.",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Cap the returned rows; unresolved/abnormal rows stay prioritized and truncated=true reports the cut.",
					},
				},
			},
		},
		{
			Name: ToolSubagentInspectTask,
			Description: "Inspect one subagent or durable task inside this session's own scope (read-only): returns the bounded durable result (source/status/findings/changes/artifacts/errors/usage/truncated) and, unless include_status=false, the subject's current supervision row (execution_status, supervision_state, heartbeat/progress ages, allowed_actions, action_required). " +
				"Output is byte-bounded by max_chars (default 4000, clamped to 256..20000); page a long summary with offset/limit instead of losing the tail. " +
				"A subject outside your scope, or a missing durable record, is reported as an observation (status_source / source=none + error_code=no_result_recorded + next_action), never as a tool failure; follow that next_action (read_agent_events / wait_agent) instead of retrying the same read. If the status read fails the result is still returned with status_source=matrix_unavailable. " +
				"Prefer this over re-reading the whole matrix when you need one subject's deliverable.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{
						"type":        "string",
						"description": "Child session id or agent path (required). Aliases: session_id, target, agent, child_session_id.",
					},
					"task_id": map[string]interface{}{
						"type":        "string",
						"description": "Optional batch task id; narrows the read to that durable task record.",
					},
					"sections": map[string]interface{}{
						"description": "Optional subset of summary | findings | changes | artifacts | errors | usage (default: all). An unknown section is rejected instead of widening the read.",
						"oneOf": []map[string]interface{}{
							{"type": "string"},
							{"type": "array", "items": map[string]interface{}{"type": "string"}},
						},
					},
					"offset": map[string]interface{}{
						"type":        "integer",
						"description": "Rune offset into the summary; use the previous next_offset to continue instead of re-reading the head.",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Cap the summary runes returned by this read; zero uses the max_chars budget.",
					},
					"max_chars": map[string]interface{}{
						"type":        "integer",
						"description": "Output budget in characters (default 4000, clamped to 256..20000).",
					},
					"include_status": map[string]interface{}{
						"type":        "boolean",
						"description": "Attach the subject's supervision row (default true). Set false when you only need the durable result.",
					},
				},
				"required": []string{"id"},
			},
		},
	}
}

// legacySupervisionToolNames exposes the retired-name set to the registry tests
// so the advertised and dispatchable surfaces are asserted against one source
// instead of a test-local copy that can go stale.
func legacySupervisionToolNames() map[string]bool {
	return retiredSupervisionToolNames
}

// parseSupervisionSnapshotArgs converts raw tool args into the typed input.
//
// after_seq and limit go through the shared JSON-number readers: the previous
// intToolValue folded every other spelling (a stringified cursor, uint,
// json.Number) into 0, which silently re-read the digest from the beginning
// instead of resuming the caller's cursor.
func parseSupervisionSnapshotArgs(args map[string]interface{}) (SupervisionSnapshotArgs, error) {
	parsed := SupervisionSnapshotArgs{}
	if value, ok, err := toolArgInt64(ToolSupervisionSnapshot, args, "after_seq"); err != nil {
		return parsed, err
	} else if ok {
		parsed.AfterSeq = value
	}
	if value, ok := args["include_resolved"].(bool); ok {
		parsed.IncludeResolved = value
	}
	if value, ok, err := brokerToolArgInt(ToolSupervisionSnapshot, args, "limit"); err != nil {
		return parsed, err
	} else if ok && value > 0 {
		parsed.Limit = value
	}
	return parsed, nil
}

// parseSupervisionDescendantsArgs converts raw tool args into the typed input.
// mode/health are closed vocabularies: an unknown spelling is rejected instead
// of silently widening the read to the whole scope (a typo must not turn a
// filtered inspection into a broader one).
func parseSupervisionDescendantsArgs(args map[string]interface{}) (SupervisionDescendantsArgs, error) {
	parsed := SupervisionDescendantsArgs{
		Mode:   strings.ToLower(supervisionArgValue(args, "mode")),
		Health: strings.ToLower(supervisionArgValue(args, "health")),
	}
	switch parsed.Mode {
	case "", "descendants", "children":
	default:
		return parsed, fmt.Errorf("mode must be children or descendants, got %q", parsed.Mode)
	}
	switch parsed.Health {
	case "", "any", "abnormal", "action_required":
	default:
		return parsed, fmt.Errorf("health must be any, abnormal or action_required, got %q", parsed.Health)
	}
	if value, ok := args["include_terminal"].(bool); ok {
		parsed.IncludeTerminal = value
	}
	if value, ok := args["include_results"].(bool); ok {
		parsed.IncludeResults = value
	}
	if value, ok, err := toolArgInt64(ToolSupervisionDescendants, args, "after_seq"); err != nil {
		return parsed, err
	} else if ok {
		parsed.AfterSeq = value
	}
	if value, ok, err := brokerToolArgInt(ToolSupervisionDescendants, args, "limit"); err != nil {
		return parsed, err
	} else if ok && value > 0 {
		parsed.Limit = value
	}
	return parsed, nil
}

// parseReadAgentResultArgs converts raw tool args into the typed input.
// sections is a closed vocabulary: an unknown value is rejected instead of
// silently widening the read to every section.
func parseReadAgentResultArgs(args map[string]interface{}) (ReadAgentResultArgs, error) {
	parsed := ReadAgentResultArgs{
		SessionID: supervisionArgValue(args, "id"),
		TaskID:    supervisionArgValue(args, "task_id"),
	}
	if parsed.SessionID == "" {
		return parsed, fmt.Errorf("id is required")
	}
	sections, err := readResultSectionsArg(args)
	if err != nil {
		return parsed, err
	}
	parsed.Sections = sections
	if value, ok, err := brokerToolArgInt(ToolReadAgentResult, args, "offset"); err != nil {
		return parsed, err
	} else if ok && value > 0 {
		parsed.Offset = value
	}
	if value, ok, err := brokerToolArgInt(ToolReadAgentResult, args, "limit"); err != nil {
		return parsed, err
	} else if ok && value > 0 {
		parsed.Limit = value
	}
	if value, ok, err := brokerToolArgInt(ToolReadAgentResult, args, "max_chars"); err != nil {
		return parsed, err
	} else if ok && value > 0 {
		parsed.MaxChars = value
	}
	return parsed, nil
}

// SubagentStatusArgs is the parsed input of subagent_status: the ledger matrix
// (same argument vocabulary as supervision_descendants, so the two names cannot
// drift apart into different filters) plus the optional lifecycle digest fold.
type SubagentStatusArgs struct {
	SupervisionDescendantsArgs
	// IncludeDigest folds the scoped lifecycle digest into the same answer
	// (the legacy supervision_snapshot payload). Default false: the matrix
	// payload stays byte-identical to before.
	IncludeDigest bool
	// IncludeResolved extends the folded digest with items resolved after
	// AfterSeq. Ignored when IncludeDigest is false.
	IncludeResolved bool
}

// parseSubagentStatusArgs converts raw tool args into the ledger-overview
// input. subagent_status shares supervision_descendants' argument vocabulary
// (same data plane, same closed vocabularies) so the two names cannot drift
// apart into different filters.
func parseSubagentStatusArgs(args map[string]interface{}) (SubagentStatusArgs, error) {
	matrix, err := parseSupervisionDescendantsArgs(args)
	if err != nil {
		return SubagentStatusArgs{}, err
	}
	parsed := SubagentStatusArgs{SupervisionDescendantsArgs: matrix}
	if value, ok := args["include_digest"].(bool); ok {
		parsed.IncludeDigest = value
	}
	if value, ok := args["include_resolved"].(bool); ok {
		parsed.IncludeResolved = value
	}
	return parsed, nil
}

// SubagentInspectTaskArgs is the parsed input of subagent_inspect_task: the
// bounded result read (same budget as read_agent_result) plus an optional
// subject-state attachment from the descendant matrix.
type SubagentInspectTaskArgs struct {
	ReadAgentResultArgs
	// IncludeStatus attaches the subject's supervision row (execution status,
	// supervision state, heartbeat/progress ages, allowed_actions). Default
	// true: the point of the deep look is "what is it doing now, and what did
	// it produce". A matrix read failure degrades to
	// status_source=matrix_unavailable instead of failing the observation.
	IncludeStatus bool
}

// parseSubagentInspectTaskArgs converts raw tool args into the typed input.
// It accepts the read_agent_result keys plus the session-handle aliases a
// caller may carry over from the sibling tools (session_id/target/agent).
func parseSubagentInspectTaskArgs(args map[string]interface{}) (SubagentInspectTaskArgs, error) {
	parsed := SubagentInspectTaskArgs{IncludeStatus: true}
	if value, ok := args["include_status"].(bool); ok {
		parsed.IncludeStatus = value
	}
	readArgs := args
	if supervisionArgValue(args, "id") == "" {
		for _, key := range []string{"session_id", "target", "agent", "child_session_id"} {
			value := supervisionArgValue(args, key)
			if value == "" {
				continue
			}
			readArgs = make(map[string]interface{}, len(args)+1)
			for existingKey, existingValue := range args {
				readArgs[existingKey] = existingValue
			}
			readArgs["id"] = value
			break
		}
	}
	read, err := parseReadAgentResultArgs(readArgs)
	if err != nil {
		return parsed, err
	}
	parsed.ReadAgentResultArgs = read
	return parsed, nil
}

// readResultSectionsArg reads the optional sections list, accepting a single
// string or a list of strings.
func readResultSectionsArg(args map[string]interface{}) ([]string, error) {
	raw, ok := args["sections"]
	if !ok || raw == nil {
		return nil, nil
	}
	values := make([]string, 0, 6)
	switch typed := raw.(type) {
	case string:
		values = append(values, typed)
	case []string:
		values = append(values, typed...)
	case []interface{}:
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("sections must be a list of strings")
			}
			values = append(values, text)
		}
	default:
		return nil, fmt.Errorf("sections must be a list of strings")
	}
	return supervision.NormalizeReadResultSections(values)
}

// parseAckLifecycleArgs converts raw tool args into the typed input.
func parseAckLifecycleArgs(args map[string]interface{}) (AckLifecycleArgs, error) {
	parsed := AckLifecycleArgs{
		NotificationID: supervisionArgValue(args, "notification_id"),
		Decision:       strings.ToLower(supervisionArgValue(args, "decision")),
		Note:           supervisionArgValue(args, "note"),
		Reason:         supervisionArgValue(args, "reason"),
		Resolution:     strings.ToLower(supervisionArgValue(args, "state")),
	}
	if parsed.NotificationID == "" {
		return parsed, fmt.Errorf("notification_id is required")
	}
	switch parsed.Decision {
	case "ack", "acknowledge":
		parsed.Decision = "acknowledge"
	case "defer":
	case "resolve":
	default:
		return parsed, fmt.Errorf("decision must be one of acknowledge|defer|resolve, got %q", parsed.Decision)
	}
	if raw := supervisionArgValue(args, "until"); raw != "" {
		until, err := parseSupervisionDeadline(raw)
		if err != nil {
			return parsed, err
		}
		parsed.Until = until
	}
	if version, ok, err := toolArgInt64(ToolAckLifecycle, args, "expected_version"); err != nil {
		return parsed, err
	} else if ok {
		parsed.ExpectedVersion = version
		parsed.HasExpectedVersion = true
	}
	return parsed, nil
}

// parseControlDescendantArgs converts raw tool args into the typed input.
func parseControlDescendantArgs(args map[string]interface{}) (ControlDescendantArgs, error) {
	parsed := ControlDescendantArgs{
		NotificationID: supervisionArgValue(args, "notification_id"),
		Action:         strings.ToLower(supervisionArgValue(args, "action")),
		Reason:         supervisionArgValue(args, "reason"),
		Cascade:        strings.ToLower(supervisionArgValue(args, "cascade")),
	}
	if parsed.NotificationID == "" {
		return parsed, fmt.Errorf("notification_id is required")
	}
	switch parsed.Action {
	case "cancel", "close", "cancel_subtree", "retry", "reassign", "inspect":
	default:
		return parsed, fmt.Errorf("unsupported action %q (want cancel|close|cancel_subtree|retry|reassign)", parsed.Action)
	}
	switch parsed.Cascade {
	case "", "target", "descendants":
	default:
		return parsed, fmt.Errorf("cascade must be target or descendants, got %q", parsed.Cascade)
	}
	if version, ok, err := toolArgInt64(ToolControlDescendant, args, "expected_version"); err != nil {
		return parsed, err
	} else if ok {
		parsed.ExpectedVersion = version
		parsed.HasExpectedVersion = true
	}
	return parsed, nil
}

// supervisionArgValue reads one optional string argument. A missing key or an
// explicit JSON null must read as empty: an absent deadline or state must not be
// parsed as a real value, and the placeholder text "<nil>" must never be
// rejected as if the caller had sent an unsupported deadline/state.
func supervisionArgValue(args map[string]interface{}, key string) string {
	raw, ok := args[key]
	if !ok || raw == nil {
		return ""
	}
	return strings.TrimSpace(stringValue(raw))
}

// parseSupervisionDeadline accepts an RFC3339 timestamp or a Go duration.
func parseSupervisionDeadline(raw string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		return parsed.UTC(), nil
	}
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("until %q is neither RFC3339 nor a duration such as 30m", raw)
	}
	return time.Now().UTC().Add(duration), nil
}

// supervisionNotificationPayload renders a compact, model-facing view of a
// decided notification row.
func supervisionNotificationPayload(n *supervision.Notification) map[string]interface{} {
	if n == nil {
		return nil
	}
	payload := map[string]interface{}{
		"notification_id":   n.NotificationID,
		"subject_kind":      string(n.SubjectKind),
		"subject_id":        n.SubjectID,
		"supervision_state": string(n.SupervisionState),
		"decision_state":    string(n.DecisionState),
		"resolution_state":  string(n.ResolutionState),
		"version":           n.Version,
	}
	if n.DeferUntil != nil {
		payload["defer_until"] = n.DeferUntil.UTC().Format(time.RFC3339)
	}
	return payload
}

// supervisionDecisionNextAction tells the model what the decision means for the
// next turn, so it does not keep re-reading rows it already handled.
func supervisionDecisionNextAction(n *supervision.Notification) string {
	if n == nil {
		return ""
	}
	switch n.DecisionState {
	case supervision.DecisionAcknowledged:
		return "handled: the row leaves critical_unresolved and is not re-injected"
	case supervision.DecisionDeferred:
		return "deferred: the row is hidden until the deadline, then re-enters automatically"
	default:
		if n.ResolutionState != "" && n.ResolutionState != supervision.ResolutionUnresolved {
			return "resolved: the row is converged (no further action expected)"
		}
		return "state unchanged"
	}
}

// supervisionDescendantsSummary renders the one-line business readout of a
// descendant matrix: how many rows exist and how they are distributed, plus the
// single next step when the matrix asks for one. It stays compact because it is
// the summary the model sees when the full row payload is truncated.
func supervisionDescendantsSummary(snapshot *supervision.Snapshot) string {
	if snapshot == nil {
		return "no supervision descendants in this scope"
	}
	parts := make([]string, 0, 8)
	for _, entry := range []struct {
		label string
		count int
	}{
		{"running", snapshot.Summary.Running},
		{"blocked", snapshot.Summary.Blocked},
		{"stalled", snapshot.Summary.Stalled},
		{"timed_out", snapshot.Summary.TimedOut},
		{"orphaned", snapshot.Summary.Orphaned},
		{"invalid", snapshot.Summary.Invalid},
		{"canceling", snapshot.Summary.Canceling},
		{"terminal_unacknowledged", snapshot.Summary.TerminalUnacknowledged},
	} {
		if entry.count > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", entry.label, entry.count))
		}
	}
	state := "no live descendants"
	if len(parts) > 0 {
		state = strings.Join(parts, " ")
	}
	summary := fmt.Sprintf("descendant matrix: %d row(s), %s", len(snapshot.Descendants), state)
	if snapshot.Truncated {
		summary += fmt.Sprintf(" (truncated; next_seq=%d)", snapshot.NextSeq)
	}
	if action := supervisionDescendantsNextAction(snapshot); action != "" {
		summary += "; " + action
	}
	return summary
}

// supervisionDescendantsNextAction states the single next step for a matrix,
// empty when the rows need no decision.
func supervisionDescendantsNextAction(snapshot *supervision.Snapshot) string {
	return supervisionDescendantsNextActionExcluding(snapshot, nil)
}

// supervisionDescendantsNextActionExcluding is the stale-aware variant: subject
// keys in `stale` (digest verdict: absent from the control plane) must not drive
// "go decide" instructions, because the model has no allowed action to take on
// them (2026-09-26 真机：digest 说 0 action_required，矩阵行却让模型去裁决)。
func supervisionDescendantsNextActionExcluding(snapshot *supervision.Snapshot, stale map[string]bool) string {
	if snapshot == nil {
		return ""
	}
	// 2026-09-26 真机：Summary.ActionRequired 会把"subject 已不在控制面"的
	// 陈旧行（AllowedActions 为空、模型无从下手）也算进去，于是模型收到
	// "decide the action_required rows"的指令却找不到可裁决的行（与 digest 的
	// 0 action_required 自相矛盾）。只有真正带动作的行才值得让模型去裁决。
	if supervisionDescendantsActionableRows(snapshot, stale) > 0 {
		return "decide the action_required rows (notification_id + allowed_actions) with " + ToolControlDescendant + " or " + ToolAckLifecycle
	}
	if snapshot.Summary.TerminalUnacknowledged > 0 {
		return "report the finished rows to the user, then " + ToolAckLifecycle + " or close_agent them to converge the lifecycle"
	}
	if snapshot.Summary.Running+snapshot.Summary.Blocked > 0 {
		return "children still running: continue independent work; re-read this matrix instead of polling wait_agent"
	}
	return ""
}

// supervisionDescendantsActionableRows counts rows the model can actually act on:
// action-required rows that still expose allowed actions. Stale subjects (no live
// row, no allowed actions) must not produce a "go decide something" instruction.
func supervisionDescendantsActionableRows(snapshot *supervision.Snapshot, stale map[string]bool) int {
	if snapshot == nil {
		return 0
	}
	count := 0
	for _, row := range snapshot.Descendants {
		if row.ActionRequired && len(row.AllowedActions) > 0 && !stale[supervisionSubjectKey(row.Kind, row.ID)] {
			count++
		}
	}
	return count
}

// supervisionSubjectKey mirrors the snapshot/digest join key (kind|id).
func supervisionSubjectKey(kind supervision.SubjectKind, id string) string {
	return string(kind) + "|" + strings.TrimSpace(id)
}

// staleSubjectKeys maps a digest's stale rows to matrix row keys so the matrix
// guidance can exclude exactly the subjects the digest already wrote off.
func staleSubjectKeys(digest *supervision.Digest) map[string]bool {
	if digest == nil {
		return nil
	}
	keys := make(map[string]bool, len(digest.Items))
	for _, item := range digest.Items {
		if !item.Stale {
			continue
		}
		keys[supervisionSubjectKey(item.SubjectKind, item.SubjectID)] = true
	}
	return keys
}

// readAgentResultSummary renders the one-line cache-safe summary of a
// read_agent_result payload: how much was returned and what to do when nothing
// durable was recorded.
func readAgentResultSummary(payload supervision.ReadResultPayload) string {
	if payload.Source == supervision.ResultSourceNone {
		summary := "no durable result recorded"
		if payload.ErrorCode != "" {
			summary += " (" + payload.ErrorCode + ")"
		}
		if payload.NextAction != "" {
			summary += "; " + payload.NextAction
		}
		return summary
	}
	parts := []string{fmt.Sprintf("result source=%s", payload.Source)}
	if payload.Status != "" {
		parts = append(parts, "status="+payload.Status)
	}
	counts := make([]string, 0, 4)
	for _, entry := range []struct {
		label string
		count int
	}{
		{"findings", len(payload.Findings)},
		{"changes", len(payload.Changes)},
		{"artifacts", len(payload.Artifacts)},
		{"errors", len(payload.Errors)},
	} {
		if entry.count > 0 {
			counts = append(counts, fmt.Sprintf("%s=%d", entry.label, entry.count))
		}
	}
	if len(counts) > 0 {
		parts = append(parts, strings.Join(counts, " "))
	}
	if payload.Truncated {
		parts = append(parts, "truncated=true")
	}
	return strings.Join(parts, "; ")
}

// subagentStatusPayload shapes the ledger overview (plan §C3-1): the
// descendant matrix plus the rollup a parent needs to decide whether it may
// finalize. It is a read model over the existing snapshot - no new storage.
//
// Contract notes (plan AC-P2-1e):
//   - rows keep the SnapshotItem shape, so allowed_actions[] stays per row; no
//     top-level union is invented, because a union would hide which row may do
//     what and control_descendant re-validates per row anyway.
//   - terminal_delta[] lists the rows that reached terminal *since the
//     caller's after_seq cursor* - the same cursor interpretation the data
//     plane already uses for terminal_unacknowledged.
//   - digest_ref is not emitted: this layer has no artifact archiver, and a
//     fabricated reference would be worse than its absence (the caller pages
//     the same read with limit/after_seq instead).
func subagentStatusPayload(snapshot *supervision.Snapshot, afterSeq int64) map[string]interface{} {
	if snapshot == nil {
		return map[string]interface{}{"next_action": "finalize"}
	}
	rows := snapshot.Descendants
	if rows == nil {
		rows = []supervision.SnapshotItem{}
	}
	terminalDelta := make([]string, 0, 4)
	terminalCount := 0
	pendingCount := 0
	for _, row := range rows {
		if !isTerminalSupervisionState(row.SupervisionState) {
			pendingCount++
			continue
		}
		terminalCount++
		if row.LastChangeSeq > afterSeq {
			terminalDelta = append(terminalDelta, firstNonEmptyToolValue(row.ID, row.NotificationID))
		}
	}
	payload := map[string]interface{}{
		"scope":          snapshot.Scope,
		"generated_at":   snapshot.GeneratedAt,
		"snapshot_seq":   snapshot.SnapshotSeq,
		"next_seq":       snapshot.NextSeq,
		"summary":        snapshot.Summary,
		"rows":           rows,
		"pending_count":  pendingCount,
		"terminal_count": terminalCount,
		"terminal_delta": terminalDelta,
		"truncated":      snapshot.Truncated,
	}
	payload["next_action"] = subagentStatusNextAction(snapshot, pendingCount)
	return payload
}

// attachSubagentStatusDigest folds the scoped lifecycle digest into the ledger
// payload - the same read the legacy supervision_snapshot name returns. It is
// deliberately fail-open: a digest read failure is reported as digest_error
// next to the intact matrix, so a supervision-store hiccup can never hide the
// rows or fake a finalize (§13.8 - the matrix half stays authoritative for
// pending work, the digest half only adds attention items).
func attachSubagentStatusDigest(ctx context.Context, controller AgentSupervisionController, sessionID string, request SubagentStatusArgs, payload map[string]interface{}) {
	digest, err := controller.SupervisionSnapshot(ctx, sessionID, SupervisionSnapshotArgs{
		AfterSeq:        request.AfterSeq,
		IncludeResolved: request.IncludeResolved,
		Limit:           request.Limit,
	})
	if err != nil {
		payload["digest_error"] = err.Error()
		return
	}
	if digest == nil {
		digest = &supervision.Digest{}
	}
	payload["digest"] = digest
	payload["digest_next_action"] = digestNextAction(digest)
}

// digestNextAction mirrors the guidance the standalone digest tool used to
// render, so a caller that folded the digest still learns what to do with it.
func digestNextAction(digest *supervision.Digest) string {
	if digest != nil && (digest.CriticalUnresolved > 0 || digest.ActionRequired > 0) {
		return "decide the listed notification_id rows with " + ToolAckLifecycle + " (acknowledge/defer/resolve), then continue the task"
	}
	return "no unresolved supervision items for this session"
}

// subagentStatusNextAction is the ledger-driven next step. An empty ledger
// (nothing pending, nothing to decide) returns finalize immediately so the
// caller does not wait on work that does not exist (AC-P2-1c). Every other
// state reuses the shared matrix guidance; finalize is withheld whenever any
// row may still be in flight, which is the conservative direction of the
// fail-open rule (§13.8): never claim a parent may finalize while work may be
// pending.
func subagentStatusNextAction(snapshot *supervision.Snapshot, pendingCount int) string {
	if action := supervisionDescendantsNextAction(snapshot); action != "" {
		return action
	}
	if snapshot == nil {
		return "finalize"
	}
	summary := snapshot.Summary
	if pendingCount == 0 &&
		summary.Stalled == 0 && summary.TimedOut == 0 && summary.Orphaned == 0 &&
		summary.Invalid == 0 && summary.Canceling == 0 &&
		summary.ActionRequired == 0 && summary.TerminalUnacknowledged == 0 {
		return "finalize"
	}
	return ""
}

// isTerminalSupervisionState reports whether a matrix row reached the terminal
// supervision state. The vocabulary is the data plane's own: the only state
// internal/supervision/snapshot.go treats as end-of-lifecycle (and counts as
// terminal_unacknowledged) is SupervisionTerminated - there is no "terminal"
// literal in the state set. Every other state, including one this build does
// not know, is read as pending: that is the conservative direction for the
// finalize gate, which must never claim the parent may finalize while work may
// still be in flight (§13.8).
func isTerminalSupervisionState(state supervision.SupervisionState) bool {
	return state == supervision.SupervisionTerminated
}

// subagentStatusSummary renders the one-line cache-safe summary of the ledger
// overview: row/count rollup plus the next step, so a cached or compacted
// result cannot hide pending work.
func subagentStatusSummary(snapshot *supervision.Snapshot, payload map[string]interface{}) string {
	if snapshot == nil {
		return "subagent status: empty ledger; next_action=finalize"
	}
	pending, _ := payload["pending_count"].(int)
	terminal, _ := payload["terminal_count"].(int)
	summary := fmt.Sprintf("subagent status: %d row(s), %d pending, %d terminal", len(snapshot.Descendants), pending, terminal)
	if delta, ok := payload["terminal_delta"].([]string); ok && len(delta) > 0 {
		shown := delta
		suffix := ""
		if len(shown) > 8 {
			shown = shown[:8]
			suffix = fmt.Sprintf(" (+%d more)", len(delta)-8)
		}
		summary += "; finished since cursor: " + strings.Join(shown, ", ") + suffix
	}
	if snapshot.Truncated {
		summary += fmt.Sprintf(" (truncated; next_seq=%d)", snapshot.NextSeq)
	}
	if digest, ok := payload["digest"].(*supervision.Digest); ok && digest != nil {
		summary += fmt.Sprintf("; digest: %d critical_unresolved, %d action_required", digest.CriticalUnresolved, digest.ActionRequired)
	} else if _, failed := payload["digest_error"]; failed {
		summary += "; digest unavailable"
	}
	if action, _ := payload["next_action"].(string); action != "" {
		summary += "; next_action=" + action
	}
	return summary
}

// subagentInspectTaskPayload composes the bounded deep look (plan §C3-1): the
// subject's matrix row (what it is doing now) plus the durable bounded result
// (what it produced). Both halves are bounded by existing budgets - the result
// by read_agent_result's max_chars, the row by the snapshot row contract - so
// this composition cannot produce an unbounded payload.
//
// The matrix half is fail-open: a failed status read degrades to
// status_source=matrix_unavailable with the reason, because an observation
// failure must never turn a readable result into a tool failure (AC-P2-1d).
func subagentInspectTaskPayload(payload supervision.ReadResultPayload, row *supervision.SnapshotItem, statusErr error) map[string]interface{} {
	out := map[string]interface{}{
		"result": payload,
	}
	switch {
	case statusErr != nil:
		out["status_source"] = "matrix_unavailable"
		out["status_error"] = statusErr.Error()
	case row != nil:
		out["subject"] = *row
		out["status_source"] = "matrix"
	default:
		out["status_source"] = "matrix_no_row"
	}
	if action := strings.TrimSpace(payload.NextAction); action != "" {
		out["next_action"] = action
	}
	return out
}

// subagentInspectTaskSummary renders the one-line cache-safe summary of a deep
// look: the bounded result plus the subject-state source.
func subagentInspectTaskSummary(payload map[string]interface{}, read supervision.ReadResultPayload) string {
	summary := "inspect task: " + readAgentResultSummary(read)
	if source, ok := payload["status_source"].(string); ok && source != "matrix" {
		summary += "; subject state " + source
	}
	return summary
}

// subagentInspectTaskRow reads the subject's row from the same scoped matrix
// the status tool uses (terminal rows included, so a finished subject still
// reports its last state). A matrix failure is returned so the caller can
// degrade to an observation note instead of failing the deep look (AC-P2-1d).
func (b *Broker) subagentInspectTaskRow(ctx context.Context, sessionID string, read ReadAgentResultArgs) (*supervision.SnapshotItem, error) {
	if b.Supervision == nil {
		return nil, nil
	}
	snapshot, err := b.Supervision.SupervisionDescendants(ctx, sessionID, SupervisionDescendantsArgs{
		Mode:            "descendants",
		IncludeTerminal: true,
	})
	if err != nil {
		return nil, err
	}
	if snapshot == nil {
		return nil, nil
	}
	targets := []string{strings.TrimSpace(read.SessionID), strings.TrimSpace(read.TaskID)}
	for index := range snapshot.Descendants {
		row := snapshot.Descendants[index]
		for _, target := range targets {
			if target == "" {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(row.ID), target) {
				return &row, nil
			}
		}
	}
	return nil, nil
}

// executeSupervisionTool handles the control-plane tool family. The dispatcher
// stays thin on purpose: scoping, allowed_actions re-validation and CAS all
// live in the host implementation (LocalControlService), so the model can never
// talk the broker into a different policy.
func (b *Broker) executeSupervisionTool(ctx context.Context, toolName, sessionID string, args map[string]interface{}) (interface{}, map[string]interface{}, error) {
	if b == nil || b.Supervision == nil {
		return nil, nil, fmt.Errorf("supervision controller is not configured")
	}
	switch normalizeToolName(toolName) {
	case ToolSupervisionSnapshot:
		request, err := parseSupervisionSnapshotArgs(args)
		if err != nil {
			return nil, nil, err
		}
		digest, err := b.Supervision.SupervisionSnapshot(ctx, sessionID, request)
		if err != nil {
			return nil, nil, err
		}
		if digest == nil {
			digest = &supervision.Digest{}
		}
		nextAction := "no unresolved supervision items for this session"
		if digest.CriticalUnresolved > 0 || digest.ActionRequired > 0 {
			nextAction = "decide the listed notification_id rows with " + ToolAckLifecycle + " (acknowledge/defer/resolve), then continue the task"
		}
		return digest, attachCacheSafeSummary(map[string]interface{}{
			"critical_unresolved": digest.CriticalUnresolved,
			"action_required":     digest.ActionRequired,
			"stale_subjects":      digest.StaleSubjects,
			"truncated":           digest.Truncated,
			"item_count":          len(digest.Items),
			"next_seq":            digest.NextSeq,
			"next_action":         nextAction,
		}, digest.Text), nil

	case ToolSupervisionDescendants:
		request, err := parseSupervisionDescendantsArgs(args)
		if err != nil {
			return nil, nil, err
		}
		snapshot, err := b.Supervision.SupervisionDescendants(ctx, sessionID, request)
		if err != nil {
			return nil, nil, err
		}
		if snapshot == nil {
			snapshot = &supervision.Snapshot{}
		}
		return snapshot, attachCacheSafeSummary(map[string]interface{}{
			"row_count":        len(snapshot.Descendants),
			"running":          snapshot.Summary.Running,
			"blocked":          snapshot.Summary.Blocked,
			"stalled":          snapshot.Summary.Stalled,
			"timed_out":        snapshot.Summary.TimedOut,
			"orphaned":         snapshot.Summary.Orphaned,
			"invalid":          snapshot.Summary.Invalid,
			"canceling":        snapshot.Summary.Canceling,
			"terminal_unacked": snapshot.Summary.TerminalUnacknowledged,
			"action_required":  snapshot.Summary.ActionRequired,
			"truncated":        snapshot.Truncated,
			"next_seq":         snapshot.NextSeq,
			"next_action":      supervisionDescendantsNextAction(snapshot),
		}, supervisionDescendantsSummary(snapshot)), nil

	case ToolSubagentStatus:
		request, err := parseSubagentStatusArgs(args)
		if err != nil {
			return nil, nil, err
		}
		snapshot, err := b.Supervision.SupervisionDescendants(ctx, sessionID, request.SupervisionDescendantsArgs)
		if err != nil {
			return nil, nil, err
		}
		payload := subagentStatusPayload(snapshot, request.AfterSeq)
		if request.IncludeDigest {
			attachSubagentStatusDigest(ctx, b.Supervision, sessionID, request, payload)
			// digest 是 stale 规则的权威（2026-09-26 真机）：矩阵行里"subject 已不在
			// 控制面"的陈旧行不能再驱动"去裁决 action_required 行"的指令，否则
			// cache_safe_summary 会与它自己的 digest 计数（0 action_required）矛盾，
			// 模型也会去找一个它无从下手的行。无可用指引时回落到 digest 的下一步。
			if digest, ok := payload["digest"].(*supervision.Digest); ok && digest != nil {
				if next := supervisionDescendantsNextActionExcluding(snapshot, staleSubjectKeys(digest)); next != "" {
					payload["next_action"] = next
				} else if next, _ := payload["digest_next_action"].(string); strings.TrimSpace(next) != "" {
					payload["next_action"] = strings.TrimSpace(next)
				}
			}
		}
		return payload, attachCacheSafeSummary(payload, subagentStatusSummary(snapshot, payload)), nil

	case ToolReadAgentResult:
		request, err := parseReadAgentResultArgs(args)
		if err != nil {
			return nil, nil, err
		}
		payload, err := b.Supervision.ReadAgentResult(ctx, sessionID, request)
		if err != nil {
			return nil, nil, err
		}
		return payload, attachCacheSafeSummary(map[string]interface{}{
			"source":      payload.Source,
			"status":      payload.Status,
			"truncated":   payload.Truncated,
			"error_code":  payload.ErrorCode,
			"next_action": payload.NextAction,
		}, readAgentResultSummary(payload)), nil

	case ToolSubagentInspectTask:
		request, err := parseSubagentInspectTaskArgs(args)
		if err != nil {
			return nil, nil, err
		}
		read, err := b.Supervision.ReadAgentResult(ctx, sessionID, request.ReadAgentResultArgs)
		if err != nil {
			return nil, nil, err
		}
		var row *supervision.SnapshotItem
		var statusErr error
		if request.IncludeStatus {
			row, statusErr = b.subagentInspectTaskRow(ctx, sessionID, request.ReadAgentResultArgs)
		}
		payload := subagentInspectTaskPayload(read, row, statusErr)
		return payload, attachCacheSafeSummary(payload, subagentInspectTaskSummary(payload, read)), nil

	case ToolAckLifecycle:
		request, err := parseAckLifecycleArgs(args)
		if err != nil {
			return nil, nil, err
		}
		notification, err := b.Supervision.AckLifecycle(ctx, sessionID, request)
		if err != nil {
			return nil, nil, err
		}
		if notification == nil {
			return nil, nil, fmt.Errorf("supervision notification %s not found", request.NotificationID)
		}
		payload := supervisionNotificationPayload(notification)
		return payload, attachCacheSafeSummary(payload, fmt.Sprintf(
			"notification %s decision=%s resolution=%s version=%d; %s",
			notification.NotificationID, notification.DecisionState, notification.ResolutionState, notification.Version,
			supervisionDecisionNextAction(notification))), nil

	case ToolControlDescendant:
		request, err := parseControlDescendantArgs(args)
		if err != nil {
			return nil, nil, err
		}
		record, err := b.Supervision.ControlDescendant(ctx, sessionID, request)
		if err != nil {
			return nil, nil, err
		}
		payload := map[string]interface{}{
			"action_id":   record.ActionID,
			"action":      string(record.Action),
			"status":      string(record.Status),
			"target_kind": string(record.TargetKind),
			"target_id":   record.TargetID,
		}
		if result := strings.TrimSpace(record.Result); result != "" {
			payload["result"] = result
		}
		return payload, attachCacheSafeSummary(payload, fmt.Sprintf(
			"action %s on %s/%s status=%s; re-read subagent_status(include_digest=true) to confirm the row converged",
			record.ActionID, record.TargetKind, record.TargetID, record.Status)), nil
	}
	return nil, nil, fmt.Errorf("unsupported supervision tool %q", toolName)
}
