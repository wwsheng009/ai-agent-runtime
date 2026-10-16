package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// EnsureBuiltinProfiles 是显式补齐入口：缺名物化、已有零碰（用户改过的内置名
// 与自建名都赢）、force 只补缺失文件不改已有文件。本组测试钉死这些语义。
func TestEnsureBuiltinProfilesFillsMissingNames(t *testing.T) {
	root := filepath.Join(t.TempDir(), "profiles")
	if err := os.MkdirAll(filepath.Join(root, "review"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 模拟"旧版本播种过 review"：只放一个空 profile.yaml 占位。
	if err := os.WriteFile(filepath.Join(root, "review", "profile.yaml"), []byte("profile:\n  name: review\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	written, skipped, err := EnsureBuiltinProfiles(root, false)
	if err != nil {
		t.Fatalf("EnsureBuiltinProfiles: %v", err)
	}

	writtenNames := make([]string, 0, len(written))
	for _, entry := range written {
		writtenNames = append(writtenNames, entry.Name)
	}
	// full（以及其它缺失名）被补齐；review 已存在被跳过。
	if len(writtenNames) == 0 || !containsString(writtenNames, "full") {
		t.Fatalf("缺失的 full 应被物化，got %v", writtenNames)
	}
	if !containsString(skipped, "review") {
		t.Fatalf("已存在的 review 应被跳过，got %v", skipped)
	}
	// review 的占位内容零改写。
	raw, err := os.ReadFile(filepath.Join(root, "review", "profile.yaml"))
	if err != nil || string(raw) != "profile:\n  name: review\n" {
		t.Fatalf("已有 review 被改写: %v / %q", err, string(raw))
	}
	// 补齐的 full 目录完整（template.yaml 是模板元数据，不进渲染产物）。
	for _, rel := range []string{"profile.yaml", "agents/default/agent.yaml", "agents/default/prompts/role.md", "README.md"} {
		if _, err := os.Stat(filepath.Join(root, "full", filepath.FromSlash(rel))); err != nil {
			t.Fatalf("full 缺少 %s: %v", rel, err)
		}
	}
	// 二次执行零写入（幂等）。
	written2, _, err := EnsureBuiltinProfiles(root, false)
	if err != nil || len(written2) != 0 {
		t.Fatalf("补齐应幂等，got %d 条 / err=%v", len(written2), err)
	}
}

func TestEnsureBuiltinProfilesForceOnlyFillsMissingFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "profiles")
	if err := os.MkdirAll(filepath.Join(root, "review"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 用户改过的 profile.yaml + 删掉 role.md 模拟旧版本缺文件。
	custom := "profile:\n  name: review\n  description: 用户改过的描述\n"
	if err := os.WriteFile(filepath.Join(root, "review", "profile.yaml"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}

	written, _, err := EnsureBuiltinProfiles(root, true)
	if err != nil {
		t.Fatalf("EnsureBuiltinProfiles(force): %v", err)
	}
	// force 只补缺失文件；用户改过的 profile.yaml 原样保留。
	raw, err := os.ReadFile(filepath.Join(root, "review", "profile.yaml"))
	if err != nil || string(raw) != custom {
		t.Fatalf("force 改写了已有文件: %v / %q", err, string(raw))
	}
	if _, err := os.Stat(filepath.Join(root, "review", "agents", "default", "prompts", "role.md")); err != nil {
		t.Fatalf("force 应补写缺失文件: %v", err)
	}
	filled := false
	for _, entry := range written {
		if entry.Name == "review" {
			filled = true
		}
	}
	if !filled {
		t.Fatalf("force 应把补写了缺失文件的 review 计入 written, got %v", written)
	}
}

func TestEnsureBuiltinProfilesSkipsUserContentWithoutForce(t *testing.T) {
	root := filepath.Join(t.TempDir(), "profiles")
	if err := os.MkdirAll(filepath.Join(root, "my-own"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "my-own", "profile.yaml"), []byte("profile:\n  name: my-own\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, skipped, err := EnsureBuiltinProfiles(root, false)
	if err != nil {
		t.Fatalf("EnsureBuiltinProfiles: %v", err)
	}
	if containsString(skipped, "my-own") {
		t.Fatalf("用户自建名不该出现在内置补齐口径里, got %v", skipped)
	}
	if _, err := os.Stat(filepath.Join(root, "my-own", "agents")); !os.IsNotExist(err) {
		t.Fatalf("用户自建目录被写入了内置文件 (err=%v)", err)
	}
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if strings.TrimSpace(item) == want {
			return true
		}
	}
	return false
}
