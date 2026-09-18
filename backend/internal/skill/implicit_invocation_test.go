package skill

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// makeSummary 构造一个用于隐式调用测试的轻量摘要。
func makeImplicitSummary(name, scope, srcPath, srcDir, layer string, codex *CodexSkillMetadata) *SkillSummary {
	return &SkillSummary{
		Name:   name,
		Source: &SkillSource{Path: srcPath, Dir: srcDir, Layer: layer, Format: SkillSourceFormatCodex},
		Codex:  codex,
	}
}

// noopSkillHandler 是一个满足 SkillHandler 接口的空实现，用于构造非文档模式摘要。
var noopSkillHandler = SkillHandlerFunc(func(ctx interface{}, req *types.Request) (*types.Result, error) {
	return &types.Result{}, nil
})

func TestBuildImplicitInvocationIndex_Empty(t *testing.T) {
	if idx := BuildImplicitInvocationIndex(nil); idx == nil {
		// 返回非 nil 的空索引是可接受的；关键是后续判定不崩溃。
		t.Fatalf("expected non-nil empty index")
	}
	if idx := BuildImplicitInvocationIndex([]*SkillSummary{}); idx == nil {
		t.Fatalf("expected non-nil empty index")
	}
}

func TestBuildImplicitInvocationIndex_SkipsNonCodex(t *testing.T) {
	summaries := []*SkillSummary{
		{Name: "legacy-skill", Source: &SkillSource{Path: "/a/SKILL.md", Dir: "/a", Layer: "user", Format: SkillSourceFormatLegacy}},
	}
	idx := BuildImplicitInvocationIndex(summaries)
	if _, ok := idx.byDocPath[filepath.Clean("/a/SKILL.md")]; ok {
		t.Fatalf("legacy skill must not be indexed for implicit invocation")
	}
}

func TestBuildImplicitInvocationIndex_SkipsDocMode(t *testing.T) {
	// 文档模式技能（Codex 格式、无 handler/workflow）不应参与隐式调用。
	summaries := []*SkillSummary{
		makeImplicitSummary("doc-skill", "user", "/a/SKILL.md", "/a", "user", &CodexSkillMetadata{Scope: "user"}),
	}
	idx := BuildImplicitInvocationIndex(summaries)
	if _, ok := idx.byDocPath[filepath.Clean("/a/SKILL.md")]; ok {
		t.Fatalf("document-mode skill must not be indexed for implicit invocation")
	}
}

func TestBuildImplicitInvocationIndex_SkipsAllowFalse(t *testing.T) {
	allowFalse := false
	summaries := []*SkillSummary{
		{
			Name:    "opt-out",
			Handler: noopSkillHandler, // 有 handler → 非文档模式，可被索引，除非显式排除
			Source:  &SkillSource{Path: "/a/SKILL.md", Dir: "/a", Layer: "user", Format: SkillSourceFormatCodex},
			Codex:   &CodexSkillMetadata{Scope: "user", Policy: &CodexSkillPolicy{AllowImplicitInvocationValue: &allowFalse}},
		},
	}
	idx := BuildImplicitInvocationIndex(summaries)
	if _, ok := idx.byDocPath[filepath.Clean("/a/SKILL.md")]; ok {
		t.Fatalf("skill with allow_implicit_invocation=false must not be indexed")
	}
}

// TestExecutorRefreshImplicitInvocationIndex 验证宿主侧刷新入口：
// 执行器从当前 registry 摘要重建索引后，隐式调用判定立即可用（SK-3）。
func TestExecutorRefreshImplicitInvocationIndex(t *testing.T) {
	registry := NewRegistry(nil)
	summary := &SkillSummary{
		Name:        "build-tool",
		Description: "build tool",
		Handler:     noopSkillHandler,
		Source:      &SkillSource{Path: "/skills/build/SKILL.md", Dir: "/skills/build", Layer: "user", Format: SkillSourceFormatCodex},
		Codex:       &CodexSkillMetadata{Scope: "user"},
	}
	if err := registry.Register(summary.ToSkillStub()); err != nil {
		t.Fatalf("register stub: %v", err)
	}

	executor := NewExecutor(registry, nil, nil)
	if executor.implicitIndex != nil {
		t.Fatalf("index must start nil before refresh")
	}
	executor.RefreshImplicitInvocationIndex()
	if executor.implicitIndex == nil {
		t.Fatalf("refresh must build index from registry summaries")
	}

	got := DetectImplicitInvocations(executor.implicitIndex, "run_shell_command", map[string]interface{}{
		"command": "bash build.sh",
		"cwd":     "/skills/build/scripts",
	})
	if len(got) != 1 || got[0].Name != "build-tool" {
		t.Fatalf("expected 1 invocation of build-tool, got %+v", got)
	}
}

// TestBuildImplicitInvocationIndexWithOptions_IncludesDocumentMode 验证主循环观测口径：
// 文档模式技能在 IncludeDocumentMode 下参与 SKILL.md / scripts 归因，默认口径不参与。
func TestBuildImplicitInvocationIndexWithOptions_IncludesDocumentMode(t *testing.T) {
	summaries := []*SkillSummary{
		makeImplicitSummary("doc-skill", "user", "/a/SKILL.md", "/a", "user", &CodexSkillMetadata{Scope: "user"}),
	}

	if idx := BuildImplicitInvocationIndex(summaries); len(idx.byDocPath) != 0 || len(idx.byScriptsDir) != 0 {
		t.Fatalf("document-mode skill must be excluded by default")
	}

	idx := BuildImplicitInvocationIndexWithOptions(summaries, ImplicitInvocationIndexOptions{IncludeDocumentMode: true})
	read := DetectImplicitInvocations(idx, "read_file", map[string]interface{}{"path": "/a/SKILL.md"})
	if len(read) != 1 || read[0].Name != "doc-skill" || read[0].Basis != ImplicitBasisDocPath {
		t.Fatalf("expected read of SKILL.md to attribute doc-skill, got %+v", read)
	}
	shell := DetectImplicitInvocations(idx, "run_shell_command", map[string]interface{}{"command": "bash run.sh", "cwd": "/a/scripts"})
	if len(shell) != 1 || shell[0].Name != "doc-skill" || shell[0].Basis != ImplicitBasisScriptsDir {
		t.Fatalf("expected scripts cwd to attribute doc-skill, got %+v", shell)
	}
}

// TestSkillInvokedEventPayload 验证事件载荷契约（name/scope/path/kind + 可选 basis/tool）。
func TestSkillInvokedEventPayload(t *testing.T) {
	payload := SkillInvokedEventPayload(ImplicitInvocation{
		Name:  "build-tool",
		Scope: "user",
		Path:  "/skills/build",
		Kind:  InvocationKindImplicit,
		Basis: ImplicitBasisScriptsDir,
		Tool:  "run_shell_command",
	})
	if payload["name"] != "build-tool" || payload["kind"] != "implicit" {
		t.Fatalf("unexpected payload core fields: %+v", payload)
	}
	if payload["basis"] != "scripts_dir" || payload["tool"] != "run_shell_command" {
		t.Fatalf("unexpected payload optional fields: %+v", payload)
	}
	if _, ok := payload["session_id"]; ok {
		t.Fatalf("session_id must be supplied by the caller, not the builder")
	}
}

func TestDetectImplicitInvocations_ScriptsDir(t *testing.T) {
	summaries := []*SkillSummary{
		{
			Name:    "build-tool",
			Handler: noopSkillHandler,
			Source:  &SkillSource{Path: "/skills/build/SKILL.md", Dir: "/skills/build", Layer: "user", Format: SkillSourceFormatCodex},
			Codex:   &CodexSkillMetadata{Scope: "user"},
		},
	}
	idx := BuildImplicitInvocationIndex(summaries)

	cases := []struct {
		name string
		tool string
		args map[string]interface{}
		want int
	}{
		{"cwd in scripts dir", "run_shell_command", map[string]interface{}{"command": "bash build.sh", "cwd": "/skills/build/scripts"}, 1},
		{"cwd in skill root", "run_shell_command", map[string]interface{}{"command": "make", "cwd": "/skills/build"}, 1},
		{"command path in scripts", "bash", map[string]interface{}{"command": "/skills/build/scripts/deploy.sh"}, 1},
		{"unrelated cwd", "run_shell_command", map[string]interface{}{"command": "ls", "cwd": "/tmp"}, 0},
		{"read_file no match", "read_file", map[string]interface{}{"path": "/skills/build/README.md"}, 0},
	}
	for _, tc := range cases {
		got := DetectImplicitInvocations(idx, tc.tool, tc.args)
		if len(got) != tc.want {
			t.Fatalf("%s: expected %d invocation(s), got %d (%+v)", tc.name, tc.want, len(got), got)
		}
		if tc.want == 0 {
			continue
		}
		if got[0].Kind != InvocationKindImplicit {
			t.Fatalf("%s: expected implicit kind, got %q", tc.name, got[0].Kind)
		}
		if got[0].Basis != ImplicitBasisScriptsDir {
			t.Fatalf("%s: expected scripts_dir basis, got %q", tc.name, got[0].Basis)
		}
		if got[0].Tool != tc.tool {
			t.Fatalf("%s: expected tool %q, got %q", tc.name, tc.tool, got[0].Tool)
		}
	}
}

func TestDetectImplicitInvocations_DocPath(t *testing.T) {
	summary := &SkillSummary{
		Name:    "docs-only",
		Handler: noopSkillHandler, // 有 handler → 非文档模式，可索引
		Source:  &SkillSource{Path: "/skills/docs/SKILL.md", Dir: "/skills/docs", Layer: "user", Format: SkillSourceFormatCodex},
		Codex:   &CodexSkillMetadata{Scope: "user"},
	}
	idx := BuildImplicitInvocationIndex([]*SkillSummary{summary})

	got := DetectImplicitInvocations(idx, "read_file", map[string]interface{}{"path": "/skills/docs/SKILL.md"})
	if len(got) != 1 {
		t.Fatalf("expected 1 invocation, got %d", len(got))
	}
	if got[0].Kind != InvocationKindImplicit {
		t.Fatalf("expected implicit kind, got %q", got[0].Kind)
	}
	if got[0].Basis != ImplicitBasisDocPath {
		t.Fatalf("expected doc_path basis, got %q", got[0].Basis)
	}
	if got[0].Tool != "read_file" {
		t.Fatalf("expected tool read_file, got %q", got[0].Tool)
	}
	if got[0].Name != "docs-only" || got[0].Scope != "user" {
		t.Fatalf("unexpected invocation: %+v", got[0])
	}
}

func TestNewExplicitInvocation(t *testing.T) {
	inv := NewExplicitInvocation("  build-tool ", " repo ", " /skills/build ")
	if inv.Kind != InvocationKindExplicit {
		t.Fatalf("expected explicit kind, got %q", inv.Kind)
	}
	if inv.Basis != ImplicitBasisMention {
		t.Fatalf("expected mention basis, got %q", inv.Basis)
	}
	if inv.Name != "build-tool" || inv.Scope != "repo" || inv.Path != "/skills/build" {
		t.Fatalf("expected trimmed fields, got %+v", inv)
	}
}

func TestDetectImplicitInvocations_ToolNameVariants(t *testing.T) {
	summary := &SkillSummary{
		Name:    "variant",
		Handler: noopSkillHandler,
		Source:  &SkillSource{Path: "/skills/v/SKILL.md", Dir: "/skills/v", Layer: "repo", Format: SkillSourceFormatCodex},
		Codex:   &CodexSkillMetadata{Scope: "repo"},
	}
	idx := BuildImplicitInvocationIndex([]*SkillSummary{summary})

	for _, tool := range []string{"run_shell_command", "bash", "mcp__bash", "execute_command", "EXEC"} {
		got := DetectImplicitInvocations(idx, tool, map[string]interface{}{"command": "/skills/v/scripts/run.sh"})
		if len(got) != 1 {
			t.Fatalf("tool %q: expected 1 invocation, got %d", tool, len(got))
		}
	}
}

func TestDetectImplicitInvocations_NilArgs(t *testing.T) {
	summary := &SkillSummary{
		Name:    "nilargs",
		Handler: noopSkillHandler,
		Source:  &SkillSource{Path: "/skills/n/SKILL.md", Dir: "/skills/n", Layer: "user", Format: SkillSourceFormatCodex},
		Codex:   &CodexSkillMetadata{Scope: "user"},
	}
	idx := BuildImplicitInvocationIndex([]*SkillSummary{summary})
	if got := DetectImplicitInvocations(idx, "run_shell_command", nil); len(got) != 0 {
		t.Fatalf("nil args should yield no invocations, got %d", len(got))
	}
	if got := DetectImplicitInvocations(idx, "", map[string]interface{}{"command": "/skills/n/scripts/x.sh"}); len(got) != 0 {
		t.Fatalf("empty tool should yield no invocations, got %d", len(got))
	}
	if got := DetectImplicitInvocations(nil, "read_file", map[string]interface{}{"path": "/skills/n/SKILL.md"}); len(got) != 0 {
		t.Fatalf("nil index should yield no invocations, got %d", len(got))
	}
}

func TestDedupeInvocations(t *testing.T) {
	in := []ImplicitInvocation{
		{Name: "a", Scope: "user", Path: "/a", Kind: InvocationKindImplicit, Basis: ImplicitBasisScriptsDir},
		{Name: "a", Scope: "user", Path: "/a", Kind: InvocationKindImplicit, Basis: ImplicitBasisScriptsDir}, // 重复
		{Name: "b", Scope: "repo", Path: "/b", Kind: InvocationKindImplicit, Basis: ImplicitBasisDocPath},
	}
	got := DedupeInvocations(in)
	if len(got) != 2 {
		t.Fatalf("expected 2 deduped, got %d", len(got))
	}
	if got[0].Name != "a" || got[1].Name != "b" {
		t.Fatalf("unexpected dedupe order: %+v", got)
	}
	if empty := DedupeInvocations(nil); empty != nil {
		t.Fatalf("nil input should yield nil, got %#v", empty)
	}
}

func TestCollectImplicitInvocations_Executor(t *testing.T) {
	summary := &SkillSummary{
		Name:    "exec-skill",
		Handler: noopSkillHandler,
		Source:  &SkillSource{Path: "/skills/exec/SKILL.md", Dir: "/skills/exec", Layer: "user", Format: SkillSourceFormatCodex},
		Codex:   &CodexSkillMetadata{Scope: "user"},
	}
	idx := BuildImplicitInvocationIndex([]*SkillSummary{summary})

	e := &Executor{implicitIndex: idx}
	var implicit []ImplicitInvocation
	calls := []types.ToolCall{
		{Name: "run_shell_command", Args: map[string]interface{}{"command": "/skills/exec/scripts/run.sh"}},
		{Name: "read_file", Args: map[string]interface{}{"path": "/unrelated/file.txt"}},
		{Name: "read_file", Args: map[string]interface{}{"path": "/skills/exec/SKILL.md"}},
	}
	e.collectImplicitInvocations(calls, &implicit)
	if len(implicit) != 2 {
		t.Fatalf("expected 2 raw invocations, got %d (%+v)", len(implicit), implicit)
	}

	result := e.attachImplicitInvocations(&ExecuteResult{SkillName: "wrapper"}, implicit)
	if len(result.ImplicitInvocations) != 2 {
		t.Fatalf("expected 2 attached, got %d", len(result.ImplicitInvocations))
	}

	// nil 索引时静默忽略。
	e2 := &Executor{}
	var buf []ImplicitInvocation
	e2.collectImplicitInvocations(calls, &buf)
	if len(buf) != 0 {
		t.Fatalf("nil index should collect nothing, got %d", len(buf))
	}
}

// SK-3 集成：工具循环里跑技能脚本 → 判定隐式调用并按 turn 去重。
func TestExecutor_ExecuteDefault_CollectsImplicitInvocationsFromToolLoop(t *testing.T) {
	mcp := &toolLoopMCPManager{outputs: map[string]string{"shell": "script output"}}
	provider := &scriptedLLMProvider{responses: []llm.LLMResponse{
		{
			ToolCalls: []types.ToolCall{
				{ID: "call_1", Type: "function", Name: "shell", Args: map[string]interface{}{"command": "bash /skills/build/scripts/run.sh"}},
				{ID: "call_2", Type: "function", Name: "shell", Args: map[string]interface{}{"command": "bash /skills/build/scripts/test.sh"}},
				{ID: "call_3", Type: "function", Name: "shell", Args: map[string]interface{}{"command": "bash /tmp/unrelated.sh"}},
			},
			Model: "scripted-loop",
		},
		{Content: "BUILD_DONE", Model: "scripted-loop"},
	}}
	executor := newToolLoopExecutor(t, mcp, provider)
	summary := &SkillSummary{
		Name:    "build-tool",
		Handler: noopSkillHandler,
		Source:  &SkillSource{Path: "/skills/build/SKILL.md", Dir: "/skills/build", Layer: "user", Format: SkillSourceFormatCodex},
		Codex:   &CodexSkillMetadata{Scope: "user"},
	}
	executor.SetImplicitInvocationIndex(BuildImplicitInvocationIndex([]*SkillSummary{summary}))

	req := types.NewRequest("build it")
	req.Options = map[string]interface{}{"tool_loop": true}
	result, err := executor.Execute(context.Background(), &Skill{
		Name:         "build-tool",
		SystemPrompt: "build the project",
		UserPrompt:   "build it",
	}, req)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, "BUILD_DONE", result.Output)
	require.Equal(t, 3, mcp.callCount())
	// 两次命中同一技能（不同脚本）→ turn 级去重后只记一次；非技能路径不误报。
	require.Len(t, result.ImplicitInvocations, 1)
	inv := result.ImplicitInvocations[0]
	require.Equal(t, "build-tool", inv.Name)
	require.Equal(t, "user", inv.Scope)
	require.Equal(t, InvocationKindImplicit, inv.Kind)
	require.Equal(t, ImplicitBasisScriptsDir, inv.Basis)
	require.Equal(t, "shell", inv.Tool)
}
