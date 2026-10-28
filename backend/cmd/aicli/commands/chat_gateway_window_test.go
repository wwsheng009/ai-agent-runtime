package commands

import (
	"testing"

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

func TestCloseGatewaySessionWindowRefusesSelfBeforeMeshCheck(t *testing.T) {
	// 目标就是本窗口时，先给出「别自杀」这条更具体的原因：它在任何环境下都成立，
	// 而 mesh 可用性只是这台机器此刻的编排能力。
	session := &ChatSession{RuntimeSession: &runtimechat.Session{ID: "session_self"}}
	_, err := closeGatewaySessionWindow(session, "SESSION_SELF")
	require.ErrorContains(t, err, "拒绝自杀")
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
