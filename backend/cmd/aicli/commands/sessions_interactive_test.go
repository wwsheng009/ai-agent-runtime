package commands

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeexec "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/termopen"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ---------------------------------------------------------------------------
// 测试脚手架：会话行与脚本化渲染器
// ---------------------------------------------------------------------------

func browserTestItem(id, title string, launchArgs ...string) sessionsCommandItem {
	return sessionsCommandItem{
		SessionID:    id,
		Title:        title,
		State:        string(runtimechat.StateActive),
		Provider:     "openai",
		Model:        "gpt-4.1",
		Workspace:    "E:/projects/demo",
		MessageCount: 7,
		UpdatedAt:    time.Now().Add(-time.Hour),
		LaunchArgs:   launchArgs,
		LaunchCwd:    "E:/projects/demo",
		LaunchBinary: "E:/tools/aicli.exe",
	}
}

// newScriptRunner 按步骤脚本化「一屏渲染」：步骤是 spec -> pick 的函数，因此
// 断言可以按标题找行，而不是把行号写死（动作菜单会随可用性增减行）。
func newScriptRunner(steps ...func(sessionsBrowserListSpec) sessionsBrowserListPick) (*scriptedRunner, sessionsBrowserListRunner) {
	runner := &scriptedRunner{steps: steps}
	return runner, runner.run
}

type scriptedRunner struct {
	steps []func(sessionsBrowserListSpec) sessionsBrowserListPick
	specs []sessionsBrowserListSpec
	calls int
}

func (r *scriptedRunner) run(spec sessionsBrowserListSpec) (sessionsBrowserListPick, error) {
	r.specs = append(r.specs, spec)
	index := r.calls
	r.calls++
	if index >= len(r.steps) {
		// 脚本走完即取消：保证 runSessionsBrowser 一定收敛，不写死循环。
		return sessionsBrowserListPick{Cancelled: true}, nil
	}
	return r.steps[index](spec), nil
}

func (r *scriptedRunner) specAt(t *testing.T, index int) sessionsBrowserListSpec {
	t.Helper()
	require.Less(t, index, len(r.specs), "expected at least %d rendered screens", index+1)
	return r.specs[index]
}

// pickByTitle 在渲染出来的行里按标题前缀找一行；找不到即失败（防止动作被静默隐藏）。
func pickByTitle(prefix string) func(sessionsBrowserListSpec) sessionsBrowserListPick {
	return func(spec sessionsBrowserListSpec) sessionsBrowserListPick {
		for index, row := range spec.Items {
			if strings.HasPrefix(row.Title, prefix) {
				return sessionsBrowserListPick{Index: index}
			}
		}
		return sessionsBrowserListPick{Cancelled: true}
	}
}

func cancelScreen() func(sessionsBrowserListSpec) sessionsBrowserListPick {
	return func(sessionsBrowserListSpec) sessionsBrowserListPick {
		return sessionsBrowserListPick{Cancelled: true}
	}
}

// confirmDelete 在确认屏上选一行（yes = 确认删除，no = 取消），并先断言确认真的
// 弹了一道确认——删除不可逆，不能「点了就删」。
func confirmDelete(t *testing.T, confirmation string) func(sessionsBrowserListSpec) sessionsBrowserListPick {
	t.Helper()
	return func(spec sessionsBrowserListSpec) sessionsBrowserListPick {
		require.Contains(t, spec.Title, "删除")
		return sessionsBrowserListPick{Index: map[string]int{"yes": 0, "no": 1}[confirmation]}
	}
}

// ---------------------------------------------------------------------------
// 行投影
// ---------------------------------------------------------------------------

func TestBuildSessionsBrowserItemsOrdersTimeIDDescriptionAndSearchesLaunchArgs(t *testing.T) {
	items := []sessionsCommandItem{
		browserTestItem("session_1", "重构会话浏览器", "--web", "--yolo"),
		browserTestItem("session_2", ""),
	}
	rows, index := buildSessionsBrowserItems(items, "session_2")

	require.Len(t, rows, 2)
	// 一行的阅读顺序：时间（Leading）→ 会话 ID → 描述（Title 内）。ID 必须落在 Title
	// 而不是 Detail：Detail 列只有 min(32, 宽/3)，ID 放那里会被静默截掉。
	require.True(t, strings.HasPrefix(rows[0].Title, "session_1 · "),
		"Title 必须是「会话 ID · 描述」：%q", rows[0].Title)
	require.Contains(t, rows[0].Title, "重构会话浏览器", "描述紧跟在 ID 之后")
	require.NotContains(t, rows[0].Detail, "session_1", "Detail 不得重复 ID")
	require.Contains(t, rows[0].Detail, "E:/projects/demo")
	require.Contains(t, rows[0].Detail, "openai/gpt-4.1")
	require.Contains(t, rows[0].SearchText, "--yolo", "启动参数必须参与搜索：用户就是靠它找回某次启动方式")
	require.Equal(t, "session_2 · (untitled)", rows[1].Title, "无标题会话也要有一行可读描述")
	require.NotContains(t, rows[1].SearchText, "--")
	require.True(t, strings.HasPrefix(rows[1].Leading, "▸"), "刚操作过的行要被标记，重开列表后一眼能找到")
	require.False(t, strings.HasPrefix(rows[0].Leading, "▸"))

	picked, ok := index.at(1)
	require.True(t, ok)
	require.Equal(t, "session_2", picked.SessionID)
	_, ok = index.at(99)
	require.False(t, ok)
}

func TestBuildSessionsBrowserActionItemsHidesUnavailableActions(t *testing.T) {
	item := browserTestItem("session_1", "标题")

	// 终端后端缺失：那一行不出现（比「点了报错」更诚实）。
	noTerminal := sessionsBrowserActions{
		Rename: func(string, string) error { return nil },
		Delete: func(string) error { return nil },
	}
	rows, ids := buildSessionsBrowserActionItems(item, noTerminal)
	require.NotContains(t, strings.Join(actionTitles(rows), "|"), "在新终端里打开")
	require.Equal(t, sessionsActionBack, ids[len(ids)-1], "返回永远是最后一项")

	withTerminal := sessionsBrowserActions{
		OpenTerminal:    func(sessionsCommandItem) (string, error) { return "ok", nil },
		ResumeHere:      func(sessionsCommandItem) (string, error) { return "ok", nil },
		CanResumeHere:   true,
		TerminalSummary: "windows_terminal (wt.exe)",
	}
	rows, ids = buildSessionsBrowserActionItems(item, withTerminal)
	require.Contains(t, strings.Join(actionTitles(rows), "|"), "在新终端里打开")
	require.Contains(t, strings.Join(actionTitles(rows), "|"), "在当前终端里恢复")

	// 已归档会话给「取消归档」，未归档给「归档」：同一时刻只出现一个。
	archived := item
	archived.State = string(runtimechat.StateArchived)
	archivedRows, archivedIDs := buildSessionsBrowserActionItems(archived, sessionsBrowserActions{
		Archive:  func(string) error { return nil },
		Activate: func(string) error { return nil },
	})
	titles := strings.Join(actionTitles(archivedRows), "|")
	require.Contains(t, titles, "取消归档")
	require.NotContains(t, titles, "归档会话")
	require.Len(t, archivedIDs, len(archivedRows), "行号与动作枚举必须同长（菜单选中靠行号映射动作）")
}

func actionTitles(rows []uiFullScreenListItemLike) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Title)
	}
	return out
}

// ---------------------------------------------------------------------------
// 光标保持：动作完成后重开列表，必须停在刚操作的那一行
// ---------------------------------------------------------------------------

func TestSessionsBrowserKeepsCursorOnActedSession(t *testing.T) {
	items := []sessionsCommandItem{
		browserTestItem("session_1", "A"),
		browserTestItem("session_2", "B", "--web"),
		browserTestItem("session_3", "C"),
	}
	recorder, actions := newBrowserRecorder(items)
	script, runner := newScriptRunner(
		// 用户选中第二行 → 动作菜单 → 「在新终端里打开」→ 列表重开。
		func(sessionsBrowserListSpec) sessionsBrowserListPick { return sessionsBrowserListPick{Index: 1} },
		pickByTitle("在新终端里打开"),
		cancelScreen(),
	)

	outcome := runSessionsBrowser(actions, runner)
	require.Equal(t, sessionsExitOK, outcome.ExitCode)
	require.Equal(t, []string{"session_2"}, recorder.opened)

	// 第一屏没有历史选中（InitialRow=0）；动作后的重开屏必须指回第 1 行。
	listScreen := script.specAt(t, script.calls-1)
	require.Equal(t, 1, listScreen.InitialRow,
		"打开会话后光标不得跳回第一行，应停在刚操作的 session_2")
	require.Equal(t, "session_2 · B", listScreen.Items[listScreen.InitialRow].Title,
		"Title 是「会话 ID · 描述」；启动参数留在 Detail，不挤进标题列")
	require.Contains(t, listScreen.Items[listScreen.InitialRow].Detail, "启动 --web")
}

func TestSessionsBrowserCursorResetsAfterDelete(t *testing.T) {
	items := []sessionsCommandItem{
		browserTestItem("session_1", "A"),
		browserTestItem("session_2", "B"),
	}
	recorder, actions := newBrowserRecorder(items)
	script, runner := newScriptRunner(
		func(sessionsBrowserListSpec) sessionsBrowserListPick {
			return sessionsBrowserListPick{Index: 1, DeleteRequested: true}
		},
		confirmDelete(t, "yes"),
		cancelScreen(),
	)
	runSessionsBrowser(actions, runner)
	require.Equal(t, []string{"session_2"}, recorder.deleted)

	// 会话已不存在：光标无处可停，必须显式复位而不是留一个指向别人的旧下标。
	listScreen := script.specAt(t, script.calls-1)
	require.Equal(t, 0, listScreen.InitialRow)
}

func TestSessionsBrowserRowForID(t *testing.T) {
	items := []sessionsCommandItem{
		browserTestItem("session_1", "A"),
		browserTestItem("session_2", "B"),
	}
	require.Equal(t, 0, sessionsBrowserRowForID(items, ""))
	require.Equal(t, 1, sessionsBrowserRowForID(items, "session_2"))
	require.Equal(t, 1, sessionsBrowserRowForID(items, "SESSION_2"), "会话 ID 比较不区分大小写")
	require.Equal(t, 0, sessionsBrowserRowForID(items, "session_gone"), "不在结果里就回到首行")
	require.Equal(t, 0, sessionsBrowserRowForID(nil, "session_1"))
}

func TestSessionsBrowserListOptionsThreadsInitialRowToPicker(t *testing.T) {
	spec := sessionsBrowserListSpec{
		Title:      "会话列表",
		Items:      []uiFullScreenListItemLike{{Title: "a"}, {Title: "b"}},
		InitialRow: 1,
	}
	options := sessionsBrowserListOptions(spec)
	require.Equal(t, 1, options.InitialIndex, "行下标必须透传给全屏列表的 InitialIndex")
	require.Len(t, options.Items, 2)

	// 单值输入屏没有列表语义，不得带 InitialIndex。
	freeText := sessionsBrowserListSpec{
		Title:    "重命名",
		FreeText: &sessionsBrowserFreeText{Hint: "输入新标题", Value: "旧标题"},
	}
	freeOptions := sessionsBrowserListOptions(freeText)
	require.True(t, freeOptions.FreeTextMode)
	require.Equal(t, 0, freeOptions.InitialIndex)
	require.Empty(t, freeOptions.Items)
	require.NoError(t, freeOptions.OnConfirmText("新标题"))
}

// ---------------------------------------------------------------------------
// 交互主循环（纯逻辑 + 脚本渲染器）
// ---------------------------------------------------------------------------

// browserRecorder 记录动作表被调用的情况，供断言。
type browserRecorder struct {
	listed      int
	opened      []string
	resumed     []string
	renamed     map[string]string
	deleted     []string
	archived    []string
	activated   []string
	openMessage string
}

func newBrowserRecorder(items []sessionsCommandItem) (*browserRecorder, sessionsBrowserActions) {
	recorder := &browserRecorder{renamed: map[string]string{}}
	actions := sessionsBrowserActions{
		List: func(string) ([]sessionsCommandItem, error) {
			recorder.listed++
			return append([]sessionsCommandItem(nil), items...), nil
		},
		OpenTerminal: func(item sessionsCommandItem) (string, error) {
			recorder.opened = append(recorder.opened, item.SessionID)
			recorder.openMessage = "已在新终端打开会话 " + item.SessionID
			return recorder.openMessage, nil
		},
		ResumeHere: func(item sessionsCommandItem) (string, error) {
			recorder.resumed = append(recorder.resumed, item.SessionID)
			return "会话 " + item.SessionID + " 已结束", nil
		},
		Detail: func(sessionID string) (sessionsSessionDetail, error) {
			return sessionsSessionDetail{SessionID: sessionID, Title: "标题", LaunchArgs: []string{"--web"}}, nil
		},
		Rename: func(sessionID, title string) error {
			recorder.renamed[sessionID] = title
			return nil
		},
		Delete:          func(sessionID string) error { recorder.deleted = append(recorder.deleted, sessionID); return nil },
		Archive:         func(sessionID string) error { recorder.archived = append(recorder.archived, sessionID); return nil },
		Activate:        func(sessionID string) error { recorder.activated = append(recorder.activated, sessionID); return nil },
		CanResumeHere:   true,
		TerminalSummary: "gnome_terminal",
	}
	return recorder, actions
}

func TestRunSessionsBrowserOpensTerminalThenExitsWithMessage(t *testing.T) {
	recorder, actions := newBrowserRecorder([]sessionsCommandItem{browserTestItem("session_1", "A", "--web")})
	_, runner := newScriptRunner(
		pickByTitle(""), // 第一屏：选中第 0 行（任意标题都匹配）
		pickByTitle("在新终端里打开"),
		cancelScreen(), // 回到列表后 Esc 退出
	)

	outcome := runSessionsBrowser(actions, runner)
	require.Equal(t, sessionsExitOK, outcome.ExitCode)
	require.Equal(t, []string{"session_1"}, recorder.opened)
	require.Contains(t, outcome.Message, "已在新终端打开会话 session_1")
	require.GreaterOrEqual(t, recorder.listed, 2, "动作完成后必须重新拉一次列表，界面才看得到变化")
}

func TestRunSessionsBrowserResumeHereEndsTheBrowse(t *testing.T) {
	recorder, actions := newBrowserRecorder([]sessionsCommandItem{browserTestItem("session_1", "A")})
	_, runner := newScriptRunner(
		pickByTitle(""),
		pickByTitle("在当前终端里恢复"),
	)

	outcome := runSessionsBrowser(actions, runner)
	require.Equal(t, sessionsExitOK, outcome.ExitCode)
	require.Equal(t, []string{"session_1"}, recorder.resumed)
	require.Contains(t, outcome.Message, "已结束")
}

func TestRunSessionsBrowserDeleteRequiresConfirmation(t *testing.T) {
	recorder, actions := newBrowserRecorder([]sessionsCommandItem{browserTestItem("session_1", "A")})
	_, runner := newScriptRunner(
		// x 键：列表直接回 DeleteRequested，进确认屏后选「取消」。
		func(sessionsBrowserListSpec) sessionsBrowserListPick {
			return sessionsBrowserListPick{Index: 0, DeleteRequested: true}
		},
		confirmDelete(t, "no"),
		cancelScreen(),
	)
	outcome := runSessionsBrowser(actions, runner)
	require.Equal(t, sessionsExitOK, outcome.ExitCode)
	require.Empty(t, recorder.deleted, "取消确认绝不能删任何东西")

	recorder, actions = newBrowserRecorder([]sessionsCommandItem{browserTestItem("session_2", "B")})
	_, runner = newScriptRunner(
		func(sessionsBrowserListSpec) sessionsBrowserListPick {
			return sessionsBrowserListPick{Index: 0, DeleteRequested: true}
		},
		confirmDelete(t, "yes"),
		cancelScreen(),
	)
	outcome = runSessionsBrowser(actions, runner)
	require.Equal(t, []string{"session_2"}, recorder.deleted)
	require.Contains(t, outcome.Message, "已删除")
}

func TestRunSessionsBrowserRenameSubmitsNonEmptyTitle(t *testing.T) {
	recorder, actions := newBrowserRecorder([]sessionsCommandItem{browserTestItem("session_1", "旧标题")})
	_, runner := newScriptRunner(
		pickByTitle(""),
		pickByTitle("重命名会话"),
		func(spec sessionsBrowserListSpec) sessionsBrowserListPick {
			require.NotNil(t, spec.FreeText, "重命名必须是单值输入屏")
			require.Equal(t, "旧标题", spec.FreeText.Value, "输入框要预填当前标题")
			return sessionsBrowserListPick{Text: "  新标题  "}
		},
		cancelScreen(),
	)
	outcome := runSessionsBrowser(actions, runner)
	require.Equal(t, "新标题", recorder.renamed["session_1"])
	require.Contains(t, outcome.Message, "已重命名")

	// 空标题被校验拦住：validate 返回错误时不该落库。
	recorder, actions = newBrowserRecorder([]sessionsCommandItem{browserTestItem("session_1", "旧标题")})
	_, runner = newScriptRunner(
		pickByTitle(""),
		pickByTitle("重命名会话"),
		func(spec sessionsBrowserListSpec) sessionsBrowserListPick {
			require.Error(t, spec.FreeText.Validate(""))
			require.NoError(t, spec.FreeText.Validate("可以"))
			return sessionsBrowserListPick{Cancelled: true}
		},
		cancelScreen(),
	)
	runSessionsBrowser(actions, runner)
	require.Empty(t, recorder.renamed, "取消输入不得改标题")
}

func TestRunSessionsBrowserDetailIsReadOnlyAndReturns(t *testing.T) {
	recorder, actions := newBrowserRecorder([]sessionsCommandItem{browserTestItem("session_1", "A", "--web")})
	var detailSpec sessionsBrowserListSpec
	_, runner := newScriptRunner(
		pickByTitle(""),
		pickByTitle("查看详情"),
		func(spec sessionsBrowserListSpec) sessionsBrowserListPick {
			detailSpec = spec
			return sessionsBrowserListPick{Cancelled: true}
		},
		cancelScreen(),
	)
	outcome := runSessionsBrowser(actions, runner)
	require.Equal(t, sessionsExitOK, outcome.ExitCode)
	require.Contains(t, strings.Join(actionTitles(detailSpec.Items), "|"), "--web", "详情必须把启动参数摊开")
	require.Equal(t, 0, len(recorder.deleted))
}

func TestRunSessionsBrowserListFailureExitsWithFailureCode(t *testing.T) {
	_, actions := newBrowserRecorder(nil)
	actions.List = func(string) ([]sessionsCommandItem, error) { return nil, context.DeadlineExceeded }
	_, runner := newScriptRunner()

	outcome := runSessionsBrowser(actions, runner)
	require.Equal(t, sessionsExitFailure, outcome.ExitCode)
}

// ---------------------------------------------------------------------------
// 入口门控与非交互直达
// ---------------------------------------------------------------------------

func TestShouldRunSessionsInteractiveKeepsScriptContracts(t *testing.T) {
	// 非 TTY（nil 终端）：一律文本，脚本与管道行为不能被 TTY 探测改变。
	require.False(t, shouldRunSessionsInteractive(sessionsCommandRequest{Interactive: true}, nil))
	require.False(t, shouldRunSessionsInteractive(sessionsCommandRequest{Interactive: true, Plain: true}, nil))
	require.False(t, shouldRunSessionsInteractive(sessionsCommandRequest{Interactive: false}, nil))
	jsonRequest := sessionsCommandRequest{Interactive: true}
	jsonRequest.Output = structuredOutputOptions{Format: "json"}
	require.False(t, shouldRunSessionsInteractive(jsonRequest, nil), "JSON 输出永远走机器可读通道")
}

func TestParseSessionsCommandRequestReadsNewFlags(t *testing.T) {
	cmd := NewSessionsCommand(func() *config.Config { return &config.Config{} })
	cmd.SetArgs([]string{"--plain", "--terminal", "kitty", "--open-terminal", "session_9", "--limit", "5"})
	require.NoError(t, cmd.ParseFlags([]string{"--plain", "--terminal", "kitty", "--open-terminal", "session_9", "--limit", "5"}))

	req, err := parseSessionsCommandRequest(cmd)
	require.NoError(t, err)
	require.True(t, req.Plain)
	require.False(t, req.Interactive, "--plain 只改界面开关，文本路径仍走同一条渲染")
	require.Equal(t, "kitty", req.Terminal)
	require.Equal(t, "session_9", req.OpenTerminal)
	require.True(t, req.LimitExplicit)
	require.Equal(t, 5, req.Limit)

	defaults, err := parseSessionsCommandRequest(NewSessionsCommand(func() *config.Config { return &config.Config{} }))
	require.NoError(t, err)
	require.True(t, defaults.Interactive, "默认进交互界面（非 TTY 时再由门控降级）")
	require.False(t, defaults.LimitExplicit, "默认 --limit 没被显式指定，交互界面才能浏览更多")
}

// ---------------------------------------------------------------------------
// 存储读路径：启动参数必须真的能从会话库读回界面
// ---------------------------------------------------------------------------

func seedLaunchAwareStore(t *testing.T, dir, workspace string) []string {
	t.Helper()
	manager, userID, _, err := newChatSessionManagerWithRuntimeConfig(dir, nil, "", sessionsCommandTestUser)
	require.NoError(t, err)
	defer manager.Stop()

	ctx := context.Background()
	ids := make([]string, 0, 2)
	// 一条带启动参数、一条完全没记录（老会话），外加一条空占位会话。
	record := &launchInvocationRecord{
		Instance: "pid@nano", Entry: "chat",
		RawArgv: []string{"chat", "--web", "--web-token", launchRedactedValue, "-m", "gpt-4.1"},
		Flags:   []string{"--web", "-m", "gpt-4.1"},
		Binary:  "E:/tools/aicli.exe", Cwd: workspace,
		RecordedAt: time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC),
	}
	for index := 0; index < 3; index++ {
		session, err := manager.Create(ctx, userID)
		require.NoError(t, err)
		session.SetContext(sessionmeta.WorkspacePath, workspace)
		session.UpdateTitle("会话 " + string(rune('1'+index)))
		if index != 2 {
			session.AddMessage(*runtimetypes.NewUserMessage("hello"))
		}
		if index == 0 {
			require.True(t, applyLaunchInvocationToContext(session.Metadata.Context, record, record.RecordedAt))
			session.SetContext(sessionmeta.LaunchCommand, formatLaunchCommand(record.Binary, session.ID, record.Flags))
		}
		require.NoError(t, manager.Update(ctx, session))
		ids = append(ids, session.ID)
	}
	return ids
}

func TestSessionsBrowserStoreReadsLaunchArgsBack(t *testing.T) {
	dir := t.TempDir()
	workspace := t.TempDir()
	ids := seedLaunchAwareStore(t, dir, workspace)

	manager, userID, _, err := newChatSessionManagerWithRuntimeConfig(dir, nil, "", sessionsCommandTestUser)
	require.NoError(t, err)
	defer manager.Stop()
	store := &sessionsBrowserStore{
		manager:   manager,
		userID:    userID,
		workspace: workspace,
		limit:     sessionsUnlimitedLimit,
		filter:    ChatSessionListFilter{Workspace: workspace, Limit: sessionsUnlimitedLimit},
	}

	items, err := store.List("")
	require.NoError(t, err)
	require.Len(t, items, 3)

	var withLaunch *sessionsCommandItem
	for index := range items {
		if items[index].SessionID == ids[0] {
			withLaunch = &items[index]
		}
	}
	require.NotNil(t, withLaunch)
	require.Equal(t, []string{"--web", "-m", "gpt-4.1"}, withLaunch.LaunchArgs)
	require.Equal(t, workspace, withLaunch.LaunchCwd)
	require.Equal(t, "E:/tools/aicli.exe", withLaunch.LaunchBinary)
	require.Equal(t, "chat", withLaunch.LaunchEntry)
	require.Contains(t, withLaunch.LaunchCommand, "resume "+ids[0])
	require.False(t, withLaunch.LaunchRecorded.IsZero())

	// itemByID 走元数据口径（不受界面筛选态影响）；latestSessionItem 跳过空占位会话。
	item, err := store.itemByID(ids[0])
	require.NoError(t, err)
	require.Equal(t, withLaunch.LaunchArgs, item.LaunchArgs)
	_, err = store.itemByID("session_missing")
	require.ErrorContains(t, err, "不存在")

	latest, err := store.latestSessionItem()
	require.NoError(t, err)
	require.NotEqual(t, ids[2], latest.SessionID, "只有占位消息的会话不可恢复")

	detail, err := store.Detail(ids[0])
	require.NoError(t, err)
	require.Equal(t, withLaunch.LaunchArgs, detail.LaunchArgs)
	require.NotEmpty(t, detail.lines())
	require.Contains(t, strings.Join(detail.lines(), "\n"), "--web")
}

// ---------------------------------------------------------------------------
// 新终端拉起：命令与目录必须来自记录下来的启动参数
// ---------------------------------------------------------------------------

// newTerminalCapturingLauncher 造一个注入 Start 的终端 Launcher：只记录平台真正
// 收到的 DetachedLaunch，不开任何窗口。available 决定哪些终端程序「存在」，
// 从而让 Windows 用例能落到 new_console 回退分支。
func newTerminalCapturingLauncher(goos string, available map[string]bool, captured **runtimeexec.DetachedLaunch) *termopen.Launcher {
	return &termopen.Launcher{
		GOOS: goos,
		LookPath: func(name string) (string, error) {
			if available[name] {
				return "/usr/bin/" + name, nil
			}
			return "", exec.ErrNotFound
		},
		Getenv:  func(string) string { return "" },
		Environ: func() []string { return []string{"PATH=/usr/bin"} },
		Start: func(spec runtimeexec.DetachedLaunch) (*runtimeexec.DetachedProcess, error) {
			copied := spec
			*captured = &copied
			return &runtimeexec.DetachedProcess{PID: 99, Mode: runtimeexec.DetachModeSetsid}, nil
		},
	}
}

func TestOpenSessionInNewTerminalReplaysRecordedFlagsInSessionWorkspace(t *testing.T) {
	var captured *runtimeexec.DetachedLaunch
	launcher := newTerminalCapturingLauncher("linux", map[string]bool{"gnome-terminal": true}, &captured)

	item := browserTestItem("session_1", "重构", "--web", "--yolo")
	message, err := openSessionInNewTerminal(item, launcher)
	require.NoError(t, err)
	require.NotNil(t, captured)
	require.Equal(t, "E:/projects/demo", captured.Dir, "新窗口必须落在会话自己的工作目录")
	// 终端型后端的 Path 是宿主程序（gnome-terminal），aicli 只能出现在 argv 尾部：
	// `… -- <binary> resume <id> <记录的旗标>`。
	require.Equal(t, "gnome-terminal", baseNameForTest(captured.Path))
	tail := captured.Args[len(captured.Args)-5:]
	require.Equal(t, []string{"E:/tools/aicli.exe", "resume", "session_1", "--web", "--yolo"}, tail)
	require.Contains(t, captured.Args, "--yolo", "全部启动参数都要带上，重放才是同一次启动")
	require.Contains(t, captured.Env, "AICLI_SESSION_LAUNCHED_BY=sessions-ui")
	require.Contains(t, message, "session_1")
}

func TestOpenSessionInNewTerminalUsesSameBinaryWithoutRecordedLaunch(t *testing.T) {
	// 老会话没记录 launch_*，且本机没有 Windows Terminal：走 new_console 回退，
	// 由平台直接给 aicli 开一个新控制台窗口。此时 Path 就必须是二进制本身
	// （用 os.Executable，而不是 PATH 里可能存在的同名旧版）。
	var captured *runtimeexec.DetachedLaunch
	launcher := newTerminalCapturingLauncher("windows", map[string]bool{}, &captured)
	item := browserTestItem("session_9", "旧会话")
	item.LaunchBinary = ""
	item.LaunchCwd = ""
	item.Workspace = "E:/projects/ws"

	_, err := openSessionInNewTerminal(item, launcher)
	require.NoError(t, err)
	require.Equal(t, []string{"resume", "session_9"}, captured.Args)
	require.Equal(t, "E:/projects/ws", captured.Dir, "没有 launch_cwd 时用会话工作区兜底")
	require.True(t, filepath.IsAbs(captured.Path), "拉起用的二进制必须是绝对路径: %q", captured.Path)
}

func baseNameForTest(path string) string {
	if index := strings.LastIndexAny(path, `/\`); index >= 0 && index+1 < len(path) {
		return path[index+1:]
	}
	return path
}

func TestOpenSessionInNewTerminalNeedsSessionID(t *testing.T) {
	launcher := &termopen.Launcher{GOOS: "linux", LookPath: func(name string) (string, error) { return name, nil }, Getenv: func(string) string { return "" }}
	_, err := openSessionInNewTerminal(sessionsCommandItem{}, launcher)
	require.Error(t, err)
}

func TestSessionsTerminalLauncherHonoursOverride(t *testing.T) {
	base := map[string]string{"AICLI_TERMINAL": "kitty", "HOME": "/home/u"}
	launcher := termopen.Default()
	launcher.Getenv = func(key string) string {
		if key == termopen.EnvTerminal {
			return "alacritty"
		}
		return base[key]
	}
	backend, err := launcher.Detect()
	require.NoError(t, err)
	require.Equal(t, termopen.BackendAlacritty, backend)
	// 没覆盖时按环境走（不要求本机真的装了 kitty，只断言解析没被吞掉）。
	environment := sessionsTerminalLauncher("")
	require.NotNil(t, environment)
}

// ---------------------------------------------------------------------------
// resume --detach 的请求映射
// ---------------------------------------------------------------------------

func TestResumeDetachRequestMapsChatFlagsOntoSessionsScope(t *testing.T) {
	cmd := NewResumeCommand(func() *config.Config { return &config.Config{} })
	require.NoError(t, cmd.ParseFlags([]string{"--cwd=false", "--session-query", "review", "--session-limit", "7", "--terminal", "wezterm"}))
	req, sessionID, err := resumeDetachRequest(cmd, []string{"session_5"})
	require.NoError(t, err)
	require.Equal(t, "session_5", sessionID)
	require.True(t, req.All, "--cwd=false 就是全工作目录")
	require.Equal(t, "review", req.Query)
	require.Equal(t, 7, req.Limit)
	require.True(t, req.LimitExplicit)
	require.Equal(t, "wezterm", req.Terminal)
	require.True(t, req.Plain, "detach 入口永远不该弹出界面")

	cwdDefault := NewResumeCommand(func() *config.Config { return &config.Config{} })
	req, sessionID, err = resumeDetachRequest(cwdDefault, nil)
	require.NoError(t, err)
	require.Empty(t, sessionID)
	require.False(t, req.All, "默认只看当前工作目录（与 resume 本体同口径）")
}

func TestResumeDetachRequestRejectsConflicts(t *testing.T) {
	cmd := NewResumeCommand(func() *config.Config { return &config.Config{} })
	_, _, err := resumeDetachRequest(cmd, []string{"a", "b"})
	require.Error(t, err)

	list := NewResumeCommand(func() *config.Config { return &config.Config{} })
	require.NoError(t, list.ParseFlags([]string{"--list-sessions"}))
	_, _, err = resumeDetachRequest(list, nil)
	require.ErrorContains(t, err, "--list-sessions")

	conflict := NewResumeCommand(func() *config.Config { return &config.Config{} })
	require.NoError(t, conflict.ParseFlags([]string{"--session", "session_other"}))
	_, _, err = resumeDetachRequest(conflict, []string{"session_1"})
	require.ErrorContains(t, err, "冲突")
}

func TestLaunchInvocationFlagsAreRegisteredOnResume(t *testing.T) {
	// --detach/--terminal 只属于顶层 resume；不进 registerChatFlags，避免污染
	// chat/acp/exec 的旗标面（那条契约由本断言钉住）。
	resume := NewResumeCommand(func() *config.Config { return &config.Config{} })
	require.NotNil(t, resume.Flags().Lookup("detach"))
	require.NotNil(t, resume.Flags().Lookup("terminal"))

	chat := NewChatCommand(func() *config.Config { return &config.Config{} })
	require.Nil(t, chat.Flags().Lookup("detach"))
}
