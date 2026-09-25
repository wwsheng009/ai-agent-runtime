package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// Batch 13 G4：`/profile` 逐段补全测试（chat_profile_completion.go）。
// 覆盖：一级子命令、引用位 profile 名（只收可解析项）、旗标与枚举值、
// 自由文本位不伪造候选（弹窗关闭）。

func TestChatSlashArgumentCompletionProfileSubcommands(t *testing.T) {
	t.Parallel()

	controller := newChatSlashCompletionController(&ChatSession{})
	controller.UpdateAt("/profile ", len([]rune("/profile ")))
	if !controller.state.Active || !controller.state.Context.InArguments {
		t.Fatalf("expected /profile args popup to be active, got %#v", controller.state)
	}
	for _, command := range []string{"status", "list", "show", "diff", "use", "pick", "reload", "off", "save", "help"} {
		if !containsSlashCandidate(controller.state.Candidates, command) {
			t.Fatalf("expected /profile candidates to include %q, got %#v", command, controller.state.Candidates)
		}
	}
	if containsSlashCandidate(controller.state.Candidates, "--to") {
		t.Fatalf("did not expect flags at the subcommand position, got %#v", controller.state.Candidates)
	}

	controller.UpdateAt("/profile li", len([]rune("/profile li")))
	if !containsSlashCandidate(controller.state.Candidates, "list") {
		t.Fatalf("expected /profile li to complete list, got %#v", controller.state.Candidates)
	}
	nextText, nextCursor, ok := controller.ApplyCompletion("/profile li", len([]rune("/profile li")))
	if !ok {
		t.Fatal("expected /profile li completion to be accepted")
	}
	if nextText != "/profile list" || nextCursor != len([]rune("/profile list")) {
		t.Fatalf("expected /profile list without forced trailing space, got %q cursor=%d", nextText, nextCursor)
	}
}

func TestChatSlashArgumentCompletionProfileUseRefs(t *testing.T) {
	t.Parallel()

	profilesRoot := t.TempDir()
	writeProfileCommandFixtures(t, profilesRoot)
	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()

	controller := newChatSlashCompletionController(session)
	controller.UpdateAt("/profile use ", len([]rune("/profile use ")))
	if !controller.state.Active || !controller.state.Context.InArguments {
		t.Fatalf("expected /profile use ref popup to be active, got %#v", controller.state)
	}
	for _, name := range []string{"coding", "minimal"} {
		if !containsSlashCandidate(controller.state.Candidates, name) {
			t.Fatalf("expected /profile use candidates to include %q, got %#v", name, controller.state.Candidates)
		}
	}
	if containsSlashCandidate(controller.state.Candidates, "--to") {
		t.Fatalf("did not expect flags at the ref position, got %#v", controller.state.Candidates)
	}

	controller.UpdateAt("/profile use co", len([]rune("/profile use co")))
	if !containsSlashCandidate(controller.state.Candidates, "coding") {
		t.Fatalf("expected /profile use co to complete coding, got %#v", controller.state.Candidates)
	}
	nextText, nextCursor, ok := controller.ApplyCompletion("/profile use co", len([]rune("/profile use co")))
	if !ok {
		t.Fatal("expected /profile use co completion to be accepted")
	}
	if nextText != "/profile use coding" || nextCursor != len([]rune("/profile use coding")) {
		t.Fatalf("expected /profile use coding, got %q cursor=%d", nextText, nextCursor)
	}

	controller.UpdateAt("/profile show mi", len([]rune("/profile show mi")))
	if !containsSlashCandidate(controller.state.Candidates, "minimal") {
		t.Fatalf("expected /profile show mi to complete minimal, got %#v", controller.state.Candidates)
	}
}

// 引用位只给可解析项：缺失 root 与解析失败的 profile 不进候选（与前端菜单同纪律）。
func TestChatSlashArgumentCompletionProfileRefsSkipUnresolvable(t *testing.T) {
	t.Parallel()

	profilesRoot := t.TempDir()
	writeProfileCommandFixtures(t, profilesRoot)
	brokenRoot := filepath.Join(profilesRoot, "broken")
	if err := os.MkdirAll(brokenRoot, 0o755); err != nil {
		t.Fatalf("mkdir broken: %v", err)
	}
	// YAML 不允许用 tab 缩进，这里制造一个"存在但解析不了"的 profile。
	if err := os.WriteFile(filepath.Join(brokenRoot, "profile.yaml"), []byte("profile:\n\tname: broken\n"), 0o644); err != nil {
		t.Fatalf("write broken profile: %v", err)
	}

	session, cleanup := newProfileSwitchTestSession(t, profilesRoot)
	defer cleanup()
	session.Config.Profiles.Items = map[string]config.ProfileConfig{
		"ghost": {Root: filepath.Join(profilesRoot, "missing")},
	}

	controller := newChatSlashCompletionController(session)
	controller.UpdateAt("/profile use ", len([]rune("/profile use ")))
	for _, name := range []string{"coding", "minimal"} {
		if !containsSlashCandidate(controller.state.Candidates, name) {
			t.Fatalf("expected /profile use candidates to include %q, got %#v", name, controller.state.Candidates)
		}
	}
	for _, name := range []string{"ghost", "broken"} {
		if containsSlashCandidate(controller.state.Candidates, name) {
			t.Fatalf("did not expect unresolvable profile %q in ref candidates, got %#v", name, controller.state.Candidates)
		}
	}
}

func TestChatSlashArgumentCompletionProfileFlagsAndValues(t *testing.T) {
	t.Parallel()

	controller := newChatSlashCompletionController(&ChatSession{})

	controller.UpdateAt("/profile save --", len([]rune("/profile save --")))
	for _, command := range []string{"--to", "--yes"} {
		if !containsSlashCandidate(controller.state.Candidates, command) {
			t.Fatalf("expected /profile save candidates to include %q, got %#v", command, controller.state.Candidates)
		}
	}

	controller.UpdateAt("/profile save --to ", len([]rune("/profile save --to ")))
	for _, layer := range []string{chatRoutingLayerSession, chatRoutingLayerWorkspace, chatRoutingLayerConfig} {
		if !containsSlashCandidate(controller.state.Candidates, layer) {
			t.Fatalf("expected /profile save --to candidates to include %q, got %#v", layer, controller.state.Candidates)
		}
	}

	controller.UpdateAt("/profile create --template ", len([]rune("/profile create --template ")))
	for _, template := range profilesys.TemplateNames() {
		if !containsSlashCandidate(controller.state.Candidates, template) {
			t.Fatalf("expected /profile create --template candidates to include %q, got %#v", template, controller.state.Candidates)
		}
	}

	// 生命周期命令的 --to 是 user|project（与 save 的 session|workspace|config 不同层）。
	controller.UpdateAt("/profile move coding --to ", len([]rune("/profile move coding --to ")))
	for _, layer := range []string{"user", "project"} {
		if !containsSlashCandidate(controller.state.Candidates, layer) {
			t.Fatalf("expected /profile move --to candidates to include %q, got %#v", layer, controller.state.Candidates)
		}
	}
	if containsSlashCandidate(controller.state.Candidates, chatRoutingLayerSession) {
		t.Fatalf("did not expect session layer for /profile move, got %#v", controller.state.Candidates)
	}

	// `--flag=value` 等号形态与空格形态同源（取值前缀匹配）。
	controller.UpdateAt("/profile create --template=re", len([]rune("/profile create --template=re")))
	if !containsSlashCandidate(controller.state.Candidates, "review") {
		t.Fatalf("expected /profile create --template=re to complete review, got %#v", controller.state.Candidates)
	}
}

// 自由文本位（rename 的新名字、--out/--name 取值）不做枚举：返回 nil 让弹窗关闭。
func TestChatSlashArgumentCompletionProfileFreeFormClosesPopup(t *testing.T) {
	t.Parallel()

	controller := newChatSlashCompletionController(&ChatSession{})

	controller.UpdateAt("/profile rename coding ", len([]rune("/profile rename coding ")))
	if controller.state.Active {
		t.Fatalf("expected free-form rename target to close the popup, got %#v", controller.state)
	}

	controller.UpdateAt("/profile export coding --out ", len([]rune("/profile export coding --out ")))
	if controller.state.Active {
		t.Fatalf("expected free-form --out value to close the popup, got %#v", controller.state)
	}

	controller.UpdateAt("/profile import pkg.zip --name ", len([]rune("/profile import pkg.zip --name ")))
	if controller.state.Active {
		t.Fatalf("expected free-form --name value to close the popup, got %#v", controller.state)
	}
}

// 无活动会话 / 无 config 时不 panic：目录发现为空，弹窗关闭而不是伪造候选。
//
// HOME 隔离：层发现（G1/G2）会读 user 层根 <home>/.aicli/profiles，不隔离就会把
// 开发机上的真实 profile 当成候选；t.Setenv 与 t.Parallel 互斥，故本用例不并行。
func TestChatSlashArgumentCompletionProfileWithoutSession(t *testing.T) {
	useTemporaryHome(t)

	controller := newChatSlashCompletionController(&ChatSession{})
	controller.UpdateAt("/profile use ", len([]rune("/profile use ")))
	if controller.state.Active {
		t.Fatalf("expected empty profile catalog to close the ref popup, got %#v", controller.state)
	}

	if candidates := completeProfileSlashArgs(nil, "use ", len([]rune("use "))); len(candidates) != 0 {
		t.Fatalf("expected nil session to yield no ref candidates, got %#v", candidates)
	}
	if !strings.Contains(chatProfileUsageText(), "/profile use <name>") {
		t.Fatal("/profile 用法文本应保持为补全候选的语义来源")
	}
}
