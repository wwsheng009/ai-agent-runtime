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

// supervisionToolDefinitions returns the five control-plane tool definitions.
// Callers gate on the host capability before appending them.
func supervisionToolDefinitions() []types.ToolDefinition {
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
						"description": "Optional subset of summary | findings | changes | artifacts | errors | usage (default: all). An unknown section is rejected instead of widening the read.",
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
				"notification_id must be copied verbatim from supervision_snapshot or supervision_descendants output - never invented or synthesized. Pass expected_version from the snapshot; on a version conflict re-read the snapshot instead of retrying blindly.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"notification_id": map[string]interface{}{
						"type":        "string",
						"description": "Notification id copied verbatim from supervision_snapshot or supervision_descendants; do not invent or synthesize ids.",
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
						"description": "Optional optimistic-concurrency guard taken from the snapshot.",
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
						"description": "Notification id whose subject should be controlled; copy it verbatim from supervision_snapshot or supervision_descendants (never invent it).",
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
						"description": "Optional optimistic-concurrency guard taken from the snapshot.",
					},
				},
				"required": []string{"notification_id", "action", "reason"},
			},
		},
	}
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
	if value, ok, err := brokerToolArgInt(ToolReadAgentResult, args, "max_chars"); err != nil {
		return parsed, err
	} else if ok && value > 0 {
		parsed.MaxChars = value
	}
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
	if snapshot == nil {
		return ""
	}
	if snapshot.Summary.ActionRequired > 0 {
		return "decide the action_required rows (notification_id + allowed_actions) with control_descendant or ack_lifecycle"
	}
	if snapshot.Summary.TerminalUnacknowledged > 0 {
		return "report the finished rows to the user, then ack_lifecycle or close them to converge the lifecycle"
	}
	if snapshot.Summary.Running+snapshot.Summary.Blocked > 0 {
		return "children still running: continue independent work; re-read this matrix instead of polling wait_agent"
	}
	return ""
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

// executeSupervisionTool handles the four control-plane tools. The dispatcher
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
			nextAction = "decide the listed notification_id rows with ack_lifecycle (acknowledge/defer/resolve), then continue the task"
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
			"action %s on %s/%s status=%s; re-read supervision_snapshot to confirm the row converged",
			record.ActionID, record.TargetKind, record.TargetID, record.Status)), nil
	}
	return nil, nil, fmt.Errorf("unsupported supervision tool %q", toolName)
}
