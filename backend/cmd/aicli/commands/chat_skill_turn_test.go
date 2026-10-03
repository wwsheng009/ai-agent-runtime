package commands

import (
	"reflect"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// P3：`/skill` 默认路径回合化的命令层契约。
// 覆盖：默认路径只登记 SendSkillTurn（不渲染命令单元、不直执）；
// `--direct` 保留 legacy 直执；一次性 pin 消费即焚；pin 叠加不污染稳定选择。

func TestExecuteStructuredSkillCommandDefaultsToSkillTurn(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := newTestSkillSession()

	result, handled := executeStructuredSkillCommand(session, "/skill imagegen a cat")
	if !handled {
		t.Fatal("/skill was not handled by the structured executor")
	}
	if result.SendSkillTurn == nil {
		t.Fatalf("default /skill must request a chat turn, got %#v", result)
	}
	if got := strings.TrimSpace(result.SendSkillTurn.SkillName); got != "skill__imagegen" {
		t.Fatalf("SkillName=%q, want skill__imagegen", got)
	}
	if got := strings.TrimSpace(result.SendSkillTurn.Prompt); got != "a cat" {
		t.Fatalf("Prompt=%q, want %q", got, "a cat")
	}
	if got := strings.TrimSpace(result.SendSkillTurn.VisiblePrompt); got != "/skill imagegen a cat" {
		t.Fatalf("VisiblePrompt=%q, want %q", got, "/skill imagegen a cat")
	}
	if len(result.Blocks) != 0 {
		t.Fatalf("turn path must not render a command cell, got %d block(s)", len(result.Blocks))
	}
}

func TestExecuteStructuredSkillCommandDirectKeepsLegacyPath(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := newTestSkillSession()

	result, handled := executeStructuredSkillCommand(session, "/skill --direct imagegen a cat")
	if !handled {
		t.Fatal("--direct /skill was not handled by the structured executor")
	}
	if result.SendSkillTurn != nil {
		t.Fatal("--direct must keep the legacy direct-invoke path, not submit a turn")
	}
	if len(result.Blocks) == 0 {
		t.Fatal("--direct must render one command cell (execution report or error)")
	}
}

func TestStripSkillDirectOption(t *testing.T) {
	cases := []struct {
		in         string
		want       string
		wantDirect bool
	}{
		{"--direct imagegen a cat", "imagegen a cat", true},
		{"--Direct imagegen", "imagegen", true},
		{"imagegen --direct x", "imagegen --direct x", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, direct := stripSkillDirectOption(tc.in)
		if got != tc.want || direct != tc.wantDirect {
			t.Fatalf("stripSkillDirectOption(%q)=(%q,%t), want (%q,%t)", tc.in, got, direct, tc.want, tc.wantDirect)
		}
	}
}

func TestConsumeSkillTurnPinIsOneShot(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := newTestSkillSession()

	if pin := consumeSkillTurnPin(session); pin != nil {
		t.Fatalf("no pending pin must yield nil, got %#v", pin)
	}
	stashPendingSkillTurn(session, &SendSkillTurnRequest{
		SkillName:     "skill__imagegen",
		Prompt:        "a cat",
		VisiblePrompt: "/skill imagegen a cat",
	})
	first := consumeSkillTurnPin(session)
	if first == nil {
		t.Fatal("first consume must materialize the pending pin")
	}
	if !strings.Contains(first.Guide, "Skill program guide") || !strings.Contains(first.Guide, "imagegen") {
		t.Fatalf("guide must carry the ProgramGuide text, got %q", first.Guide)
	}
	if len(first.PinnedTools) == 0 || !strings.EqualFold(first.PinnedTools[0].Name, "skill__imagegen") {
		t.Fatalf("pinned tools must include the skill function, got %#v", first.PinnedTools)
	}
	if second := consumeSkillTurnPin(session); second != nil {
		t.Fatalf("pin must be one-shot, second consume got %#v", second)
	}
}

func TestOverlayPinnedFunctionsKeepsStableSelectionIntact(t *testing.T) {
	stable := &aicliFunctionSelection{
		FinalFunctionNames: []string{"read_file"},
		BuiltinFunctions:   []string{"read_file"},
		Schemas:            []map[string]interface{}{{"name": "read_file", "description": "read"}},
	}
	pin := &skillTurnPin{
		PinnedFunctions: []string{"skill__imagegen"},
		PinnedTools:     []runtimetypes.ToolDefinition{{Name: "skill__imagegen", Description: "Generate images"}},
	}

	overlaid := overlayPinnedFunctions(stable, pin)
	if overlaid == stable {
		t.Fatal("overlay must return a copy, not the stable selection itself")
	}
	if len(stable.FinalFunctionNames) != 1 || stable.FinalFunctionNames[0] != "read_file" {
		t.Fatalf("stable selection was mutated: %v", stable.FinalFunctionNames)
	}
	if len(stable.Schemas) != 1 {
		t.Fatalf("stable schemas were mutated: %v", stable.Schemas)
	}
	if !skillTurnTestContains(overlaid.FinalFunctionNames, "skill__imagegen") {
		t.Fatalf("pinned function missing from overlay: %v", overlaid.FinalFunctionNames)
	}
	if !skillTurnTestContains(overlaid.SkillFunctions, "skill__imagegen") {
		t.Fatalf("pinned skill function must land in SkillFunctions: %v", overlaid.SkillFunctions)
	}
	if len(overlaid.Schemas) != 2 {
		t.Fatalf("overlay schemas=%d, want 2", len(overlaid.Schemas))
	}
	if overlay := overlayPinnedFunctions(stable, nil); overlay != stable {
		t.Fatal("nil pin must return the input selection unchanged")
	}
}

func skillTurnTestContains(list []string, want string) bool {
	for _, item := range list {
		if strings.EqualFold(strings.TrimSpace(item), want) {
			return true
		}
	}
	return false
}

// 非文档模式技能的 guide 不含正文：必须显式引导模型用 skill 函数加载指令，
// 避免它再用文件工具读一遍 SKILL.md（同一正文会在上下文出现两份）。
func TestSkillInstructionLoadHint(t *testing.T) {
	hint := skillInstructionLoadHint("skill__brand-guidelines")
	if !strings.Contains(hint, "skill__brand-guidelines") {
		t.Fatalf("hint must name the skill function, got %q", hint)
	}
	if !strings.Contains(hint, "SKILL.md") {
		t.Fatalf("hint must discourage re-reading SKILL.md, got %q", hint)
	}
	if hint := skillInstructionLoadHint("   "); hint != "" {
		t.Fatalf("blank function name must not produce a hint, got %q", hint)
	}
}

// P1 常驻 catalog（plan §4.5/§5 P1 行 1）：构建会话级抽象指令消息、pin 去重、预算降级。

func newResidentCatalogTurnTestSession(t *testing.T, resident bool, budgetChars int) *ChatSession {
	t.Helper()
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)
	binding := &skillsRuntimeBinding{
		skillFunctions:       map[string]*SkillFunction{},
		skillFunctionsByPath: map[string]*SkillFunction{},
	}
	skills := []*runtimeskill.Skill{
		{
			Name:        "imagegen",
			Description: strings.Repeat("Generate images from a prompt. ", 12),
			Source:      &runtimeskill.SkillSource{Path: "/skills/imagegen/SKILL.md", Dir: "/skills/imagegen", Layer: "repo"},
		},
		{
			Name:        "docx",
			Description: strings.Repeat("Draft and edit document files. ", 12),
			Source:      &runtimeskill.SkillSource{Path: "/skills/docx/SKILL.md", Dir: "/skills/docx", Layer: "repo"},
		},
	}
	for _, item := range skills {
		fn := &SkillFunction{
			functionName: "skill__" + item.Name,
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
	cfg := &config.SkillsRuntimeConfig{CatalogResident: resident, CatalogBudgetChars: budgetChars}
	return &ChatSession{
		FunctionCatalog:  catalog,
		FunctionRegistry: registry,
		SkillsBinding:    binding,
		Config:           &config.Config{SkillsRuntime: cfg},
		Model:            "resident-catalog-test-model",
	}
}

func TestBuildResidentSkillCatalogMessageDisabledReturnsNil(t *testing.T) {
	session := newResidentCatalogTurnTestSession(t, false, 0)
	// 前置：确认 fixture 的目录文本本身非空，nil 确实来自开关而非空目录。
	if body := buildSkillCatalogText(session, session.FunctionCatalog); strings.TrimSpace(body) == "" {
		t.Fatal("fixture catalog body must be non-empty")
	}
	if got := buildResidentSkillCatalogMessage(session); got != nil {
		t.Fatalf("catalog_resident=off must return nil, got %#v", got)
	}
	if got := buildResidentSkillCatalogMessage(nil); got != nil {
		t.Fatalf("nil session must return nil, got %#v", got)
	}
}

func TestBuildResidentSkillCatalogMessageEnabledCarriesSessionScope(t *testing.T) {
	session := newResidentCatalogTurnTestSession(t, true, 0)
	message := buildResidentSkillCatalogMessage(session)
	if message == nil {
		t.Fatal("catalog_resident=on must build a resident message")
	}
	if message.Role != "system" {
		t.Fatalf("canonical instruction placeholder role = %q, want system", message.Role)
	}
	if got := runtimetypes.InstructionScopeOf(*message); got != runtimetypes.InstructionScopeSession {
		t.Fatalf("instruction scope = %q, want %q", got, runtimetypes.InstructionScopeSession)
	}
	if got := runtimetypes.InstructionSourceOf(*message); got != runtimetypes.InstructionSourceSkillsCatalog {
		t.Fatalf("instruction source = %q, want %q", got, runtimetypes.InstructionSourceSkillsCatalog)
	}
	want := strings.TrimSpace(buildSkillCatalogText(session, session.FunctionCatalog))
	if message.Content != want {
		t.Fatalf("resident body must equal buildSkillCatalogText output:\n--- got ---\n%s\n--- want ---\n%s", message.Content, want)
	}
	// 同一输入连续两次构建必须逐字节一致（fingerprint 稳定，不重排/不重复）。
	again := buildResidentSkillCatalogMessage(session)
	if again == nil {
		t.Fatal("second resident build must not be nil")
	}
	if !reflect.DeepEqual(*message, *again) {
		t.Fatalf("consecutive resident builds must be byte-identical:\n--- first ---\n%#v\n--- second ---\n%#v", *message, *again)
	}
}

func TestResolveSkillTurnPinDeduplicatesResidentCatalog(t *testing.T) {
	session := newResidentCatalogTurnTestSession(t, true, 0)
	catalogBody := buildSkillCatalogText(session, session.FunctionCatalog)
	if strings.TrimSpace(catalogBody) == "" {
		t.Fatal("fixture catalog body must be non-empty")
	}
	stashPendingSkillTurn(session, &SendSkillTurnRequest{
		SkillName:     "skill__imagegen",
		Prompt:        "a cat",
		VisiblePrompt: "/skill imagegen a cat",
	})
	pin := consumeSkillTurnPin(session)
	if pin == nil {
		t.Fatal("pin must materialize")
	}
	if strings.Contains(pin.Guide, "### Available skills") || strings.Contains(pin.Guide, catalogBody) {
		t.Fatalf("resident catalog must not be repeated in the pin guide, got %q", pin.Guide)
	}
	if !strings.Contains(pin.Guide, "imagegen") {
		t.Fatalf("pin guide must still carry the ProgramGuide/fallback description, got %q", pin.Guide)
	}
}

func TestResolveSkillTurnPinKeepsCatalogWhenResidentDisabled(t *testing.T) {
	session := newResidentCatalogTurnTestSession(t, false, 0)
	catalogBody := buildSkillCatalogText(session, session.FunctionCatalog)
	if strings.TrimSpace(catalogBody) == "" {
		t.Fatal("fixture catalog body must be non-empty")
	}
	stashPendingSkillTurn(session, &SendSkillTurnRequest{
		SkillName:     "skill__imagegen",
		Prompt:        "a cat",
		VisiblePrompt: "/skill imagegen a cat",
	})
	pin := consumeSkillTurnPin(session)
	if pin == nil {
		t.Fatal("pin must materialize")
	}
	if !strings.Contains(pin.Guide, catalogBody) {
		t.Fatalf("catalog_resident=off must keep the pin-time catalog, got %q", pin.Guide)
	}
}

func TestBuildResidentSkillCatalogMessageBudgetDegradation(t *testing.T) {
	session := newResidentCatalogTurnTestSession(t, true, 100)
	body := buildSkillCatalogText(session, session.FunctionCatalog)
	if strings.TrimSpace(body) == "" {
		t.Fatal("budget-degraded catalog must still render")
	}
	// 预算超限的降级顺序：先截/去描述，技能条目永不消失。
	for _, name := range []string{"imagegen", "docx"} {
		if !strings.Contains(body, name) {
			t.Fatalf("skill %q must never disappear from a degraded catalog:\n%s", name, body)
		}
	}
	if strings.Contains(body, "Generate images from a prompt") || strings.Contains(body, "Draft and edit document files") {
		t.Fatalf("descriptions must degrade under a tiny budget:\n%s", body)
	}
	if message := buildResidentSkillCatalogMessage(session); message == nil || message.Content != strings.TrimSpace(body) {
		t.Fatalf("resident message must carry the degraded catalog body, got %#v", message)
	}
}
