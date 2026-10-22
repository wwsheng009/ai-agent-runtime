package remotetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	toolcoreoutput "github.com/wwsheng009/ai-agent-runtime/internal/toolcore/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// Tool names are the canonical (unprefixed) identities. The registry applies the
// configured prefix on top of these.
const (
	ToolView = "view"
	ToolLs   = "ls"
	ToolGlob = "glob"
	ToolGrep = "grep"
)

// readOnlyTools is the P1 surface. Order is stable so tools/list is stable.
var readOnlyTools = []string{ToolView, ToolLs, ToolGlob, ToolGrep}

// writeTools is the P2 mutation surface.
var writeTools = []string{ToolWrite, ToolEdit, ToolAppend, ToolApplyPatch}

// shellTools is the P2 command surface.
var shellTools = []string{ToolShell}

// enabledTools resolves profile + explicit allowlist into the effective names.
//
//   - readonly: the four read tools;
//   - standard: read + write tools;
//   - ops: standard + shell;
//   - custom: exactly cfg.Tools.
//
// In every case cfg.Tools acts as an additional narrowing filter when non-empty.
func (c *Config) enabledTools() []string {
	base := make([]string, 0, len(readOnlyTools)+len(writeTools)+len(shellTools))
	switch c.Profile {
	case ProfileCustom:
		for _, name := range c.Tools {
			trimmed := strings.TrimSpace(name)
			if trimmed != "" {
				base = append(base, trimmed)
			}
		}
	case ProfileReadonly:
		base = append(base, readOnlyTools...)
	case ProfileStandard:
		base = append(base, readOnlyTools...)
		base = append(base, writeTools...)
	case ProfileOps:
		base = append(base, readOnlyTools...)
		base = append(base, writeTools...)
		base = append(base, shellTools...)
	}
	// Job and artifact tools are capabilities, not profile tiers: they are
	// exposed whenever the backing store is configured, because a host that
	// cannot persist output must not offer them at all.
	if c.Jobs.Root != "" && (c.Profile == ProfileOps || c.Profile == ProfileCustom) {
		base = append(base, jobTools...)
	}
	if c.Artifacts.Root != "" {
		base = append(base, artifactTools...)
	}
	// ReadOnly is a hard override: even a misconfigured `ops` profile cannot
	// expose a mutation tool when the operator asked for read-only.
	if c.ReadOnly {
		filtered := make([]string, 0, len(base))
		for _, name := range base {
			switch name {
			case ToolView, ToolLs, ToolGlob, ToolGrep, ToolArtifactRead:
				filtered = append(filtered, name)
			}
		}
		base = filtered
	}
	if len(c.Tools) == 0 {
		return dedupeStrings(base)
	}
	allowed := map[string]bool{}
	for _, name := range c.Tools {
		allowed[strings.TrimSpace(name)] = true
	}
	filtered := make([]string, 0, len(base))
	for _, name := range base {
		if allowed[name] {
			filtered = append(filtered, name)
		}
	}
	return dedupeStrings(filtered)
}

func dedupeStrings(input []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(input))
	for _, item := range input {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}

// remoteToolBase carries the shared workspace/limits wiring. It is embedded by
// each tool implementation.
type remoteToolBase struct {
	cfg       *Config
	workspace *Workspace
}

// describe formats a tool description with the configured style.
//
// "explicit" prepends [remote:<target>] so the model cannot mistake a remote
// path for a local one (design §6.4). It costs a few tokens per tool and is the
// recommended default once more than one target is configured.
func (b remoteToolBase) describe(text string) string {
	if b.cfg == nil || b.cfg.DescriptionStyle != "explicit" {
		return text
	}
	target := b.cfg.TargetName
	if target == "" {
		target = "remote"
	}
	return fmt.Sprintf("[remote:%s] %s This is NOT the local filesystem; paths resolve inside %s.",
		target, text, b.workspace.Root())
}

// resolve maps a tool path through the workspace jail.
//
// The context is consulted first: when the client declared MCP roots, the call
// executes against the narrowed workspace instead of the server's full one. Tools
// therefore never close over a single workspace, which is what lets one registry
// serve sessions with different declarations.
func (b remoteToolBase) resolve(ctx context.Context, raw string) (string, error) {
	return workspaceForCall(ctx, b.workspace).Resolve(raw)
}

// maxViewBytes resolves the payload ceiling for this call.
func (b remoteToolBase) maxViewBytes() int {
	if b.cfg == nil {
		return DefaultMaxBytesPerCall
	}
	if b.cfg.Limits.CaptureLimit > 0 {
		return b.cfg.Limits.CaptureLimit
	}
	if b.cfg.Limits.MaxBytesPerCall > 0 {
		return b.cfg.Limits.MaxBytesPerCall
	}
	return DefaultMaxBytesPerCall
}

// modelVisibleCap resolves the remote trust ceiling for this target.
func (b remoteToolBase) modelVisibleCap() int {
	if b.cfg == nil || b.cfg.Limits.ModelVisibleCap <= 0 {
		return DefaultModelVisibleCap
	}
	cap := b.cfg.Limits.ModelVisibleCap
	// Legacy configs used maxOutputBytes as the model window. Keep a smaller
	// explicitly configured value as a narrowing override while the new v1
	// capture/model split uses modelVisibleCap for the default.
	if legacy := b.cfg.Limits.MaxOutputBytes; legacy > 0 && legacy < cap {
		cap = legacy
	}
	return cap
}

// shapeModelOutput applies the shared output contract before a remote result
// crosses the wire. The local client revalidates this window; shaping here only
// reduces bytes and preserves the capture, it is never an authorization.
func (b remoteToolBase) shapeModelOutput(tool, content string, metadata map[string]interface{}) (string, map[string]interface{}) {
	shaped, shapedMetadata, _, _ := toolcoreoutput.ShapeText(tool, content, metadata, 0, b.modelVisibleCap())
	return shaped, shapedMetadata
}

func boolMetadata(metadata map[string]interface{}, key string) bool {
	if len(metadata) == 0 {
		return false
	}
	switch value := metadata[key].(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(strings.TrimSpace(value), "true")
	case int:
		return value != 0
	case float64:
		return value != 0
	default:
		return false
	}
}

// result builds a successful text result with structured metadata.
func remoteTextResult(content string, metadata map[string]interface{}) *toolkit.ToolResult {
	return &toolkit.ToolResult{
		Success:    true,
		OutputKind: "text",
		Content:    content,
		Metadata:   metadata,
	}
}

// remoteErrorResult builds a classified failure result.
//
// Tool errors are returned as successful tool calls whose content explains the
// failure (matching the local tools' "content result" style) unless the caller
// asks for a hard error; the MCP layer maps IsError separately.
func remoteErrorResult(err error) *toolkit.ToolResult {
	message := err.Error()
	metadata := map[string]interface{}{"error": true}
	var toolErr *ToolError
	if asToolError(err, &toolErr) {
		metadata["error_code"] = toolErr.Code
		if toolErr.NextAction != "" {
			metadata["next_action"] = toolErr.NextAction
		}
		metadata["retriable"] = toolErr.Retriable
	}
	return &toolkit.ToolResult{
		Success:    false,
		OutputKind: "text",
		Error:      fmt.Errorf("%s", message),
		Content:    message,
		Metadata:   metadata,
	}
}

// asToolError is errors.As without importing errors in every tool file.
func asToolError(err error, target **ToolError) bool {
	for err != nil {
		if typed, ok := err.(*ToolError); ok {
			*target = typed
			return true
		}
		type unwrapper interface{ Unwrap() error }
		unwrapped, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = unwrapped.Unwrap()
	}
	return false
}

// truncateUTF8Safe cuts s to at most limit bytes without splitting a rune.
func truncateUTF8Safe(s string, limit int) (string, bool) {
	if limit <= 0 || len(s) <= limit {
		return s, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// isDirectory reports whether path exists and is a directory.
func isDirectory(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.IsDir(), nil
}

// cleanDisplayPath renders a resolved path relative to the workspace for model
// output. Tool results never echo the absolute server path: it leaks host
// layout and differs across machines.
func (b remoteToolBase) cleanDisplayPath(abs string) string {
	return b.workspace.Relative(abs)
}

// ensureNotDevice rejects device/FIFO style paths before any open. Reading a FIFO
// blocks until a writer appears; the local path guard exists for the same reason.
func ensureNotDevice(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	mode := info.Mode()
	switch {
	case mode&os.ModeNamedPipe != 0:
		return newToolError(CodeInvalidArgument, "use a regular file", "path is a named pipe: %s", filepath.Base(path))
	case mode&os.ModeSocket != 0:
		return newToolError(CodeInvalidArgument, "use a regular file", "path is a socket: %s", filepath.Base(path))
	case mode&os.ModeDevice != 0, mode&os.ModeCharDevice != 0:
		return newToolError(CodeInvalidArgument, "use a regular file", "path is a device: %s", filepath.Base(path))
	}
	return nil
}

// contextErr classifies a cancelled context so callers return a stable code.
func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return newToolError(CodeTimeout, "retry with a narrower path or a longer timeout", "request cancelled: %v", err)
	}
	return nil
}
