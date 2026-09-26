package commands

import (
	"os"
	"strings"

	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// applyChatPermissionsOverlay loads the accumulated permission layers
// (~/.aicli/permissions.yaml → <project>/.aicli/permissions.yaml →
// <project>/.aicli/permissions.local.yaml, §4.9) and merges CLI
// --allow-tool / --deny-tool into session.ToolPolicy + PermissionsOverlay.
// Safe to call multiple times; later calls rebuild from BaseToolPolicy + CLI lists + project root.
func applyChatPermissionsOverlay(session *ChatSession, projectRoot string) {
	if session == nil {
		return
	}
	// Capture pre-overlay base once (profile policy or nil). Must happen before any
	// overlay mutation so reloads never double-intersect allowlists.
	if session.BaseToolPolicy == nil && session.ToolPolicy != nil && !chatPermissionsOverlayApplied(session) {
		session.BaseToolPolicy = session.ToolPolicy.Clone()
	}

	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot == "" {
		if cwd, err := os.Getwd(); err == nil {
			projectRoot = cwd
		}
	}

	var mergedFile *runtimepolicy.PermissionsFile
	if projectRoot != "" {
		file, layers, err := runtimepolicy.LoadLayeredPermissions(projectRoot)
		if err != nil {
			logpkg.Warnf("load layered permissions failed (%s): %v", projectRoot, err)
		} else {
			mergedFile = file
			for _, layer := range layers {
				logpkg.Debugf("permissions layer %s: %s", layer.Scope, layer.Path)
			}
		}
	}

	overlay := runtimepolicy.BuildPermissionsOverlay(mergedFile, session.CLIAllowTools, session.CLIDenyTools)
	session.PermissionsOverlay = overlay

	if !chatPermissionsOverlayHasEffect(overlay) {
		if session.BaseToolPolicy != nil {
			session.ToolPolicy = session.BaseToolPolicy.Clone()
			if session.FunctionCatalog != nil {
				session.FunctionCatalog.SetToolPolicy(session.ToolPolicy)
			}
		}
		return
	}

	var policy *runtimepolicy.ToolExecutionPolicy
	if session.BaseToolPolicy != nil {
		policy = session.BaseToolPolicy.Clone()
	}
	session.ToolPolicy = runtimepolicy.ApplyPermissionsOverlayToPolicy(policy, overlay)
	if session.FunctionCatalog != nil && session.ToolPolicy != nil {
		session.FunctionCatalog.SetToolPolicy(session.ToolPolicy)
	}
}

func chatPermissionsOverlayApplied(session *ChatSession) bool {
	if session == nil {
		return false
	}
	return chatPermissionsOverlayHasEffect(session.PermissionsOverlay)
}

func chatPermissionsOverlayHasEffect(overlay runtimepolicy.PermissionsOverlay) bool {
	return len(overlay.Rules) > 0 || len(overlay.DenyTools) > 0 || len(overlay.AllowTools) > 0 || len(overlay.Sources) > 0
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
	engine := apiAgent.GetPermissionEngine()
	if engine == nil && len(overlay.Rules) == 0 && !overlay.DisableBypass {
		return
	}
	if engine == nil {
		engine = &runtimepolicy.Engine{}
		runtimepolicy.EnsurePlanWriteAllowPaths(engine)
		apiAgent.SetPermissionEngine(engine)
	}
	// disable_bypass (§4.9) is a process-level policy independent of rules.
	engine.DisableBypass = overlay.DisableBypass
	if len(overlay.Rules) == 0 {
		return
	}
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
