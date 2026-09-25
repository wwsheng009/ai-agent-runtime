package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolnames"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
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
	// mainLoopPrompt 非空时表示该技能走“指令注入主循环”；为空时保持旧行为
	// （调用方因注入文本为空而回退到 Execute），避免既有断言大范围改写。
	mainLoopPrompt string
}

func (f *fakeSkillExecutor) Execute(_ context.Context, skill *runtimeskill.Skill, req *runtimetypes.Request) (*runtimeskill.ExecuteResult, error) {
	f.lastSkill = skill
	f.lastReq = req
	return f.result, nil
}

func (f *fakeSkillExecutor) BuildMainLoopPrompt(_ *runtimeskill.Skill, _ *runtimetypes.Request) (string, error) {
	return f.mainLoopPrompt, nil
}

type recordingAICLIBridgeMCP struct {
	callCount int
	lastMCP   string
	lastTool  string
	lastArgs  map[string]interface{}
}

func (m *recordingAICLIBridgeMCP) FindTool(toolName string) (runtimeskill.ToolInfo, error) {
	if toolName != aicliExecToolName {
		return runtimeskill.ToolInfo{}, fmt.Errorf("tool not found: %s", toolName)
	}
	return runtimeskill.ToolInfo{
		Name:        aicliExecToolName,
		Description: "test aicli exec",
		MCPName:     "toolkit",
		Enabled:     true,
	}, nil
}

func (m *recordingAICLIBridgeMCP) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	m.callCount++
	m.lastMCP = mcpName
	m.lastTool = toolName
	m.lastArgs = cloneFunctionSchema(args)
	return "AICLI_EXEC_OK", nil
}

func (m *recordingAICLIBridgeMCP) ListTools() []runtimeskill.ToolInfo {
	return []runtimeskill.ToolInfo{{
		Name:        aicliExecToolName,
		Description: "test aicli exec",
		MCPName:     "toolkit",
		Enabled:     true,
	}}
}

func newAICLIExecBridgeSummaryForTest(name string) *runtimeskill.SkillSummary {
	return &runtimeskill.SkillSummary{
		Name:        name,
		Description: "AICLI exec bridge",
		Codex: &runtimeskill.CodexSkillMetadata{
			Name:        name,
			Description: "AICLI exec bridge",
			Dependencies: &runtimeskill.CodexSkillDependencies{
				Tools: []runtimeskill.CodexSkillToolDependency{{
					Type:  "tool",
					Value: aicliExecToolName,
				}},
			},
		},
	}
}

func TestBuildSkillFunctionName(t *testing.T) {
	name := buildSkillFunctionName("ABAP/Search Object")
	if name != "skill__abap_search_object" {
		t.Fatalf("unexpected function name: %s", name)
	}
}

func TestBuildSkillFunctionNameForSummary_UsesSourcePathWhenNamesCollide(t *testing.T) {
	counts := map[string]int{"shared-codex": 2}
	firstPath := filepath.Join(t.TempDir(), "first", "SKILL.md")
	secondPath := filepath.Join(t.TempDir(), "second", "SKILL.md")

	first := &runtimeskill.SkillSummary{
		Name: "shared-codex",
		Source: &runtimeskill.SkillSource{
			Path: firstPath,
		},
	}
	second := &runtimeskill.SkillSummary{
		Name: "shared-codex",
		Source: &runtimeskill.SkillSource{
			Path: secondPath,
		},
	}

	firstName := buildSkillFunctionNameForSummary(first, counts)
	secondName := buildSkillFunctionNameForSummary(second, counts)

	if firstName == secondName {
		t.Fatalf("expected distinct function names for different paths, got %q", firstName)
	}
	if firstName == buildSkillFunctionName("shared-codex") || secondName == buildSkillFunctionName("shared-codex") {
		t.Fatalf("expected path-aware aliases, got %q and %q", firstName, secondName)
	}
}

func TestSkillsRuntimeBinding_FunctionNameForSkillUsesPathIdentity(t *testing.T) {
	firstPath := filepath.Join(t.TempDir(), "first", "SKILL.md")
	secondPath := filepath.Join(t.TempDir(), "second", "SKILL.md")
	firstName := buildSkillFunctionNameFromIdentity("shared-codex", firstPath)
	secondName := buildSkillFunctionNameFromIdentity("shared-codex", secondPath)

	firstFn := &SkillFunction{
		functionName: firstName,
		sourcePath:   firstPath,
		skill: &runtimeskill.Skill{
			Name: "shared-codex",
			Source: &runtimeskill.SkillSource{
				Path: firstPath,
			},
		},
	}
	secondFn := &SkillFunction{
		functionName: secondName,
		sourcePath:   secondPath,
		skill: &runtimeskill.Skill{
			Name: "shared-codex",
			Source: &runtimeskill.SkillSource{
				Path: secondPath,
			},
		},
	}

	binding := &skillsRuntimeBinding{
		skillFunctions: map[string]*SkillFunction{
			firstName:  firstFn,
			secondName: secondFn,
		},
		skillFunctionsByPath: map[string]*SkillFunction{
			filepath.Clean(firstPath):  firstFn,
			filepath.Clean(secondPath): secondFn,
		},
	}

	if got := binding.functionNameForSkill(firstFn.skill); got != firstName {
		t.Fatalf("expected first path alias %q, got %q", firstName, got)
	}
	if got := binding.functionNameForSkill(secondFn.skill); got != secondName {
		t.Fatalf("expected second path alias %q, got %q", secondName, got)
	}
}

func TestResolveDirectCallableFunctionName_UsesPathAndRejectsAmbiguousPlainName(t *testing.T) {
	firstPath := filepath.Join(t.TempDir(), "first", "SKILL.md")
	secondPath := filepath.Join(t.TempDir(), "second", "SKILL.md")
	firstName := buildSkillFunctionNameFromIdentity("shared-codex", firstPath)
	secondName := buildSkillFunctionNameFromIdentity("shared-codex", secondPath)

	session := &ChatSession{
		FunctionRegistry: functions.NewFunctionRegistry(),
	}
	catalog := ensureFunctionCatalog(session)
	if catalog == nil {
		t.Fatal("expected function catalog")
	}

	firstFn := &SkillFunction{
		functionName: firstName,
		sourcePath:   firstPath,
		skill: &runtimeskill.Skill{
			Name: "shared-codex",
			Source: &runtimeskill.SkillSource{
				Path: firstPath,
			},
		},
	}
	secondFn := &SkillFunction{
		functionName: secondName,
		sourcePath:   secondPath,
		skill: &runtimeskill.Skill{
			Name: "shared-codex",
			Source: &runtimeskill.SkillSource{
				Path: secondPath,
			},
		},
	}
	catalog.RegisterSkillFunction(firstFn)
	catalog.RegisterSkillFunction(secondFn)
	session.SkillsBinding = &skillsRuntimeBinding{
		catalog: catalog,
		skillFunctions: map[string]*SkillFunction{
			firstName:  firstFn,
			secondName: secondFn,
		},
		skillFunctionsByPath: map[string]*SkillFunction{
			filepath.Clean(firstPath):  firstFn,
			filepath.Clean(secondPath): secondFn,
		},
	}

	resolved, isSkill, err := resolveDirectCallableFunctionName(session, firstPath, true)
	if err != nil {
		t.Fatalf("expected path-based resolution to succeed: %v", err)
	}
	if resolved != firstName || !isSkill {
		t.Fatalf("unexpected path resolution result: %s skill=%t", resolved, isSkill)
	}

	if _, _, err := resolveDirectCallableFunctionName(session, "shared-codex", true); err == nil {
		t.Fatalf("expected ambiguous plain skill name to fail")
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

func TestSkillFunctionExecute_DirectToolBridgeUsesDeclaredAICLIExecTool(t *testing.T) {
	mcp := &recordingAICLIBridgeMCP{}
	summary := newAICLIExecBridgeSummaryForTest("renamed-agent")
	skillItem := summary.ToSkillStub()
	attachDirectToolBridgeSkillHandler(summary, skillItem, mcp)
	fn := &SkillFunction{
		functionName: buildSkillFunctionName("renamed-agent"),
		skill:        skillItem,
		executor:     runtimeskill.NewExecutor(nil, mcp, nil),
	}

	output, err := fn.Execute(context.Background(), map[string]interface{}{
		"prompt": "查看时间",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "AICLI_EXEC_OK" {
		t.Fatalf("unexpected output: %s", output)
	}
	if mcp.callCount != 1 {
		t.Fatalf("expected aicli_exec to be called once, got %d", mcp.callCount)
	}
	if mcp.lastTool != aicliExecToolName {
		t.Fatalf("expected %s tool, got %s", aicliExecToolName, mcp.lastTool)
	}
	if mcp.lastArgs["prompt"] != "查看时间" {
		t.Fatalf("unexpected prompt arg: %#v", mcp.lastArgs["prompt"])
	}
	if mcp.lastArgs["disable_tools"] != true {
		t.Fatalf("expected disable_tools=true by default, got %#v", mcp.lastArgs["disable_tools"])
	}
	if mcp.lastArgs["output"] != "text" {
		t.Fatalf("expected text output by default, got %#v", mcp.lastArgs["output"])
	}
	if mcp.lastArgs["timeout"] != directBridgeTimeout {
		t.Fatalf("expected default timeout %s, got %#v", directBridgeTimeout, mcp.lastArgs["timeout"])
	}
	for _, forbidden := range []string{"config", "provider", "model"} {
		if _, ok := mcp.lastArgs[forbidden]; ok {
			t.Fatalf("expected no implicit %s to be injected into aicli_exec args: %#v", forbidden, mcp.lastArgs)
		}
	}
}

func TestSkillFunctionExecute_DirectToolBridgePreservesHandlerAfterLazyResolve(t *testing.T) {
	mcp := &recordingAICLIBridgeMCP{}
	summary := newAICLIExecBridgeSummaryForTest("renamed-agent")
	skillItem := summary.ToSkillStub()
	attachDirectToolBridgeSkillHandler(summary, skillItem, mcp)
	fn := &SkillFunction{
		functionName: buildSkillFunctionName("renamed-agent"),
		skill:        skillItem,
		skillResolver: func() (*runtimeskill.Skill, error) {
			return &runtimeskill.Skill{
				Name:        "renamed-agent",
				Description: "AICLI bridge loaded from SKILL.md",
			}, nil
		},
		executor: runtimeskill.NewExecutor(nil, mcp, nil),
	}

	output, err := fn.Execute(context.Background(), map[string]interface{}{
		"prompt": "查看时间",
		"options": map[string]interface{}{
			"timeout":         "5s",
			"request-timeout": "4s",
			"provider":        "explicit_provider",
			"model":           "explicit_model",
			"log-dir":         `E:\logs\aicli-child`,
			"session-dir":     `E:\sessions\aicli-child`,
			"user":            "tester",
			"title":           "direct bridge run",
			"debug-http":      true,
			"fail-fast":       true,
			"ignored":         "not forwarded",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "AICLI_EXEC_OK" {
		t.Fatalf("unexpected output: %s", output)
	}
	if mcp.callCount != 1 {
		t.Fatalf("expected resolver-preserved handler to call aicli_exec once, got %d", mcp.callCount)
	}
	if mcp.lastArgs["timeout"] != "5s" {
		t.Fatalf("expected explicit timeout option, got %#v", mcp.lastArgs["timeout"])
	}
	if mcp.lastArgs["request_timeout"] != "4s" {
		t.Fatalf("expected hyphenated request-timeout option to normalize, got %#v", mcp.lastArgs["request_timeout"])
	}
	if mcp.lastArgs["provider"] != "explicit_provider" || mcp.lastArgs["model"] != "explicit_model" {
		t.Fatalf("expected explicit provider/model options to be forwarded, got %#v", mcp.lastArgs)
	}
	if mcp.lastArgs["log_dir"] != `E:\logs\aicli-child` || mcp.lastArgs["session_dir"] != `E:\sessions\aicli-child` {
		t.Fatalf("expected log/session directory options to be forwarded, got %#v", mcp.lastArgs)
	}
	if mcp.lastArgs["user"] != "tester" || mcp.lastArgs["title"] != "direct bridge run" {
		t.Fatalf("expected user/title options to be forwarded, got %#v", mcp.lastArgs)
	}
	if mcp.lastArgs["debug_http"] != true || mcp.lastArgs["fail_fast"] != true {
		t.Fatalf("expected debug options to be forwarded, got %#v", mcp.lastArgs)
	}
	if _, ok := mcp.lastArgs["ignored"]; ok {
		t.Fatalf("unexpected unsupported option forwarded: %#v", mcp.lastArgs)
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

// TestSkillFunctionExecute_InjectsInstructionsIntoMainLoop 固化方案 A：说明型
// 技能（无 handler / workflow）经 skill__x 调用时返回技能指令文本，由主循环的
// 模型用常规工具执行，而不是另起一次技能桥 LLM 交互。
func TestSkillFunctionExecute_InjectsInstructionsIntoMainLoop(t *testing.T) {
	injected := "已加载技能「abap_search」的指令；请使用当前对话已有的常规工具完成下面的技能指令与用户请求。\n\n## 技能指令\nSearch ABAP objects\n\n## 用户请求\nsearch z* objects"
	executor := &fakeSkillExecutor{
		mainLoopPrompt: injected,
		result: &runtimeskill.ExecuteResult{
			SkillName: "abap_search",
			Success:   true,
			Output:    "bridge-ran",
		},
	}
	fn := &SkillFunction{
		functionName: "skill__abap_search",
		skill: &runtimeskill.Skill{
			Name:        "abap_search",
			Description: "Search ABAP objects",
		},
		executor: executor,
	}

	output, err := fn.Execute(context.Background(), map[string]interface{}{"prompt": "search z* objects"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != injected {
		t.Fatalf("expected injected instructions, got %q", output)
	}
	if executor.lastReq != nil {
		t.Fatalf("技能桥不应在说明型技能上执行: %#v", executor.lastReq)
	}
}

// TestSkillFunctionExecute_HandlerSkillStillUsesBridge：带 handler 的技能保留
// 技能桥执行（说明型之外的能力不走注入）。
func TestSkillFunctionExecute_HandlerSkillStillUsesBridge(t *testing.T) {
	executor := &fakeSkillExecutor{
		mainLoopPrompt: "should-not-be-used",
		result: &runtimeskill.ExecuteResult{
			SkillName: "installer",
			Success:   true,
			Output:    "bridge-ok",
		},
	}
	fn := &SkillFunction{
		functionName: "skill__installer",
		skill: &runtimeskill.Skill{
			Name:    "installer",
			Handler: runtimeskill.SkillHandlerFunc(func(_ interface{}, _ *runtimetypes.Request) (*runtimetypes.Result, error) { return nil, nil }),
		},
		executor: executor,
	}

	output, err := fn.Execute(context.Background(), map[string]interface{}{"prompt": "install"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "bridge-ok" {
		t.Fatalf("handler 技能应保留技能桥执行, got %q", output)
	}
	if executor.lastReq == nil {
		t.Fatalf("技能桥未被调用")
	}
}

// TestBuildSkillMainLoopInjection_HonorsBridgeOption：/skill 直调默认走注入，
// 显式 options.execution=bridge 时回退到技能桥执行。
func TestBuildSkillMainLoopInjection_HonorsBridgeOption(t *testing.T) {
	executor := &fakeSkillExecutor{mainLoopPrompt: "injected-skill-instructions"}
	fn := &SkillFunction{
		functionName: "skill__abap_search",
		skill:        &runtimeskill.Skill{Name: "abap_search", Description: "Search ABAP objects"},
		executor:     executor,
	}
	session := &ChatSession{
		SkillsBinding: &skillsRuntimeBinding{
			skillFunctions: map[string]*SkillFunction{"skill__abap_search": fn},
		},
	}

	injected, ok := buildSkillMainLoopInjection(session, "skill__abap_search", map[string]interface{}{"prompt": "search"})
	if !ok || injected != "injected-skill-instructions" {
		t.Fatalf("expected injection, ok=%v injected=%q", ok, injected)
	}

	_, ok = buildSkillMainLoopInjection(session, "skill__abap_search", map[string]interface{}{
		"prompt":  "search",
		"options": map[string]interface{}{"execution": "bridge"},
	})
	if ok {
		t.Fatalf("options.execution=bridge 应回退到技能桥")
	}

	if _, ok := buildSkillMainLoopInjection(session, "skill__missing", map[string]interface{}{"prompt": "search"}); ok {
		t.Fatalf("未知技能不应注入")
	}
}

func TestInitSkillFunctionsRegistersSkills(t *testing.T) {
	tempDir := t.TempDir()
	// 固定工作区：本包目录位于仓库内，工作区锚点会（按设计）发现仓库根的
	// .agents/skills；这里只验证显式 skill_dir 的注册行为。
	t.Chdir(tempDir)
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

	binding, err := initSkillFunctions(cfg, session, nil, nil, 0, "")
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

// TestInitSkillFunctionsBindsSessionModelToBridge 回归“技能桥使用 runtime
// 默认模型而不是会话模型”：共享 runtime host bootstrap 不会把会话模型写入
// runtime 默认模型，技能桥必须由调用方显式绑定，否则请求会被发到无关模型。
func TestInitSkillFunctionsBindsSessionModelToBridge(t *testing.T) {
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

	session := &ChatSession{
		ProviderName:     "commandgo",
		Model:            "meituan/LongCat-2.0:free",
		FunctionRegistry: functions.NewFunctionRegistry(),
	}
	cfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{
			Enabled:  true,
			SkillDir: tempDir,
		},
	}

	binding, err := initSkillFunctions(cfg, session, nil, nil, 0, "")
	if err != nil {
		t.Fatalf("initSkillFunctions failed: %v", err)
	}
	if binding == nil {
		t.Fatalf("expected skill binding")
	}
	defer func() { _ = binding.Close() }()

	fn := binding.skillFunctions["skill__abap_search"]
	if fn == nil || fn.executor == nil {
		t.Fatalf("skill function executor not wired")
	}
	realExecutor, ok := fn.executor.(*runtimeskill.Executor)
	if !ok {
		t.Fatalf("unexpected executor type %T", fn.executor)
	}
	if got := realExecutor.DefaultModel(); got != session.Model {
		t.Fatalf("bridge default model = %q, want session model %q", got, session.Model)
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

	binding, err := initSkillFunctions(cfg, session, nil, nil, 0, "")
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
	// 固定工作区：避免本包目录（位于仓库内）的工作区锚点影响计数；该锚点
	// 由 TestResolveConfiguredSkillDirs_IncludesWorkspaceAgentsSkillsWithoutConfig
	// 单独覆盖。
	t.Chdir(t.TempDir())
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

func TestResolveConfiguredSkillDirs_ResolvesUpwardRelativePaths(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, ".agents", "skills")
	extraDir := filepath.Join(root, "extra-skills")
	cliDir := filepath.Join(root, "cli-skills")

	mustMkdirAll := func(path string) {
		t.Helper()
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	mustMkdirAll(skillDir)
	mustMkdirAll(extraDir)
	mustMkdirAll(cliDir)

	backendDir := filepath.Join(root, "backend")
	mustMkdirAll(backendDir)

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(backendDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	})

	resolved := resolveConfiguredSkillDirs(&config.SkillsRuntimeConfig{
		SkillDir:       "./.agents/skills",
		SkillDirs:      []string{"./.agents/skills"},
		ExtraSkillDirs: []string{"./extra-skills"},
	}, []string{"./.agents/skills", "./cli-skills"})

	if len(resolved) != 3 {
		t.Fatalf("unexpected resolved dir count: %d (%#v)", len(resolved), resolved)
	}
	if resolved[0] != skillDir {
		t.Fatalf("unexpected resolved system dir: %q", resolved[0])
	}
	if resolved[1] != extraDir {
		t.Fatalf("unexpected resolved extra dir: %q", resolved[1])
	}
	if resolved[2] != cliDir {
		t.Fatalf("unexpected resolved cli dir: %q", resolved[2])
	}
}

func TestInitSkillFunctions_LoadsImagegenSkillWithResolvedPaths(t *testing.T) {
	t.Setenv("CODEX_04_API_KEYS", "test-key")
	t.Setenv("CODEX_04_BASE_URL", "https://example.com")
	homeRoot := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", homeRoot)
	t.Setenv("USERPROFILE", homeRoot)

	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	backendDir := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", "..", ".."))

	originalWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(backendDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(originalWD); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	})

	if _, err := config.InitGlobalConfig(filepath.Join("configs", "config.yaml")); err != nil {
		t.Fatalf("InitGlobalConfig failed: %v", err)
	}

	session := &ChatSession{
		FunctionRegistry: functions.NewFunctionRegistry(),
	}
	catalog := ensureFunctionCatalog(session)
	if catalog == nil {
		t.Fatal("expected function catalog")
	}

	toolManager := runtimetools.NewDefaultManagerWithRuntimeConfig(nil, runtimecfg.DefaultRuntimeConfig())
	binding, err := initSkillFunctions(&config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{
			Enabled:        true,
			SkillDir:       "./.agents/skills",
			ConfigFile:     filepath.Join("configs", "runtime.yaml"),
			ExtraSkillDirs: nil,
		},
	}, session, toolManager, nil, 0, "")
	if err != nil {
		t.Fatalf("initSkillFunctions failed: %v", err)
	}
	if binding == nil {
		t.Fatal("expected skill binding")
	}
	defer func() { _ = binding.Close() }()

	if _, ok := binding.skillFunctions["skill__imagegen"]; !ok {
		t.Fatalf("expected imagegen skill to be registered, got %v", binding.skillFunctions)
	}
	if _, ok := catalog.Registry().Get("skill__imagegen"); !ok {
		t.Fatal("expected imagegen skill to be registered in function catalog")
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

	binding, err := initSkillFunctions(cfg, session, nil, nil, 0, "")
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
		SkillsBinding: &skillsRuntimeBinding{
			skillFunctions: map[string]*SkillFunction{
				"skill__abap_search": {
					functionName: "skill__abap_search",
					skill:        &runtimeskill.Skill{Name: "abap_search"},
				},
			},
		},
	}
	replaceRuntimeMessages(session, []runtimetypes.Message{
		{
			Role: "assistant",
			ToolCalls: []runtimetypes.ToolCall{
				{ID: "call-1", Name: "skill__abap_search", Args: map[string]interface{}{"prompt": "search z*"}},
			},
			Metadata: runtimetypes.NewMetadata(),
		},
	})
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

	binding, err := initSkillFunctions(cfg, session, nil, nil, 0, "")
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

	binding, err := initSkillFunctions(cfg, session, nil, nil, 0, "")
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

	binding, err := initSkillFunctions(cfg, session, nil, nil, 0, "")
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

	binding, err := initSkillFunctions(cfg, session, nil, nil, 0, "")
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

func TestBuildRequestFunctionSchemas_ImagegenSkillSuppressesOpenAIImageGenerateTool(t *testing.T) {
	tempDir := t.TempDir()
	skillDir := filepath.Join(tempDir, "imagegen")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	skillYAML := `name: imagegen
description: Generate an image by calling openai_image_generate
triggers:
  - type: pattern
    values:
      - (?:生成|创建|画|绘制|做|出).{0,20}(?:图片|图像|插画|海报|头像|壁纸|封面|照片)
    weight: 2
tools:
  - openai_image_generate
workflow:
  steps:
    - id: generate_image
      name: Generate image
      tool: openai_image_generate
      args:
        prompt: "{{prompt}}"
`
	if err := os.WriteFile(filepath.Join(skillDir, "skill.yaml"), []byte(skillYAML), 0o644); err != nil {
		t.Fatalf("write skill failed: %v", err)
	}

	session := &ChatSession{
		ProviderName:     "nvidia",
		Model:            "z-ai/glm4.7",
		FunctionRegistry: functions.NewFunctionRegistry(),
	}
	catalog := ensureFunctionCatalog(session)
	catalog.RegisterBuiltinToolFunction(&testFunction{name: toolnames.OpenAIImageGenerateToolName}, runtimetools.ToolDescriptor{
		Name:        toolnames.OpenAIImageGenerateToolName,
		Description: "generate image via /v1/images/generations",
		Parameters:  map[string]interface{}{"type": "object"},
	})

	cfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{
			Enabled:  true,
			SkillDir: tempDir,
		},
	}

	binding, err := initSkillFunctions(cfg, session, nil, nil, 0, "")
	if err != nil {
		t.Fatalf("initSkillFunctions failed: %v", err)
	}
	if binding == nil {
		t.Fatal("expected skill binding")
	}
	defer func() { _ = binding.Close() }()
	session.SkillsBinding = binding

	schemas := buildRequestFunctionSchemas(session, "帮我生成一个美女图片")
	names := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		names = append(names, schema["name"].(string))
	}

	if len(names) != 1 {
		t.Fatalf("expected only imagegen skill, got %v", names)
	}
	if names[0] != imagegenSkillFunctionName {
		t.Fatalf("expected %s, got %v", imagegenSkillFunctionName, names[0])
	}
	if strings.Contains(strings.Join(names, ","), toolnames.OpenAIImageGenerateToolName) {
		t.Fatalf("did not expect %s when imagegen skill is exposed: %v", toolnames.OpenAIImageGenerateToolName, names)
	}
}

func TestBuildRequestFunctionSchemas_CodexNativeImageSuppressesImagegenSkillAndOpenAIImageTool(t *testing.T) {
	tempDir := t.TempDir()
	skillDir := filepath.Join(tempDir, "imagegen")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	skillYAML := `name: imagegen
description: Generate an image by calling openai_image_generate
triggers:
  - type: pattern
    values:
      - (?:生成|创建|画|绘制|做|出).{0,20}(?:图片|图像|插画|海报|头像|壁纸|封面|照片)
    weight: 2
tools:
  - openai_image_generate
workflow:
  steps:
    - id: generate_image
      name: Generate image
      tool: openai_image_generate
      args:
        prompt: "{{prompt}}"
`
	if err := os.WriteFile(filepath.Join(skillDir, "skill.yaml"), []byte(skillYAML), 0o644); err != nil {
		t.Fatalf("write skill failed: %v", err)
	}

	enabledImageGeneration := true
	session := &ChatSession{
		ProviderName:     "codex_fox",
		Model:            "gpt-5.4-mini",
		FunctionRegistry: functions.NewFunctionRegistry(),
		Provider: config.Provider{
			Protocol:              "codex",
			EnableImageGeneration: &enabledImageGeneration,
			ModelCapabilities: map[string]config.ModelCapabilitySpec{
				"gpt-5.4-mini": {
					InputModalities: []string{"text", "image"},
					NativeTools: config.NativeToolCapabilities{
						ImageGeneration: true,
					},
				},
			},
		},
	}
	catalog := ensureFunctionCatalog(session)
	catalog.RegisterBuiltinToolFunction(&testFunction{name: toolnames.OpenAIImageGenerateToolName}, runtimetools.ToolDescriptor{
		Name:        toolnames.OpenAIImageGenerateToolName,
		Description: "generate image via /v1/images/generations",
		Parameters:  map[string]interface{}{"type": "object"},
	})

	cfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{
			Enabled:  true,
			SkillDir: tempDir,
		},
	}

	binding, err := initSkillFunctions(cfg, session, nil, nil, 0, "")
	if err != nil {
		t.Fatalf("initSkillFunctions failed: %v", err)
	}
	if binding == nil {
		t.Fatal("expected skill binding")
	}
	defer func() { _ = binding.Close() }()
	session.SkillsBinding = binding

	schemas := buildRequestFunctionSchemas(session, "帮我生成一个美女图片")
	if len(schemas) != 0 {
		names := make([]string, 0, len(schemas))
		for _, schema := range schemas {
			names = append(names, schema["name"].(string))
		}
		t.Fatalf("expected local image tool/skill exposure to be suppressed when codex native image is available, got %v", names)
	}
}

func TestBuildSkillsProviderConfigsPropagatesRetryPolicyFromAgentConfig(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			Timeout:    45 * time.Second,
			MaxRetries: 0,
			Headers: map[string]string{
				"X-Global": "global-value",
				"X-Shared": "global-value",
			},
			Backoff: config.BackoffConfig{
				InitialInterval: 300 * time.Millisecond,
				MaxInterval:     4 * time.Second,
				MaxElapsedTime:  30 * time.Second,
				Multiplier:      1.8,
				Randomization:   0.25,
				Schedule:        []time.Duration{time.Second, 2 * time.Second},
			},
			Items: map[string]config.Provider{
				"openai-main": {
					Enabled:      true,
					Protocol:     "openai",
					BaseURL:      "https://api.example.com",
					DefaultModel: "gpt-5",
					Headers: map[string]string{
						"x-shared": "provider-value",
					},
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
	if providerCfg.Headers["X-Global"] != "global-value" || providerCfg.Headers["X-Shared"] != "provider-value" {
		t.Fatalf("unexpected effective headers: %+v", providerCfg.Headers)
	}
	if providerCfg.MaxRetries != 4 {
		t.Fatalf("expected max retries 4, got %d", providerCfg.MaxRetries)
	}
	if providerCfg.MaxTransportRetries != runtimellm.DefaultTransportMaxRetries {
		t.Fatalf("expected max transport retries %d, got %d", runtimellm.DefaultTransportMaxRetries, providerCfg.MaxTransportRetries)
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
	if !reflect.DeepEqual(providerCfg.RetryTuning.Schedule, []time.Duration{time.Second, 2 * time.Second}) {
		t.Fatalf("unexpected retry schedule: %v", providerCfg.RetryTuning.Schedule)
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

func TestSkillUsesDefaultExecution_AndToolLoopOption(t *testing.T) {
	if skillUsesDefaultExecution(nil) {
		t.Fatal("nil skill must not use default execution")
	}
	plain := &runtimeskill.Skill{Name: "installer"}
	if !skillUsesDefaultExecution(plain) {
		t.Fatal("handler-less workflow-less skill must use default execution")
	}
	withWorkflow := &runtimeskill.Skill{
		Name: "wf",
		Workflow: &runtimeskill.Workflow{Steps: []runtimeskill.WorkflowStep{
			{ID: "step_a", Name: "A", Tool: "tool_a"},
		}},
	}
	if skillUsesDefaultExecution(withWorkflow) {
		t.Fatal("workflow skill must not use default execution")
	}

	options := enableSkillToolLoopOption(nil)
	if options["tool_loop"] != true {
		t.Fatalf("expected tool_loop=true, got %#v", options["tool_loop"])
	}
	// 用户显式关闭时不得被覆盖。
	disabled := enableSkillToolLoopOption(map[string]interface{}{"tool_loop": false})
	if disabled["tool_loop"] != false {
		t.Fatalf("explicit tool_loop=false must be preserved, got %#v", disabled["tool_loop"])
	}
}

// TestResolveConfiguredSkillDirs_IncludesWorkspaceAgentsSkillsWithoutConfig 锁定
// 「工作区 .agents/skills 无条件参与加载」这条契约：不依赖 config_file 的位置
// （用户级配置的常态是 ~/.aicli/config.yaml，位于工作区之外）也不依赖
// skills_runtime.skill_dir 是否配置。修复前该场景下 resolveConfiguredSkillDirs
// 返回空列表，chat 的 skill catalog 恒为 total=0。
func TestResolveConfiguredSkillDirs_IncludesWorkspaceAgentsSkillsWithoutConfig(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	skillsDir := filepath.Join(workspace, ".agents", "skills")
	writeTestFile(t, filepath.Join(skillsDir, "demo", "skill.yaml"), "name: demo\ndescription: demo skill\n")
	t.Chdir(workspace)

	got := resolveConfiguredSkillDirs(nil, nil)

	want := skillsDir
	if resolved, err := filepath.EvalSymlinks(skillsDir); err == nil && strings.TrimSpace(resolved) != "" {
		want = resolved
	}
	for _, dir := range got {
		if dir == want || dir == skillsDir {
			return
		}
	}
	t.Fatalf("workspace .agents/skills not discovered: got %#v, want %q", got, want)
}

// profile 的 runtime.overrides 只写会话生效配置（session.Config），不写回全局 cfg：
// skills 加载面（enabled 门 / 目录 / runtime 配置路径）必须以会话生效配置为准，否则
// 白名单里的 skills_runtime.* 覆盖是假开关（D13/D14）。
func TestInitSkillFunctionsUsesSessionEffectiveConfig(t *testing.T) {
	t.Chdir(t.TempDir()) // 固定工作区：避免仓库 .agents/skills 干扰计数
	skillRoot := t.TempDir()
	skillDir := filepath.Join(skillRoot, "abap_search")
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
	writeTestFile(t, filepath.Join(skillDir, "skill.yaml"), skillYAML)

	newSession := func(cfg *config.Config) *ChatSession {
		return &ChatSession{
			ProviderName:     "nvidia",
			Model:            "z-ai/glm4.7",
			Config:           cfg,
			FunctionRegistry: functions.NewFunctionRegistry(),
		}
	}
	enabledCfg := &config.Config{SkillsRuntime: &config.SkillsRuntimeConfig{Enabled: true, SkillDir: skillRoot}}
	disabledCfg := &config.Config{SkillsRuntime: &config.SkillsRuntimeConfig{Enabled: false, SkillDir: skillRoot}}

	// 1) 全局启用 + profile 覆盖 enabled=false → 必须关闭（会话生效面优先）。
	session := newSession(disabledCfg)
	binding, err := initSkillFunctions(enabledCfg, session, nil, nil, 0, "")
	if err != nil {
		t.Fatalf("initSkillFunctions: %v", err)
	}
	if binding != nil {
		_ = binding.Close()
		t.Fatalf("profile 覆盖 enabled=false 必须关闭 skills，got count=%d", binding.Count())
	}

	// 2) 全局关闭 + profile 覆盖 enabled=true → 必须开启（反向也不能被全局 cfg 吞掉）。
	session = newSession(enabledCfg)
	binding, err = initSkillFunctions(disabledCfg, session, nil, nil, 0, "")
	if err != nil {
		t.Fatalf("initSkillFunctions: %v", err)
	}
	if binding == nil {
		t.Fatal("profile 覆盖 enabled=true 必须开启 skills")
	}
	defer func() { _ = binding.Close() }()
	if binding.Count() != 1 {
		t.Fatalf("unexpected skill count: %d", binding.Count())
	}

	// 3) 暴露模式同样必须读会话生效面（否则 profile 覆盖的
	// aicli_skill_exposure_mode / top_k 也是假开关）。
	exposureCfg := &config.Config{SkillsRuntime: &config.SkillsRuntimeConfig{
		Enabled:                true,
		SkillDir:               skillRoot,
		AICLISkillExposureMode: skillExposurePrefer,
		AICLISkillExposureTopK: 3,
	}}
	session = newSession(exposureCfg)
	binding, err = initSkillFunctions(enabledCfg, session, nil, nil, 0, "")
	if err != nil {
		t.Fatalf("initSkillFunctions: %v", err)
	}
	if binding == nil {
		t.Fatal("profile 覆盖 enabled=true 必须开启 skills")
	}
	defer func() { _ = binding.Close() }()
	if binding.exposureMode != skillExposurePrefer || binding.exposureTopK != 3 {
		t.Fatalf("会话生效面的 exposure 覆盖未生效: mode=%q topK=%d", binding.exposureMode, binding.exposureTopK)
	}
}
