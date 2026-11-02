package contextmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/factledger"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func (m *Manager) factLedger() *factledger.Ledger {
	if m == nil {
		return nil
	}
	if m.Facts != nil {
		return m.Facts
	}
	return factledger.New(m.Ledger)
}

// The fact ledger is deliberately *not* a sink for tool results.
//
// Tool execution produces process artifacts: the outcome is already in the
// transcript verbatim, the compaction summary is asked to carry its conclusion
// (compactruntime's prompt requests decisions, constraints, critical references,
// pitfalls to avoid repeating and remaining work), oversized originals live in
// the artifact store behind `artifact_read`, and repeated reads are stopped at
// the tool layer by the file-read ledger. Recording each call as a "verified
// fact" produced a lossy fourth copy that outranked the prose it claimed to
// supervise.
//
// What this file keeps is the residue a compaction is free to drop and no other
// layer preserves: verbatim user constraints, decisions, work still owed, open
// questions and recorded pitfalls — written by `persistStructuredFacts` at the
// compaction boundary and injected only after that boundary, delta-only.
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
		if _, injectable := injectableFactKinds[kind]; !injectable {
			continue
		}
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

// injectableFactKinds is the entire ledger payload after the process/conclusion
// split. The value is the per-kind slot cap.
//
// Tool outcomes — execution, test_result, state_change, runtime, workspace — are
// process artifacts and are deliberately absent, so the rows older builds stored
// under those kinds stop reaching the prompt without any data migration.
var injectableFactKinds = map[string]int{
	"constraint":       4,
	"decision":         4,
	"remaining_work":   3,
	"open_question":    2,
	"failure":          2,
	"goal":             2,
	"completed_action": 2,
}

// declarativeFactKinds lists the injectable kinds for a storage-level filter.
// Order is fixed so a pushed-down query keeps a stable statement shape.
func declarativeFactKinds() []string {
	kinds := make([]string, 0, len(injectableFactKinds))
	for kind := range injectableFactKinds {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}

// PersistCompactionFacts records the declarative residue of a compaction at the
// boundary that destroyed the prose it came from.
//
// This is the ledger's only writer. It runs where history is actually replaced,
// not on every tool call, and it stores only statements classified as
// constraints, decisions, owed work, open questions or recorded pitfalls. The
// per-message catch-all is rejected on purpose: one "fact" per compacted message
// would simply relabel the transcript and re-import the noise this split exists
// to remove. Returns the number of facts appended.
func (m *Manager) PersistCompactionFacts(ctx context.Context, workspaceID, sessionID, taskID string, segment []types.Message) int {
	ledger := m.factLedger()
	if ledger == nil || strings.TrimSpace(sessionID) == "" || len(segment) == 0 {
		return 0
	}
	normalizedTaskID := firstNonEmpty(strings.TrimSpace(taskID), strings.TrimSpace(sessionID))
	// Harvest only what the conversation actually asserted. Injected prompt
	// layers (goal stamp, todos, team, a previous ledger block, the compaction
	// checkpoint itself) and tool payloads must never be mined back into the
	// ledger: production data already shows an older extractor turning
	// `developer`-role ledger text into a "failure" fact — the ledger feeding on
	// itself.
	source := make([]types.Message, 0, len(segment))
	for _, message := range segment {
		if ledgerHarvestableMessage(message) {
			source = append(source, message)
		}
	}
	if len(source) == 0 {
		return 0
	}
	entries := deriveMemoryEntries(strings.TrimSpace(sessionID), normalizedTaskID, ledgerCheckpointSegmentReason, source, nil)
	if len(entries) == 0 {
		return 0
	}
	pending := make([]artifact.MemoryEntry, 0, len(entries))
	for _, entry := range entries {
		kind, _, _ := structuredFactClassification(entry.Kind, entry.Content)
		if _, injectable := injectableFactKinds[kind]; !injectable {
			continue
		}
		pending = append(pending, entry)
	}
	if len(pending) == 0 {
		return 0
	}
	m.persistStructuredFacts(ctx, workspaceID, strings.TrimSpace(sessionID), "", pending)
	return len(pending)
}

// buildFactLedgerMessage renders the declarative residue as one short block.
//
// Only kinds on the allowlist are rendered, each slot-capped, newest first, and
// anything this transcript already announced is skipped so the block carries the
// delta instead of re-dumping the same statements every request. Evidence
// references stay in message metadata: an internal
// `event:trace_<uuid>:tool:view:1` string is not dereferenceable by the model,
// so inlining it only burned tokens.
func (m *Manager) buildFactLedgerMessage(ctx context.Context, input BuildInput) (*types.Message, int, []string) {
	ledger := m.factLedger()
	if ledger == nil || strings.TrimSpace(input.SessionID) == "" {
		return nil, 0, nil
	}
	limit := m.Strategy.LedgerLoadLimit
	if limit <= 0 {
		limit = 12
	}
	announced := announcedFactIDs(input.History)
	// The kind filter is pushed into storage so the read window counts matching
	// rows. Without the pushdown, a session that filed thousands of
	// process-artifact rows before this split could never surface its handful of
	// declarative facts: they sat behind rows the selection was about to discard.
	facts, err := ledger.ListActive(ctx, factledger.Query{
		WorkspaceID: strings.TrimSpace(input.WorkspaceID),
		SessionID:   strings.TrimSpace(input.SessionID),
		GoalID:      strings.TrimSpace(input.GoalID),
		Kinds:       declarativeFactKinds(),
		Limit:       limit,
	})
	if err != nil || len(facts) == 0 {
		return nil, 0, nil
	}

	selected := selectRenderableFacts(facts, limit, announced)
	if len(selected) == 0 {
		return nil, 0, nil
	}

	lines := []string{factLedgerHeader}
	evidence := make([]string, 0, len(selected))
	factIDs := make([]string, 0, len(selected))
	for _, fact := range selected {
		lines = append(lines, renderFactLine(fact))
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
	return message, len(selected), evidence
}

// selectRenderableFacts keeps only declarative facts, honours each kind's slot
// cap, and skips anything this transcript already put in front of the model.
//
// Selection runs over a list factledger already ordered newest-first. The caps
// are hard: an under-filled window is the intended outcome, not a bug.
func selectRenderableFacts(facts []factledger.Fact, limit int, announced map[string]bool) []factledger.Fact {
	if limit <= 0 {
		return nil
	}
	counted := make(map[string]int, len(injectableFactKinds))
	selected := make([]factledger.Fact, 0, limit)
	for _, fact := range facts {
		if len(selected) >= limit {
			break
		}
		kind := strings.ToLower(strings.TrimSpace(fact.Kind))
		slotCap, injectable := injectableFactKinds[kind]
		if !injectable {
			// Process artifact: the transcript, the compaction summary, the
			// artifact store or the tool-layer read ledger already carry it.
			continue
		}
		if announced[fact.FactID] {
			continue
		}
		value := factRenderValue(fact)
		if strings.TrimSpace(value) == "" || selfIngestedFactValue(value) ||
			types.IsControlPlanePromptText(value) {
			continue
		}
		if narratedFactSubject(fact.Subject) {
			// Rows written before the harvest was restricted carry the originating
			// role in Subject, so agent narration stored as a "rule" or "todo"
			// (`- [rule] assistant requires: Let me do final due-diligence...`) can
			// still be recognized and dropped instead of re-injected.
			continue
		}
		if kind == "failure" && reportsACleanResult(strings.ToLower(value)) {
			// A row stored as a pitfall that in fact reports a clean run was
			// misfiled by the vocabulary heuristic. The same rule that stops new
			// writes also refuses to inherit the old mistake.
			continue
		}
		if counted[kind] >= slotCap {
			continue
		}
		counted[kind]++
		selected = append(selected, fact)
	}
	return selected
}

// ledgerHarvestableMessage reports whether a message may be examined for
// declarative residue.
//
// Only the user's own assertions qualify. Anything the agent narrated — the
// decisions it took, the pitfalls it hit, its progress recaps — is exactly what
// the compaction summary is instructed to carry: the local prompt asks for key
// decisions, constraints and preferences, critical references, pitfalls to avoid
// repeating, and remaining work. Storing those prose again gave the model its own
// words back, mislabelled: real sessions produced
// `- [rule] assistant requires: Let me do final due-diligence...` for ordinary
// narration, which is the same low-value-authoritative pattern this split was
// meant to remove.
func ledgerHarvestableMessage(message types.Message) bool {
	if !strings.EqualFold(strings.TrimSpace(message.Role), "user") {
		return false
	}
	// A user row carrying a context_stage is an injected layer (goal stamp, todo
	// state, a prior ledger block, the compaction checkpoint), not an assertion.
	if strings.TrimSpace(message.Metadata.GetString(metaContextStage, "")) != "" {
		return false
	}
	// Host control-plane prompts (supervision digest, auto-wake, resume) ride in
	// as plain user turns. Their “child session failed / background job finished”
	// lines are runtime bookkeeping; a real session produced two such rows that
	// survived every other guard, so the check belongs here rather than in a
	// keyword table.
	if types.IsControlPlanePromptText(message.Content) {
		return false
	}
	// One-run re-prompts (completion audit and friends) are never user speech.
	if message.Metadata.GetBool(types.MetadataKeyTransientPrompt, false) {
		return false
	}
	return strings.TrimSpace(message.Content) != ""
}

// narratedFactSubject reports a fact whose Subject names a conversation role the
// model itself spoke in. Those rows are agent narration filed as a rule, todo or
// pitfall; the compaction summary owns them, the ledger must not echo them.
func narratedFactSubject(subject string) bool {
	switch strings.ToLower(strings.TrimSpace(subject)) {
	case "assistant", "developer", "system", "tool":
		return true
	default:
		return false
	}
}

// selfIngestedFactValue rejects rows whose content is a rendering of an injected
// prompt layer. Stored sessions already contain them; they are feedback, not
// evidence, and showing them again re-imports the noise this split removed.
func selfIngestedFactValue(value string) bool {
	trimmed := strings.TrimSpace(value)
	for _, header := range legacyFactLedgerHeaders() {
		if strings.HasPrefix(trimmed, header) {
			return true
		}
	}
	return strings.HasPrefix(trimmed, "Compacted context from earlier turns")
}

// announcedFactIDs returns the fact ids an earlier fact_ledger layer in the same
// transcript already delivered.
//
// Context layers are append-only across turns — the first copy stays in
// canonical history — so re-emitting the same facts each turn only grew the
// prompt without saying anything new. Sending just the delta matches how codex
// advances context (render_diff / RFC 7386 merge patch) instead of re-dumping
// it. Once the carrying message is itself compacted away these ids stop being
// visible and the facts are re-announced, which is precisely the post-compaction
// rehydrate docs/compact/README.md describes.
func announcedFactIDs(history []types.Message) map[string]bool {
	announced := map[string]bool{}
	for index := range history {
		message := history[index]
		if strings.TrimSpace(message.Metadata.GetString(metaContextStage, "")) != "fact_ledger" {
			continue
		}
		for _, id := range metadataStringValues(message.Metadata["fact_ids"]) {
			if strings.TrimSpace(id) != "" {
				announced[id] = true
			}
		}
	}
	return announced
}

// metadataStringValues reads a string list whether it is still a []string held
// in memory or a []interface{} decoded from stored JSON.
func metadataStringValues(raw interface{}) []string {
	switch typed := raw.(type) {
	case []string:
		return typed
	case []interface{}:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
		return values
	default:
		return nil
	}
}

// factRenderValue returns the human clause stored in a fact. Values are written
// as strings by the only live writer; the fallback keeps a row with a structured
// value from rendering as an empty line.
func factRenderValue(fact factledger.Fact) string {
	switch typed := fact.Value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(factValueText(typed))
	}
}

func renderFactLine(fact factledger.Fact) string {
	label := factKindLabel(fact.Kind)
	// The subject is stored as written — a role, a goal id, a component name —
	// and must not be normalised: rewriting `goal-active` into `goal_active`
	// would break the identity the rest of the system refers to.
	subject := strings.TrimSpace(fact.Subject)
	predicate := strings.ToLower(strings.TrimSpace(fact.Predicate))
	value := summarizeLine(factRenderValue(fact), 200)

	head := subject
	switch {
	case head == "" || head == "conversation":
		head = predicate
	case predicate != "" && predicate != "succeeded":
		head = head + " " + predicate
	}
	return "- [" + label + "] " + head + ": " + value
}

func factKindLabel(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "constraint":
		return "rule"
	case "decision":
		return "decision"
	case "remaining_work":
		return "todo"
	case "open_question":
		return "open"
	case "goal":
		return "goal"
	case "completed_action":
		return "done"
	case "failure":
		return "pitfall"
	default:
		return "fact"
	}
}

func structuredFactClassification(kind string, content map[string]interface{}) (string, string, float64) {
	summary := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", content["summary"])))
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "decision":
		return "decision", "decided", 0.9
	case "plan":
		return "remaining_work", "plans", 0.75
	case "failure":
		// A recorded failure is a constraint on the next attempt ("do not retry
		// this way"), not a process artifact: keep it declarative and injectable.
		return "failure", "failed", 0.95
	case "open_question":
		return "open_question", "needs_verification", 0.65
	default:
		if strings.Contains(summary, "must") || strings.Contains(summary, "only") || strings.Contains(summary, "不要") || strings.Contains(summary, "必须") {
			return "constraint", "requires", 0.85
		}
		// Not a rule, a restatement. Kept in the checkpoint ledger for audit,
		// never promoted to an injected fact.
		return "conversation", "states", 0.5
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
