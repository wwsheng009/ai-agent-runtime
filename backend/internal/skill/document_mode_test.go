package skill

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func codexStubSkill() *Skill {
	return &Skill{
		Name:   "demo",
		Source: &SkillSource{Format: SkillSourceFormatCodex, Path: "/repo/.agents/skills/demo/SKILL.md"},
	}
}

func TestIsDocumentMode_ExplicitDocument(t *testing.T) {
	s := &Skill{
		Name:          "demo",
		ExecutionMode: ExecutionModeDocument,
		Source:        &SkillSource{Format: SkillSourceFormatLegacy, Path: "/x/skill.yaml"},
	}
	if !s.IsDocumentMode() {
		t.Fatal("explicit document mode must be document")
	}
}

func TestIsDocumentMode_AutoDetectCodexPromptOnly(t *testing.T) {
	s := codexStubSkill()
	if !s.IsDocumentMode() {
		t.Fatal("Codex skill without handler/workflow should auto-detect as document mode")
	}
}

func TestIsDocumentMode_LegacyIsRuntime(t *testing.T) {
	s := &Skill{
		Name:   "legacy",
		Source: &SkillSource{Format: SkillSourceFormatLegacy, Path: "/x/skill.yaml"},
	}
	if s.IsDocumentMode() {
		t.Fatal("legacy skill must not be document mode")
	}
}

func TestIsDocumentMode_WithWorkflowIsRuntime(t *testing.T) {
	s := codexStubSkill()
	s.Workflow = &Workflow{Steps: []WorkflowStep{{ID: "s1", Tool: "bash"}}}
	if s.IsDocumentMode() {
		t.Fatal("Codex skill with workflow must not be document mode")
	}
}

func TestIsDocumentMode_WithHandlerIsRuntime(t *testing.T) {
	s := codexStubSkill()
	s.Handler = SkillHandlerFunc(func(ctx interface{}, req *types.Request) (*types.Result, error) {
		return nil, nil
	})
	if s.IsDocumentMode() {
		t.Fatal("Codex skill with custom handler must not be document mode")
	}
}

func TestIsDocumentMode_ExplicitModelIsRuntime(t *testing.T) {
	s := codexStubSkill()
	s.ExecutionMode = ExecutionModeModel
	if s.IsDocumentMode() {
		t.Fatal("model mode is not document mode")
	}
}

func TestIsDocumentMode_Nil(t *testing.T) {
	var s *Skill
	if s.IsDocumentMode() {
		t.Fatal("nil skill must not be document mode")
	}
}
