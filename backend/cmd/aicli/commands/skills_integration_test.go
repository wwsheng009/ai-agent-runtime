package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type testFunction struct {
	name string
}

func (f *testFunction) Name() string { return f.name }

func (f *testFunction) Description() string { return "test function" }

func (f *testFunction) Parameters() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

func (f *testFunction) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	return "ok", nil
}

type fakeSkillExecutor struct {
	lastSkill *runtimeskill.Skill
	lastReq   *runtimetypes.Request
	result    *runtimeskill.ExecuteResult
}

func (f *fakeSkillExecutor) Execute(_ context.Context, skill *runtimeskill.Skill, req *runtimetypes.Request) (*runtimeskill.ExecuteResult, error) {
	f.lastSkill = skill
	f.lastReq = req
	return f.result, nil
}

func TestBuildSkillFunctionName(t *testing.T) {
	name := buildSkillFunctionName("ABAP/Search Object")
	if name != "skill__abap_search_object" {
		t.Fatalf("unexpected function name: %s", name)
	}
}

func TestSkillFunctionExecuteBuildsRuntimeRequest(t *testing.T) {
	executor := &fakeSkillExecutor{
		result: &runtimeskill.ExecuteResult{
			SkillName: "abap_search",
			Success:   true,
			Output:    "ok",
		},
	}

	fn := &SkillFunction{
		functionName: "skill__abap_search",
		skill: &runtimeskill.Skill{
			Name:        "abap_search",
			Description: "Search ABAP objects",
		},
		executor: executor,
		historyProvider: func() []runtimetypes.Message {
			return []runtimetypes.Message{*runtimetypes.NewUserMessage("previous")}
		},
		metadataProvider: func() runtimetypes.Metadata {
			metadata := runtimetypes.NewMetadata()
			metadata.Set("source", "test")
			return metadata
		},
	}

	output, err := fn.Execute(context.Background(), map[string]interface{}{
		"prompt": "search z* objects",
		"context": map[string]interface{}{
			"system": "abap",
		},
		"options": map[string]interface{}{
			"limit": 5,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "ok" {
		t.Fatalf("unexpected output: %s", output)
	}
	if executor.lastSkill == nil || executor.lastSkill.Name != "abap_search" {
		t.Fatalf("skill was not passed to executor")
	}
	if executor.lastReq == nil {
		t.Fatalf("request was not built")
	}
	if executor.lastReq.Prompt != "search z* objects" {
		t.Fatalf("unexpected prompt: %s", executor.lastReq.Prompt)
	}
	if executor.lastReq.Context["system"] != "abap" {
		t.Fatalf("context not propagated")
	}
	if executor.lastReq.Options["limit"] != 5 {
		t.Fatalf("options not propagated")
	}
	if len(executor.lastReq.History) != 1 || executor.lastReq.History[0].Content != "previous" {
		t.Fatalf("history not propagated")
	}
	if executor.lastReq.Metadata.GetString("source", "") != "test" {
		t.Fatalf("metadata not propagated")
	}
}

func TestSkillFunctionExecute_UsesLazyResolverWhenPresent(t *testing.T) {
	executor := &fakeSkillExecutor{
		result: &runtimeskill.ExecuteResult{
			SkillName: "abap_search",
			Success:   true,
			Output:    "ok",
		},
	}

	resolverCalled := false
	fn := &SkillFunction{
		functionName: "skill__abap_search",
		skill: &runtimeskill.Skill{
			Name:        "abap_search",
			Description: "Search ABAP objects",
		},
		skillResolver: func() (*runtimeskill.Skill, error) {
			resolverCalled = true
			return &runtimeskill.Skill{
				Name:        "abap_search_resolved",
				Description: "Resolved at execution time",
			}, nil
		},
		executor: executor,
	}

	output, err := fn.Execute(context.Background(), map[string]interface{}{
		"prompt": "search z* objects",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "ok" {
		t.Fatalf("unexpected output: %s", output)
	}
	if !resolverCalled {
		t.Fatalf("expected lazy resolver to be called")
	}
	if executor.lastSkill == nil || executor.lastSkill.Name != "abap_search_resolved" {
		t.Fatalf("expected resolved skill to be passed to executor, got %#v", executor.lastSkill)
	}
}

func TestSkillFunctionDescription_UsesSummaryWhenSkillStubAbsent(t *testing.T) {
	fn := &SkillFunction{
		functionName: "skill__abap_search",
		summary: &runtimeskill.SkillSummary{
			Name:         "abap_search",
			Description:  "Search ABAP objects",
			Category:     "abap",
			Capabilities: []string{"object_search"},
			Tags:         []string{"sap"},
			Tools:        []string{"se16n_query"},
		},
	}

	description := fn.Description()
	if !strings.Contains(description, `"abap_search"`) {
		t.Fatalf("expected summary-backed skill name, got %s", description)
	}
	if !strings.Contains(description, "Search ABAP objects") {
		t.Fatalf("expected summary-backed description, got %s", description)
	}
	if !strings.Contains(description, "Capabilities: object_search.") {
		t.Fatalf("expected summary-backed capabilities, got %s", description)
	}
}

func TestSkillFunctionExecute_MergesProfileContext(t *testing.T) {
	executor := &fakeSkillExecutor{
		result: &runtimeskill.ExecuteResult{
			SkillName: "abap_search",
			Success:   true,
			Output:    "ok",
		},
	}

	fn := &SkillFunction{
		functionName: "skill__abap_search",
		skill:        &runtimeskill.Skill{Name: "abap_search"},
		executor:     executor,
		contextProvider: func() map[string]interface{} {
			return map[string]interface{}{
				"profile_resources": map[string]interface{}{
					"memory": map[string]interface{}{"content": `{"summary":"cached profile memory"}`},
				},
				"profile_memory_path": "E:/profiles/dev/agents/coder/memory/memory.json",
			}
		},
	}

	_, err := fn.Execute(context.Background(), map[string]interface{}{
		"prompt": "search z* objects",
		"context": map[string]interface{}{
			"system": "abap",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if executor.lastReq == nil {
		t.Fatal("expected runtime request")
	}
	if executor.lastReq.Context["profile_memory_path"] != "E:/profiles/dev/agents/coder/memory/memory.json" {
		t.Fatalf("expected profile context to be merged, got %#v", executor.lastReq.Context["profile_memory_path"])
	}
	if executor.lastReq.Context["system"] != "abap" {
		t.Fatalf("expected explicit context to be preserved")
	}
	contextPack, ok := executor.lastReq.Context["context_pack"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected context_pack to be synthesized, got %#v", executor.lastReq.Context["context_pack"])
	}
	profilePack, ok := contextPack["profile"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected profile layer in context_pack, got %#v", contextPack["profile"])
	}
	if profilePack["memory_path"] != "E:/profiles/dev/agents/coder/memory/memory.json" {
		t.Fatalf("unexpected profile memory path in context_pack: %#v", profilePack["memory_path"])
	}
}

func TestSkillFunctionExecutePrefersPlainOutputOnSuccess(t *testing.T) {
	executor := &fakeSkillExecutor{
		result: &runtimeskill.ExecuteResult{
			SkillName: "skill_runtime_smoke",
			Success:   true,
			Output:    "SKILL_RUNTIME_OK",
			Usage: &runtimetypes.TokenUsage{
				PromptTokens:     1,
				CompletionTokens: 1,
				TotalTokens:      2,
			},
		},
	}

	fn := &SkillFunction{
		functionName: "skill__skill_runtime_smoke",
		skill:        &runtimeskill.Skill{Name: "skill_runtime_smoke"},
		executor:     executor,
	}

	output, err := fn.Execute(context.Background(), map[string]interface{}{
		"prompt": "run smoke test",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "SKILL_RUNTIME_OK" {
		t.Fatalf("unexpected output: %s", output)
	}
}

func TestInitSkillFunctionsRegistersSkills(t *testing.T) {
	tempDir := t.TempDir()
	skillDir := filepath.Join(tempDir, "abap_search")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	skillYAML := `name: abap_search
description: Search ABAP objects
category: abap
capabilities:
  - object_search
triggers:
  - type: keyword
    values: ["abap", "search"]
    weight: 1
`
	if err := os.WriteFile(filepath.Join(skillDir, "skill.yaml"), []byte(skillYAML), 0o644); err != nil {
		t.Fatalf("write skill failed: %v", err)
	}

	session := &ChatSession{
		ProviderName:     "nvidia",
		Model:            "z-ai/glm4.7",
		FunctionRegistry: functions.NewFunctionRegistry(),
	}
	cfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{
			Enabled:  true,
			SkillDir: tempDir,
		},
	}

	binding, err := initSkillFunctions(cfg, session, nil, 0, "")
	if err != nil {
		t.Fatalf("initSkillFunctions failed: %v", err)
	}
	if binding == nil {
		t.Fatalf("expected skill binding")
	}
	defer func() {
		_ = binding.Close()
	}()

	if binding.Count() != 1 {
		t.Fatalf("unexpected skill count: %d", binding.Count())
	}
	if _, ok := session.FunctionRegistry.Get("skill__abap_search"); !ok {
		t.Fatalf("skill function not registered")
	}
}

func TestInitSkillFunctions_DiscoverOnlyKeepsPromptLazy(t *testing.T) {
	tempDir := t.TempDir()
	skillDir := filepath.Join(tempDir, "abap_search")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}

	skillYAML := `name: abap_search
description: Search ABAP objects
triggers:
  - type: keyword
    values: ["abap", "search"]
    weight: 1
`
	if err := os.WriteFile(filepath.Join(skillDir, "skill.yaml"), []byte(skillYAML), 0o644); err != nil {
		t.Fatalf("write skill failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "prompt.md"), []byte("You are lazily discovered."), 0o644); err != nil {
		t.Fatalf("write prompt failed: %v", err)
	}

	session := &ChatSession{
		ProviderName:     "nvidia",
		Model:            "z-ai/glm4.7",
		FunctionRegistry: functions.NewFunctionRegistry(),
	}
	cfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{
			Enabled:  true,
			SkillDir: tempDir,
		},
	}

	binding, err := initSkillFunctions(cfg, session, nil, 0, "")
	if err != nil {
		t.Fatalf("initSkillFunctions failed: %v", err)
	}
	if binding == nil {
		t.Fatalf("expected skill binding")
	}
	defer func() { _ = binding.Close() }()

	fn := binding.skillFunctions["skill__abap_search"]
	if fn == nil || fn.skill == nil {
		t.Fatalf("expected skill function")
	}
	if fn.skill.SystemPrompt != "" || fn.skill.UserPrompt != "" {
		t.Fatalf("expected prompt to remain lazy, got system=%q user=%q", fn.skill.SystemPrompt, fn.skill.UserPrompt)
	}
	if fn.skill.Source == nil || fn.skill.Source.PromptPath == "" {
		t.Fatalf("expected prompt path discovery")
	}
}

func TestResolveConfiguredSkillDirs_AppendsCLIAndConfigDirs(t *testing.T) {
	systemDir := t.TempDir()
	extraDir := t.TempDir()
	cliDir := t.TempDir()

	resolved := resolveConfiguredSkillDirs(&config.SkillsRuntimeConfig{
		SkillDir:       systemDir,
		ExtraSkillDirs: []string{extraDir},
	}, []string{cliDir, extraDir})

	if len(resolved) != 3 {
		t.Fatalf("unexpected resolved dir count: %d", len(resolved))
	}
	if resolved[0] != systemDir || resolved[1] != extraDir || resolved[2] != cliDir {
		t.Fatalf("unexpected resolved order: %#v", resolved)
	}
}

func TestBuildRequestFunctionSchemas_OnlyExposesMatchedSkills(t *testing.T) {
	tempDir := t.TempDir()

	skillOneDir := filepath.Join(tempDir, "abap_search")
	if err := os.MkdirAll(skillOneDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	skillOne := `name: abap_search
description: Search ABAP objects
triggers:
  - type: keyword
    values: ["abap", "search"]
    weight: 1
`
	if err := os.WriteFile(filepath.Join(skillOneDir, "skill.yaml"), []byte(skillOne), 0o644); err != nil {
		t.Fatalf("write skill failed: %v", err)
	}

	skillTwoDir := filepath.Join(tempDir, "view_file")
	if err := os.MkdirAll(skillTwoDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	skillTwo := `name: view_file
description: View a file
triggers:
  - type: keyword
    values: ["file", "view"]
    weight: 1
`
	if err := os.WriteFile(filepath.Join(skillTwoDir, "skill.yaml"), []byte(skillTwo), 0o644); err != nil {
		t.Fatalf("write skill failed: %v", err)
	}

	session := &ChatSession{
		ProviderName:     "nvidia",
		Model:            "z-ai/glm4.7",
		FunctionRegistry: functions.NewFunctionRegistry(),
	}
	session.FunctionRegistry.Register(&testFunction{name: "builtin__diagnose"})

	cfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{
			Enabled:  true,
			SkillDir: tempDir,
		},
	}

	binding, err := initSkillFunctions(cfg, session, nil, 0, "")
	if err != nil {
		t.Fatalf("initSkillFunctions failed: %v", err)
	}
	if binding == nil {
		t.Fatalf("expected skill binding")
	}
	defer func() { _ = binding.Close() }()
	session.SkillsBinding = binding

	schemas := buildRequestFunctionSchemas(session, "please search abap objects")
	names := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		names = append(names, schema["name"].(string))
	}

	if len(names) != 2 {
		t.Fatalf("expected 2 exposed functions, got %d (%v)", len(names), names)
	}
	if names[0] != "builtin__diagnose" && names[1] != "builtin__diagnose" {
		t.Fatalf("expected builtin function to remain exposed: %v", names)
	}
	if names[0] != "skill__abap_search" && names[1] != "skill__abap_search" {
		t.Fatalf("expected only matched skill to be exposed: %v", names)
	}
	if names[0] == "skill__view_file" || names[1] == "skill__view_file" {
		t.Fatalf("unexpected unrelated skill exposed: %v", names)
	}
}

func TestBuildRequestFunctionSchemas_RetainsPreviouslyCalledSkillWhenPromptEmpty(t *testing.T) {
	session := &ChatSession{
		FunctionRegistry: functions.NewFunctionRegistry(),
		Messages: []map[string]interface{}{
			{
				"role": "assistant",
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call-1",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "skill__abap_search",
							"arguments": `{"prompt":"search z*"}`,
						},
					},
				},
			},
		},
		SkillsBinding: &skillsRuntimeBinding{
			skillFunctions: map[string]*SkillFunction{
				"skill__abap_search": {
					functionName: "skill__abap_search",
					skill:        &runtimeskill.Skill{Name: "abap_search"},
				},
			},
		},
	}
	session.FunctionRegistry.Register(session.SkillsBinding.skillFunctions["skill__abap_search"])
	session.FunctionRegistry.Register(&testFunction{name: "builtin__diagnose"})

	schemas := buildRequestFunctionSchemas(session, "")
	names := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		names = append(names, schema["name"].(string))
	}

	if len(names) != 2 {
		t.Fatalf("expected builtin + previous skill, got %v", names)
	}
}

func TestResolveConfiguredSkillExposureTopK(t *testing.T) {
	cfg := &config.SkillsRuntimeConfig{
		AICLISkillExposureTopK: 7,
	}

	if got := resolveConfiguredSkillExposureTopK(cfg, 0); got != 7 {
		t.Fatalf("expected config top-k 7, got %d", got)
	}
	if got := resolveConfiguredSkillExposureTopK(cfg, 3); got != 3 {
		t.Fatalf("expected cli override top-k 3, got %d", got)
	}
	if got := resolveConfiguredSkillExposureTopK(nil, 0); got != defaultSkillExposureK {
		t.Fatalf("expected default top-k %d, got %d", defaultSkillExposureK, got)
	}
}

func TestResolveConfiguredSkillExposureMode(t *testing.T) {
	cfg := &config.SkillsRuntimeConfig{
		AICLISkillExposureMode: skillExposurePrefer,
	}

	if got := resolveConfiguredSkillExposureMode(cfg, ""); got != skillExposurePrefer {
		t.Fatalf("expected config mode prefer, got %s", got)
	}
	if got := resolveConfiguredSkillExposureMode(cfg, "only"); got != skillExposureOnly {
		t.Fatalf("expected cli override only, got %s", got)
	}
	if got := resolveConfiguredSkillExposureMode(nil, ""); got != skillExposureAuto {
		t.Fatalf("expected default mode auto, got %s", got)
	}
	if got := resolveConfiguredSkillExposureMode(cfg, "bad-mode"); got != skillExposureAuto {
		t.Fatalf("expected invalid mode to normalize to auto, got %s", got)
	}
}

func TestBuildRequestFunctionSchemas_RespectsExposureTopK(t *testing.T) {
	tempDir := t.TempDir()

	alphaDir := filepath.Join(tempDir, "alpha")
	requireNoError := func(err error) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	requireNoError(os.MkdirAll(alphaDir, 0o755))
	requireNoError(os.WriteFile(filepath.Join(alphaDir, "skill.yaml"), []byte(`name: alpha
description: Alpha search
triggers:
  - type: keyword
    values: ["search", "alpha"]
    weight: 2
`), 0o644))

	betaDir := filepath.Join(tempDir, "beta")
	requireNoError(os.MkdirAll(betaDir, 0o755))
	requireNoError(os.WriteFile(filepath.Join(betaDir, "skill.yaml"), []byte(`name: beta
description: Beta search
triggers:
  - type: keyword
    values: ["search", "beta"]
    weight: 1
`), 0o644))

	session := &ChatSession{
		ProviderName:     "nvidia",
		Model:            "z-ai/glm4.7",
		FunctionRegistry: functions.NewFunctionRegistry(),
	}
	session.FunctionRegistry.Register(&testFunction{name: "builtin__diagnose"})

	cfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{
			Enabled:                true,
			SkillDir:               tempDir,
			AICLISkillExposureTopK: 1,
		},
	}

	binding, err := initSkillFunctions(cfg, session, nil, 0, "")
	if err != nil {
		t.Fatalf("initSkillFunctions failed: %v", err)
	}
	if binding == nil {
		t.Fatalf("expected skill binding")
	}
	defer func() { _ = binding.Close() }()
	session.SkillsBinding = binding

	schemas := buildRequestFunctionSchemas(session, "search alpha data")
	names := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		names = append(names, schema["name"].(string))
	}

	if len(names) != 2 {
		t.Fatalf("expected builtin + top1 skill, got %v", names)
	}
	if names[0] != "skill__alpha" && names[1] != "skill__alpha" {
		t.Fatalf("expected top-ranked alpha skill to be exposed, got %v", names)
	}
	if names[0] == "skill__beta" || names[1] == "skill__beta" {
		t.Fatalf("did not expect beta skill with top-k=1, got %v", names)
	}
}

func TestBuildRequestFunctionSchemas_PreferModeSuppressesBuiltinWhenSkillMatches(t *testing.T) {
	tempDir := t.TempDir()
	alphaDir := filepath.Join(tempDir, "alpha")
	if err := os.MkdirAll(alphaDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(alphaDir, "skill.yaml"), []byte(`name: alpha
description: Alpha search
triggers:
  - type: keyword
    values: ["alpha", "search"]
    weight: 2
`), 0o644); err != nil {
		t.Fatalf("write skill failed: %v", err)
	}

	session := &ChatSession{
		ProviderName:     "nvidia",
		Model:            "z-ai/glm4.7",
		FunctionRegistry: functions.NewFunctionRegistry(),
	}
	session.FunctionRegistry.Register(&testFunction{name: "builtin__diagnose"})

	cfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{
			Enabled:                true,
			SkillDir:               tempDir,
			AICLISkillExposureMode: skillExposurePrefer,
		},
	}

	binding, err := initSkillFunctions(cfg, session, nil, 0, "")
	if err != nil {
		t.Fatalf("initSkillFunctions failed: %v", err)
	}
	if binding == nil {
		t.Fatal("expected skill binding")
	}
	defer func() { _ = binding.Close() }()
	session.SkillsBinding = binding

	schemas := buildRequestFunctionSchemas(session, "search alpha data")
	if len(schemas) != 1 {
		t.Fatalf("expected only routed skill in prefer mode, got %v", schemas)
	}
	if schemas[0]["name"] != "skill__alpha" {
		t.Fatalf("expected skill__alpha, got %v", schemas[0]["name"])
	}
}

func TestBuildRequestFunctionSchemas_OnlyModeExposesOnlyMatchedSkills(t *testing.T) {
	tempDir := t.TempDir()
	alphaDir := filepath.Join(tempDir, "alpha")
	if err := os.MkdirAll(alphaDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(alphaDir, "skill.yaml"), []byte(`name: alpha
description: Alpha search
triggers:
  - type: keyword
    values: ["alpha", "search"]
    weight: 2
`), 0o644); err != nil {
		t.Fatalf("write skill failed: %v", err)
	}

	session := &ChatSession{
		ProviderName:     "nvidia",
		Model:            "z-ai/glm4.7",
		FunctionRegistry: functions.NewFunctionRegistry(),
	}
	session.FunctionRegistry.Register(&testFunction{name: "builtin__diagnose"})

	cfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{
			Enabled:                true,
			SkillDir:               tempDir,
			AICLISkillExposureMode: skillExposureOnly,
		},
	}

	binding, err := initSkillFunctions(cfg, session, nil, 0, "")
	if err != nil {
		t.Fatalf("initSkillFunctions failed: %v", err)
	}
	if binding == nil {
		t.Fatal("expected skill binding")
	}
	defer func() { _ = binding.Close() }()
	session.SkillsBinding = binding

	schemas := buildRequestFunctionSchemas(session, "search alpha data")
	if len(schemas) != 1 {
		t.Fatalf("expected only routed skill in only mode, got %v", schemas)
	}
	if schemas[0]["name"] != "skill__alpha" {
		t.Fatalf("expected skill__alpha, got %v", schemas[0]["name"])
	}
}

func TestBuildRequestFunctionSchemas_PreferModeKeepsBuiltinWhenNoSkillMatches(t *testing.T) {
	session := &ChatSession{
		FunctionRegistry: functions.NewFunctionRegistry(),
		SkillsBinding: &skillsRuntimeBinding{
			exposureMode: skillExposurePrefer,
			skillFunctions: map[string]*SkillFunction{
				"skill__alpha": {
					functionName: "skill__alpha",
					skill:        &runtimeskill.Skill{Name: "alpha"},
				},
			},
		},
	}
	session.FunctionRegistry.Register(session.SkillsBinding.skillFunctions["skill__alpha"])
	session.FunctionRegistry.Register(&testFunction{name: "builtin__diagnose"})

	schemas := buildRequestFunctionSchemas(session, "totally unrelated request")
	if len(schemas) != 1 {
		t.Fatalf("expected only builtin schema when no skill matches, got %v", schemas)
	}
	if schemas[0]["name"] != "builtin__diagnose" {
		t.Fatalf("expected builtin__diagnose, got %v", schemas[0]["name"])
	}
}

func TestAnalyzeRequestFunctionSchemas_ReturnsExposureDetails(t *testing.T) {
	tempDir := t.TempDir()
	alphaDir := filepath.Join(tempDir, "alpha")
	if err := os.MkdirAll(alphaDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(alphaDir, "skill.yaml"), []byte(`name: alpha
description: Alpha search
triggers:
  - type: keyword
    values: ["alpha", "search"]
    weight: 2
`), 0o644); err != nil {
		t.Fatalf("write skill failed: %v", err)
	}

	session := &ChatSession{
		ProviderName:     "nvidia",
		Model:            "z-ai/glm4.7",
		FunctionRegistry: functions.NewFunctionRegistry(),
	}
	session.FunctionRegistry.Register(&testFunction{name: "builtin__diagnose"})

	cfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{
			Enabled:                true,
			SkillDir:               tempDir,
			AICLISkillExposureTopK: 1,
			AICLISkillExposureMode: skillExposurePrefer,
		},
	}

	binding, err := initSkillFunctions(cfg, session, nil, 0, "")
	if err != nil {
		t.Fatalf("initSkillFunctions failed: %v", err)
	}
	if binding == nil {
		t.Fatal("expected skill binding")
	}
	defer func() { _ = binding.Close() }()
	session.SkillsBinding = binding

	schemas, details := analyzeRequestFunctionSchemas(session, "search alpha data")
	if len(schemas) != 1 {
		t.Fatalf("expected only routed skill schema, got %v", schemas)
	}
	if details == nil {
		t.Fatal("expected exposure details")
	}
	if details.Mode != skillExposurePrefer {
		t.Fatalf("expected prefer mode, got %s", details.Mode)
	}
	if details.TopK != 1 {
		t.Fatalf("expected top-k 1, got %d", details.TopK)
	}
	if len(details.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %v", details.Candidates)
	}
	if details.Candidates[0].FunctionName != "skill__alpha" {
		t.Fatalf("expected skill__alpha candidate, got %v", details.Candidates[0].FunctionName)
	}
	if len(details.ExposedFunctions) != 1 || details.ExposedFunctions[0] != "skill__alpha" {
		t.Fatalf("expected skill__alpha exposed, got %v", details.ExposedFunctions)
	}
}

func TestBuildSkillsProviderConfigsPropagatesRetryPolicyFromAgentConfig(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			Timeout:    45 * time.Second,
			MaxRetries: 0,
			Backoff: config.BackoffConfig{
				InitialInterval: 300 * time.Millisecond,
				MaxInterval:     4 * time.Second,
				MaxElapsedTime:  30 * time.Second,
				Multiplier:      1.8,
				Randomization:   0.25,
			},
			Items: map[string]config.Provider{
				"openai-main": {
					Enabled:      true,
					Protocol:     "openai",
					BaseURL:      "https://api.example.com",
					DefaultModel: "gpt-5",
				},
			},
		},
		Retry: &config.RetryConfig{
			Enabled:           true,
			DefaultMaxRetries: 4,
			Rules: []config.RetryRuleConfig{
				{
					Name:         "http_5xx_retry",
					Enabled:      true,
					MaxRetries:   4,
					RetryDelayMS: 900,
					StatusCode: config.RetryStatusCodeConfig{
						Range: "500-504",
					},
				},
			},
		},
	}

	result := buildSkillsProviderConfigs(cfg)
	providerCfg := result["openai-main"]
	if providerCfg == nil {
		t.Fatalf("expected provider config to be built")
	}
	if providerCfg.Timeout != 45*time.Second {
		t.Fatalf("expected timeout 45s, got %v", providerCfg.Timeout)
	}
	if providerCfg.MaxRetries != 4 {
		t.Fatalf("expected max retries 4, got %d", providerCfg.MaxRetries)
	}
	if providerCfg.RetryTuning.BaseDelay != 300*time.Millisecond {
		t.Fatalf("expected base delay 300ms, got %v", providerCfg.RetryTuning.BaseDelay)
	}
	if providerCfg.RetryTuning.MaxDelay != 4*time.Second {
		t.Fatalf("expected max delay 4s, got %v", providerCfg.RetryTuning.MaxDelay)
	}
	if providerCfg.RetryTuning.MaxElapsedTime != 30*time.Second {
		t.Fatalf("expected max elapsed time 30s, got %v", providerCfg.RetryTuning.MaxElapsedTime)
	}
	if providerCfg.RetryTuning.Multiplier != 1.8 {
		t.Fatalf("expected multiplier 1.8, got %v", providerCfg.RetryTuning.Multiplier)
	}
	if providerCfg.RetryTuning.Randomization != 0.25 {
		t.Fatalf("expected randomization 0.25, got %v", providerCfg.RetryTuning.Randomization)
	}
	if len(providerCfg.RetryRules) != 1 {
		t.Fatalf("expected 1 retry rule, got %d", len(providerCfg.RetryRules))
	}
	if providerCfg.RetryRules[0].Name != "http_5xx_retry" {
		t.Fatalf("expected retry rule http_5xx_retry, got %s", providerCfg.RetryRules[0].Name)
	}
	if providerCfg.RetryRules[0].RetryDelay != 900*time.Millisecond {
		t.Fatalf("expected retry delay 900ms, got %v", providerCfg.RetryRules[0].RetryDelay)
	}
	if providerCfg.RetryRules[0].StatusCode.Range != "500-504" {
		t.Fatalf("expected status code range 500-504, got %s", providerCfg.RetryRules[0].StatusCode.Range)
	}
}
