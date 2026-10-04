package runtimeapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func newSpawnWorkspaceInheritanceHandler(t *testing.T) *Handler {
	t.Helper()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	t.Cleanup(handler.getSessionHub().StopAll)
	handler.SetSessionManager(sessionManager)
	cfg := runtimecfg.DefaultRuntimeConfig()
	// 2 = 允许 spawn 孙代理，nested 用例需要验证 parent_agent_id 逐层链接。
	cfg.Agents.MaxDepth = 2
	handler.SetRuntimeConfig(cfg, "")
	return handler
}

func seedParentWorkspace(t *testing.T, ctx context.Context, handler *Handler, workspace string) *chat.Session {
	t.Helper()
	parent, err := handler.sessionManager.Create(ctx, "user-workspace-inherit")
	require.NoError(t, err)
	if parent.Metadata.Context == nil {
		parent.Metadata.Context = map[string]interface{}{}
	}
	sessionmeta.Set(parent.Metadata.Context, sessionmeta.WorkspacePath, workspace)
	require.NoError(t, handler.sessionManager.Update(ctx, parent))
	return parent
}

// TestSessionAgentControllerSpawn_InheritsParentWorkspacePath 是本次修复的核心回归：
// spawn 装配点必须把父会话绑定的工作目录继承进子会话。
//
// 不继承时子会话的 workspace_path 为空，子 turn 会落到
// agentChatEffectiveWorkspacePath 的兜底分支（空路径 → server cwd），于是子代理
// 在 runtime server 所在目录而不是父代理的仓库里读写文件。
func TestSessionAgentControllerSpawn_InheritsParentWorkspacePath(t *testing.T) {
	ctx := context.Background()
	handler := newSpawnWorkspaceInheritanceHandler(t)

	workspace := t.TempDir()
	parent := seedParentWorkspace(t, ctx, handler, workspace)

	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)
	childID := "workspace-inherit-child"
	result, err := controller.Spawn(ctx, parent.ID, toolbroker.SpawnAgentArgs{ID: childID})
	require.NoError(t, err)
	require.NotNil(t, result)

	// 从存储重新加载：证明绑定不仅进了内存对象，也随子会话持久化。
	child, err := handler.sessionManager.GetStorage().Load(ctx, childID)
	require.NoError(t, err)
	assert.Equal(t, workspace, sessionmeta.String(child.Metadata.Context, sessionmeta.WorkspacePath),
		"child session must inherit the parent working directory")
	// 出处快照：即使生效目录与父一致，来源键也要写下来，供子代理自报家门。
	assert.Equal(t, workspace, agentcontrol.ContextString(child, toolbroker.AgentSessionContextParentWorkspacePath))

	// 工具层可见：spawn_agent 的返回值直接回读子会话状态，必须带上工作目录，
	// 否则父代理无法确认子代理被派到了哪个仓库。
	assert.Equal(t, workspace, result.WorkspacePath)
	assert.Equal(t, "root:"+parent.ID, result.ParentAgentID)
}

// TestSessionAgentControllerSpawn_MintsAgentIdentityAndCarriesParentAgent 覆盖
// 「创建时铸造唯一 agent id」+「子会话携带父 agent 信息」。
func TestSessionAgentControllerSpawn_MintsAgentIdentityAndCarriesParentAgent(t *testing.T) {
	ctx := context.Background()
	handler := newSpawnWorkspaceInheritanceHandler(t)

	workspace := t.TempDir()
	parent := seedParentWorkspace(t, ctx, handler, workspace)

	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)
	childID := "agent-identity-child"
	_, err := controller.Spawn(ctx, parent.ID, toolbroker.SpawnAgentArgs{ID: childID, AgentType: "explore"})
	require.NoError(t, err)

	child, err := handler.sessionManager.GetStorage().Load(ctx, childID)
	require.NoError(t, err)

	agentID := agentcontrol.ContextString(child, toolbroker.AgentSessionContextAgentID)
	require.NotEmpty(t, agentID, "spawn must mint a unique agent id into the child session")
	// 与执行容器同值：保持既有 AgentRecord.AgentID==session_id 的可寻址性不变，
	// 区别在于它现在是显式落库字段，不再依赖隐式约定反推。
	assert.Equal(t, childID, agentID)

	// 父 agent 身份：根代理为 "root:<root_session_id>"。
	assert.Equal(t, "root:"+parent.ID, agentcontrol.ContextString(child, toolbroker.AgentSessionContextParentAgentID))
	assert.Equal(t, parent.ID, agentcontrol.ContextString(child, toolbroker.AgentSessionContextParentSessionID))
}

// TestSessionAgentControllerSpawn_NestedChildCarriesParentAgentID 验证多层 spawn
// 的 parent_agent_id 链逐层指向上一层铸造的 agent id，而不是每层重新从执行容器推导。
func TestSessionAgentControllerSpawn_NestedChildCarriesParentAgentID(t *testing.T) {
	ctx := context.Background()
	handler := newSpawnWorkspaceInheritanceHandler(t)

	workspace := t.TempDir()
	root := seedParentWorkspace(t, ctx, handler, workspace)

	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)
	midID := "agent-identity-mid"
	_, err := controller.Spawn(ctx, root.ID, toolbroker.SpawnAgentArgs{ID: midID})
	require.NoError(t, err)

	leafID := "agent-identity-leaf"
	_, err = controller.Spawn(ctx, midID, toolbroker.SpawnAgentArgs{ID: leafID})
	require.NoError(t, err)

	leaf, err := handler.sessionManager.GetStorage().Load(ctx, leafID)
	require.NoError(t, err)

	// 父是代理会话 → parent_agent_id 读回中层铸造的 agent id。
	assert.Equal(t, midID, agentcontrol.ContextString(leaf, toolbroker.AgentSessionContextParentAgentID))
	// 工作目录逐层继承：中层自己已经继承过 root 的目录，孙层再从中层继承。
	assert.Equal(t, workspace, sessionmeta.String(leaf.Metadata.Context, sessionmeta.WorkspacePath))
}

// TestSessionAgentControllerSpawn_WithoutParentWorkspaceKeepsChildUnbound 守护
// 「不声明 = 零变化」：父会话未绑定目录时，spawn 不得凭空写一个目录。
func TestSessionAgentControllerSpawn_WithoutParentWorkspaceKeepsChildUnbound(t *testing.T) {
	ctx := context.Background()
	handler := newSpawnWorkspaceInheritanceHandler(t)

	parent, err := handler.sessionManager.Create(ctx, "user-no-workspace")
	require.NoError(t, err)

	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)
	childID := "no-workspace-child"
	_, err = controller.Spawn(ctx, parent.ID, toolbroker.SpawnAgentArgs{ID: childID})
	require.NoError(t, err)

	child, err := handler.sessionManager.GetStorage().Load(ctx, childID)
	require.NoError(t, err)
	assert.Empty(t, sessionmeta.String(child.Metadata.Context, sessionmeta.WorkspacePath),
		"child must not invent a working directory when the parent has none")
	// agent 身份仍然要铸：与目录无关。
	assert.NotEmpty(t, agentcontrol.ContextString(child, toolbroker.AgentSessionContextAgentID))
}

// TestApiChildAgentRecord_CarriesWorkspaceAndParentAgent 覆盖控制面：AgentRecord 必须
// 落 working directory 与 parent agent id，否则 list_agents / 监督投影无法回答
// 「这个子代理在哪个目录、属于谁」。
func TestApiChildAgentRecord_CarriesWorkspaceAndParentAgent(t *testing.T) {
	ctx := context.Background()
	handler := newSpawnWorkspaceInheritanceHandler(t)

	workspace := t.TempDir()
	parent := seedParentWorkspace(t, ctx, handler, workspace)

	child := chat.NewSession("agent")
	child.ID = "api-record-child"
	child.SetContext(toolbroker.AgentSessionContextAgentID, child.ID)
	child.SetContext(sessionmeta.WorkspacePath, workspace)

	record := apiChildAgentRecord(parent, parent.ID, child, toolbroker.SpawnAgentArgs{AgentType: "explore"}, 1)
	assert.Equal(t, child.ID, record.AgentID)
	assert.Equal(t, workspace, record.WorkspacePath)
	assert.Equal(t, "root:"+parent.ID, record.ParentAgentID)
	assert.Equal(t, parent.ID, record.ParentSessionID)

	root := apiRootAgentRecord(parent, parent.ID)
	assert.Equal(t, workspace, root.WorkspacePath)
}

// TestApiChildAgentID_FallsBackToSessionID 守护历史数据：agent_id 出现之前创建的子
// 会话必须仍能按 session id 解析出同一个 agent id，否则唤醒/回收/关闭链路会失联。
func TestApiChildAgentID_FallsBackToSessionID(t *testing.T) {
	assert.Empty(t, apiChildAgentID(nil))

	legacy := chat.NewSession("agent")
	legacy.ID = "legacy-child"
	assert.Equal(t, "legacy-child", apiChildAgentID(legacy))

	minted := chat.NewSession("agent")
	minted.ID = "minted-child"
	minted.SetContext(toolbroker.AgentSessionContextAgentID, "minted-child")
	assert.Equal(t, "minted-child", apiChildAgentID(minted))
}
