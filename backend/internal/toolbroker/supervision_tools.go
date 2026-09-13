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
// supervision_snapshot / ack_lifecycle / control_descendant tools. Implementations
// must derive the caller's root scope from parentSessionID and enforce that a
// notification outside that scope is rejected.
type AgentSupervisionController interface {
	// SupervisionSnapshot returns the scoped preflight digest (read-only).
	SupervisionSnapshot(ctx context.Context, parentSessionID string, args SupervisionSnapshotArgs) (*supervision.Digest, error)
	// AckLifecycle applies one decision (acknowledge / defer / resolve).
	AckLifecycle(ctx context.Context, parentSessionID string, args AckLifecycleArgs) (*supervision.Notification, error)
	// ControlDescendant requests/executes a durable control action against the
	// notification subject (cancel / close / retry / reassign).
	ControlDescendant(ctx context.Context, parentSessionID string, args ControlDescendantArgs) (supervision.ActionRecord, error)
}

// SupervisionSnapshotArgs is the parsed input of supervision_snapshot.
type SupervisionSnapshotArgs struct {
	AfterSeq        int64
	IncludeResolved bool
	Limit           int
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

// supervisionToolDefinitions returns the three control-plane tool definitions.
// Callers gate on the host capability before appending them.
func supervisionToolDefinitions() []types.ToolDefinition {
	return []types.ToolDefinition{
		{
			Name:        ToolSupervisionSnapshot,
			Description: "Read the scoped supervision digest for this session (read-only). Returns critical_unresolved / action_required counts, the lifecycle items with their notification_id + version, and next_seq for incremental reads. Rows whose subject no longer exists are reported as stale and need no action. Call this before ack_lifecycle whenever a version conflict is reported.",
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
						"description": "Cap the number of returned items; unresolved critical rows stay prioritized.",
					},
				},
			},
		},
		{
			Name:        ToolAckLifecycle,
			Description: "Decide one supervision notification so it stops being re-injected: decision=acknowledge (accept the handled risk; note required), decision=defer (postpone until an RFC3339 deadline or Go duration such as 30m; reason required), decision=resolve (set the terminal resolution state closed|recovered|failed). Only notifications inside this session's own scope can be decided. Pass expected_version from supervision_snapshot; on a version conflict re-read the snapshot instead of retrying blindly.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"notification_id": map[string]interface{}{
						"type":        "string",
						"description": "Notification id from supervision_snapshot.",
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
			Name:        ToolControlDescendant,
			Description: "Execute a durable control action against the subject of a supervision notification: action=cancel|close|cancel_subtree|retry|reassign. The subject is taken from the notification, so only rows inside this session's own scope can be controlled. reason is required (durable audit), expected_version is optional. The action is persisted before execution; the returned action_id can be re-read even when execution fails.",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"notification_id": map[string]interface{}{
						"type":        "string",
						"description": "Notification id whose subject should be controlled.",
					},
					"action": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"cancel", "close", "cancel_subtree", "retry", "reassign"},
						"description": "Required control action; validated against the notification's server-computed allowed actions.",
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
func parseSupervisionSnapshotArgs(args map[string]interface{}) SupervisionSnapshotArgs {
	parsed := SupervisionSnapshotArgs{}
	parsed.AfterSeq = int64(intToolValue(args["after_seq"]))
	if value, ok := args["include_resolved"].(bool); ok {
		parsed.IncludeResolved = value
	}
	if value := intToolValue(args["limit"]); value > 0 {
		parsed.Limit = value
	}
	return parsed
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
	if value, ok := args["expected_version"]; ok {
		if version, err := int64ToolValue(value); err == nil {
			parsed.ExpectedVersion = version
			parsed.HasExpectedVersion = true
		} else {
			return parsed, fmt.Errorf("expected_version: %w", err)
		}
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
	if value, ok := args["expected_version"]; ok {
		version, err := int64ToolValue(value)
		if err != nil {
			return parsed, fmt.Errorf("expected_version: %w", err)
		}
		parsed.ExpectedVersion = version
		parsed.HasExpectedVersion = true
	}
	return parsed, nil
}

// supervisionArgValue reads one optional string argument. A missing key or an
// explicit JSON null must read as empty: the generic stringValue helper renders
// nil as the literal "<nil>", which would otherwise be parsed as a real
// deadline/state and reject an argument the caller never sent.
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

func intToolValue(value interface{}) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	default:
		return 0
	}
}

func int64ToolValue(value interface{}) (int64, error) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), nil
	case int:
		return int64(typed), nil
	case int64:
		return typed, nil
	default:
		return 0, fmt.Errorf("expected an integer, got %T", value)
	}
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

// executeSupervisionTool handles the three control-plane tools. The dispatcher
// stays thin on purpose: scoping, allowed_actions re-validation and CAS all
// live in the host implementation (LocalControlService), so the model can never
// talk the broker into a different policy.
func (b *Broker) executeSupervisionTool(ctx context.Context, toolName, sessionID string, args map[string]interface{}) (interface{}, map[string]interface{}, error) {
	if b == nil || b.Supervision == nil {
		return nil, nil, fmt.Errorf("supervision controller is not configured")
	}
	switch normalizeToolName(toolName) {
	case ToolSupervisionSnapshot:
		request := parseSupervisionSnapshotArgs(args)
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
