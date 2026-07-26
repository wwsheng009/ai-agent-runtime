package commands

import (
	"os"
	"strings"

	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// applyChatPermissionsOverlay loads project .aicli/permissions.yaml (when present)
// and merges CLI --allow-tool / --deny-tool into session.ToolPolicy + PermissionsOverlay.
// Safe to call multiple times; later calls rebuild from current CLI lists + project root.
func applyChatPermissionsOverlay(session *ChatSession, projectRoot string) {
	if session == nil {
		return
	}
	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot == "" {
		if cwd, err := os.Getwd(); err == nil {
			projectRoot = cwd
		}
	}

	var projectFile *runtimepolicy.PermissionsFile
	if projectRoot != "" {
		file, err := runtimepolicy.LoadProjectPermissions(projectRoot)
		if err != nil {
			logpkg.Warnf("load project permissions failed (%s): %v", projectRoot, err)
		} else {
			projectFile = file
		}
	}

	overlay := runtimepolicy.BuildPermissionsOverlay(projectFile, session.CLIAllowTools, session.CLIDenyTools)
	session.PermissionsOverlay = overlay

	// Always re-derive ToolPolicy from the pre-overlay base so repeated calls
	// (cwd bootstrap → resolved workspace root) do not double-intersect allowlists.
	base := session.BaseToolPolicy
	if base == nil && session.ToolPolicy != nil && len(overlay.Rules) == 0 && len(overlay.DenyTools) == 0 && len(overlay.AllowTools) == 0 {
		// No overlay and no explicit base: keep existing policy as-is.
		return
	}
	if base == nil {
		// Capture first-seen policy as base when profile never set one.
		if session.ToolPolicy != nil {
			session.BaseToolPolicy = session.ToolPolicy.Clone()
			base = session.BaseToolPolicy
		}
	}
	if len(overlay.Rules) == 0 && len(overlay.DenyTools) == 0 && len(overlay.AllowTools) == 0 {
		if base != nil {
			session.ToolPolicy = base.Clone()
			if session.FunctionCatalog != nil {
				session.FunctionCatalog.SetToolPolicy(session.ToolPolicy)
			}
		}
		return
	}
	var policy *runtimepolicy.ToolExecutionPolicy
	if base != nil {
		policy = base.Clone()
	}
	session.ToolPolicy = runtimepolicy.ApplyPermissionsOverlayToPolicy(policy, overlay)
	if session.FunctionCatalog != nil && session.ToolPolicy != nil {
		session.FunctionCatalog.SetToolPolicy(session.ToolPolicy)
	}
}

// applyChatPermissionsOverlayToAgent wires overlay rules onto the permission engine
// used by the actor loop (in addition to ToolExecutionPolicy hard gates).
func applyChatPermissionsOverlayToAgent(apiAgent interface {
	GetPermissionEngine() *runtimepolicy.Engine
	SetPermissionEngine(*runtimepolicy.Engine)
}, session *ChatSession) {
	if apiAgent == nil || session == nil {
		return
	}
	overlay := session.PermissionsOverlay
	if len(overlay.Rules) == 0 {
		return
	}
	engine := apiAgent.GetPermissionEngine()
	if engine == nil {
		engine = &runtimepolicy.Engine{}
		runtimepolicy.EnsurePlanWriteAllowPaths(engine)
		apiAgent.SetPermissionEngine(engine)
	}
	// Replace product-sourced rules by rebuilding: keep non-product engine rules if any,
	// but for chat we set product rules first via Apply (prepend).
	// Avoid double-prepending on repeated prepare hooks: strip prior cli:/project: names.
	if len(engine.Rules) > 0 {
		kept := engine.Rules[:0]
		for _, rule := range engine.Rules {
			name := strings.TrimSpace(rule.Name)
			if strings.HasPrefix(name, "cli:") || strings.HasPrefix(name, "project:") {
				continue
			}
			kept = append(kept, rule)
		}
		engine.Rules = kept
	}
	runtimepolicy.ApplyPermissionsOverlayToEngine(engine, overlay)
}
