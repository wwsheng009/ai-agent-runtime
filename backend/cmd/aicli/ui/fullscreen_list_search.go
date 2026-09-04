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
// Matching semantics:
//   - query is split on whitespace and every token must hit;
//   - a single token scores exact 1000 > prefix 800 > contains 600 >
//     wildcard 400~599 (when the token contains '*'), computed per field
//     with the best field score winning;
//   - tokens without '*' must appear as a contiguous substring (exact /
//     prefix / contains): query characters are never matched in a split or
//     reordered fashion, so e.g. "muse" cannot match "stepfun-ai/...";
//   - '*' acts as a wildcard matching any run of characters (including
//     empty), e.g. "m*spark" matches "muse-spark-1.3";
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
	case strings.Contains(token, "*"):
		return fullScreenListWildcardScore(field, token)
	default:
		// Without an explicit wildcard, a token must match as a contiguous
		// substring; split/order-preserving character matching is not
		// performed ("muse" must not match "stepfun-ai/step-3.5-flash").
		return 0, false
	}
}

// fullScreenListWildcardScore matches token as a glob pattern where '*'
// matches any run of characters. Matching is containment-style: the pattern
// may match any substring of field, so e.g. "c*x" matches "acbxz" and
// "gpt*4o" matches "...gpt-4o...". Contiguous contains (600) still ranks
// above wildcard hits; longer literal prefixes rank higher.
func fullScreenListWildcardScore(field, token string) (int, bool) {
	if !strings.Contains(token, "*") || !globMatch(field, "*"+token+"*") {
		return 0, false
	}
	score := 400 - min(len([]rune(field))/4, 50)
	literalPrefix := token[:strings.Index(token, "*")]
	score += min(len([]rune(literalPrefix))*10, 100)
	if score > 599 {
		score = 599
	}
	return score, true
}

// globMatch reports whether s matches pattern, where '*' matches any run of
// characters (including the empty run). Other characters match literally.
func globMatch(s, pattern string) bool {
	sRunes := []rune(s)
	pRunes := []rune(pattern)
	if len(pRunes) == 0 {
		return len(sRunes) == 0
	}
	// dp[i][j]: pattern[:i] matches s[:j].
	dp := make([][]bool, len(pRunes)+1)
	for i := range dp {
		dp[i] = make([]bool, len(sRunes)+1)
	}
	dp[0][0] = true
	for i := 1; i <= len(pRunes); i++ {
		if pRunes[i-1] == '*' {
			dp[i][0] = dp[i-1][0]
		}
	}
	for i := 1; i <= len(pRunes); i++ {
		for j := 1; j <= len(sRunes); j++ {
			if pRunes[i-1] == '*' {
				dp[i][j] = dp[i-1][j] || dp[i][j-1]
			} else {
				dp[i][j] = dp[i-1][j-1] && pRunes[i-1] == sRunes[j-1]
			}
		}
	}
	return dp[len(pRunes)][len(sRunes)]
}
