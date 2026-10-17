package tools

import (
	"net/url"
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

// P3-3 signal weights: a term hit in the title counts more than one in the
// snippet, which counts more than one that only appears in the URL.
const (
	webSearchQualityTitleWeight   = 1.0
	webSearchQualitySnippetWeight = 0.7
	webSearchQualityURLWeight     = 0.4
	// Modulation floor: the diversity and snippet-presence dampeners can only
	// shrink the coverage score (bounded to [floor, 1]); they never inflate it,
	// so unrelated results stay at 0 and relevant ones are not inflated.
	webSearchQualityModulationFloor = 0.7
)

// webSearchQuality describes how well a provider result set matches the
// query. Score is the weighted term coverage (title > snippet > URL) times a
// bounded modulation factor (domain diversity + snippet presence); Signals
// carries the per-signal values for the explainable quality_signals metadata.
// tier is "ok" for a normal hit and "low" for a result that should trigger
// fallback but may still be returned if it is the best attempt.
type webSearchQuality struct {
	Score   float64
	Tier    string
	Terms   []string
	Signals map[string]float64
	Factor  float64
}

// assessWebSearchQuality scores a result set against the query. Empty result
// sets are reported as empty so callers can distinguish "no results" from
// "results that look unrelated".
func assessWebSearchQuality(query string, results []DuckDuckGoResult) webSearchQuality {
	terms := webSearchQueryTerms(query)
	if len(results) == 0 {
		return webSearchQuality{Score: 0, Tier: "empty", Terms: terms, Factor: 1}
	}
	if len(terms) == 0 {
		return webSearchQuality{Score: 1, Tier: "ok", Terms: terms, Factor: 1}
	}

	normalizedQuery := strings.Join(terms, " ")
	// termWeight 记录每个词项命中的最高字段权重（title > snippet > URL）。
	termWeight := make(map[string]float64, len(terms))
	for _, term := range terms {
		termWeight[term] = 0
	}
	phraseHit := false
	for _, result := range results {
		title := strings.ToLower(result.Title)
		snippet := strings.ToLower(result.Snippet)
		url := strings.ToLower(result.URL)
		if normalizedQuery != "" && strings.Contains(strings.Join([]string{title, snippet, url}, " "), normalizedQuery) {
			phraseHit = true
		}
		for _, term := range terms {
			weight := termWeight[term]
			if strings.Contains(title, term) {
				weight = webSearchQualityTitleWeight
			} else if weight < webSearchQualitySnippetWeight && strings.Contains(snippet, term) {
				weight = webSearchQualitySnippetWeight
			} else if weight < webSearchQualityURLWeight && strings.Contains(url, term) {
				weight = webSearchQualityURLWeight
			}
			termWeight[term] = weight
		}
	}

	coverage := 0.0
	for _, weight := range termWeight {
		coverage += weight
	}
	coverage /= float64(len(terms))

	diversity := webSearchDomainDiversity(results)
	snippetRatio := webSearchSnippetPresence(results)
	factor := webSearchQualityModulationFloor + (1-webSearchQualityModulationFloor)*(0.5*diversity+0.5*snippetRatio)

	score := coverage * factor
	if phraseHit {
		score = 1
	}
	signals := map[string]float64{
		"coverage":         coverage,
		"domain_diversity": diversity,
		"snippet_presence": snippetRatio,
	}
	tier := "low"
	if score >= webSearchQualityThreshold() {
		tier = "ok"
	}
	return webSearchQuality{Score: score, Tier: tier, Terms: terms, Signals: signals, Factor: factor}
}

// webSearchDomainDiversity reports the unique-host share among the top few
// results (P3-3): a provider echoing one domain five times scores low.
func webSearchDomainDiversity(results []DuckDuckGoResult) float64 {
	const window = 5
	if len(results) == 0 {
		return 0
	}
	seen := make(map[string]struct{}, window)
	for index, result := range results {
		if index >= window {
			break
		}
		if host := webSearchHostOf(result.URL); host != "" {
			seen[host] = struct{}{}
		}
	}
	denominator := window
	if len(results) < window {
		denominator = len(results)
	}
	if denominator == 0 {
		return 0
	}
	return float64(len(seen)) / float64(denominator)
}

// webSearchSnippetPresence reports the non-empty-snippet ratio (P3-3).
func webSearchSnippetPresence(results []DuckDuckGoResult) float64 {
	if len(results) == 0 {
		return 0
	}
	nonEmpty := 0
	for _, result := range results {
		if strings.TrimSpace(result.Snippet) != "" {
			nonEmpty++
		}
	}
	return float64(nonEmpty) / float64(len(results))
}

// webSearchHostOf extracts the lowercase host of one result URL; unparseable
// URLs return "" and never inflate the diversity denominator.
func webSearchHostOf(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
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
