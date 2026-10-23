package remotetools

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

	"github.com/wwsheng009/ai-agent-runtime/internal/ripgrep"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

const (
	rgScanMaxLineBytes = 4 * 1024 * 1024
	rgStderrCapBytes   = 8 * 1024
)

// ripgrepRunner delegates remote traversal to the resolver-backed ripgrep
// binary. The caller falls back to searchcore's pure-Go walker when the binary
// is unavailable or a run fails.
type ripgrepRunner struct {
	path string
}

func resolveRipgrepRunner() ripgrepRunner {
	resolved, err := ripgrep.Resolve()
	if err != nil {
		return ripgrepRunner{}
	}
	return ripgrepRunner{path: strings.TrimSpace(resolved.Path)}
}

func (r ripgrepRunner) available() bool {
	return r.path != ""
}

// baseArgs keep ripgrep's traversal aligned with searchcore: hidden files are
// included, ignore files are not consulted, and the same dependency/VCS trees
// are skipped. A single thread plus path sorting makes limit behavior
// deterministic.
func (r ripgrepRunner) baseArgs() []string {
	args := []string{
		"--no-config",
		"--hidden",
		"--no-ignore",
		"--threads", "1",
		"--sort", "path",
	}
	return args
}

// skipArgs must be appended after any caller-supplied glob. ripgrep applies
// glob rules in order, with later rules winning, so the walker's skipped trees
// stay excluded even when a caller writes a broad include glob.
func (r ripgrepRunner) skipArgs() []string {
	args := make([]string, 0, 2*len(searchcore.SkippedDirNames))
	for _, name := range searchcore.SkippedDirNames {
		args = append(args, "--glob", "!**/"+name+"/**")
	}
	return args
}

func (r ripgrepRunner) glob(ctx context.Context, opts searchcore.GlobOptions) (searchcore.GlobResult, bool, error) {
	if !r.available() {
		return searchcore.GlobResult{}, false, nil
	}
	base, pattern, limit := opts.Base, opts.Pattern, opts.Limit
	matcher, err := searchcore.NewGlobMatcher(pattern)
	if err != nil {
		return searchcore.GlobResult{}, true, err
	}
	if !safeRipgrepGlob(pattern) {
		return searchcore.GlobResult{}, false, nil
	}
	info, statErr := os.Stat(base)
	if statErr != nil || !info.IsDir() {
		// Match the walker's file-base behavior by leaving it to the walker.
		return searchcore.GlobResult{}, false, nil
	}
	if limit < 1 {
		limit = searchcore.DefaultGlobLimit
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}

	args := append(r.baseArgs(), "--files", "--glob", pattern)
	args = append(args, r.skipArgs()...)
	args = append(args, "--", ".")
	lines, outputTruncated, runErr := r.runLines(ctx, base, args, opts.Offset+limit+1)
	if runErr != nil {
		return searchcore.GlobResult{}, true, runErr
	}

	result := searchcore.GlobResult{}
	for _, line := range lines {
		rel := filepath.ToSlash(strings.TrimPrefix(strings.TrimSpace(line), "./"))
		if rel == "" || rel == "." || hasSkippedPathComponent(rel) || !matcher.Match(rel) {
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

func hasSkippedPathComponent(rel string) bool {
	parts := strings.Split(rel, "/")
	for i := 0; i < len(parts)-1; i++ {
		if searchcore.ShouldSkipDir(parts[i]) {
			return true
		}
	}
	return false
}

// grep runs one ripgrep process per search root. ripgrep's --sort path sorts
// globally across roots, which would page differently from the walker's
// root-by-root walk (and from the local tool's per-scope loop); one process per
// root keeps the page composition identical on every engine.
func (r ripgrepRunner) grep(ctx context.Context, opts searchcore.GrepOptions) (searchcore.GrepResult, bool, error) {
	if !r.available() {
		return searchcore.GrepResult{}, false, nil
	}
	roots := ripgrepGrepRoots(opts)
	if len(roots) <= 1 {
		return r.grepSingle(ctx, opts)
	}
	if opts.Limit < 1 {
		opts.Limit = searchcore.DefaultGrepLimit
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	mode, err := searchcore.NormalizeGrepMode(string(opts.Mode))
	if err != nil {
		return searchcore.GrepResult{}, true, err
	}
	// Page entries needed including the one that proves has_more. In content
	// mode an entry is a match; in files/count mode it is a whole file, which
	// is also why the page's last count is trustworthy.
	needed := opts.Offset + opts.Limit + 1
	collected := func(matches []searchcore.GrepMatch) int {
		if mode == searchcore.GrepModeContent {
			return len(matches)
		}
		return searchcore.CountDistinctGrepFiles(matches)
	}
	combined := searchcore.GrepResult{}
	for _, root := range roots {
		remaining := needed - collected(combined.Matches)
		if remaining < 1 {
			break
		}
		perRoot := opts
		perRoot.Path = root
		perRoot.Paths = nil
		perRoot.Offset = 0
		perRoot.Limit = remaining
		page, used, err := r.grepSingle(ctx, perRoot)
		if err != nil {
			return searchcore.GrepResult{}, true, err
		}
		if !used {
			// A filter this root cannot express must take the walker for the
			// whole call, not just this root.
			return searchcore.GrepResult{}, false, nil
		}
		combined.FilesScanned += page.FilesScanned
		combined.Matches = append(combined.Matches, page.Matches...)
		if searchcore.GrepPageComplete(combined.Matches, mode, opts) {
			break
		}
	}
	combined.SingleFile = false
	searchcore.ApplyGrepPageWindow(&combined, mode, opts)
	return combined, true, nil
}

func ripgrepGrepRoots(opts searchcore.GrepOptions) []string {
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

func (r ripgrepRunner) grepSingle(ctx context.Context, opts searchcore.GrepOptions) (searchcore.GrepResult, bool, error) {
	if !r.available() {
		return searchcore.GrepResult{}, false, nil
	}
	if opts.Limit < 1 {
		opts.Limit = searchcore.DefaultGrepLimit
	}
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	mode, err := searchcore.NormalizeGrepMode(string(opts.Mode))
	if err != nil {
		return searchcore.GrepResult{}, true, err
	}
	var globMatcher *searchcore.GlobMatcher
	if strings.TrimSpace(opts.Glob) != "" {
		matcher, err := searchcore.NewGlobMatcher(opts.Glob)
		if err != nil {
			return searchcore.GrepResult{}, true, err
		}
		if !safeRipgrepGlob(opts.Glob) {
			return searchcore.GrepResult{}, false, nil
		}
		globMatcher = matcher
	}
	// Include/exclude share the glob safety rule: ripgrep's path matching and
	// the walker's basename/relative matching only agree on the simple forms
	// safeRipgrepGlob accepts. Anything else takes the walker so one filter
	// language serves both paths.
	includeGlobs, excludeGlobs := searchcore.EffectiveGrepFilters(opts)
	for _, pattern := range append(append([]string{}, includeGlobs...), excludeGlobs...) {
		if !safeRipgrepGlob(pattern) {
			return searchcore.GrepResult{}, false, nil
		}
	}
	roots := make([]string, 0, 1+len(opts.Paths))
	if strings.TrimSpace(opts.Path) != "" {
		roots = append(roots, opts.Path)
	}
	for _, root := range opts.Paths {
		if strings.TrimSpace(root) != "" {
			roots = append(roots, root)
		}
	}
	if len(roots) == 0 {
		return searchcore.GrepResult{}, true, fmt.Errorf("ripgrep: no search root")
	}
	result := searchcore.GrepResult{}
	if len(roots) == 1 {
		if info, statErr := os.Stat(roots[0]); statErr == nil && info.Mode().IsRegular() {
			result.SingleFile = true
		}
	}

	args := append(r.baseArgs(), "--text", "--json")
	if globMatcher != nil {
		args = append(args, "--glob", opts.Glob)
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
	if searchcore.EffectiveIgnoreCase(patterns, searchcore.GrepPatternOptions{
		IgnoreCase:    opts.IgnoreCase || opts.CaseInsensitive,
		CaseSensitive: opts.CaseSensitive,
		SmartCase:     opts.SmartCase,
	}) {
		args = append(args, "--ignore-case")
	} else if opts.CaseSensitive {
		args = append(args, "--case-sensitive")
	}
	if opts.MaxFileBytes > 0 {
		args = append(args, "--max-filesize", strconv.Itoa(opts.MaxFileBytes))
	}
	// An explicit 0 means "this file contributes nothing", which is exactly
	// what rg -m 0 does; unset means no per-file cap.
	if opts.MaxCountSet {
		args = append(args, "--max-count", strconv.Itoa(opts.MaxCount))
	}
	args = append(args, r.skipArgs()...)
	for _, pattern := range patterns {
		args = append(args, "-e", pattern)
	}
	args = append(args, "--")
	args = append(args, roots...)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, r.path, args...)
	stdout, pipeErr := cmd.StdoutPipe()
	if pipeErr != nil {
		return searchcore.GrepResult{}, true, pipeErr
	}
	stderr := &cappedBuffer{limit: rgStderrCapBytes}
	cmd.Stderr = stderr
	if startErr := cmd.Start(); startErr != nil {
		return searchcore.GrepResult{}, true, startErr
	}

	// With one root ripgrep may print paths relative to it; with several roots
	// every root is absolute and so are the printed paths.
	baseDir := ""
	if len(roots) == 1 {
		baseDir = roots[0]
		if result.SingleFile {
			baseDir = filepath.Dir(roots[0])
		}
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), rgScanMaxLineBytes)
	truncated := false
	for scanner.Scan() {
		var event rgJSONEvent
		if jsonErr := json.Unmarshal(scanner.Bytes(), &event); jsonErr != nil {
			continue
		}
		switch event.Type {
		case "summary":
			result.FilesScanned = event.Data.Stats.FilesSearched
		case "match":
			matchPath := filepath.FromSlash(event.Data.Path.Text)
			if !filepath.IsAbs(matchPath) {
				matchPath = filepath.Join(baseDir, matchPath)
			}
			if globMatcher != nil && !globMatcher.Match(filepath.Base(matchPath)) {
				continue
			}
			var text string
			switch {
			case event.Data.Lines.Bytes != nil:
				raw, decodeErr := base64.StdEncoding.DecodeString(*event.Data.Lines.Bytes)
				if decodeErr != nil {
					continue
				}
				text = string(raw)
			case event.Data.Lines.Text != nil:
				text = *event.Data.Lines.Text
			}
			text = strings.TrimSuffix(strings.TrimSuffix(text, "\n"), "\r")
			if !utf8.ValidString(text) {
				continue
			}
			if len(text) > searchcore.MaxGrepLineBytes {
				text = text[:searchcore.MaxGrepLineBytes] + "…"
			}
			result.Matches = append(result.Matches, searchcore.GrepMatch{
				File: matchPath,
				Line: event.Data.LineNumber,
				Text: text,
			})
			// Stop one page entry past the page so Truncated/has_more stays
			// accurate; the shared window then slices the raw list down.
			if searchcore.GrepPageComplete(result.Matches, mode, opts) {
				truncated = true
				cancel()
				break
			}
		}
		if truncated {
			break
		}
	}
	scanErr := scanner.Err()
	if truncated {
		_ = cmd.Wait()
		searchcore.ApplyGrepPageWindow(&result, mode, opts)
		result.Truncated = true
		return result, true, nil
	}
	waitErr := cmd.Wait()
	if ctx != nil && ctx.Err() != nil {
		return searchcore.GrepResult{}, true, ctx.Err()
	}
	if scanErr != nil {
		return searchcore.GrepResult{}, true, scanErr
	}
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return result, true, nil
		}
		return searchcore.GrepResult{}, true, fmt.Errorf("ripgrep: %v: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	searchcore.ApplyGrepPageWindow(&result, mode, opts)
	return result, true, nil
}

// safeRipgrepGlob reports whether the walker's glob interpretation is a
// guaranteed superset of ripgrep's. Constructs with different meanings
// (negation, character classes, braces, escapes and rooted patterns) take the
// pure-Go walker instead of risking a silent false negative.
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

func (r ripgrepRunner) runLines(ctx context.Context, dir string, args []string, maxLines int) ([]string, bool, error) {
	if maxLines < 1 {
		maxLines = 1
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, r.path, args...)
	cmd.Dir = dir
	stdout, pipeErr := cmd.StdoutPipe()
	if pipeErr != nil {
		return nil, false, pipeErr
	}
	stderr := &cappedBuffer{limit: rgStderrCapBytes}
	cmd.Stderr = stderr
	if startErr := cmd.Start(); startErr != nil {
		return nil, false, startErr
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), rgScanMaxLineBytes)
	capacity := maxLines
	if capacity > 1024 {
		capacity = 1024
	}
	lines := make([]string, 0, capacity)
	truncated := false
	for scanner.Scan() {
		if len(lines) >= maxLines {
			truncated = true
			cancel()
			break
		}
		lines = append(lines, scanner.Text())
	}
	scanErr := scanner.Err()
	if truncated {
		_ = cmd.Wait()
		return lines, true, nil
	}
	waitErr := cmd.Wait()
	if ctx != nil && ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	if scanErr != nil {
		return nil, false, scanErr
	}
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return lines, false, nil
		}
		return nil, false, fmt.Errorf("ripgrep: %v: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return lines, false, nil
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
		Stats      struct {
			FilesSearched int `json:"files_searched"`
		} `json:"stats"`
	} `json:"data"`
}

type cappedBuffer struct {
	data  bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
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

func (b *cappedBuffer) String() string {
	if b == nil {
		return ""
	}
	return b.data.String()
}
