package tools

import (
	"os"
	"strconv"
	"strings"
	"unicode"
)

// defaultWebSearchQualityThreshold is the minimum query-term coverage required
// for a provider result to be considered a normal hit. It is intentionally
// conservative: low-scoring results are still returned when no provider does
// better, but they trigger the next provider in the chain first.
const (
	defaultWebSearchQualityThreshold = 0.30
	webSearchQualityThresholdEnv     = "WEB_SEARCH_MIN_QUALITY"
)

func webSearchQualityThreshold() float64 {
	raw := strings.TrimSpace(os.Getenv(webSearchQualityThresholdEnv))
	if raw == "" {
		return defaultWebSearchQualityThreshold
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value < 0 || value > 1 {
		return defaultWebSearchQualityThreshold
	}
	return value
}

// webSearchQuality describes how well a provider result set matches the
// query. Score is the fraction of meaningful query terms found in the result
// title/snippet/URL; tier is "ok" for a normal hit and "low" for a result that
// should trigger fallback but may still be returned if it is the best attempt.
type webSearchQuality struct {
	Score float64
	Tier  string
	Terms []string
}

// assessWebSearchQuality scores a result set against the query. Empty result
// sets are reported as empty so callers can distinguish "no results" from
// "results that look unrelated".
func assessWebSearchQuality(query string, results []DuckDuckGoResult) webSearchQuality {
	terms := webSearchQueryTerms(query)
	if len(results) == 0 {
		return webSearchQuality{Score: 0, Tier: "empty", Terms: terms}
	}
	if len(terms) == 0 {
		return webSearchQuality{Score: 1, Tier: "ok", Terms: terms}
	}

	normalizedQuery := strings.Join(terms, " ")
	matched := make(map[string]struct{}, len(terms))
	phraseHit := false
	for _, result := range results {
		text := strings.ToLower(strings.Join([]string{result.Title, result.Snippet, result.URL}, " "))
		if normalizedQuery != "" && strings.Contains(text, normalizedQuery) {
			phraseHit = true
		}
		for _, term := range terms {
			if strings.Contains(text, term) {
				matched[term] = struct{}{}
			}
		}
	}

	score := float64(len(matched)) / float64(len(terms))
	if phraseHit {
		score = 1
	}
	tier := "low"
	if score >= webSearchQualityThreshold() {
		tier = "ok"
	}
	return webSearchQuality{Score: score, Tier: tier, Terms: terms}
}

// webSearchQueryTerms extracts meaningful, lower-cased terms for the quality
// heuristic. ASCII words shorter than two characters are dropped; contiguous
// CJK runs are kept as terms so Chinese queries still receive a useful score.
func webSearchQueryTerms(query string) []string {
	lower := strings.ToLower(strings.TrimSpace(query))
	if lower == "" {
		return nil
	}

	var terms []string
	seen := make(map[string]struct{})
	add := func(term string) {
		term = strings.Trim(term, " \t\r\n-–—_.,:;!?()[]{}\"'`/\\|@#$%^&*+=<>~")
		if term == "" || isWebSearchStopWord(term) {
			return
		}
		if !containsCJK(term) && len([]rune(term)) < 2 {
			return
		}
		if _, ok := seen[term]; ok {
			return
		}
		seen[term] = struct{}{}
		terms = append(terms, term)
	}

	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			add(current.String())
			current.Reset()
		}
	}
	for _, r := range lower {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			current.WriteRune(r)
		case isCJK(r):
			flush()
			current.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
	return terms
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		(r >= 0x3040 && r <= 0x30ff) ||
		(r >= 0xAC00 && r <= 0xD7AF)
}

func containsCJK(value string) bool {
	for _, r := range value {
		if isCJK(r) {
			return true
		}
	}
	return false
}

func isWebSearchStopWord(term string) bool {
	switch term {
	case "a", "an", "and", "are", "as", "at", "be", "been", "by", "can",
		"did", "do", "does", "for", "from", "had", "has", "have", "how",
		"in", "into", "is", "it", "its", "of", "on", "or", "our", "out",
		"over", "she", "so", "than", "that", "the", "their", "them",
		"there", "these", "they", "this", "to", "under", "up", "was",
		"we", "were", "what", "when", "where", "which", "who", "why",
		"will", "with", "you", "your":
		return true
	case "的", "了", "和", "与", "及", "或", "在", "是", "有":
		return true
	default:
		return false
	}
}
