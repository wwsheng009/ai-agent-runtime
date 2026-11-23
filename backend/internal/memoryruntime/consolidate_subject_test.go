package memoryruntime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestConflictSubjectKeyRequiresIdentityAndValueEvidence(t *testing.T) {
	tests := []struct {
		name        string
		derivedText string
		manualText  string
		sameSubject bool
	}{
		{
			name:        "numbered servers",
			derivedText: "Server 1 listens on port 8000",
			manualText:  "Server 2 listens on port 9000",
		},
		{
			name:        "numbered entities with scalar assignments",
			derivedText: "Server 1 timeout is 30s",
			manualText:  "Server 2 timeout is 60s",
		},
		{
			name:        "alphanumeric identifiers",
			derivedText: "worker7 timeout is 30s",
			manualText:  "worker8 timeout is 60s",
		},
		{
			name:        "unit-suffixed entity identifiers",
			derivedText: "Pool 30s timeout is 5s",
			manualText:  "Pool 60s timeout is 10s",
		},
		{
			name:        "semantic versions in entity names",
			derivedText: "Runtime v1.2.3 timeout is 30s",
			manualText:  "Runtime v1.2.4 timeout is 60s",
		},
		{
			name:        "protocol versions",
			derivedText: "HTTP/1.1 retry count is 3",
			manualText:  "HTTP/2 retry count is 5",
		},
		{
			name:        "version values are not scalar settings",
			derivedText: "Runtime version is 1.2.3",
			manualText:  "Runtime version is 1.2.4",
		},
		{
			name:        "IP address identities",
			derivedText: "Host 10.0.0.1 timeout is 30s",
			manualText:  "Host 10.0.0.2 timeout is 60s",
		},
		{
			name:        "path identities",
			derivedText: "/api/v1 timeout is 30s",
			manualText:  "/api/v2 timeout is 60s",
		},
		{
			name:        "language symbols",
			derivedText: "C++ compiler timeout is 30s",
			manualText:  "C# compiler timeout is 60s",
		},
		{
			name:        "punctuation inside identifiers",
			derivedText: "cache-worker port is 8000",
			manualText:  "cache_worker port is 9000",
		},
		{
			name:        "case-sensitive technical identifiers",
			derivedText: "/API/v1 timeout is 30s",
			manualText:  "/api/v1 timeout is 60s",
		},
		{
			name:        "prepositions carry meaning",
			derivedText: "Write to the cache",
			manualText:  "Write for the cache",
		},
		{
			name:        "conjunctions carry meaning",
			derivedText: "Use red and blue",
			manualText:  "Use red or blue",
		},
		{
			name:        "plurality can identify a different subject",
			derivedText: "Worker task timeout is 30s",
			manualText:  "Worker tasks timeout is 60s",
		},
		{
			name:        "negation is not discarded",
			derivedText: "API timeout is 30s",
			manualText:  "API timeout is not 60s",
		},
		{
			name:        "recommendations are not assignments",
			derivedText: "API timeout should be 30s",
			manualText:  "API timeout is 60s",
		},
		{
			name:        "a service can listen on multiple ports",
			derivedText: "API listens on port 8000",
			manualText:  "API listens on port 9000",
		},
		{
			name:        "shorthand without an assignment is ambiguous",
			derivedText: "API timeout30s",
			manualText:  "API timeout60s",
		},
		{
			name:        "numbers without an identified entity",
			derivedText: "timeout is 30s",
			manualText:  "timeout is 60s",
		},
		{
			name:        "articles and modifiers do not identify an entity",
			derivedText: "The default timeout is 30s",
			manualText:  "The default timeout is 60s",
		},
		{
			name:        "pronouns do not identify an entity",
			derivedText: "Its timeout is 30s",
			manualText:  "Its timeout is 60s",
		},
		{
			name:        "punctuation does not identify an entity",
			derivedText: "? timeout is 30s",
			manualText:  "? timeout is 60s",
		},
		{
			name:        "unit conversion is not inferred",
			derivedText: "API timeout is 30s",
			manualText:  "API timeout is 2min",
		},
		{
			name:        "unrecognized duration units",
			derivedText: "API timeout is 30widgets",
			manualText:  "API timeout is 60widgets",
		},
		{
			name:        "a port must be an integer",
			derivedText: "API port is 1.2",
			manualText:  "API port is 1.3",
		},
		{
			name:        "a port cannot have a unit",
			derivedText: "API port is 8000s",
			manualText:  "API port is 9000s",
		},
		{
			name:        "ranges are not scalar assignments",
			derivedText: "API timeout is 30-60s",
			manualText:  "API timeout is 60-90s",
		},
		{
			name:        "lists are not scalar assignments",
			derivedText: "API timeout is 30s or 60s",
			manualText:  "API timeout is 60s or 90s",
		},
		{
			name:        "literal placeholders are not values",
			derivedText: "API timeout is 30s",
			manualText:  "API timeout is #s",
		},
		{
			name:        "exact claim with whitespace changes",
			derivedText: "Use append-only memory writes.",
			manualText:  "  Use  append-only\tmemory writes.\n",
			sameSubject: true,
		},
		{
			name:        "explicit timeout value",
			derivedText: "API timeout is 30s",
			manualText:  "API timeout is 60s",
			sameSubject: true,
		},
		{
			name:        "modifiers do not hide an identified entity",
			derivedText: "The default API timeout is 30s",
			manualText:  "The default API timeout is 60s",
			sameSubject: true,
		},
		{
			name:        "decimal durations",
			derivedText: "API timeout is 1.5s",
			manualText:  "API timeout is 2.5s",
			sameSubject: true,
		},
		{
			name:        "assignment forms",
			derivedText: "API timeout=30s",
			manualText:  "API timeout: 60s",
			sameSubject: true,
		},
		{
			name:        "explicit setting and sentence punctuation",
			derivedText: "API timeout is set to 30 seconds.",
			manualText:  "API timeout is 60 seconds",
			sameSubject: true,
		},
		{
			name:        "same numeric entity with explicit port setting",
			derivedText: "Server 1 port is 8000",
			manualText:  "Server 1 port is 9000",
			sameSubject: true,
		},
		{
			name:        "same symbolic entity",
			derivedText: "C++ compiler timeout is 30s",
			manualText:  "C++ compiler timeout is 60s",
			sameSubject: true,
		},
		{
			name:        "same versioned entity",
			derivedText: "Runtime v1.2.3 timeout is 30s",
			manualText:  "Runtime v1.2.3 timeout is 60s",
			sameSubject: true,
		},
		{
			name:        "count setting",
			derivedText: "Job-23 retry count is 3",
			manualText:  "Job-23 retry count is 5",
			sameSubject: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			derived := memorystore.Record{ID: "derived", Kind: "fact", Text: tt.derivedText, SourceKind: "session_extract", Confidence: 0.9}
			manual := memorystore.Record{ID: "manual", Kind: "decision", Text: tt.manualText, SourceKind: "manual"}
			require.Equal(t, tt.sameSubject, conflictSubjectKey(derived) == conflictSubjectKey(manual))

			// A priority advantage must not turn lexical similarity into
			// evidence of a conflict, in either input order.
			for _, records := range [][]memorystore.Record{{derived, manual}, {manual, derived}} {
				active, supersessions, ambiguous := resolveConflicts(records)
				require.Empty(t, ambiguous)
				if tt.sameSubject {
					require.Equal(t, []memorystore.Record{manual}, active)
					require.Equal(t, []memorySupersession{{loserID: derived.ID, winnerID: manual.ID}}, supersessions)
				} else {
					require.Equal(t, records, active)
					require.Empty(t, supersessions)
				}
			}
		})
	}
	require.Empty(t, conflictSubjectKey(memorystore.Record{Text: " \t\n"}))
}

func TestConsolidatorPreservesUnrelatedTechnicalSubjects(t *testing.T) {
	tests := []struct {
		name        string
		derivedText string
		manualText  string
	}{
		{"numbered servers", "Server 1 listens on port 8000", "Server 2 listens on port 9000"},
		{"language symbols", "C++ tooling uses local builds", "C# tooling uses local builds"},
		{"versioned entities", "API v1 timeout is 30s", "API v2 timeout is 60s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, store := newConsolidatorForTest(t)
			ctx := context.Background()
			candidate, err := store.InsertCandidate(ctx, memorystore.Candidate{
				SessionID: "s1", Kind: "fact", Text: tt.derivedText,
				Confidence: 0.9, SourceHash: "derived-source", SourceRevision: 1,
			})
			require.NoError(t, err)
			derived, err := store.PromoteCandidate(ctx, candidate.ID)
			require.NoError(t, err)
			manual, err := store.Append(memorystore.AppendNoteOptions{
				Text: tt.manualText, Kind: "decision",
			})
			require.NoError(t, err)
			before, err := store.ListRecords(ctx, memorystore.RecordFilter{Status: memorystore.StatusActive, Limit: 10})
			require.NoError(t, err)
			require.Equal(t, 2, before.Count)

			result, err := c.Run(ctx)
			require.NoError(t, err)
			require.Zero(t, result.Superseded)
			require.Zero(t, result.OpenQuestions)
			require.NotEmpty(t, result.SummaryID)
			require.Contains(t, result.SummaryText, tt.derivedText)

			active, err := store.ListRecords(ctx, memorystore.RecordFilter{Status: memorystore.StatusActive, Limit: 10})
			require.NoError(t, err)
			require.Equal(t, 2, active.Count)
			var ids []string
			for _, record := range active.Items {
				ids = append(ids, record.ID)
				require.Empty(t, record.SupersedesID)
				require.Equal(t, memorystore.FreshnessFresh, record.Freshness)
			}
			require.ElementsMatch(t, []string{derived.ID, manual.ID}, ids)
			preserved, err := store.GetRecord(ctx, derived.ID)
			require.NoError(t, err)
			require.Equal(t, derived.Revision, preserved.Revision)
			require.Equal(t, tt.derivedText, preserved.Text)

			events, err := store.ListEvents(ctx, 50)
			require.NoError(t, err)
			for _, event := range events {
				require.NotEqual(t, "superseded", event.EventType)
				require.NotEqual(t, "open_question", event.EventType)
			}
		})
	}
}

func TestResolveConflictsPreservesPriorityAndProtection(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	old := memorystore.Record{
		ID: "old", Kind: "fact", Text: "API timeout is 30s", SourceKind: "session_extract",
		Confidence: 0.9, UpdatedAt: now.Add(-time.Hour),
	}
	newer := memorystore.Record{
		ID: "new", Kind: "fact", Text: "API timeout is 60s", SourceKind: "session_extract",
		Confidence: 0.9, UpdatedAt: now,
	}
	tests := []struct {
		name          string
		prepare       func(*memorystore.Record, *memorystore.Record)
		supersedesOld bool
		ambiguous     bool
	}{
		{
			name: "manual overrides derived",
			prepare: func(_, newer *memorystore.Record) {
				newer.SourceKind = "manual"
			},
			supersedesOld: true,
		},
		{
			name: "explicit correction overrides verified fact",
			prepare: func(_, newer *memorystore.Record) {
				newer.Kind = "correction"
				newer.Confidence = 0.1
			},
			supersedesOld: true,
		},
		{
			name: "correction cannot automatically discard manual memory",
			prepare: func(old, newer *memorystore.Record) {
				old.SourceKind = "manual"
				newer.Kind = "correction"
			},
		},
		{
			name: "correction cannot automatically discard pinned manual memory",
			prepare: func(old, newer *memorystore.Record) {
				old.SourceKind, old.Pinned = "manual", true
				newer.Kind = "correction"
			},
		},
		{
			name: "high confidence reversal requires review",
			prepare: func(_, newer *memorystore.Record) {
				newer.Kind = "correction"
			},
			ambiguous: true,
		},
		{
			name: "policy reversal requires review",
			prepare: func(old, newer *memorystore.Record) {
				old.Kind = "policy"
				newer.Kind = "correction"
			},
			ambiguous: true,
		},
		{
			name: "procedure reversal requires review",
			prepare: func(old, newer *memorystore.Record) {
				old.Kind = "procedure"
				newer.Kind = "correction"
			},
			ambiguous: true,
		},
		{
			name: "cross scope reversal requires review",
			prepare: func(_, newer *memorystore.Record) {
				newer.Kind = "correction"
				newer.Scope = memorystore.ScopeUser
			},
			ambiguous: true,
		},
		{
			name: "manual cannot automatically discard pinned derived memory",
			prepare: func(old, newer *memorystore.Record) {
				old.Pinned = true
				newer.SourceKind = "manual"
			},
		},
		{
			name: "correction priority does not prove subject identity",
			prepare: func(old, newer *memorystore.Record) {
				old.Text = "API v1 timeout is 30s"
				newer.Text, newer.Kind = "API v2 timeout is 60s", "correction"
			},
		},
		{
			name:      "same priority preserves both different values",
			ambiguous: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left, right := old, newer
			if tt.prepare != nil {
				tt.prepare(&left, &right)
			}
			for _, records := range [][]memorystore.Record{{left, right}, {right, left}} {
				active, supersessions, ambiguous := resolveConflicts(records)
				if tt.supersedesOld {
					require.Equal(t, []memorystore.Record{right}, active)
					require.Equal(t, []memorySupersession{{loserID: left.ID, winnerID: right.ID}}, supersessions)
				} else {
					require.Equal(t, records, active)
					require.Empty(t, supersessions)
				}
				if tt.ambiguous {
					require.ElementsMatch(t, []string{left.ID, right.ID}, ambiguous)
				} else {
					require.Empty(t, ambiguous)
				}
			}
		})
	}
}
