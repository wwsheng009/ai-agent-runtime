package searchcore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	toolcoreoutput "github.com/wwsheng009/ai-agent-runtime/internal/toolcore/output"
)

type GlobResult struct {
	Matches   []string
	Truncated bool
	// Offset and NextOffset let both surfaces page deterministically.
	Offset     int
	NextOffset int
	HasMore    bool
	// TimedOut marks a page that stopped at the search budget. The collected
	// matches are real; the caller just cannot read them as a complete list.
	TimedOut bool
	// LimitHit reports that the page size or the fetch hard cap cut the list.
	// It is separate from Truncated because the local tool publishes both:
	// `truncated` also covers the byte window, `limit_hit` only the row cap.
	LimitHit bool
	// MatchedTotal is the ordered result count before offset was applied. It
	// lets the caller tell "nothing matched" apart from "the offset is past the
	// end of a non-empty result", which need different recovery guidance.
	MatchedTotal int
	// BraceExpanded/ExpandedPatterns and UnsupportedBracePattern carry the
	// brace recovery state the local tool exposes to the model.
	BraceExpanded           bool
	ExpandedPatterns        []string
	UnsupportedBracePattern bool
}

const (
	// GlobSearchBudget bounds one glob call. Expiry returns the matches
	// collected so far with TimedOut set instead of failing the call.
	GlobSearchBudget = 20 * time.Second
	// GlobFetchHardCap bounds offset+limit for one call: paging is for
	// continuing a listing, not for reading an entire tree into memory.
	GlobFetchHardCap = 5000
	// MaxGlobLimit is the largest page a caller may request.
	MaxGlobLimit = 1000
)

// Glob walks base and returns paths matching pattern. Unreadable subtrees are
// skipped; cancellation is returned to the caller.
func Glob(ctx context.Context, base, pattern string, limit int) (GlobResult, error) {
	return GlobWithOptions(ctx, GlobOptions{Base: base, Pattern: pattern, Limit: limit})
}

type GlobOptions struct {
	Base    string
	Pattern string
	Limit   int
	Offset  int
	// CaseInsensitive matches the pattern without regard to letter case. The
	// local tool has always exposed this as case_insensitive/ignore_case, and
	// finding a file whose casing you do not remember is the main reason to
	// reach for glob.
	CaseInsensitive bool
	// SortByModTime orders matches newest-first (equal mtimes fall back to the
	// path) instead of the default name order. This is the local tool's order:
	// the files a caller is usually looking for are the ones just touched.
	SortByModTime bool
	// Access is the I/O seam used for the timestamps that ordering needs. nil means
	// this machine's filesystem, exactly like GrepOptions.Access: an unconverted
	// caller keeps the behaviour it always had.
	Access toolcore.Access
}

// GlobWithOptions runs one glob. The pattern is expanded for shell braces
// first, the walker and the optional ripgrep engine both enforce the same
// options, and the page is ordered before it is cut.
func GlobWithOptions(ctx context.Context, opts GlobOptions) (GlobResult, error) {
	base := opts.Base
	pattern := strings.TrimSpace(opts.Pattern)
	limit := opts.Limit
	if limit < 1 {
		limit = DefaultGlobLimit
	}
	if limit > MaxGlobLimit {
		limit = MaxGlobLimit
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	if opts.Offset > GlobFetchHardCap {
		opts.Offset = GlobFetchHardCap
	}
	// The fetch window must cover offset+limit so a deep page does not silently
	// become "the newest entries of whatever the walk happened to reach".
	fetchLimit := opts.Offset + limit
	fetchClipped := false
	if fetchLimit > GlobFetchHardCap {
		fetchLimit = GlobFetchHardCap
		fetchClipped = opts.Offset+limit > GlobFetchHardCap
	}

	expanded := ExpandGlobBraces(pattern)
	if len(expanded) == 0 {
		expanded = []string{pattern}
	}
	braceExpanded := HasGlobBraceAlternatives(pattern) && len(expanded) > 1
	braceUnsupported := HasGlobBraceAlternatives(pattern) && !braceExpanded
	if braceUnsupported {
		// The pattern could not be expanded safely (too many alternatives or a
		// malformed group). Matching it literally would find nothing and look
		// like a legitimate empty result; report the unsupported shape instead
		// and let the caller split the pattern. No search is performed, exactly
		// as the local tool ends up with no match for an unexpanded brace.
		return GlobResult{
			Offset:                  opts.Offset,
			NextOffset:              opts.Offset,
			ExpandedPatterns:        append([]string(nil), expanded...),
			UnsupportedBracePattern: true,
		}, nil
	}

	searchCtx := ctx
	if searchCtx == nil {
		searchCtx = context.Background()
	}
	budgetCtx, cancel := context.WithTimeout(searchCtx, GlobSearchBudget)
	defer cancel()

	// A single-file root is its own match set: `path: "a/b.go"` with any
	// pattern that names the file now returns it, which is what the local tool
	// has always done. Walking from a file root would return nothing.
	if info, err := os.Stat(base); err == nil && !info.IsDir() {
		result := GlobResult{}
		name := filepath.Base(filepath.Clean(base))
		for _, candidate := range expanded {
			matcher, matchErr := NewGlobMatcher(candidate)
			if matchErr != nil {
				return GlobResult{}, matchErr
			}
			matcher.caseInsensitive = opts.CaseInsensitive
			if !matcher.MatchRelative(name) {
				continue
			}
			// The caller's `path` names the file, so it is one row no matter
			// which brace alternative matched it.
			if opts.Offset == 0 {
				result.Matches = []string{base}
			}
			break
		}
		result.Offset = opts.Offset
		result.NextOffset = opts.Offset + len(result.Matches)
		result.BraceExpanded = braceExpanded
		result.ExpandedPatterns = append([]string(nil), expanded...)
		result.UnsupportedBracePattern = braceUnsupported
		return result, nil
	}

	matches, timedOut, walkErr := walkGlobMatches(budgetCtx, base, expanded, fetchLimit, opts)
	if walkErr != nil {
		return GlobResult{}, walkErr
	}
	result := GlobResult{Matches: matches, TimedOut: timedOut}
	// Order before paging. Paging a list and sorting the page afterwards would
	// give every page its own order, so next_offset could not describe a stable
	// sequence.
	if opts.SortByModTime {
		sortGlobMatchesByModTime(ctx, seamAccess(opts.Access), result.Matches)
	} else {
		sort.Strings(result.Matches)
	}
	result.MatchedTotal = len(result.Matches)
	// Only cut to the fetch window after ordering. Cutting a walk-order prefix
	// and sorting it afterwards made page 2 of an mtime-ordered listing repeat
	// entries: the prefix is not the newest prefix, it is just the first paths
	// the walk happened to visit.
	truncatedByFetch := len(result.Matches) > fetchLimit
	if truncatedByFetch {
		result.Matches = result.Matches[:fetchLimit]
	}
	if opts.Offset > 0 {
		if opts.Offset >= len(result.Matches) {
			result.Matches = nil
		} else {
			result.Matches = result.Matches[opts.Offset:]
		}
	}
	if len(result.Matches) > limit {
		result.Matches = result.Matches[:limit]
		result.LimitHit = true
	}
	if truncatedByFetch || fetchClipped {
		result.LimitHit = true
	}
	result.Truncated = result.LimitHit
	result.Offset = opts.Offset
	result.NextOffset = opts.Offset + len(result.Matches)
	result.HasMore = result.Truncated
	result.BraceExpanded = braceExpanded
	result.ExpandedPatterns = append([]string(nil), expanded...)
	result.UnsupportedBracePattern = braceUnsupported
	return result, nil
}

// walkGlobMatches is the shared pure-Go traversal. It unions the brace
// alternatives, keeps directories when the caller allows them, never follows a
// symlink, and returns what it collected when the budget expires.
func walkGlobMatches(ctx context.Context, base string, patterns []string, fetchLimit int, opts GlobOptions) ([]string, bool, error) {
	matchers := make([]*GlobMatcher, 0, len(patterns))
	for _, pattern := range patterns {
		matcher, err := NewGlobMatcher(pattern)
		if err != nil {
			return nil, false, err
		}
		matcher.caseInsensitive = opts.CaseInsensitive
		matchers = append(matchers, matcher)
	}
	matches := make([]string, 0, 64)
	seen := make(map[string]struct{}, 64)
	timedOut := false
	// WalkDir reports the root itself; it is not a candidate (a glob pattern
	// describes children of the base, never the base).
	walkErr := filepath.WalkDir(base, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			// Unreadable subtrees are skipped, not fatal: a permission error on
			// one directory must not hide the rest of the tree.
			return nil
		}
		if ctx != nil && ctx.Err() != nil {
			timedOut = true
			return filepath.SkipAll
		}
		if path == base {
			return nil
		}
		// Traversal never follows links. A discovered link can point outside
		// the admitted root, so listing it would leak paths the path jail has
		// not resolved.
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, relErr := filepath.Rel(base, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		for _, matcher := range matchers {
			// The glob tool matches the root-relative path with standard glob
			// depth rules: `*.go` is depth 1, `**/*.go` recurses.
			if !matcher.MatchRelative(rel) {
				continue
			}
			if _, ok := seen[path]; ok {
				break
			}
			seen[path] = struct{}{}
			matches = append(matches, path)
			break
		}
		// Stop early only while the walk order is the page order. With name
		// ordering, the first N names visited are the first N of the page.
		// With mtime ordering the list is sorted after the walk, so stopping
		// at N would keep an arbitrary prefix and then sort it - "newest
		// page" would silently mean "newest among those visited first".
		//
		// Directories count as matches exactly like files: `**/docs` finds
		// docs/, which is what the local tool has always returned. There is no
		// hardcoded skipped tree list either - the same policy as the local
		// glob tool.
		if !opts.SortByModTime && len(matches) > fetchLimit {
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil && walkErr != filepath.SkipAll {
		return nil, false, walkErr
	}
	return matches, timedOut, nil
}

// maxGlobBraceExpansion bounds one pattern's alternatives. Past it the pattern
// is left alone so the caller sees the unsupported-brace guidance instead of a
// silently partial expansion (matching the local tool's fail-closed rule).
const maxGlobBraceExpansion = 64

// ExpandGlobBraces expands shell-style brace alternatives the way the local
// glob tool always has: `*.{go,ts}` becomes `*.go` and `*.ts`, nesting expands
// left to right, and a pattern whose expansion would exceed the cap is returned
// unchanged rather than partially expanded.
func ExpandGlobBraces(pattern string) []string {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil
	}
	pending := []string{pattern}
	expanded := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	for len(pending) > 0 {
		current := pending[0]
		pending = pending[1:]
		open, close := findGlobBraceRange(current)
		if open < 0 {
			if _, ok := seen[current]; ok {
				continue
			}
			seen[current] = struct{}{}
			expanded = append(expanded, current)
			continue
		}
		alternatives := splitGlobBraceAlternatives(current[open+1 : close])
		if len(alternatives) <= 1 {
			return []string{pattern}
		}
		if len(expanded)+len(pending)+len(alternatives) > maxGlobBraceExpansion {
			return []string{pattern}
		}
		prefix := current[:open]
		suffix := current[close+1:]
		for _, alternative := range alternatives {
			pending = append(pending, prefix+alternative+suffix)
		}
	}
	if len(expanded) == 0 {
		return []string{pattern}
	}
	return expanded
}

// HasGlobBraceAlternatives reports a pattern that looks like a shell brace
// expansion, whether or not it could be expanded. It is the trigger for the
// recovery guidance the model sees.
func HasGlobBraceAlternatives(pattern string) bool {
	open := strings.Index(pattern, "{")
	close := strings.LastIndex(pattern, "}")
	if open < 0 || close <= open {
		return false
	}
	return strings.Contains(pattern[open+1:close], ",")
}

// validateGlobPatternShape rejects a pattern the root-relative matcher could
// never satisfy: absolute spellings and segments that climb above the search
// root. Returning an error is honest; matching nothing would look like a
// legitimate empty result and invite the caller to retry the same call.
func validateGlobPatternShape(pattern string) error {
	trimmed := strings.TrimSpace(pattern)
	if trimmed == "" {
		return nil
	}
	normalized := strings.ReplaceAll(trimmed, `\`, "/")
	if strings.HasPrefix(normalized, "/") || filepath.IsAbs(trimmed) {
		return fmt.Errorf("sandbox policy requires a relative pattern")
	}
	depth := 0
	for _, segment := range strings.Split(normalized, "/") {
		switch segment {
		case "", ".":
			continue
		case "..":
			depth--
			if depth < 0 {
				return fmt.Errorf("sandbox policy forbids escaping the search path")
			}
		default:
			depth++
		}
	}
	return nil
}

// findGlobBraceRange returns the leftmost {...} span holding a top-level comma.
func findGlobBraceRange(pattern string) (open, close int) {
	for i := 0; i < len(pattern); i++ {
		if pattern[i] != '{' {
			continue
		}
		depth := 1
		hasComma := false
		for j := i + 1; j < len(pattern); j++ {
			switch pattern[j] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					if hasComma {
						return i, j
					}
					i = j
				}
			case ',':
				if depth == 1 {
					hasComma = true
				}
			}
			if depth == 0 {
				break
			}
		}
	}
	return -1, -1
}

func splitGlobBraceAlternatives(inner string) []string {
	if inner == "" {
		return []string{""}
	}
	parts := make([]string, 0, 4)
	start := 0
	depth := 0
	for i := 0; i < len(inner); i++ {
		switch inner[i] {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, inner[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, inner[start:])
	return parts
}

// renderGlobPage writes the page in the local tool's shape: one relative path
// per line, no header. It returns the rows it actually printed, so the caller's
// metadata counts the delivered rows rather than the rows it hoped to deliver.
//
// The byte window is reserved before the list is written, exactly as the local
// tool does: the notice length is subtracted first, so a page that hits the
// budget still ends with a usable next_offset instead of a dangling list.
func renderGlobPage(builder *strings.Builder, paths toolcore.PathResolver, result GlobResult, offset, budget int) ([]string, bool) {
	lines := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		lines = append(lines, paths.Relative(match))
	}
	if len(lines) == 0 {
		builder.WriteString("未找到匹配项")
		return nil, false
	}
	// Reserve the larger of the two notice shapes: a row cut by the byte window
	// switches to the range shape, so reserving only the shape that would have
	// been printed could overflow by a few bytes.
	noticeReserve := len(globTruncationNotice(len(lines), offset+len(lines)))
	if candidate := len(globPaginationNotice(offset, offset+len(lines), offset+len(lines))); candidate > noticeReserve {
		noticeReserve = candidate
	}
	listingBudget := budget - noticeReserve
	if budget <= 0 {
		listingBudget = 0
	}
	if listingBudget < 0 {
		listingBudget = 0
	}
	used := 0
	kept := 0
	for _, line := range lines {
		lineBytes := len(line) + 1
		if budget > 0 && kept > 0 && used+lineBytes > listingBudget {
			break
		}
		used += lineBytes
		kept++
	}
	trimmed := kept < len(lines)
	shown := lines[:kept]
	builder.WriteString(strings.Join(shown, "\n"))
	// Only a cut list gets a notice. A complete page that merely has more rows
	// behind it must stay a bare path list: appending "next_offset" to a
	// complete page made `glob "*.go"` look truncated when it was not.
	if !result.Truncated {
		return shown, trimmed
	}
	if trimmed {
		// Re-render the notice against the rows actually shown: the row count,
		// the window range and next_offset must all describe this page.
		next := offset + kept
		if offset > 0 {
			builder.WriteString(globPaginationNotice(offset, next, next))
		} else {
			builder.WriteString(globTruncationNotice(kept, next))
		}
		return shown, trimmed
	}
	next := offset + len(shown)
	if offset > 0 {
		builder.WriteString(globPaginationNotice(offset, next, next))
	} else {
		builder.WriteString(globTruncationNotice(len(shown), next))
	}
	return shown, trimmed
}

func globTruncationNotice(shown, next int) string {
	return fmt.Sprintf("\n\n(已分页显示前 %d 个文件；next_offset=%d 可继续分页)", shown, next)
}

func globPaginationNotice(start, end, next int) string {
	return fmt.Sprintf("\n(显示第 %d-%d 个文件；next_offset=%d 可继续分页)", start+1, end, next)
}

// GlobOutputBudgetBytes is glob's model-visible window, owned by the shared
// output package so the handler and the render layer agree on one number.
const GlobOutputBudgetBytes = toolcoreoutput.GlobOutputBudgetBytes

// globTimeoutNotice marks a page that stopped at the search budget, so the
// model does not read a partial list as a complete one.
func globTimeoutNotice() string {
	return "\n(搜索预算 20s 内未完成：以上是已收集的部分结果；请收窄 path/pattern 后重试，或改用 shell 运行 rg --files)"
}

// sortGlobMatchesByModTime orders newest first, breaking ties by path so the
// sequence is stable across runs. A match that cannot be stat'ed sorts last
// rather than being dropped: it still is a real match.
//
// The timestamps come from the accessor, not from this process's disk: the list being
// ordered is a list of paths that accessor enumerated, and ordering remote files by
// local mtimes would produce a confidently wrong order rather than a degraded one.
func sortGlobMatchesByModTime(ctx context.Context, access toolcore.Access, matches []string) {
	type entry struct {
		path    string
		modTime time.Time
	}
	entries := make([]entry, 0, len(matches))
	for _, match := range matches {
		var mod time.Time
		if info, err := access.Stat(ctx, match); err == nil {
			mod = info.ModTime()
		}
		entries = append(entries, entry{path: match, modTime: mod})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].modTime.Equal(entries[j].modTime) {
			return entries[i].path < entries[j].path
		}
		return entries[i].modTime.After(entries[j].modTime)
	})
	for i, item := range entries {
		matches[i] = item.path
	}
}
