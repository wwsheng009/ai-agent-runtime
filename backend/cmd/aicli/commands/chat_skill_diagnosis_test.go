package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/capability"
)

// 清单在拿不到 skill 根时必须把挂载链各道闸的真实取值摊开。四种完全不同的故障
// （enabled=false / 目录解析为空 / NoSkills / DisableTools）以前都渲染成同一个
// "total=0 + <none>"，只能翻配置猜；这里钉住"诊断必须包含真实取值"这条契约。
func TestSkillCatalogRootsExposeMountChainDiagnosis(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	workspace := t.TempDir()
	skillDir := filepath.Join(workspace, ".agents", "skills", "alpha")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\nname: alpha\ndescription: probe\n---\n\nBody.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	t.Chdir(workspace)

	// enabled=false：最常见的"配置没写 enabled"故障。
	session := &ChatSession{
		FunctionRegistry: functions.NewFunctionRegistry(),
		Config: &config.Config{
			SkillsRuntime: &config.SkillsRuntimeConfig{Enabled: false},
		},
	}
	roots := sessionSkillRoots(session)
	if len(roots) != 1 {
		t.Fatalf("roots = %v, want a single diagnosis entry", roots)
	}
	for _, want := range []string{"enabled=false", "no_skills=false", "disable_tools=false", "binding=false", "resolved_dirs="} {
		if !strings.Contains(roots[0], want) {
			t.Fatalf("diagnosis %q must expose %q", roots[0], want)
		}
	}

	// 目录解析为空（enabled=true 但一个 skill 目录都没有）：诊断必须显示
	// enabled=true 而 resolved_dirs=0，这样才和上面那行区分得开。
	empty := t.TempDir()
	t.Chdir(empty)
	emptySession := &ChatSession{
		FunctionRegistry: functions.NewFunctionRegistry(),
		Config: &config.Config{
			SkillsRuntime: &config.SkillsRuntimeConfig{Enabled: true},
		},
	}
	// 隔离 HOME，避免 ~/.aicli 下的真实 skill 目录混进解析结果。
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	emptyRoots := sessionSkillRoots(emptySession)
	if len(emptyRoots) != 1 {
		t.Fatalf("roots = %v, want a single diagnosis entry", emptyRoots)
	}
	if !strings.Contains(emptyRoots[0], "enabled=true") {
		t.Fatalf("diagnosis %q must show enabled=true", emptyRoots[0])
	}

	// NoSkills / DisableTools 两个开关必须各自出现在诊断里。
	session.NoSkills = true
	session.DisableTools = true
	switched := sessionSkillRoots(session)
	if !strings.Contains(switched[0], "no_skills=true") || !strings.Contains(switched[0], "disable_tools=true") {
		t.Fatalf("diagnosis %q must reflect NoSkills/DisableTools", switched[0])
	}
}

// 诊断串只是给人看的排障信息，不得泄漏进 JSON 载荷：--json 消费方是程序，
// 不该因为排障文案变化而破坏契约。
func TestSessionSkillRootsKeepsJSONPathUnchanged(t *testing.T) {
	if got := sessionSkillRoots(nil); len(got) != 1 || !strings.Contains(got[0], "no session") {
		t.Fatalf("nil session diagnosis = %v", got)
	}
}

// 当能力面初始化（discover/attach）失败时，/skills 诊断里必须把 error
// 吐出来——否则用户在“各闸全绿但 binding=nil”的诡异状态里毫无线索。
func TestSkillLoadDiagnosisSurfacesCapabilitiesInitError(t *testing.T) {
	session := &ChatSession{}
	diag := sessionSkillLoadDiagnosis(session)
	if strings.Contains(diag, "capabilities_init_error=") {
		t.Fatalf("nil error must not render capabilities_init_error, got %q", diag)
	}
	session.CapabilitiesInitError = "初始化 actor runtime host 失败: runtime host is nil"
	diag = sessionSkillLoadDiagnosis(session)
	if !strings.Contains(diag, `capabilities_init_error="初始化 actor runtime host 失败: runtime host is nil"`) {
		t.Fatalf("diagnosis must surface the init error verbatim, got %q", diag)
	}
}

// 清单每一条必须恒为一行：描述先被截断到可用宽度；元信息（function=/category=/
// path=）要么完整保留、要么整段省略，绝不能被切在半截（"| function=skil" 这种
// 残片比不显示更糟）。渲染通道在命令行结果外还会加缩进，故预算要预留余量。
func TestFormatSkillCatalogItemLineKeepsDescriptionOnOneLine(t *testing.T) {
	item := aicliFunctionDescriptorReport{
		FunctionName: "skill__aicli",
		Descriptor: &capability.Descriptor{
			Description: "Use when Codex should delegate a bounded task to the local aicli command-line agent, call aicli exec/chat for model reasoning",
			Category:    "automation",
			Metadata: map[string]interface{}{
				"function_name": "skill__aicli",
				"skill_path":    "E:/projects/ai/ai-agent-runtime/.agents/skills/aicli/SKILL.md",
			},
		},
	}
	for _, width := range []int{60, 80, 120, 156} {
		line := formatSkillCatalogItemLine(0, item, 18, width)
		if strings.Contains(line, "\n") {
			t.Fatalf("width=%d: line must be single-line, got %q", width, line)
		}
		budget := width - skillCatalogLineReserve
		if got := ui.DisplayWidth(line); got > budget {
			t.Fatalf("width=%d: line width %d exceeds budget %d, line=%q", width, got, budget, line)
		}
		if idx := strings.Index(line, "| function="); idx >= 0 && !strings.Contains(line, "function=skill__aicli") {
			t.Fatalf("width=%d: meta truncated mid-token, line=%q", width, line)
		}
	}

	// 窄到放不下元信息：描述仍在，元信息整段消失（不给残片）。
	narrow := formatSkillCatalogItemLine(0, item, 18, 60)
	if !strings.Contains(narrow, "Use when Codex") {
		t.Fatalf("narrow line must still show the description, got %q", narrow)
	}
	if strings.Contains(narrow, "| function=") {
		t.Fatalf("narrow line must drop meta entirely, got %q", narrow)
	}

	// 放得下：元信息完整保留。
	wide := formatSkillCatalogItemLine(0, item, 18, 156)
	if !strings.Contains(wide, "function=skill__aicli") || !strings.Contains(wide, "category=automation") {
		t.Fatalf("wide line must keep meta intact, got %q", wide)
	}
}
