package policy

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

// This file owns the precise path semantics of the plan-mode write allowlist
// (docs/analysis/commandcode-plan-mode-design-borrowing-20260925.md §4.7):
//
//   - a target is allowed only when its cleaned path equals an allowlist entry
//     or lives under an allowlist directory (separator-boundary prefix);
//   - base-name equality is deliberately NOT a match, so a file that merely
//     shares the plan file's name (other/plan.md) is not writable;
//   - workspace-resolved (absolute) allow entries win over the raw relative
//     ones, and the target is anchored to the session workspace root the same
//     way the executor anchors it (toolctx.WorkspaceRoot).
//
// Both the permission engine (mode=plan write gate) and the tool execution
// policy (narrow --allow-tool exemption) use these helpers so the two layers
// cannot drift apart.

// planWriteToolNames are the built-in file-mutating tools whose write targets
// the plan allowlist can verify by path. Aliases are normalized (lowercase,
// dashes to underscores) before the lookup; the runtime's control-plane tools
// never belong here.
var planWriteToolNames = map[string]bool{
	"write":              true,
	"write_file":         true,
	"create_file":        true,
	"overwrite_file":     true,
	"edit":               true,
	"update_file":        true,
	"str_replace":        true,
	"str_replace_editor": true,
	"multiedit":          true,
	"multi_edit":         true,
	"append_write":       true,
	"append_file":        true,
	"apply_patch":        true,
	"applypatch":         true,
	"patch_file":         true,
}

// isPlanWriteToolName reports whether a tool mutates file contents through a
// path the plan allowlist can check.
func isPlanWriteToolName(toolName string) bool {
	return planWriteToolNames[normalizeToolName(toolName)]
}

// planWriteTargets returns every file a call may mutate: the explicit path
// arguments of the tool (canonical names plus aliases) and the paths parsed out
// of patch text. Patch parsing covers exactly the headers the runtime executes
// (*** Add File:, *** Update File:, *** Delete File:, *** Move to:) plus
// unified-diff headers; see patchutil.ExtractPaths.
func planWriteTargets(toolName string, args map[string]interface{}) []string {
	if len(args) == 0 {
		return nil
	}
	return collectPathArgs(args, policyArgKeysForTool(toolName))
}

// planWriteTargetsAllowed reports whether every target of a call is covered by
// the plan write allowlist. A call whose targets cannot be extracted is not
// allowed: the allowlist is a verification gate, not a hint.
func planWriteTargetsAllowed(toolName string, args map[string]interface{}, resolved, raw []string, workspaceRoot string) bool {
	targets := planWriteTargets(toolName, args)
	if len(targets) == 0 {
		return false
	}
	for _, target := range targets {
		if !planTargetAllowed(target, resolved, raw, workspaceRoot) {
			return false
		}
	}
	return true
}

// planTargetAllowed reports whether one write target is covered by the plan
// write allowlist.
//
// Resolved (workspace absolute) entries take precedence: when at least one is
// configured, only cleaned absolute equality or a separator-boundary directory
// prefix matches, with a relative target anchored to workspaceRoot first.
// Without resolved entries the raw allowlist is compared as cleaned relative
// paths (the historical default plan.md semantics). Base-name equality is
// never accepted, so "plan.md" no longer licenses "any/dir/plan.md".
func planTargetAllowed(target string, resolved, raw []string, workspaceRoot string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	if hasPlanAllowEntries(resolved) {
		for _, allow := range resolved {
			if planPathsMatch(target, allow, workspaceRoot) {
				return true
			}
		}
		return false
	}
	for _, allow := range raw {
		if planPathsMatch(target, allow, workspaceRoot) {
			return true
		}
	}
	return false
}

func hasPlanAllowEntries(paths []string) bool {
	for _, path := range paths {
		if strings.TrimSpace(path) != "" {
			return true
		}
	}
	return false
}

// cleanPlanAllowPaths trims, cleans, and dedupes allowlist entries the same way
// the engine setters do, so both layers compare identical values.
func cleanPlanAllowPaths(paths []string) []string {
	cleaned := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		cleanedPath := filepath.Clean(path)
		key := planPathKey(cleanedPath)
		if seen[key] {
			continue
		}
		seen[key] = true
		cleaned = append(cleaned, cleanedPath)
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}

// planPathsMatch compares a target and one allowlist entry after cleaning and
// anchoring relative paths to workspaceRoot. Equality or a separator-boundary
// directory prefix both count; a mismatch in absolute/relative space never
// matches, because the two then refer to files resolved under different roots.
func planPathsMatch(target, allow, workspaceRoot string) bool {
	allow = strings.TrimSpace(allow)
	if allow == "" {
		return false
	}
	for _, targetPath := range planPathCandidates(target, workspaceRoot) {
		for _, allowPath := range planPathCandidates(allow, workspaceRoot) {
			if filepath.IsAbs(targetPath) != filepath.IsAbs(allowPath) {
				continue
			}
			if pathEqualOrWithin(targetPath, allowPath) {
				return true
			}
		}
	}
	return false
}

// planPathCandidates returns the cleaned forms of a path: the path itself plus,
// when it is relative and a workspace root is known, its workspace-anchored
// absolute form. This mirrors how the executor resolves relative tool paths
// (toolkit effectiveBasePath / tool_policy resolvePolicyPath).
func planPathCandidates(path, workspaceRoot string) []string {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	cleaned := filepath.Clean(path)
	if filepath.IsAbs(cleaned) {
		return []string{cleaned}
	}
	candidates := []string{cleaned}
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		return candidates
	}
	if !filepath.IsAbs(root) {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			return candidates
		}
		root = absRoot
	}
	return append(candidates, filepath.Clean(filepath.Join(root, cleaned)))
}

// pathEqualOrWithin reports cleaned-path equality or containment under a
// directory prefix. The prefix must end at a path separator, so an allowlist
// entry "docs/plan" covers "docs/plan/child.md" but never "docs/plan-notes.md".
func pathEqualOrWithin(target, allow string) bool {
	if planPathKey(target) == planPathKey(allow) {
		return true
	}
	prefix := allow
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(planPathKey(target), planPathKey(prefix))
}

// planPathKey normalizes a cleaned path for comparison: Windows file systems are
// case-insensitive, POSIX ones are not.
func planPathKey(path string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

// planWorkspaceRoot returns the session-bound workspace root carried in the
// evaluation context, matching the root the executor anchors relative paths to.
func planWorkspaceRoot(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	return strings.TrimSpace(toolctx.WorkspaceRoot(ctx))
}
