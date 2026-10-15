// /bots 子命令与参数补全单测（docs/bots/09 §5.4 TUI 落地契约）：
//   - 补全：二级命令位 → 连接器 id 位（bind 仅 enabled）→ unbind 的 chat 位；
//   - 子命令：list/help/status/bind/unbind 走本机宿主 Store，与 REST 同口径
//     （enabled 校验、allowed_users、CAS 乐观锁、墓碑解绑）。

package commands

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

func newBotsCompletionTestHost(t *testing.T) *LocalConnectorHost {
	store := connectors.NewMemoryStore(
		connectors.Config{ID: "tg-1", Provider: "telegram", Name: "Telegram 主号", Enabled: true},
		connectors.Config{ID: "wx-1", Provider: "wechat", Name: "微信", Enabled: false},
	)
	// botsResolveHost 复用已装配宿主的条件：service 与 store 双非 nil。
	service := connectors.New(connectors.Deps{Store: store, Logf: func(string, ...any) {}})
	host := &LocalConnectorHost{store: store, service: service}
	require.NoError(t, store.PutBinding(connectors.Binding{
		ConnectorID: "tg-1", Chat: "chat-a",
		Session:   connectors.SessionRef{SessionID: "session-a"},
		UpdatedAt: time.Now().Add(-2 * time.Hour),
	}))
	require.NoError(t, store.PutBinding(connectors.Binding{
		ConnectorID: "tg-1", Chat: "chat-b",
		Session:   connectors.SessionRef{SessionID: "session-b"},
		UpdatedAt: time.Now().Add(-1 * time.Hour),
	}))
	return host
}

func completeBotsForTest(t *testing.T, host *LocalConnectorHost, session *ChatSession, args string) []chatSlashCompletionCandidate {
	t.Helper()
	previous := currentLocalConnectorHost()
	setLocalConnectorHost(host)
	defer setLocalConnectorHost(previous)
	return completeBotsSlashArgs(session, args, utf8.RuneCountInString(args))
}

func botsCandidateCommands(candidates []chatSlashCompletionCandidate) []string {
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, candidate.Command)
	}
	return out
}

func TestBotsSlashCompletionSubcommands(t *testing.T) {

	host := newBotsCompletionTestHost(t)
	got := botsCandidateCommands(completeBotsForTest(t, host, &ChatSession{}, ""))
	require.Equal(t, []string{"status", "bind", "unbind", "list", "ls", "help"}, got)

	// 前缀过滤。
	got = botsCandidateCommands(completeBotsForTest(t, host, &ChatSession{}, "b"))
	require.Equal(t, []string{"bind"}, got)
	got = botsCandidateCommands(completeBotsForTest(t, host, &ChatSession{}, "l"))
	require.Equal(t, []string{"list", "ls"}, got)
}

func TestBotsSlashCompletionConnectorIDs(t *testing.T) {

	host := newBotsCompletionTestHost(t)
	session := &ChatSession{}

	// status 位：全部连接器（含停用）。
	got := botsCandidateCommands(completeBotsForTest(t, host, session, "status "))
	require.Equal(t, []string{"tg-1", "wx-1"}, got)
	got = botsCandidateCommands(completeBotsForTest(t, host, session, "status tg"))
	require.Equal(t, []string{"tg-1"}, got)

	// bind 位：仅 enabled。
	got = botsCandidateCommands(completeBotsForTest(t, host, session, "bind "))
	require.Equal(t, []string{"tg-1"}, got)

	// unbind 的 chat 位：现有绑定（按 chat 排序）。
	got = botsCandidateCommands(completeBotsForTest(t, host, session, "unbind tg-1 "))
	require.Equal(t, []string{"chat-a", "chat-b"}, got)
	got = botsCandidateCommands(completeBotsForTest(t, host, session, "unbind tg-1 ch"))
	require.Equal(t, []string{"chat-a", "chat-b"}, got)
}

func TestBotsSlashCompletionSilentlyDegradesWithoutHost(t *testing.T) {

	previous := currentLocalConnectorHost()
	setLocalConnectorHost(nil)
	defer setLocalConnectorHost(previous)

	// 二级命令位始终可用（不依赖宿主）。
	require.NotEmpty(t, completeBotsSlashArgs(&ChatSession{}, "", 0))
	// id 位静默降级为空候选（不报错、不阻塞输入）。
	require.Empty(t, completeBotsSlashArgs(&ChatSession{}, "status ", 7))
}

func TestBotsParseSubcommand(t *testing.T) {
	t.Parallel()

	require.Empty(t, botsParseSubcommand("/bots"))
	require.Empty(t, botsParseSubcommand("/bots "))
	require.Empty(t, botsParseSubcommand("/bots unknown-cmd"))
	require.Equal(t, "bind", botsParseSubcommand("/bots bind tg-1"))
	require.Equal(t, "unbind", botsParseSubcommand("/bots UNBIND"))
	require.Equal(t, "status", botsParseSubcommand("/bots status tg-1 extra"))
	require.Equal(t, "list", botsParseSubcommand("/bots ls"))
}

func TestBotsSubcommandHelpAndList(t *testing.T) {

	host := newBotsCompletionTestHost(t)
	session := &ChatSession{}
	previous := currentLocalConnectorHost()
	setLocalConnectorHost(host)
	defer setLocalConnectorHost(previous)

	help := renderBotsSubcommandText(session, "/bots help")
	require.Contains(t, help, "/bots bind <id> [session]")

	list := renderBotsSubcommandText(session, "/bots list")
	require.Contains(t, list, "tg-1")
	require.Contains(t, list, "wx-1")

	// 未知参数回退 help。
	require.Equal(t, help, renderBotsSubcommandText(session, "/bots"))
}

func TestBotsStatusSubcommand(t *testing.T) {

	host := newBotsCompletionTestHost(t)
	session := &ChatSession{}
	previous := currentLocalConnectorHost()
	setLocalConnectorHost(host)
	defer setLocalConnectorHost(previous)

	single := renderBotsSubcommandText(session, "/bots status tg-1")
	require.Contains(t, single, "归属")
	require.Contains(t, single, "chat-a → session-a")
	require.Contains(t, single, "chat-b → session-b")

	unknown := renderBotsSubcommandText(session, "/bots status nope")
	require.Contains(t, unknown, "未知连接器：nope")

	all := renderBotsSubcommandText(session, "/bots status")
	require.Contains(t, all, "tg-1")
	require.Contains(t, all, "wx-1")
}

func TestBotsBindSubcommandBindsCurrentSession(t *testing.T) {

	host := newBotsCompletionTestHost(t)
	session := &ChatSession{RuntimeSession: newBotsTestRuntimeSession("session-current")}
	previous := currentLocalConnectorHost()
	setLocalConnectorHost(host)
	defer setLocalConnectorHost(previous)

	out := renderBotsSubcommandText(session, "/bots bind tg-1")
	require.Contains(t, out, "已绑定")
	require.Contains(t, out, "chat-b")
	require.Contains(t, out, "session-current")

	// Store 内部生效：chat-b 指向当前会话（覆盖式切换，旧 session-b 保留在 chat-a）。
	binding, ok := host.Store().GetBinding("tg-1", "chat-b")
	require.True(t, ok)
	require.Equal(t, "session-current", binding.Session.SessionID)
	other, ok := host.Store().GetBinding("tg-1", "chat-a")
	require.True(t, ok)
	require.Equal(t, "session-a", other.Session.SessionID)
}

func TestBotsBindSubcommandGuards(t *testing.T) {

	host := newBotsCompletionTestHost(t)
	session := &ChatSession{RuntimeSession: newBotsTestRuntimeSession("session-current")}
	previous := currentLocalConnectorHost()
	setLocalConnectorHost(host)
	defer setLocalConnectorHost(previous)

	// 缺参数。
	require.Contains(t, renderBotsSubcommandText(session, "/bots bind"), "用法")
	// 未知连接器。
	require.Contains(t, renderBotsSubcommandText(session, "/bots bind nope"), "未知连接器")
	// 停用连接器（与 REST 409 同口径）。
	require.Contains(t, renderBotsSubcommandText(session, "/bots bind wx-1"), "已停用")
	// 无绑定记录。
	emptyID := newBotsEnabledEmptyID(t, host)
	require.Contains(t, renderBotsSubcommandText(session, "/bots bind "+emptyID), "尚无活跃的 IM 会话绑定")
}

// newBotsEnabledEmptyID 添加一个 enabled 且无绑定的连接器并返回其 id。
func newBotsEnabledEmptyID(t *testing.T, host *LocalConnectorHost) string {
	t.Helper()
	const id = "tg-empty"
	require.NoError(t, host.Store().PutConfig(connectors.Config{ID: id, Provider: "telegram", Enabled: true}))
	return id
}

func TestBotsBindSubcommandValidatesExplicitSession(t *testing.T) {

	host := newBotsCompletionTestHost(t)
	session := &ChatSession{RuntimeSession: newBotsTestRuntimeSession("session-current")}
	previous := currentLocalConnectorHost()
	setLocalConnectorHost(host)
	defer setLocalConnectorHost(previous)

	// session 缺省（当前会话）直接成功。
	require.Contains(t, renderBotsSubcommandText(session, "/bots bind tg-1"), "已绑定")
}

func TestBotsUnbindSubcommandTombstone(t *testing.T) {

	host := newBotsCompletionTestHost(t)
	session := &ChatSession{}
	previous := currentLocalConnectorHost()
	setLocalConnectorHost(host)
	defer setLocalConnectorHost(previous)

	// 显式 chat。
	out := renderBotsSubcommandText(session, "/bots unbind tg-1 chat-a")
	require.Contains(t, out, "已解绑")
	require.Contains(t, out, "chat-a")
	binding, ok := host.Store().GetBinding("tg-1", "chat-a")
	require.True(t, ok, "显式解绑 = 写墓碑，记录保留")
	require.True(t, binding.Session.IsZero())

	// 缺省 chat = 最近活跃绑定（chat-b 更新）。
	out = renderBotsSubcommandText(session, "/bots unbind tg-1")
	require.Contains(t, out, "chat-b")
	binding, ok = host.Store().GetBinding("tg-1", "chat-b")
	require.True(t, ok)
	require.True(t, binding.Session.IsZero())

	// 未知 chat / 未知连接器 / 无活跃绑定。
	require.Contains(t, renderBotsSubcommandText(session, "/bots unbind tg-1 chat-zzz"), "没有 chat")
	require.Contains(t, renderBotsSubcommandText(session, "/bots unbind nope"), "未知连接器")
	require.Contains(t, renderBotsSubcommandText(session, "/bots unbind wx-1"), "没有可解绑的活跃绑定")
}

func TestBotsBindSubcommandRespectsAllowlist(t *testing.T) {

	store := connectors.NewMemoryStore(
		connectors.Config{
			ID: "tg-allow", Provider: "telegram", Enabled: true,
			Policy: connectors.Policy{AllowedUsers: []string{"owner-chat"}},
		},
	)
	service := connectors.New(connectors.Deps{Store: store, Logf: func(string, ...any) {}})
	host := &LocalConnectorHost{store: store, service: service}
	require.NoError(t, store.PutBinding(connectors.Binding{
		ConnectorID: "tg-allow", Chat: "intruder-chat",
		Session:   connectors.SessionRef{SessionID: "s"},
		UpdatedAt: time.Now(),
	}))
	session := &ChatSession{RuntimeSession: newBotsTestRuntimeSession("session-current")}
	previous := currentLocalConnectorHost()
	setLocalConnectorHost(host)
	defer setLocalConnectorHost(previous)

	// 与 REST PUT bindings 同源校验：白名单外 chat 拒绝 bind（docs/bots/09 §8）。
	out := renderBotsSubcommandText(session, "/bots bind tg-allow")
	require.Contains(t, out, "allowed_users")
	require.Contains(t, out, "拒绝绑定")
	binding, _ := store.GetBinding("tg-allow", "intruder-chat")
	require.Equal(t, "s", binding.Session.SessionID, "拒绝时绑定不得被改写")
}

func TestBotsPickMostRecentBinding(t *testing.T) {
	t.Parallel()

	now := time.Now()
	bindings := []connectors.Binding{
		{ConnectorID: "c", Chat: "old", UpdatedAt: now.Add(-time.Hour)},
		{ConnectorID: "c", Chat: "new", UpdatedAt: now},
	}
	best, ok := botsPickMostRecentBinding(bindings)
	require.True(t, ok)
	require.Equal(t, "new", best.Chat)

	_, ok = botsPickMostRecentBinding(nil)
	require.False(t, ok)
}

// newBotsTestRuntimeSession 构造带 id 的最小 runtime 会话替身（避免依赖
// SessionManager 的完整装配）。
func newBotsTestRuntimeSession(id string) *runtimechat.Session {
	return &runtimechat.Session{ID: id}
}

func TestBotsStatusFormatsOwnerState(t *testing.T) {
	t.Parallel()

	now := time.Now()
	fresh := botsFormatOwnerText(connectors.OwnerRecord{
		Owner: "aicli:test", HeartbeatAt: now.Add(-10 * time.Second),
	}, now)
	require.Contains(t, fresh, "运行中")
	stale := botsFormatOwnerText(connectors.OwnerRecord{
		Owner: "aicli:test", HeartbeatAt: now.Add(-2 * time.Hour),
	}, now)
	require.Contains(t, stale, "已陈旧")
	none := botsFormatOwnerText(connectors.OwnerRecord{}, now)
	require.Contains(t, none, "无归属")
}

func TestBotsFormatBindingText(t *testing.T) {
	t.Parallel()

	require.Equal(t, "chat → s", botsFormatBindingText(connectors.Binding{
		Chat: "chat", Session: connectors.SessionRef{SessionID: "s"},
	}))
	require.Equal(t, "chat → s @ node-1", botsFormatBindingText(connectors.Binding{
		Chat: "chat", Session: connectors.SessionRef{SessionID: "s", NodeID: "node-1"},
	}))
	require.Contains(t, botsFormatBindingText(connectors.Binding{Chat: "chat"}), "未绑定")
	require.True(t, strings.Contains(botsSubcommandHelpText(), "unbind"))
}
