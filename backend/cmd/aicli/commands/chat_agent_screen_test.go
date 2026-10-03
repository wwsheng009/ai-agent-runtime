package commands

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// newUnifiedChatScreenTestSession 构造一个已启用统一渲染器的测试会话：
// unifiedDirectInteractiveOutput 为 true，副屏 Spec 生产与派发路径与真实
// TUI 会话一致（终端能力由 chatScreenTestSeamsInstall 注入）。
func newUnifiedChatScreenTestSession(t *testing.T) *ChatSession {
	t.Helper()
	t.Setenv("NO_COLOR", "1")
	session := &ChatSession{}
	session.RuntimeEventBridge = newChatRuntimeEventBridge(session)
	interaction := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(interaction.Shutdown)
	session.Interaction = interaction

	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(88, 24)
	interaction.SetSurface(surface)
	var terminal bytes.Buffer
	if !interaction.enableUnifiedRendererWithWriter(&terminal) {
		t.Fatal("unified renderer did not attach")
	}
	interaction.waitUIActorIdle()
	awaitUnifiedPresenterIdle(t, interaction)
	return session
}

// TestChatScreenAgentsListSpecListsDurableAgents 锁定 /agents 副屏列表出口：
// 有子 agent 时返回 chatScreenList Spec（而非主屏内联单元格），行内容来自
// 与 /agents 快照同源的 durable registry。
func TestChatScreenAgentsListSpecListsDurableAgents(t *testing.T) {
	ctx := context.Background()
	registry, err := agentcontrol.NewRegistryService(ctx, agentcontrol.RegistryServiceConfig{
		StorePath: filepath.Join(t.TempDir(), "agent-control.sqlite"),
	})
	require.NoError(t, err)
	defer registry.Close()

	sessionStore := &countingSessionStorage{InMemoryStorage: runtimechat.NewInMemoryStorage()}
	root := runtimechat.NewSession("agents-screen-user")
	root.ID = "agents-screen-root"
	require.NoError(t, sessionStore.Save(ctx, root))
	_, err = registry.AgentStore.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:         "screen-worker",
		RootSessionID:   root.ID,
		ParentAgentID:   localRootAgentID(root.ID),
		ParentSessionID: root.ID,
		SessionID:       "screen-worker-session",
		AgentPath:       "/root/screen-worker",
		Depth:           1,
		AgentType:       "worker",
		Status:          agentcontrol.AgentStatusActive,
	})
	require.NoError(t, err)

	session := newUnifiedChatScreenTestSession(t)
	session.RuntimeSession = root
	session.SessionUserID = "agents-screen-user"
	session.LocalRuntimeHost = &localChatRuntimeHost{
		SessionStore:       sessionStore,
		SessionUser:        "agents-screen-user",
		AgentRegistryStore: registry.AgentStore,
	}
	session.LocalRuntimeHost.ActorRegistry = newLocalActorRegistry(session.LocalRuntimeHost)

	result := executeStructuredAgentsCommand(session, "/agents")
	require.NotNil(t, result.Screen, "/agents 必须走副屏 Spec 出口")
	require.Equal(t, chatScreenList, result.Screen.Kind)
	require.Equal(t, chatAgentScreenListID, result.Screen.ID)
	require.Len(t, result.Screen.Rows, 1)
	require.Equal(t, "/root/screen-worker", result.Screen.Rows[0].Title)
	require.NotEmpty(t, result.Screen.Subtitle)
	require.Equal(t, "查看输出", result.Screen.ConfirmLabel)
	require.NotNil(t, result.Screen.AfterClose)
	require.NoError(t, result.Screen.validate())
	require.Empty(t, result.Blocks, "Screen 结果不得同时提交主屏内联单元格")
	require.NotEmpty(t, result.Screen.DegradeDoc.Blocks, "降级文档必须保留 Agent Graph 文本")
}

// TestChatScreenAgentsListSpecPlaceholderKeepsScreenPath 锁定无子 agent 时
// 仍返回合法副屏列表（占位行），而不是回退主屏内联输出。
func TestChatScreenAgentsListSpecPlaceholderKeepsScreenPath(t *testing.T) {
	session := newUnifiedChatScreenTestSession(t)

	spec := chatScreenAgentsListSpec(session)
	require.Len(t, spec.Rows, 1, "空列表必须放占位行，Spec 才可开副屏")
	require.Contains(t, spec.Rows[0].Title, "暂无子 agent")
	require.NoError(t, spec.validate())

	result := executeStructuredAgentsCommand(session, "/agents")
	require.NotNil(t, result.Screen)
	require.Equal(t, chatScreenList, result.Screen.Kind)
	require.Empty(t, result.Blocks)
}

// TestDispatchChatAgentsOpensAlternateScreenList 锁定主分派器路径：
// /agents 的 Screen 效应必须被派发（能力门通过时由列表原语接管，
// 而不是停留在主屏内联）。
func TestDispatchChatAgentsOpensAlternateScreenList(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)
	session := newUnifiedChatScreenTestSession(t)

	var got ui.FullScreenListOptions
	chatScreenListRunner = func(_ *ChatSession, _ ui.ScreenLease, options ui.FullScreenListOptions) (ui.FullScreenListResult, error) {
		got = options
		return ui.FullScreenListResult{Cancelled: true}, nil
	}

	if dispatchChatCommand(session, "/agents", false) {
		t.Fatal("/agents 不应请求退出会话")
	}
	require.Equal(t, "Agent 列表", got.Title)
	require.Equal(t, "↑↓ 选择 agent，Enter 查看输出，Esc 返回", got.Subtitle)
	require.Equal(t, "查看输出", got.ConfirmLabel)
	require.Len(t, got.Items, 1, "无子 agent 时占位行仍应进入列表副屏")

	if active := chatScreenOpenActiveForTest(); active != 0 {
		t.Fatalf("framework active screens = %d，期望 0", active)
	}
	if session.Surface.LeaseActive() {
		t.Fatal("副屏关闭后租约必须已释放")
	}
	require.Empty(t, chatSessionSelectedAgentTarget(session),
		"占位行不得写入选中 agent target")
}

// TestChatAgentListAfterCloseIgnoresPlaceholder 锁定占位行确认是空操作：
// 不写入选中 target、不打开 transcript 副屏。
func TestChatAgentListAfterCloseIgnoresPlaceholder(t *testing.T) {
	handler := chatAgentListAfterCloseHandler(nil)
	session := &ChatSession{}
	handler(session, chatScreenOutcome{Result: chatScreenClosedConfirm, Index: 0})
	require.Empty(t, chatSessionSelectedAgentTarget(session))
}
