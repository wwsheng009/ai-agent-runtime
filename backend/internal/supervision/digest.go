package supervision

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// DigestRequest describes what the preflight digest should include.
type DigestRequest struct {
	RootScopeID           string
	TargetParentSessionID string
	TargetParentTeamID    string
	// AfterSeq is the last lifecycle sequence already seen by the parent.
	AfterSeq int64
	// Limit caps the number of items injected (unresolved_digest_limit).
	Limit int
	// IncludeResolvedSince injects a compact list of items resolved after
	// AfterSeq when true.
	IncludeResolvedSince bool
}

// DigestItem is one row of the preflight lifecycle digest (doc 6.4).
type DigestItem struct {
	SubjectKind       SubjectKind
	SubjectID         string
	SupervisionState  SupervisionState
	Reason            string
	RecommendedAction string
	AllowedActions    []string
	AutoActionID      string
	ActionRequired    bool
	NotificationID    string
	EventSeq          int64
	Resolved          bool
	ResolutionState   ResolutionState
}

// Digest is the deterministic, budget-limited preflight payload (doc 6.4).
type Digest struct {
	SnapshotSeq           int64        `json:"snapshot_seq,omitempty"`
	CriticalUnresolved    int          `json:"critical_unresolved,omitempty"`
	ActionRequired        int          `json:"action_required,omitempty"`
	AutoActionsInProgress int          `json:"auto_actions_in_progress,omitempty"`
	ResolvedSinceLastTurn int          `json:"resolved_since_last_turn,omitempty"`
	Truncated             bool         `json:"truncated,omitempty"`
	Items                 []DigestItem `json:"items,omitempty"`
	// NextSeq is the cursor for supervision_snapshot(after_seq=...).
	NextSeq int64 `json:"next_seq,omitempty"`
	// Full text block injected into the parent turn.
	Text string `json:"text,omitempty"`
}

// BuildDigest assembles the preflight lifecycle digest for a root scope.
// Aggregation guarantees: multiple children failing at once produce a single
// digest (doc 6.4), and unresolved critical items are always prioritized over
// the context budget (rule 6).
func BuildDigest(ctx context.Context, store Store, req DigestRequest) (*Digest, error) {
	if store == nil {
		return nil, fmt.Errorf("supervision store is required")
	}
	filter := NotificationFilter{
		RootScopeID:           req.RootScopeID,
		TargetParentSessionID: req.TargetParentSessionID,
		TargetParentTeamID:    req.TargetParentTeamID,
		AfterSeq:              req.AfterSeq,
		IncludeResolved:       true,
	}
	notifications, err := store.ListNotifications(ctx, filter)
	if err != nil {
		return nil, err
	}
	evaluator := Evaluator{}
	digest := &Digest{
		SnapshotSeq: req.AfterSeq,
		NextSeq:     req.AfterSeq,
	}
	var items []DigestItem
	now := time.Now()
	for _, n := range notifications {
		// A deferred item is deliberately out of the ordinary preflight view
		// until its due time. It remains durable and re-enters automatically
		// once the deadline passes; seen never becomes acknowledged merely
		// because it was previously injected.
		if n.DecisionState == DecisionDeferred && n.DeferUntil != nil && now.Before(*n.DeferUntil) {
			continue
		}
		item := DigestItem{
			SubjectKind:       n.SubjectKind,
			SubjectID:         n.SubjectID,
			SupervisionState:  n.SupervisionState,
			Reason:            n.Reason,
			RecommendedAction: evaluator.EvaluateRecommendedAction(n),
			AllowedActions:    evaluator.EvaluateAllowedActions(n),
			AutoActionID:      n.AutoActionID,
			ActionRequired:    n.ActionRequired(),
			NotificationID:    n.NotificationID,
			EventSeq:          n.EventSeq,
			Resolved:          n.ResolutionState != "" && n.ResolutionState != ResolutionUnresolved,
			ResolutionState:   n.ResolutionState,
		}
		if item.EventSeq > digest.SnapshotSeq {
			digest.SnapshotSeq = item.EventSeq
		}
		if item.EventSeq > digest.NextSeq {
			digest.NextSeq = item.EventSeq
		}
		if n.Unresolved() && (n.Severity == SeverityCritical || n.Severity == SeverityWarning) {
			digest.CriticalUnresolved++
		}
		if item.ActionRequired {
			digest.ActionRequired++
		}
		if n.AutoActionID != "" && (n.DecisionState == DecisionUnacknowledged || n.DecisionState == DecisionDeferred) && n.ResolutionState == ResolutionUnresolved {
			digest.AutoActionsInProgress++
		}
		if item.Resolved && req.IncludeResolvedSince {
			digest.ResolvedSinceLastTurn++
		}
		// Acknowledgement is the parent's durable decision that no further
		// attention is required. It is intentionally distinct from delivery:
		// seen unresolved critical notifications keep reappearing, while an
		// acknowledged notification must leave ordinary preflight injection
		// even if its underlying execution has no recovery resolution yet.
		if n.DecisionState == DecisionAcknowledged {
			continue
		}
		if !item.Resolved || req.IncludeResolvedSince {
			items = append(items, item)
		}
	}

	// Prioritize: action-required unresolved first, then unresolved, then
	// resolved; stable by event_seq.
	sort.SliceStable(items, func(i, j int) bool {
		pi := itemPriority(items[i])
		pj := itemPriority(items[j])
		if pi != pj {
			return pi < pj
		}
		return items[i].EventSeq < items[j].EventSeq
	})

	limit := req.Limit
	if limit <= 0 {
		limit = defaultDigestLimit
	}
	if len(items) > limit {
		items = items[:limit]
		digest.Truncated = true
	}
	digest.Items = items
	digest.Text = formatDigestText(digest)
	return digest, nil
}

func itemPriority(item DigestItem) int {
	switch {
	case item.Resolved:
		return 3
	case item.ActionRequired:
		return 0
	default:
		return 1
	}
}

const defaultDigestLimit = 50

func formatDigestText(digest *Digest) string {
	var b strings.Builder
	b.WriteString("[Child lifecycle preflight]\n")
	fmt.Fprintf(&b, "snapshot_seq: %d\n", digest.SnapshotSeq)
	fmt.Fprintf(&b, "critical_unresolved: %d\n", digest.CriticalUnresolved)
	fmt.Fprintf(&b, "action_required: %d\n", digest.ActionRequired)
	fmt.Fprintf(&b, "auto_actions_in_progress: %d\n", digest.AutoActionsInProgress)
	fmt.Fprintf(&b, "resolved_since_last_turn: %d\n", digest.ResolvedSinceLastTurn)
	if digest.Truncated {
		fmt.Fprintf(&b, "truncated: true (use supervision_snapshot(after_seq=%d) for full details)\n", digest.NextSeq)
	}
	if len(digest.Items) > 0 {
		b.WriteString("\n")
	}
	for _, item := range digest.Items {
		if item.Resolved {
			fmt.Fprintf(&b, "- %s %s: %s; resolved (%s)\n", item.SubjectKind, item.SubjectID, item.ResolutionState, item.Reason)
			continue
		}
		status := string(item.SupervisionState)
		line := fmt.Sprintf("- %s %s: %s; recommended=%s; allowed=[%s]",
			item.SubjectKind, item.SubjectID, status, item.RecommendedAction, strings.Join(item.AllowedActions, ","))
		if item.AutoActionID != "" {
			line += "; runtime action in progress (no duplicate required)"
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	if len(digest.Items) > 0 {
		fmt.Fprintf(&b, "\nUse supervision_snapshot(after_seq=%d) for full diagnostics.\n", digest.NextSeq)
	}
	return strings.TrimRight(b.String(), "\n")
}
