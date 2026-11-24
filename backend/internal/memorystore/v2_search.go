package memorystore

import (
	"context"
	"strings"
	"time"
)

// RecordSearchQuery is the unified, project-scoped search contract used by the
// HTTP, CLI and agent-tool surfaces. It intentionally maps onto the existing
// RecordFilter so every caller shares one retrieval path (FTS5 when available,
// bounded LIKE fallback otherwise) instead of maintaining separate SQL.
type RecordSearchQuery struct {
	Text                    string
	Status                  Status
	Statuses                []Status
	Kind                    string
	Kinds                   []string
	Tag                     string
	Tags                    []string
	SourceKind              string
	SourceSessionID         string
	SourceSessionGeneration *int64
	PinnedOnly              bool
	IncludeArchived         bool
	AsOf                    *time.Time
	Limit                   int
	Cursor                  string
}

// RecordSearchHit carries the record plus retrieval diagnostics.
type RecordSearchHit struct {
	Record      Record
	Score       float64
	MatchSource string
}

// RecordSearchPage is the bounded result of SearchRecords.
type RecordSearchPage struct {
	Items      []RecordSearchHit
	NextCursor string
	Count      int
	// Backend is one of "fts5", "like_fallback", "recent", "as_of",
	// "entity" or "hybrid".
	Backend string
}

// SearchRecords is the single store-level entry point for list/search. HTTP,
// CLI and tools must call this method rather than assembling RecordFilter or
// SearchOptions independently.
func (s *Store) SearchRecords(ctx context.Context, query RecordSearchQuery) (RecordSearchPage, error) {
	filter := RecordFilter{
		Status:                  query.Status,
		Statuses:                query.Statuses,
		Kind:                    query.Kind,
		Kinds:                   query.Kinds,
		Tag:                     query.Tag,
		Tags:                    query.Tags,
		Query:                   strings.TrimSpace(query.Text),
		SourceKind:              query.SourceKind,
		SourceSessionID:         query.SourceSessionID,
		SourceSessionGeneration: query.SourceSessionGeneration,
		PinnedOnly:              query.PinnedOnly,
		IncludeArchived:         query.IncludeArchived,
		AsOf:                    query.AsOf,
		Limit:                   query.Limit,
		Cursor:                  query.Cursor,
	}
	page, err := s.ListRecords(ctx, filter)
	if err != nil {
		return RecordSearchPage{}, err
	}
	backend := page.SearchBackend
	if backend == "" {
		backend = "recent"
		if query.AsOf != nil {
			backend = "as_of"
		} else if filter.Query != "" {
			backend = "like_fallback"
		}
	}
	queryLimit := query.Limit
	if queryLimit <= 0 {
		queryLimit = 50
	}
	if queryLimit > 200 {
		queryLimit = 200
	}
	items := make([]RecordSearchHit, 0, len(page.Items))
	for _, record := range page.Items {
		items = append(items, RecordSearchHit{
			Record:      record,
			MatchSource: backend,
			Score:       reciprocalRankScore(len(items) + 1),
		})
	}
	if filter.Query != "" && filter.AsOf == nil {
		entityIDs, entityErr := s.searchEntityLinkedRecordIDs(ctx, filter.Query, queryLimit)
		if entityErr == nil && len(entityIDs) > 0 {
			seen := make(map[string]struct{}, len(items))
			for _, hit := range items {
				seen[hit.Record.ID] = struct{}{}
			}
			addedEntity := false
			for _, id := range entityIDs {
				if _, ok := seen[id]; ok {
					continue
				}
				record, getErr := s.GetRecord(ctx, id)
				if getErr != nil || !recordMatchesSearchQuery(record, query, filter) {
					continue
				}
				items = append(items, RecordSearchHit{
					Record: record, MatchSource: "entity",
					Score: reciprocalRankScore(len(items) + 1),
				})
				seen[id] = struct{}{}
				addedEntity = true
				if len(items) >= queryLimit {
					break
				}
			}
			if addedEntity {
				backend = "hybrid"
				if len(page.Items) == 0 {
					backend = "entity"
				}
				for index := range items {
					if items[index].MatchSource != "entity" {
						items[index].MatchSource = backend
					}
				}
			}
		}
	}
	return RecordSearchPage{
		Items:      items,
		NextCursor: page.NextCursor,
		Count:      len(items),
		Backend:    backend,
	}, nil
}

func reciprocalRankScore(rank int) float64 {
	if rank <= 0 {
		return 0
	}
	return 1 / (60 + float64(rank))
}

func recordMatchesSearchQuery(record Record, query RecordSearchQuery, filter RecordFilter) bool {
	if !query.IncludeArchived && record.Archived {
		return false
	}
	statuses := query.Statuses
	if len(statuses) == 0 {
		status := query.Status
		if status == "" {
			status = StatusActive
		}
		statuses = []Status{status}
	}
	statusMatched := false
	for _, status := range statuses {
		if record.Status == status {
			statusMatched = true
			break
		}
	}
	if !statusMatched {
		return false
	}
	if !record.ValidUntil.IsZero() && !record.ValidUntil.After(time.Now().UTC()) {
		return false
	}
	if query.Kind != "" && record.Kind != query.Kind {
		return false
	}
	if len(query.Kinds) > 0 {
		kindMatched := false
		for _, kind := range query.Kinds {
			if record.Kind == kind {
				kindMatched = true
				break
			}
		}
		if !kindMatched {
			return false
		}
	}
	if query.SourceKind != "" && !strings.EqualFold(record.SourceKind, query.SourceKind) {
		return false
	}
	if query.SourceSessionID != "" && record.SourceSessionID != query.SourceSessionID {
		return false
	}
	if query.SourceSessionGeneration != nil && record.SourceSessionGeneration != *query.SourceSessionGeneration {
		return false
	}
	if query.PinnedOnly && !record.Pinned {
		return false
	}
	if strings.TrimSpace(filter.Tag) != "" {
		found := false
		for _, tag := range record.Tags {
			if strings.EqualFold(strings.TrimSpace(tag), strings.TrimSpace(filter.Tag)) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(query.Tags) > 0 {
		for _, required := range query.Tags {
			found := false
			for _, tag := range record.Tags {
				if strings.EqualFold(strings.TrimSpace(tag), strings.TrimSpace(required)) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return true
}
