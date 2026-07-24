package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultPathCandidateLimit = 3

// BuildPathNotFoundHintForPath builds a path-not-found hint for a single path string.
func BuildPathNotFoundHintForPath(path, workdir string) string {
	suggestion := buildPathNotFoundSuggestion(path, workdir)
	if suggestion == "" {
		return ""
	}
	return formatPathRelatedHint("提示: 文件或目录不存在", []string{suggestion}, workdir)
}

// BuildPathNotFoundHintFromTokens builds a path-not-found hint from tokenized command input.
func BuildPathNotFoundHintFromTokens(tokens []string, workdir string) string {
	candidates := ExtractPathCandidatesFromTokens(tokens)
	if len(candidates) == 0 {
		return ""
	}

	suggestions := make([]string, 0, defaultPathCandidateLimit)
	for _, candidate := range candidates {
		if suggestion := buildPathNotFoundSuggestion(candidate, workdir); suggestion != "" {
			suggestions = append(suggestions, suggestion)
		}
		if len(suggestions) >= defaultPathCandidateLimit {
			break
		}
	}
	if len(suggestions) == 0 {
		return ""
	}

	return formatPathRelatedHint("提示: 文件或目录不存在", suggestions, workdir)
}

// BuildPathKindMismatchHintForPath builds a hint for a path that exists but is the wrong kind.
func BuildPathKindMismatchHintForPath(path, workdir string) string {
	suggestion := buildPathKindMismatchSuggestion(path, workdir)
	if suggestion == "" {
		return ""
	}
	return formatPathRelatedHint("提示: 路径是目录，不是文件", []string{suggestion}, workdir)
}

func buildPathNotFoundSuggestion(path, workdir string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	detail := ResolveUpwardPathDetailInWorkdir(trimmed, workdir)
	if len(detail.Candidates) == 0 {
		return ""
	}
	filtered := filterRelevantPathCandidates(trimmed, detail.Candidates, workdir)
	if len(filtered) == 0 {
		return ""
	}
	return fmt.Sprintf("%s -> %s", trimmed, strings.Join(filtered, ", "))
}

func buildPathKindMismatchSuggestion(path, workdir string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}

	resolved := resolveHintTargetPath(trimmed, workdir)
	if resolved == "" {
		return ""
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return ""
	}

	parent := filepath.Dir(resolved)
	if parent == "" || parent == resolved {
		return ""
	}

	entries, err := os.ReadDir(parent)
	if err != nil {
		return ""
	}
	ranked := rankPathEntries(entries, filepath.Base(resolved))
	if len(ranked) == 0 {
		return ""
	}

	candidates := make([]string, 0, defaultPathCandidateLimit)
	for _, entry := range ranked {
		if strings.EqualFold(entry.name, filepath.Base(resolved)) {
			continue
		}
		candidates = append(candidates, filepath.Clean(filepath.Join(parent, entry.name)))
		if len(candidates) >= defaultPathCandidateLimit {
			break
		}
	}
	if len(candidates) == 0 {
		return ""
	}

	return fmt.Sprintf("%s -> %s", trimmed, strings.Join(candidates, ", "))
}

func formatPathRelatedHint(message string, suggestions []string, workdir string) string {
	if len(suggestions) == 0 {
		return ""
	}

	if trimmed := strings.TrimSpace(workdir); trimmed != "" {
		return fmt.Sprintf(
			"%s，请先确认当前 workdir=%s；可能的路径候选: %s",
			message,
			trimmed,
			strings.Join(suggestions, "； "),
		)
	}
	return fmt.Sprintf(
		"%s；可能的路径候选: %s",
		message,
		strings.Join(suggestions, "； "),
	)
}

func filterRelevantPathCandidates(target string, candidates []string, workdir string) []string {
	if len(candidates) == 0 {
		return nil
	}
	targetBase := strings.ToLower(filepath.Base(strings.TrimSpace(target)))
	workdirAnchor := normalizeResolutionBaseDir(workdir)
	filtered := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidate = filepath.Clean(strings.TrimSpace(candidate))
		if candidate == "" {
			continue
		}
		// Prefer candidates that share a base-name fragment or sit under workdir.
		candidateBase := strings.ToLower(filepath.Base(candidate))
		underWorkdir := workdirAnchor != "" && isPathWithinRoot(candidate, workdirAnchor)
		baseRelated := targetBase != "" && candidateBase != "" &&
			(strings.Contains(candidateBase, targetBase) ||
				strings.Contains(targetBase, candidateBase) ||
				pathBaseSimilarity(targetBase, candidateBase) >= 0.5)
		if underWorkdir || baseRelated {
			filtered = append(filtered, candidate)
		}
	}
	if len(filtered) > 0 {
		return filtered
	}
	// Absolute paths outside workdir often get drive-root siblings that confuse models.
	// Prefer no candidates over unrelated noise when workdir is known.
	if workdirAnchor != "" && filepath.IsAbs(strings.TrimSpace(target)) {
		return nil
	}
	return candidates
}

func isPathWithinRoot(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(strings.ToLower(path), strings.ToLower(root+sep))
}

func pathBaseSimilarity(a, b string) float64 {
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	// Dice coefficient over bigrams is lightweight and good enough for short basenames.
	if len(a) < 2 || len(b) < 2 {
		if strings.Contains(a, b) || strings.Contains(b, a) {
			return 0.6
		}
		return 0
	}
	bigrams := func(s string) map[string]int {
		out := make(map[string]int, len(s))
		for i := 0; i+1 < len(s); i++ {
			out[s[i:i+2]]++
		}
		return out
	}
	aa := bigrams(a)
	bb := bigrams(b)
	overlap := 0
	for k, av := range aa {
		if bv, ok := bb[k]; ok {
			if av < bv {
				overlap += av
			} else {
				overlap += bv
			}
		}
	}
	return 2 * float64(overlap) / float64(len(a)-1+len(b)-1)
}

func resolveHintTargetPath(path, workdir string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}

	expanded := expandTildePath(trimmed)
	if filepath.IsAbs(expanded) {
		return filepath.Clean(expanded)
	}

	if anchor := normalizeResolutionBaseDir(workdir); anchor != "" {
		return filepath.Clean(filepath.Join(anchor, expanded))
	}

	if abs, err := filepath.Abs(expanded); err == nil {
		return filepath.Clean(abs)
	}

	return filepath.Clean(expanded)
}

// ExtractPathCandidatesFromTokens extracts likely path arguments from a tokenized command.
func ExtractPathCandidatesFromTokens(tokens []string) []string {
	if len(tokens) < 2 {
		return nil
	}

	segment := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if isShellCommandSeparator(token) {
			break
		}
		segment = append(segment, token)
	}
	if len(segment) < 2 {
		return nil
	}

	pathLike := make([]string, 0, len(segment)-1)
	fallback := make([]string, 0, len(segment)-1)
	seen := make(map[string]struct{}, len(segment)-1)
	addCandidate := func(list *[]string, token string) {
		token = normalizePathToken(token)
		if token == "" {
			return
		}
		if _, exists := seen[token]; exists {
			return
		}
		seen[token] = struct{}{}
		*list = append(*list, token)
	}

	for _, token := range segment[1:] {
		token = normalizePathToken(token)
		if token == "" || strings.HasPrefix(token, "-") || token == "--" {
			continue
		}
		if isLikelyPathToken(token) {
			addCandidate(&pathLike, token)
			continue
		}
		addCandidate(&fallback, token)
	}

	if len(pathLike) > 0 {
		return pathLike
	}
	if len(fallback) == 0 {
		return nil
	}

	for left, right := 0, len(fallback)-1; left < right; left, right = left+1, right-1 {
		fallback[left], fallback[right] = fallback[right], fallback[left]
	}
	return fallback
}

func normalizePathToken(token string) string {
	token = strings.TrimSpace(token)
	token = strings.Trim(token, `"'`)
	token = strings.TrimSuffix(token, ",")
	token = strings.TrimSuffix(token, ";")
	return token
}

func isLikelyPathToken(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	lower := strings.ToLower(token)
	switch lower {
	case ".", "..":
		return false
	}
	if strings.Contains(lower, "...") {
		return false
	}
	if strings.ContainsAny(token, `/\`) {
		return true
	}
	if strings.HasPrefix(lower, "~") {
		return true
	}
	if strings.Contains(token, ":") {
		return true
	}
	if strings.Contains(token, "*") || strings.Contains(token, "?") {
		return true
	}
	if strings.Contains(token, ".") {
		return true
	}
	return false
}

func isShellCommandSeparator(token string) bool {
	switch token {
	case "|", "||", "&&", ";":
		return true
	default:
		return strings.HasPrefix(token, "|") || strings.HasSuffix(token, "|")
	}
}
