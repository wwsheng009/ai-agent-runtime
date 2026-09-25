package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件钉住「内置 profile 首次初始化落盘」的四条纪律：
//  1. 内容与内置模板同源（逐文件字节相同），落盘权限与 `profile create` 一致；
//  2. 只在用户层根还空着时播种（非空层一律不动，删除过的内置项不复活）；
//  3. 落盘产物必须是**可用** profile（可加载 / 可校验 / 可解析出 agent）；
//  4. 写盘失败要回滚本次创建，且不碰层里既有内容。

func TestBuiltinProfileNamesMatchTemplates(t *testing.T) {
	names := BuiltinProfileNames()
	templates := TemplateNames()
	if len(names) == 0 {
		t.Fatal("内置 profile 名单为空（模板目录可能被清空）")
	}
	if strings.Join(names, "\n") != strings.Join(templates, "\n") {
		t.Fatalf("内置 profile 名单必须与模板同源：\n got: %v\nwant: %v", names, templates)
	}
	for _, name := range names {
		if err := ValidateProfileName(name); err != nil {
			t.Fatalf("内置 profile 名 %q 不是合法引用名（会在播种后选不中）：%v", name, err)
		}
	}
}

func TestSeedBuiltinProfilesWritesUsableProfiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".aicli", "profiles")
	seeded, err := seedBuiltinProfilesAt(root)
	if err != nil {
		t.Fatalf("seedBuiltinProfilesAt: %v", err)
	}
	if len(seeded) != len(BuiltinProfileNames()) {
		t.Fatalf("播种条目 = %d, want %d", len(seeded), len(BuiltinProfileNames()))
	}

	for _, entry := range seeded {
		spec, err := LoadProfile(entry.Root)
		if err != nil {
			t.Fatalf("%s: LoadProfile: %v", entry.Name, err)
		}
		if got := strings.TrimSpace(spec.Profile.Name); got != entry.Name {
			t.Fatalf("%s: profile.name = %q, want %q", entry.Name, got, entry.Name)
		}
		if got := strings.TrimSpace(spec.Profile.DefaultAgent); got != TemplateDefaultAgent {
			t.Fatalf("%s: default_agent = %q, want %q", entry.Name, got, TemplateDefaultAgent)
		}
		for _, issue := range ValidateProfileSpec(spec) {
			if issue.Severity == ProfileSpecIssueError {
				t.Fatalf("%s: 落盘后校验失败：%s: %s", entry.Name, issue.Path, issue.Message)
			}
		}
		resolved, err := Resolve(ResolveOptions{Root: entry.Root})
		if err != nil {
			t.Fatalf("%s: Resolve: %v", entry.Name, err)
		}
		if resolved.AgentID != TemplateDefaultAgent {
			t.Fatalf("%s: AgentID = %q, want %q", entry.Name, resolved.AgentID, TemplateDefaultAgent)
		}

		// 与内置模板逐文件字节比对：内置 profile 的"内容源"只能是模板，
		// 否则它会成为第二份需要手工同步的 profile 内容。
		files, err := RenderTemplate(entry.Name, entry.Name, TemplateDefaultAgent)
		if err != nil {
			t.Fatalf("%s: RenderTemplate: %v", entry.Name, err)
		}
		if len(files) != len(entry.Files) {
			t.Fatalf("%s: 落盘文件数 = %d, want %d", entry.Name, len(entry.Files), len(files))
		}
		for rel, want := range files {
			raw, err := os.ReadFile(filepath.Join(entry.Root, filepath.FromSlash(rel)))
			if err != nil {
				t.Fatalf("%s: 读取落盘文件 %s: %v", entry.Name, rel, err)
			}
			if string(raw) != string(want) {
				t.Fatalf("%s: 落盘内容与模板不一致（%s）", entry.Name, rel)
			}
		}
	}
}

// 层里已有任意一个 profile ⇒ 整体不动：不补齐缺失的内置项、不覆盖同名目录。
func TestSeedBuiltinProfilesDoesNotTouchNonEmptyLayer(t *testing.T) {
	root := filepath.Join(t.TempDir(), "profiles")
	userOwned := filepath.Join(root, "coding")
	if err := os.MkdirAll(userOwned, 0o755); err != nil {
		t.Fatal(err)
	}
	custom := "profile:\n  name: coding\n  default_agent: default\n# user-owned\n"
	if err := os.WriteFile(filepath.Join(userOwned, "profile.yaml"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}

	seeded, err := seedBuiltinProfilesAt(root)
	if err != nil {
		t.Fatalf("seedBuiltinProfilesAt: %v", err)
	}
	if len(seeded) != 0 {
		t.Fatalf("层里已有 profile 时不得播种，got %d 条", len(seeded))
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("除用户自建项外不得新增目录，got %v", names)
	}
	raw, err := os.ReadFile(filepath.Join(userOwned, "profile.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != custom {
		t.Fatalf("用户自建 profile 被改写：\n%s", string(raw))
	}
}

// 播种过一次后，用户删掉某个内置 profile ⇒ 不再复活（删除是用户的合法选择）。
func TestSeedBuiltinProfilesDoesNotReviveDeletedEntries(t *testing.T) {
	root := filepath.Join(t.TempDir(), "profiles")
	if _, err := seedBuiltinProfilesAt(root); err != nil {
		t.Fatalf("首次播种: %v", err)
	}
	names := BuiltinProfileNames()
	deleted := names[len(names)-1]
	if err := os.RemoveAll(filepath.Join(root, deleted)); err != nil {
		t.Fatal(err)
	}

	seeded, err := seedBuiltinProfilesAt(root)
	if err != nil {
		t.Fatalf("二次播种: %v", err)
	}
	if len(seeded) != 0 {
		t.Fatalf("层里仍有 profile 时不得播种，got %d 条", len(seeded))
	}
	if hasProfileFile(filepath.Join(root, deleted)) {
		t.Fatalf("已删除的内置 profile %q 被复活", deleted)
	}
}

// 只有"含 profile.yaml 的目录"才算层里已有内容——与 LayerProfiles / profile list
// 的发现口径一致；散落文件或普通目录不该阻止播种，也不该被清理。
func TestSeedBuiltinProfilesIgnoresNonProfileDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), "profiles")
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("layer readme\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	seeded, err := seedBuiltinProfilesAt(root)
	if err != nil {
		t.Fatalf("seedBuiltinProfilesAt: %v", err)
	}
	if len(seeded) != len(BuiltinProfileNames()) {
		t.Fatalf("非 profile 目录不该阻止播种，got %d 条", len(seeded))
	}
	if _, err := os.Stat(filepath.Join(root, "notes")); err != nil {
		t.Fatalf("既有目录被破坏：%v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil || string(raw) != "layer readme\n" {
		t.Fatalf("既有文件被破坏：%v / %q", err, string(raw))
	}
}

// 反证：落盘中途失败必须回滚本次创建，且预先存在的内容原样保留。
func TestSeedBuiltinProfilesRollsBackOnWriteFailure(t *testing.T) {
	root := filepath.Join(t.TempDir(), "profiles")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	names := BuiltinProfileNames()
	blocked := names[len(names)-1] // 字典序最后一个：前面的条目会先写成功
	if err := os.WriteFile(filepath.Join(root, blocked), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := seedBuiltinProfilesAt(root); err == nil {
		t.Fatal("目标位置被同名文件占用时，播种必须报错而不是静默跳过")
	}
	for _, name := range names[:len(names)-1] {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("失败后未回滚 %q（err=%v）", name, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(root, blocked))
	if err != nil {
		t.Fatalf("预先存在的文件被回滚删除了：%v", err)
	}
	if string(raw) != "not a dir" {
		t.Fatalf("预先存在的文件被改写：%q", string(raw))
	}
}

// SeedUserProfiles 必须落在**用户层根**（<home>/.aicli/profiles），并因此被层枚举
// 发现（来源=user）；重复调用无操作。
func TestSeedUserProfilesLandsInUserLayerRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home) // Windows
	t.Setenv("HOME", home)        // 类 Unix
	if resolved, err := os.UserHomeDir(); err != nil || filepath.Clean(resolved) != filepath.Clean(home) {
		t.Skipf("当前平台无法用环境变量重定向 home（got %q, err=%v）", resolved, err)
	}

	layerRoot, err := LayerRoot("user")
	if err != nil {
		t.Fatalf("LayerRoot(user): %v", err)
	}
	if want := filepath.Join(home, ".aicli", "profiles"); layerRoot != want {
		t.Fatalf("用户层根 = %q, want %q", layerRoot, want)
	}

	seeded, err := SeedUserProfiles()
	if err != nil {
		t.Fatalf("SeedUserProfiles: %v", err)
	}
	if len(seeded) != len(BuiltinProfileNames()) {
		t.Fatalf("播种条目 = %d, want %d", len(seeded), len(BuiltinProfileNames()))
	}
	for _, entry := range seeded {
		if !pathWithin(layerRoot, entry.Root) {
			t.Fatalf("播种越界：%q 不在用户层根 %q 内", entry.Root, layerRoot)
		}
	}

	// 发现口径（与 profile list 同源）：project 层指向空工作区，命中必须来自 user 层。
	found := make(map[string]string)
	for _, layerProfile := range LayerProfilesForWorkspace(t.TempDir()) {
		found[layerProfile.Name] = layerProfile.Layer
	}
	for _, name := range BuiltinProfileNames() {
		if got := found[name]; got != "user" {
			t.Fatalf("内置 profile %q 未被 user 层发现（got %q）", name, got)
		}
	}

	again, err := SeedUserProfiles()
	if err != nil {
		t.Fatalf("二次 SeedUserProfiles: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("二次调用不得重复播种，got %d 条", len(again))
	}
}
