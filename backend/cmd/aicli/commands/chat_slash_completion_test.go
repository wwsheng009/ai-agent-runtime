package commands

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

type slashRouteExpectation struct {
	canonical    string
	forms        []string
	acceptsArgs  bool
	requiresArgs bool
	shortcutOf   string
}

func TestDetectSlashCompletionContext(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		text       string
		cursor     int
		active     bool
		query      string
		inArgs     bool
		tokenStart int
		tokenEnd   int
	}{
		{name: "root slash", text: "/", cursor: 1, active: true, query: "/", tokenStart: 0, tokenEnd: 1},
		{name: "prefix query", text: "/m", cursor: 2, active: true, query: "/m", tokenStart: 0, tokenEnd: 2},
		{name: "non command text", text: "hello /m", cursor: 8, active: false},
		{name: "first token args", text: "/model ", cursor: 7, active: false, query: "/model", inArgs: true, tokenStart: 0, tokenEnd: 6},
		{name: "leading spaces before token", text: "  /m", cursor: 1, active: false},
		{name: "cursor inside token", text: "  /m", cursor: 3, active: true, query: "/", tokenStart: 2, tokenEnd: 4},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := detectSlashCompletionContext(tc.text, tc.cursor)
			if got.Active != tc.active {
				t.Fatalf("expected active=%v, got %#v", tc.active, got)
			}
			if got.Query != tc.query {
				t.Fatalf("expected query %q, got %q", tc.query, got.Query)
			}
			if got.InArguments != tc.inArgs {
				t.Fatalf("expected inArgs=%v, got %#v", tc.inArgs, got)
			}
			if tc.active {
				if got.TokenStart != tc.tokenStart || got.TokenEnd != tc.tokenEnd {
					t.Fatalf("expected token range %d..%d, got %#v", tc.tokenStart, tc.tokenEnd, got)
				}
			}
		})
	}
}

func TestMatchSlashCommandCandidates(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		query     string
		want      []string
		wantAlias map[string]string
	}{
		{
			name:  "shared m prefix",
			query: "/m",
			want:  []string{"/model", "/mode"},
		},
		{
			name:  "slash s prefix order",
			query: "/s",
			want:  []string{"/s", "/session", "/sessions", "/status", "/stream", "/skill", "/skills", "/shell"},
		},
		{
			name:  "exact alias outranks prefix",
			query: "/mode",
			want:  []string{"/mode", "/model"},
		},
		{
			name:  "case insensitive",
			query: "/MO",
			want:  []string{"/model", "/mode"},
		},
		{
			name:      "alias exact match",
			query:     "/?",
			want:      []string{"/?"},
			wantAlias: map[string]string{"/?": "/help"},
		},
		{
			name:      "alias q exact match",
			query:     "/q",
			want:      []string{"/q", "/queue", "/quit"},
			wantAlias: map[string]string{"/q": "/exit", "/quit": "/exit"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := matchSlashCommandCandidates(chatSlashCommandCatalog(), tc.query)
			commands := make([]string, 0, len(got))
			for _, candidate := range got {
				commands = append(commands, candidate.Command)
				if wantAlias := tc.wantAlias[candidate.Command]; wantAlias != "" && candidate.AliasOf != wantAlias {
					t.Fatalf("expected %s to be alias of %s, got %#v", candidate.Command, wantAlias, candidate)
				}
			}
			if len(commands) != len(tc.want) {
				t.Fatalf("expected %d candidates, got %d: %#v", len(tc.want), len(commands), commands)
			}
			for i := range tc.want {
				if commands[i] != tc.want[i] {
					t.Fatalf("expected candidate %d to be %q, got %q (full=%#v)", i, tc.want[i], commands[i], commands)
				}
			}
		})
	}
}

func TestLongestCommonSlashPrefix(t *testing.T) {
	t.Parallel()

	candidates := []chatSlashCompletionCandidate{
		{Command: "/mode"},
		{Command: "/model"},
	}
	if got := longestCommonSlashPrefix(candidates); got != "/mode" {
		t.Fatalf("expected common prefix /mode, got %q", got)
	}

	candidates = []chatSlashCompletionCandidate{
		{Command: "/s"},
		{Command: "/session"},
		{Command: "/sessions"},
		{Command: "/stream"},
	}
	if got := longestCommonSlashPrefix(candidates); got != "/s" {
		t.Fatalf("expected common prefix /s, got %q", got)
	}
}

func TestApplySlashCommandCompletion(t *testing.T) {
	t.Parallel()

	text, cursor := applySlashCommandCompletion("/m", 2, 0, 2, "/model", true)
	if text != "/model " {
		t.Fatalf("expected completion to append a trailing space, got %q", text)
	}
	if cursor != len([]rune("/model ")) {
		t.Fatalf("expected cursor to land after inserted space, got %d", cursor)
	}

	text, cursor = applySlashCommandCompletion("  /m hello", 4, 2, 4, "/model", true)
	if text != "  /model hello" {
		t.Fatalf("expected replacement to preserve suffix, got %q", text)
	}
	if cursor != len([]rune("  /model")) {
		t.Fatalf("expected cursor to land at command end when a space already exists, got %d", cursor)
	}

	text, cursor = applySlashCommandCompletion("/he", 3, 0, 3, "/help", false)
	if text != "/help" {
		t.Fatalf("expected replacement without trailing space, got %q", text)
	}
	if cursor != len([]rune("/help")) {
		t.Fatalf("expected cursor to land at command end, got %d", cursor)
	}
}

func TestBuildSlashCompletionStateAndControllerActions(t *testing.T) {
	t.Parallel()

	state := buildSlashCompletionStateWithPrevious("/s", 2, 4, "/stream")
	if !state.Active {
		t.Fatalf("expected slash completion state to be active, got %#v", state)
	}
	if len(state.Candidates) == 0 {
		t.Fatalf("expected candidates for /s, got %#v", state)
	}
	if state.Candidates[state.Selected].Command != "/stream" {
		t.Fatalf("expected previous command to stay selected, got %#v", state)
	}

	controller := newChatSlashCompletionController(&ChatSession{})
	controller.UpdateAt("/s", 2)
	if !controller.state.Active {
		t.Fatalf("expected controller state to be active, got %#v", controller.state)
	}
	if len(controller.state.Candidates) == 0 {
		t.Fatalf("expected controller candidates, got %#v", controller.state)
	}
	if !controller.Navigate(1) {
		t.Fatal("expected navigate to consume popup navigation")
	}
	if controller.state.Selected != 1 {
		t.Fatalf("expected selection to advance, got %#v", controller.state)
	}
	if controller.Navigate(-1) && controller.state.Selected != 0 {
		t.Fatalf("expected selection to wrap back to first candidate, got %#v", controller.state)
	}
	controller.Clear()
	if controller.state.Active || controller.state.Selected != -1 || len(controller.state.Candidates) != 0 {
		t.Fatalf("expected clear to reset controller state, got %#v", controller.state)
	}

	nextText, nextCursor, ok := controller.ApplyCompletion("/sh", 3)
	if !ok {
		t.Fatal("expected /sh tab completion to be accepted")
	}
	if nextText != "/shell " {
		t.Fatalf("expected /sh to complete to /shell with trailing space, got %q", nextText)
	}
	if nextCursor != len([]rune("/shell ")) {
		t.Fatalf("expected cursor after completion space, got %d", nextCursor)
	}
	if controller.state.Active {
		t.Fatalf("expected controller to deactivate after completing into arguments, got %#v", controller.state)
	}

	nextText, nextCursor, ok = controller.ApplySubmission("/help", 5)
	if ok {
		t.Fatal("expected exact command submission to remain unconsumed")
	}
	if nextText != "/help" || nextCursor != 5 {
		t.Fatalf("expected exact command to remain unchanged, got %q/%d", nextText, nextCursor)
	}

	nextText, nextCursor, ok = controller.ApplySubmission("/unknown", len([]rune("/unknown")))
	if ok {
		t.Fatal("expected unknown command submission to remain unconsumed")
	}
	if nextText != "/unknown" || nextCursor != len([]rune("/unknown")) {
		t.Fatalf("expected unknown command to remain unchanged, got %q/%d", nextText, nextCursor)
	}
}

func TestChatSlashCompletionControllerCancel(t *testing.T) {
	t.Parallel()

	controller := newChatSlashCompletionController(&ChatSession{})
	if controller.Cancel() {
		t.Fatal("expected cancel to be ignored when no popup is active")
	}

	controller.UpdateAt("/m", 2)
	if !controller.Cancel() {
		t.Fatal("expected cancel to clear an active popup")
	}
	if controller.state.Active || controller.state.Selected != -1 {
		t.Fatalf("expected cancel to reset controller state, got %#v", controller.state)
	}
}

func TestRenderSlashCommandCompletionPopup(t *testing.T) {
	t.Parallel()

	state := buildSlashCompletionState("/s", len([]rune("/s")), -1)
	lines := renderSlashCommandCompletionPopup(state, 36)
	if len(lines) != 8 {
		t.Fatalf("expected 8 rendered lines, got %d: %#v", len(lines), lines)
	}
	if !strings.HasPrefix(lines[0], "命令补全") {
		t.Fatalf("expected title line, got %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "> /s") {
		t.Fatalf("expected selected exact command on the second line, got %q", lines[1])
	}
	if !strings.Contains(lines[len(lines)-1], "提示") {
		t.Fatalf("expected hint line at the end, got %q", lines[len(lines)-1])
	}
	for i, line := range lines {
		if ui.DisplayWidth(line) > 36 {
			t.Fatalf("line %d exceeds width limit: width=%d line=%q", i, ui.DisplayWidth(line), line)
		}
	}

	empty := renderSlashCommandCompletionPopup(chatSlashCompletionState{
		Active:  true,
		Query:   "/zzz",
		Warning: "未找到匹配命令: /zzz",
	}, 40)
	if len(empty) != 3 {
		t.Fatalf("expected no-match popup to render title, warning, and hint, got %#v", empty)
	}
	if !strings.Contains(empty[1], "未找到匹配命令") {
		t.Fatalf("expected warning line, got %#v", empty)
	}

	rootSlash := renderSlashCommandCompletionPopup(chatSlashCompletionState{
		Active: true,
		Query:  "/",
		Candidates: []chatSlashCompletionCandidate{
			{Command: "/help", Summary: "显示命令帮助", Group: string(chatSlashCommandGroupHelp)},
		},
		Selected: 0,
	}, 80)
	if !strings.Contains(strings.Join(rootSlash, "\n"), "Shell 快捷: !git status") {
		t.Fatalf("expected root slash popup to include shell hint, got %#v", rootSlash)
	}
}

func TestChatSlashCommandCatalogMatchesHandleCommandRoutes(t *testing.T) {
	t.Parallel()

	expectations := []slashRouteExpectation{
		{canonical: "/help", forms: []string{"/help", "/?"}, acceptsArgs: false, requiresArgs: false},
		{canonical: "/exit", forms: []string{"/exit", "/quit", "/q"}, acceptsArgs: false, requiresArgs: false},
		{canonical: "/clear", forms: []string{"/clear", "/cls"}, acceptsArgs: false, requiresArgs: false},
		{canonical: "/new", forms: []string{"/new"}, acceptsArgs: false, requiresArgs: false},
		{canonical: "/session", forms: []string{"/session"}, acceptsArgs: false, requiresArgs: false},
		{canonical: "/status", forms: []string{"/status"}, acceptsArgs: false, requiresArgs: false},
		{canonical: "/debug", forms: []string{"/debug"}, acceptsArgs: true, requiresArgs: false},
		{canonical: "/agents", forms: []string{"/agents"}, acceptsArgs: true, requiresArgs: false},
		{canonical: "/timeline", forms: []string{"/timeline"}, acceptsArgs: true, requiresArgs: false},
		{canonical: "/collab", forms: []string{"/collab"}, acceptsArgs: true, requiresArgs: false},
		{canonical: "/sessions", forms: []string{"/sessions"}, acceptsArgs: true, requiresArgs: false},
		{canonical: "/load", forms: []string{"/load"}, acceptsArgs: true, requiresArgs: true},
		{canonical: "/resume", forms: []string{"/resume"}, acceptsArgs: true, requiresArgs: false},
		{canonical: "/export", forms: []string{"/export"}, acceptsArgs: true, requiresArgs: false},
		{canonical: "/title", forms: []string{"/title"}, acceptsArgs: true, requiresArgs: true},
		{canonical: "/history", forms: []string{"/history", "/h"}, acceptsArgs: false, requiresArgs: false},
		{canonical: "/stream", forms: []string{"/stream"}, acceptsArgs: true, requiresArgs: false},
		{canonical: "/s", forms: []string{"/s"}, acceptsArgs: false, requiresArgs: false, shortcutOf: "/stream"},
		{canonical: "/normal", forms: []string{"/normal", "/n"}, acceptsArgs: false, requiresArgs: false, shortcutOf: "/stream"},
		{canonical: "/model", forms: []string{"/model"}, acceptsArgs: true, requiresArgs: false},
		{canonical: "/login", forms: []string{"/login"}, acceptsArgs: true, requiresArgs: false},
		{canonical: "/compact", forms: []string{"/compact"}, acceptsArgs: true, requiresArgs: false},
		{canonical: "/image", forms: []string{"/image"}, acceptsArgs: true, requiresArgs: false},
		{canonical: "/queue", forms: []string{"/queue"}, acceptsArgs: true, requiresArgs: false},
		{canonical: "/permission-mode", forms: []string{"/permission-mode", "/mode"}, acceptsArgs: true, requiresArgs: false},
		{canonical: "/approval-reuse", forms: []string{"/approval-reuse"}, acceptsArgs: true, requiresArgs: false},
		{canonical: "/yolo", forms: []string{"/yolo"}, acceptsArgs: false, requiresArgs: false},
		{canonical: "/functions", forms: []string{"/functions", "/catalog"}, acceptsArgs: true, requiresArgs: false},
		{canonical: "/function", forms: []string{"/function", "/describe"}, acceptsArgs: true, requiresArgs: true},
		{canonical: "/call", forms: []string{"/call", "/tool"}, acceptsArgs: true, requiresArgs: true},
		{canonical: "/skill", forms: []string{"/skill"}, acceptsArgs: true, requiresArgs: true},
		{canonical: "/skills", forms: []string{"/skills"}, acceptsArgs: true, requiresArgs: false},
		{canonical: "/shell", forms: []string{"/shell", "/cmd"}, acceptsArgs: true, requiresArgs: true},
	}

	sourceRoutes := extractHandleCommandRouteSet(t)
	catalogRoutes := collectSlashCommandRouteSet(chatSlashCommandCatalog())
	if len(sourceRoutes) != len(catalogRoutes) {
		t.Fatalf("expected handleCommand routes to match catalog routes, got source=%d catalog=%d", len(sourceRoutes), len(catalogRoutes))
	}
	for route := range sourceRoutes {
		if _, ok := catalogRoutes[route]; !ok {
			t.Fatalf("handleCommand route %q is missing from catalog", route)
		}
	}

	catalog := chatSlashCommandCatalog()
	index := chatSlashCommandCatalogMap()
	seen := make(map[string]slashRouteExpectation, len(catalog))
	for _, spec := range catalog {
		seen[spec.Name] = slashRouteExpectation{canonical: spec.Name, acceptsArgs: spec.AcceptsArgs, requiresArgs: spec.RequiresArgs, forms: spec.allNames()}
	}

	for _, exp := range expectations {
		spec, ok := index[exp.canonical]
		if !ok {
			t.Fatalf("catalog missing canonical route %q", exp.canonical)
		}
		if spec.AcceptsArgs != exp.acceptsArgs {
			t.Fatalf("expected %s acceptsArgs=%v, got %v", exp.canonical, exp.acceptsArgs, spec.AcceptsArgs)
		}
		if spec.RequiresArgs != exp.requiresArgs {
			t.Fatalf("expected %s requiresArgs=%v, got %v", exp.canonical, exp.requiresArgs, spec.RequiresArgs)
		}
		if spec.ShortcutOf != exp.shortcutOf {
			t.Fatalf("expected %s shortcutOf=%q, got %q", exp.canonical, exp.shortcutOf, spec.ShortcutOf)
		}
		for _, form := range exp.forms {
			got, ok := index[form]
			if !ok {
				t.Fatalf("catalog missing route form %q for %s", form, exp.canonical)
			}
			if got.Name != exp.canonical {
				t.Fatalf("expected route form %q to resolve to %s, got %s", form, exp.canonical, got.Name)
			}
		}
	}

	for _, spec := range catalog {
		for _, name := range spec.allNames() {
			found := false
			for _, exp := range expectations {
				for _, form := range exp.forms {
					if form == name {
						found = true
						break
					}
				}
				if found {
					break
				}
			}
			if !found {
				t.Fatalf("catalog command %s is not covered by handleCommand routes", name)
			}
		}
		if spec.ShortcutOf != "" {
			if _, ok := index[spec.ShortcutOf]; !ok {
				t.Fatalf("shortcut target %q for %s is not present in catalog", spec.ShortcutOf, spec.Name)
			}
		}
	}

	if len(seen) != len(catalog) {
		t.Fatalf("expected one catalog entry per canonical route, got %d canonical routes for %d specs", len(seen), len(catalog))
	}
}

func TestChatSlashCommandCatalogTimelineIncludesFilterArg(t *testing.T) {
	spec, ok := chatSlashCommandCatalogMap()["/timeline"]
	if !ok {
		t.Fatal("catalog missing /timeline")
	}
	if !strings.Contains(spec.Usage, "filter=<text>") {
		t.Fatalf("expected /timeline usage to mention filter, got %q", spec.Usage)
	}
	found := false
	for _, arg := range spec.Args {
		if arg.Token == "filter=<text>" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected /timeline args to include filter token, got %#v", spec.Args)
	}
}

func TestShouldEnableSlashCompletion(t *testing.T) {
	t.Parallel()

	if shouldEnableSlashCompletion(nil) {
		t.Fatal("expected nil session to disable slash completion")
	}
	if shouldEnableSlashCompletion(&ChatSession{}) {
		t.Fatal("expected session without surface to disable slash completion")
	}
}

func TestSlashCompletionCandidateDetailIncludesShortcutNote(t *testing.T) {
	t.Parallel()

	detail := slashCompletionCandidateDetail(chatSlashCompletionCandidate{
		Summary:    "流式开启快捷",
		ShortcutOf: "/stream",
	})
	if !strings.Contains(detail, "/stream") {
		t.Fatalf("expected shortcut detail to mention target command, got %q", detail)
	}
	if !strings.Contains(detail, "快捷命令") {
		t.Fatalf("expected shortcut detail to mention shortcut semantics, got %q", detail)
	}
}

func TestChatSlashCompletionControllerBlocksOnPasteOrQueuedDraft(t *testing.T) {
	t.Parallel()

	controller := newChatSlashCompletionController(&ChatSession{})
	controller.UpdateSnapshot(ui.LineEditorSnapshot{Text: "/m", Cursor: 2, PasteActive: true})
	if !controller.editorPasteActive {
		t.Fatal("expected snapshot update to record paste-active state")
	}
	if !controller.isPopupBlockedLocked() {
		t.Fatal("expected paste-active snapshot to block popup rendering")
	}

	controller.editorPasteActive = false
	controller.session = &ChatSession{
		Interaction: &chatInteractionCoordinator{promptPasteActive: true},
	}
	if !controller.isPopupBlockedLocked() {
		t.Fatal("expected prompt paste state to block popup rendering")
	}

	controller.session = &ChatSession{
		InputQueue: &chatInputQueue{
			draftActive: true,
			draftText:   "/m",
			draftLines:  1,
		},
	}
	if !controller.isPopupBlockedLocked() {
		t.Fatal("expected queued paste draft to block popup rendering")
	}
}

func TestChatSlashArgumentCompletionModelAndResume(t *testing.T) {
	t.Parallel()

	session := newSlashCompletionArgumentTestSession()
	controller := newChatSlashCompletionController(session)

	controller.UpdateAt("/model --provider=", len([]rune("/model --provider=")))
	if !controller.state.Active {
		t.Fatalf("expected model provider args popup to be active, got %#v", controller.state)
	}
	if !controller.state.Context.InArguments {
		t.Fatalf("expected model provider popup to be in argument mode, got %#v", controller.state.Context)
	}
	if !containsSlashCandidate(controller.state.Candidates, "openai") {
		t.Fatalf("expected provider candidates to include openai, got %#v", controller.state.Candidates)
	}

	nextText, nextCursor, ok := controller.ApplyCompletion("/model --provider=o", len([]rune("/model --provider=o")))
	if !ok {
		t.Fatal("expected provider completion to be accepted")
	}
	if nextText != "/model --provider=openai " {
		t.Fatalf("expected provider value to be completed in place, got %q", nextText)
	}
	if nextCursor != len([]rune(nextText)) {
		t.Fatalf("expected cursor to land after appended space, got %d", nextCursor)
	}

	controller.UpdateAt("/model --model=", len([]rune("/model --model=")))
	if !containsSlashCandidate(controller.state.Candidates, "gpt-5.2") {
		t.Fatalf("expected model candidates to include gpt-5.2, got %#v", controller.state.Candidates)
	}
	if !containsSlashCandidate(controller.state.Candidates, "gpt-5.3") {
		t.Fatalf("expected model candidates to include gpt-5.3, got %#v", controller.state.Candidates)
	}

	nextText, nextCursor, ok = controller.ApplySubmission("/model --model=gpt-5.2", len([]rune("/model --model=gpt-5.2")))
	if !ok {
		t.Fatal("expected exact model submission to be consumed as completion")
	}
	if nextText != "/model --model=gpt-5.2 " {
		t.Fatalf("expected exact model value to accept and append a space, got %q", nextText)
	}
	if nextCursor != len([]rune(nextText)) {
		t.Fatalf("expected exact model cursor after appended space, got %d", nextCursor)
	}

	resumeStorage := &countingSessionStorage{InMemoryStorage: runtimechat.NewInMemoryStorage()}
	manager := runtimechat.NewSessionManager(resumeStorage, nil)
	t.Cleanup(func() {
		manager.Stop()
	})

	first := runtimechat.NewSession("tester")
	first.ID = "resume-1"
	first.Metadata.Title = "First session"
	if err := resumeStorage.Save(context.Background(), first); err != nil {
		t.Fatalf("failed to save first resume session: %v", err)
	}
	second := runtimechat.NewSession("tester")
	second.ID = "resume-2"
	second.Metadata.Title = "Second session"
	if err := resumeStorage.Save(context.Background(), second); err != nil {
		t.Fatalf("failed to save second resume session: %v", err)
	}

	resumeSession := &ChatSession{
		SessionManager: manager,
		SessionUserID:  "tester",
	}
	resumeController := newChatSlashCompletionController(resumeSession)
	resumeController.UpdateAt("/resume ", len([]rune("/resume ")))
	if !resumeController.state.Active {
		t.Fatalf("expected resume popup to be active, got %#v", resumeController.state)
	}
	if !containsSlashCandidate(resumeController.state.Candidates, "latest") {
		t.Fatalf("expected resume popup to include latest shortcut, got %#v", resumeController.state.Candidates)
	}
	if candidate := findSlashCandidate(resumeController.state.Candidates, "latest"); candidate == nil || candidate.Summary != "直接恢复最近可恢复会话" {
		t.Fatalf("expected latest shortcut summary to describe resumable sessions, got %#v", candidate)
	}
	if !containsSlashCandidate(resumeController.state.Candidates, "resume-1") || !containsSlashCandidate(resumeController.state.Candidates, "resume-2") {
		t.Fatalf("expected resume popup to include cached session ids, got %#v", resumeController.state.Candidates)
	}
	if got := atomic.LoadInt32(&resumeStorage.listCalls); got != 1 {
		t.Fatalf("expected session list to be loaded once, got %d", got)
	}
	resumeController.UpdateAt("/resume ", len([]rune("/resume ")))
	if got := atomic.LoadInt32(&resumeStorage.listCalls); got != 1 {
		t.Fatalf("expected cached resume candidates to avoid repeated list calls, got %d", got)
	}
}

func TestChatSlashArgumentCompletionAgents(t *testing.T) {
	t.Parallel()

	controller := newChatSlashCompletionController(&ChatSession{})
	controller.UpdateAt("/agents ", len([]rune("/agents ")))
	if !controller.state.Active || !controller.state.Context.InArguments {
		t.Fatalf("expected agents args popup to be active, got %#v", controller.state)
	}
	for _, command := range []string{"panel", "dashboard", "pick", "target", "send", "followup"} {
		if !containsSlashCandidate(controller.state.Candidates, command) {
			t.Fatalf("expected /agents candidates to include %q, got %#v", command, controller.state.Candidates)
		}
	}

	controller.UpdateAt("/agents pa", len([]rune("/agents pa")))
	if !containsSlashCandidate(controller.state.Candidates, "panel") {
		t.Fatalf("expected /agents pa to complete panel, got %#v", controller.state.Candidates)
	}
	nextText, nextCursor, ok := controller.ApplyCompletion("/agents pa", len([]rune("/agents pa")))
	if !ok {
		t.Fatal("expected /agents pa completion to be accepted")
	}
	if nextText != "/agents panel" {
		t.Fatalf("expected panel completion without forced trailing space, got %q", nextText)
	}
	if nextCursor != len([]rune("/agents panel")) {
		t.Fatalf("expected cursor after panel, got %d", nextCursor)
	}

	controller.UpdateAt("/agents panel ", len([]rune("/agents panel ")))
	for _, command := range []string{"follow", "target", "next", "prev"} {
		if !containsSlashCandidate(controller.state.Candidates, command) {
			t.Fatalf("expected /agents panel candidates to include %q, got %#v", command, controller.state.Candidates)
		}
	}

	controller.UpdateAt("/agents target ", len([]rune("/agents target ")))
	for _, command := range []string{"clear", "none"} {
		if !containsSlashCandidate(controller.state.Candidates, command) {
			t.Fatalf("expected /agents target candidates to include %q, got %#v", command, controller.state.Candidates)
		}
	}
}

func TestChatSlashArgumentCompletionFinalValueSubmissionDoesNotConsume(t *testing.T) {
	t.Parallel()

	controller := newChatSlashCompletionController(&ChatSession{})
	controller.UpdateAt("/stream on", len([]rune("/stream on")))
	if !controller.state.Active {
		t.Fatalf("expected stream args popup to be active, got %#v", controller.state)
	}
	nextText, nextCursor, ok := controller.ApplySubmission("/stream on", len([]rune("/stream on")))
	if ok {
		t.Fatal("expected final stream value to submit normally instead of being consumed")
	}
	if nextText != "/stream on" || nextCursor != len([]rune("/stream on")) {
		t.Fatalf("expected exact stream value to remain unchanged, got %q/%d", nextText, nextCursor)
	}
}

func TestDetectSlashCompletionContextArgumentRange(t *testing.T) {
	t.Parallel()

	ctx := detectSlashCompletionContext("/model --provider=", len([]rune("/model --provider=")))
	if ctx.Active {
		t.Fatalf("expected argument cursor to deactivate command-mode popup, got %#v", ctx)
	}
	if !ctx.InArguments {
		t.Fatalf("expected argument cursor to enter argument mode, got %#v", ctx)
	}
	if ctx.Command != "/model" {
		t.Fatalf("expected canonical command to be recorded, got %#v", ctx)
	}
	if ctx.ArgsTokenStart <= ctx.TokenStart {
		t.Fatalf("expected args token start to move inside the command tail, got %#v", ctx)
	}
	if ctx.ArgsTokenEnd != len([]rune("/model --provider=")) {
		t.Fatalf("expected args token end to land at the end of the token, got %#v", ctx)
	}
}

func TestBuildChatSlashHelpLinesUsesCatalog(t *testing.T) {
	t.Parallel()

	lines := buildChatSlashHelpLines()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "!git status --short") {
		t.Fatalf("expected help text to include shell usage example, got %#v", lines)
	}
	for _, spec := range chatSlashCommandCatalog() {
		label := spec.Usage
		if label == "" {
			label = spec.Name
		}
		if len(spec.Aliases) > 0 {
			label += ", " + strings.Join(spec.Aliases, ", ")
		}
		if !strings.Contains(joined, label) {
			t.Fatalf("expected help text to include %q, got %#v", label, lines)
		}
	}
	if !strings.Contains(joined, "恢复最近可恢复会话或弹出恢复菜单") {
		t.Fatalf("expected help text to use resumable-session wording, got %#v", lines)
	}
}

type countingSessionStorage struct {
	*runtimechat.InMemoryStorage
	listCalls int32
}

func (s *countingSessionStorage) List(ctx context.Context, userID string) ([]*runtimechat.Session, error) {
	atomic.AddInt32(&s.listCalls, 1)
	return s.InMemoryStorage.List(ctx, userID)
}

func newSlashCompletionArgumentTestSession() *ChatSession {
	return &ChatSession{
		Config: &config.Config{
			Providers: config.ProvidersConfig{
				DefaultProvider: "openai",
				Items: map[string]config.Provider{
					"anthropic": {
						Enabled:         true,
						DefaultModel:    "claude-3.7",
						SupportedModels: []string{"claude-3.7"},
					},
					"openai": {
						Enabled:         true,
						DefaultModel:    "gpt-5.2",
						SupportedModels: []string{"gpt-5.2", "gpt-5.3"},
					},
				},
			},
		},
		ProviderName: "openai",
		Provider: config.Provider{
			Enabled:         true,
			DefaultModel:    "gpt-5.2",
			SupportedModels: []string{"gpt-5.2", "gpt-5.3"},
		},
		Model: "gpt-5.2",
	}
}

func containsSlashCandidate(candidates []chatSlashCompletionCandidate, command string) bool {
	for _, candidate := range candidates {
		if strings.EqualFold(candidate.Command, command) {
			return true
		}
	}
	return false
}

func findSlashCandidate(candidates []chatSlashCompletionCandidate, command string) *chatSlashCompletionCandidate {
	for i := range candidates {
		if strings.EqualFold(candidates[i].Command, command) {
			return &candidates[i]
		}
	}
	return nil
}

func extractHandleCommandRouteSet(t *testing.T) map[string]struct{} {
	t.Helper()

	_, filePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve test file path")
	}
	commandPath := filepath.Join(filepath.Dir(filePath), "command.go")

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, commandPath, nil, 0)
	if err != nil {
		t.Fatalf("failed to parse command.go: %v", err)
	}

	routes := make(map[string]struct{})
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != "handleCommand" || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.SwitchStmt:
				for _, stmt := range n.Body.List {
					clause, ok := stmt.(*ast.CaseClause)
					if !ok {
						continue
					}
					for _, expr := range clause.List {
						if route, ok := extractSlashRouteLiteral(expr); ok {
							routes[route] = struct{}{}
						}
					}
				}
			case *ast.CallExpr:
				if route, ok := extractSlashRouteFromCall(n); ok {
					routes[route] = struct{}{}
				}
			}
			return true
		})
	}
	return routes
}

func collectSlashCommandRouteSet(specs []chatSlashCommandSpec) map[string]struct{} {
	routes := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		for _, name := range spec.allNames() {
			routes[name] = struct{}{}
		}
	}
	return routes
}

func extractSlashRouteFromCall(call *ast.CallExpr) (string, bool) {
	if call == nil {
		return "", false
	}
	if len(call.Args) < 2 {
		return "", false
	}
	literal, ok := call.Args[1].(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	route, ok := extractSlashRouteLiteral(literal)
	if !ok {
		return "", false
	}
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		x, ok := fun.X.(*ast.Ident)
		if !ok || x.Name != "strings" || fun.Sel == nil || fun.Sel.Name != "HasPrefix" {
			return "", false
		}
	case *ast.Ident:
		if fun.Name != "commandMatches" {
			return "", false
		}
	default:
		return "", false
	}
	return route, true
}

func extractSlashRouteLiteral(expr ast.Expr) (string, bool) {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return "", false
	}
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "/") {
		return "", false
	}
	return value, true
}
