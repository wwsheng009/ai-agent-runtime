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

func (r ripgrepRunner) glob(ctx context.Context, base, pattern string, limit int) (searchcore.GlobResult, bool, error) {
	if !r.available() {
		return searchcore.GlobResult{}, false, nil
	}
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
		limit = remoteGlobDefaultLimit
	}

	args := append(r.baseArgs(), "--files", "--glob", pattern)
	args = append(args, r.skipArgs()...)
	args = append(args, "--", ".")
	lines, outputTruncated, runErr := r.runLines(ctx, base, args, limit+1)
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
	if outputTruncated || len(result.Matches) > limit {
		result.Truncated = true
	}
	if len(result.Matches) > limit {
		result.Matches = result.Matches[:limit]
	}
	sort.Strings(result.Matches)
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

func (r ripgrepRunner) grep(ctx context.Context, opts searchcore.GrepOptions) (searchcore.GrepResult, bool, error) {
	if !r.available() {
		return searchcore.GrepResult{}, false, nil
	}
	if opts.Limit < 1 {
		opts.Limit = searchcore.DefaultGrepLimit
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
	info, statErr := os.Stat(opts.Path)
	result := searchcore.GrepResult{SingleFile: statErr == nil && info.Mode().IsRegular()}

	args := append(r.baseArgs(), "--text", "--json")
	if globMatcher != nil {
		args = append(args, "--glob", opts.Glob)
	}
	if opts.CaseInsensitive {
		args = append(args, "--ignore-case")
	}
	if opts.MaxFileBytes > 0 {
		args = append(args, "--max-filesize", strconv.Itoa(opts.MaxFileBytes))
	}
	args = append(args, r.skipArgs()...)
	args = append(args, "-e", opts.Pattern, "--", opts.Path)

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

	baseDir := opts.Path
	if result.SingleFile {
		baseDir = filepath.Dir(opts.Path)
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
			if len(result.Matches) > opts.Limit {
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
		if len(result.Matches) > opts.Limit {
			result.Matches = result.Matches[:opts.Limit]
		}
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
	if len(result.Matches) > opts.Limit {
		result.Matches = result.Matches[:opts.Limit]
		result.Truncated = true
	}
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
