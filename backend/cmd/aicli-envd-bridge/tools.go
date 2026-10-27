package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
)

// Limits on what one call may return.
//
// These exist because the agent loop pays for every byte twice: once on the wire
// and once in the context window. Truncation is announced rather than silent, so a
// model never reasons over a file believing it saw all of it.
const (
	maxViewBytes  = 1 << 20 // 1 MiB
	maxWriteBytes = 1 << 20 // argv limits make anything larger unreliable
	maxOutputText = 400 << 10
)

// toolView reads a file.
//
// The size is checked before reading so a multi-gigabyte log is refused with a
// clear message instead of stalling the bridge.
func (b *bridge) toolView(ctx context.Context, args json.RawMessage) callResult {
	var in struct {
		Path string `json:"path"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return errorResult("invalid arguments: %v", err)
	}
	target, err := b.resolve(in.Path)
	if err != nil {
		return errorResult("%v", err)
	}

	entry, err := b.client.Stat(ctx, target)
	if err != nil {
		return errorResult("%s", describeEnvDError(err, target))
	}
	if entry.IsDir() {
		return errorResult("%s is a directory; use ls to list it", target)
	}
	if size := entry.SizeBytes(); size > maxViewBytes {
		return errorResult("%s is %d bytes, above the %d byte view limit", target, size, maxViewBytes)
	}

	stdout, stderr, code, err := b.client.Run(ctx, envdclient.StartRequest{
		Process: envdclient.ProcessConfig{Cmd: "/bin/cat", Args: []string{target}, Cwd: b.workspace},
	})
	if err != nil {
		return errorResult("read %s: %v", target, err)
	}
	if code != 0 {
		return errorResult("read %s failed (exit %d): %s", target, code, strings.TrimSpace(string(stderr)))
	}
	return textResult("%s", string(stdout))
}

// toolLs lists a directory.
//
// It is named toolLs rather than toolList because toolList already returns the MCP
// tool catalog; the two would otherwise collide.
func (b *bridge) toolLs(ctx context.Context, args json.RawMessage) callResult {
	var in struct {
		Path  string `json:"path"`
		Depth uint32 `json:"depth"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return errorResult("invalid arguments: %v", err)
	}
	target, err := b.resolve(in.Path)
	if err != nil {
		return errorResult("%v", err)
	}
	depth := in.Depth
	if depth == 0 {
		depth = 1
	}

	entries, err := b.client.ListDir(ctx, target, depth)
	if err != nil {
		return errorResult("%s", describeEnvDError(err, target))
	}
	if len(entries) == 0 {
		return textResult("%s is empty", target)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s (%d entries)\n", target, len(entries))
	for _, entry := range entries {
		kind := "file"
		switch {
		case entry.IsDir():
			kind = "dir"
		case entry.IsSymlink():
			kind = "link"
		}
		fmt.Fprintf(&sb, "%-4s %10d %-10s %s\n", kind, entry.SizeBytes(), entry.Permissions, entry.Name)
		if entry.IsSymlink() && entry.SymlinkTarget != "" {
			fmt.Fprintf(&sb, "     -> %s\n", entry.SymlinkTarget)
		}
	}
	return textResult("%s", sb.String())
}

// toolGlob finds files by base-name pattern.
//
// `find -name` is used rather than a local walk because the target's tree is not
// mirrored here; walking it remotely is one round trip instead of thousands.
func (b *bridge) toolGlob(ctx context.Context, args json.RawMessage) callResult {
	var in struct {
		Path    string `json:"path"`
		Pattern string `json:"pattern"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return errorResult("invalid arguments: %v", err)
	}
	if strings.TrimSpace(in.Pattern) == "" {
		return errorResult("pattern is required")
	}
	root, err := b.resolve(in.Path)
	if err != nil {
		return errorResult("%v", err)
	}

	stdout, stderr, code, err := b.client.Run(ctx, envdclient.StartRequest{
		Process: envdclient.ProcessConfig{
			Cmd:  "/usr/bin/find",
			Args: []string{root, "-name", in.Pattern, "-type", "f", "-print"},
			Cwd:  b.workspace,
		},
	})
	if err != nil {
		return errorResult("glob %s: %v", root, err)
	}
	if code != 0 {
		return errorResult("glob failed (exit %d): %s", code, strings.TrimSpace(string(stderr)))
	}
	matches := strings.TrimSpace(string(stdout))
	if matches == "" {
		return textResult("no files under %s match %q", root, in.Pattern)
	}
	lines := strings.Split(matches, "\n")
	sort.Strings(lines)
	return textResult("%s", truncate(strings.Join(lines, "\n"), maxOutputText))
}

// toolGrep searches file contents.
//
// ripgrep is preferred and grep is the fallback, with the choice reported: silently
// switching tools changes the dialect of the pattern being matched, and a caller
// debugging a surprising result needs to know which one ran.
func (b *bridge) toolGrep(ctx context.Context, args json.RawMessage) callResult {
	var in struct {
		Path    string `json:"path"`
		Pattern string `json:"pattern"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return errorResult("invalid arguments: %v", err)
	}
	if strings.TrimSpace(in.Pattern) == "" {
		return errorResult("pattern is required")
	}
	root, err := b.resolve(in.Path)
	if err != nil {
		return errorResult("%v", err)
	}

	// One shell invocation decides the tool and runs it, so the fallback costs no
	// extra round trip.
	script := `if command -v rg >/dev/null 2>&1; then ` +
		`rg --line-number --no-heading --color never --max-columns 400 -e "$1" "$2" 2>&1 | head -n 400; ` +
		`echo "__TOOL__=rg"; ` +
		`else grep -rnI --color=never -e "$1" "$2" 2>&1 | head -n 400; echo "__TOOL__=grep"; fi`

	stdout, stderr, code, err := b.client.Run(ctx, envdclient.StartRequest{
		Process: envdclient.ProcessConfig{
			Cmd:  "/bin/sh",
			Args: []string{"-c", script, "aicli-envd-grep", in.Pattern, root},
			Cwd:  b.workspace,
		},
	})
	if err != nil {
		return errorResult("grep %s: %v", root, err)
	}

	out := string(stdout)
	tool := "rg"
	if idx := strings.LastIndex(out, "__TOOL__="); idx >= 0 {
		tool = strings.TrimSpace(out[idx+len("__TOOL__="):])
		out = out[:idx]
	}
	out = strings.TrimRight(out, "\n")
	// grep and rg both exit 1 for "no match", which is not a failure.
	if code != 0 && code != 1 && out == "" {
		return errorResult("grep failed (exit %d): %s", code, strings.TrimSpace(string(stderr)))
	}
	if strings.TrimSpace(out) == "" {
		return textResult("no matches for %q under %s (searched with %s)", in.Pattern, root, tool)
	}
	return textResult("searched with %s\n%s", tool, truncate(out, maxOutputText))
}

// toolWrite writes a file.
//
// envd exposes no filesystem write RPC: the filesystem service offers Stat,
// ListDir, MakeDir, Remove, Move and the watcher family, and nothing that accepts
// content. Writing therefore goes through a process, and because envd's stdin
// channel needs HTTP/2 while a plaintext envd only offers HTTP/1.1, the content is
// carried as a *positional argument* rather than on stdin.
//
// That keeps the call injection-free: the shell script is a fixed string, and the
// path and content arrive as "$1" and "$2", so no quoting of caller data is
// involved. The cost is the argv size limit, which is why content is capped at
// 1 MiB.
func (b *bridge) toolWrite(ctx context.Context, args json.RawMessage) callResult {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return errorResult("invalid arguments: %v", err)
	}
	target, err := b.resolve(in.Path)
	if err != nil {
		return errorResult("%v", err)
	}
	if len(in.Content) > maxWriteBytes {
		return errorResult("content is %d bytes, above the %d byte write limit; split the file or use shell", len(in.Content), maxWriteBytes)
	}

	script := `set -e; d=$(dirname -- "$1"); [ -d "$d" ] || mkdir -p -- "$d"; printf '%s' "$2" > "$1"`
	_, stderr, code, err := b.client.Run(ctx, envdclient.StartRequest{
		Process: envdclient.ProcessConfig{
			Cmd:  "/bin/sh",
			Args: []string{"-c", script, "aicli-envd-write", target, in.Content},
			Cwd:  b.workspace,
		},
	})
	if err != nil {
		return errorResult("write %s: %v", target, err)
	}
	if code != 0 {
		return errorResult("write %s failed (exit %d): %s", target, code, strings.TrimSpace(string(stderr)))
	}

	// Confirm the bytes landed: a shell redirect that partially failed would
	// otherwise be reported as success.
	entry, statErr := b.client.Stat(ctx, target)
	if statErr != nil {
		return textResult("wrote %s (%d bytes requested; size not verified: %v)", target, len(in.Content), statErr)
	}
	return textResult("wrote %s (%d bytes on disk)", target, entry.SizeBytes())
}

// toolMkdir creates a directory.
func (b *bridge) toolMkdir(ctx context.Context, args json.RawMessage) callResult {
	var in struct {
		Path string `json:"path"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return errorResult("invalid arguments: %v", err)
	}
	target, err := b.resolve(in.Path)
	if err != nil {
		return errorResult("%v", err)
	}
	if err := b.client.MakeDir(ctx, target); err != nil {
		var connectErr *envdclient.ConnectError
		if errors.As(err, &connectErr) && connectErr.Code == "already_exists" {
			return textResult("%s already exists", target)
		}
		return errorResult("mkdir %s: %v", target, err)
	}
	return textResult("created %s", target)
}

// toolRemove deletes a path.
func (b *bridge) toolRemove(ctx context.Context, args json.RawMessage) callResult {
	var in struct {
		Path string `json:"path"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return errorResult("invalid arguments: %v", err)
	}
	target, err := b.resolve(in.Path)
	if err != nil {
		return errorResult("%v", err)
	}
	if err := b.client.Remove(ctx, target); err != nil {
		return errorResult("%s", describeEnvDError(err, target))
	}
	return textResult("removed %s", target)
}

// toolMove renames or moves a path.
//
// Both ends are resolved through the jail: checking only the source would let a
// move write outside the workspace.
func (b *bridge) toolMove(ctx context.Context, args json.RawMessage) callResult {
	var in struct {
		Source      string `json:"source"`
		Destination string `json:"destination"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return errorResult("invalid arguments: %v", err)
	}
	source, err := b.resolve(in.Source)
	if err != nil {
		return errorResult("source: %v", err)
	}
	destination, err := b.resolve(in.Destination)
	if err != nil {
		return errorResult("destination: %v", err)
	}
	if err := b.client.Move(ctx, source, destination); err != nil {
		return errorResult("move %s -> %s: %v", source, destination, err)
	}
	return textResult("moved %s -> %s", source, destination)
}

// toolShell runs a command.
//
// envd applies no command filtering at all, so this is reachable only under the ops
// profile. The working directory is resolved through the jail, because a command
// that starts outside the workspace could read and write anything the user can.
func (b *bridge) toolShell(ctx context.Context, args json.RawMessage) callResult {
	var in struct {
		Command string `json:"command"`
		Cwd     string `json:"cwd"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return errorResult("invalid arguments: %v", err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return errorResult("command is required")
	}
	cwd := b.workspace
	if strings.TrimSpace(in.Cwd) != "" {
		resolved, err := b.resolve(in.Cwd)
		if err != nil {
			return errorResult("cwd: %v", err)
		}
		cwd = resolved
	}

	stdout, stderr, code, err := b.client.Run(ctx, envdclient.StartRequest{
		Process: envdclient.ProcessConfig{
			Cmd:  "/bin/sh",
			Args: []string{"-c", in.Command},
			Cwd:  cwd,
		},
	})
	if err != nil {
		return errorResult("shell: %v", err)
	}

	var sb strings.Builder
	if len(stdout) > 0 {
		sb.WriteString(truncate(string(stdout), maxOutputText))
	}
	if len(stderr) > 0 {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "[stderr]\n%s", truncate(string(stderr), maxOutputText))
	}
	fmt.Fprintf(&sb, "\n[exit %d]", code)
	return textResult("%s", sb.String())
}

// toolMetrics reports host capacity.
func (b *bridge) toolMetrics(ctx context.Context) callResult {
	metrics, err := b.client.Metrics(ctx)
	if err != nil {
		return errorResult("metrics: %v", err)
	}
	return textResult("envd %s\ncpus: %d\nmemory: %d MiB total, %d MiB used\nmemory available: %d MiB\ndisk: %d MiB used of %d MiB",
		b.client.Addr(), metrics.CPUCount,
		metrics.MemTotal>>20, metrics.MemUsed>>20, metrics.MemAvailable>>20,
		metrics.DiskUsed>>20, metrics.DiskTotal>>20)
}

// loadTLS builds a TLS config from a CA bundle.
//
// The CA is required rather than optional on the TLS path: envd's token travels in
// a Basic header, which is only base64 and therefore only safe under TLS with a
// verified peer.
func loadTLS(caFile string) (*tls.Config, error) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read CA file %q: %w", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("CA file %q contains no valid certificate", caFile)
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}

// describeEnvDError turns a common failure into an actionable message.
//
// A missing path and a bad credential are the two an operator actually hits, and
// both otherwise surface as an opaque code.
func describeEnvDError(err error, target string) string {
	var connectErr *envdclient.ConnectError
	if errors.As(err, &connectErr) {
		switch {
		case connectErr.IsNotFound():
			return fmt.Sprintf("%s does not exist", target)
		case connectErr.IsUnauthenticated():
			return fmt.Sprintf("envd rejected the credentials for %s (%s); check --user and the token environment variable",
				target, connectErr.Message)
		case connectErr.Code == "permission_denied":
			return fmt.Sprintf("permission denied on %s: %s", target, connectErr.Message)
		}
	}
	return err.Error()
}

// truncate caps text and says so.
func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + fmt.Sprintf("\n... [truncated at %d of %d bytes]", limit, len(text))
}
