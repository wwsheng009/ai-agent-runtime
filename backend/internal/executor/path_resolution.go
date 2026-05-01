package executor

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const defaultPathSuggestionLimit = 3

// PathResolution describes how a path was resolved and which nearby paths may be intended.
type PathResolution struct {
	Input      string
	Cleaned    string
	Resolved   string
	Candidates []string
}

// EffectivePath returns the resolved path when one was found, otherwise the cleaned input.
func (r PathResolution) EffectivePath() string {
	if trimmed := strings.TrimSpace(r.Resolved); trimmed != "" {
		return trimmed
	}
	return r.Cleaned
}

// ResolveUpwardPath resolves a path against the current working directory and parent directories.
func ResolveUpwardPath(path string) string {
	return ResolveUpwardPathDetail(path).EffectivePath()
}

// ResolveUpwardPaths resolves a list of paths with the same upward search behavior.
func ResolveUpwardPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	resolved := make([]string, 0, len(paths))
	for _, path := range paths {
		resolved = append(resolved, ResolveUpwardPath(path))
	}
	return resolved
}

// ResolveUpwardPathDetail resolves a path using the current process working directory.
func ResolveUpwardPathDetail(path string) PathResolution {
	return resolvePathResolution(path, "", defaultPathSuggestionLimit)
}

// ResolveUpwardPathDetailInWorkdir resolves a path using the provided working directory anchor.
func ResolveUpwardPathDetailInWorkdir(path, workdir string) PathResolution {
	return resolvePathResolution(path, workdir, defaultPathSuggestionLimit)
}

func resolvePathResolution(path string, baseDir string, suggestionLimit int) PathResolution {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return PathResolution{}
	}

	expanded := expandTildePath(trimmed)
	cleaned := filepath.Clean(expanded)
	resolution := PathResolution{
		Input:   trimmed,
		Cleaned: cleaned,
	}

	if cleaned == "." {
		if pathExists(cleaned) {
			resolution.Resolved = cleaned
		}
		return resolution
	}

	if filepath.IsAbs(cleaned) {
		if pathExists(cleaned) {
			resolution.Resolved = cleaned
			return resolution
		}
		resolution.Candidates = suggestPathCandidates(cleaned, absolutePathRoot(cleaned), suggestionLimit)
		return resolution
	}

	anchor := normalizeResolutionBaseDir(baseDir)
	if anchor == "" {
		resolution.Candidates = suggestPathCandidates(cleaned, "", suggestionLimit)
		return resolution
	}

	anchoredCandidate := filepath.Clean(filepath.Join(anchor, cleaned))
	if pathExists(anchoredCandidate) {
		resolution.Resolved = cleaned
		return resolution
	}

	for dir := anchor; dir != ""; {
		candidate := filepath.Join(dir, cleaned)
		if pathExists(candidate) {
			resolution.Resolved = candidate
			return resolution
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	resolution.Candidates = suggestPathCandidates(cleaned, anchor, suggestionLimit)
	return resolution
}

func suggestPathCandidates(path string, baseDir string, limit int) []string {
	if limit <= 0 {
		limit = defaultPathSuggestionLimit
	}

	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil
	}

	cleaned := filepath.Clean(trimmed)
	if cleaned == "." {
		return nil
	}

	segments := splitPathSegments(cleaned)
	if len(segments) == 0 {
		return nil
	}

	if filepath.IsAbs(cleaned) && strings.TrimSpace(baseDir) == "" {
		baseDir = absolutePathRoot(cleaned)
	}

	anchor := normalizeResolutionBaseDir(baseDir)
	if anchor == "" {
		return nil
	}

	currentPath, index := findDeepestExistingPrefix(anchor, segments)
	if currentPath == "" {
		return nil
	}

	results := collectPathCandidateBranches(currentPath, segments, index, limit)
	if len(results) == 0 {
		return nil
	}

	candidates := make(map[string]pathCandidate)
	for _, candidate := range results {
		candidate.path = filepath.Clean(candidate.path)
		if existing, ok := candidates[candidate.path]; !ok || candidate.score > existing.score {
			candidates[candidate.path] = candidate
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	merged := make([]pathCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		merged = append(merged, candidate)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].score == merged[j].score {
			return merged[i].path < merged[j].path
		}
		return merged[i].score > merged[j].score
	})

	if len(merged) > limit {
		merged = merged[:limit]
	}

	paths := make([]string, 0, len(merged))
	for _, candidate := range merged {
		if candidate.path == "" {
			continue
		}
		paths = append(paths, candidate.path)
	}
	return paths
}

type pathCandidate struct {
	path  string
	score float64
}

type pathEntryScore struct {
	name  string
	score float64
	isDir bool
}

func collectPathCandidateBranches(currentPath string, segments []string, index int, limit int) []pathCandidate {
	if limit <= 0 {
		return nil
	}
	if index >= len(segments) {
		return []pathCandidate{{path: filepath.Clean(currentPath), score: 0}}
	}

	segment := segments[index]
	exactPath := filepath.Join(currentPath, segment)
	if info, err := os.Stat(exactPath); err == nil && (index == len(segments)-1 || info.IsDir()) {
		if exactResults := collectPathCandidateBranches(exactPath, segments, index+1, limit); len(exactResults) > 0 {
			for i := range exactResults {
				exactResults[i].score += 1
			}
			return exactResults
		}
	}

	entries, err := os.ReadDir(currentPath)
	if err != nil {
		return nil
	}
	ranked := rankPathEntries(entries, segment)
	if len(ranked) == 0 {
		return nil
	}

	results := make([]pathCandidate, 0, limit)
	for _, entry := range ranked {
		if len(results) >= limit {
			break
		}
		if index < len(segments)-1 && !entry.isDir {
			continue
		}
		childPath := filepath.Join(currentPath, entry.name)
		childResults := collectPathCandidateBranches(childPath, segments, index+1, limit-len(results))
		if len(childResults) == 0 {
			continue
		}
		for _, candidate := range childResults {
			candidate.score += entry.score
			results = append(results, candidate)
			if len(results) >= limit {
				break
			}
		}
	}
	return results
}

func rankPathEntries(entries []os.DirEntry, target string) []pathEntryScore {
	target = strings.ToLower(strings.TrimSpace(target))
	if target == "" {
		return nil
	}

	ranked := make([]pathEntryScore, 0, len(entries))
	for _, entry := range entries {
		name := strings.TrimSpace(entry.Name())
		if name == "" || name == "." || name == ".." {
			continue
		}
		score := pathSegmentSimilarity(target, strings.ToLower(name))
		if score <= 0 {
			continue
		}
		if strings.HasPrefix(target, ".") != strings.HasPrefix(strings.ToLower(name), ".") {
			score -= 0.05
		}
		if score < 0.2 {
			continue
		}
		ranked = append(ranked, pathEntryScore{
			name:  name,
			score: score,
			isDir: entry.IsDir(),
		})
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score == ranked[j].score {
			if ranked[i].isDir != ranked[j].isDir {
				return ranked[i].isDir
			}
			if len(ranked[i].name) != len(ranked[j].name) {
				return len(ranked[i].name) < len(ranked[j].name)
			}
			return ranked[i].name < ranked[j].name
		}
		return ranked[i].score > ranked[j].score
	})
	return ranked
}

func findDeepestExistingPrefix(anchor string, segments []string) (string, int) {
	anchor = filepath.Clean(strings.TrimSpace(anchor))
	if anchor == "" {
		return "", 0
	}

	current := anchor
	index := 0
	for index < len(segments) {
		candidate := filepath.Join(current, segments[index])
		info, err := os.Stat(candidate)
		if err != nil {
			break
		}
		current = candidate
		index++
		if !info.IsDir() {
			break
		}
	}

	return current, index
}

func pathSegmentSimilarity(expected, actual string) float64 {
	expected = strings.ToLower(strings.TrimSpace(expected))
	actual = strings.ToLower(strings.TrimSpace(actual))
	if expected == "" || actual == "" {
		return 0
	}
	if expected == actual {
		return 1
	}
	if strings.HasPrefix(expected, actual) || strings.HasPrefix(actual, expected) {
		longest := len(expected)
		if len(actual) > longest {
			longest = len(actual)
		}
		shortest := len(expected)
		if len(actual) < shortest {
			shortest = len(actual)
		}
		if longest == 0 {
			return 0
		}
		score := 0.88 + 0.12*float64(shortest)/float64(longest)
		if score > 1 {
			score = 1
		}
		return score
	}

	distance := levenshteinDistance(expected, actual)
	longest := len(expected)
	if len(actual) > longest {
		longest = len(actual)
	}
	if longest == 0 {
		return 0
	}
	score := 1 - float64(distance)/float64(longest)
	if score < 0 {
		score = 0
	}
	if commonPrefixLength(expected, actual) >= 2 {
		score += 0.05
	}
	if score > 1 {
		score = 1
	}
	return score
}

func commonPrefixLength(left, right string) int {
	leftRunes := []rune(left)
	rightRunes := []rune(right)
	limit := len(leftRunes)
	if len(rightRunes) < limit {
		limit = len(rightRunes)
	}
	count := 0
	for count < limit && leftRunes[count] == rightRunes[count] {
		count++
	}
	return count
}

func levenshteinDistance(left, right string) int {
	leftRunes := []rune(left)
	rightRunes := []rune(right)
	if len(leftRunes) == 0 {
		return len(rightRunes)
	}
	if len(rightRunes) == 0 {
		return len(leftRunes)
	}

	prev := make([]int, len(rightRunes)+1)
	curr := make([]int, len(rightRunes)+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(leftRunes); i++ {
		curr[0] = i
		for j := 1; j <= len(rightRunes); j++ {
			cost := 0
			if leftRunes[i-1] != rightRunes[j-1] {
				cost = 1
			}
			deletion := prev[j] + 1
			insertion := curr[j-1] + 1
			substitution := prev[j-1] + cost
			curr[j] = minInt(deletion, insertion, substitution)
		}
		prev, curr = curr, prev
	}

	return prev[len(rightRunes)]
}

func minInt(values ...int) int {
	if len(values) == 0 {
		return 0
	}
	min := values[0]
	for _, value := range values[1:] {
		if value < min {
			min = value
		}
	}
	return min
}

func splitPathSegments(path string) []string {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" || cleaned == "." {
		return nil
	}

	normalized := filepath.ToSlash(cleaned)
	if volume := filepath.VolumeName(cleaned); volume != "" {
		normalized = strings.TrimPrefix(normalized, filepath.ToSlash(volume))
	}
	normalized = strings.TrimPrefix(normalized, "/")
	normalized = strings.TrimPrefix(normalized, "./")
	if normalized == "" || normalized == "." {
		return nil
	}

	parts := strings.Split(normalized, "/")
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "." {
			continue
		}
		segments = append(segments, part)
	}
	if len(segments) == 0 {
		return nil
	}
	return segments
}

func normalizeResolutionBaseDir(baseDir string) string {
	trimmed := strings.TrimSpace(baseDir)
	if trimmed == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return ""
		}
		trimmed = cwd
	}
	if abs, err := filepath.Abs(trimmed); err == nil {
		trimmed = abs
	}
	trimmed = filepath.Clean(trimmed)
	if info, err := os.Stat(trimmed); err == nil && info.IsDir() {
		return trimmed
	}
	return ""
}

func absolutePathRoot(path string) string {
	cleaned := filepath.Clean(strings.TrimSpace(path))
	if cleaned == "" {
		return ""
	}
	if volume := filepath.VolumeName(cleaned); volume != "" {
		return volume + string(filepath.Separator)
	}
	if filepath.IsAbs(cleaned) {
		return string(filepath.Separator)
	}
	return ""
}

func expandTildePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || !strings.HasPrefix(trimmed, "~") {
		return trimmed
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return trimmed
	}
	if trimmed == "~" {
		return home
	}
	if len(trimmed) >= 2 {
		switch trimmed[1] {
		case '/', '\\':
			return filepath.Join(home, trimmed[2:])
		}
	}
	return trimmed
}

func pathExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}
