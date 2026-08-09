package ui

import (
	"sort"
	"strings"
)

// fullScreenListSearchFields returns the text fields of one item that
// participate in fuzzy scoring. Each field is scored independently (best
// score wins per token), mirroring legacy single-field semantics and
// preventing cross-field subsequence false positives.
func fullScreenListSearchFields(item FullScreenListItem) []string {
	return []string{item.Title, item.Detail, item.Preview, item.SearchText}
}

// fullScreenListMatches returns the item indexes matching query, ranked by
// relevance (best first).
//
// The matching semantics mirror the legacy filterLoginProviders search:
//   - query is split on whitespace and every token must hit;
//   - a single token scores exact 1000 > prefix 800 > contains 600 >
//     subsequence 300 (with density/early/length adjustments), computed per
//     field with the best field score winning;
//   - ties keep the original list order (stable sort).
//
// An empty query returns every item in original order.
func fullScreenListMatches(items []FullScreenListItem, query string) []int {
	query = strings.TrimSpace(query)
	if query == "" {
		out := make([]int, len(items))
		for i := range items {
			out[i] = i
		}
		return out
	}
	tokens := strings.Fields(strings.ToLower(query))
	type scored struct {
		index int
		score int
	}
	ranked := make([]scored, 0, len(items))
	for index, item := range items {
		score, ok := fullScreenListMatchScore(item, tokens)
		if !ok {
			continue
		}
		ranked = append(ranked, scored{index: index, score: score})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].index < ranked[j].index
	})
	out := make([]int, len(ranked))
	for i, entry := range ranked {
		out[i] = entry.index
	}
	return out
}

func fullScreenListMatchScore(item FullScreenListItem, tokens []string) (int, bool) {
	if len(tokens) == 0 {
		return 0, true
	}
	total := 0
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		score, ok := fullScreenListTokenBestScore(item, token)
		if !ok {
			return 0, false
		}
		total += score
	}
	return total, true
}

// fullScreenListTokenBestScore scores token against every field of item and
// returns the best score. Exact matches on a short field (e.g. the title)
// therefore rank above fuzzy hits on a long preview, and subsequences never
// span field boundaries.
func fullScreenListTokenBestScore(item FullScreenListItem, token string) (int, bool) {
	best := -1
	for _, field := range fullScreenListSearchFields(item) {
		field = strings.ToLower(strings.TrimSpace(field))
		if field == "" {
			continue
		}
		score, ok := fullScreenListTokenScore(field, token)
		if !ok {
			continue
		}
		if score > best {
			best = score
		}
	}
	if best < 0 {
		return 0, false
	}
	return best, true
}

func fullScreenListTokenScore(field, token string) (int, bool) {
	if token == "" {
		return 0, false
	}
	switch {
	case field == token:
		return 1000, true
	case strings.HasPrefix(field, token):
		return 800 - min(len([]rune(field)), 100), true
	case strings.Contains(field, token):
		idx := strings.Index(field, token)
		return 600 - min(idx, 100) - min(len([]rune(field))/4, 50), true
	default:
		return fullScreenListSubsequenceScore(field, token)
	}
}

// fullScreenListSubsequenceScore mirrors the legacy
// loginProviderSubsequenceScore: query characters must appear in order;
// earlier, denser matches on shorter names score higher.
func fullScreenListSubsequenceScore(field, query string) (int, bool) {
	if query == "" {
		return 0, true
	}
	nameRunes := []rune(field)
	queryRunes := []rune(query)
	if len(queryRunes) > len(nameRunes) {
		return 0, false
	}
	j := 0
	first := -1
	last := -1
	for i := 0; i < len(nameRunes) && j < len(queryRunes); i++ {
		if nameRunes[i] != queryRunes[j] {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
		j++
	}
	if j != len(queryRunes) {
		return 0, false
	}
	span := last - first + 1
	densityBonus := min(80, (len(queryRunes)*80)/max(span, 1))
	earlyBonus := 40 - min(first, 40)
	lengthPenalty := min(len(nameRunes), 40)
	return 300 + densityBonus + earlyBonus - lengthPenalty, true
}
