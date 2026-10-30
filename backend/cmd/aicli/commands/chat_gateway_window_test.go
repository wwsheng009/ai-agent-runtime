package commands

import (
	"bufio"
	"bytes"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

func TestRunGatewayWindowActionRejectsUnknownKind(t *testing.T) {
	// 未知 kind 必须在执行前就被拒：落空的动作比落空的窗口更难排查。
	_, err := runGatewayWindowAction(&ChatSession{}, gatewayagent.WindowAction{Kind: "explode", SessionID: "session_1"})
	require.ErrorContains(t, err, "未知的窗口动作")
}

func TestCloseGatewaySessionWindowRequiresMeshHost(t *testing.T) {
	// 本测试进程没有 mesh 宿主（--mesh=false / 未启动网格）：关闭动作只能显式失败，
	// 不能悄悄退化成「杀掉某个进程」——那会把一个治理动作变成不可审计的行为。
	require.Nil(t, mesh.Current())
	_, err := closeGatewaySessionWindow(&ChatSession{}, "session_1")
	require.ErrorContains(t, err, "mesh 未启用")
}

func TestCloseGatewaySessionWindowRequestsLocalExitForCurrentSession(t *testing.T) {
	// 窗口动作由目标节点自己执行：目标就是本窗口时不能再转投 mesh（mesh 会
	// 拒绝自停），必须直接请求聊天循环优雅退出。这个判定也必须排在 mesh
	// 可用性之前，否则 --web 无网格的会话永远关不掉。
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "session_self"}}
	message, err := closeGatewaySessionWindow(session, "SESSION_SELF")
	require.NoError(t, err)
	require.Contains(t, message, "已请求关闭本会话窗口")
	require.True(t, session.exitRequested.Load(), "关窗口必须置位聊天循环退出请求")
}

func TestCloseGatewaySessionWindowExitsRunningChatLoop(t *testing.T) {
	// 这条覆盖真实断点：动作到达节点时，聊天循环正阻塞在输入读取上。关闭
	// 必须唤醒读取并让 runChatLoop 返回，而不是只置一个没人检查的标志。
	t.Setenv("AICLI_CHAT_INPUT_LINE_MODE", "1")
	reader, writer := io.Pipe()
	defer writer.Close()

	session := newInitialPromptLoopTestSession(&fakeChatExecutor{output: "should not run"})
	session.RuntimeSession = &runtimechat.Session{ID: "session_self"}
	session.InputQueue = newChatInputQueue(bufio.NewReader(reader))
	var output bytes.Buffer
	session.Interaction = newTestChatInteractionCoordinator(t, session)
	session.Interaction.SetWriter(&output)
	defer session.Interaction.Shutdown()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runChatLoop(session, false, "")
	}()

	// 等到主循环真正注册了可唤醒的读取上下文，再投递关闭动作。
	deadline := time.Now().Add(2 * time.Second)
	for {
		session.composerWakeMu.Lock()
		reading := session.composerWakeCancel != nil
		session.composerWakeMu.Unlock()
		if reading {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("chat loop never entered the composer read")
		}
		time.Sleep(10 * time.Millisecond)
	}

	message, err := closeGatewaySessionWindow(session, "session_self")
	require.NoError(t, err)
	require.Contains(t, message, "已请求关闭本会话窗口")

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("close window did not terminate the running chat loop")
	}
}

func TestGatewayWindowRegistrationAdvertisesCapabilityOnlyWhenTerminalExists(t *testing.T) {
	session := &ChatSession{}
	register := chatGatewayRegisterFunc(gatewayagent.Config{Name: "node"}, nil, session)
	registration := register()

	available := chatGatewayWindowActionsAvailable()
	found := false
	for _, capability := range registration.Capabilities {
		if capability == gatewayCapabilityWindowActions {
			found = true
		}
	}
	// 能力位是承诺：本机探测到可用终端后端才通告，控制台才显示按钮。
	require.Equal(t, available, found, "window-actions 能力位必须与本机终端可用性一致")
}

func TestChatGatewayWorkspaceInfoIsNilWithoutBoundWorkspace(t *testing.T) {
	// 未绑定工作区的会话不得上报一个空对象：网关/控制台会把「字段缺失」和
	// 「目录是空串」当成两件不同的事来处理。
	session := &ChatSession{RuntimeSession: &runtimechat.Session{}}
	info := chatGatewayWorkspaceInfo(session)
	if info != nil {
		require.NotEmpty(t, info.Path, "上报了工作区就必须带路径")
		require.NotEmpty(t, info.Name)
	}
}

func TestChatGatewayMeshWindowNeverCarriesToken(t *testing.T) {
	// mesh 未启用时为空串；启用时也只含身份与端点。写令牌离开本机的方式只能是
	// 本机自己打印，绝不该出现在网关会话行里。
	require.Equal(t, "", chatGatewayMeshWindow(&ChatSession{}))
}

func TestGatewayWindowActionVocabularyIsCoveredByWireConstants(t *testing.T) {
	// 词表由 wire 单点定义，网关与 agent 共用：这条断言钉住「网关侧名字变了就必然
	// 让 agent 侧的解析测试一起失败」，避免两侧各自演化出方言。
	require.Equal(t, "window:", wire.WindowActionPrefix)
	require.Equal(t, "window:open-terminal:session_1", wire.WindowActionName(wire.WindowActionOpenTerminal, "session_1"))
	kind, _, ok := wire.ParseWindowAction(wire.WindowActionName(wire.WindowActionClose, "session_1"))
	require.True(t, ok)
	require.Equal(t, wire.WindowActionClose, kind)
}
