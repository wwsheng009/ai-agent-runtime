package searchcore

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	DefaultGrepLimit = 200
	MaxGrepLineBytes = 2048
	// GrepUnlimitedPageSize is what an explicit "no page cap" resolves to. The
	// local tool has always read head_limit=0 that way (unlimited up to a safety
	// ceiling), and the same ceiling bounds both spellings of the page size here,
	// so one rule serves limit and head_limit.
	GrepUnlimitedPageSize = 5000
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
	// Engine selection. A non-default engine (PCRE2, auto-hybrid-regex) and
	// multiline matching are ripgrep-only capabilities: the pure-Go walker cannot
	// reproduce them, so a call that asks for one has to reach ripgrep first.
	Pcre2           bool
	Engine          string
	AutoHybridRegex bool
	Multiline       bool
	MultilineDotall bool
	// CRLF asks ripgrep to treat CRLF as the line terminator. The pure-Go walker
	// already does that (its reader trims the CR), so this flag exists to make the
	// two engines agree instead of differing by an invisible byte.
	CRLF bool
	// NoConfig / NoConfigSet carry the `no_config` request. The engine asks
	// ripgrep not to read its own configuration by default - a shared tool must
	// not answer differently on two machines because one has a ~/.ripgreprc - and
	// `no_config=false` is the caller explicitly asking for that inheritance, so
	// the flag is simply not passed. NoConfigSet distinguishes "unset" (keep the
	// deterministic default) from "false" (allow the config).
	NoConfig    bool
	NoConfigSet bool
	// Replace is the rg --replace/-r template. A non-empty template switches the
	// rendered content to the substituted text, and both engines implement it:
	// ripgrep is asked to substitute by itself (its JSON event stream carries
	// match offsets but no capture-group offsets, so `$1`/`${name}` cannot be
	// re-expanded from it), while the walker expands the same template through
	// Go's regexp - so a host without rg keeps the ability instead of losing it.
	Replace string
	// ContextSeparator / ContextSeparatorSet / NoContextSeparator carry the
	// `context_separator` / `no_context_separator` request. The separator is
	// rendered by the shared renderer rather than passed to ripgrep: the walker
	// has no such flag to forward, and one renderer for both engines is what
	// keeps the group boundaries identical there.
	ContextSeparator    string
	ContextSeparatorSet bool
	NoContextSeparator  bool
	// Follow makes the traversal descend through links that resolve to
	// directories and search links that resolve to files - rg's --follow. Both
	// engines implement it: the walker follows the links itself (with cycle
	// protection), ripgrep is asked for its own flag, so the flag is never the
	// one thing a host without rg cannot do.
	Follow bool
	// TypeAdd / TypeClear modify the file-type table for this call. Both are
	// resolved by the shared core into include/exclude globs, exactly like
	// Type/TypeNot, so both engines search the same set: type_add extends (or
	// defines) a type, type_clear removes a definition, and clearing wins when
	// the same name appears in both - the order the local tool has always sent
	// to ripgrep. No engine is handed the flags themselves.
	TypeAdd   []string
	TypeClear []string
	// SortKey / SortReverse choose the file order before paging. Empty means
	// the engine's deterministic default (path ascending); "none" explicitly
	// leaves the order unspecified. The walker collects candidates and sorts
	// them only when a key is requested, so the default streaming path keeps
	// its bounded memory. Both engines use the same key, which keeps the page
	// boundaries identical on a host with ripgrep and one without.
	SortKey     string
	SortReverse bool
	// SortSet distinguishes "the caller did not ask" from "the caller asked
	// for no sort". The first keeps the deterministic default (path); the
	// second must reach the engine as an explicit `--sort none`, otherwise the
	// base `--sort path` would silently override the request.
	SortSet bool
	// Passthru prints every line of every searched file, not only the matches:
	// matching lines keep the match shape and non-matching lines become context
	// entries. It applies to content mode only, exactly like rg --passthru
	// (files/count modes ignore it). Both engines implement it, so a host
	// without ripgrep keeps the whole-file output.
	Passthru bool
	// JSONOutput asks for ripgrep's own JSON Lines event stream instead of the
	// rendered page. It is a request for *that* format, so it needs the engine
	// that emits it: the walker has no event stream to pass through, and the
	// handler reports that plainly rather than returning some other text under a
	// key that promises JSON.
	JSONOutput bool
}

// RequiresRipgrep reports whether this request needs the ripgrep engine rather
// than the pure-Go walker. The distinction decides the dispatch order, not just
// the args: Go's regexp rejects patterns ripgrep accepts (lookaround,
// backreferences), so for such a call the walker must not get a turn first.
func (o GrepOptions) RequiresRipgrep() bool {
	return o.Pcre2 || o.Multiline || o.MultilineDotall || o.AutoHybridRegex || o.CRLF ||
		o.JSONOutput || requiresRipgrepForEngine(o.Engine) || !grepSortKeySupported(o.SortKey)
}

// requiresRipgrepForEngine mirrors rg's own default: an unset or "default"
// engine is the built-in one, anything else is ripgrep's.
func requiresRipgrepForEngine(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "default":
		return false
	default:
		return true
	}
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
	// IsContext marks a non-matching line that passthru included. It is only
	// ever set when Passthru is on and the mode is content; every other entry
	// is a real match and counts as one.
	IsContext bool
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
	// JSONEvents carries the raw event lines when the caller asked for json
	// output, and JSONEventPaths the absolute path each line describes ("" for
	// events that carry none, such as the summary). The page is a prefix of the
	// stream, so it stays parseable JSON Lines - which is also why the paths are
	// rewritten by the handler instead of being rendered away here.
	JSONEvents     []string
	JSONEventPaths []string
	// JSONStats sums the run's summary events: the counters ripgrep itself
	// reported, which is the same evidence the local tool published for this
	// mode. Collected only on this path, because only here is the stream itself
	// part of the answer.
	JSONStats GrepJSONStats
}

// GrepJSONStats is the subset of ripgrep's summary event the shared tool
// publishes, named exactly as ripgrep names it.
type GrepJSONStats struct {
	Matches          int
	MatchedLines     int
	FilesSearched    int
	FilesWithMatches int
	BytesSearched    int64
	BytesPrinted     int64
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
	includeGlobs, excludeGlobs, err := EffectiveGrepFilters(opts)
	if err != nil {
		return GrepResult{}, err
	}
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
		passthru := opts.Passthru && mode == GrepModeContent
		for {
			lineNo, text, truncated, ok := reader.next()
			if !ok {
				break
			}
			display := reader.displayText(text, truncated)
			if !validUTF8(display) {
				return nil
			}
			selected := compiled.Match(display) != opts.InvertMatch
			if selected {
				hasMatch = true
			}
			if filesWithout {
				// One selected line is enough to prove the file belongs on the
				// other side; the rest of it does not need reading.
				if selected {
					break
				}
				continue
			}
			if passthru {
				// max_count caps the number of *matching* lines, but passthru
				// keeps printing the whole file; lines selected after the cap
				// become context, exactly like rg --passthru -m N.
				capped := opts.MaxCountSet && fileLines >= opts.MaxCount
				if appendGrepLineEntries(&result, compiled, opts, mode, path, lineNo, display, !selected || capped) {
					return filepath.SkipAll
				}
				if selected && !capped {
					fileLines++
				}
				continue
			}
			if !selected {
				continue
			}
			if opts.MaxCountSet && fileLines >= opts.MaxCount {
				break
			}
			if appendGrepLineEntries(&result, compiled, opts, mode, path, lineNo, display, false) {
				return filepath.SkipAll
			}
			fileLines++
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

	// Sorting is opt-in: the default path streams and stops as soon as the page
	// is complete, so its memory stays bounded. A requested key is the caller
	// accepting that one root's candidate list has to be materialized first.
	sortKey := strings.TrimSpace(opts.SortKey)
	sortRequested := opts.SortSet && sortKey != "" && sortKey != "none"
	for _, root := range roots {
		rules := newGrepIgnoreRules(newGrepIgnorePolicy(opts), root)
		if info, statErr := os.Stat(root); statErr == nil && info.Mode().IsRegular() {
			if globMatcher == nil || globMatcher.matches(filepath.Base(root)) {
				if err := visit(root, rules, root); err != nil && err != filepath.SkipAll {
					return GrepResult{}, err
				}
			}
		} else {
			var sortCandidates []grepSortCandidate
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
			onEntry := func(path string, entry os.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				// Links discovered during traversal are not followed by default: a
				// link inside the workspace must not read a target outside the
				// admitted roots. follow=true is the caller accepting that walk,
				// exactly like `rg -L`, and both engines then see the same tree.
				if entry.Type()&os.ModeSymlink != 0 && !opts.Follow {
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
				if sortRequested {
					sortCandidates = append(sortCandidates, grepSortCandidate{root: root, rules: rules, path: path})
					return nil
				}
				return visit(root, rules, path)
			}
			walkErr := grepWalkRoot(root, opts.Follow, onEntry)
			if walkErr != nil && walkErr != filepath.SkipAll {
				return GrepResult{}, walkErr
			}
			if sortRequested {
				ordered, sortErr := sortGrepCandidates(sortCandidates, opts.SortKey, opts.SortReverse)
				if sortErr != nil {
					return GrepResult{}, sortErr
				}
				for _, candidate := range ordered {
					if err := visit(candidate.root, candidate.rules, candidate.path); err != nil {
						if err == filepath.SkipAll {
							break
						}
						return GrepResult{}, err
					}
				}
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

// appendGrepLineEntries appends one scanned line in the shape the caller asked
// for. isContext is the passthru case: the line did not match (or the file's
// match cap was already reached), so it is kept as a context entry instead.
// The return value reports that the page is complete, which lets the caller stop
// the walk exactly one entry past the page.
func appendGrepLineEntries(result *GrepResult, compiled *regexp.Regexp, opts GrepOptions, mode GrepMode, path string, lineNo int, display []byte, isContext bool) bool {
	if isContext {
		result.Matches = append(result.Matches, GrepMatch{
			File:      path,
			Line:      lineNo,
			Text:      sanitizeGrepNulText(string(display)),
			IsContext: true,
		})
		return GrepPageComplete(result.Matches, mode, opts)
	}
	// Fragments are the output unit when only_matching is on. Invert keeps
	// whole lines, exactly like rg -o -v.
	if opts.OnlyMatching && !opts.InvertMatch && mode != GrepModeFiles {
		displayText := string(display)
		// Submatch indices, not plain match indices: the template may reference
		// capture groups, and ExpandString needs their offsets.
		for _, loc := range compiled.FindAllStringSubmatchIndex(displayText, -1) {
			fragment := displayText[loc[0]:loc[1]]
			if opts.Replace != "" {
				// rg prints one substituted fragment per match; Go's
				// ExpandString applies the same `$1`/`${name}` template to the
				// same offsets, which is how both engines answer this call under
				// one contract.
				fragment = string(compiled.ExpandString(nil, opts.Replace, displayText, loc))
			}
			result.Matches = append(result.Matches, GrepMatch{
				File:   path,
				Line:   lineNo,
				Column: loc[0] + 1,
				Text:   sanitizeGrepNulText(fragment),
			})
			if GrepPageComplete(result.Matches, mode, opts) {
				return true
			}
		}
		return false
	}
	// Column is computed on the original line, so Trim can drop the indentation
	// without moving the reported position.
	column := 0
	if opts.Column && !opts.InvertMatch {
		if loc := compiled.FindIndex(display); len(loc) == 2 {
			column = loc[0] + 1
		}
	}
	lineText := string(display)
	if opts.Replace != "" && !opts.InvertMatch {
		// Invert has no match to substitute, exactly like rg -v -r.
		lineText = compiled.ReplaceAllString(lineText, opts.Replace)
	}
	result.Matches = append(result.Matches, GrepMatch{
		File:   path,
		Line:   lineNo,
		Column: column,
		Text:   sanitizeGrepNulText(lineText),
	})
	return GrepPageComplete(result.Matches, mode, opts)
}

// grepWalkRoot walks one search root. Without follow this is exactly
// filepath.WalkDir; with follow the traversal also descends through links that
// resolve to directories, which is what `rg --follow` does - and a link cycle
// terminates instead of recursing forever.
func grepWalkRoot(root string, follow bool, onEntry func(path string, entry os.DirEntry, err error) error) error {
	if !follow {
		return filepath.WalkDir(root, onEntry)
	}
	return walkGrepFollowing(root, onEntry)
}

// walkGrepFollowing is filepath.WalkDir with links followed: a link that
// resolves to a directory is descended into (under the link's own path, which
// is the path ripgrep prints too), and a link that resolves to a file is offered
// like any other file.
//
// Termination is ours and is deliberately stronger than `rg --follow`: a
// directory a link already led to is not followed twice, so mutual links (a->b,
// b->a) terminate instead of growing the path until the OS gives up, and two
// links to one directory are searched once. The cost of that guarantee is that
// a duplicate target stops producing a second path - observed against rg 14.1.0,
// which prints both paths and only refuses links that point at an ancestor.
func walkGrepFollowing(root string, onEntry func(path string, entry os.DirEntry, err error) error) error {
	rootInfo, statErr := os.Stat(root)
	if statErr != nil {
		// Match WalkDir: report the error to the callback and stop.
		return onEntry(root, nil, statErr)
	}
	seen := map[string]bool{}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		seen[resolved] = true
	}
	if err := onEntry(root, fs.FileInfoToDirEntry(rootInfo), nil); err != nil {
		if err == filepath.SkipAll {
			return err
		}
		if err == filepath.SkipDir {
			return nil
		}
		return err
	}
	if !rootInfo.IsDir() {
		return nil
	}

	type dirFrame struct {
		dir     string
		entries []os.DirEntry
		index   int
	}
	stack := []*dirFrame{{dir: root}}
	for len(stack) > 0 {
		frame := stack[len(stack)-1]
		if frame.entries == nil {
			entries, readErr := os.ReadDir(frame.dir)
			if readErr != nil {
				stack = stack[:len(stack)-1]
				continue
			}
			frame.entries = entries
		}
		if frame.index >= len(frame.entries) {
			stack = stack[:len(stack)-1]
			continue
		}
		entry := frame.entries[frame.index]
		frame.index++
		path := filepath.Join(frame.dir, entry.Name())

		isDir := entry.IsDir()
		if entry.Type()&os.ModeSymlink != 0 {
			info, statErr := os.Stat(path)
			if statErr != nil {
				continue
			}
			if info.IsDir() {
				resolved, resolveErr := filepath.EvalSymlinks(path)
				if resolveErr != nil || seen[resolved] {
					continue
				}
				seen[resolved] = true
				isDir = true
				entry = fs.FileInfoToDirEntry(info)
			}
		}

		if err := onEntry(path, entry, nil); err != nil {
			switch {
			case err == filepath.SkipAll:
				return err
			case err == filepath.SkipDir && isDir:
				continue
			case err == filepath.SkipDir:
				// WalkDir's rule for a file entry: skip the rest of this directory.
				frame.index = len(frame.entries)
				continue
			default:
				return err
			}
		}
		if isDir {
			stack = append(stack, &dirFrame{dir: path})
		}
	}
	return nil
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
