package toolkit

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// ToolSearchName is the model-facing meta-tool for catalog discovery.
const ToolSearchName = "search_tool"

// DefaultToolSearchThreshold is the catalog size at which hosts may inject
// search_tool and project non-core tools out of the direct model surface.
const DefaultToolSearchThreshold = 24

// ToolSearchEntry is one searchable tool catalog entry.
type ToolSearchEntry struct {
	Name        string
	Description string
	ServerName  string
	Parameters  map[string]interface{}
	Metadata    map[string]interface{}
}

// ToolSearchResult is a single ranked search hit.
type ToolSearchResult struct {
	Name        string                 `json:"name"`
	ServerName  string                 `json:"server_name,omitempty"`
	Description string                 `json:"description,omitempty"`
	Score       float64                `json:"score"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// SearchSnapshot is a point-in-time query result plus index metadata.
type SearchSnapshot struct {
	Results          []ToolSearchResult `json:"results"`
	TotalTools       int                `json:"total_tools"`
	TotalHiddenTools int                `json:"total_hidden_tools"`
	Query            string             `json:"query"`
	IsReady          bool               `json:"is_ready"`
}

// ToolSearchIndex is a backend-agnostic catalog search interface.
type ToolSearchIndex interface {
	SearchSnapshot(query string, limit int) SearchSnapshot
	Len() int
}

// InMemoryToolSearchIndex is a lightweight token BM25-lite index over tool
// names and descriptions. Suitable for local catalogs (tens to low thousands).
type InMemoryToolSearchIndex struct {
	entries []ToolSearchEntry
	docs    []indexedDoc
	df      map[string]int
	avgLen  float64
}

type indexedDoc struct {
	entry  ToolSearchEntry
	tokens []string
	tf     map[string]int
	len    int
}

// NewInMemoryToolSearchIndex builds an index from catalog entries.
func NewInMemoryToolSearchIndex(entries []ToolSearchEntry) *InMemoryToolSearchIndex {
	idx := &InMemoryToolSearchIndex{
		entries: append([]ToolSearchEntry(nil), entries...),
		df:      make(map[string]int),
	}
	if len(entries) == 0 {
		return idx
	}
	totalLen := 0
	idx.docs = make([]indexedDoc, 0, len(entries))
	for _, entry := range entries {
		tokens := tokenizeToolSearchText(entry.Name + " " + entry.Description + " " + entry.ServerName)
		tf := make(map[string]int, len(tokens))
		seen := make(map[string]struct{}, len(tokens))
		for _, token := range tokens {
			tf[token]++
			if _, ok := seen[token]; !ok {
				seen[token] = struct{}{}
				idx.df[token]++
			}
		}
		doc := indexedDoc{
			entry:  entry,
			tokens: tokens,
			tf:     tf,
			len:    len(tokens),
		}
		totalLen += doc.len
		idx.docs = append(idx.docs, doc)
	}
	if len(idx.docs) > 0 {
		idx.avgLen = float64(totalLen) / float64(len(idx.docs))
	}
	return idx
}

// Len returns indexed tool count.
func (idx *InMemoryToolSearchIndex) Len() int {
	if idx == nil {
		return 0
	}
	return len(idx.docs)
}

// SearchSnapshot ranks tools for query and returns up to limit hits.
func (idx *InMemoryToolSearchIndex) SearchSnapshot(query string, limit int) SearchSnapshot {
	if idx == nil || len(idx.docs) == 0 {
		return SearchSnapshot{
			Query:   strings.TrimSpace(query),
			IsReady: true,
		}
	}
	if limit <= 0 {
		limit = 10
	}
	query = strings.TrimSpace(query)
	queryTokens := tokenizeToolSearchText(query)
	if query == "" || len(queryTokens) == 0 {
		// Empty query: return alphabetical prefix of the catalog.
		results := make([]ToolSearchResult, 0, minInt(limit, len(idx.docs)))
		ordered := append([]indexedDoc(nil), idx.docs...)
		sort.SliceStable(ordered, func(i, j int) bool {
			return strings.ToLower(ordered[i].entry.Name) < strings.ToLower(ordered[j].entry.Name)
		})
		for i := 0; i < len(ordered) && len(results) < limit; i++ {
			results = append(results, resultFromEntry(ordered[i].entry, 0))
		}
		return SearchSnapshot{
			Results:          results,
			TotalTools:       len(idx.docs),
			TotalHiddenTools: maxInt(0, len(idx.docs)-len(results)),
			Query:            query,
			IsReady:          true,
		}
	}

	type scored struct {
		entry ToolSearchEntry
		score float64
	}
	n := float64(len(idx.docs))
	hits := make([]scored, 0, len(idx.docs))
	for _, doc := range idx.docs {
		score := 0.0
		// Exact / prefix name boosts.
		nameLower := strings.ToLower(strings.TrimSpace(doc.entry.Name))
		queryLower := strings.ToLower(query)
		if nameLower == queryLower {
			score += 20
		} else if strings.Contains(nameLower, queryLower) {
			score += 8
		}
		for _, token := range queryTokens {
			tf := float64(doc.tf[token])
			if tf == 0 {
				continue
			}
			df := float64(idx.df[token])
			if df <= 0 {
				continue
			}
			// BM25-lite with k1=1.2, b=0.75.
			const k1 = 1.2
			const b = 0.75
			idf := math.Log(1 + (n-df+0.5)/(df+0.5))
			denom := tf + k1*(1-b+b*float64(doc.len)/(idx.avgLen+1e-9))
			score += idf * ((tf * (k1 + 1)) / denom)
			if strings.Contains(nameLower, token) {
				score += 1.5
			}
		}
		if score > 0 {
			hits = append(hits, scored{entry: doc.entry, score: score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score == hits[j].score {
			return strings.ToLower(hits[i].entry.Name) < strings.ToLower(hits[j].entry.Name)
		}
		return hits[i].score > hits[j].score
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	results := make([]ToolSearchResult, 0, len(hits))
	for _, hit := range hits {
		results = append(results, resultFromEntry(hit.entry, hit.score))
	}
	return SearchSnapshot{
		Results:          results,
		TotalTools:       len(idx.docs),
		TotalHiddenTools: maxInt(0, len(idx.docs)-len(results)),
		Query:            query,
		IsReady:          true,
	}
}

func resultFromEntry(entry ToolSearchEntry, score float64) ToolSearchResult {
	return ToolSearchResult{
		Name:        entry.Name,
		ServerName:  entry.ServerName,
		Description: entry.Description,
		Score:       score,
		Parameters:  entry.Parameters,
		Metadata:    entry.Metadata,
	}
}

func tokenizeToolSearchText(text string) []string {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return nil
	}
	// Split camelCase / snake_case / separators into tokens.
	var b strings.Builder
	var tokens []string
	flush := func() {
		if b.Len() == 0 {
			return
		}
		token := b.String()
		b.Reset()
		if len(token) >= 2 || isCJKToken(token) {
			tokens = append(tokens, token)
		}
	}
	var prev rune
	for _, r := range text {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || isCJKRune(r):
			if prev != 0 && unicode.IsLower(prev) && unicode.IsUpper(r) {
				flush()
			}
			b.WriteRune(unicode.ToLower(r))
			prev = r
		default:
			flush()
			prev = 0
		}
	}
	flush()
	// Also keep original underscore-joined name fragments already flushed.
	return tokens
}

func isCJKRune(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana)
}

func isCJKToken(token string) bool {
	for _, r := range token {
		if isCJKRune(r) {
			return true
		}
	}
	return false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
