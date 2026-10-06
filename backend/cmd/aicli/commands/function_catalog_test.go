package commands

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolnames"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

type richTestFunction struct {
	testFunction
	metadata map[string]interface{}
}

func (f *richTestFunction) ExecuteWithMeta(ctx context.Context, args map[string]interface{}) (string, map[string]interface{}, error) {
	output, err := f.Execute(ctx, args)
	return output, cloneFunctionSchema(f.metadata), err
}

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

func TestAICLIFunctionCatalog_SelectRequestFunctions_KeepsGoalToolsInvariantWithSkillPrefer(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)
	registerGoalFunctions(&ChatSession{FunctionCatalog: catalog, FunctionRegistry: registry})

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

	selection, _ := catalog.SelectRequestFunctions(session, "please use skill__alpha to handle this request")
	if selection == nil {
		t.Fatal("expected function selection")
	}
	if !selectionContainsFunction(selection, getGoalFunctionName) || !selectionContainsFunction(selection, updateGoalFunctionName) {
		t.Fatalf("expected goal tools to remain in prefer-mode request tools, got %v", selection.FinalFunctionNames)
	}
	if selectionContainsFunction(selection, "builtin__diagnose") {
		t.Fatalf("expected non-invariant builtin to remain suppressed, got %v", selection.FinalFunctionNames)
	}
}

func TestAICLIFunctionCatalog_SelectStableSessionFunctions_UsesFixedSuperset(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)
	session := &ChatSession{
		FunctionCatalog:  catalog,
		FunctionRegistry: registry,
		SkillsMode:       skillExposurePrefer,
	}
	registerGoalFunctions(session)

	catalog.RegisterBuiltinToolFunction(&testFunction{name: "builtin__diagnose"}, runtimetools.ToolDescriptor{
		Name:        "builtin__diagnose",
		Description: "builtin diagnose",
		Parameters:  map[string]interface{}{"type": "object"},
	})
	catalog.RegisterBuiltinToolFunction(&testFunction{name: toolnames.OpenAIImageGenerateToolName}, runtimetools.ToolDescriptor{
		Name:        toolnames.OpenAIImageGenerateToolName,
		Description: "generate image via /v1/images/generations",
		Parameters:  map[string]interface{}{"type": "object"},
	})
	skillFn := &SkillFunction{
		functionName: "skill__alpha",
		skill:        &runtimeskill.Skill{Name: "alpha"},
		schema: map[string]interface{}{
			"name":        "skill__alpha",
			"description": "alpha skill",
			"parameters":  map[string]interface{}{"type": "object"},
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
	session.SkillsBinding = binding

	selection := catalog.SelectStableSessionFunctions(session)
	if selection == nil {
		t.Fatal("expected stable function selection")
	}
	for _, name := range []string{
		"builtin__diagnose",
		"skill__alpha",
		toolnames.OpenAIImageGenerateToolName,
		getGoalFunctionName,
		updateGoalFunctionName,
	} {
		if !selectionContainsFunction(selection, name) {
			t.Fatalf("expected stable selection to include %s, got %v", name, selection.FinalFunctionNames)
		}
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

func TestAICLIFunctionCatalog_ExecuteFunctionWithMeta_PreservesMetadata(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)

	catalog.RegisterBuiltinToolFunction(&richTestFunction{
		testFunction: testFunction{name: "background_task"},
		metadata: map[string]interface{}{
			toolresult.SourceKey:   toolresult.SourceBroker,
			toolresult.MetadataKey: toolresult.KindText,
		},
	}, runtimetools.ToolDescriptor{
		Name:        "background_task",
		Description: "background task",
		Parameters:  map[string]interface{}{"type": "object"},
	})

	output, metadata, err := catalog.ExecuteFunctionWithMeta(context.Background(), "background_task", map[string]interface{}{"command": "git status"})
	if err != nil {
		t.Fatalf("ExecuteFunctionWithMeta failed: %v", err)
	}
	if output != "ok" {
		t.Fatalf("expected output ok, got %q", output)
	}
	if got := metadata[toolresult.SourceKey]; got != toolresult.SourceBroker {
		t.Fatalf("expected %s=%q, got %#v", toolresult.SourceKey, toolresult.SourceBroker, got)
	}
	if got := metadata[toolresult.MetadataKey]; got != toolresult.KindText {
		t.Fatalf("expected %s=%q, got %#v", toolresult.MetadataKey, toolresult.KindText, got)
	}
}

func TestAICLIFunctionCatalog_SelectRequestFunctions_HidesOpenAIImageGenerateWithoutImageIntent(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)
	catalog.RegisterBuiltinToolFunction(&testFunction{name: toolnames.OpenAIImageGenerateToolName}, runtimetools.ToolDescriptor{
		Name:        toolnames.OpenAIImageGenerateToolName,
		Description: "generate image via /v1/images/generations",
		Parameters:  map[string]interface{}{"type": "object"},
	})

	session := &ChatSession{
		FunctionCatalog:  catalog,
		FunctionRegistry: registry,
	}

	selection, _ := catalog.SelectRequestFunctions(session, "inspect config and explain startup")
	if selection == nil {
		t.Fatal("expected function selection")
	}
	if selectionContainsFunction(selection, toolnames.OpenAIImageGenerateToolName) {
		t.Fatalf("did not expect %s to be exposed for non-image prompt: %+v", toolnames.OpenAIImageGenerateToolName, selection.FinalFunctionNames)
	}
}

func TestAICLIFunctionCatalog_SelectRequestFunctions_ExposesOpenAIImageGenerateForImageIntent(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)
	catalog.RegisterBuiltinToolFunction(&testFunction{name: toolnames.OpenAIImageGenerateToolName}, runtimetools.ToolDescriptor{
		Name:        toolnames.OpenAIImageGenerateToolName,
		Description: "generate image via /v1/images/generations",
		Parameters:  map[string]interface{}{"type": "object"},
	})

	session := &ChatSession{
		FunctionCatalog:  catalog,
		FunctionRegistry: registry,
	}

	selection, _ := catalog.SelectRequestFunctions(session, "帮我生成一个美女图片")
	if selection == nil {
		t.Fatal("expected function selection")
	}
	if !selectionContainsFunction(selection, toolnames.OpenAIImageGenerateToolName) {
		t.Fatalf("expected %s to be exposed for image prompt: %+v", toolnames.OpenAIImageGenerateToolName, selection.FinalFunctionNames)
	}
}

func TestAICLIFunctionCatalog_SelectRequestFunctions_HidesOpenAIImageGenerateWhenCodexNativeImageAvailable(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("codex", registry)
	catalog.RegisterBuiltinToolFunction(&testFunction{name: toolnames.OpenAIImageGenerateToolName}, runtimetools.ToolDescriptor{
		Name:        toolnames.OpenAIImageGenerateToolName,
		Description: "generate image via /v1/images/generations",
		Parameters:  map[string]interface{}{"type": "object"},
	})

	enabledImageGeneration := true
	session := &ChatSession{
		FunctionCatalog:  catalog,
		FunctionRegistry: registry,
		Model:            "gpt-5.4",
		Provider: config.Provider{
			Protocol:              "codex",
			EnableImageGeneration: &enabledImageGeneration,
			ModelCapabilities: map[string]config.ModelCapabilitySpec{
				"gpt-5.4": {
					InputModalities: []string{"text", "image"},
					NativeTools: config.NativeToolCapabilities{
						ImageGeneration: true,
					},
				},
			},
		},
	}

	selection, _ := catalog.SelectRequestFunctions(session, "帮我生成一个美女图片")
	if selection == nil {
		t.Fatal("expected function selection")
	}
	if selectionContainsFunction(selection, toolnames.OpenAIImageGenerateToolName) {
		t.Fatalf("did not expect %s when codex native image tool is available: %+v", toolnames.OpenAIImageGenerateToolName, selection.FinalFunctionNames)
	}
}

// The CLI /call surface resolves relative path arguments against the session
// workspace root (resolveLocalWorkspacePath), while the static policy used to
// resolve them against the process working directory. A relative argument could
// therefore pass the sandbox check and still escape the bound project
// directory, so the catalog now binds the resolver to the policy context.
func TestAICLIFunctionCatalog_ResolvesRelativePathsAgainstWorkspaceRoot(t *testing.T) {
	workspaceRoot := t.TempDir()

	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)
	catalog.RegisterBuiltinToolFunction(&testFunction{name: "grep"}, runtimetools.ToolDescriptor{
		Name:        "grep",
		Description: "search files",
		Parameters:  map[string]interface{}{"type": "object"},
	})

	policy := runtimepolicy.NewToolExecutionPolicy(nil, false)
	policy.Sandbox = runtimeexecutor.NewSandbox(&runtimeexecutor.SandboxConfig{
		Enabled:      true,
		AllowedPaths: []string{workspaceRoot},
	})
	catalog.SetToolPolicy(policy)
	catalog.workspaceRootResolver = func() string { return "  " + workspaceRoot + "  " }

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if _, _, err := catalog.ExecuteFunctionWithMeta(context.Background(), "grep", map[string]interface{}{"path": outside}); err == nil {
		t.Fatal("expected absolute path outside the sandbox to be blocked")
	}

	// No resolver (embedders, tests) keeps the historical process-cwd behavior.
	catalog.workspaceRootResolver = nil
	relative := map[string]interface{}{"path": filepath.Join("sub", "inside.txt")}
	withoutResolver, _, cwdErr := catalog.ExecuteFunctionWithMeta(context.Background(), "grep", relative)
	if cwdErr == nil || withoutResolver != "" {
		t.Fatalf("expected the cwd-resolved call to be denied by the sandbox, got output %q err %v", withoutResolver, cwdErr)
	}
	catalog.workspaceRootResolver = func() string { return workspaceRoot }

	output, _, err := catalog.ExecuteFunctionWithMeta(context.Background(), "grep", relative)
	if err != nil {
		t.Fatalf("expected relative path inside the workspace root to pass the policy, got %v", err)
	}
	if output != "ok" {
		t.Fatalf("expected the tool to run after the policy allowed it, got %q", output)
	}
}

// skillMentionHideTestSession 构造 P3 函数面收敛用例的会话：技能面含文本类
// （alpha）、handler（bravo）、workflow（charlie）三种形态，配置开关与交互形态
// 由调用方指定（hideText=nil 表示默认 on）。
func skillMentionHideTestSession(t *testing.T, hideText *bool, noInteractive bool) *ChatSession {
	t.Helper()
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)

	register := func(name string, skillDef *runtimeskill.Skill) *SkillFunction {
		fn := &SkillFunction{
			functionName: name,
			skill:        skillDef,
			schema: map[string]interface{}{
				"name":        name,
				"description": name + " skill",
				"parameters":  map[string]interface{}{"type": "object"},
			},
		}
		catalog.RegisterSkillFunction(fn)
		return fn
	}
	textFn := register("skill__alpha", &runtimeskill.Skill{Name: "alpha"})
	handlerFn := register("skill__bravo", &runtimeskill.Skill{
		Name:    "bravo",
		Handler: runtimeskill.SkillHandlerFunc(nil),
	})
	workflowFn := register("skill__charlie", &runtimeskill.Skill{
		Name:     "charlie",
		Workflow: &runtimeskill.Workflow{Steps: []runtimeskill.WorkflowStep{{ID: "step-1", Name: "step"}}},
	})

	binding := &skillsRuntimeBinding{
		exposureMode: skillExposurePrefer,
		catalog:      catalog,
		skillFunctions: map[string]*SkillFunction{
			"skill__alpha":   textFn,
			"skill__bravo":   handlerFn,
			"skill__charlie": workflowFn,
		},
	}
	catalog.SetSkillsBinding(binding)
	return &ChatSession{
		FunctionCatalog:  catalog,
		FunctionRegistry: registry,
		SkillsBinding:    binding,
		SkillsMode:       skillExposurePrefer,
		Config: &config.Config{
			SkillsRuntime: &config.SkillsRuntimeConfig{MentionHideTextSkillFunctions: hideText},
		},
		NoInteractive: noInteractive,
	}
}

func TestAICLIFunctionCatalog_SelectRequestFunctions_HidesTextSkillFunctionsInInteractiveTurn(t *testing.T) {
	session := skillMentionHideTestSession(t, nil, false)
	catalog := session.FunctionCatalog

	selection, details := catalog.SelectRequestFunctions(session, "please use skill__alpha skill__bravo skill__charlie to handle this request")
	if selection == nil {
		t.Fatal("expected function selection")
	}
	if selectionContainsFunction(selection, "skill__alpha") {
		t.Fatalf("expected text skill hidden from interactive request surface, got %v", selection.FinalFunctionNames)
	}
	if !selectionContainsFunction(selection, "skill__bravo") || !selectionContainsFunction(selection, "skill__charlie") {
		t.Fatalf("expected handler/workflow skills to stay exposed, got %v", selection.FinalFunctionNames)
	}
	for _, name := range selection.SkillFunctions {
		if name == "skill__alpha" {
			t.Fatalf("SkillFunctions must stay consistent with hidden text skill, got %v", selection.SkillFunctions)
		}
	}
	for _, schema := range selection.Schemas {
		if schema["name"] == "skill__alpha" {
			t.Fatalf("Schemas must stay consistent with hidden text skill, got %v", selection.Schemas)
		}
	}
	if details == nil || !stringSliceContains(details.ExplicitMentions, "skill__alpha") {
		t.Fatalf("expected lexical explicit mention retained for diagnostics, got %+v", details)
	}
	// /call 与 /skill --direct 走注册表而非请求选择面：隐藏后函数仍须可解析。
	if catalog.SkillSchema("skill__alpha") == nil {
		t.Fatal("hidden text skill must stay registered for /call and /skill --direct")
	}

	stable := catalog.SelectStableSessionFunctions(session)
	if stable == nil {
		t.Fatal("expected stable function selection")
	}
	if selectionContainsFunction(stable, "skill__alpha") {
		t.Fatalf("expected text skill hidden from stable surface, got %v", stable.FinalFunctionNames)
	}
	if !selectionContainsFunction(stable, "skill__bravo") || !selectionContainsFunction(stable, "skill__charlie") {
		t.Fatalf("expected handler/workflow skills in stable surface, got %v", stable.FinalFunctionNames)
	}
}

func TestAICLIFunctionCatalog_SelectRequestFunctions_KeepsTextSkillFunctionWhenHideDisabledOrHeadless(t *testing.T) {
	disabled := false
	cases := []struct {
		name          string
		hideText      *bool
		noInteractive bool
		jsonOutput    bool
	}{
		{name: "explicit disable", hideText: &disabled},
		{name: "headless", noInteractive: true},
		{name: "json output", jsonOutput: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := skillMentionHideTestSession(t, tc.hideText, tc.noInteractive)
			session.JSONOutput = tc.jsonOutput

			selection, _ := session.FunctionCatalog.SelectRequestFunctions(session, "please use skill__alpha to handle this request")
			if selection == nil {
				t.Fatal("expected function selection")
			}
			if !selectionContainsFunction(selection, "skill__alpha") {
				t.Fatalf("expected text skill exposed when hide gate is off, got %v", selection.FinalFunctionNames)
			}
			stable := session.FunctionCatalog.SelectStableSessionFunctions(session)
			if stable == nil {
				t.Fatal("expected stable function selection")
			}
			if !selectionContainsFunction(stable, "skill__alpha") {
				t.Fatalf("expected text skill exposed in stable surface when hide gate is off, got %v", stable.FinalFunctionNames)
			}
		})
	}
}

func TestAICLIFunctionCatalog_SelectRequestFunctions_HidesTextSkillFunctionInAutoMode(t *testing.T) {
	session := skillMentionHideTestSession(t, nil, false)
	// 生产默认走 auto：显式提及先经 AnalyzeSkillExposure 剪枝，再由选择面兜底。
	session.SkillsBinding.exposureMode = skillExposureAuto

	selection, details := session.FunctionCatalog.SelectRequestFunctions(session, "please use skill__alpha to handle this request")
	if selection == nil {
		t.Fatal("expected function selection")
	}
	if selectionContainsFunction(selection, "skill__alpha") {
		t.Fatalf("expected text skill hidden in auto mode, got %v", selection.FinalFunctionNames)
	}
	if details == nil || !stringSliceContains(details.ExplicitMentions, "skill__alpha") {
		t.Fatalf("expected lexical explicit mention retained for diagnostics, got %+v", details)
	}
	if details != nil && stringSliceContains(details.ExposedFunctions, "skill__alpha") {
		t.Fatalf("expected exposure analysis pruned of text skill, got %v", details.ExposedFunctions)
	}
}

// TestAICLIFunctionCatalog_AccessorsRaceHotRegister 是「残余直读收口」的栅栏：
// 后台 goroutine 经公共注册入口热注册（写锁），前台并发调用全部读访问器；
// -race 下必须零报告（收口前的 entries/registry 直读会在此触发竞争）。
func TestAICLIFunctionCatalog_AccessorsRaceHotRegister(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			catalog.RegisterFunction(&testFunction{name: fmt.Sprintf("builtin__race_%d", i)})
		}
	}()

	for i := 0; i < 400; i++ {
		catalog.entryForRead("builtin__race_0")
		catalog.entriesSnapshot()
		catalog.skillFunctionForRead("builtin__race_0")
		catalog.executableSkillFunctionNamesForRead()
		catalog.registeredFunction("builtin__race_0")
		catalog.listRegisteredFunctions()
	}
	close(stop)
	wg.Wait()
}
