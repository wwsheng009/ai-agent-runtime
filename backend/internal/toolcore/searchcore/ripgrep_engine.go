package searchcore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

const (
	ripgrepScanMaxLineBytes = 4 * 1024 * 1024
	ripgrepStderrCapBytes   = 8 * 1024
)

// RipgrepStream feeds one ripgrep invocation to the engine. The consumer
// returns false to stop early; the default exec stream then cancels the
// process. A surface that already owns a process runner (the local tool's
// lookPath/runCommand seam) can implement this over its buffered runner
// instead, and gets identical results because the engine owns all parsing.
type RipgrepStream func(ctx context.Context, args []string, dir string, consume func(line []byte) bool) error

// Ripgrep is the shared ripgrep-backed Searcher. Every surface runs this one
// implementation, so the flags, the JSON parsing and the paging rules cannot
// drift apart.
type Ripgrep struct {
	Path   string
	Stream RipgrepStream
}

// Available reports whether a ripgrep binary is bound.
func (e Ripgrep) Available() bool { return strings.TrimSpace(e.Path) != "" }

func (e Ripgrep) lines(ctx context.Context, dir string, args []string, maxLines int) ([]string, bool, error) {
	// maxLines <= 0 means "read the whole stream" (used when the caller must
	// order the full listing before paging).
	unbounded := maxLines < 1
	capacity := maxLines
	if capacity < 16 {
		capacity = 16
	}
	if capacity > 1024 {
		capacity = 1024
	}
	lines := make([]string, 0, capacity)
	truncated := false
	err := e.stream()(ctx, args, dir, func(line []byte) bool {
		if !unbounded && len(lines) >= maxLines {
			truncated = true
			return false
		}
		lines = append(lines, string(line))
		return true
	})
	if err != nil {
		// Keep what was read before the stream stopped. A cancelled search has
		// already produced real paths, and the caller reports them as a partial
		// page; returning nil here threw that evidence away.
		return lines, truncated, err
	}
	return lines, truncated, nil
}

func (e Ripgrep) stream() RipgrepStream {
	if e.Stream != nil {
		return e.Stream
	}
	return execRipgrepStream(e.Path)
}

// execRipgrepStream is the default implementation: one process, streaming
// stdout, cancelled as soon as the consumer has enough.
func execRipgrepStream(path string) RipgrepStream {
	return func(ctx context.Context, args []string, dir string, consume func(line []byte) bool) error {
		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		cmd := exec.CommandContext(runCtx, path, args...)
		cmd.Dir = dir
		stdout, pipeErr := cmd.StdoutPipe()
		if pipeErr != nil {
			return pipeErr
		}
		stderr := &ripgrepCappedBuffer{limit: ripgrepStderrCapBytes}
		cmd.Stderr = stderr
		if startErr := cmd.Start(); startErr != nil {
			return startErr
		}

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), ripgrepScanMaxLineBytes)
		stopped := false
		for scanner.Scan() {
			if !consume(scanner.Bytes()) {
				stopped = true
				cancel()
				break
			}
		}
		scanErr := scanner.Err()
		if stopped {
			_ = cmd.Wait()
			return nil
		}
		waitErr := cmd.Wait()
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if scanErr != nil {
			return scanErr
		}
		if waitErr != nil {
			if exitErr, ok := waitErr.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				// No matches: a normal empty result, not a failure.
				return nil
			}
			return fmt.Errorf("ripgrep: %v: %s", waitErr, strings.TrimSpace(stderr.String()))
		}
		return nil
	}
}

// ripgrepBaseArgs hold the flags every caller shares: no ripgrep config file,
// one thread and path sorting for deterministic paging. Ignore/hidden/skip
// handling is per caller, because grep mirrors the tool's ignore-file contract
// while glob keeps its own.
func ripgrepBaseArgs() []string {
	return []string{
		"--no-config",
		"--threads", "1",
		"--sort", "path",
	}
}

// ripgrepNoConfigFlag names the determinism flag that a caller may explicitly
// opt out of.
const ripgrepNoConfigFlag = "--no-config"

// ripgrepSearchArgs is ripgrepBaseArgs as one grep invocation uses it. The only
// base flag a caller can turn off is --no-config: `no_config=false` asks ripgrep
// to read its own configuration, which is a request this engine honors by not
// passing the flag. The default stays deterministic on purpose - a shared tool
// must not answer differently on two machines because one has a ~/.ripgreprc -
// and both explicit values are honored, so the capability is not lost either way.
func ripgrepSearchArgs(opts GrepOptions) []string {
	base := ripgrepBaseArgs()
	// A requested sort replaces the base "--sort path" pair rather than being
	// appended after it. Both spellings let ripgrep's last flag win, but one
	// pair in argv is one fact to read - and the default stays byte-identical.
	replaceSort := opts.SortSet
	args := make([]string, 0, len(base)+2)
	skipSortValue := false
	for _, arg := range base {
		if skipSortValue {
			skipSortValue = false
			continue
		}
		if opts.NoConfigSet && !opts.NoConfig && arg == ripgrepNoConfigFlag {
			continue
		}
		if replaceSort && arg == "--sort" {
			skipSortValue = true
			continue
		}
		args = append(args, arg)
	}
	if replaceSort {
		key := strings.TrimSpace(opts.SortKey)
		if key == "" {
			// Explicitly no sorting: the base --sort path was removed, and
			// "none" states that the order is intentionally unspecified.
			key = "none"
		}
		flag := "--sort"
		if opts.SortReverse {
			flag = "--sortr"
		}
		args = append(args, flag, key)
	}
	return args
}

// Glob implements the Searcher contract with ripgrep's file listing.
func (e Ripgrep) Glob(ctx context.Context, opts GlobOptions) (GlobResult, bool, error) {
	if !e.Available() {
		return GlobResult{}, false, nil
	}
	base, pattern, limit := opts.Base, strings.TrimSpace(opts.Pattern), opts.Limit
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
	// The engine runs where the caller put it, but the shape of the path it is being
	// handed is the caller's filesystem's answer: with a remote accessor, stat-ing the
	// host here would decide "is this a file search" from a same-named local path.
	access := seamAccess(opts.Access)
	info, statErr := access.Stat(ctx, base)
	if statErr != nil || !info.IsDir() {
		// Match the walker's file-base behavior by leaving it to the walker.
		return GlobResult{}, false, nil
	}
	expanded := ExpandGlobBraces(pattern)
	if len(expanded) == 0 {
		expanded = []string{pattern}
	}
	// ripgrep's --files lists files only, while the glob tool also returns
	// directories. Use rg only for a pattern whose last segment looks like a
	// file name; a trailing `**` or a directory-shaped tail (`**/docs`) needs
	// the walker, which can report both kinds.
	for _, candidate := range expanded {
		if !engineCanExpressGlob(candidate) {
			return GlobResult{}, false, nil
		}
	}
	// One rg process serves every brace alternative: ripgrep accepts repeated
	// --glob flags and unions them, which is the same union the walker builds.
	globFlag := "--glob"
	if opts.CaseInsensitive {
		globFlag = "--iglob"
	}
	enginePatterns := make([]string, 0, len(expanded))
	for _, candidate := range expanded {
		if !safeRipgrepGlob(candidate) {
			// A pattern ripgrep cannot express the way the walker does takes the
			// whole call to the walker: mixing engines per alternative would
			// make one page come from two different rule sets.
			return GlobResult{}, false, nil
		}
		enginePatterns = append(enginePatterns, candidate)
	}
	matchers := make([]*GlobMatcher, 0, len(enginePatterns))
	for _, candidate := range enginePatterns {
		matcher, err := NewGlobMatcher(candidate)
		if err != nil {
			return GlobResult{}, true, err
		}
		matcher.caseInsensitive = opts.CaseInsensitive
		matchers = append(matchers, matcher)
	}

	args := ripgrepBaseArgs()
	args = append(args, "--files")
	for _, candidate := range enginePatterns {
		args = append(args, globFlag, candidate)
	}
	// Glob's contract still includes hidden entries on both surfaces; that
	// stays explicit here until its hidden policy is parameterized too, and it
	// keeps ignoring nothing (the same policy the local glob tool has).
	args = append(args, "--hidden", "--no-ignore")
	if opts.Follow {
		args = append(args, "--follow")
	}
	args = append(args, "--", ".")
	// Readers take the first N lines ripgrep prints. That is sound only while the
	// output order is the page order: with --sort path, the first N lines are the
	// first N names. Sorting by mtime instead happens after reading, so stopping
	// early would keep an arbitrary prefix and then sort it - and "newest 100"
	// would silently become "newest of whatever came first alphabetically".
	// Ordering by mtime therefore needs the whole listing.
	maxLines := opts.Offset + limit + 1
	if opts.SortByModTime {
		maxLines = 0 // unbounded: collect everything, then order and page
	}
	lines, outputTruncated, runErr := e.lines(ctx, base, args, maxLines)
	// The budget expiring mid-listing still produced real paths: the local tool
	// returns those as a partial page (TimedOut) instead of failing the call.
	// `lines` holds every line read before the cancellation, so it is the
	// partial result, not an empty one.
	timedOut := runErr != nil && ctx != nil && ctx.Err() != nil
	if runErr != nil && !timedOut {
		return GlobResult{}, true, runErr
	}

	result := GlobResult{}
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		// rg prints paths in the platform's separator spelling (backslashes on
		// Windows). Normalize the same way the matcher does, or a Windows
		// listing would be matched as one giant filename instead of segments.
		rel := NormalizeGlobPattern(strings.TrimSpace(line))
		if rel == "" || rel == "." {
			continue
		}
		matched := false
		for _, matcher := range matchers {
			if matcher.MatchRelative(rel) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		candidate := toolcore.NormalizeTargetPath(access, rel)
		if !toolcore.IsAbsTargetPath(access, candidate) {
			candidate = toolcore.JoinTargetPath(access, base, candidate)
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		result.Matches = append(result.Matches, candidate)
	}
	// Order before paging, exactly like the walker: sorting a page afterwards
	// gives each page its own order and makes next_offset meaningless.
	if opts.SortByModTime {
		sortGlobMatchesByModTime(ctx, seamAccess(opts.Access), result.Matches)
	} else {
		sort.Strings(result.Matches)
	}
	// Record the ordered total before the offset window, so "offset is past the
	// end of a non-empty result" can be distinguished from "nothing matched".
	result.MatchedTotal = len(result.Matches)
	if opts.Offset > 0 {
		if opts.Offset >= len(result.Matches) {
			result.Matches = nil
		} else {
			result.Matches = result.Matches[opts.Offset:]
		}
	}
	if len(result.Matches) > limit {
		result.LimitHit = true
		result.Matches = result.Matches[:limit]
	}
	if outputTruncated {
		result.LimitHit = true
	}
	result.TimedOut = timedOut
	result.Truncated = result.LimitHit
	result.Offset = opts.Offset
	result.NextOffset = opts.Offset + len(result.Matches)
	result.HasMore = result.Truncated
	result.BraceExpanded = HasGlobBraceAlternatives(pattern) && len(expanded) > 1
	result.ExpandedPatterns = append([]string(nil), expanded...)
	result.UnsupportedBracePattern = HasGlobBraceAlternatives(pattern) && !result.BraceExpanded
	return result, true, nil
}

// engineCanExpressGlob decides whether `rg --files` can produce a superset whose
// filtered result equals the walker's. The last segment must look like a file
// name; everything else (a directory-shaped tail such as `**/docs`, a trailing
// `**`) would silently drop directories that the glob tool is expected to
// return, so those go to the walker.
//
// A single-segment pattern (`*.go`) is fine: rg would list deeper files too, but
// every line is re-matched against the same root-relative rule, so the extra
// candidates are filtered out and the page is identical.
func engineCanExpressGlob(pattern string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	parts := SplitGlobSegments(NormalizeGlobPattern(pattern))
	if len(parts) == 0 {
		return false
	}
	last := parts[len(parts)-1]
	if last == "**" || last == "" {
		return false
	}
	return strings.Contains(last, ".")
}

// Grep implements the Searcher contract. It runs one ripgrep process per
// search root: ripgrep's --sort path sorts globally across roots, which would
// page differently from the walker's root-by-root walk, so one process per root
// keeps the page composition identical on every engine.
func (e Ripgrep) Grep(ctx context.Context, opts GrepOptions) (GrepResult, bool, error) {
	if !e.Available() {
		return GrepResult{}, false, nil
	}
	roots := ripgrepGrepRoots(opts)
	if len(roots) <= 1 {
		return e.grepRoot(ctx, opts)
	}
	if opts.Limit < 1 {
		opts.Limit = DefaultGrepLimit
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	mode, err := NormalizeGrepMode(string(opts.Mode))
	if err != nil {
		return GrepResult{}, true, err
	}
	// Page entries needed including the one that proves has_more. In content
	// mode an entry is a match; in files/count mode it is a whole file, which
	// is also why the page's last count is trustworthy.
	needed := opts.Offset + opts.Limit + 1
	collected := func(matches []GrepMatch) int {
		if mode == GrepModeContent {
			return len(matches)
		}
		return CountDistinctGrepFiles(matches)
	}
	// The page unit decides what "collected" means, and json changes it: the
	// caller receives stream lines, so a line - not a match - is what fills and
	// completes the page there.
	pageUnits := func(combined GrepResult) int {
		switch {
		case mode == GrepModeFilesWithout:
			return len(combined.Files)
		case mode == GrepModeContent && opts.JSONOutput:
			return len(combined.JSONEvents)
		default:
			return collected(combined.Matches)
		}
	}
	combined := GrepResult{}
	for _, root := range roots {
		remaining := needed - pageUnits(combined)
		if remaining < 1 {
			break
		}
		perRoot := opts
		perRoot.Path = root
		perRoot.Paths = nil
		perRoot.Offset = 0
		perRoot.Limit = remaining
		page, used, err := e.grepRoot(ctx, perRoot)
		if err != nil {
			return GrepResult{}, true, err
		}
		if !used {
			// A filter this root cannot express must take the walker for the
			// whole call, not just this root.
			return GrepResult{}, false, nil
		}
		combined.FilesScanned += page.FilesScanned
		combined.Matches = append(combined.Matches, page.Matches...)
		combined.Files = append(combined.Files, page.Files...)
		combined.JSONEvents = append(combined.JSONEvents, page.JSONEvents...)
		combined.JSONEventPaths = append(combined.JSONEventPaths, page.JSONEventPaths...)
		combined.JSONStats.Matches += page.JSONStats.Matches
		combined.JSONStats.MatchedLines += page.JSONStats.MatchedLines
		combined.JSONStats.FilesSearched += page.JSONStats.FilesSearched
		combined.JSONStats.FilesWithMatches += page.JSONStats.FilesWithMatches
		combined.JSONStats.BytesSearched += page.JSONStats.BytesSearched
		combined.JSONStats.BytesPrinted += page.JSONStats.BytesPrinted
		if pageUnits(combined) >= needed {
			break
		}
	}
	combined.SingleFile = false
	ApplyGrepPageWindow(&combined, mode, opts)
	return combined, true, nil
}

func ripgrepGrepRoots(opts GrepOptions) []string {
	roots := make([]string, 0, 1+len(opts.Paths))
	if strings.TrimSpace(opts.Path) != "" {
		roots = append(roots, opts.Path)
	}
	for _, root := range opts.Paths {
		if strings.TrimSpace(root) != "" {
			roots = append(roots, root)
		}
	}
	return roots
}

func (e Ripgrep) grepRoot(ctx context.Context, opts GrepOptions) (GrepResult, bool, error) {
	if !e.Available() {
		return GrepResult{}, false, nil
	}
	// One accessor for the whole root: every path this function builds (the base directory
	// ripgrep's output is made absolute against, and the relative paths it prints) has to be
	// spelled the way the machine holding the files spells it.
	access := seamAccess(opts.Access)
	if opts.Limit < 1 {
		opts.Limit = DefaultGrepLimit
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	mode, err := NormalizeGrepMode(string(opts.Mode))
	if err != nil {
		return GrepResult{}, true, err
	}
	if mode == GrepModeFilesWithout && opts.MaxCountSet && opts.MaxCount == 0 {
		// The local contract for an explicit `max_count: 0` is "this file
		// produces no match", so every candidate is a no-match file. rg -m 0
		// prints nothing instead, so this case belongs to the walker.
		return GrepResult{}, false, nil
	}
	var globMatcher *grepGlobFilter
	globPatterns := append([]string{}, opts.Globs...)
	if strings.TrimSpace(opts.Glob) != "" {
		globPatterns = append(globPatterns, opts.Glob)
	}
	var engineGlobs []string
	for _, pattern := range globPatterns {
		trimmed := strings.TrimSpace(pattern)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "!") {
			engineGlobs = append(engineGlobs, trimmed)
			continue
		}
		engineGlobs = append(engineGlobs, trimmed)
	}
	if len(engineGlobs) > 0 {
		matcher, err := newGrepGlobFilterWithCase(engineGlobs, opts.GlobCaseInsensitive)
		if err != nil {
			return GrepResult{}, true, err
		}
		for _, pattern := range engineGlobs {
			if !safeRipgrepGlob(strings.TrimPrefix(pattern, "!")) {
				return GrepResult{}, false, nil
			}
		}
		globMatcher = matcher
	}
	// Include/exclude share the glob safety rule: ripgrep's path matching and
	// the walker's basename/relative matching only agree on the simple forms
	// safeRipgrepGlob accepts. Anything else takes the walker so one filter
	// language serves both paths.
	includeGlobs, excludeGlobs, err := EffectiveGrepFilters(opts)
	if err != nil {
		return GrepResult{}, false, err
	}
	for _, pattern := range append(append([]string{}, includeGlobs...), excludeGlobs...) {
		if !safeRipgrepGlob(pattern) {
			return GrepResult{}, false, nil
		}
	}
	roots := ripgrepGrepRoots(opts)
	if len(roots) == 0 {
		return GrepResult{}, true, fmt.Errorf("ripgrep: no search root")
	}
	result := GrepResult{}
	if len(roots) == 1 {
		access := seamAccess(opts.Access)
		if info, statErr := access.Stat(ctx, roots[0]); statErr == nil && info.Mode().IsRegular() {
			result.SingleFile = true
		}
	}

	args := ripgrepSearchArgs(opts)
	// Engine selection is ripgrep's own business: the walker cannot express PCRE2
	// features or multiline semantics, which is why a call asking for them is
	// dispatched here first instead of being compiled with Go's regexp.
	if opts.Pcre2 {
		args = append(args, "--pcre2")
	}
	if strings.TrimSpace(opts.Engine) != "" && requiresRipgrepForEngine(opts.Engine) {
		args = append(args, "--engine", strings.TrimSpace(opts.Engine))
	}
	if opts.AutoHybridRegex {
		args = append(args, "--auto-hybrid-regex")
	}
	if opts.Multiline {
		args = append(args, "--multiline")
	}
	if opts.MultilineDotall {
		args = append(args, "--multiline-dotall")
	}
	if opts.CRLF {
		// The walker's reader trims the CR; ripgrep keeps it in the line unless
		// told otherwise, which would make the same file render differently on
		// the two engines.
		args = append(args, "--crlf")
	}
	// Binary handling follows the walker: the default behaves like ripgrep's own
	// default (a NUL byte ends the search for that file), and only the top
	// unrestricted level lets ripgrep look past it.
	//
	// --text rather than --binary, even though both read past the NUL: rg's
	// --binary treats the NUL as a line terminator for *display*, so a line like
	// "a\0b" is reported as line 1 "a" and line 2 "b". The walker has no such
	// notion, so --binary would give the same files but different line numbers.
	// --text keeps the line structure and the NUL itself; the engine then
	// sanitizes that NUL into a printable symbol before it reaches the caller.
	if opts.Unrestricted >= 3 {
		args = append(args, "--text")
	}
	if opts.Follow {
		// The walker follows the links itself, so the flag means the same tree on
		// both engines rather than "only ripgrep can do this".
		args = append(args, "--follow")
	}
	// Invert keeps whole lines (rg -o -v) and files mode only needs existence,
	// so the flag is passed only where fragments are the output unit.
	onlyMatching := opts.OnlyMatching && !opts.InvertMatch &&
		mode != GrepModeFiles && mode != GrepModeFilesWithout
	// replaceOutput makes ripgrep substitute by itself. Its JSON event stream
	// reports match offsets but no capture-group offsets, so `$1`/`${name}`
	// cannot be re-expanded from it; the text mode keeps rg's own replacement as
	// the single implementation, and the parse below turns its output back into
	// the same GrepMatch shape every other mode produces.
	replaceOutput := opts.Replace != "" && mode == GrepModeContent
	// The JSON mode always reports the fragment offset under --only-matching,
	// and a whole-line match carries one only when the caller asked for it, so
	// the text mode asks rg for exactly the same columns.
	wantColumn := opts.Column || onlyMatching
	// jsonOutput hands ripgrep's own event stream to the caller. The page unit is
	// then a stream line, not a match - one matching line arrives as several
	// events - so the engine stops one line past the page and the handler slices
	// what arrived, exactly like the local tool's json window.
	jsonOutput := opts.JSONOutput
	jsonLineBudget := 0
	if jsonOutput {
		jsonLineBudget = opts.Offset + opts.Limit
	}
	// --passthru turns the stream into the whole file, with non-matching lines
	// as context events (json) or `path<sep>line<sep>text` lines (text mode).
	// files/count modes ignore it, exactly like rg.
	if opts.Passthru && mode == GrepModeContent {
		args = append(args, "--passthru")
	}
	if mode == GrepModeFilesWithout {
		// rg ignores --json in this mode and prints plain paths, so the stats
		// block is the only structured evidence available. It arrives after
		// the paths; the parser below separates it from them.
		args = append(args, "--stats", "--files-without-match")
	} else if replaceOutput {
		// --null terminates the printed path with a NUL byte, which is what
		// makes this line parse unambiguous: a path may contain a colon of its
		// own (C:\... on Windows), so the usual path:line: separator cannot be
		// trusted to split in the right place.
		args = append(args,
			"--null", "--line-number", "--with-filename",
			"--color=never", "--no-heading",
			"--replace", opts.Replace)
		if wantColumn {
			args = append(args, "--column")
		}
	} else {
		args = append(args, "--json")
	}
	// The walker applies ignore files wherever they exist; ripgrep only applies
	// them inside a git repository unless told otherwise (verified: without
	// this flag a temp directory's .gitignore is not consulted at all).
	// --no-require-git keeps the two engines on the same contract.
	args = append(args, "--no-require-git")
	if opts.IncludeHidden {
		args = append(args, "--hidden")
	}
	// Ignore-file handling mirrors the walker and the local tool: files are
	// consulted by default, every `no_ignore*` switch maps to its ripgrep flag,
	// and explicit ignore_file entries are passed through.
	if opts.NoIgnore {
		args = append(args, "--no-ignore")
	}
	if opts.NoIgnoreParent {
		args = append(args, "--no-ignore-parent")
	}
	if opts.NoIgnoreVCS {
		args = append(args, "--no-ignore-vcs")
	}
	if opts.NoIgnoreDot {
		args = append(args, "--no-ignore-dot")
	}
	if opts.NoIgnoreGlobal {
		args = append(args, "--no-ignore-global")
	}
	if opts.NoIgnoreFiles {
		args = append(args, "--no-ignore-files")
	}
	if opts.NoMessages {
		// rg writes permission/IO diagnostics to stderr and keeps exit 0. The
		// engine reads stdout only, so either way those warnings never reach the
		// caller; passing the flag keeps rg from doing the formatting work and
		// states the intent explicitly.
		args = append(args, "--no-messages")
	}
	if !opts.NoIgnoreFiles && !opts.NoIgnore {
		// no_ignore_files discards the explicit list entirely; rg's own
		// --no-ignore-files only disables files added *by that flag* the other
		// way round, so the list must be dropped here rather than relied on.
		for _, file := range opts.IgnoreFiles {
			args = append(args, "--ignore-file", file)
		}
	}
	if globMatcher != nil {
		for _, pattern := range engineGlobs {
			args = append(args, "--glob", pattern)
		}
	}
	for _, pattern := range includeGlobs {
		args = append(args, "--glob", pattern)
	}
	for _, pattern := range excludeGlobs {
		args = append(args, "--glob", "!"+pattern)
	}
	if opts.Literal {
		args = append(args, "--fixed-strings")
	}
	if opts.Word {
		args = append(args, "--word-regexp")
	}
	if opts.LineRegexp {
		args = append(args, "--line-regexp")
	}
	if opts.InvertMatch {
		args = append(args, "--invert-match")
	}
	// The case mode is resolved once, in the shared core, so the flag rg
	// receives is the same boolean the pure-Go walker compiles into its
	// expression. rg's own --smart-case is deliberately unused: its
	// multi-pattern behaviour is not something the walker can mirror.
	patterns := opts.Patterns
	if len(patterns) == 0 && opts.Pattern != "" {
		patterns = []string{opts.Pattern}
	}
	if len(opts.PatternFiles) > 0 {
		// Pattern files are read by the core, not passed to rg: the effective
		// pattern list must be identical on both engines.
		withFiles, err := GrepPatternsWithFiles(ctx, opts)
		if err != nil {
			return GrepResult{}, true, err
		}
		patterns = withFiles
	}
	if EffectiveIgnoreCase(patterns, GrepPatternOptions{
		IgnoreCase:    opts.IgnoreCase || opts.CaseInsensitive,
		CaseSensitive: opts.CaseSensitive,
		SmartCase:     opts.SmartCase,
	}) {
		args = append(args, "--ignore-case")
	} else if opts.CaseSensitive {
		args = append(args, "--case-sensitive")
	}
	// The capture budget and the user filter are independent; ripgrep only has
	// one file-size flag, and the tighter of the two is the one that preserves
	// the walker's result set.
	maxFileSize := opts.MaxFileSizeBytes
	if opts.MaxFileBytes > 0 && (maxFileSize == 0 || int64(opts.MaxFileBytes) < maxFileSize) {
		maxFileSize = int64(opts.MaxFileBytes)
	}
	if maxFileSize > 0 {
		args = append(args, "--max-filesize", strconv.FormatInt(maxFileSize, 10))
	}
	if opts.MaxDepthSet {
		// ripgrep counts levels beyond the path it was given, so rg's 0 means
		// "do not enter the directory at all" (verified: `rg --max-depth 0`
		// with a directory root returns nothing). The shared contract counts
		// directory levels below each root, which is rg's depth+1.
		args = append(args, "--max-depth", strconv.Itoa(opts.MaxDepth+1))
	}
	if opts.OneFileSystem {
		// Limits recursion only: an explicitly named root on another device is
		// still searched, verified against rg's own behaviour.
		args = append(args, "--one-file-system")
	}
	// An explicit 0 means "this file contributes nothing", which is exactly
	// what rg -m 0 does; unset means no per-file cap.
	if opts.MaxCountSet {
		args = append(args, "--max-count", strconv.Itoa(opts.MaxCount))
	}
	// rg stops reading a file once its match cap is reached, so under
	// --passthru the lines after the last match of that file never arrive. The
	// walker keeps reading them as context (rg -m 0/-v aside, a cap is a match
	// cap, not a read cap). The walker is therefore the engine that implements
	// this combination.
	if opts.Passthru && mode == GrepModeContent && opts.MaxCountSet {
		return GrepResult{}, false, nil
	}
	if onlyMatching {
		args = append(args, "--only-matching")
	}
	// trim and max_columns are applied by the shared renderer, not by rg: the
	// markers they print are part of the contract, and rg would print its own
	// wording (and a different length measure) for the same flags.
	// Column is different - it only affects the line prefix, which the renderer
	// owns - so it is not forwarded either; the engine computes it from the
	// match offsets it already parses.
	if opts.GlobCaseInsensitive {
		// Applies to the glob family only; the case mode of the search pattern
		// is resolved separately above.
		args = append(args, "--glob-case-insensitive")
	}
	if opts.IgnoreFileCaseInsensitive {
		args = append(args, "--ignore-file-case-insensitive")
	}
	for _, pattern := range patterns {
		args = append(args, "-e", pattern)
	}
	args = append(args, "--")
	args = append(args, roots...)

	// With one root ripgrep may print paths relative to it; with several roots
	// every root is absolute and so are the printed paths.
	baseDir := ""
	if len(roots) == 1 {
		baseDir = roots[0]
		if result.SingleFile {
			baseDir = toolcore.DirTargetPath(access, roots[0])
		}
	}

	if mode == GrepModeFilesWithout {
		return e.grepRootFilesWithout(ctx, opts, mode, result, args, baseDir, globMatcher)
	}

	truncated := false
	streamErr := e.stream()(ctx, args, "", func(line []byte) bool {
		if replaceOutput {
			rawPath, lineNo, column, text, isContext, ok := parseRipgrepReplaceLine(line, wantColumn)
			if !ok {
				return true
			}
			if isContext {
				if appendRipgrepContext(opts, mode, globMatcher, baseDir, rawPath, lineNo, text, &result) {
					truncated = true
					return false
				}
				return true
			}
			if appendRipgrepMatch(opts, mode, globMatcher, baseDir, rawPath, lineNo, column, text, &result) {
				truncated = true
				return false
			}
			return true
		}
		if jsonOutput {
			event := collectRipgrepJSONEvent(&result, line, baseDir, access)
			if event.Type == "summary" {
				result.JSONStats.Matches += event.Data.Stats.Matches
				result.JSONStats.MatchedLines += event.Data.Stats.MatchedLines
				result.JSONStats.FilesSearched += event.Data.Stats.searchedFiles()
				result.JSONStats.FilesWithMatches += event.Data.Stats.SearchesWithMatch
				result.JSONStats.BytesSearched += event.Data.Stats.BytesSearched
				result.JSONStats.BytesPrinted += event.Data.Stats.BytesPrinted
				result.FilesScanned += event.Data.Stats.searchedFiles()
			}
			// One line past the page is the evidence that more existed; the
			// handler slices the page out of what arrived.
			if len(result.JSONEvents) > jsonLineBudget {
				truncated = true
				return false
			}
			return true
		}
		var event rgJSONEvent
		if jsonErr := json.Unmarshal(line, &event); jsonErr != nil {
			return true
		}
		switch event.Type {
		case "summary":
			result.FilesScanned = event.Data.Stats.searchedFiles()
		case "match":
			text, ok := ripgrepJSONEventText(event)
			if !ok {
				return true
			}
			// lineText keeps the raw line (trailing newline included) because
			// submatch offsets point into it; the display form is the shared
			// normalization every output mode feeds.
			lineText := text
			if onlyMatching && len(event.Data.Submatches) > 0 {
				for _, sub := range event.Data.Submatches {
					if sub.Start < 0 || sub.End > len(lineText) || sub.Start >= sub.End {
						continue
					}
					if appendRipgrepMatch(opts, mode, globMatcher, baseDir, event.Data.Path.Text,
						event.Data.LineNumber, sub.Start+1, lineText[sub.Start:sub.End], &result) {
						truncated = true
						return false
					}
				}
				return true
			}
			// Stop one page entry past the page so Truncated/has_more stays
			// accurate; the shared window then slices the raw list down.
			if appendRipgrepMatch(opts, mode, globMatcher, baseDir, event.Data.Path.Text,
				event.Data.LineNumber, grepLineColumn(opts, lineText, event.Data.Submatches), text, &result) {
				truncated = true
				return false
			}
		case "context":
			// Context events only arrive when --passthru asked for them: the
			// shared engine never passes --context (the walker expands context
			// from the matched files instead).
			if !opts.Passthru {
				return true
			}
			text, ok := ripgrepJSONEventText(event)
			if !ok {
				return true
			}
			if appendRipgrepContext(opts, mode, globMatcher, baseDir, event.Data.Path.Text,
				event.Data.LineNumber, text, &result) {
				truncated = true
				return false
			}
		}
		return true
	})
	if streamErr != nil {
		return GrepResult{}, true, streamErr
	}
	ApplyGrepPageWindow(&result, mode, opts)
	if truncated {
		result.Truncated = true
	}
	return result, true, nil
}

// RipgrepGlobIsSafe reports whether the walker's glob interpretation is a
// guaranteed superset of ripgrep's. Constructs with different meanings
// (negation, character classes, braces, escapes and rooted patterns) take the
// pure-Go walker instead of risking a silent false negative.
func RipgrepGlobIsSafe(pattern string) bool {
	return safeRipgrepGlob(pattern)
}

// grepRootFilesWithout parses `rg --files-without-match` output. ripgrep ignores
// --json for this mode and prints one plain path per file, plus a trailing
// --stats block; both are parsed here so the page, the has_more bit and the
// scanned-file count mean the same thing as on every other engine.
func (e Ripgrep) grepRootFilesWithout(ctx context.Context, opts GrepOptions, mode GrepMode, result GrepResult, args []string, baseDir string, globMatcher *grepGlobFilter) (GrepResult, bool, error) {
	lines := make([]string, 0, 64)
	streamErr := e.stream()(ctx, args, "", func(line []byte) bool {
		lines = append(lines, string(line))
		return true
	})
	if streamErr != nil {
		return GrepResult{}, true, streamErr
	}
	content, stats := splitRipgrepStatsTail(lines)
	result.FilesScanned = stats.FilesSearched
	truncated := false
	for _, line := range content {
		// Only the CR of CRLF is stripped: a path may legitimately begin or end
		// with a space, and trimming it would name a different file.
		display := strings.TrimRight(line, "\r")
		if strings.TrimSpace(display) == "" {
			continue
		}
		path := toolcore.NormalizeTargetPath(opts.Access, display)
		if !toolcore.IsAbsTargetPath(opts.Access, path) {
			path = toolcore.JoinTargetPath(opts.Access, baseDir, path)
		}
		if globMatcher != nil && !globMatcher.matches(path) {
			continue
		}
		result.Files = append(result.Files, GrepFileSummary{File: path})
		if GrepFilesWithoutComplete(result.Files, opts) {
			// One entry past the page proves has_more; stop reading.
			truncated = true
			break
		}
	}
	ApplyGrepPageWindow(&result, mode, opts)
	if truncated {
		result.Truncated = true
	}
	return result, true, nil
}

// ripgrepStatsTail is the part of rg's --stats block the shared engine uses.
type ripgrepStatsTail struct {
	FilesSearched int
}

// splitRipgrepStatsTail separates the trailing --stats block from the path
// lines. Paths are matched first: only a line that is exactly a stats sentence
// belongs to the tail, so a file whose name contains prose stays a path.
func splitRipgrepStatsTail(lines []string) ([]string, ripgrepStatsTail) {
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	start := end
	for start > 0 && isRipgrepStatsTailLine(strings.TrimSpace(lines[start-1])) {
		start--
	}
	var stats ripgrepStatsTail
	for _, line := range lines[start:end] {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[1] == "files" && fields[2] == "searched" {
			if value, err := strconv.Atoi(fields[0]); err == nil {
				stats.FilesSearched = value
			}
		}
	}
	contentEnd := start
	for contentEnd > 0 && strings.TrimSpace(lines[contentEnd-1]) == "" {
		contentEnd--
	}
	return lines[:contentEnd], stats
}

func isRipgrepStatsTailLine(line string) bool {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return false
	}
	if _, err := strconv.Atoi(fields[0]); err != nil {
		if _, floatErr := strconv.ParseFloat(fields[0], 64); floatErr != nil {
			return false
		}
	}
	rest := strings.Join(fields[1:], " ")
	switch rest {
	case "matches", "matched lines", "files contained matches", "files searched",
		"bytes printed", "bytes searched", "seconds spent searching", "seconds total":
		return true
	}
	// ripgrep 14 prints the total as "0.001296 seconds"; 15 appends "total".
	// Accepting only one spelling silently turns the other into a file path.
	if len(fields) == 2 && fields[1] == "seconds" {
		return true
	}
	return false
}

// grepLineColumn derives the reported column for a whole-line match from the
// offsets ripgrep already sends. It is only computed when the caller asked for
// column, so the ordinary path stays free of the extra work. rg reports byte
// offsets into the raw line, which is exactly the coordinate the walker's
// regexp indices use.
func grepLineColumn(opts GrepOptions, lineText string, submatches []rgSubmatch) int {
	if !opts.Column {
		return 0
	}
	if len(submatches) == 0 {
		return 0
	}
	start := submatches[0].Start
	if start < 0 || start > len(lineText) {
		return 0
	}
	return start + 1
}

// normalizeGrepEngineText is the one text contract every ripgrep output mode
// feeds: drop the trailing newline, reject invalid UTF-8, cap the line and keep
// a raw NUL away from the caller - which truncates the string in most clients
// and hides everything after it.
func normalizeGrepEngineText(text string) (string, bool) {
	text = strings.TrimSuffix(strings.TrimSuffix(text, "\n"), "\r")
	if !utf8.ValidString(text) {
		return "", false
	}
	if len(text) > MaxGrepLineBytes {
		text = text[:MaxGrepLineBytes] + "…"
	}
	return sanitizeGrepNulText(text), true
}

// appendRipgrepMatch records one match the way both output modes must: resolve
// the printed path, apply the caller's glob filter, shape the text like every
// other engine, then report whether the page is complete. The caller stops the
// stream on true, so Truncated/has_more stay accurate.
func appendRipgrepMatch(opts GrepOptions, mode GrepMode, globMatcher *grepGlobFilter, baseDir, rawPath string, lineNo, column int, text string, result *GrepResult) bool {
	matchPath := resolveRipgrepPath(seamAccess(opts.Access), rawPath, baseDir)
	if globMatcher != nil && !globMatcher.matches(matchPath) {
		return false
	}
	display, ok := normalizeGrepEngineText(text)
	if !ok {
		return false
	}
	result.Matches = append(result.Matches, GrepMatch{
		File:   matchPath,
		Line:   lineNo,
		Column: column,
		Text:   display,
	})
	return GrepPageComplete(result.Matches, mode, opts)
}

// appendRipgrepContext records one non-matching passthru line. The path and
// text normalization are the same as a match; the entry is marked context so
// the renderer and the match counters can tell them apart.
func appendRipgrepContext(opts GrepOptions, mode GrepMode, globMatcher *grepGlobFilter, baseDir, rawPath string, lineNo int, text string, result *GrepResult) bool {
	matchPath := resolveRipgrepPath(seamAccess(opts.Access), rawPath, baseDir)
	if globMatcher != nil && !globMatcher.matches(matchPath) {
		return false
	}
	display, ok := normalizeGrepEngineText(text)
	if !ok {
		return false
	}
	result.Matches = append(result.Matches, GrepMatch{
		File:      matchPath,
		Line:      lineNo,
		Text:      display,
		IsContext: true,
	})
	return GrepPageComplete(result.Matches, mode, opts)
}

// ripgrepJSONEventText decodes the line payload shared by match and context
// events. rg sends UTF-8 text when it can and base64 when it cannot.
func ripgrepJSONEventText(event rgJSONEvent) (string, bool) {
	switch {
	case event.Data.Lines.Bytes != nil:
		raw, err := base64.StdEncoding.DecodeString(*event.Data.Lines.Bytes)
		if err != nil {
			return "", false
		}
		return string(raw), true
	case event.Data.Lines.Text != nil:
		return *event.Data.Lines.Text, true
	default:
		return "", false
	}
}

// resolveRipgrepPath turns a printed path into an absolute one: ripgrep echoes
// a relative path when it was given a relative root (the single-root case), and
// every consumer downstream works in absolute paths.
func resolveRipgrepPath(access toolcore.Access, rawPath, baseDir string) string {
	matchPath := toolcore.NormalizeTargetPath(access, rawPath)
	if !toolcore.IsAbsTargetPath(access, matchPath) {
		matchPath = toolcore.JoinTargetPath(access, baseDir, matchPath)
	}
	return matchPath
}

// collectRipgrepJSONEvent records one raw event line together with the absolute
// path it describes ("" for events that carry none, such as the summary), so the
// handler can rewrite it into the surface's display path without reparsing the
// stream. The line itself is kept byte-for-byte: json output is a passthrough,
// and reformatting it would change what the caller receives.
func collectRipgrepJSONEvent(result *GrepResult, line []byte, baseDir string, access toolcore.Access) rgJSONEvent {
	var event rgJSONEvent
	_ = json.Unmarshal(line, &event)
	result.JSONEvents = append(result.JSONEvents, string(line))
	path := ""
	if event.Data.Path.Text != "" {
		path = resolveRipgrepPath(access, event.Data.Path.Text, baseDir)
	}
	result.JSONEventPaths = append(result.JSONEventPaths, path)
	return event
}

// parseRipgrepReplaceLine splits one `rg --null` content line from the
// replacement mode: the path ends at the NUL byte ripgrep writes after it, then
// `line:` (or `line:column:` when --column was asked for) precedes the text
// ripgrep substituted. A passthru context line uses `line-text` and carries no
// column; that separator is what tells the two apart. The NUL is what makes the
// split unambiguous - a path may contain a colon of its own.
func parseRipgrepReplaceLine(line []byte, wantColumn bool) (rawPath string, lineNo, column int, text string, isContext, ok bool) {
	nul := bytes.IndexByte(line, 0)
	if nul <= 0 {
		return "", 0, 0, "", false, false
	}
	rawPath = string(line[:nul])
	rest := string(line[nul+1:])
	lineNo, separator, rest, ok := cutRipgrepIntFieldSep(rest)
	if !ok {
		return "", 0, 0, "", false, false
	}
	if separator == '-' {
		return rawPath, lineNo, 0, rest, true, true
	}
	if wantColumn {
		column, separator, rest, ok = cutRipgrepIntFieldSep(rest)
		if !ok || separator != ':' {
			return "", 0, 0, "", false, false
		}
	}
	return rawPath, lineNo, column, rest, false, true
}

// cutRipgrepIntFieldSep reads a leading decimal field terminated by ':' or '-'
// and returns the separator that followed it.
func cutRipgrepIntFieldSep(raw string) (int, byte, string, bool) {
	i := 0
	for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(raw) || (raw[i] != ':' && raw[i] != '-') {
		return 0, 0, raw, false
	}
	value, err := strconv.Atoi(raw[:i])
	if err != nil {
		return 0, 0, raw, false
	}
	return value, raw[i], raw[i+1:], true
}

func safeRipgrepGlob(pattern string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || strings.HasPrefix(pattern, "!") || strings.HasPrefix(pattern, "/") {
		return false
	}
	if strings.ContainsAny(pattern, "[\\]{}") {
		return false
	}
	// Basename matching in searchcore drops a leading "**/" prefix. Other
	// embedded slashes make ripgrep's path match and the walker's basename
	// match diverge, so those patterns use the walker.
	pattern = strings.TrimPrefix(pattern, "**/")
	return !strings.Contains(pattern, "/")
}

type rgJSONEvent struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text  *string `json:"text"`
			Bytes *string `json:"bytes"`
		} `json:"lines"`
		LineNumber int `json:"line_number"`
		// Submatches carries one entry per matched fragment when ripgrep runs
		// with --only-matching; offsets are byte positions into the raw line
		// (including its trailing newline).
		Submatches []rgSubmatch `json:"submatches"`
		Stats      rgJSONStats  `json:"stats"`
	} `json:"data"`
}

// rgJSONStats is the counter block ripgrep puts in its summary event. It reports
// "searches"/"searches_with_match" rather than naming files, which is why the
// tool publishes them under the names callers already read.
type rgJSONStats struct {
	FilesSearched     int   `json:"files_searched"`
	Searches          int   `json:"searches"`
	SearchesWithMatch int   `json:"searches_with_match"`
	Matches           int   `json:"matches"`
	MatchedLines      int   `json:"matched_lines"`
	BytesSearched     int64 `json:"bytes_searched"`
	BytesPrinted      int64 `json:"bytes_printed"`
}

// searchedFiles is the count ripgrep actually reports: its summary has no
// "files_searched" field, so "searches" is the same fact under ripgrep's own
// name.
func (s rgJSONStats) searchedFiles() int {
	if s.Searches > 0 {
		return s.Searches
	}
	return s.FilesSearched
}

// rgSubmatch is one match location inside a line, in byte offsets.
type rgSubmatch struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type ripgrepCappedBuffer struct {
	data  bytes.Buffer
	limit int
}

func (b *ripgrepCappedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return len(p), nil
	}
	remaining := b.limit - b.data.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = b.data.Write(p[:remaining])
		} else {
			_, _ = b.data.Write(p)
		}
	}
	return len(p), nil
}

func (b *ripgrepCappedBuffer) String() string {
	if b == nil {
		return ""
	}
	return b.data.String()
}
