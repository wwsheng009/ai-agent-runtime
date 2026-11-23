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
	// Backend is one of "fts5", "like_fallback", "recent" or "as_of".
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
	items := make([]RecordSearchHit, 0, len(page.Items))
	for _, record := range page.Items {
		items = append(items, RecordSearchHit{
			Record:      record,
			MatchSource: backend,
		})
	}
	return RecordSearchPage{
		Items:      items,
		NextCursor: page.NextCursor,
		Count:      len(items),
		Backend:    backend,
	}, nil
}
