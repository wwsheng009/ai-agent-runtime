package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
)

// chat_screen_batch3_test.go 覆盖批次 3（长文档迁入）的验收项：
//   - /help、/status、/sessions、/plans detail、/functions、/timeline、
//     /collab、/hotkeys、/agents panel full、/agent transcript 在统一出口生成
//     ScreenDocument（§5.2（2）），非统一会话保持原有内联文档（字符级一致）；
//   - 每个新 Spec 都经框架生命周期（命令 → 副屏 → Esc → 主屏），租约释放；
//   - A3：统一交互路径上 printChatCommandOutput 直写归零（观测点断言）。

// batch3UnifiedSession 构造"已跨越终极端口所有权"的会话：统一出口判定由
// TerminalSession 承担（无需真实 TTY），交互协调器复用批次 1 测试夹具。
func batch3UnifiedSession(t *testing.T) *ChatSession {
	t.Helper()
	session := newChatScreenTestSession(t)
	session.TerminalSession = &ui.TerminalSession{}
	return session
}

// TestChatScreenBatch3UnifiedRouting 断言批次 3 命令在统一出口返回
// ScreenDocument Spec（而不是主屏内联单元格），且副屏正文与 legacy 文本同源。
func TestChatScreenBatch3UnifiedRouting(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)

	cases := []struct {
		command      string
		screenID     string
		title        string
		legacyMarker string
	}{
		{command: "/help", screenID: "help.screen", title: "命令帮助", legacyMarker: "可用命令"},
		{command: "/status", screenID: "status.screen", title: "会话状态", legacyMarker: "Context used"},
		{command: "/hotkeys", screenID: "hotkeys.screen", title: "快捷键", legacyMarker: "快捷键（当前生效）"},
		{command: "/timeline", screenID: "timeline.screen", title: "协作时间线", legacyMarker: "Collab Timeline:"},
		{command: "/collab", screenID: "collab.screen", title: "协作邮箱", legacyMarker: "Mailbox"},
	}

	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			session := batch3UnifiedSession(t)
			result, handled, err := tryExecuteStructuredChatCommand(session, tc.command)
			if err != nil || !handled {
				t.Fatalf("%s handled=%v err=%v", tc.command, handled, err)
			}
			if result.Screen == nil {
				t.Fatalf("统一 %s 必须携带 Screen Spec", tc.command)
			}
			if result.Screen.ID != tc.screenID {
				t.Fatalf("screen id = %q，期望 %q", result.Screen.ID, tc.screenID)
			}
			if result.Screen.Kind != chatScreenDocument || result.Screen.Trigger != "command" {
				t.Fatalf("spec = %+v，期望 document/command", *result.Screen)
			}
			if result.Screen.Title != tc.title {
				t.Fatalf("title = %q，期望 %q", result.Screen.Title, tc.title)
			}
			if err := result.Screen.validate(); err != nil {
				t.Fatalf("spec 非法：%v", err)
			}
			body := ui.RenderDocumentPlain(result.Screen.Doc)
			if !strings.Contains(body, tc.legacyMarker) {
				t.Fatalf("副屏正文缺失 %q：%q", tc.legacyMarker, body)
			}
			if strings.TrimSpace(ui.RenderDocumentPlain(result.Document())) != "" {
				t.Fatalf("统一 %s 不应同时提交内联命令单元格：%q", tc.command, ui.RenderDocumentPlain(result.Document()))
			}
		})
	}
}

// TestChatScreenBatch3PlainKeepsInlineDocuments 验证非统一会话（plain/JSON/
// headless）仍走既有内联文档：Screen 为空，正文与统一副屏同源。
func TestChatScreenBatch3PlainKeepsInlineDocuments(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)

	cases := []struct {
		command string
		marker  string
	}{
		{command: "/help", marker: "可用命令"},
		{command: "/status", marker: "Context used"},
		{command: "/hotkeys", marker: "快捷键（当前生效）"},
		{command: "/timeline", marker: "Collab Timeline:"},
		{command: "/collab", marker: "Mailbox"},
	}

	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			session := &ChatSession{}
			result, handled, err := tryExecuteStructuredChatCommand(session, tc.command)
			if err != nil || !handled {
				t.Fatalf("%s handled=%v err=%v", tc.command, handled, err)
			}
			if result.Screen != nil {
				t.Fatalf("非统一会话不应生成 Screen：%+v", result.Screen)
			}
			body := ui.RenderDocumentPlain(result.Document())
			if !strings.Contains(body, tc.marker) {
				t.Fatalf("plain %s 文档缺失 %q：%q", tc.command, tc.marker, body)
			}
		})
	}
}

// TestChatScreenBatch3SpecsLifecycleEscReleasesLease 验证每个新 Spec 经统一
// 生命周期：命令 → 副屏（Esc）→ 主屏，结果在租约释放后返回。
func TestChatScreenBatch3SpecsLifecycleEscReleasesLease(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)

	specs := []struct {
		name string
		spec chatScreenSpec
	}{
		{name: "help", spec: chatScreenHelpSpec()},
		{name: "status", spec: chatScreenStatusSpec(newChatScreenTestSession(t))},
		{name: "sessions", spec: chatScreenSessionsSpec(textLinesDocument([]string{"历史会话:", "  a", "  b"}))},
		{name: "plans detail", spec: chatScreenPlansDetailSpec(textLinesDocument([]string{"计划: demo"}))},
		{name: "functions", spec: chatScreenFunctionsSpec("函数目录", textLinesDocument([]string{"函数: demo"}))},
		{name: "hotkeys", spec: chatScreenHotkeysSpec()},
		{name: "timeline", spec: chatScreenTimelineSpec(newChatScreenTestSession(t), "/timeline")},
		{name: "collab", spec: chatScreenCollabSpec(newChatScreenTestSession(t), "/collab")},
		{name: "agents panel", spec: chatScreenAgentPanelSpec([]string{"Agent Panel:", "  a"})},
		{name: "agent transcript", spec: chatScreenAgentTranscriptSpec([]string{"Agent Transcript:", "  a"})},
	}

	for _, tc := range specs {
		t.Run(tc.name, func(t *testing.T) {
			session := newChatScreenTestSession(t)
			if err := tc.spec.validate(); err != nil {
				t.Fatalf("spec 非法：%v", err)
			}
			before := chatScreenCounterSnapshotForDebug()
			var ran bool
			chatScreenDocumentRunner = func(_ *ChatSession, lease ui.ScreenLease, _ ui.DebugOverlayOptions) error {
				ran = true
				if lease == nil || !lease.Active() {
					t.Fatal("runner 必须在已持有的租约内运行")
				}
				return nil // Esc
			}
			outcome := openChatScreen(session, tc.spec)
			if !ran || outcome.Degraded || outcome.Result != chatScreenClosedEsc {
				t.Fatalf("outcome = %+v ran=%v，期望副屏 Esc 关闭", outcome, ran)
			}
			if session.Surface.LeaseActive() {
				t.Fatal("Esc 关闭后租约必须已释放（I2）")
			}
			snapshot := chatScreenCounterSnapshotForDebug()
			// 计数器是包级累计量：断言本次子测试的增量，避免子测试间相互污染。
			if snapshot.Opens-before.Opens != 1 || snapshot.Closes-before.Closes != 1 || snapshot.CloseEsc-before.CloseEsc != 1 {
				t.Fatalf("counters = %+v（before=%+v），期望本次增量 opens/closes/esc = 1", snapshot, before)
			}
		})
	}
}

// TestChatScreenBatch3UnifiedRoutingDocumentFixtures 覆盖需要夹具的其余批次 3
// 命令：/sessions、/functions、/plans detail、/agents panel full；/agent
// transcript 缺 runtime event store 时保持内联错误（不进入副屏）。
func TestChatScreenBatch3UnifiedRoutingDocumentFixtures(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)

	store := withPlanArtifactStore(t)
	record, err := store.Record(planstore.RecordOptions{
		ID:        "batch3-plan",
		SessionID: "batch3-user",
		PlanPath:  "docs/plan/batch3.md",
		Title:     "批次 3 演示",
	})
	if err != nil {
		t.Fatalf("record plan: %v", err)
	}
	if _, err := store.Snapshot(planstore.SnapshotOptions{
		ID:       record.ID,
		Decision: "approve",
		Source:   "test",
		Content:  []byte("# 批次 3 演示正文\n\n迁移后的详情正文。\n"),
	}); err != nil {
		t.Fatalf("snapshot plan: %v", err)
	}

	storage := runtimechat.NewInMemoryStorage()
	manager := runtimechat.NewSessionManager(storage, nil)
	defer manager.Stop()
	runtimeSession, err := manager.Create(context.Background(), "batch3-user")
	if err != nil {
		t.Fatalf("create runtime session: %v", err)
	}

	newSession := func(t *testing.T) *ChatSession {
		t.Helper()
		session := batch3UnifiedSession(t)
		session.SessionManager = manager
		session.SessionUserID = "batch3-user"
		session.RuntimeSession = runtimeSession
		return session
	}

	cases := []struct {
		command  string
		screenID string
		title    string
		marker   string
	}{
		{command: "/sessions", screenID: "sessions.screen", title: "历史会话"},
		{command: "/functions --json", screenID: "functions.screen", title: "函数目录"},
		{command: "/plans batch3-plan", screenID: "plans.detail", title: "计划详情", marker: "迁移后的详情正文"},
		{command: "/agents panel full", screenID: "agents.panel", title: "Agent 面板"},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			session := newSession(t)
			result, handled, err := tryExecuteStructuredChatCommand(session, tc.command)
			if err != nil || !handled {
				t.Fatalf("%s handled=%v err=%v", tc.command, handled, err)
			}
			if result.Screen == nil {
				t.Fatalf("统一 %s 必须携带 Screen Spec", tc.command)
			}
			if result.Screen.ID != tc.screenID || result.Screen.Title != tc.title {
				t.Fatalf("spec = %+v，期望 %s/%s", *result.Screen, tc.screenID, tc.title)
			}
			if err := result.Screen.validate(); err != nil {
				t.Fatalf("spec 非法：%v", err)
			}
			body := ui.RenderDocumentPlain(result.Screen.Doc)
			if strings.TrimSpace(body) == "" {
				t.Fatalf("%s 副屏正文为空", tc.command)
			}
			if tc.marker != "" && !strings.Contains(body, tc.marker) {
				t.Fatalf("%s 副屏正文缺失 %q：%q", tc.command, tc.marker, body)
			}
			if strings.TrimSpace(ui.RenderDocumentPlain(result.Document())) != "" {
				t.Fatalf("统一 %s 不应同时提交内联命令单元格", tc.command)
			}
		})
	}

	t.Run("/agent transcript degrades inline", func(t *testing.T) {
		session := batch3UnifiedSession(t)
		result, handled, err := tryExecuteStructuredChatCommand(session, "/agent transcript")
		if err != nil || !handled {
			t.Fatalf("handled=%v err=%v", handled, err)
		}
		if result.Screen != nil {
			t.Fatalf("缺少 runtime event store 时不应进入副屏：%+v", result.Screen)
		}
		if body := ui.RenderDocumentPlain(result.Document()); !strings.Contains(body, "runtime event store") {
			t.Fatalf("内联错误正文 = %q", body)
		}
	})
}

// TestChatScreenBatch3UnifiedPathHasNoDirectCommandWrites 是 A3 守卫：批次 3
// 命令在统一出口全部经结构化通道处理，不再发生 printChatCommandOutput 直写。
func TestChatScreenBatch3UnifiedPathHasNoDirectCommandWrites(t *testing.T) {
	chatScreenTestSeamsInstall(t, true)

	previous := chatCommandOutputObserver
	t.Cleanup(func() { chatCommandOutputObserver = previous })
	var observed []string
	chatCommandOutputObserver = func(_ *ChatSession, text string) {
		observed = append(observed, text)
	}

	session := batch3UnifiedSession(t)
	for _, command := range []string{
		"/help", "/status", "/hotkeys", "/timeline", "/collab",
		// 尾批只读变体（§5.2（2）末行）同样不得回落到 legacy stdout。
		"/model status", "/provider status", "/theme status", "/theme list",
		"/theme preview", "/mcp list", "/mcp status", "/profile status", "/profile list",
	} {
		if _, handled, err := tryExecuteStructuredChatCommand(session, command); err != nil || !handled {
			t.Fatalf("%s handled=%v err=%v", command, handled, err)
		}
	}
	if len(observed) != 0 {
		t.Fatalf("统一路径发生 legacy stdout 直写：%q", observed)
	}
}

// TestChatScreenBatch3ReadOnlyVariantsUseScreenDocument 覆盖批次 3 尾批：只读
// list/status 变体（/model status、/provider status、/theme
// status|list|preview、/skills list、/mcp list|status、/profile
// status|list|show|diff）在统一出口返回 ScreenDocument；plain 出口保持内联，
// 且两侧正文逐行一致（同一构建函数的两个投影）。
func TestChatScreenBatch3ReadOnlyVariantsUseScreenDocument(t *testing.T) {
	type variantCase struct {
		name       string
		id         string
		markers    []string
		setup      func(*ChatSession)
		newSession func(t *testing.T) *ChatSession
		run        func(*ChatSession) (CommandResult, bool)
	}

	modelSetup := func(session *ChatSession) {
		session.ProviderName = "alpha"
		session.Model = "gpt-4.1"
	}
	themeRun := func(command string) func(*ChatSession) (CommandResult, bool) {
		return func(session *ChatSession) (CommandResult, bool) {
			return executeStructuredThemeCommand(session, command)
		}
	}
	skillsSession := func(t *testing.T) *ChatSession {
		t.Helper()
		return newTestSkillSession()
	}

	profilesRoot := t.TempDir()
	writeProfileCommandFixtures(t, profilesRoot)
	profileSession := func(t *testing.T) *ChatSession {
		t.Helper()
		session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
		t.Cleanup(cleanup)
		return session
	}

	cases := []variantCase{
		{
			name: "model status", id: "model.status",
			markers: []string{"当前 provider: alpha", "当前模型: gpt-4.1"},
			setup:   modelSetup,
			run: func(session *ChatSession) (CommandResult, bool) {
				return executeStructuredModelCommand(session, "/model status")
			},
		},
		{
			name: "provider status", id: "provider.status",
			markers: []string{"当前 provider: alpha"},
			setup:   modelSetup,
			run: func(session *ChatSession) (CommandResult, bool) {
				return executeStructuredProviderCommand(session, "/provider status")
			},
		},
		{name: "theme status", id: "theme.status", run: themeRun("/theme status")},
		{name: "theme list", id: "theme.list", run: themeRun("/theme list")},
		{name: "theme preview", id: "theme.preview", run: themeRun("/theme preview")},
		{
			name: "skills list", id: "skills.list",
			newSession: skillsSession,
			run: func(session *ChatSession) (CommandResult, bool) {
				return executeStructuredSkillsMenuCommand(session, "/skills list")
			},
		},
		{
			name: "mcp list", id: "mcp.list",
			run: func(session *ChatSession) (CommandResult, bool) {
				return executeStructuredMCPCommand(session, "/mcp list"), true
			},
		},
		{
			name: "mcp status", id: "mcp.status",
			run: func(session *ChatSession) (CommandResult, bool) {
				return executeStructuredMCPCommand(session, "/mcp status"), true
			},
		},
	}
	for _, spec := range []struct{ command, id string }{
		{"/profile status", "profile.status"},
		{"/profile list", "profile.list"},
		{"/profile show coding", "profile.show"},
		{"/profile diff coding", "profile.diff"},
	} {
		command, id := spec.command, spec.id
		cases = append(cases, variantCase{
			name: command, id: id,
			newSession: profileSession,
			run: func(session *ChatSession) (CommandResult, bool) {
				return tryExecuteStructuredProfileCommand(session, command)
			},
		})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			newSession := tc.newSession
			if newSession == nil {
				newSession = func(t *testing.T) *ChatSession { return &ChatSession{} }
			}

			unified := newSession(t)
			unified.TerminalSession = &ui.TerminalSession{}
			plain := newSession(t)
			if tc.setup != nil {
				tc.setup(unified)
				tc.setup(plain)
			}

			result, handled := tc.run(unified)
			assertBatch3ScreenDocument(t, result, handled, tc.id, tc.markers...)

			plainResult, plainHandled := tc.run(plain)
			if !plainHandled {
				t.Fatalf("plain 出口未接管 %s", tc.name)
			}
			if plainResult.Screen != nil {
				t.Fatalf("plain 出口不应进入副屏：%+v", plainResult.Screen)
			}
			want := strings.TrimRight(ui.RenderDocumentPlain(plainResult.Document()), "\n")
			got := strings.TrimRight(ui.RenderDocumentPlain(result.Screen.Doc), "\n")
			if want == "" || got != want {
				t.Fatalf("副屏正文与 plain 出口不一致\nplain: %q\nscreen: %q", want, got)
			}
		})
	}
}

// assertBatch3ScreenDocument 断言统一出口返回合法 ScreenDocument，且正文非空、
// 包含全部标记。
func assertBatch3ScreenDocument(t *testing.T, result CommandResult, handled bool, id string, markers ...string) {
	t.Helper()
	if !handled {
		t.Fatalf("%s 未被结构化入口接管", id)
	}
	if result.Screen == nil {
		t.Fatalf("期望 ScreenDocument(%s)，got inline=%q", id, ui.RenderDocumentPlain(result.Document()))
	}
	if result.Screen.ID != id || result.Screen.Kind != chatScreenDocument {
		t.Fatalf("spec = %+v，期望 id=%s kind=document", *result.Screen, id)
	}
	if err := result.Screen.validate(); err != nil {
		t.Fatalf("spec 非法：%v", err)
	}
	body := ui.RenderDocumentPlain(result.Screen.Doc)
	if strings.TrimSpace(body) == "" {
		t.Fatalf("%s 副屏正文为空", id)
	}
	for _, marker := range markers {
		if !strings.Contains(body, marker) {
			t.Fatalf("%s 副屏正文缺失 %q：%q", id, marker, body)
		}
	}
}

// TestChatScreenBatch3HotkeysLegacyParity 验证 /hotkeys 迁移后 legacy/plain
// 出口的键位表内容与副屏 Spec 同源（逐行一致）。
func TestChatScreenBatch3HotkeysLegacyParity(t *testing.T) {
	session := &ChatSession{}
	doc := chatScreenHotkeysSpec().Doc
	lines := ui.RenderDocumentPlain(doc)

	for _, marker := range []string{
		"快捷键（当前生效）",
		"固定快捷键（不可重映射）",
		"配置文件: " + chatKeybindingsPath(),
	} {
		if !strings.Contains(lines, marker) {
			t.Fatalf("副屏键位表缺失 %q：%q", marker, lines)
		}
	}

	// plain 出口与副屏正文同源：同一构建函数，仅投影不同。
	plain := ui.RenderDocumentPlain(chatScreenPlainHotkeysDocument(session))
	if plain != lines {
		t.Fatalf("plain 键位表与副屏正文不一致\nplain: %q\nscreen: %q", plain, lines)
	}
}

// chatScreenPlainHotkeysDocument 仅用于让 parity 断言引用与 /hotkeys plain
// 出口相同的文档构建路径（避免测试复制构建逻辑）。
func chatScreenPlainHotkeysDocument(session *ChatSession) render.Document {
	return textLinesDocument(buildChatHotkeysLines(session))
}
