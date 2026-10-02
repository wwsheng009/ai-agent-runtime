package commands

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func newCatalogEntrySession() *ChatSession {
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

// /skills 是只读清单命令：bare /skills 与 /skills list|ls|status 等价，都列出
// 当前会话加载的技能，**不开选择器**。选择是显式动作（/skills select）。
func TestExecuteStructuredSkillsMenuAlwaysListsNeverPicks(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := newCatalogEntrySession()
	for _, command := range []string{"/skills", "/skills list", "/skills ls", "/skills status"} {
		result, handled := executeStructuredSkillsMenuCommand(session, command)
		if !handled {
			t.Fatalf("%s was not handled by the structured executor", command)
		}
		if result.Screen != nil {
			t.Fatalf("%s must not open a picker/screen: %#v", command, result.Screen)
		}
		text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
		if !strings.Contains(text, "Skill Catalog: total=1") || !strings.Contains(text, "imagegen") {
			t.Fatalf("%s must list the session's skills, got:\n%s", command, text)
		}
	}
}

// 清单没有能力门：即使没有可开选择器的交互面，bare /skills 仍然出清单。
func TestExecuteStructuredSkillsMenuListsWithoutPickerSurface(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := newCatalogEntrySession() // 无 Interaction/Surface
	result, handled := executeStructuredSkillsMenuCommand(session, "/skills")
	if !handled {
		t.Fatal("bare /skills was not handled")
	}
	if result.Screen != nil {
		t.Fatalf("bare /skills must not open a picker: %#v", result.Screen)
	}
	text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
	if !strings.Contains(text, "Skill Catalog: total=1") {
		t.Fatalf("bare /skills must always list, got:\n%s", text)
	}
}

// 过滤查询走清单，并标注过滤条件。
func TestExecuteStructuredSkillsMenuQueryFiltersList(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := newCatalogEntrySession()
	result, handled := executeStructuredSkillsMenuCommand(session, "/skills image")
	if !handled {
		t.Fatal("/skills <query> was not handled")
	}
	if result.Screen != nil {
		t.Fatalf("/skills <query> must not open the picker: %#v", result.Screen)
	}
	text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
	if !strings.Contains(text, "Filter: image") || !strings.Contains(text, "imagegen") {
		t.Fatalf("/skills <query> must list the filtered catalog, got:\n%s", text)
	}
}

// 清单必须回显本次加载实际扫描的 skill 根：这是“工作区 .agents/skills /
// 用户 ~/.aicli/skills 里我改的 skill 到底被扫到没有”的唯一现场证据。
func TestExecuteStructuredSkillsMenuShowsLoadedRoots(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := newCatalogEntrySession()
	binding := &skillsRuntimeBinding{
		skillFunctions: map[string]*SkillFunction{},
		roots:          []string{`E:\ws\.agents\skills`, `C:\Users\v\.aicli\skills`},
	}
	session.SkillsBinding = binding
	session.FunctionCatalog.SetSkillsBinding(binding)

	result, handled := executeStructuredSkillsMenuCommand(session, "/skills list")
	if !handled {
		t.Fatal("/skills list was not handled")
	}
	text := ui.RenderDocumentPlain(result.Document())
	for _, want := range []string{`E:\ws\.agents\skills`, `C:\Users\v\.aicli\skills`} {
		if !strings.Contains(text, want) {
			t.Fatalf("catalog must echo loaded root %s, got:\n%s", want, text)
		}
	}
}

// 显式 /skills select 在副屏能力不足时必须给出可见的替代路径，而不是静默返回
// 或假装列了清单。
func TestExecuteStructuredSkillsMenuExplicitSelectWithoutSurfaceIsVisible(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	session := newCatalogEntrySession() // 无 Interaction/Surface
	result, handled := executeStructuredSkillsMenuCommand(session, "/skills select")
	if !handled {
		t.Fatal("/skills select was not handled")
	}
	if result.Screen != nil {
		t.Fatalf("/skills select without a surface must not open a picker: %#v", result.Screen)
	}
	text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
	if !strings.Contains(text, "不支持全屏选择器") || !strings.Contains(text, "/skills 查看技能清单") {
		t.Fatalf("unavailable picker must be visible with an alternative, got:\n%s", text)
	}
}

// user-invocable:false 的技能必须仍出现在清单里。
//
// 口径是"能调用就应该能显示"：实测 /skill <name> 的执行链
// （resolveSkillCallableReference → SendSkillTurn）从不查 UserInvocable()，技能照常
// 注册进函数面，也就是说它一直可以显式调用。清单若把它藏起来，就造出了
// "清单里没有、敲命令能跑"的不一致——正是 /skills 最初要消灭的那类问题。
func TestSkillCatalogListShowsUserInvocableFalseSkills(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	notUserInvocable := false
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)
	// skillFunctionForName 走 skills binding，fixture 必须挂上，否则
	// user-invocable 判定会因 binding 缺失而退化成"全部放行"，测试恒真。
	fn := &SkillFunction{
		functionName: "skill__secret",
		skill: &runtimeskill.Skill{
			Name:        "secret",
			Description: "user-invocable false",
			Codex: &runtimeskill.CodexSkillMetadata{
				UserInvocable: &notUserInvocable,
			},
		},
	}
	catalog.RegisterSkillFunction(fn)
	binding := &skillsRuntimeBinding{skillFunctions: map[string]*SkillFunction{fn.functionName: fn}}
	catalog.SetSkillsBinding(binding)
	session := &ChatSession{FunctionCatalog: catalog, FunctionRegistry: registry, SkillsBinding: binding}

	// 前置：确认这个 fixture 真的造出了 user-invocable:false，否则下面恒真。
	if got := skillFunctionForName(catalog, "skill__secret"); got == nil {
		t.Fatal("fixture skill is not resolvable from the skills binding")
	} else if got.UserInvocable() {
		t.Fatal("fixture must declare user-invocable:false")
	}

	result, handled := executeStructuredSkillsMenuCommand(session, "/skills")
	if !handled {
		t.Fatal("/skills was not handled")
	}
	text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
	if !strings.Contains(text, "Skill Catalog: total=1") || !strings.Contains(text, "secret") {
		t.Fatalf("user-invocable:false skill must stay listed, got:\n%s", text)
	}
}

func TestOpenChatSkillPickerNilSessionIsSafe(t *testing.T) {
	openChatSkillPicker(nil, SkillPickerRequest{})
}
