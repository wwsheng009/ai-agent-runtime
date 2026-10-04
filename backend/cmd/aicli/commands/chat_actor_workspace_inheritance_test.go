package commands

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func newLocalWorkspaceInheritanceHost(t *testing.T) (*localChatRuntimeHost, *runtimechat.Session, string) {
	t.Helper()
	ctx := context.Background()
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(manager.Stop)

	rootSession, err := manager.Create(ctx, userID)
	require.NoError(t, err)

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = teamStore.Close() })

	host := newLocalOrchestrationTestHost(t, manager, userID, runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{}), teamStore)
	host.RuntimeConfig = runtimecfg.DefaultRuntimeConfig()
	host.BaseSession = &ChatSession{
		RuntimeSession:   rootSession,
		SessionUserID:    userID,
		LocalRuntimeHost: host,
	}
	return host, rootSession, userID
}

// TestLocalActorRegistrySpawn_InheritsParentWorkspacePath 是 CLI 侧与 API 侧对等的
// 回归：spawn 装配点必须把父会话绑定的工作目录继承进子会话。
//
// 不继承时子会话 workspace_path 为空，子 turn 会落到 chat_actor_host 里
// workspaceRoot 的兜底值，于是子代理在 runtime 进程目录而不是父代理的仓库里读写。
func TestLocalActorRegistrySpawn_InheritsParentWorkspacePath(t *testing.T) {
	ctx := context.Background()
	host, rootSession, _ := newLocalWorkspaceInheritanceHost(t)

	workspace := t.TempDir()
	if rootSession.Metadata.Context == nil {
		rootSession.Metadata.Context = map[string]interface{}{}
	}
	sessionmeta.Set(rootSession.Metadata.Context, sessionmeta.WorkspacePath, workspace)
	require.NoError(t, host.SessionStore.Save(ctx, rootSession))

	_, err := host.ActorRegistry.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "ws-inherit-child"})
	require.NoError(t, err)

	// 从存储重新加载：证明绑定随子会话持久化，而不是只留在内存对象上。
	child, err := host.SessionStore.Load(ctx, "ws-inherit-child")
	require.NoError(t, err)
	require.Equal(t, workspace, sessionmeta.String(child.Metadata.Context, sessionmeta.WorkspacePath),
		"child session must inherit the parent working directory")
	require.Equal(t, workspace, agentcontrol.ContextString(child, toolbroker.AgentSessionContextParentWorkspacePath))
}

// TestLocalActorRegistrySpawn_MintsAgentIdentity 覆盖 CLI 侧的 agent 身份铸造与
// 父 agent 身份携带，与 API 侧 TestSessionAgentControllerSpawn_MintsAgentIdentity
// 同口径。
func TestLocalActorRegistrySpawn_MintsAgentIdentity(t *testing.T) {
	ctx := context.Background()
	host, rootSession, _ := newLocalWorkspaceInheritanceHost(t)

	_, err := host.ActorRegistry.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "ws-agent-child", AgentType: "explore"})
	require.NoError(t, err)

	child, err := host.SessionStore.Load(ctx, "ws-agent-child")
	require.NoError(t, err)
	require.Equal(t, "ws-agent-child", agentcontrol.ContextString(child, toolbroker.AgentSessionContextAgentID),
		"spawn must mint a unique agent id into the child session")
	require.Equal(t, "root:"+rootSession.ID, agentcontrol.ContextString(child, toolbroker.AgentSessionContextParentAgentID))
}

// TestLocalChildAgentRecord_CarriesWorkspaceAndParentAgent 覆盖 CLI 控制面记录。
func TestLocalChildAgentRecord_CarriesWorkspaceAndParentAgent(t *testing.T) {
	root := runtimechat.NewSession("agent")
	root.ID = "local-record-root"
	root.SetContext(sessionmeta.WorkspacePath, "/repos/main")

	child := runtimechat.NewSession("agent")
	child.ID = "local-record-child"
	child.SetContext(toolbroker.AgentSessionContextAgentID, child.ID)
	child.SetContext(sessionmeta.WorkspacePath, "/repos/main")

	record := localChildAgentRecord(root, root.ID, child, toolbroker.SpawnAgentArgs{AgentType: "explore"}, 1)
	require.Equal(t, child.ID, record.AgentID)
	require.Equal(t, "/repos/main", record.WorkspacePath)
	require.Equal(t, "root:"+root.ID, record.ParentAgentID)

	rootRecord := localRootAgentRecord(root, root.ID)
	require.Equal(t, "/repos/main", rootRecord.WorkspacePath)

	require.Empty(t, localChildAgentID(nil))
	legacy := runtimechat.NewSession("agent")
	legacy.ID = "local-legacy-child"
	require.Equal(t, "local-legacy-child", localChildAgentID(legacy))
}
