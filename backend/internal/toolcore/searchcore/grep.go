package searchcore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultGrepLimit = 200
	MaxGrepLineBytes = 2048
)

type GrepOptions struct {
	Pattern string
	// Patterns is the batch spelling (rg -e ...). When it is non-empty the
	// single-value Pattern is ignored, matching the handler's precedence.
	Patterns []string
	// PatternFiles holds absolute paths whose lines are appended to Patterns as
	// one pattern each (rg -f). A blank line stays an empty regex, which matches
	// every line - the long-standing local behaviour.
	PatternFiles []string
	Path         string
	Glob         string
	// Globs carries the array spelling of `glob`: several patterns, any may
	// match, `!`-prefixed ones exclude (the local tool's partition rule).
	// The single-value Glob joins the same list when set.
	Globs []string
	// Include/Exclude are file filters applied to the path relative to the
	// search root (a bare file name for a single-file root). Include is an
	// allow-list; exclude wins over include. Patterns without a slash match
	// the basename anywhere below the root.
	Include []string
	Exclude []string
	// Paths holds additional search roots, searched in order after Path. The
	// ordered pair mirrors the local tool: `path` first, then `paths`.
	Paths []string
	// Mode selects the output unit (content lines, files, per-file counts) and
	// with it the pagination unit. Empty means content.
	Mode GrepMode
	// Type/TypeNot are ripgrep file-type names; they are folded into the
	// include/exclude filters through the shared FileTypeToGlob table so both
	// engines agree on what `type=go` means.
	Type    string
	TypeNot string
	// MaxCount caps matching lines per file. MaxCountSet distinguishes an
	// explicit 0 - the file contributes nothing, matching rg -m 0 - from
	// "unset", which means no per-file cap.
	MaxCount    int
	MaxCountSet bool
	// OnlyMatching emits one entry per matched fragment instead of the whole
	// line. InvertMatch keeps whole lines (rg -o -v), files mode ignores it,
	// and in count mode the per-file count becomes a match count (rg -o -c).
	OnlyMatching bool
	// CountMatches makes count mode count occurrences instead of matching
	// lines (rg --count-matches). It is the same arithmetic only_matching
	// already produces in count mode, so it folds into OnlyMatching rather
	// than becoming a third counting rule.
	CountMatches bool
	// GlobCaseInsensitive applies case-insensitive matching to the include/
	// exclude/glob patterns (rg --glob-case-insensitive). It never changes how
	// the search pattern itself is compared.
	GlobCaseInsensitive bool
	// IgnoreFileCaseInsensitive applies case-insensitive matching to the rules
	// read from an explicit ignore_file only (rg --ignore-file-case-insensitive).
	// The conventionally discovered ignore files keep their normal semantics.
	IgnoreFileCaseInsensitive bool
	// Column renders `path:line:column: text` instead of `path:line: text`.
	// It adds real information (where on the line the match starts), so it is
	// a search parameter, not a style switch.
	Column bool
	// Trim strips leading indentation from the rendered line only. Column
	// numbers are computed on the original line, exactly like rg --trim.
	Trim bool
	// MaxColumns replaces an over-long rendered line with a marker. Zero means
	// no limit; MaxColumnsSet distinguishes "unset" from an explicit 0.
	// MaxColumnsPreview keeps a prefix instead of omitting the line entirely.
	MaxColumns        int
	MaxColumnsSet     bool
	MaxColumnsPreview bool
	// MaxDepth bounds directory descent relative to each search root: 0 means
	// the roots' immediate contents only. MaxDepthSet distinguishes unset from
	// an explicit 0, mirroring the local tool.
	MaxDepth    int
	MaxDepthSet bool
	// MaxFileSizeBytes skips files larger than this. It is the user-facing
	// `max_filesize` filter; MaxFileBytes is the runtime capture budget and the
	// two are applied independently.
	MaxFileSizeBytes int64
	// OneFileSystem keeps the walk inside the filesystem each search root lives
	// on: a directory on another device is not descended. It only bounds
	// recursion - a root the caller names explicitly is always searched, which
	// is exactly what rg --one-file-system does.
	OneFileSystem bool
	// IncludeHidden searches dotfiles and dot-directories. The tool default is
	// false on every surface: hidden content is skipped unless the caller asks
	// for it, which is both the long-standing local behaviour and ripgrep's own
	// default.
	IncludeHidden bool
	// Unrestricted is rg's -u/-uu/-uuu ladder. Only the top level changes which
	// bytes a search can see: 1 and 2 are expressible as NoIgnore/IncludeHidden
	// (the handler already folds them), while 3 additionally lets a file be
	// searched past its first NUL byte. Under the default the file ends at the
	// first NUL, exactly like ripgrep; under 3 the remaining lines are searched
	// but the NUL itself is never emitted (rg --binary also stops the *display*
	// at the NUL, and a raw NUL in tool output is unreadable to a model).
	Unrestricted int
	// Ignore-file handling follows the local tool: `.ignore`/`.rgignore`,
	// `.gitignore`, `.git/info/exclude`, ancestor directories and the two global
	// candidates are consulted unless a switch turns that source off, and
	// NoIgnore turns the whole mechanism off. IgnoreFiles holds absolute paths
	// from the `ignore_file(s)` argument.
	NoIgnore       bool
	NoIgnoreParent bool
	NoIgnoreVCS    bool
	NoIgnoreDot    bool
	NoIgnoreGlobal bool
	NoIgnoreFiles  bool
	IgnoreFiles    []string
	// NoMessages decides what an unreadable *explicit* ignore_file does. The
	// local tool has always treated an explicit file as a promise: if it cannot
	// be read, the caller gets an error instead of a silently different rule
	// set. NoMessages turns that into a silent skip, which is what rg
	// --no-messages is for. Discovered ignore files (.gitignore, .ignore, the
	// globals) are always optional and are never reported either way.
	NoMessages bool
	// CaseInsensitive is the legacy alias kept for callers that predate
	// IgnoreCase; it is folded into IgnoreCase by the pattern options.
	CaseInsensitive bool
	Literal         bool
	IgnoreCase      bool
	CaseSensitive   bool
	SmartCase       bool
	Word            bool
	LineRegexp      bool
	InvertMatch     bool
	Limit           int
	MaxFileBytes    int
	// Offset skips the first N matches before applying Limit. It is the
	// pagination cursor shared by local and remote grep.
	Offset int
	// IgnoredPresentation lists the presentation-only rg flags the caller set
	// (line numbers, headings, colours, buffering, NUL separators, ...). The
	// tool's output shape already satisfies them, so they change nothing; they
	// are reported so a caller can see that its rg spelling was understood and
	// deliberately ignored rather than silently dropped.
	IgnoredPresentation []string
}

type GrepMatch struct {
	File string
	Line int
	// Column is the 1-based byte column where the match starts: always set for
	// only_matching fragments, and set for whole lines when Column was asked
	// for. Zero means "not computed" (invert_match, or column not requested).
	Column int
	// Text is the raw line or fragment. Rendering applies trim/max_columns to
	// a copy, so the raw text stays available for counting and metadata.
	Text string
}

type GrepResult struct {
	Matches []GrepMatch
	// Files carries the page for files/count modes; Matches keeps the raw
	// matches those entries were summarized from (including the one file the
	// engines read past the page as has_more evidence).
	Files        []GrepFileSummary
	Mode         GrepMode
	FilesScanned int
	SingleFile   bool
	Truncated    bool
}

// Grep searches an authorized path with a bounded line reader. Binary-looking
// files are skipped without failing the whole search.
func Grep(ctx context.Context, opts GrepOptions) (GrepResult, error) {
	if opts.Limit < 1 {
		opts.Limit = DefaultGrepLimit
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	mode, err := NormalizeGrepMode(string(opts.Mode))
	if err != nil {
		return GrepResult{}, err
	}
	patterns, err := GrepPatternsWithFiles(opts)
	if err != nil {
		return GrepResult{}, err
	}
	if len(patterns) == 0 {
		// An empty pattern file is a valid zero-match search (rg -f with no
		// patterns matches nothing), not a missing-pattern error.
		if len(opts.PatternFiles) > 0 {
			var empty GrepResult
			ApplyGrepPageWindow(&empty, mode, opts)
			return empty, nil
		}
		return GrepResult{}, fmt.Errorf("pattern 参数缺失或无效")
	}
	compiled, err := CompileGrepPattern(patterns, GrepPatternOptions{
		Literal:       opts.Literal,
		IgnoreCase:    opts.IgnoreCase || opts.CaseInsensitive,
		CaseSensitive: opts.CaseSensitive,
		SmartCase:     opts.SmartCase,
		Word:          opts.Word,
		LineRegexp:    opts.LineRegexp,
	})
	if err != nil {
		return GrepResult{}, err
	}
	var globMatcher *grepGlobFilter
	globPatterns := append([]string{}, opts.Globs...)
	if strings.TrimSpace(opts.Glob) != "" {
		globPatterns = append(globPatterns, opts.Glob)
	}
	var excludeFromGlobs []string
	includeGlobsFromParam := make([]string, 0, len(globPatterns))
	for _, pattern := range globPatterns {
		trimmed := strings.TrimSpace(pattern)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "!") {
			excludeFromGlobs = append(excludeFromGlobs, strings.TrimSpace(strings.TrimPrefix(trimmed, "!")))
			continue
		}
		includeGlobsFromParam = append(includeGlobsFromParam, trimmed)
	}
	if len(includeGlobsFromParam) > 0 {
		globMatcher, err = newGrepGlobFilterWithCase(includeGlobsFromParam, opts.GlobCaseInsensitive)
		if err != nil {
			return GrepResult{}, err
		}
	}
	if len(excludeFromGlobs) > 0 {
		opts.Exclude = append(append([]string{}, opts.Exclude...), excludeFromGlobs...)
	}
	includeGlobs, excludeGlobs := EffectiveGrepFilters(opts)
	includeFilter, err := newGrepGlobFilterWithCase(includeGlobs, opts.GlobCaseInsensitive)
	if err != nil {
		return GrepResult{}, err
	}
	excludeFilter, err := newGrepGlobFilterWithCase(excludeGlobs, opts.GlobCaseInsensitive)
	if err != nil {
		return GrepResult{}, err
	}

	var result GrepResult
	roots := grepRoots(opts)
	if len(roots) == 1 {
		if info, statErr := os.Stat(roots[0]); statErr == nil && info.Mode().IsRegular() {
			result.SingleFile = true
		}
	}
	reader := newLineReader()
	// files_without inverts the unit of the page: the entry is a file proven to
	// have no matching line, so matches are not collected and the page check
	// counts proven files instead.
	filesWithout := mode == GrepModeFilesWithout
	visit := func(root string, rules *grepIgnoreRules, path string) error {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		// The explicit root may be a symlink that the surface adapter already
		// resolved and verified. Links discovered during traversal are rejected
		// by the WalkDir callback below and never reach this visit.
		info, statErr := os.Stat(path)
		if statErr != nil || !info.Mode().IsRegular() {
			return nil
		}
		if opts.MaxFileBytes > 0 && info.Size() > int64(opts.MaxFileBytes) {
			return nil
		}
		if opts.MaxFileSizeBytes > 0 && info.Size() > opts.MaxFileSizeBytes {
			return nil
		}
		if !opts.IncludeHidden && isHiddenGrepPath(relativeGrepCandidate(root, path)) {
			return nil
		}
		if rules != nil && ignoredByGrepRules(rules.rulesFor(path), path) {
			return nil
		}
		if shouldFilterGrepCandidate(relativeGrepCandidate(root, path), includeFilter, excludeFilter) {
			return nil
		}
		if filesWithout && opts.MaxCountSet && opts.MaxCount == 0 {
			// Local contract for an explicit `max_count: 0`: the file
			// contributes no match at all, so every candidate is a
			// no-match file. ripgrep's -m 0 disagrees (it prints nothing),
			// which is why that case never reaches the ripgrep engine.
			result.FilesScanned++
			result.Files = append(result.Files, GrepFileSummary{File: path})
			if GrepFilesWithoutComplete(result.Files, opts) {
				return filepath.SkipAll
			}
			return nil
		}
		if opts.Unrestricted < 3 && grepFileIsBinary(path) {
			// ripgrep's default: a file with a NUL byte is skipped outright.
			// It is neither a match nor a proven no-match file, so it is
			// reported to neither mode. -uuu (Unrestricted 3) searches it.
			return nil
		}
		result.FilesScanned++
		file, openErr := os.Open(path)
		if openErr != nil {
			return nil
		}
		defer file.Close()
		reader.reset(file)
		fileLines := 0
		hasMatch := false
		for {
			lineNo, text, truncated, ok := reader.next()
			if !ok {
				break
			}
			display := reader.displayText(text, truncated)
			if !validUTF8(display) {
				return nil
			}
			matched := compiled.Match(display)
			if matched == opts.InvertMatch {
				continue
			}
			hasMatch = true
			if filesWithout {
				// One matching line is enough to prove the file belongs on the
				// other side; the rest of it does not need reading.
				break
			}
			if opts.MaxCountSet && fileLines >= opts.MaxCount {
				break
			}
			// Fragments are the output unit when only_matching is on. Invert
			// keeps whole lines, exactly like rg -o -v.
			if opts.OnlyMatching && !opts.InvertMatch && mode != GrepModeFiles {
				for _, loc := range compiled.FindAllIndex(display, -1) {
					result.Matches = append(result.Matches, GrepMatch{
						File:   path,
						Line:   lineNo,
						Column: loc[0] + 1,
						Text:   sanitizeGrepNulText(string(display[loc[0]:loc[1]])),
					})
					if GrepPageComplete(result.Matches, mode, opts) {
						return filepath.SkipAll
					}
				}
			} else {
				// Column is computed on the original line, so Trim can drop the
				// indentation without moving the reported position.
				column := 0
				if opts.Column && !opts.InvertMatch {
					if loc := compiled.FindIndex(display); len(loc) == 2 {
						column = loc[0] + 1
					}
				}
				result.Matches = append(result.Matches, GrepMatch{
					File:   path,
					Line:   lineNo,
					Column: column,
					Text:   sanitizeGrepNulText(string(display)),
				})
			}
			fileLines++
			if GrepPageComplete(result.Matches, mode, opts) {
				return filepath.SkipAll
			}
		}
		if filesWithout {
			if hasMatch {
				return nil
			}
			result.Files = append(result.Files, GrepFileSummary{File: path})
			if GrepFilesWithoutComplete(result.Files, opts) {
				return filepath.SkipAll
			}
		}
		return nil
	}

	for _, root := range roots {
		rules := newGrepIgnoreRules(newGrepIgnorePolicy(opts), root)
		if info, statErr := os.Stat(root); statErr == nil && info.Mode().IsRegular() {
			if globMatcher == nil || globMatcher.matches(filepath.Base(root)) {
				if err := visit(root, rules, root); err != nil && err != filepath.SkipAll {
					return GrepResult{}, err
				}
			}
		} else {
			rootDevice := ""
			if opts.OneFileSystem {
				// The root itself is always searched: the boundary only stops
				// the walk from entering a *different* filesystem below it.
				identity, identityErr := FilesystemIdentityFunc(root)
				if identityErr != nil {
					return GrepResult{}, identityErr
				}
				rootDevice = identity
			}
			walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				// File symlinks are not followed: a link inside the workspace must
				// not read a target outside the admitted roots.
				if entry.Type()&os.ModeSymlink != 0 {
					return nil
				}
				if entry.IsDir() {
					if rootDevice != "" && path != root {
						// A mount point on another filesystem: do not descend.
						// The directory entry itself was already returned by
						// the walk, and skipping it here keeps its files out.
						identity, identityErr := FilesystemIdentityFunc(path)
						if identityErr != nil {
							return identityErr
						}
						if identity != rootDevice {
							return filepath.SkipDir
						}
					}
					// Hidden directories are skipped wholesale, so a search
					// never descends into .git or a hidden cache tree. The
					// explicitly requested root is exempt: asking for `.config`
					// must work.
					if !opts.IncludeHidden && path != root && strings.HasPrefix(entry.Name(), ".") {
						return filepath.SkipDir
					}
					if rules != nil && ignoredByGrepRules(rules.rulesFor(path), path) {
						return filepath.SkipDir
					}
					// Depth mirrors the local tool: a direct child directory has
					// depth 1, so max_depth=0 searches only the root's own files.
					if opts.MaxDepthSet {
						if rel, err := filepath.Rel(root, path); err == nil && rel != "." {
							depth := strings.Count(filepath.ToSlash(rel), "/") + 1
							if depth > opts.MaxDepth {
								return filepath.SkipDir
							}
						}
					}
					return nil
				}
				// `glob` is path-aware: a pattern with a slash matches the
				// root-relative path, a bare pattern matches the basename. This is
				// the local tool's rule (verified against rg: `pkg/*.go` does not
				// match pkg/nested/deep.go).
				if globMatcher != nil && !globMatcher.matches(relativeGrepCandidate(root, path)) {
					return nil
				}
				return visit(root, rules, path)
			})
			if walkErr != nil && walkErr != filepath.SkipAll {
				return GrepResult{}, walkErr
			}
		}
		// Every root after the page is complete would only be scanned to be
		// discarded by ApplyGrepWindow.
		if GrepResultPageComplete(&result, mode, opts) {
			break
		}
	}

	ApplyGrepPageWindow(&result, mode, opts)
	return result, nil
}

// GrepPatternsWithFiles returns the effective pattern list: the direct
// spellings followed by every line of each pattern file. Direct spellings win
// in order, exactly like the local tool's loadPatternFiles.
func GrepPatternsWithFiles(opts GrepOptions) ([]string, error) {
	patterns := grepPatterns(opts)
	if len(opts.PatternFiles) == 0 {
		return patterns, nil
	}
	out := make([]string, 0, len(patterns)+8)
	out = append(out, patterns...)
	for _, path := range opts.PatternFiles {
		lines, err := readGrepPatternFile(path)
		if err != nil {
			if opts.NoMessages {
				// Same rule as an explicit ignore_file: no_messages turns a
				// missing/unreadable auxiliary input into a skip.
				continue
			}
			return nil, fmt.Errorf("读取 pattern_file 失败 %s: %w", path, err)
		}
		out = append(out, lines...)
	}
	return out, nil
}

// readGrepPatternFile reads one pattern file through the shared bounded line
// reader, so a huge line cannot blow memory and line numbering stays stable.
func readGrepPatternFile(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := newLineReader()
	reader.reset(file)
	lines := make([]string, 0, 16)
	for {
		_, text, truncated, ok := reader.next()
		if !ok {
			return lines, nil
		}
		line := string(reader.displayText(text, truncated))
		lines = append(lines, line)
	}
}

// grepRoots returns the ordered search roots: the single spelling first, then
// the batch spelling, exactly like the local tool builds searchPaths.
func grepRoots(opts GrepOptions) []string {
	roots := make([]string, 0, 1+len(opts.Paths))
	if strings.TrimSpace(opts.Path) != "" {
		roots = append(roots, opts.Path)
	}
	for _, path := range opts.Paths {
		if strings.TrimSpace(path) != "" {
			roots = append(roots, path)
		}
	}
	return roots
}

// relativeGrepCandidate is the path an include/exclude pattern is matched
// against: the slash path relative to the configured search root, or the bare
// file name when the root itself is the searched file.
func relativeGrepCandidate(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Base(path)
	}
	return filepath.ToSlash(rel)
}

// isHiddenGrepPath reports whether any segment of the relative candidate starts
// with a dot, which is exactly what ripgrep treats as hidden.
func isHiddenGrepPath(candidate string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(candidate), "/") {
		if segment == "" || segment == "." || segment == ".." {
			continue
		}
		if strings.HasPrefix(segment, ".") {
			return true
		}
	}
	return false
}

// grepPatterns returns the pattern list a GrepOptions carries, preferring the
// batch spelling exactly like the handler does.
func grepPatterns(opts GrepOptions) []string {
	if len(opts.Patterns) > 0 {
		return opts.Patterns
	}
	if opts.Pattern != "" {
		return []string{opts.Pattern}
	}
	return nil
}

// ApplyGrepWindow applies the shared offset/limit window to raw matches.
// Every grep engine (pure-Go walker, ripgrep runner) funnels its raw matches
// through this one function, so pagination cannot diverge by engine.
func ApplyGrepWindow(result *GrepResult, opts GrepOptions) {
	if result == nil {
		return
	}
	if opts.Limit < 1 {
		opts.Limit = DefaultGrepLimit
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	if opts.Offset > 0 {
		if opts.Offset >= len(result.Matches) {
			result.Matches = nil
			return
		}
		result.Matches = result.Matches[opts.Offset:]
	}
	if len(result.Matches) > opts.Limit {
		result.Matches = result.Matches[:opts.Limit]
		result.Truncated = true
	}
}
