package toolbroker

import (
	"strings"
)

const (
	synthesisSummaryLimit = 1200
	// SynthesisChildLimit bounds the compact packet; full outputs remain in the
	// wait result / task capsule.
	SynthesisChildLimit = 16
)

// SynthesisChildResult is the bounded per-child projection used by the parent
// synthesis phase. Full child output stays in the wait result; this packet is
// deliberately small enough to be injected into a resume mailbox.
type SynthesisChildResult struct {
	TaskID        string   `json:"task_id,omitempty"`
	SessionID     string   `json:"session_id,omitempty"`
	Status        string   `json:"status,omitempty"`
	DurationMs    int64    `json:"duration_ms,omitempty"`
	Summary       string   `json:"summary,omitempty"`
	Error         string   `json:"error,omitempty"`
	ArtifactRef   string   `json:"artifact_ref,omitempty"`
	RouteWarnings []string `json:"route_warnings,omitempty"`
}

// SynthesisPacket is the shared resume/synthesis contract for subagent_wait and
// batch completion mailboxes. It tells the parent what completed, in what
// order, and that the next action is synthesis rather than further exploration.
type SynthesisPacket struct {
	BatchID         string                 `json:"batch_id,omitempty"`
	Status          string                 `json:"status,omitempty"`
	TaskCount       int                    `json:"task_count,omitempty"`
	CompletedCount  int                    `json:"completed_count,omitempty"`
	FailedCount     int                    `json:"failed_count,omitempty"`
	CanceledCount   int                    `json:"canceled_count,omitempty"`
	TimedOutCount   int                    `json:"timed_out_count,omitempty"`
	ElapsedMs       int64                  `json:"elapsed_ms,omitempty"`
	CompletionOrder []string               `json:"completion_order,omitempty"`
	Children        []SynthesisChildResult `json:"children,omitempty"`
	NextAction      string                 `json:"next_action,omitempty"`
	Truncated       bool                   `json:"truncated,omitempty"`
}

// BuildSynthesisPacketFromWaitResult derives the compact packet from a wait
// result. It is intentionally best-effort: a wait result without agent rows
// returns nil rather than inventing a packet.
func BuildSynthesisPacketFromWaitResult(result *AgentWaitResult) *SynthesisPacket {
	if result == nil {
		return nil
	}
	agents := result.Agents
	if result.Agent != nil && !agentWaitListContains(agents, result.Agent) {
		agents = append(append([]AgentStatusResult(nil), agents...), *result.Agent)
	}
	if len(agents) == 0 {
		return nil
	}
	packet := &SynthesisPacket{
		TaskCount:  len(agents),
		NextAction: strings.TrimSpace(result.NextAction),
	}
	for _, obligation := range result.Obligations {
		if strings.EqualFold(strings.TrimSpace(obligation.SubjectKind), "batch") ||
			strings.HasPrefix(strings.TrimSpace(obligation.ObligationID), "batch_") {
			packet.BatchID = firstNonEmptySynthesisValue(obligation.SubjectID, obligation.ObligationID)
			break
		}
	}
	pending := 0
	for _, agent := range agents {
		child := SynthesisChildResult{
			TaskID:        firstNonEmptySynthesisValue(agent.CurrentTaskID, agent.ID),
			SessionID:     strings.TrimSpace(agent.SessionID),
			Status:        firstNonEmptySynthesisValue(agent.CurrentTaskStatus, agent.Status),
			Summary:       truncateSynthesisSummary(agent.Output),
			Error:         strings.TrimSpace(agent.Error),
			RouteWarnings: append([]string(nil), agent.RouteWarnings...),
		}
		if len(packet.Children) < SynthesisChildLimit {
			packet.Children = append(packet.Children, child)
		} else {
			packet.Truncated = true
		}
		switch synthesisStatusClass(child.Status) {
		case "completed":
			packet.CompletedCount++
			packet.CompletionOrder = append(packet.CompletionOrder, child.TaskID)
		case "failed":
			packet.FailedCount++
			packet.CompletionOrder = append(packet.CompletionOrder, child.TaskID)
		case "canceled":
			packet.CanceledCount++
			packet.CompletionOrder = append(packet.CompletionOrder, child.TaskID)
		case "timed_out":
			packet.TimedOutCount++
			packet.CompletionOrder = append(packet.CompletionOrder, child.TaskID)
		default:
			pending++
		}
	}
	if pending == 0 && strings.TrimSpace(packet.NextAction) == "" {
		packet.NextAction = "synthesize_and_finalize"
	}
	return packet
}

// AttachSynthesisPacket refreshes the compact packet and finalization flag on a
// wait result. It is idempotent and safe to call after ledger attachment.
func AttachSynthesisPacket(result *AgentWaitResult) *AgentWaitResult {
	if result == nil {
		return nil
	}
	packet := BuildSynthesisPacketFromWaitResult(result)
	if packet == nil {
		return result
	}
	result.Synthesis = packet
	pendingObligations := 0
	for _, obligation := range result.Obligations {
		if !obligation.Terminal {
			pendingObligations++
		}
	}
	result.FinalizationRequired = result.PendingCount == 0 && pendingObligations == 0 &&
		len(result.Obligations) > 0 && packet.TaskCount > 0 &&
		packet.CompletedCount+packet.FailedCount+packet.CanceledCount+packet.TimedOutCount >= packet.TaskCount
	if result.FinalizationRequired {
		packet.NextAction = "synthesize_and_finalize"
	}
	return result
}

func synthesisStatusClass(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "complete", "succeeded", "success", "idle":
		return "completed"
	case "failed", "failure", "error":
		return "failed"
	case "canceled", "cancelled", "stopped", "closed":
		return "canceled"
	case "timed_out", "timeout":
		return "timed_out"
	default:
		return "pending"
	}
}

func truncateSynthesisSummary(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= synthesisSummaryLimit {
		return value
	}
	return string(runes[:synthesisSummaryLimit]) + "…"
}

func firstNonEmptySynthesisValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
