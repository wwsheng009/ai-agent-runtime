package searchcore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
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
	if maxLines < 1 {
		maxLines = 1
	}
	capacity := maxLines
	if capacity > 1024 {
		capacity = 1024
	}
	lines := make([]string, 0, capacity)
	truncated := false
	err := e.stream()(ctx, args, dir, func(line []byte) bool {
		if len(lines) >= maxLines {
			truncated = true
			return false
		}
		lines = append(lines, string(line))
		return true
	})
	if err != nil {
		return nil, false, err
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

// Glob implements the Searcher contract with ripgrep's file listing.
func (e Ripgrep) Glob(ctx context.Context, opts GlobOptions) (GlobResult, bool, error) {
	if !e.Available() {
		return GlobResult{}, false, nil
	}
	base, pattern, limit := opts.Base, opts.Pattern, opts.Limit
	matcher, err := NewGlobMatcher(pattern)
	if err != nil {
		return GlobResult{}, true, err
	}
	if !safeRipgrepGlob(pattern) {
		return GlobResult{}, false, nil
	}
	info, statErr := os.Stat(base)
	if statErr != nil || !info.IsDir() {
		// Match the walker's file-base behavior by leaving it to the walker.
		return GlobResult{}, false, nil
	}
	if limit < 1 {
		limit = DefaultGlobLimit
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}

	args := append(ripgrepBaseArgs(), "--files", "--glob", pattern)
	// Glob's contract still includes hidden entries on both surfaces; that
	// stays explicit here until its hidden policy is parameterized too, and it
	// keeps ignoring nothing (the same policy the local glob tool has).
	args = append(args, "--hidden", "--no-ignore")
	args = append(args, "--", ".")
	lines, outputTruncated, runErr := e.lines(ctx, base, args, opts.Offset+limit+1)
	if runErr != nil {
		return GlobResult{}, true, runErr
	}

	result := GlobResult{}
	for _, line := range lines {
		rel := filepath.ToSlash(strings.TrimPrefix(strings.TrimSpace(line), "./"))
		if rel == "" || rel == "." || !matcher.Match(rel) {
			continue
		}
		candidate := filepath.FromSlash(rel)
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(base, candidate)
		}
		result.Matches = append(result.Matches, candidate)
	}
	if opts.Offset > 0 {
		if opts.Offset >= len(result.Matches) {
			result.Matches = nil
		} else {
			result.Matches = result.Matches[opts.Offset:]
		}
	}
	if outputTruncated || len(result.Matches) > limit {
		result.Truncated = true
	}
	if len(result.Matches) > limit {
		result.Matches = result.Matches[:limit]
	}
	sort.Strings(result.Matches)
	result.Offset = opts.Offset
	result.NextOffset = opts.Offset + len(result.Matches)
	result.HasMore = result.Truncated
	return result, true, nil
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
	combined := GrepResult{}
	for _, root := range roots {
		remaining := needed - collected(combined.Matches)
		if mode == GrepModeFilesWithout {
			// The page unit is the proven no-match file, so the per-root
			// budget and the completion test count files, not matches.
			remaining = needed - len(combined.Files)
		}
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
		if GrepResultPageComplete(&combined, mode, opts) {
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
	var globMatcher *GlobMatcher
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
		matcher, err := NewGlobMatcher(strings.Join(engineGlobs, ","))
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
	includeGlobs, excludeGlobs := EffectiveGrepFilters(opts)
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
		if info, statErr := os.Stat(roots[0]); statErr == nil && info.Mode().IsRegular() {
			result.SingleFile = true
		}
	}

	args := ripgrepBaseArgs()
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
	if mode == GrepModeFilesWithout {
		// rg ignores --json in this mode and prints plain paths, so the stats
		// block is the only structured evidence available. It arrives after
		// the paths; the parser below separates it from them.
		args = append(args, "--stats", "--files-without-match")
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
	for _, file := range opts.IgnoreFiles {
		args = append(args, "--ignore-file", file)
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
		withFiles, err := GrepPatternsWithFiles(opts)
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
	// Invert keeps whole lines (rg -o -v) and files mode only needs existence,
	// so the flag is passed only where fragments are the output unit.
	onlyMatching := opts.OnlyMatching && !opts.InvertMatch &&
		mode != GrepModeFiles && mode != GrepModeFilesWithout
	if onlyMatching {
		args = append(args, "--only-matching")
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
			baseDir = filepath.Dir(roots[0])
		}
	}

	if mode == GrepModeFilesWithout {
		return e.grepRootFilesWithout(ctx, opts, mode, result, args, baseDir, globMatcher)
	}

	truncated := false
	streamErr := e.stream()(ctx, args, "", func(line []byte) bool {
		var event rgJSONEvent
		if jsonErr := json.Unmarshal(line, &event); jsonErr != nil {
			return true
		}
		switch event.Type {
		case "summary":
			result.FilesScanned = event.Data.Stats.FilesSearched
		case "match":
			matchPath := filepath.FromSlash(event.Data.Path.Text)
			if !filepath.IsAbs(matchPath) {
				matchPath = filepath.Join(baseDir, matchPath)
			}
			if globMatcher != nil && !globMatcher.Match(matchPath) {
				return true
			}
			var text string
			switch {
			case event.Data.Lines.Bytes != nil:
				raw, decodeErr := base64.StdEncoding.DecodeString(*event.Data.Lines.Bytes)
				if decodeErr != nil {
					return true
				}
				text = string(raw)
			case event.Data.Lines.Text != nil:
				text = *event.Data.Lines.Text
			}
			// lineText keeps the raw line (trailing newline included) because
			// submatch offsets point into it; text is the display form.
			lineText := text
			text = strings.TrimSuffix(strings.TrimSuffix(text, "\n"), "\r")
			if !utf8.ValidString(text) {
				return true
			}
			if len(text) > MaxGrepLineBytes {
				text = text[:MaxGrepLineBytes] + "…"
			}
			// A raw NUL must never reach the caller: it truncates the string in
			// most clients and hides everything after it.
			text = sanitizeGrepNulText(text)
			if onlyMatching && len(event.Data.Submatches) > 0 {
				for _, sub := range event.Data.Submatches {
					if sub.Start < 0 || sub.End > len(lineText) || sub.Start >= sub.End {
						continue
					}
					fragment := strings.TrimSuffix(strings.TrimSuffix(lineText[sub.Start:sub.End], "\n"), "\r")
					if !utf8.ValidString(fragment) {
						continue
					}
					if len(fragment) > MaxGrepLineBytes {
						fragment = fragment[:MaxGrepLineBytes] + "…"
					}
					result.Matches = append(result.Matches, GrepMatch{
						File:   matchPath,
						Line:   event.Data.LineNumber,
						Column: sub.Start + 1,
						Text:   sanitizeGrepNulText(fragment),
					})
					if GrepPageComplete(result.Matches, mode, opts) {
						truncated = true
						return false
					}
				}
				return true
			}
			result.Matches = append(result.Matches, GrepMatch{
				File: matchPath,
				Line: event.Data.LineNumber,
				Text: text,
			})
			// Stop one page entry past the page so Truncated/has_more stays
			// accurate; the shared window then slices the raw list down.
			if GrepPageComplete(result.Matches, mode, opts) {
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
func (e Ripgrep) grepRootFilesWithout(ctx context.Context, opts GrepOptions, mode GrepMode, result GrepResult, args []string, baseDir string, globMatcher *GlobMatcher) (GrepResult, bool, error) {
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
		path := filepath.FromSlash(display)
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		if globMatcher != nil && !globMatcher.Match(path) {
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
		Submatches []struct {
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"submatches"`
		Stats struct {
			FilesSearched int `json:"files_searched"`
		} `json:"stats"`
	} `json:"data"`
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
