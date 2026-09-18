package commands

import (
	"strings"
	"testing"

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
