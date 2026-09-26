package chat

import (
	"os"
	"strings"

	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// 本文件实现 §4.8 的 grant store 接线：每个会话一个内存 store（session 作用域），
// 加上工作区维度的 durable store（project 作用域，落 <workspace>/.aicli/grants.json）。
// 审批响应通过 ApprovalResponse.RememberScope 选择写哪个 store；引擎的
// grants 阶段同时查询两者。

// permissionGrantStores lazily builds the actor's grant stores. The session
// store is always available; the project store needs a workspace root and is
// left nil when the root cannot be resolved (project-scoped remembers then fall
// back to the session store inside the engine).
func (a *SessionActor) permissionGrantStores() (*runtimepolicy.MemoryGrantStore, runtimepolicy.GrantStore) {
	if a == nil {
		return nil, nil
	}
	a.grantsOnce.Do(func() {
		a.sessionGrants, a.projectGrants = newPermissionGrantStores(a.permissionWorkspaceRoot())
	})
	return a.sessionGrants, a.projectGrants
}

// newPermissionGrantStores builds the session/project pair for one workspace
// root; an empty or unusable root leaves the project store nil.
func newPermissionGrantStores(root string) (*runtimepolicy.MemoryGrantStore, runtimepolicy.GrantStore) {
	session := &runtimepolicy.MemoryGrantStore{}
	root = strings.TrimSpace(root)
	if root == "" {
		return session, nil
	}
	store, err := runtimepolicy.OpenProjectGrantStore(root)
	if err != nil {
		logpkg.Warnf("permission grants: project store unavailable for %s: %v", root, err)
		return session, nil
	}
	return session, store
}

// permissionWorkspaceRoot resolves the workspace root used for the durable
// grant store: the tool policy anchor first (hosts set it from the runtime
// workspace config), then the process working directory.
func (a *SessionActor) permissionWorkspaceRoot() string {
	if a == nil || a.agent == nil {
		return ""
	}
	if policy := a.agent.GetToolExecutionPolicy(); policy != nil {
		if root := strings.TrimSpace(policy.PathAnchorRoot); root != "" {
			return root
		}
	}
	if root, err := os.Getwd(); err == nil {
		return strings.TrimSpace(root)
	}
	return ""
}

// attachPermissionGrantStores wires the stores onto an engine without
// clobbering stores a host already provided (e.g. tests or embedded hosts).
func attachPermissionGrantStores(engine *runtimepolicy.Engine, session *runtimepolicy.MemoryGrantStore, project runtimepolicy.GrantStore) {
	if engine == nil {
		return
	}
	if engine.Grants == nil && session != nil {
		engine.Grants = session
	}
	if engine.ProjectGrants == nil && project != nil {
		engine.ProjectGrants = project
	}
}
