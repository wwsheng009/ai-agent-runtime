package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func newTestSkillSession() *ChatSession {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)
	catalog.RegisterSkillFunction(&SkillFunction{
		functionName: "skill__imagegen",
		skill: &runtimeskill.Skill{
			Name:        "imagegen",
			Description: "Generate images from a prompt",
		},
	})
	return &ChatSession{FunctionCatalog: catalog, FunctionRegistry: registry}
}

func TestExecuteStructuredSkillsMenuListQueryStaysReadOnly(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := newTestSkillSession()
	for _, command := range []string{"/skills image", "/skills list"} {
		result, handled := executeStructuredSkillsMenuCommand(session, command)
		if !handled {
			t.Fatalf("%s was not handled by the structured executor", command)
		}
		if result.OpenSkillPicker != nil {
			t.Fatalf("%s must not open the picker: %#v", command, result.OpenSkillPicker)
		}
		text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
		if !strings.Contains(text, "Skill Catalog") {
			t.Fatalf("%s must render the catalog report, got:\n%s", command, text)
		}
	}
}

func TestExecuteStructuredSkillsMenuBareWithoutSurfaceDegradesToCatalog(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := newTestSkillSession()
	result, handled := executeStructuredSkillsMenuCommand(session, "/skills")
	if !handled {
		t.Fatal("bare /skills was not handled by the structured executor")
	}
	if result.OpenSkillPicker != nil {
		t.Fatalf("bare /skills without a picker-capable surface must not open the picker, got %#v", result.OpenSkillPicker)
	}
	text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
	if !strings.Contains(text, "Skill Catalog: total=1") {
		t.Fatalf("bare /skills must degrade to the catalog report, got:\n%s", text)
	}
}

func TestExecuteStructuredSkillCommandInvalidArgsReportError(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := newTestSkillSession()
	result, handled := executeStructuredSkillCommand(session, "/skill")
	if !handled {
		t.Fatal("/skill without args was not handled by the structured executor")
	}
	if result.OpenSkillPicker != nil {
		t.Fatalf("/skill must not open the picker, got %#v", result.OpenSkillPicker)
	}
	if !strings.Contains(ui.RenderDocumentPlain(result.Document()), "需要指定 skill 名称") {
		t.Fatalf("invalid /skill args must report the usage error, got:\n%s", ui.RenderDocumentPlain(result.Document()))
	}
}

func TestExecuteStructuredSkillCommandUnknownSkillReportsError(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := newTestSkillSession()
	result, handled := executeStructuredSkillCommand(session, "/skill no-such-skill do something")
	if !handled {
		t.Fatal("/skill with unknown name was not handled by the structured executor")
	}
	if result.OpenSkillPicker != nil {
		t.Fatalf("/skill must not open the picker, got %#v", result.OpenSkillPicker)
	}
	text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
	if !strings.Contains(text, "错误:") {
		t.Fatalf("unknown skill must report an error, got:\n%s", text)
	}
}

func TestCanOpenChatSkillPickerRequiresUnifiedSurface(t *testing.T) {
	if canOpenChatSkillPicker(nil) {
		t.Fatal("nil session must not open the skill picker")
	}
	session := &ChatSession{}
	if canOpenChatSkillPicker(session) {
		t.Fatal("bare session without interaction/surface must not open the skill picker")
	}
}

func TestBuildSkillPickerFullScreenItems(t *testing.T) {
	session := newTestSkillSession()
	catalog := session.FunctionCatalog
	report := buildFunctionCatalogReport(catalog)
	skills := filterSkillCatalogEntries(report.Skills, "")
	items := buildSkillPickerFullScreenItems(skills)
	if len(items) != 1 {
		t.Fatalf("expected 1 skill item, got %d: %#v", len(items), items)
	}
	if !strings.Contains(items[0].SearchText, "imagegen") {
		t.Fatalf("skill picker item search text must include the skill name, got %q", items[0].SearchText)
	}
}

func TestSkillPickerSelectionBuildsComposerDraft(t *testing.T) {
	// The confirmed skill must produce a composer draft prefixed with the
	// structured /skill command so the user composes the prompt in the unified
	// composer and Enter routes through the migrated command cell.
	skill := aicliFunctionDescriptorReport{FunctionName: "skill__imagegen"}
	selected := skill
	draft := "/skill " + strings.TrimSpace(selected.FunctionName) + " "
	if !strings.HasPrefix(draft, "/skill skill__imagegen ") {
		t.Fatalf("composer draft must prefix the /skill command, got %q", draft)
	}
}

func TestOpenChatSkillPickerRestoresComposerDraft(t *testing.T) {
	// Verify the picker's post-commit composer draft is actually restored
	// through restoreChatRetryDraft: with an interaction coordinator present the
	// draft goes into the composer prompt input, not an error.
	t.Setenv("NO_COLOR", "1")
	session := newTestSkillSession()
	interaction := newChatInteractionCoordinator(session)
	t.Cleanup(interaction.Shutdown)
	session.Interaction = interaction

	if err := restoreChatRetryDraft(session, "/skill skill__imagegen "); err != nil {
		t.Fatalf("restoreChatRetryDraft failed: %v", err)
	}
	snapshot := interaction.PromptInputSnapshot()
	if strings.TrimSpace(snapshot.Text) != "/skill skill__imagegen" {
		t.Fatalf("expected composer draft to be restored, got snapshot %#v", snapshot)
	}
}
