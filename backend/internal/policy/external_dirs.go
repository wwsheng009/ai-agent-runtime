package policy

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

// This file implements the external-directory gate of
// docs/analysis/commandcode-permissions-design-borrowing-20260926.md §4.5:
// a tool path argument (including shell cwd/workdir) that resolves outside the
// session workspace and outside the session's admitted roots is admitted once
// (ask), then joins the session root set. Exemptions: the OS temp directory
// (silent, reads and writes alike — writes still follow the normal mode rules),
// plan-file writes in plan mode, and registered read-only roots (skill/plugin
// directories) for non-write capabilities. Absolute paths merely *inside* a
// shell command string are deliberately not mined here (same contract as the
// specifier rules, §7).

// externalDirGate returns nil while every inspected target stays inside the
// workspace or the session's admitted roots.
func (e *Engine) externalDirGate(ctx context.Context, req EvalRequest, mode Mode) *Decision {
	if e == nil || e.DisableExternalDirGate {
		return nil
	}
	root := externalDirWorkspaceRoot(ctx, e)
	if root == "" {
		return nil
	}
	paths := collectPathArgs(req.Args, policyArgKeysForTool(req.ToolName))
	if len(paths) == 0 {
		return nil
	}
	roots := append([]string{root}, toolctx.AllowedRoots(ctx)...)
	roots = append(roots, e.ExternalAllowedRoots...)

	outside := make(map[string]bool)
	for _, raw := range paths {
		target := resolveExternalPath(raw, root)
		if target == "" || pathInsideAnyRoot(target, roots) {
			continue
		}
		if e.externalPathExempt(ctx, req, target, mode) {
			continue
		}
		outside[admittedRootFor(target)] = true
	}
	if len(outside) == 0 {
		return nil
	}
	dirs := make([]string, 0, len(outside))
	for dir := range outside {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	detail := strings.Join(dirs, ", ")
	switch mode {
	case ModeBypassPermissions:
		// yolo silently admits the directories instead of prompting (§4.5).
		e.approveExternalDirs(dirs)
		decision := withStage(Decision{Type: DecisionAllow, ExternalDirs: dirs, RuleDetail: detail}, StageExternalDir, "admit_bypass")
		return &decision
	case ModeDontAsk:
		// Fail-closed unattended mode: only pre-approved roots run.
		decision := withStage(Decision{Type: DecisionDeny, ExternalDirs: dirs, RuleDetail: detail}, StageExternalDir, "admit")
		return &decision
	default:
		decision := withStage(Decision{Type: DecisionAsk, ExternalDirs: dirs, RuleDetail: detail}, StageExternalDir, "admit")
		return &decision
	}
}

// externalPathExempt reports whether a target outside every root is exempt from
// the gate.
func (e *Engine) externalPathExempt(ctx context.Context, req EvalRequest, target string, mode Mode) bool {
	for _, temp := range e.externalTempRoots() {
		if pathInsideRoot(temp, target) {
			return true
		}
	}
	// Plan-mode writes to plan files are the plan pipeline's business: the mode
	// stage and the plan write allowlist still apply to them.
	if mode == ModePlan && e.planWriteAllowed(ctx, req) {
		return true
	}
	// Registered skill/plugin roots are read-only exempt; writes keep the gate.
	if !hasCapability(req.Capabilities, CapWriteFS) {
		for _, root := range e.ExternalReadOnlyRoots {
			if pathInsideRoot(canonicalExternalPath(root), target) {
				return true
			}
		}
	}
	return false
}

// approveExternalDirs hands an approved external directory set back to the host
// so it can widen the session root set.
func (e *Engine) approveExternalDirs(dirs []string) {
	if e == nil || e.ApproveExternalDir == nil || len(dirs) == 0 {
		return
	}
	e.ApproveExternalDir(append([]string(nil), dirs...))
}

// externalDirWorkspaceRoot mirrors the anchor used by the rule stage.
func externalDirWorkspaceRoot(ctx context.Context, e *Engine) string {
	root := planWorkspaceRoot(ctx)
	if root == "" && e != nil && e.Policy != nil {
		root = strings.TrimSpace(e.Policy.PathAnchorRoot)
	}
	return canonicalExternalPath(root)
}

// resolveExternalPath expands ~, anchors relative paths to the workspace root,
// and canonicalizes the result (abs + symlinks when resolvable).
func resolveExternalPath(raw, root string) string {
	value := expandUserPath(raw)
	if value == "" {
		return ""
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(root, value)
	}
	return canonicalExternalPath(value)
}

// canonicalExternalPath cleans a path, makes it absolute, and resolves symlinks
// when the path exists.
func canonicalExternalPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) || path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		path = expandUserPath(path)
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

func expandUserPath(path string) string {
	value := strings.TrimSpace(path)
	if value == "" {
		return ""
	}
	if value != "~" && !strings.HasPrefix(value, "~/") && !strings.HasPrefix(value, `~\`) {
		return value
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return value
	}
	if value == "~" {
		return home
	}
	return filepath.Join(home, value[2:])
}

// externalTempRoots returns the silent temp-directory exemptions: the engine
// override when set, otherwise the canonicalized OS temp directory.
func (e *Engine) externalTempRoots() []string {
	configured := []string{}
	if e != nil {
		configured = e.ExternalDirTempRoots
	}
	roots := make([]string, 0, len(configured)+1)
	seen := make(map[string]bool, len(configured)+1)
	appendRoot := func(dir string) {
		dir = canonicalExternalPath(dir)
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		roots = append(roots, dir)
	}
	if len(configured) > 0 {
		for _, dir := range configured {
			appendRoot(dir)
		}
		return roots
	}
	appendRoot(os.TempDir())
	return roots
}

func pathInsideAnyRoot(target string, roots []string) bool {
	for _, root := range roots {
		if pathInsideRoot(canonicalExternalPath(root), target) {
			return true
		}
	}
	return false
}

// pathInsideRoot reports whether target is root itself or inside its subtree.
func pathInsideRoot(root, target string) bool {
	root = strings.TrimSpace(root)
	target = strings.TrimSpace(target)
	if root == "" || target == "" {
		return false
	}
	if runtime.GOOS == "windows" {
		root = strings.ToLower(root)
		target = strings.ToLower(target)
	}
	if root == target {
		return true
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// admittedRootFor picks the directory admitted by an external-dir approval:
// the path itself for directories, the parent directory for files (including
// not-yet-created ones).
func admittedRootFor(target string) string {
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		return target
	}
	return filepath.Dir(target)
}
