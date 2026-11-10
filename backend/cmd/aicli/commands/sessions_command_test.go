package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// sessionsCommandTestUser 是列表测试的固定会话身份：fixture 与命令解析走同一个
// ResolveSessionUserID 通路，显式 ID 逐字返回，因此两边必然一致。
const sessionsCommandTestUser = "sessions-command-test-user"

// seedSessionsCommandStore 在会话库里创建带工作目录绑定的会话，返回创建顺序的 ID 列表。
func seedSessionsCommandStore(t *testing.T, dir string, workspaces ...string) []string {
	t.Helper()
	manager, userID, _, err := newChatSessionManagerWithRuntimeConfig(dir, nil, "", sessionsCommandTestUser)
	require.NoError(t, err)
	defer manager.Stop()

	ctx := context.Background()
	ids := make([]string, 0, len(workspaces))
	for index, workspace := range workspaces {
		session, err := manager.Create(ctx, userID)
		require.NoError(t, err)
		session.SetContext(sessionmeta.WorkspacePath, workspace)
		session.UpdateTitle(fmt.Sprintf("会话 %d", index+1))
		session.AddMessage(*runtimetypes.NewUserMessage("hello from " + workspace))
		require.NoError(t, manager.Update(ctx, session))
		ids = append(ids, session.ID)
	}
	return ids
}

// pinSessionsCommandWorkspace 固定「当前工作目录」解析，避免测试依赖进程 cwd 与
// 用户级 runtime.yaml 的 workspace.root。
func pinSessionsCommandWorkspace(t *testing.T, workspace string) {
	t.Helper()
	restore := sessionsCurrentWorkspace
	sessionsCurrentWorkspace = func(*config.Config) (string, error) { return workspace, nil }
	t.Cleanup(func() { sessionsCurrentWorkspace = restore })
}

func captureSessionsCommandStdout(t *testing.T, run func()) string {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	require.NoError(t, err)

	os.Stdout = writer
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(reader)
		done <- string(data)
	}()

	func() {
		defer func() { os.Stdout = original }()
		run()
		require.NoError(t, writer.Close())
	}()

	out := <-done
	require.NoError(t, reader.Close())
	return out
}

func TestSessionsCommandDefaultsToCurrentWorkspace(t *testing.T) {
	t.Setenv("AICLI_HOME", t.TempDir())
	sessionDir := t.TempDir()
	currentWorkspace := t.TempDir()
	otherWorkspace := t.TempDir()
	ids := seedSessionsCommandStore(t, sessionDir, otherWorkspace, currentWorkspace)
	pinSessionsCommandWorkspace(t, currentWorkspace)

	result, sessions, err := collectSessionsCommandResult(&config.Config{}, sessionsCommandRequest{
		SessionDir: sessionDir,
		User:       sessionsCommandTestUser,
		Limit:      sessionsDefaultLimit,
	})
	require.NoError(t, err)
	require.Equal(t, "workspace", result.Scope)
	require.Equal(t, currentWorkspace, result.Workspace)
	require.Equal(t, 1, result.Count)
	require.Len(t, sessions, 1)
	require.Equal(t, ids[1], result.Sessions[0].SessionID)
	require.Equal(t, currentWorkspace, result.Sessions[0].Workspace)
	require.Equal(t, string(runtimechat.StateActive), result.Sessions[0].State)
}

func TestSessionsCommandAllSpansEveryWorkspace(t *testing.T) {
	t.Setenv("AICLI_HOME", t.TempDir())
	sessionDir := t.TempDir()
	firstWorkspace := t.TempDir()
	secondWorkspace := t.TempDir()
	ids := seedSessionsCommandStore(t, sessionDir, firstWorkspace, secondWorkspace)
	pinSessionsCommandWorkspace(t, firstWorkspace)

	result, sessions, err := collectSessionsCommandResult(&config.Config{}, sessionsCommandRequest{
		All:        true,
		SessionDir: sessionDir,
		User:       sessionsCommandTestUser,
		Limit:      sessionsDefaultLimit,
	})
	require.NoError(t, err)
	require.Equal(t, "all", result.Scope)
	require.Empty(t, result.Workspace, "--all 不带当前工作目录限定")
	require.Equal(t, 2, result.Count)
	require.Len(t, sessions, 2)

	listed := map[string]bool{}
	for _, item := range result.Sessions {
		listed[item.SessionID] = true
	}
	for _, id := range ids {
		require.True(t, listed[id], "会话 %s 必须在 --all 列表里", id)
	}
}

func TestSessionsCommandUsesActivityTimeNotRowMaintenanceTime(t *testing.T) {
	t.Setenv("AICLI_HOME", t.TempDir())
	sessionDir := t.TempDir()
	workspace := t.TempDir()

	manager, userID, _, err := newChatSessionManagerWithRuntimeConfig(
		sessionDir, nil, "", sessionsCommandTestUser)
	require.NoError(t, err)
	ctx := context.Background()
	session, err := manager.Create(ctx, userID)
	require.NoError(t, err)
	session.SetContext(sessionmeta.WorkspacePath, workspace)
	session.UpdateTitle("activity time")
	session.AddMessage(*runtimetypes.NewUserMessage("hello"))
	require.NoError(t, manager.Update(ctx, session))

	// Simulate an old maintenance bug: the row timestamp is far newer than the
	// last canonical message. The user-visible list must keep the message time.
	loaded, err := manager.Get(ctx, session.ID)
	require.NoError(t, err)
	maintenanceAt := time.Date(2099, 1, 2, 3, 4, 5, 0, time.UTC)
	loaded.UpdatedAt = maintenanceAt
	loaded.PreserveUpdatedAt = true
	require.NoError(t, manager.Update(ctx, loaded))
	manager.Stop()

	pinSessionsCommandWorkspace(t, workspace)
	result, sessions, err := collectSessionsCommandResult(&config.Config{}, sessionsCommandRequest{
		All:        true,
		SessionDir: sessionDir,
		User:       sessionsCommandTestUser,
		Limit:      10,
	})
	require.NoError(t, err)
	require.Len(t, result.Sessions, 1)
	item := result.Sessions[0]
	require.True(t, item.RowUpdatedAt.Equal(maintenanceAt),
		"row_updated_at must preserve the persisted maintenance timestamp")
	require.True(t, item.UpdatedAt.Before(item.RowUpdatedAt),
		"updated_at must come from message activity, got %s vs row %s", item.UpdatedAt, item.RowUpdatedAt)
	require.False(t, item.UpdatedAt.IsZero())

	out := captureSessionsCommandStdout(t, func() {
		renderSessionsCommandText(result, sessions)
	})
	require.NotContains(t, out, "2099-01-02", "text list must not render the row-maintenance timestamp")
}

func TestSessionsCommandQueryAndLimitFilterTheAllScope(t *testing.T) {
	t.Setenv("AICLI_HOME", t.TempDir())
	sessionDir := t.TempDir()
	firstWorkspace := t.TempDir()
	secondWorkspace := t.TempDir()
	ids := seedSessionsCommandStore(t, sessionDir, firstWorkspace, firstWorkspace, secondWorkspace)

	result, sessions, err := collectSessionsCommandResult(&config.Config{}, sessionsCommandRequest{
		All:        true,
		Query:      firstWorkspace,
		SessionDir: sessionDir,
		User:       sessionsCommandTestUser,
		Limit:      1,
	})
	require.NoError(t, err)
	require.Equal(t, 1, result.Count)
	require.Len(t, sessions, 1)
	require.Contains(t, []string{ids[0], ids[1]}, result.Sessions[0].SessionID,
		"--query 只应命中第一个工作目录里的会话")
}

func TestParseSessionsCommandRequestFlagsAndLimit(t *testing.T) {
	cmd := NewSessionsCommand(nil)
	require.NoError(t, cmd.ParseFlags([]string{"--all", "--json", "--limit", "5", "--query", "review"}))
	req, err := parseSessionsCommandRequest(cmd)
	require.NoError(t, err)
	require.True(t, req.All)
	require.Equal(t, "review", req.Query)
	require.Equal(t, 5, req.Limit)
	require.Equal(t, "json", req.Output.Format)
	require.False(t, req.Output.Envelope)

	negative := NewSessionsCommand(nil)
	require.NoError(t, negative.ParseFlags([]string{"--limit", "-3"}))
	_, err = parseSessionsCommandRequest(negative)
	require.Error(t, err, "负 limit 必须作为参数错误拒绝")

	enveloped := NewSessionsCommand(nil)
	require.NoError(t, enveloped.ParseFlags([]string{"--json", "--envelope"}))
	req, err = parseSessionsCommandRequest(enveloped)
	require.NoError(t, err)
	require.True(t, req.Output.Envelope)
}

func TestSessionsCommandRunsAndPrintsJSONList(t *testing.T) {
	t.Setenv("AICLI_HOME", t.TempDir())
	sessionDir := t.TempDir()
	workspace := t.TempDir()
	seedSessionsCommandStore(t, sessionDir, workspace)

	cmd := NewSessionsCommand(func() *config.Config { return &config.Config{} })
	cmd.SetArgs([]string{"--session-dir", sessionDir, "--user", sessionsCommandTestUser, "--all", "--json"})

	out := captureSessionsCommandStdout(t, func() {
		require.NoError(t, cmd.Execute())
	})

	var payload sessionsCommandResult
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(out)), &payload))
	require.Equal(t, "all", payload.Scope)
	require.Equal(t, sessionsCommandTestUser, payload.UserID)
	require.Equal(t, 1, payload.Count)
	require.Len(t, payload.Sessions, 1)
	require.Equal(t, workspace, payload.Sessions[0].Workspace)
}

func TestSessionsCommandTextOutputScopesToCurrentWorkspace(t *testing.T) {
	t.Setenv("AICLI_HOME", t.TempDir())
	t.Setenv("NO_COLOR", "1")
	sessionDir := t.TempDir()
	currentWorkspace := t.TempDir()
	otherWorkspace := t.TempDir()
	ids := seedSessionsCommandStore(t, sessionDir, otherWorkspace, currentWorkspace)
	pinSessionsCommandWorkspace(t, currentWorkspace)

	newRun := func(args ...string) *cobra.Command {
		cmd := NewSessionsCommand(func() *config.Config { return &config.Config{} })
		cmd.SetArgs(append([]string{"--session-dir", sessionDir, "--user", sessionsCommandTestUser}, args...))
		return cmd
	}

	out := captureSessionsCommandStdout(t, func() {
		require.NoError(t, newRun().Execute())
	})
	require.Contains(t, out, "可用会话（当前工作目录:")

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	require.Len(t, lines, 2, "头部 1 行 + 每条会话 1 行；会话信息不得拆成两行")
	row := lines[1]
	require.Regexp(t, `\d{4}-\d{2}-\d{2} \d{2}:\d{2}`, row, "行内必须给出时间")
	require.Contains(t, row, " · ", "字段之间必须有可扫读的分隔符")
	require.Regexp(t, `\d{4}-\d{2}-\d{2} \d{2}:\d{2}[^\n]*`+regexp.QuoteMeta(ids[1]), row,
		"时间必须排在 session ID 之前（同一行）")

	idIndex := strings.Index(row, ids[1])
	dirIndex := strings.Index(row, currentWorkspace)
	briefIndex := strings.Index(row, "会话 2")
	require.GreaterOrEqual(t, idIndex, 0, "文本列表必须给出可直接 resume 的 session id")
	// 顺序是「时间 · 会话 ID · 描述 · 目录」：描述是「这条会话是什么」的第一答案，
	// 必须排在目录之前；目录作为跨目录（--all）时的定位兜底留在末尾。
	require.True(t, briefIndex > idIndex && dirIndex > briefIndex,
		"字段顺序必须是 时间 · session ID · 简要 · 目录：%q", row)

	require.NotContains(t, out, "轮次=", "控制台不得再输出轮次/消息数等诊断字段")
	require.NotContains(t, out, "消息=")
	require.NotContains(t, out, "协议=")
	require.NotContains(t, out, "provider=")
	require.NotContains(t, out, ids[0], "默认范围不得泄漏其他工作目录的会话")
	require.NotContains(t, out, otherWorkspace, "默认范围不得泄漏其他工作目录的会话")

	emptyWorkspace := t.TempDir()
	pinSessionsCommandWorkspace(t, emptyWorkspace)
	out = captureSessionsCommandStdout(t, func() {
		require.NoError(t, newRun().Execute())
	})
	require.Contains(t, out, "当前工作目录暂无可用会话（--all 可查看全部工作目录）")
}
