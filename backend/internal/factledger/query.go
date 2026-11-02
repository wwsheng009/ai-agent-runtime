package factledger

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
)

// ListActive reconstructs the current projection without deleting superseded history.
//
// Ordering is recency-first with confidence only as a tie-break. The previous
// confidence-first order let every stored failure outrank newer successes, so a
// transient error that had already been retried away kept occupying the injected
// window: the ledger then described the session's past problems rather than its
// present state.
func (l *Ledger) ListActive(ctx context.Context, query Query) ([]Fact, error) {
	history, err := l.ListHistory(ctx, query)
	if err != nil {
		return nil, err
	}
	invalidated := make(map[string]string)
	for _, fact := range history {
		if fact.InvalidatedBy != "" {
			invalidated[fact.FactID] = fact.InvalidatedBy
		}
		if fact.Supersedes != "" {
			invalidated[fact.Supersedes] = fact.FactID
		}
	}
	entries, err := l.loadEntries(ctx, query.SessionID, []string{memoryKindInvalidation}, ledgerLoadLimit(query.Limit))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		factID := contentString(entry.Content, "fact_id")
		if factID != "" {
			invalidated[factID] = contentString(entry.Content, "invalidated_by")
		}
	}
	active := make([]Fact, 0, len(history))
	for _, fact := range history {
		if _, removed := invalidated[fact.FactID]; removed {
			continue
		}
		active = append(active, fact)
	}
	sort.SliceStable(active, func(i, j int) bool {
		if active[i].UpdatedAt.Equal(active[j].UpdatedAt) {
			return active[i].Confidence > active[j].Confidence
		}
		return active[i].UpdatedAt.After(active[j].UpdatedAt)
	})
	if query.Limit > 0 && len(active) > query.Limit {
		active = active[:query.Limit]
	}
	return active, nil
}

// recencyAwareStore loads rows newest-first. The default LoadMemoryEntries
// orders by priority before recency, which for a long session silently drops
// the most recent facts (they share the lowest priority band) instead of the
// oldest ones.
type recencyAwareStore interface {
	LoadMemoryEntriesRecent(ctx context.Context, sessionID string, kinds []string, limit int) ([]artifact.MemoryEntry, error)
}

// factKindAwareStore filters on the semantic kind stored *inside* content_json.
// Without it the read window is measured in raw rows, and a long session's
// sparse declarative facts are pushed out by the process-artifact rows that
// older builds stored — rows the reader is going to discard anyway.
type factKindAwareStore interface {
	LoadMemoryEntriesByContentKind(ctx context.Context, sessionID, entryKind string, contentKinds []string, limit int) ([]artifact.MemoryEntry, error)
}

func (l *Ledger) loadEntries(ctx context.Context, sessionID string, kinds []string, limit int) ([]artifact.MemoryEntry, error) {
	if loader, ok := l.store.(recencyAwareStore); ok {
		return loader.LoadMemoryEntriesRecent(ctx, sessionID, kinds, limit)
	}
	return l.store.LoadMemoryEntries(ctx, sessionID, kinds, limit)
}

// loadFacts reads the fact rows matching the query, pushing the semantic kind
// filter into storage when the store can do it.
func (l *Ledger) loadFacts(ctx context.Context, sessionID string, query Query, limit int) ([]artifact.MemoryEntry, error) {
	contentKinds := sortedLowerList(query.Kinds)
	if len(contentKinds) > 0 {
		if filter, ok := l.store.(factKindAwareStore); ok {
			return filter.LoadMemoryEntriesByContentKind(ctx, sessionID, memoryKindFact, contentKinds, limit)
		}
	}
	return l.loadEntries(ctx, sessionID, []string{memoryKindFact}, limit)
}

func (l *Ledger) ListHistory(ctx context.Context, query Query) ([]Fact, error) {
	if l == nil || l.store == nil {
		return nil, fmt.Errorf("fact ledger store is not configured")
	}
	query.SessionID = strings.TrimSpace(query.SessionID)
	if query.SessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	entries, err := l.loadFacts(ctx, query.SessionID, query, ledgerLoadLimit(query.Limit))
	if err != nil {
		return nil, err
	}
	kinds := stringSet(query.Kinds)
	facts := make([]Fact, 0, len(entries))
	for _, entry := range entries {
		fact, err := factFromEntry(entry)
		if err != nil || !matchesQuery(fact, query, kinds) {
			continue
		}
		facts = append(facts, fact)
	}
	sort.SliceStable(facts, func(i, j int) bool { return facts[i].UpdatedAt.Before(facts[j].UpdatedAt) })
	return facts, nil
}

func normalizeFact(fact Fact) Fact {
	fact.FactID = strings.TrimSpace(fact.FactID)
	if fact.FactID == "" {
		fact.FactID = "fact_" + strings.ReplaceAll(uuidString(), "-", "")
	}
	fact.WorkspaceID = strings.TrimSpace(fact.WorkspaceID)
	fact.SessionID = strings.TrimSpace(fact.SessionID)
	fact.GoalID = strings.TrimSpace(fact.GoalID)
	fact.Scope = strings.ToLower(strings.TrimSpace(fact.Scope))
	if fact.Scope == "" {
		if fact.GoalID != "" {
			fact.Scope = ScopeGoal
		} else {
			fact.Scope = ScopeSession
		}
	}
	fact.Kind = strings.ToLower(strings.TrimSpace(fact.Kind))
	fact.Subject = strings.TrimSpace(fact.Subject)
	fact.Predicate = strings.TrimSpace(fact.Predicate)
	fact.SourceEventID = strings.TrimSpace(fact.SourceEventID)
	fact.EvidenceRefs = dedupeStrings(fact.EvidenceRefs)
	fact.InvalidatedBy = strings.TrimSpace(fact.InvalidatedBy)
	fact.Supersedes = strings.TrimSpace(fact.Supersedes)
	if fact.Confidence == 0 {
		fact.Confidence = 1
	}
	if fact.ValidFrom.IsZero() {
		fact.ValidFrom = time.Now().UTC()
	}
	if fact.UpdatedAt.IsZero() {
		fact.UpdatedAt = fact.ValidFrom
	}
	return fact
}

func factContent(fact Fact) map[string]interface{} {
	payload, _ := json.Marshal(fact)
	content := map[string]interface{}{}
	_ = json.Unmarshal(payload, &content)
	return content
}

func factFromEntry(entry artifact.MemoryEntry) (Fact, error) {
	payload, err := json.Marshal(entry.Content)
	if err != nil {
		return Fact{}, err
	}
	var fact Fact
	if err := json.Unmarshal(payload, &fact); err != nil {
		return Fact{}, err
	}
	fact.SessionID = firstValue(fact.SessionID, entry.SessionID)
	fact.GoalID = firstValue(fact.GoalID, entry.TaskID)
	if fact.UpdatedAt.IsZero() {
		fact.UpdatedAt = entry.CreatedAt
	}
	return fact, nil
}

func matchesQuery(fact Fact, query Query, kinds map[string]bool) bool {
	if query.WorkspaceID != "" && fact.WorkspaceID != "" && fact.WorkspaceID != query.WorkspaceID {
		return false
	}
	// A goal-less query must NOT drop goal-scoped facts. The old rule hid every
	// goal fact the moment the goal ended, so the ledger — and with it the
	// statement "these outcomes are verified" — silently emptied itself in the
	// middle of a still-running session, which also relocated the injected
	// message between requests and broke the provider prompt-cache prefix.
	if query.GoalID != "" && fact.GoalID != "" && fact.GoalID != query.GoalID {
		return false
	}
	return len(kinds) == 0 || kinds[fact.Kind]
}

func ledgerLoadLimit(limit int) int {
	if limit <= 0 {
		return 2048
	}
	if limit < 128 {
		return limit * 8
	}
	return limit
}

func contentString(content map[string]interface{}, key string) string {
	value, _ := content[key].(string)
	return strings.TrimSpace(value)
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		if value = strings.ToLower(strings.TrimSpace(value)); value != "" {
			set[value] = true
		}
	}
	return set
}

// sortedLowerList normalizes and sorts a kind list so a pushed-down SQL filter
// gets a deterministic, duplicate-free argument list.
func sortedLowerList(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func firstValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
