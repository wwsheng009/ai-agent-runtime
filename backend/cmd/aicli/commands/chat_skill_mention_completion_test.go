package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/foldertrust"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// ---------------------------------------------------------------------------
// helpers

// skillMentionCompletionTestSurface 构造可用 surface，并把 UI action 收集到
// 返回的切片里（与 chat_slash_completion_test.go 的 stub 模式一致）。
func skillMentionCompletionTestSurface(t *testing.T) (*ui.FixedBottomSurface, *[]ui.UIAction) {
	t.Helper()
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(80, 24)
	actions := make([]ui.UIAction, 0, 4)
	surface.SetUIActorPoster(func(action ui.UIAction) bool {
		actions = append(actions, action)
		return true
	})
	return surface, &actions
}

func skillMentionCompletionTestController(t *testing.T, cfg *config.SkillsRuntimeConfig, skills ...*runtimeskill.Skill) (*chatSkillMentionCompletionController, *ChatSession, *skillsRuntimeBinding) {
	t.Helper()
	session, binding := mentionTestSession(t, cfg, skills...)
	return newChatSkillMentionCompletionController(session), session, binding
}

func completionCandidateNames(candidates []chatSkillMentionCompletionCandidate) []string {
	names := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		names = append(names, candidate.Name)
	}
	return names
}

func assertCompletionCandidateNames(t *testing.T, candidates []chatSkillMentionCompletionCandidate, want []string) {
	t.Helper()
	got := completionCandidateNames(candidates)
	if len(got) != len(want) {
		t.Fatalf("candidates = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("candidates = %v, want %v", got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// 词法

func TestDetectChatSkillMentionToken(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		text   string
		cursor int
		active bool
		query  string
		start  int
		end    int
	}{
		{name: "line start", text: "$al", cursor: 3, active: true, query: "al", start: 0, end: 3},
		{name: "after space", text: "use $al", cursor: 7, active: true, query: "al", start: 4, end: 7},
		{name: "after paren", text: "($al", cursor: 4, active: true, query: "al", start: 1, end: 4},
		{name: "cursor inside token uses left side only", text: "$alpha", cursor: 4, active: true, query: "alp", start: 0, end: 4},
		{name: "empty query opens all", text: "$", cursor: 1, active: true, query: "", start: 0, end: 1},
		{name: "adjacent name rejected", text: "a$b", cursor: 3, active: false},
		{name: "double dollar rejected", text: "$$", cursor: 2, active: false},
		{name: "env home ignored", text: "$HOME", cursor: 5, active: false},
		{name: "env path ignored", text: "$PATH", cursor: 5, active: false},
		{name: "powershell env form ignored", text: "$env", cursor: 4, active: false},
		{name: "pure digits ignored", text: "$100", cursor: 4, active: false},
		{name: "inline code ignored", text: "`$al`", cursor: 4, active: false},
		{name: "fenced code ignored", text: "```\n$al\n```", cursor: 7, active: false},
		{name: "non name char before cursor rejects token", text: "$alpha!", cursor: 7, active: false},
		{name: "closed fence then token", text: "```\ncode\n```\n$al", cursor: 16, active: true, query: "al", start: 13, end: 16},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := detectChatSkillMentionToken(tc.text, tc.cursor)
			if got.Active != tc.active {
				t.Fatalf("active = %v, want %v (context=%+v)", got.Active, tc.active, got)
			}
			if !tc.active {
				return
			}
			if got.Query != tc.query || got.TokenStart != tc.start || got.TokenEnd != tc.end {
				t.Fatalf("context = %+v, want query=%q range=%d..%d", got, tc.query, tc.start, tc.end)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 候选

func TestSkillMentionCompletionCandidatesTextOnlyAndOrdered(t *testing.T) {
	t.Parallel()

	handler := mentionTestSkill("handler-skill", "handled", `C:\skills\handler\SKILL.md`)
	handler.Handler = runtimeskill.SkillHandlerFunc(nil)
	workflow := mentionTestSkill("workflow-skill", "steps", `C:\skills\workflow\SKILL.md`)
	workflow.Workflow = &runtimeskill.Workflow{Steps: []runtimeskill.WorkflowStep{{ID: "1", Name: "step"}}}
	cfg := mentionTestRuntimeConfig()
	cfg.DisabledSkills = []string{"blocked"}

	controller, _, _ := skillMentionCompletionTestController(t, cfg,
		mentionTestSkill("beta", "beta body", `C:\skills\beta\SKILL.md`),
		mentionTestSkill("Alpha", "alpha body", `C:\skills\alpha\SKILL.md`),
		mentionTestSkill("blocked", "nope", `C:\skills\blocked\SKILL.md`),
		mentionTestSkill("dup", "dup one", `C:\skills\dup-a\SKILL.md`),
		mentionTestSkill("dup", "dup two", `C:\skills\dup-b\SKILL.md`),
		handler,
		workflow,
	)

	controller.UpdateSnapshot(ui.LineEditorSnapshot{Text: "$", Cursor: 1})
	assertCompletionCandidateNames(t, controller.state.Candidates, []string{"Alpha", "beta"})

	controller.UpdateSnapshot(ui.LineEditorSnapshot{Text: "$al", Cursor: 3})
	assertCompletionCandidateNames(t, controller.state.Candidates, []string{"Alpha"})

	controller.UpdateSnapshot(ui.LineEditorSnapshot{Text: "$B", Cursor: 2})
	assertCompletionCandidateNames(t, controller.state.Candidates, []string{"beta"})

	controller.UpdateSnapshot(ui.LineEditorSnapshot{Text: "$zzz", Cursor: 4})
	if !controller.state.Active || len(controller.state.Candidates) != 0 {
		t.Fatalf("unknown query must keep an active no-match popup, got %+v", controller.state)
	}
}

func TestSkillMentionCompletionCandidatesCatalogOrderWins(t *testing.T) {
	t.Parallel()

	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)
	binding := &skillsRuntimeBinding{
		skillFunctions:       map[string]*SkillFunction{},
		skillFunctionsByPath: map[string]*SkillFunction{},
	}
	// 函数名排序与展示名排序刻意相反：Catalog 按函数名（skill__aaa < skill__zzz）
	// 定序，若实现退化成展示名升序会得到 [alpha, zeta]。
	for _, entry := range []struct{ functionName, displayName string }{
		{functionName: "skill__aaa", displayName: "zeta"},
		{functionName: "skill__zzz", displayName: "alpha"},
	} {
		item := mentionTestSkill(entry.displayName, entry.displayName+" body", `C:\skills\`+entry.displayName+`\SKILL.md`)
		fn := &SkillFunction{
			functionName: entry.functionName,
			sourcePath:   item.Source.Path,
			skill:        item,
			summary:      runtimeskill.SummaryFromSkill(item),
		}
		catalog.RegisterSkillFunction(fn)
		binding.skillFunctions[fn.functionName] = fn
		if path := normalizeSkillMentionPath(fn.sourcePath); path != "" {
			binding.skillFunctionsByPath[path] = fn
		}
	}
	catalog.SetSkillsBinding(binding)
	binding.catalog = catalog

	session := &ChatSession{
		SkillsBinding: binding,
		Config:        &config.Config{SkillsRuntime: mentionTestRuntimeConfig()},
		Model:         "skill-mention-completion-test-model",
	}
	controller := newChatSkillMentionCompletionController(session)
	controller.UpdateSnapshot(ui.LineEditorSnapshot{Text: "$", Cursor: 1})
	assertCompletionCandidateNames(t, controller.state.Candidates, []string{"zeta", "alpha"})
}

func TestSkillMentionCompletionCandidatesLimit(t *testing.T) {
	t.Parallel()

	skills := make([]*runtimeskill.Skill, 0, 12)
	for _, name := range []string{"aa", "ab", "ac", "ad", "ae", "af", "ag", "ah", "ai", "aj", "ak", "al"} {
		skills = append(skills, mentionTestSkill(name, name+" body", `C:\skills\`+name+`\SKILL.md`))
	}
	controller, _, _ := skillMentionCompletionTestController(t, nil, skills...)
	controller.UpdateSnapshot(ui.LineEditorSnapshot{Text: "$a", Cursor: 2})
	if len(controller.state.Candidates) != skillMentionCompletionMaxCandidates {
		t.Fatalf("candidates = %d, want capped %d", len(controller.state.Candidates), skillMentionCompletionMaxCandidates)
	}
	if got := controller.state.Candidates[0].Name; got != "aa" {
		t.Fatalf("first candidate = %q, want aa", got)
	}
	if got := controller.state.Candidates[len(controller.state.Candidates)-1].Name; got != "aj" {
		t.Fatalf("last candidate = %q, want aj (sorted prefix window)", got)
	}
}

// ---------------------------------------------------------------------------
// Apply

func TestSkillMentionCompletionApplyUniqueBindsPath(t *testing.T) {
	t.Parallel()

	controller, session, _ := skillMentionCompletionTestController(t, nil,
		mentionTestSkill("alpha", "alpha body", `C:\skills\alpha\SKILL.md`),
		mentionTestSkill("beta", "beta body", `C:\skills\beta\SKILL.md`),
	)
	nextText, nextCursor, ok := controller.ApplyCompletion("$alp", 4)
	if !ok {
		t.Fatal("expected unique match Tab to be handled")
	}
	if nextText != "$alpha " || nextCursor != len([]rune("$alpha ")) {
		t.Fatalf("completion = %q/%d, want $alpha /7", nextText, nextCursor)
	}
	if controller.state.Active {
		t.Fatalf("accepted completion must clear popup state, got %+v", controller.state)
	}
	if got := session.skillMentionBoundPath("alpha"); got != "C:/skills/alpha/SKILL.md" {
		t.Fatalf("bound path = %q, want C:/skills/alpha/SKILL.md", got)
	}
	if got := skillMentionKnownNames(session)["alpha"]; got != "C:/skills/alpha/SKILL.md" {
		t.Fatalf("skillMentionKnownNames[alpha] = %q, want bound path", got)
	}
}

func TestSkillMentionCompletionApplyMultiCandidatePrefixThenAccept(t *testing.T) {
	t.Parallel()

	controller, session, _ := skillMentionCompletionTestController(t, nil,
		mentionTestSkill("alpha", "alpha body", `C:\skills\alpha\SKILL.md`),
		mentionTestSkill("alpine", "alpine body", `C:\skills\alpine\SKILL.md`),
	)
	nextText, nextCursor, ok := controller.ApplyCompletion("$al", 3)
	if !ok {
		t.Fatal("expected multi-candidate prefix extension to be handled")
	}
	if nextText != "$alp" || nextCursor != 4 {
		t.Fatalf("prefix extension = %q/%d, want $alp /4", nextText, nextCursor)
	}
	if got := session.skillMentionBoundPath("alpha"); got != "" {
		t.Fatalf("prefix extension must not bind a path, got %q", got)
	}

	nextText, nextCursor, ok = controller.ApplyCompletion("$alp", 4)
	if !ok {
		t.Fatal("expected accept-selected to be handled")
	}
	if nextText != "$alpha " || nextCursor != len([]rune("$alpha ")) {
		t.Fatalf("accept selected = %q/%d, want $alpha /7", nextText, nextCursor)
	}
	if got := session.skillMentionBoundPath("alpha"); got != "C:/skills/alpha/SKILL.md" {
		t.Fatalf("bound path = %q, want selected candidate path", got)
	}
}

func TestSkillMentionCompletionApplyNoMatchStaysHandled(t *testing.T) {
	t.Parallel()

	controller, _, _ := skillMentionCompletionTestController(t, nil,
		mentionTestSkill("alpha", "alpha body", `C:\skills\alpha\SKILL.md`),
	)
	nextText, nextCursor, ok := controller.ApplyCompletion("$zzz", 4)
	if !ok {
		t.Fatal("zero candidates must consume Tab (handled) without text change")
	}
	if nextText != "$zzz" || nextCursor != 4 {
		t.Fatalf("zero candidates changed text: %q/%d", nextText, nextCursor)
	}
	lines := renderSkillMentionCompletionPopup(controller.state, 80)
	if len(lines) == 0 || !strings.Contains(lines[0], "未找到匹配技能: $zzz") {
		t.Fatalf("no-match popup lines = %#v", lines)
	}

	empty, _ := mentionTestSession(t, nil)
	emptyController := newChatSkillMentionCompletionController(empty)
	emptyController.UpdateSnapshot(ui.LineEditorSnapshot{Text: "$", Cursor: 1})
	lines = renderSkillMentionCompletionPopup(emptyController.state, 80)
	if len(lines) != 1 || lines[0] != "未找到匹配技能" {
		t.Fatalf("empty catalog popup lines = %#v, want bare label", lines)
	}
}

func TestSkillMentionCompletionApplyNoTokenFallsBack(t *testing.T) {
	t.Parallel()

	controller, _, _ := skillMentionCompletionTestController(t, nil,
		mentionTestSkill("alpha", "alpha body", `C:\skills\alpha\SKILL.md`),
	)
	nextText, nextCursor, ok := controller.ApplyCompletion("hello", 5)
	if ok || nextText != "hello" || nextCursor != 5 {
		t.Fatalf("plain text must not be consumed: %q/%d ok=%v", nextText, nextCursor, ok)
	}
	if controller.state.Active {
		t.Fatalf("plain text must not keep popup active: %+v", controller.state)
	}
}

func TestSkillMentionCompletionApplySubmission(t *testing.T) {
	t.Parallel()

	controller, session, _ := skillMentionCompletionTestController(t, nil,
		mentionTestSkill("alpha", "alpha body", `C:\skills\alpha\SKILL.md`),
		mentionTestSkill("alpine", "alpine body", `C:\skills\alpine\SKILL.md`),
	)
	if _, _, ok := controller.ApplySubmission("hello", 5); ok {
		t.Fatal("text without a $ token must leave Enter to the normal submit path")
	}

	controller.UpdateSnapshot(ui.LineEditorSnapshot{Text: "$al", Cursor: 3})
	nextText, nextCursor, ok := controller.ApplySubmission("$al", 3)
	if !ok {
		t.Fatal("active popup must consume Enter as accept-selected")
	}
	if nextText != "$alpha " || nextCursor != len([]rune("$alpha ")) {
		t.Fatalf("submission = %q/%d, want $alpha /7", nextText, nextCursor)
	}
	if got := session.skillMentionBoundPath("alpha"); got != "C:/skills/alpha/SKILL.md" {
		t.Fatalf("submission must record the bound path, got %q", got)
	}

	controller.UpdateSnapshot(ui.LineEditorSnapshot{Text: "$zzz", Cursor: 4})
	if _, _, ok := controller.ApplySubmission("$zzz", 4); ok {
		t.Fatal("zero candidates must leave Enter to the normal submit path")
	}
}

func TestSkillMentionCompletionNavigateAndCancel(t *testing.T) {
	t.Parallel()

	controller, _, _ := skillMentionCompletionTestController(t, nil,
		mentionTestSkill("aa", "aa body", `C:\skills\aa\SKILL.md`),
		mentionTestSkill("ab", "ab body", `C:\skills\ab\SKILL.md`),
		mentionTestSkill("ac", "ac body", `C:\skills\ac\SKILL.md`),
	)
	controller.UpdateSnapshot(ui.LineEditorSnapshot{Text: "$a", Cursor: 2})
	if !controller.state.Active || controller.state.Selected != 0 {
		t.Fatalf("expected active popup with first candidate selected, got %+v", controller.state)
	}
	if !controller.Navigate(1) || controller.state.Selected != 1 {
		t.Fatalf("navigate +1 failed: %+v", controller.state)
	}
	if !controller.Navigate(-1) || controller.state.Selected != 0 {
		t.Fatalf("navigate -1 failed: %+v", controller.state)
	}
	if !controller.Navigate(-1) || controller.state.Selected != 2 {
		t.Fatalf("navigate must wrap backwards: %+v", controller.state)
	}
	if !controller.Cancel() {
		t.Fatal("cancel must close an active popup")
	}
	if controller.Cancel() {
		t.Fatal("second cancel must report no popup")
	}
	if controller.Navigate(1) {
		t.Fatal("navigate after cancel must not be consumed")
	}
}

// ---------------------------------------------------------------------------
// 弹层

func TestSkillMentionCompletionPopupRenderLines(t *testing.T) {
	t.Parallel()

	if lines := renderSkillMentionCompletionPopup(chatSkillMentionCompletionState{}, 80); lines != nil {
		t.Fatalf("inactive state must render nothing, got %#v", lines)
	}

	state := chatSkillMentionCompletionState{
		Active:   true,
		Selected: 1,
		Candidates: []chatSkillMentionCompletionCandidate{
			{Name: "alpha", Description: "Alpha skill"},
			{Name: "beta"},
		},
	}
	lines := renderSkillMentionCompletionPopup(state, 80)
	want := []string{"  $alpha — Alpha skill", "> $beta", skillMentionCompletionKeyHintLine}
	if len(lines) != len(want) {
		t.Fatalf("popup lines = %#v, want %#v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("popup line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestSkillMentionCompletionPopupBlockedAndCleared(t *testing.T) {
	t.Parallel()

	surface, actions := skillMentionCompletionTestSurface(t)
	session, _ := mentionTestSession(t, nil,
		mentionTestSkill("alpha", "alpha body", `C:\skills\alpha\SKILL.md`),
		mentionTestSkill("alpine", "alpine body", `C:\skills\alpine\SKILL.md`),
	)
	session.Surface = surface
	controller := newChatSkillMentionCompletionController(session)

	controller.UpdateSnapshot(ui.LineEditorSnapshot{Text: "$al", Cursor: 3})
	if len(*actions) != 1 {
		t.Fatalf("expected one show action, got %#v", *actions)
	}
	show, ok := (*actions)[0].(ui.ShowPopupAction)
	if !ok {
		t.Fatalf("first action = %T, want ShowPopupAction", (*actions)[0])
	}
	if show.Owner != skillMentionCompletionPopupOwner || !show.BelowPrompt {
		t.Fatalf("show action owner/below = %q/%v", show.Owner, show.BelowPrompt)
	}

	// 签名去重：同一文本重复快照不重发弹层动作。
	controller.UpdateSnapshot(ui.LineEditorSnapshot{Text: "$al", Cursor: 3})
	if len(*actions) != 1 {
		t.Fatalf("unchanged popup re-enqueued actions: %#v", *actions)
	}

	// paste active：清理弹层且不再渲染。
	controller.UpdateSnapshot(ui.LineEditorSnapshot{Text: "$al", Cursor: 3, PasteActive: true})
	if len(*actions) != 2 {
		t.Fatalf("paste-active must clear once, got %#v", *actions)
	}
	if _, ok := (*actions)[1].(ui.ClearPopupAction); !ok {
		t.Fatalf("second action = %T, want ClearPopupAction", (*actions)[1])
	}

	// 恢复后重新渲染，再输入普通文本 → 无 token 清理自身弹层。
	controller.UpdateSnapshot(ui.LineEditorSnapshot{Text: "$al", Cursor: 3})
	controller.UpdateSnapshot(ui.LineEditorSnapshot{Text: "hello", Cursor: 5})
	last := (*actions)[len(*actions)-1]
	if clear, ok := last.(ui.ClearPopupAction); !ok || clear.Owner != skillMentionCompletionPopupOwner {
		t.Fatalf("last action = %#v, want owner ClearPopupAction", last)
	}
}

func TestSkillMentionCompletionPopupBlockedByInteractionAndQueue(t *testing.T) {
	t.Parallel()

	session, _ := mentionTestSession(t, nil,
		mentionTestSkill("alpha", "alpha body", `C:\skills\alpha\SKILL.md`),
	)
	session.Interaction = &chatInteractionCoordinator{promptPasteActive: true}
	controller := newChatSkillMentionCompletionController(session)
	controller.UpdateSnapshot(ui.LineEditorSnapshot{Text: "$al", Cursor: 3})
	if !controller.isPopupBlockedLocked() {
		t.Fatal("prompt paste state must block the skill popup")
	}

	session.Interaction = nil
	session.InputQueue = &chatInputQueue{draftActive: true, draftText: "$al", draftLines: 1}
	if !controller.isPopupBlockedLocked() {
		t.Fatal("queued draft must block the skill popup")
	}
}

// ---------------------------------------------------------------------------
// 门控

func TestShouldEnableSkillMentionCompletion(t *testing.T) {
	t.Parallel()

	if shouldEnableSkillMentionCompletion(nil) {
		t.Fatal("nil session must disable skill mention completion")
	}

	trusted := foldertrust.Resolution{FeatureEnabled: true, Trusted: true, WorkspaceKey: "completion-test"}
	untrusted := foldertrust.Resolution{FeatureEnabled: true, Trusted: false, WorkspaceKey: "completion-test"}
	newSession := func(surface *ui.FixedBottomSurface, mode string, trust foldertrust.Resolution) *ChatSession {
		cfg := &config.SkillsRuntimeConfig{}
		if mode != "" {
			cfg.MentionInjection = mode
		}
		return &ChatSession{
			Surface:       surface,
			Config:        &config.Config{SkillsRuntime: cfg},
			SkillsBinding: &skillsRuntimeBinding{skillFunctions: map[string]*SkillFunction{}},
			FolderTrust:   trust,
		}
	}

	enabledSurface, _ := skillMentionCompletionTestSurface(t)
	disabledSurface := ui.NewFixedBottomSurface(ui.NewTerminal())

	if shouldEnableSkillMentionCompletion(newSession(nil, config.SkillMentionInjectionOn, trusted)) {
		t.Fatal("missing surface must disable completion")
	}
	if shouldEnableSkillMentionCompletion(newSession(disabledSurface, config.SkillMentionInjectionOn, trusted)) {
		t.Fatal("disabled surface must disable completion")
	}
	if shouldEnableSkillMentionCompletion(newSession(enabledSurface, config.SkillMentionInjectionOff, trusted)) {
		t.Fatal("mode off must disable completion")
	}
	if shouldEnableSkillMentionCompletion(newSession(enabledSurface, config.SkillMentionInjectionAuto, untrusted)) {
		t.Fatal("auto mode with untrusted project must disable completion")
	}
	if !shouldEnableSkillMentionCompletion(newSession(enabledSurface, config.SkillMentionInjectionAuto, trusted)) {
		t.Fatal("auto mode with trusted project must enable completion")
	}
	if !shouldEnableSkillMentionCompletion(newSession(enabledSurface, config.SkillMentionInjectionOn, untrusted)) {
		t.Fatal("explicit on must override untrusted project")
	}
}
