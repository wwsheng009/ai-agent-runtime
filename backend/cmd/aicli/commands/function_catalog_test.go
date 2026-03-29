package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

func TestAICLIFunctionCatalog_TracksBuiltinAndSkillFunctions(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)

	catalog.RegisterBuiltinToolFunction(&testFunction{name: "builtin__diagnose"}, runtimetools.ToolDescriptor{
		Name:        "builtin__diagnose",
		Description: "builtin diagnose",
		Parameters: map[string]interface{}{
			"type": "object",
		},
	})
	catalog.RegisterSkillFunction(&SkillFunction{
		functionName: "skill__alpha",
		skill:        nil,
		schema: map[string]interface{}{
			"name":        "skill__alpha",
			"description": "alpha skill",
			"parameters": map[string]interface{}{
				"type": "object",
			},
		},
	})

	stats := catalog.Stats()
	if stats.TotalFunctions != 2 {
		t.Fatalf("expected 2 functions, got %d", stats.TotalFunctions)
	}
	if stats.BuiltinTools != 1 {
		t.Fatalf("expected 1 builtin tool, got %d", stats.BuiltinTools)
	}
	if stats.SkillFunctions != 1 {
		t.Fatalf("expected 1 skill function, got %d", stats.SkillFunctions)
	}

	builtinSchemas := catalog.BuiltinSchemas()
	if len(builtinSchemas) != 1 {
		t.Fatalf("expected 1 builtin schema, got %v", builtinSchemas)
	}
	if builtinSchemas[0]["name"] != "builtin__diagnose" {
		t.Fatalf("expected builtin__diagnose schema, got %v", builtinSchemas[0]["name"])
	}

	if schema := catalog.SkillSchema("skill__alpha"); schema["name"] != "skill__alpha" {
		t.Fatalf("expected skill__alpha schema, got %v", schema["name"])
	}
}

func TestAICLIFunctionCatalog_SelectRequestFunctions_UnifiesBuiltinAndSkillSelection(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)

	catalog.RegisterBuiltinToolFunction(&testFunction{name: "builtin__diagnose"}, runtimetools.ToolDescriptor{
		Name:        "builtin__diagnose",
		Description: "builtin diagnose",
		Parameters: map[string]interface{}{
			"type": "object",
		},
	})
	skillFn := &SkillFunction{
		functionName: "skill__alpha",
		skill:        &runtimeskill.Skill{Name: "alpha"},
		schema: map[string]interface{}{
			"name":        "skill__alpha",
			"description": "alpha skill",
			"parameters": map[string]interface{}{
				"type": "object",
			},
		},
	}
	catalog.RegisterSkillFunction(skillFn)

	binding := &skillsRuntimeBinding{
		exposureMode: skillExposurePrefer,
		catalog:      catalog,
		skillFunctions: map[string]*SkillFunction{
			"skill__alpha": skillFn,
		},
	}
	catalog.SetSkillsBinding(binding)

	session := &ChatSession{
		FunctionCatalog:  catalog,
		FunctionRegistry: registry,
		SkillsBinding:    binding,
		SkillsMode:       skillExposurePrefer,
	}

	selection, details := catalog.SelectRequestFunctions(session, "please use skill__alpha to handle this request")
	if selection == nil {
		t.Fatal("expected function selection")
	}
	if selection.IncludeBuiltin {
		t.Fatalf("expected builtin tools to be suppressed in prefer mode when skill matches")
	}
	if len(selection.BuiltinFunctions) != 0 {
		t.Fatalf("expected no builtin tools, got %v", selection.BuiltinFunctions)
	}
	if len(selection.SkillFunctions) != 1 || selection.SkillFunctions[0] != "skill__alpha" {
		t.Fatalf("expected skill__alpha, got %v", selection.SkillFunctions)
	}
	if len(selection.FinalFunctionNames) != 1 || selection.FinalFunctionNames[0] != "skill__alpha" {
		t.Fatalf("expected only skill__alpha final exposure, got %v", selection.FinalFunctionNames)
	}
	if len(selection.Schemas) != 1 || selection.Schemas[0]["name"] != "skill__alpha" {
		t.Fatalf("expected skill__alpha schema, got %v", selection.Schemas)
	}
	if details == nil || len(details.ExplicitMentions) != 1 || details.ExplicitMentions[0] != "skill__alpha" {
		t.Fatalf("expected explicit mention details, got %+v", details)
	}
}

func TestFormatSkillExposureDebug_IncludesCatalogAndFinalExposure(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)

	catalog.RegisterBuiltinToolFunction(&testFunction{name: "builtin__diagnose"}, runtimetools.ToolDescriptor{
		Name:        "builtin__diagnose",
		Description: "builtin diagnose",
		Parameters: map[string]interface{}{
			"type": "object",
		},
	})
	skillFn := &SkillFunction{
		functionName: "skill__alpha",
		skill:        &runtimeskill.Skill{Name: "alpha"},
		schema: map[string]interface{}{
			"name":        "skill__alpha",
			"description": "alpha skill",
			"parameters": map[string]interface{}{
				"type": "object",
			},
		},
	}
	catalog.RegisterSkillFunction(skillFn)

	report := buildFunctionExposureReport(catalog, "search alpha data", &aicliFunctionSelection{
		Mode:               skillExposurePrefer,
		IncludeBuiltin:     false,
		BuiltinFunctions:   nil,
		SkillFunctions:     []string{"skill__alpha"},
		FinalFunctionNames: []string{"skill__alpha"},
	}, &skillExposureDetails{
		Mode:             skillExposurePrefer,
		TopK:             1,
		RoutingPrompt:    "search alpha data",
		ExposedFunctions: []string{"skill__alpha"},
	})
	debugOutput := formatSkillExposureDebug(report)

	for _, expected := range []string{
		"[skills-debug] catalog total=2 builtin=1 skills=1",
		"[skills-debug] request mode=prefer include_builtin=false total_exposed=1",
		"[skills-debug] builtin_exposed=<none>",
		"[skills-debug] skill_exposed=skill__alpha",
		"[skills-debug] final_functions=skill__alpha",
		"[skills-debug] route mode=prefer top_k=1",
		"[skills-debug] routed_skills=skill__alpha",
	} {
		if !strings.Contains(debugOutput, expected) {
			t.Fatalf("expected %q in debug output:\n%s", expected, debugOutput)
		}
	}
}

func TestBuildFunctionExposureReport_MergesSelectionAndRoutingDetails(t *testing.T) {
	report := buildFunctionExposureReport(&aicliFunctionCatalog{}, "search alpha data", &aicliFunctionSelection{
		Mode:               skillExposurePrefer,
		IncludeBuiltin:     false,
		BuiltinFunctions:   []string{"builtin__diagnose"},
		SkillFunctions:     []string{"skill__alpha"},
		FinalFunctionNames: []string{"skill__alpha"},
	}, &skillExposureDetails{
		Mode:             skillExposurePrefer,
		TopK:             1,
		RoutingPrompt:    "search alpha data",
		ExplicitMentions: []string{"skill__alpha"},
		PreviouslyCalled: []string{"skill__beta"},
		Candidates: []skillExposureCandidate{
			{FunctionName: "skill__alpha", SkillName: "alpha", Score: 1.0, MatchedBy: "keyword"},
		},
		ExposedFunctions: []string{"skill__alpha"},
	})

	if report == nil {
		t.Fatal("expected exposure report")
	}
	if report.Prompt != "search alpha data" {
		t.Fatalf("unexpected prompt: %s", report.Prompt)
	}
	if report.Mode != skillExposurePrefer {
		t.Fatalf("unexpected mode: %s", report.Mode)
	}
	if report.IncludeBuiltin {
		t.Fatal("expected include_builtin=false")
	}
	if len(report.SkillFunctions) != 1 || report.SkillFunctions[0] != "skill__alpha" {
		t.Fatalf("unexpected skill functions: %v", report.SkillFunctions)
	}
	if len(report.ExplicitMentions) != 1 || report.ExplicitMentions[0] != "skill__alpha" {
		t.Fatalf("unexpected explicit mentions: %v", report.ExplicitMentions)
	}
	if len(report.RoutedSkills) != 1 || report.RoutedSkills[0] != "skill__alpha" {
		t.Fatalf("unexpected routed skills: %v", report.RoutedSkills)
	}
	if len(report.Candidates) != 1 || report.Candidates[0].MatchedBy != "keyword" {
		t.Fatalf("unexpected candidates: %+v", report.Candidates)
	}
}

func TestAICLIFunctionCatalog_RespectsToolPolicyForExposureAndExecution(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)

	catalog.RegisterBuiltinToolFunction(&testFunction{name: "read_file"}, runtimetools.ToolDescriptor{
		Name:        "read_file",
		Description: "read file",
		Parameters:  map[string]interface{}{"type": "object"},
	})
	catalog.RegisterBuiltinToolFunction(&testFunction{name: "write_file"}, runtimetools.ToolDescriptor{
		Name:        "write_file",
		Description: "write file",
		Parameters:  map[string]interface{}{"type": "object"},
	})

	policy := runtimepolicy.NewToolExecutionPolicy([]string{"read_file"}, false)
	catalog.SetToolPolicy(policy)

	session := &ChatSession{
		FunctionCatalog:  catalog,
		FunctionRegistry: registry,
		ToolPolicy:       policy,
	}

	selection, _ := catalog.SelectRequestFunctions(session, "inspect files")
	if selection == nil {
		t.Fatal("expected function selection")
	}
	if len(selection.BuiltinFunctions) != 1 || selection.BuiltinFunctions[0] != "read_file" {
		t.Fatalf("expected only read_file exposure, got %v", selection.BuiltinFunctions)
	}

	if _, err := catalog.ExecuteFunction(context.Background(), "write_file", map[string]interface{}{"path": "foo.txt"}); err == nil {
		t.Fatal("expected write_file execution to be blocked by tool policy")
	}
}
