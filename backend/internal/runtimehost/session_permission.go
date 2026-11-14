package runtimehost

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// ApplySessionPermissionMode restores the resolved session mode before applying
// agent-definition defaults. This also keeps runs without RunMeta (e.g. actor
// recreation) consistent with the normal spawn/follow-up path. It must not
// change static tool restrictions or permission overlays such as DisableBypass.
func ApplySessionPermissionMode(a *agent.Agent, raw string) {
	if a == nil {
		return
	}
	mode, ok := runtimepolicy.ParseMode(raw)
	if !ok {
		return
	}
	engine := a.GetPermissionEngine()
	if engine == nil {
		engine = agent.NewPermissionEngine()
		a.SetPermissionEngine(engine)
	}
	engine.Mode = mode
	if mode == runtimepolicy.ModePlan {
		runtimepolicy.EnsurePlanWriteAllowPaths(engine)
	}
}
