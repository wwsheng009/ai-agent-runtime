package commands

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/foldertrust"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func mentionTestRuntimeConfig() *config.SkillsRuntimeConfig {
	return &config.SkillsRuntimeConfig{
		MentionInjection:     config.SkillMentionInjectionOn,
		ArgumentSubstitution: "on",
	}
}

func mentionTestSkill(name, body, path string) *runtimeskill.Skill {
	item := &runtimeskill.Skill{Name: name, Body: body}
	if strings.TrimSpace(path) != "" {
		item.Source = &runtimeskill.SkillSource{Path: path, Dir: filepath.Dir(path)}
	}
	return item
}

func mentionTestSession(t *testing.T, cfg *config.SkillsRuntimeConfig, skills ...*runtimeskill.Skill) (*ChatSession, *skillsRuntimeBinding) {
	t.Helper()
	if cfg == nil {
		cfg = mentionTestRuntimeConfig()
	}
	binding := &skillsRuntimeBinding{
		skillFunctions:       map[string]*SkillFunction{},
		skillFunctionsByPath: map[string]*SkillFunction{},
	}
	for index, item := range skills {
		if item == nil {
			continue
		}
		functionName := "skill__" + strings.ToLower(strings.ReplaceAll(item.Name, " ", "_"))
		if _, exists := binding.skillFunctions[functionName]; exists {
			functionName = functionName + "__" + string(rune('a'+index))
		}
		fn := &SkillFunction{
			summary:      runtimeskill.SummaryFromSkill(item),
			functionName: functionName,
			skill:        item,
		}
		if item.Source != nil {
			fn.sourcePath = item.Source.Path
		}
		binding.skillFunctions[functionName] = fn
		if path := normalizeSkillMentionPath(fn.sourcePath); path != "" {
			binding.skillFunctionsByPath[path] = fn
		}
	}
	session := &ChatSession{
		SkillsBinding: binding,
		Config:        &config.Config{SkillsRuntime: cfg},
		Model:         "mention-test-model",
		// 默认已信任：避免既有用例受进程级 folder-trust（AICLI_FOLDER_TRUST /
		// TTY 探测）影响；Q12 用例在此之上显式覆盖未信任 / feature off。
		FolderTrust: foldertrust.Resolution{
			FeatureEnabled: true,
			Trusted:        true,
			WorkspaceKey:   "mention-test-workspace",
			ProjectRoot:    `C:\skills`,
			Source:         "store",
		},
	}
	return session, binding
}

func mentionTestNames(mentions []skillMention) []string {
	return skillMentionNames(mentions)
}

func TestCollectSkillMentionNames(t *testing.T) {
	known := map[string]string{
		"alpha":       "",
		"beta-skill":  "",
		"gamma_skill": "",
		"Delta":       "",
	}
	cases := []struct {
		name   string
		prompt string
		want   []string
	}{
		{name: "basic multi", prompt: "use $alpha and $beta-skill", want: []string{"alpha", "beta-skill"}},
		{name: "case insensitive", prompt: "use $ALPHA", want: []string{"alpha"}},
		{name: "hyphen and underscore", prompt: "$gamma_skill + $beta-skill", want: []string{"gamma_skill", "beta-skill"}},
		{name: "duplicate mentions collapse", prompt: "$alpha $alpha", want: []string{"alpha"}},
		{name: "env names ignored", prompt: "$HOME $PATH $PWD $USER $TEMP $UID $SHELL $env $null $_ $PSHOME", want: nil},
		{name: "powershell env form ignored", prompt: "$env:PATH", want: nil},
		{name: "pure digits ignored", prompt: "cost $100 and $1", want: nil},
		{name: "unknown ignored", prompt: "$unknown stays plain", want: nil},
		{name: "unknown prefix token not matched", prompt: "$alpha-x", want: nil},
		{name: "inline code ignored", prompt: "`$alpha` then $beta-skill", want: []string{"beta-skill"}},
		{name: "fenced code ignored", prompt: "```\n$alpha\n$gamma_skill\n```\n$beta-skill", want: []string{"beta-skill"}},
		{name: "fenced language info string", prompt: "```sh\n$alpha\n```\n$Delta", want: []string{"Delta"}},
		{name: "punctuation boundaries", prompt: "($alpha), $gamma_skill!", want: []string{"alpha", "gamma_skill"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mentionTestNames(collectSkillMentionNames(tc.prompt, known))
			if len(got) != len(tc.want) {
				t.Fatalf("mentions = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("mentions = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestResolveMentionedTextSkillsOrderAndCase(t *testing.T) {
	session, _ := mentionTestSession(t, nil,
		mentionTestSkill("zeta", "zeta body", `C:\skills\zeta\SKILL.md`),
		mentionTestSkill("Alpha", "alpha body", `C:\skills\alpha\SKILL.md`),
	)
	selection, diag := resolveMentionedTextSkills(context.Background(), session, []skillMention{
		{Name: "ZETA"},
		{Name: "alpha"},
	}, nil)
	if selection == nil || len(selection.skills) != 2 {
		t.Fatalf("selected = %+v, diag=%+v", selection, diag)
	}
	if selection.skills[0].name != "Alpha" || selection.skills[1].name != "zeta" {
		t.Fatalf("order = %q,%q; want Alpha,zeta", selection.skills[0].name, selection.skills[1].name)
	}
	if selection.skills[0].path != "C:/skills/alpha/SKILL.md" {
		t.Fatalf("path normalization = %q", selection.skills[0].path)
	}
	if len(diag.Skipped) != 0 {
		t.Fatalf("unexpected skipped: %+v", diag.Skipped)
	}
}

func TestResolveMentionedTextSkillsCatalogOrderWins(t *testing.T) {
	candidates := []skillMentionCandidate{
		{fn: &SkillFunction{functionName: "skill__alpha"}, name: "alpha"},
		{fn: &SkillFunction{functionName: "skill__beta"}, name: "beta"},
	}
	sortSkillMentionCandidates(candidates, map[string]int{"skill__beta": 0, "skill__alpha": 1})
	if candidates[0].name != "beta" || candidates[1].name != "alpha" {
		t.Fatalf("catalog order not honored: %+v", candidates)
	}
}

func TestResolveMentionedTextSkillsSkipsAmbiguousDisabledAndNonText(t *testing.T) {
	handlerSkill := mentionTestSkill("handler-skill", "handled", `C:\skills\handler\SKILL.md`)
	handlerSkill.Handler = runtimeskill.SkillHandlerFunc(nil)
	workflowSkill := mentionTestSkill("workflow-skill", "steps", `C:\skills\workflow\SKILL.md`)
	workflowSkill.Workflow = &runtimeskill.Workflow{Steps: []runtimeskill.WorkflowStep{{ID: "1", Name: "step"}}}
	cfg := mentionTestRuntimeConfig()
	cfg.DisabledSkills = []string{"Disabled-Skill"}
	session, _ := mentionTestSession(t, cfg,
		mentionTestSkill("alpha", "alpha body", `C:\skills\alpha-a\SKILL.md`),
		mentionTestSkill("alpha", "alpha duplicate", `C:\skills\alpha-b\SKILL.md`),
		mentionTestSkill("disabled-skill", "nope", `C:\skills\disabled\SKILL.md`),
		handlerSkill,
		workflowSkill,
	)
	_, diag := resolveMentionedTextSkills(context.Background(), session, []skillMention{
		{Name: "alpha"},
		{Name: "disabled-skill"},
		{Name: "handler-skill"},
		{Name: "workflow-skill"},
	}, nil)
	reasons := map[string]string{}
	for _, skipped := range diag.Skipped {
		reasons[skipped.Name] = skipped.Reason
	}
	if reasons["alpha"] != skillMentionSkipAmbiguous {
		t.Fatalf("alpha reason = %q, want ambiguous (%+v)", reasons["alpha"], diag.Skipped)
	}
	if reasons["disabled-skill"] != skillMentionSkipDisabled {
		t.Fatalf("disabled reason = %q, want disabled (%+v)", reasons["disabled-skill"], diag.Skipped)
	}
	if reasons["handler-skill"] != skillMentionSkipDisabled || reasons["workflow-skill"] != skillMentionSkipDisabled {
		t.Fatalf("non-text skills must be skipped as disabled: %+v", diag.Skipped)
	}
}

func TestResolveMentionedTextSkillsDedupesSamePath(t *testing.T) {
	first := mentionTestSkill("alpha", "alpha body", `C:\skills\alpha\SKILL.md`)
	second := mentionTestSkill("alpha", "alpha body copy", `C:\skills\alpha\SKILL.md`)
	session, _ := mentionTestSession(t, nil, first, second)
	selection, diag := resolveMentionedTextSkills(context.Background(), session, []skillMention{{Name: "alpha"}}, nil)
	if len(selection.skills) != 1 {
		t.Fatalf("same-path duplicates must collapse, got %+v (skipped=%+v)", selection.skills, diag.Skipped)
	}
}

func TestResolveMentionedTextSkillsExcludesPinnedSkill(t *testing.T) {
	session, binding := mentionTestSession(t, nil, mentionTestSkill("alpha", "alpha body", `C:\skills\alpha\SKILL.md`))
	var alphaFnName string
	for name := range binding.skillFunctions {
		alphaFnName = name
	}
	selection, diag := resolveMentionedTextSkills(context.Background(), session,
		[]skillMention{{Name: "alpha"}}, map[string]struct{}{alphaFnName: {}})
	if len(selection.skills) != 0 {
		t.Fatalf("pinned skill must not be injected again: %+v", selection.skills)
	}
	if len(diag.Skipped) != 0 {
		t.Fatalf("pin dedupe should be silent, got %+v", diag.Skipped)
	}
}

func TestResolveMentionedTextSkillsLimit(t *testing.T) {
	cfg := mentionTestRuntimeConfig()
	cfg.MentionMultiLimit = 2
	session, _ := mentionTestSession(t, cfg,
		mentionTestSkill("aaa", "a", `C:\skills\aaa\SKILL.md`),
		mentionTestSkill("bbb", "b", `C:\skills\bbb\SKILL.md`),
		mentionTestSkill("ccc", "c", `C:\skills\ccc\SKILL.md`),
	)
	selection, diag := resolveMentionedTextSkills(context.Background(), session, []skillMention{
		{Name: "aaa"}, {Name: "bbb"}, {Name: "ccc"},
	}, nil)
	if len(selection.skills) != 2 {
		t.Fatalf("multi limit not applied: %+v", selection.skills)
	}
	if len(diag.Skipped) != 1 || diag.Skipped[0].Reason != skillMentionSkipLimit || diag.Skipped[0].Name != "ccc" {
		t.Fatalf("limit diagnostic = %+v", diag.Skipped)
	}
	if diag.Notice == "" {
		t.Fatal("limit truncation must produce a summary notice")
	}
}

func TestResolveMentionedTextSkillsPathBoundAndMissing(t *testing.T) {
	session, _ := mentionTestSession(t, nil,
		mentionTestSkill("alpha", "alpha body", `C:\skills\alpha\SKILL.md`),
		mentionTestSkill("alpha", "alpha other", `C:\skills\alpha-other\SKILL.md`),
	)
	selection, diag := resolveMentionedTextSkills(context.Background(), session, []skillMention{
		{Name: "alpha", Path: `C:\skills\alpha\SKILL.md`},
	}, nil)
	if len(selection.skills) != 1 || selection.skills[0].path != "C:/skills/alpha/SKILL.md" {
		t.Fatalf("bound path resolution failed: %+v (skipped=%+v)", selection.skills, diag.Skipped)
	}
	_, diag = resolveMentionedTextSkills(context.Background(), session, []skillMention{
		{Name: "alpha", Path: `C:\skills\missing\SKILL.md`},
	}, nil)
	if len(diag.Skipped) != 1 || diag.Skipped[0].Reason != skillMentionSkipAmbiguous {
		t.Fatalf("failed bound path must block plain fallback: %+v", diag.Skipped)
	}
	_, diag = resolveMentionedTextSkills(context.Background(), session, []skillMention{{Name: "ghost"}}, nil)
	if len(diag.Skipped) != 1 || diag.Skipped[0].Reason != skillMentionSkipReadError {
		t.Fatalf("unloaded name diagnostic = %+v", diag.Skipped)
	}
}

func TestBuildSkillMentionFragmentsGoldenAndQ7(t *testing.T) {
	session, _ := mentionTestSession(t, nil,
		mentionTestSkill("brand", "line1\n$ARGUMENTS", `C:\skills\brand\SKILL.md`),
		mentionTestSkill("plain", "no arguments here", `C:\skills\plain\SKILL.md`),
	)
	selection, _ := resolveMentionedTextSkills(context.Background(), session, []skillMention{
		{Name: "brand"}, {Name: "plain"},
	}, nil)
	messages, diag := buildSkillMentionFragments(session, selection, skillMentionBudget{
		MaxCharsPerSkill: 32768,
		MaxCharsTotal:    65536,
	})
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2 (diag=%+v)", len(messages), diag)
	}
	want := "## Skill: brand\n（未提供显式参数）\n" +
		"<skill name=\"brand\" path=\"C:/skills/brand/SKILL.md\">\nline1\n</skill>"
	if messages[0].Content != want {
		t.Fatalf("fragment mismatch:\n got: %q\nwant: %q", messages[0].Content, want)
	}
	if got := runtimetypes.InstructionScopeOf(messages[0]); got != runtimetypes.InstructionScopeTurn {
		t.Fatalf("scope = %q, want turn", got)
	}
	if got := runtimetypes.InstructionSourceOf(messages[0]); got != runtimetypes.InstructionSourceSkillInstructions {
		t.Fatalf("source = %q, want skill_instructions", got)
	}
	if strings.Contains(messages[1].Content, "（未提供显式参数）") {
		t.Fatalf("Q7 note must only appear for $ARGUMENTS bodies: %q", messages[1].Content)
	}
	if strings.Contains(messages[1].Content, "\\") {
		t.Fatalf("paths must use forward slashes: %q", messages[1].Content)
	}
}

func TestBuildSkillMentionFragmentsBracedArgumentsNote(t *testing.T) {
	session, _ := mentionTestSession(t, nil, mentionTestSkill("braced", "run ${ARGUMENTS} now", `C:\skills\braced\SKILL.md`))
	selection, _ := resolveMentionedTextSkills(context.Background(), session, []skillMention{{Name: "braced"}}, nil)
	messages, _ := buildSkillMentionFragments(session, selection, skillMentionBudget{MaxCharsPerSkill: 32768, MaxCharsTotal: 65536})
	if len(messages) != 1 || !strings.Contains(messages[0].Content, "（未提供显式参数）") {
		t.Fatalf("${ARGUMENTS} must add the Q7 note: %+v", messages)
	}
}

func TestBuildSkillMentionFragmentsTruncationAndTotals(t *testing.T) {
	body := strings.Repeat("x", 200)
	session, _ := mentionTestSession(t, nil,
		mentionTestSkill("aaa", body, `C:\skills\aaa\SKILL.md`),
		mentionTestSkill("bbb", body, `C:\skills\bbb\SKILL.md`),
	)
	selection, _ := resolveMentionedTextSkills(context.Background(), session, []skillMention{
		{Name: "aaa"}, {Name: "bbb"},
	}, nil)

	// 1) 单技能截断：两个都注入，但都带单技能截断标记。
	messages, diag := buildSkillMentionFragments(session, selection, skillMentionBudget{MaxCharsPerSkill: 50, MaxCharsTotal: 100000})
	if len(messages) != 2 || len(diag.Injected) != 2 {
		t.Fatalf("per-skill truncation should keep both fragments: %+v", diag)
	}
	for _, injected := range diag.Injected {
		if !injected.Truncated {
			t.Fatalf("truncated flag missing: %+v", diag.Injected)
		}
	}
	if !strings.Contains(messages[0].Content, "per-skill character limit") {
		t.Fatalf("per-skill marker missing: %q", messages[0].Content)
	}

	// 2) 回合总量：按序保留第一条（截断到余量），第二条丢弃并记 limit。
	messages, diag = buildSkillMentionFragments(session, selection, skillMentionBudget{MaxCharsPerSkill: 32768, MaxCharsTotal: 160})
	if len(messages) != 1 {
		t.Fatalf("total budget should keep exactly one fragment: got %d (diag=%+v)", len(messages), diag)
	}
	if !strings.Contains(messages[0].Content, "per-turn character budget") {
		t.Fatalf("total marker missing: %q", messages[0].Content)
	}
	if len(diag.Skipped) != 1 || diag.Skipped[0].Name != "bbb" || diag.Skipped[0].Reason != skillMentionSkipLimit {
		t.Fatalf("total overflow diagnostic = %+v", diag.Skipped)
	}
}

func TestBuildSkillMentionFragmentsTurnPromptBudgetDegrade(t *testing.T) {
	body := strings.Repeat("y", 400)
	session, _ := mentionTestSession(t, nil, mentionTestSkill("aaa", body, `C:\skills\aaa\SKILL.md`))
	selection, _ := resolveMentionedTextSkills(context.Background(), session, []skillMention{{Name: "aaa"}}, nil)

	// 3) token 余量：截断到预算内（带 turn prompt budget 标记），不触发压缩。
	messages, diag := buildSkillMentionFragments(session, selection, skillMentionBudget{
		MaxCharsPerSkill: 32768,
		MaxCharsTotal:    65536,
		HasTokenBudget:   true,
		AvailableTokens:  40,
	})
	if len(messages) != 1 || len(diag.Injected) != 1 || !diag.Injected[0].Truncated {
		t.Fatalf("token budget should truncate: %+v", diag)
	}
	if !strings.Contains(messages[0].Content, "turn prompt budget") {
		t.Fatalf("budget marker missing: %q", messages[0].Content)
	}

	// 4) 余量过小：全弃（全部记 limit，无注入）。
	messages, diag = buildSkillMentionFragments(session, selection, skillMentionBudget{
		MaxCharsPerSkill: 32768,
		MaxCharsTotal:    65536,
		HasTokenBudget:   true,
		AvailableTokens:  2,
	})
	if len(messages) != 0 {
		t.Fatalf("tiny budget must drop all fragments, got %d", len(messages))
	}
	if len(diag.Skipped) != 1 || diag.Skipped[0].Reason != skillMentionSkipLimit {
		t.Fatalf("all-drop diagnostic = %+v", diag.Skipped)
	}
}

func TestBuildSkillMentionTurnMessagesGating(t *testing.T) {
	off := mentionTestRuntimeConfig()
	off.MentionInjection = config.SkillMentionInjectionOff
	session, _ := mentionTestSession(t, off, mentionTestSkill("alpha", "alpha body", `C:\skills\alpha\SKILL.md`))
	if messages, diag := buildSkillMentionTurnMessages(context.Background(), session, skillMentionTurnInput{
		Prompt: "$alpha", Interactive: true,
	}); len(messages) != 0 || diag != nil {
		t.Fatalf("off must not inspect mentions: messages=%v diag=%+v", messages, diag)
	}

	auto := mentionTestRuntimeConfig()
	auto.MentionInjection = config.SkillMentionInjectionAuto
	session, _ = mentionTestSession(t, auto, mentionTestSkill("alpha", "alpha body", `C:\skills\alpha\SKILL.md`))
	if messages, _ := buildSkillMentionTurnMessages(context.Background(), session, skillMentionTurnInput{
		Prompt: "$alpha", Interactive: false,
	}); len(messages) != 0 {
		t.Fatalf("auto must skip non-interactive turns, got %d fragments", len(messages))
	}
	if messages, _ := buildSkillMentionTurnMessages(context.Background(), session, skillMentionTurnInput{
		Prompt: "$alpha", Interactive: true,
	}); len(messages) != 1 {
		t.Fatalf("auto must inject on interactive turns, got %d fragments", len(messages))
	}

	// on + 系统生成输入：不注入，仅诊断 system_input。
	session, _ = mentionTestSession(t, nil, mentionTestSkill("alpha", "alpha body", `C:\skills\alpha\SKILL.md`))
	messages, diag := buildSkillMentionTurnMessages(context.Background(), session, skillMentionTurnInput{
		Prompt: "$alpha", Interactive: true, SystemGenerated: true,
	})
	if len(messages) != 0 {
		t.Fatalf("system-generated turn must not inject, got %d fragments", len(messages))
	}
	if len(diag.Skipped) != 1 || diag.Skipped[0].Reason != skillMentionSkipSystem {
		t.Fatalf("system input diagnostic = %+v", diag)
	}

	// 未知名/无 mention：零动作。
	if messages, diag := buildSkillMentionTurnMessages(context.Background(), session, skillMentionTurnInput{
		Prompt: "$unknown", Interactive: true,
	}); len(messages) != 0 || diag != nil {
		t.Fatalf("unknown mention must be a no-op: messages=%v diag=%+v", messages, diag)
	}
}

func TestBuildSkillMentionTurnMessagesPinDedupe(t *testing.T) {
	session, binding := mentionTestSession(t, nil,
		mentionTestSkill("alpha", "alpha body", `C:\skills\alpha\SKILL.md`),
		mentionTestSkill("beta", "beta body", `C:\skills\beta\SKILL.md`),
	)
	var alphaFunction string
	for name := range binding.skillFunctions {
		if strings.Contains(name, "alpha") {
			alphaFunction = name
		}
	}
	messages, _ := buildSkillMentionTurnMessages(context.Background(), session, skillMentionTurnInput{
		Prompt:      "$alpha $beta",
		Interactive: true,
		Pin:         &skillTurnPin{PinnedFunctions: []string{alphaFunction}},
	})
	if len(messages) != 1 || !strings.Contains(messages[0].Content, "Skill: beta") {
		t.Fatalf("pin must dedupe alpha and keep beta: %+v", messages)
	}
}

func TestBuildSkillMentionTurnMessagesBudgetDegrade(t *testing.T) {
	session, _ := mentionTestSession(t, nil, mentionTestSkill("alpha", strings.Repeat("z", 400), `C:\skills\alpha\SKILL.md`))
	messages, diag := buildSkillMentionTurnMessages(context.Background(), session, skillMentionTurnInput{
		Prompt:       "$alpha",
		Interactive:  true,
		UsedTokens:   0,
		BudgetTokens: 60,
	})
	if len(messages) != 1 || len(diag.Injected) != 1 || !diag.Injected[0].Truncated {
		t.Fatalf("entry budget degrade should truncate: %+v", diag)
	}
	messages, diag = buildSkillMentionTurnMessages(context.Background(), session, skillMentionTurnInput{
		Prompt:       "$alpha",
		Interactive:  true,
		UsedTokens:   0,
		BudgetTokens: 10,
	})
	if len(messages) != 0 || len(diag.Skipped) != 1 || diag.Skipped[0].Reason != skillMentionSkipLimit {
		t.Fatalf("entry budget all-drop = messages=%d diag=%+v", len(messages), diag)
	}
	if !chatSkillMentionSystemGeneratedPrompt("") || !chatSkillMentionSystemGeneratedPrompt(goalAutoContinuationPrompt) {
		t.Fatal("empty/goal prompts must be treated as system-generated")
	}
}

func FuzzCollectSkillMentionNames(f *testing.F) {
	f.Add("$alpha $beta `$alpha`")
	f.Add("```\n$HOME\n```\n$alpha")
	f.Add("$" + strings.Repeat("a", 4096))
	f.Add("$alpha-$beta_$100")
	f.Fuzz(func(t *testing.T, prompt string) {
		known := map[string]string{"alpha": "", "beta-skill": ""}
		mentions := collectSkillMentionNames(prompt, known)
		for _, mention := range mentions {
			if mention.Name != "alpha" && mention.Name != "beta-skill" {
				t.Fatalf("unexpected mention %q for prompt %q", mention.Name, prompt)
			}
		}
	})
}

// Q12：auto 模式与 folder-trust 联动。mentionTestSession 默认已信任，这里显式
// 覆盖未信任 / feature off，避免依赖进程级 TTY 或环境变量。
func TestBuildSkillMentionTurnMessagesFolderTrustGate(t *testing.T) {
	untrusted := foldertrust.Resolution{
		FeatureEnabled: true,
		Trusted:        false,
		WorkspaceKey:   "mention-test-untrusted",
		ProjectRoot:    `C:\skills`,
		Source:         "headless_deny",
	}

	auto := mentionTestRuntimeConfig()
	auto.MentionInjection = config.SkillMentionInjectionAuto

	// auto + 未信任：不注入，逐提及记 untrusted_project；eligible 仍放行到 build
	// （否则诊断会在调用点被短路掉），二者共用同一门控。
	session, _ := mentionTestSession(t, auto, mentionTestSkill("alpha", "alpha body", `C:\skills\alpha\SKILL.md`))
	session.FolderTrust = untrusted
	messages, diag := buildSkillMentionTurnMessages(context.Background(), session, skillMentionTurnInput{
		Prompt: "$alpha", Interactive: true,
	})
	if len(messages) != 0 {
		t.Fatalf("auto+untrusted must not inject, got %d fragments", len(messages))
	}
	if diag == nil || len(diag.Skipped) != 1 || diag.Skipped[0].Reason != skillMentionSkipUntrustedProject {
		t.Fatalf("auto+untrusted diagnostic = %+v", diag)
	}
	if diag.Skipped[0].Name != "alpha" {
		t.Fatalf("untrusted diagnostic name = %q, want alpha", diag.Skipped[0].Name)
	}
	if !skillMentionTurnEligible(session, "$alpha", true) {
		t.Fatal("auto+untrusted must stay eligible so the diagnostic is produced")
	}

	// auto + 未信任 + 非交互：仍是 auto 的静默短路（不注入、零诊断）。
	if messages, diag := buildSkillMentionTurnMessages(context.Background(), session, skillMentionTurnInput{
		Prompt: "$alpha", Interactive: false,
	}); len(messages) != 0 || diag != nil {
		t.Fatalf("auto+untrusted non-interactive must be a silent no-op: messages=%v diag=%+v", messages, diag)
	}
	if skillMentionTurnEligible(session, "$alpha", false) {
		t.Fatal("auto non-interactive must stay ineligible")
	}

	// on + 未信任：显式覆盖，正常注入。
	on := mentionTestRuntimeConfig() // 默认 on
	session, _ = mentionTestSession(t, on, mentionTestSkill("alpha", "alpha body", `C:\skills\alpha\SKILL.md`))
	session.FolderTrust = untrusted
	messages, diag = buildSkillMentionTurnMessages(context.Background(), session, skillMentionTurnInput{
		Prompt: "$alpha", Interactive: true,
	})
	if len(messages) != 1 || len(diag.Skipped) != 0 {
		t.Fatalf("on+untrusted must inject (explicit override): messages=%d diag=%+v", len(messages), diag)
	}

	// folder-trust feature off：信任检查整体放行，照常注入（含未信任陈旧记录）。
	featureOff := foldertrust.Resolution{
		FeatureEnabled: false,
		Trusted:        false,
		WorkspaceKey:   "mention-test-feature-off",
		Source:         "feature_off",
	}
	session, _ = mentionTestSession(t, auto, mentionTestSkill("alpha", "alpha body", `C:\skills\alpha\SKILL.md`))
	session.FolderTrust = featureOff
	messages, diag = buildSkillMentionTurnMessages(context.Background(), session, skillMentionTurnInput{
		Prompt: "$alpha", Interactive: true,
	})
	if len(messages) != 1 || len(diag.Skipped) != 0 {
		t.Fatalf("feature-off must inject normally: messages=%d diag=%+v", len(messages), diag)
	}

	// off 模式：eligible 与 build 同为静默（零动作）。
	off := mentionTestRuntimeConfig()
	off.MentionInjection = config.SkillMentionInjectionOff
	offSession, _ := mentionTestSession(t, off, mentionTestSkill("alpha", "alpha body", `C:\skills\alpha\SKILL.md`))
	if skillMentionTurnEligible(offSession, "$alpha", true) {
		t.Fatal("off must stay ineligible")
	}
	if messages, diag := buildSkillMentionTurnMessages(context.Background(), offSession, skillMentionTurnInput{
		Prompt: "$alpha", Interactive: true,
	}); len(messages) != 0 || diag != nil {
		t.Fatalf("off must be a silent no-op: messages=%v diag=%+v", messages, diag)
	}
}

// mentionTestMCPRuntime 是 Q11 FindTool 检查的测试替身：available 命中返回工具，
// 否则返回 error。
type mentionTestMCPRuntime struct {
	available map[string]bool
	calls     []string
}

func (m *mentionTestMCPRuntime) FindTool(toolName string) (runtimeskill.ToolInfo, error) {
	m.calls = append(m.calls, toolName)
	if m.available[toolName] {
		return runtimeskill.ToolInfo{Name: toolName}, nil
	}
	return runtimeskill.ToolInfo{}, errors.New("tool is not available in the current runtime surface")
}

func (m *mentionTestMCPRuntime) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	return nil, errors.New("not implemented")
}

func (m *mentionTestMCPRuntime) ListTools() []runtimeskill.ToolInfo { return nil }

// Q11：提及名命中 registry.UnavailableSkills（大小写不敏感）时跳过正文、
// 诊断 dependency_unavailable，并在注入片段之后追加一条依赖提示。
func TestBuildSkillMentionTurnMessagesDependencyUnavailableMention(t *testing.T) {
	restore := skillMentionUnavailableSkills
	skillMentionUnavailableSkills = func(binding *skillsRuntimeBinding) []runtimeskill.UnavailableSkill {
		return []runtimeskill.UnavailableSkill{{
			Name:         "PDF-Tool",
			Path:         `C:\skills\pdf-tool\SKILL.md`,
			MissingTools: []string{"fetch", "openai_image_generate"},
			Reason:       runtimeskill.UnavailableReasonMissingTools,
		}}
	}
	t.Cleanup(func() { skillMentionUnavailableSkills = restore })

	session, _ := mentionTestSession(t, nil, mentionTestSkill("alpha", "alpha body", `C:\skills\alpha\SKILL.md`))
	messages, diag := buildSkillMentionTurnMessages(context.Background(), session, skillMentionTurnInput{
		Prompt: "use $PDF-TOOL and $alpha", Interactive: true,
	})
	if len(messages) != 2 {
		t.Fatalf("want alpha fragment + one dependency notice, got %d (%+v)", len(messages), diag)
	}
	if !strings.Contains(messages[0].Content, "alpha body") {
		t.Fatalf("loaded skill must stay injected first: %q", messages[0].Content)
	}
	notice := messages[1]
	if got := runtimetypes.InstructionScopeOf(notice); got != runtimetypes.InstructionScopeTurn {
		t.Fatalf("notice scope = %q, want turn", got)
	}
	if got := runtimetypes.InstructionSourceOf(notice); got != runtimetypes.InstructionSourceSkillDependencies {
		t.Fatalf("notice source = %q, want skill_dependencies", got)
	}
	if !strings.Contains(notice.Content, "pdf-tool") ||
		!strings.Contains(notice.Content, "openai_image_generate") ||
		!strings.Contains(notice.Content, "fetch") {
		t.Fatalf("notice must list skill and missing tools: %q", notice.Content)
	}
	if !strings.Contains(notice.Content, "本回合不安装") {
		t.Fatalf("notice must state no install this turn: %q", notice.Content)
	}
	if len(diag.Skipped) != 1 || diag.Skipped[0].Reason != skillMentionSkipDependencyUnavailable {
		t.Fatalf("dependency skip diagnostic = %+v", diag.Skipped)
	}
	if !strings.Contains(diag.Skipped[0].Detail, "openai_image_generate") || !strings.Contains(diag.Skipped[0].Detail, "fetch") {
		t.Fatalf("skip detail must carry missing tools: %+v", diag.Skipped[0])
	}
	if !strings.Contains(diag.Notice, "依赖不可用") {
		t.Fatalf("diagnostics notice summary missing: %+v", diag)
	}
	if len(diag.Injected) != 1 || diag.Injected[0].Name != "alpha" {
		t.Fatalf("injected = %+v, want alpha only", diag.Injected)
	}
}

// Q11：已加载技能声明的工具缺失时正文仍注入 + 一条依赖提示；工具全可用或
// mcpRuntime 缺失时不产生提示。
func TestBuildSkillMentionTurnMessagesSelectedSkillToolAvailability(t *testing.T) {
	skillItem := mentionTestSkill("alpha", "alpha body", `C:\skills\alpha\SKILL.md`)
	skillItem.Tools = []string{"tool_ok", "tool_missing", "tool_missing"}
	session, binding := mentionTestSession(t, nil, skillItem)
	binding.mcpRuntime = &mentionTestMCPRuntime{available: map[string]bool{"tool_ok": true}}

	messages, diag := buildSkillMentionTurnMessages(context.Background(), session, skillMentionTurnInput{
		Prompt: "$alpha", Interactive: true,
	})
	if len(messages) != 2 {
		t.Fatalf("want body + dependency notice, got %d (%+v)", len(messages), diag)
	}
	if !strings.Contains(messages[0].Content, "alpha body") {
		t.Fatalf("body must still be injected: %q", messages[0].Content)
	}
	if got := runtimetypes.InstructionScopeOf(messages[1]); got != runtimetypes.InstructionScopeTurn {
		t.Fatalf("notice scope = %q, want turn", got)
	}
	if got := runtimetypes.InstructionSourceOf(messages[1]); got != runtimetypes.InstructionSourceSkillDependencies {
		t.Fatalf("notice source = %q, want skill_dependencies", got)
	}
	if !strings.Contains(messages[1].Content, "tool_missing") || strings.Contains(messages[1].Content, "tool_ok") {
		t.Fatalf("notice must list only missing tools: %q", messages[1].Content)
	}
	if !strings.Contains(diag.Notice, "tool_missing") {
		t.Fatalf("diagnostics notice summary missing: %+v", diag)
	}
	if len(diag.Skipped) != 0 {
		t.Fatalf("missing declared tools must not skip the body: %+v", diag.Skipped)
	}

	// 工具全部可用 → 只注入正文，无提示、无 Notice。
	binding.mcpRuntime = &mentionTestMCPRuntime{available: map[string]bool{"tool_ok": true, "tool_missing": true}}
	messages, diag = buildSkillMentionTurnMessages(context.Background(), session, skillMentionTurnInput{
		Prompt: "$alpha", Interactive: true,
	})
	if len(messages) != 1 || diag.Notice != "" || len(diag.Skipped) != 0 {
		t.Fatalf("available tools must not produce a dependency note: messages=%d diag=%+v", len(messages), diag)
	}

	// mcpRuntime 缺失 → 跳过检查（正文注入、无提示）。
	binding.mcpRuntime = nil
	messages, diag = buildSkillMentionTurnMessages(context.Background(), session, skillMentionTurnInput{
		Prompt: "$alpha", Interactive: true,
	})
	if len(messages) != 1 || diag.Notice != "" {
		t.Fatalf("nil mcpRuntime must skip the check: messages=%d diag=%+v", len(messages), diag)
	}
}

// Q11：多技能各自缺工具聚合为同一条提示（每回合至多一条）。
func TestBuildSkillMentionTurnMessagesDependencyNoticeAggregated(t *testing.T) {
	first := mentionTestSkill("alpha", "alpha body", `C:\skills\alpha\SKILL.md`)
	first.Tools = []string{"missing_a"}
	second := mentionTestSkill("beta", "beta body", `C:\skills\beta\SKILL.md`)
	second.Tools = []string{"missing_b"}
	session, binding := mentionTestSession(t, nil, first, second)
	binding.mcpRuntime = &mentionTestMCPRuntime{available: map[string]bool{}}

	messages, diag := buildSkillMentionTurnMessages(context.Background(), session, skillMentionTurnInput{
		Prompt: "$alpha $beta", Interactive: true,
	})
	if len(messages) != 3 {
		t.Fatalf("want two bodies + one aggregated notice, got %d (%+v)", len(messages), diag)
	}
	notice := messages[2].Content
	if !strings.Contains(notice, "alpha") || !strings.Contains(notice, "missing_a") ||
		!strings.Contains(notice, "beta") || !strings.Contains(notice, "missing_b") {
		t.Fatalf("aggregated notice must mention both skills/tools: %q", notice)
	}
	if strings.Count(notice, "不要调用这些工具") != 1 {
		t.Fatalf("dependency hint must be one aggregated message: %q", notice)
	}
}

// TestResolveMentionedTextSkillsNonInjectableDiagnostics 锁定诊断精度：
// 不可注入的提及必须报 disabled + 精确原因，而不是笼统 read_error。
func TestResolveMentionedTextSkillsNonInjectableDiagnostics(t *testing.T) {
	// 场景 A（远程实测 skill_runtime_smoke）：prompt-only skill，摘要非 document
	// 模式，即便带惰性 resolver 且解析失败 → "not an injectable instruction document"。
	session, binding := mentionTestSession(t, mentionTestRuntimeConfig())
	smokeSummary := &runtimeskill.SkillSummary{Name: "smoke"}
	binding.skillFunctions["skill__smoke"] = &SkillFunction{
		functionName: "skill__smoke",
		summary:      smokeSummary,
		// 与线上构造一致：skillRef = summary.ToSkillStub() 恒非空，
		// resolver 存在但解析失败（skill.yaml 无 SKILL.md 正文）。
		skill: smokeSummary.ToSkillStub(),
		skillResolver: func() (*runtimeskill.Skill, error) {
			return nil, errors.New("no document body")
		},
	}
	_, diag := resolveMentionedTextSkills(context.Background(), session, []skillMention{{Name: "smoke"}}, nil)
	if len(diag.Skipped) != 1 {
		t.Fatalf("skipped = %+v, want one entry", diag.Skipped)
	}
	if diag.Skipped[0].Reason != skillMentionSkipDisabled ||
		!strings.Contains(diag.Skipped[0].Detail, "not an injectable instruction document") {
		t.Fatalf("prompt-only skip = %+v, want disabled/not-an-injectable-document", diag.Skipped[0])
	}

	// 场景 B：结构性 summary-only（无 resolver、无完整定义）保留专属原因。
	stubSession, stubBinding := mentionTestSession(t, mentionTestRuntimeConfig())
	stubBinding.skillFunctions["skill__stub"] = &SkillFunction{functionName: "skill__stub"}
	_, stubDiag := resolveMentionedTextSkills(context.Background(), stubSession, []skillMention{{Name: "stub"}}, nil)
	if len(stubDiag.Skipped) != 1 || stubDiag.Skipped[0].Reason != skillMentionSkipDisabled ||
		!strings.Contains(stubDiag.Skipped[0].Detail, "no injectable instruction body") {
		t.Fatalf("summary-only skip = %+v, want disabled/no-injectable-body", stubDiag.Skipped)
	}
}
