package remotetools

// envd_tools.go carries the envd execution bodies for the file surface.
//
// ls/glob/grep are no longer here: since S6 they run the shared cores over the
// envd accessor (see envd_core.go). What remains are the bodies whose execution
// is genuinely backend-specific - writes and mutations - and those are not
// copies of the shared cores either: mutatecore resolves and reads through the
// LOCAL filesystem (os.*), which is precisely what an envd target is not. Each
// spec below therefore passes the backend jail first and then uses either an
// envd RPC or one spawned process — envd exposes no ReadFile or WriteFile, so
// reads run cat and writes run printf with content as a positional argv element
// (no quoting of caller data, hence the argv-size cap).
//
// Output folding is deliberately NOT done here: the shared adapter folds through
// toolcoreoutput.ShapeText with the configured ModelVisibleCap, so one budget rule
// governs both backends.

import (
	"context"
	"errors"
	"fmt"

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
//
// ls/glob/grep are deliberately absent: they are members of the shared-core
// surface (envd_core.go), where one schema and one handler serve every
// transport, instead of specs whose bodies reimplement a search on the target.
func envdFileToolSpecs(base remoteToolBase) []envdToolSpec {
	return []envdToolSpec{
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
	// refusal reports depends on it, and the shared search surfaces publish their
	// engine for the same reason.
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
