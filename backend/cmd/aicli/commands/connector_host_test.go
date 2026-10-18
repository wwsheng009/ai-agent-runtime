// aicli 连接器宿主接线的单测：路由挂载/503 语义、session id 派生、
// Backend 未就绪时的失败语义。真正的轮询/去重 e2e 在
// internal/connectors/e2e_restart_test.go（假 Bot API + storefile）。
package commands

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/restapi"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/storefile"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// ── 测试替身 ───────────────────────────────────────────────────────────────

type stubConnectorBackend struct{}

func (stubConnectorBackend) Name() string { return "stub" }

func (stubConnectorBackend) Handle(context.Context, connectors.BackendRequest, connectors.ReplySink) (connectors.BackendResult, error) {
	return connectors.BackendResult{}, nil
}

func newTestConnectorService(t *testing.T) (*connectors.Service, *storefile.ConfigStore) {
	t.Helper()
	dir := t.TempDir()
	store := storefile.NewConfigStore(filepath.Join(dir, "config.yaml"), filepath.Join(dir, "data", "connectors", "state.json"))
	service := connectors.New(connectors.Deps{Store: store, Backend: stubConnectorBackend{}, Logf: func(string, ...any) {}})
	return service, store
}

// ── 用例 ───────────────────────────────────────────────────────────────────

func TestLocalConnectorSessionIDDerivesStableID(t *testing.T) {
	require.Equal(t, "conn-tg-main-200", localConnectorSessionID("tg-main", "200"))
	require.Equal(t, "conn-tg-main-a-b", localConnectorSessionID("tg main", "a/b"))
	require.Equal(t, "conn-default", localConnectorSessionID("", ""))
	require.Equal(t, "conn-default", localConnectorSessionID("-", "-"))

	long := localConnectorSessionID("tg", strings.Repeat("9", 200))
	require.LessOrEqual(t, len(long), 80)
	require.True(t, strings.HasPrefix(long, "conn-tg-"))
}

func TestMountLocalConnectorRoutesWithoutServiceReturns503(t *testing.T) {
	setLocalConnectorHandlers(nil, nil)
	mux := http.NewServeMux()
	MountLocalConnectorRoutes(mux)

	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/connectors", nil),
		httptest.NewRequest(http.MethodPost, "/api/connectors", strings.NewReader("{}")),
		httptest.NewRequest(http.MethodPost, "/hooks/connectors/tg-main", strings.NewReader("{}")),
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code, request.URL.Path)
		require.Contains(t, recorder.Body.String(), "连接器未在本会话装配")
	}
}

func TestMountLocalConnectorRoutesForwardsToService(t *testing.T) {
	service, store := newTestConnectorService(t)
	deps := restapi.Deps{
		Service:   service,
		Store:     store,
		Authorize: func(*http.Request) error { return nil },
		Logf:      func(string, ...any) {},
	}
	setLocalConnectorHandlers(restapi.NewRouter(deps), restapi.NewHooksRouter(deps))
	t.Cleanup(func() { setLocalConnectorHandlers(nil, nil) })

	mux := http.NewServeMux()
	MountLocalConnectorRoutes(mux)

	// 精确路径 /api/connectors（不带尾斜杠）必须直接命中，而不是 301。
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/connectors", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"items"`)

	// schema 路由同样可达（前缀剥离正确）。
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/connectors/schema", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"providers"`)

	// hooks 入口可达：未知连接器 404 由 handlers 返回，而不是 503/404 兜底。
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/hooks/connectors/unknown", strings.NewReader("{}")))
	require.NotEqual(t, http.StatusServiceUnavailable, recorder.Code)
}

func TestLocalConnectorBackendWithoutHostFailsSoftly(t *testing.T) {
	backend := localConnectorBackend{}
	_, err := backend.Handle(context.Background(), connectors.BackendRequest{Text: "你好"}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "本机 chat 运行时未就绪")
}

// TestLocalConnectorBackendSessionRefCarriesHostNodeID 锁定 2026-10-17 真机
// qq-bot1 双 actor 复盘修复：Handle 返回的 SessionRef 必须携带本宿主实例
// 身份（HostID 全形）——空 NodeID 会把绑定归属抹空，下一条消息落进假 R2a
// 接管（反复回写 + 「原节点 (空) 掉线」提示）。
func TestLocalConnectorBackendSessionRefCarriesHostNodeID(t *testing.T) {
	backend := localConnectorBackend{hostID: "aicli:node-unit"}
	ref := backend.sessionRef("conn-x")
	require.Equal(t, "aicli:node-unit", ref.NodeID)
	require.Equal(t, "conn-x", ref.SessionID)

	// 未显式注入（测试直连构造/旧路径）时回退进程级身份，不为空。
	fallback := localConnectorBackend{}
	require.NotEmpty(t, fallback.sessionRef("s").NodeID)
	require.True(t, strings.HasPrefix(fallback.sessionRef("s").NodeID, "aicli:"))
}

func TestLocalConnectorHostCloseIsIdempotent(t *testing.T) {
	service, _ := newTestConnectorService(t)
	host := &LocalConnectorHost{service: service}
	host.Close()
	host.Close()
}

// TestStartLocalConnectorsAllowsEmptyWhitelist 覆盖 2026-10-15 语义调整：
// StartLocalConnectors 不再对空白名单 fail-closed（RequireAllowedUsers=false，
// 与 runtime-server/gateway 同口径）——allowed_users 为空 = 不限制用户，
// 启用中的连接器照常 Activate/轮询；同一份配置里白名单非空的连接器同样
// Activate，既证明配置被正确解析，也证明白名单仍是可选收窄项而非启动前提。
// 需要 fail-closed 的部署显式把 connectors.Deps.RequireAllowedUsers 置回 true
// （闸本体测试见 internal/connectors/service_test.go）。
func TestStartLocalConnectorsAllowsEmptyWhitelist(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"username":"gate-test"}}`))
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := "connectors:\n" +
		"  - id: tg-open-empty-wl\n" +
		"    provider: telegram\n" +
		"    enabled: true\n" +
		"    mode: polling\n" +
		"    settings:\n" +
		"      api_base: " + server.URL + "\n" +
		"    secrets:\n" +
		"      bot_token: \"111:gate\"\n" +
		"  - id: tg-narrowed\n" +
		"    provider: telegram\n" +
		"    enabled: true\n" +
		"    mode: webhook\n" +
		"    settings:\n" +
		"      api_base: " + server.URL + "\n" +
		"    secrets:\n" +
		"      bot_token: \"222:open\"\n" +
		"    policy:\n" +
		"      allowed_users: [\"42\"]\n"
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	var logs []string
	host, err := StartLocalConnectors(context.Background(), &localChatRuntimeHost{}, configPath, func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	})
	require.NoError(t, err)
	require.NotNil(t, host)
	t.Cleanup(host.Close)

	// 两个连接器（空白名单 + 非空白名单）都应完成 Activate。
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		joined := strings.Join(paths, "\n")
		return strings.Contains(joined, "111:gate") && strings.Contains(joined, "222:open")
	}, 3*time.Second, 10*time.Millisecond, "空白名单与非空白名单连接器都应照常 Activate")
	require.Contains(t, strings.Join(logs, "\n"), "aicli connectors: started")
}

// TestStartLocalConnectorsHonorsRuntimeOwnerGate 覆盖归属闸接线（07 §5.6）：
// aicli 宿主身份为 "aicli"——policy.runtime_owner 指定 runtime-server 的连接器
// 不得 Activate/轮询（只读配置），指定 aicli 的照常启动。
func TestStartLocalConnectorsHonorsRuntimeOwnerGate(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"username":"owner-test"}}`))
	}))
	t.Cleanup(server.Close)

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := "connectors:\n" +
		"  - id: tg-owned\n" +
		"    provider: telegram\n" +
		"    enabled: true\n" +
		"    mode: polling\n" +
		"    settings:\n" +
		"      api_base: " + server.URL + "\n" +
		"    secrets:\n" +
		"      bot_token: \"333:owned\"\n" +
		"    policy:\n" +
		"      allowed_users: [\"42\"]\n" +
		"      runtime_owner: runtime-server\n" +
		"  - id: tg-aicli\n" +
		"    provider: telegram\n" +
		"    enabled: true\n" +
		"    mode: polling\n" +
		"    settings:\n" +
		"      api_base: " + server.URL + "\n" +
		"    secrets:\n" +
		"      bot_token: \"444:aicli\"\n" +
		"    policy:\n" +
		"      allowed_users: [\"42\"]\n" +
		"      runtime_owner: aicli\n"
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	var logsMu sync.Mutex
	var logs []string
	host, err := StartLocalConnectors(context.Background(), &localChatRuntimeHost{}, configPath, func(format string, args ...any) {
		logsMu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		logsMu.Unlock()
	})
	require.NoError(t, err)
	require.NotNil(t, host)
	t.Cleanup(host.Close)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, path := range paths {
			if strings.Contains(path, "444:aicli") {
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond, "runtime_owner=aicli 的连接器应照常 Activate")

	time.Sleep(100 * time.Millisecond) // 留出闸失效时误启动的窗口
	mu.Lock()
	joined := strings.Join(paths, "\n")
	mu.Unlock()
	require.NotContains(t, joined, "333:owned", "runtime_owner=runtime-server 的连接器不得由 aicli Activate/轮询")
	logsMu.Lock()
	joinedLogs := strings.Join(logs, "\n")
	logsMu.Unlock()
	require.Contains(t, joinedLogs, "未运行（由 runtime-server 持有")
}

// ── 审批/提问命令闭环（FR-7）────────────────────────────────────────────────

type stubLocalAnswerCall struct {
	sessionID  string
	questionID string
	answer     string
}

// stubLocalConnectorCommands 是 localConnectorCommandPlane 的测试替身：
// 记录转发参数，不发真实 actor 调用（真入口由 registry 的既有用例覆盖）。
type stubLocalConnectorCommands struct {
	approvalArgs   []toolbroker.ResolveAgentApprovalArgs
	approvalResult *toolbroker.AgentApprovalResult
	approvalErr    error
	answerCalls    []stubLocalAnswerCall
	answerErr      error
}

func (s *stubLocalConnectorCommands) ResolveApproval(_ context.Context, args toolbroker.ResolveAgentApprovalArgs) (*toolbroker.AgentApprovalResult, error) {
	s.approvalArgs = append(s.approvalArgs, args)
	if s.approvalErr != nil {
		return nil, s.approvalErr
	}
	if s.approvalResult != nil {
		return s.approvalResult, nil
	}
	return &toolbroker.AgentApprovalResult{SessionID: args.ID, RequestID: args.RequestID, Allowed: args.Allow}, nil
}

func (s *stubLocalConnectorCommands) AnswerQuestion(_ context.Context, sessionID, questionID, answer string) error {
	s.answerCalls = append(s.answerCalls, stubLocalAnswerCall{sessionID: sessionID, questionID: questionID, answer: answer})
	return s.answerErr
}

func newConnectorCommandTestHost(t *testing.T, approvalID, questionID string) *localChatRuntimeHost {
	t.Helper()
	store := runtimechat.NewInMemoryRuntimeStore(64)
	state := &runtimechat.RuntimeState{SessionID: "sess-1"}
	if approvalID != "" {
		state.PendingApproval = &runtimechat.ApprovalRequest{ID: approvalID, ToolName: "shell_exec"}
	}
	if questionID != "" {
		state.PendingQuestion = &runtimechat.UserQuestionRequest{ID: questionID, Prompt: "which env?"}
	}
	require.NoError(t, store.SaveState(context.Background(), state))
	return &localChatRuntimeHost{RuntimeStore: store}
}

func TestLocalConnectorDecideApprovalForwardsToResolveApproval(t *testing.T) {
	host := newConnectorCommandTestHost(t, "apr-1", "")
	stub := &stubLocalConnectorCommands{approvalResult: &toolbroker.AgentApprovalResult{
		SessionID: "sess-1", RequestID: "apr-1", Allowed: true, Resumed: true,
	}}
	backend := localConnectorBackend{host: host, commands: stub, logf: func(string, ...any) {}}

	// 无参 = 当前挂起项。
	result, err := backend.DecideApproval(context.Background(), connectors.CommandRequest{
		Command: "approve",
		Session: connectors.SessionRef{SessionID: "sess-1"},
	})
	require.NoError(t, err)
	require.True(t, result.Handled)
	require.Len(t, result.Reply, 1)
	require.Contains(t, result.Reply[0].Text, "已批准：apr-1")
	require.Contains(t, result.Reply[0].Text, "已恢复运行")
	require.Equal(t, []toolbroker.ResolveAgentApprovalArgs{{
		ID: "sess-1", RequestID: "apr-1", Allow: true,
	}}, stub.approvalArgs)

	// 带 request_id 且与当前挂起项一致：同样命中。
	result, err = backend.DecideApproval(context.Background(), connectors.CommandRequest{
		Command: "approve",
		Session: connectors.SessionRef{SessionID: "sess-1"},
		Args:    []string{"apr-1"},
	})
	require.NoError(t, err)
	require.True(t, result.Handled)
	require.Len(t, stub.approvalArgs, 2)
}

func TestLocalConnectorDecideApprovalDenyAndNoPending(t *testing.T) {
	host := newConnectorCommandTestHost(t, "apr-1", "")
	stub := &stubLocalConnectorCommands{}
	backend := localConnectorBackend{host: host, commands: stub, logf: func(string, ...any) {}}

	result, err := backend.DecideApproval(context.Background(), connectors.CommandRequest{
		Command: "deny",
		Session: connectors.SessionRef{SessionID: "sess-1"},
	})
	require.NoError(t, err)
	require.True(t, result.Handled)
	require.Contains(t, result.Reply[0].Text, "已拒绝：apr-1")
	require.Len(t, stub.approvalArgs, 1)
	require.False(t, stub.approvalArgs[0].Allow)

	// 无挂起项：Handled=false + 文案，不得调用 ResolveApproval。
	emptyHost := newConnectorCommandTestHost(t, "", "")
	emptyBackend := localConnectorBackend{host: emptyHost, commands: stub, logf: func(string, ...any) {}}
	result, err = emptyBackend.DecideApproval(context.Background(), connectors.CommandRequest{
		Command: "approve",
		Session: connectors.SessionRef{SessionID: "sess-1"},
	})
	require.NoError(t, err)
	require.False(t, result.Handled)
	require.Contains(t, result.Reply[0].Text, "没有待审批项")
	require.Len(t, stub.approvalArgs, 1, "无挂起项不得转发")
}

func TestLocalConnectorDecideApprovalMismatchedRequestID(t *testing.T) {
	host := newConnectorCommandTestHost(t, "apr-1", "")
	stub := &stubLocalConnectorCommands{}
	backend := localConnectorBackend{host: host, commands: stub, logf: func(string, ...any) {}}

	result, err := backend.DecideApproval(context.Background(), connectors.CommandRequest{
		Command: "approve",
		Session: connectors.SessionRef{SessionID: "sess-1"},
		Args:    []string{"apr-2"},
	})
	require.NoError(t, err)
	require.False(t, result.Handled)
	require.Contains(t, result.Reply[0].Text, "不匹配")
	require.Empty(t, stub.approvalArgs)
}

func TestLocalConnectorAnswerQuestionSequenceAndText(t *testing.T) {
	host := newConnectorCommandTestHost(t, "", "q-1")
	stub := &stubLocalConnectorCommands{}
	backend := localConnectorBackend{host: host, commands: stub, logf: func(string, ...any) {}}

	result, err := backend.AnswerQuestion(context.Background(), connectors.CommandRequest{
		Command: "answer",
		Session: connectors.SessionRef{SessionID: "sess-1"},
		Args:    []string{"1", "hello", "world"},
		Text:    "1 hello world",
	})
	require.NoError(t, err)
	require.True(t, result.Handled)
	require.Contains(t, result.Reply[0].Text, "已提交回答：q-1")
	require.Equal(t, []stubLocalAnswerCall{{sessionID: "sess-1", questionID: "q-1", answer: "hello world"}}, stub.answerCalls)

	// question_id 直指 + 内部空白保留。
	result, err = backend.AnswerQuestion(context.Background(), connectors.CommandRequest{
		Command: "answer",
		Session: connectors.SessionRef{SessionID: "sess-1"},
		Args:    []string{"q-1", "多", "空格"},
		Text:    "q-1 多  空格",
	})
	require.NoError(t, err)
	require.True(t, result.Handled)
	require.Len(t, stub.answerCalls, 2)
	require.Equal(t, "多  空格", stub.answerCalls[1].answer)

	// 无挂起提问：Handled=false，不转发。
	emptyHost := newConnectorCommandTestHost(t, "", "")
	emptyBackend := localConnectorBackend{host: emptyHost, commands: stub, logf: func(string, ...any) {}}
	result, err = emptyBackend.AnswerQuestion(context.Background(), connectors.CommandRequest{
		Command: "answer",
		Session: connectors.SessionRef{SessionID: "sess-1"},
		Args:    []string{"1", "x"},
		Text:    "1 x",
	})
	require.NoError(t, err)
	require.False(t, result.Handled)
	require.Contains(t, result.Reply[0].Text, "没有待回答的问题")
	require.Len(t, stub.answerCalls, 2)
}

func TestLocalConnectorCommandBackendWithoutRuntimeFailsSoftly(t *testing.T) {
	backend := localConnectorBackend{}
	_, err := backend.DecideApproval(context.Background(), connectors.CommandRequest{Command: "approve"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "本机 chat 运行时未就绪")

	_, err = backend.AnswerQuestion(context.Background(), connectors.CommandRequest{Command: "answer"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "本机 chat 运行时未就绪")
}
