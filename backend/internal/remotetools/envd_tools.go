package remotetools

// envd_tools.go carries the envd execution bodies for the file surface.
//
// These are not copies of the shared cores: viewcore/lscore/searchcore/mutatecore
// resolve and read through the LOCAL filesystem (os.*), which is precisely what an
// envd target is not. Each spec below therefore passes the backend jail first and
// then uses either an envd RPC or one spawned process — envd exposes no ReadFile
// or WriteFile, so reads run cat and writes run printf with content as a
// positional argv element (no quoting of caller data, hence the argv-size cap).
//
// Output folding is deliberately NOT done here: the shared adapter folds through
// toolcoreoutput.ShapeText with the configured ModelVisibleCap, so one budget rule
// governs both backends.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// envdMaxWriteBytes bounds one remote write. The cap is the argv limit, not a
// policy choice: larger content must go through shell or be split.
const envdMaxWriteBytes = 1 << 20

// Tool names the envd backend adds beyond the shared surface.
const (
	ToolMkdir       = "mkdir"
	ToolRemove      = "remove"
	ToolMove        = "move"
	ToolHostMetrics = "host_metrics"
)

// envdStringProp is one JSON-schema string property.
func envdStringProp(desc string) map[string]interface{} {
	return map[string]interface{}{"type": "string", "description": desc}
}

// envdFileToolSpecs returns the file-surface specs beyond `view`.
func envdFileToolSpecs(base remoteToolBase) []envdToolSpec {
	return []envdToolSpec{
		{
			name:        ToolLs,
			description: base.describe("List a directory on the envd target. Returns entry type, size, permissions and name, sorted; symlink targets are shown."),
			version:     "1.0.0",
			parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": envdStringProp("Directory path on the target, absolute or relative to the workspace."),
					"depth": map[string]interface{}{
						"type":        "integer",
						"description": "Recursion depth; 0 or 1 lists one level.",
					},
				},
			},
			call: envdLs,
		},
		{
			name:        ToolGlob,
			description: base.describe("Find files under a directory on the envd target whose base name matches a shell glob pattern. Runs a remote find, so it never descends outside the given root."),
			version:     "1.0.0",
			parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":    envdStringProp("Directory to search under."),
					"pattern": envdStringProp("Base-name glob, for example *.go."),
				},
				"required": []string{"path", "pattern"},
			},
			call: envdGlob,
		},
		{
			name:        ToolGrep,
			description: base.describe("Search file contents on the envd target. Uses ripgrep when present, otherwise grep -rnI; the result reports which engine ran, because the two dialects differ."),
			version:     "1.0.0",
			parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":    envdStringProp("Directory or file to search."),
					"pattern": envdStringProp("Regular expression to match."),
				},
				"required": []string{"path", "pattern"},
			},
			call: envdGrep,
		},
		{
			name:        ToolWrite,
			description: base.describe("Write a complete file on the envd target (create or overwrite), creating parent directories. Content is capped at the argv limit; larger files need shell."),
			version:     "1.0.0",
			parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path":    envdStringProp("Destination file path."),
					"content": envdStringProp("Full file content to write."),
				},
				"required": []string{"path", "content"},
			},
			call: envdWrite,
		},
		{
			name:        ToolMkdir,
			description: base.describe("Create a directory on the envd target."),
			version:     "1.0.0",
			parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"path": envdStringProp("Directory path to create.")},
				"required":   []string{"path"},
			},
			call: envdMkdir,
		},
		{
			name:        ToolRemove,
			description: base.describe("Remove a path on the envd target. A symlink is removed as the link, not its target; the jail roots themselves refuse."),
			version:     "1.0.0",
			parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{"path": envdStringProp("Path to remove.")},
				"required":   []string{"path"},
			},
			call: envdRemove,
		},
		{
			name:        ToolMove,
			description: base.describe("Move or rename a path on the envd target. Both ends are checked against the jail, so a move cannot write outside the workspace."),
			version:     "1.0.0",
			parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"source":      envdStringProp("Existing path."),
					"destination": envdStringProp("Target path."),
				},
				"required": []string{"source", "destination"},
			},
			call: envdMove,
		},
		{
			name:        ToolHostMetrics,
			description: base.describe("Report host capacity of the envd target: CPU count, memory and disk usage as reported by the daemon."),
			version:     "1.0.0",
			parameters: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
			call: envdHostMetrics,
		},
	}
}

// envdLs lists one directory. Sorting and formatting stay here because the tool
// layer owns the presentation contract; envd returns raw directory order.
func envdLs(ctx context.Context, be *envdBackend, params map[string]interface{}) (*toolkit.ToolResult, error) {
	raw := envdStringParam(params, "path")
	if raw == "" {
		raw = "."
	}
	target, err := be.resolvePath(ctx, raw)
	if err != nil {
		return envdErrorResult("%v", err), nil
	}
	depth := 1
	if value, ok := params["depth"].(float64); ok && int(value) > 0 {
		depth = int(value)
	}
	entries, err := be.client.ListDir(ctx, target, uint32(depth))
	if err != nil {
		return envdErrorResult("%s", describeEnvDError(err, target)), nil
	}
	if len(entries) == 0 {
		return envdTextResult(fmt.Sprintf("%s is empty", target)), nil
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

	var builder strings.Builder
	fmt.Fprintf(&builder, "%s (%d entries)\n", target, len(entries))
	for _, entry := range entries {
		kind := "file"
		switch {
		case entry.IsDir():
			kind = "dir"
		case entry.IsSymlink():
			kind = "link"
		}
		fmt.Fprintf(&builder, "%-4s %10d %-10s %s\n", kind, entry.SizeBytes(), entry.Permissions, entry.Name)
		if entry.IsSymlink() && entry.SymlinkTarget != "" {
			fmt.Fprintf(&builder, "     -> %s\n", entry.SymlinkTarget)
		}
	}
	return envdTextResult(builder.String()), nil
}

// envdGlob runs a remote find rather than walking the tree from here: the target
// tree is not mirrored locally, and a walk would cost one RPC per directory.
func envdGlob(ctx context.Context, be *envdBackend, params map[string]interface{}) (*toolkit.ToolResult, error) {
	root := envdStringParam(params, "path")
	if root == "" {
		return envdErrorResult("path is required"), nil
	}
	pattern := envdStringParam(params, "pattern")
	if pattern == "" {
		return envdErrorResult("pattern is required"), nil
	}
	target, err := be.resolvePath(ctx, root)
	if err != nil {
		return envdErrorResult("%v", err), nil
	}
	stdout, stderr, code, err := be.run(ctx, "/usr/bin/find", []string{target, "-name", pattern, "-type", "f", "-print"})
	if err != nil {
		return envdErrorResult("glob %s: %v", target, err), nil
	}
	if code != 0 {
		return envdErrorResult("glob failed (exit %d): %s", code, strings.TrimSpace(string(stderr))), nil
	}
	matches := strings.TrimSpace(string(stdout))
	if matches == "" {
		return envdTextResult(fmt.Sprintf("no files under %s match %q", target, pattern)), nil
	}
	lines := strings.Split(matches, "\n")
	sort.Strings(lines)
	return envdTextResult(strings.Join(lines, "\n")), nil
}

// envdGrepEnginePrefix marks which engine the one-shot shell choice picked.
//
// It is echoed BEFORE the search runs, not after it: a capped read can stop in
// the middle of a large result set, and a marker at the end would be lost with
// the results it describes. Reported engine is not decoration - rg and grep -rnI
// speak different pattern dialects - so the answer must survive truncation.
const envdGrepEnginePrefix = "__TOOL__="

// envdGrep searches file contents. One shell invocation chooses the engine, and
// the chosen engine is reported: silently switching tools changes the dialect of
// the pattern.
//
// Output is consumed as a stream and stopped at the surface's own byte ceiling,
// so a recursive scan costs the window rather than the whole tree - and the
// search actually halts on the target instead of running to completion behind a
// buffer that would have discarded the tail anyway.
func envdGrep(ctx context.Context, be *envdBackend, params map[string]interface{}) (*toolkit.ToolResult, error) {
	pattern := envdStringParam(params, "pattern")
	if pattern == "" {
		if alias := envdStringParam(params, "regexp"); alias != "" {
			pattern = alias
		}
	}
	if pattern == "" {
		return envdErrorResult("pattern is required"), nil
	}
	root := envdStringParam(params, "path")
	if root == "" {
		root = "."
	}
	target, err := be.resolvePath(ctx, root)
	if err != nil {
		return envdErrorResult("%v", err), nil
	}
	// Pattern and path stay positional ("$1", "$2"): no caller data is ever quoted
	// into the script.
	script := `if command -v rg >/dev/null 2>&1; then echo "` + envdGrepEnginePrefix + `rg"; rg --line-number --no-heading --color never --max-columns 400 -e "$1" "$2" 2>&1; ` +
		`else echo "` + envdGrepEnginePrefix + `grep"; grep -rnI --color=never -e "$1" "$2" 2>&1; fi`

	limit := be.viewLimit()
	stdout, stderr, code, truncated, err := be.runCapped(ctx, "/bin/sh", []string{"-c", script, "aicli-envd-grep", pattern, target}, limit)
	if err != nil {
		return envdErrorResult("grep %s: %v", target, err), nil
	}

	out, engine := splitEnvdEngineMarker(string(stdout))
	out = strings.TrimRight(out, "\n")

	// A stopped search has no exit status on purpose, and it is not a failure.
	if !truncated && code != 0 && code != 1 && out == "" {
		return envdErrorResult("grep failed (exit %d): %s", code, strings.TrimSpace(string(stderr))), nil
	}
	if strings.TrimSpace(out) == "" && !truncated {
		return envdTextResult(fmt.Sprintf("no matches for %q under %s (searched with %s)", pattern, target, engine)), nil
	}

	content := fmt.Sprintf("searched with %s\n%s", engine, out)
	metadata := map[string]interface{}{"execution_backend": engine}
	if truncated {
		// Say so plainly: a capped list reads like a complete one otherwise. The
		// surface has no paging parameters yet, so the only honest next step is a
		// narrower search.
		content += fmt.Sprintf("\n(stopped at %d bytes: this is not the full match set, narrow the path or pattern to see more)", limit)
		metadata["is_truncated"] = true
	}
	result := envdTextResult(content)
	result.Metadata = metadata
	return result, nil
}

// splitEnvdEngineMarker removes the engine line and reports the engine it named.
// An absent marker is reported as "unknown" rather than guessed: the two engines
// differ in dialect, and inventing one would be worse than admitting the loss.
func splitEnvdEngineMarker(out string) (rest, engine string) {
	if !strings.HasPrefix(out, envdGrepEnginePrefix) {
		return out, "unknown"
	}
	line := out
	if newline := strings.Index(out, "\n"); newline >= 0 {
		line, rest = out[:newline], out[newline+1:]
	} else {
		rest = ""
	}
	return rest, strings.TrimSpace(strings.TrimPrefix(line, envdGrepEnginePrefix))
}

// envdWrite writes a file. Content arrives as "$2", so no caller data is ever
// quoted into the script; the size cap is the argv limit. The stat afterwards
// confirms the bytes landed, because a failed redirect would otherwise be
// reported as success.
func envdWrite(ctx context.Context, be *envdBackend, params map[string]interface{}) (*toolkit.ToolResult, error) {
	raw := envdStringParam(params, "path")
	if raw == "" {
		return envdErrorResult("path is required"), nil
	}
	content, _ := params["content"].(string)
	// Checked before the path is resolved: a write that no available channel can
	// carry must not touch the target at all. The raw spelling is echoed only.
	if refusal := be.refuseUncarriableWrite(ctx, raw, []byte(content)); refusal != nil {
		return refusal, nil
	}
	target, resolveErr := be.resolvePath(ctx, raw)
	if resolveErr != nil {
		return envdToolError(resolveErr), nil
	}
	// Created vs Updated is decided from the target, like the local surface does,
	// so the same tool answers the same way on either transport.
	verb := "Created"
	if entry, statErr := be.client.Stat(ctx, target); statErr == nil {
		if entry.IsDir() {
			return envdErrorResult("target is a directory: %s", target), nil
		}
		verb = "Updated"
	} else if !errorsAsConnectCode(statErr, "not_found") {
		return envdErrorResult("%s", describeEnvDError(statErr, target)), nil
	}
	// createParents=false matches the shared write contract: a missing parent is a
	// refusal, not a silent mkdir (apply_patch add is the tool that creates trees).
	sizeAfter, channel, refusal := be.storeBytes(ctx, target, []byte(content), false)
	if refusal != nil {
		return refusal, nil
	}
	result := envdTextResult(fmt.Sprintf("%s %s (%d bytes).", verb, be.workspaceRelative(target), sizeAfter))
	// Which channel moved the bytes is observable, not guessed: the ceiling a
	// refusal reports depends on it, and envdGrep already publishes its engine for
	// the same reason.
	result.Metadata = map[string]interface{}{"write_channel": channel}
	return result, nil
}

// envdMkdir creates a directory. An existing directory is reported as such
// rather than failing: mkdir is used idempotently by agents preparing layouts.
func envdMkdir(ctx context.Context, be *envdBackend, params map[string]interface{}) (*toolkit.ToolResult, error) {
	raw := envdStringParam(params, "path")
	if raw == "" {
		return envdErrorResult("path is required"), nil
	}
	target, err := be.resolvePath(ctx, raw)
	if err != nil {
		return envdErrorResult("%v", err), nil
	}
	if err := be.client.MakeDir(ctx, target); err != nil {
		if errorsAsConnectCode(err, "already_exists") {
			return envdTextResult(fmt.Sprintf("%s already exists", target)), nil
		}
		return envdErrorResult("mkdir %s: %v", target, err), nil
	}
	return envdTextResult(fmt.Sprintf("created %s", target)), nil
}

// envdRemove deletes a path through the mutation jail (parent canonicalized,
// final component untouched so a symlink is deleted as a link).
func envdRemove(ctx context.Context, be *envdBackend, params map[string]interface{}) (*toolkit.ToolResult, error) {
	raw := envdStringParam(params, "path")
	if raw == "" {
		return envdErrorResult("path is required"), nil
	}
	target, err := be.resolveMutationPath(ctx, raw)
	if err != nil {
		return envdErrorResult("%v", err), nil
	}
	if err := be.client.Remove(ctx, target); err != nil {
		return envdErrorResult("%s", describeEnvDError(err, target)), nil
	}
	return envdTextResult(fmt.Sprintf("removed %s", target)), nil
}

// envdMove renames or moves. Both ends pass the mutation jail: checking only the
// source would let a move write outside the workspace.
func envdMove(ctx context.Context, be *envdBackend, params map[string]interface{}) (*toolkit.ToolResult, error) {
	sourceRaw := envdStringParam(params, "source")
	destRaw := envdStringParam(params, "destination")
	if sourceRaw == "" || destRaw == "" {
		return envdErrorResult("source and destination are required"), nil
	}
	source, err := be.resolveMutationPath(ctx, sourceRaw)
	if err != nil {
		return envdErrorResult("source: %v", err), nil
	}
	destination, err := be.resolveMutationPath(ctx, destRaw)
	if err != nil {
		return envdErrorResult("destination: %v", err), nil
	}
	if err := be.client.Move(ctx, source, destination); err != nil {
		return envdErrorResult("move %s -> %s: %v", source, destination, err), nil
	}
	return envdTextResult(fmt.Sprintf("moved %s -> %s", source, destination)), nil
}

// envdHostMetrics reports target capacity. Some envd builds omit `available`
// from /metrics; printing 0 MiB would read as "no memory free" rather than
// "not reported", so the field degrades to n/a.
func envdHostMetrics(ctx context.Context, be *envdBackend, _ map[string]interface{}) (*toolkit.ToolResult, error) {
	metrics, err := be.client.Metrics(ctx)
	if err != nil {
		return envdErrorResult("metrics: %v", err), nil
	}
	available := "n/a"
	if metrics.MemAvailable > 0 {
		available = fmt.Sprintf("%d MiB", metrics.MemAvailable>>20)
	}
	return envdTextResult(fmt.Sprintf("envd %s\ncpus: %d\nmemory: %d MiB total, %d MiB used\nmemory available: %s\ndisk: %d MiB used of %d MiB",
		be.client.Addr(), metrics.CPUCount,
		metrics.MemTotal>>20, metrics.MemUsed>>20,
		available,
		metrics.DiskUsed>>20, metrics.DiskTotal>>20)), nil
}

// errorsAsConnectCode reports whether an envd error carries one Connect code.
func errorsAsConnectCode(err error, code string) bool {
	var connectErr *envdclient.ConnectError
	if errors.As(err, &connectErr) {
		return connectErr.Code == code
	}
	return false
}
