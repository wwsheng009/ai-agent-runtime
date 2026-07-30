package contextmgr

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/factledger"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type FactObservationInput struct {
	TraceID      string
	WorkspaceID  string
	SessionID    string
	GoalID       string
	Observations []types.Observation
}

func (m *Manager) factLedger() *factledger.Ledger {
	if m == nil {
		return nil
	}
	if m.Facts != nil {
		return m.Facts
	}
	return factledger.New(m.Ledger)
}

// RecordObservations persists tool/test outcomes immediately instead of waiting
// for a later history compaction pass.
func (m *Manager) RecordObservations(ctx context.Context, input FactObservationInput) {
	ledger := m.factLedger()
	if ledger == nil || strings.TrimSpace(input.SessionID) == "" {
		return
	}
	for index, observation := range input.Observations {
		tool := strings.TrimSpace(observation.Tool)
		if tool == "" {
			continue
		}
		sourceEventID := fmt.Sprintf("event:%s:tool:%s:%d", firstNonEmpty(input.TraceID, input.SessionID), tool, index+1)
		evidence := observationFactRefs(observation)
		if len(evidence) == 0 {
			evidence = []string{sourceEventID}
		}
		value := map[string]interface{}{"success": observation.Success}
		if observation.Success {
			value["output"] = summarizeLine(factValueText(observation.Output), 500)
		} else {
			value["error"] = strings.TrimSpace(observation.Error)
		}
		scope := factledger.ScopeSession
		if strings.TrimSpace(input.GoalID) != "" {
			scope = factledger.ScopeGoal
		}
		predicate := "succeeded"
		confidence := 0.95
		if !observation.Success {
			predicate = "failed"
			confidence = 1
		}
		_, _ = ledger.Append(ctx, factledger.Fact{
			FactID:        observationFactID(input.SessionID, input.GoalID, sourceEventID, observation),
			WorkspaceID:   strings.TrimSpace(input.WorkspaceID),
			SessionID:     strings.TrimSpace(input.SessionID),
			GoalID:        strings.TrimSpace(input.GoalID),
			Scope:         scope,
			Kind:          observationFactKind(tool),
			Subject:       tool,
			Predicate:     predicate,
			Value:         value,
			SourceEventID: sourceEventID,
			EvidenceRefs:  evidence,
			Confidence:    confidence,
			ValidFrom:     observation.Timestamp,
			UpdatedAt:     observation.Timestamp,
		})
	}
}

func (m *Manager) persistStructuredFacts(ctx context.Context, workspaceID, sessionID, goalID string, entries []artifact.MemoryEntry) {
	ledger := m.factLedger()
	if ledger == nil {
		return
	}
	for _, entry := range entries {
		summary := strings.TrimSpace(fmt.Sprintf("%v", entry.Content["summary"]))
		if summary == "" || strings.TrimSpace(entry.SourceHash) == "" {
			continue
		}
		kind, predicate, confidence := structuredFactClassification(entry.Kind, entry.Content)
		evidence := mergeSourceRefs(entry.SourceRefs)
		if len(evidence) == 0 {
			evidence = []string{"message:" + entry.SourceHash}
		}
		sourceEventID := firstEventRef(evidence)
		if sourceEventID == "" {
			sourceEventID = "message:" + entry.SourceHash
		}
		scope := factledger.ScopeSession
		if strings.TrimSpace(goalID) != "" {
			scope = factledger.ScopeGoal
		}
		_, _ = ledger.Append(ctx, factledger.Fact{
			FactID:        "fact_" + truncateHash(entry.SourceHash, 24),
			WorkspaceID:   strings.TrimSpace(workspaceID),
			SessionID:     strings.TrimSpace(sessionID),
			GoalID:        strings.TrimSpace(goalID),
			Scope:         scope,
			Kind:          kind,
			Subject:       firstNonEmpty(fmt.Sprintf("%v", entry.Content["role"]), "conversation"),
			Predicate:     predicate,
			Value:         summary,
			SourceEventID: sourceEventID,
			EvidenceRefs:  evidence,
			Confidence:    confidence,
			ValidFrom:     entry.CreatedAt,
			UpdatedAt:     entry.CreatedAt,
		})
	}
}

func (m *Manager) buildFactLedgerMessage(ctx context.Context, input BuildInput) (*types.Message, int, []string) {
	ledger := m.factLedger()
	if ledger == nil || strings.TrimSpace(input.SessionID) == "" {
		return nil, 0, nil
	}
	limit := m.Strategy.LedgerLoadLimit
	if limit <= 0 {
		limit = 12
	}
	facts, err := ledger.ListActive(ctx, factledger.Query{
		WorkspaceID: strings.TrimSpace(input.WorkspaceID),
		SessionID:   strings.TrimSpace(input.SessionID),
		GoalID:      strings.TrimSpace(input.GoalID),
		Limit:       limit,
	})
	if err != nil || len(facts) == 0 {
		return nil, 0, nil
	}
	lines := []string{"Verified fact ledger (authoritative over compacted prose):"}
	evidence := make([]string, 0, len(facts))
	factIDs := make([]string, 0, len(facts))
	for _, fact := range facts {
		value := factValueText(fact.Value)
		line := fmt.Sprintf("- [%s] %s %s %s", fact.Kind, fact.Subject, fact.Predicate, summarizeLine(value, 220))
		if len(fact.EvidenceRefs) > 0 {
			line += " [evidence=" + strings.Join(fact.EvidenceRefs, ",") + "]"
		}
		lines = append(lines, line)
		evidence = mergeSourceRefs(evidence, fact.EvidenceRefs)
		factIDs = append(factIDs, fact.FactID)
	}
	// Use developer role so the ledger stays instruction-context, not assistant
	// transcript / trailing prefill that can leak into the visible chat surface.
	message := types.NewDeveloperMessage(strings.Join(lines, "\n"))
	message.Metadata["context_stage"] = "fact_ledger"
	message.Metadata["goal_id"] = strings.TrimSpace(input.GoalID)
	message.Metadata["fact_ids"] = factIDs
	message.Metadata["evidence_refs"] = evidence
	return message, len(facts), evidence
}

func structuredFactClassification(kind string, content map[string]interface{}) (string, string, float64) {
	summary := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", content["summary"])))
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "decision":
		return "decision", "decided", 0.9
	case "plan":
		return "remaining_work", "plans", 0.75
	case "failure":
		return "execution", "failed", 0.95
	case "open_question":
		return "open_question", "needs_verification", 0.65
	default:
		if strings.Contains(summary, "must") || strings.Contains(summary, "only") || strings.Contains(summary, "不要") || strings.Contains(summary, "必须") {
			return "constraint", "requires", 0.85
		}
		return "fact", "states", 0.7
	}
}

func factValueText(value interface{}) string {
	if text, ok := value.(string); ok {
		return text
	}
	payload, err := json.Marshal(value)
	if err == nil {
		return string(payload)
	}
	return fmt.Sprintf("%v", value)
}

func firstEventRef(refs []string) string {
	for _, ref := range refs {
		if strings.HasPrefix(ref, "event:") || strings.HasPrefix(ref, "evt_") {
			return ref
		}
	}
	return ""
}

func truncateHash(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit > 0 && len(value) > limit {
		return value[:limit]
	}
	return value
}

func observationFactKind(tool string) string {
	lower := strings.ToLower(strings.TrimSpace(tool))
	switch {
	case strings.Contains(lower, "test"), strings.Contains(lower, "verify"), strings.Contains(lower, "check"):
		return "test_result"
	case strings.Contains(lower, "read"), strings.Contains(lower, "list"), strings.Contains(lower, "search"), strings.Contains(lower, "inspect"):
		return "workspace"
	default:
		return "execution"
	}
}

func observationFactRefs(observation types.Observation) []string {
	refs := make([]string, 0, 4)
	for _, key := range []string{"evidence_refs", "artifact_refs", "source_refs", "event_id"} {
		switch values := observation.Metrics[key].(type) {
		case string:
			refs = append(refs, values)
		case []string:
			refs = append(refs, values...)
		case []interface{}:
			for _, value := range values {
				if text, ok := value.(string); ok {
					refs = append(refs, text)
				}
			}
		}
	}
	return mergeSourceRefs(refs)
}

func observationFactID(sessionID, goalID, sourceEventID string, observation types.Observation) string {
	value := strings.Join([]string{
		strings.TrimSpace(sessionID), strings.TrimSpace(goalID), strings.TrimSpace(sourceEventID),
		strings.TrimSpace(observation.Tool), strings.TrimSpace(observation.Error), factValueText(observation.Output),
	}, "\x00")
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("fact_%x", digest[:12])
}
