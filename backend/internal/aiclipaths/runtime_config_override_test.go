package aiclipaths

import (
	"os"
	"path/filepath"
	"testing"
)

// config_file（skills_runtime.config_file）的语义只有一个：指向 runtime.yaml
// 的显式覆盖指针。空值/约定值 = 「跟随 .aicli 层自动发现」。历史上调用方反复
// 把「原始字符串非空」当判定门，三次把发现/加载分支误关（skills 用户级根、
// runtime tool 配置、chat 持久化配置）。本文件锁定共享判定入口，禁止再散落
// 手写判断。

func TestIsExplicitRuntimeConfigOverride(t *testing.T) {
	notOverrides := []string{
		"",
		"   ",
		".",
		// 约定布局值：从旧模板拷贝来的写法，语义是「跟随 .aicli 层」，不是覆盖。
		DefaultRuntimeConfigFileName,
		DefaultRuntimeConfigRelativePath,
		filepath.FromSlash(DefaultRuntimeConfigRelativePath),
		filepath.FromSlash(filepath.Join("backend", "configs", DefaultRuntimeConfigFileName)),
		"~/foo", // 下方单测覆盖：~ 展开后仍是非约定路径 → 是覆盖；这里仅占位避免遗漏
	}
	// 单独验证 ~ 展开：~/.aicli/runtime.yaml 是约定布局吗？不是——约定层只有
	// 相对布局（configs/...），显式 ~/.aicli 路径属于用户主动指定，算覆盖。
	home := t.TempDir()
	isolateHome(t, home)
	if !IsExplicitRuntimeConfigOverride("~/.aicli/runtime.yaml") {
		t.Fatal("~/.aicli/runtime.yaml must count as an explicit override")
	}
	// 空值与约定值在隔离 HOME 下仍然不是覆盖。
	for _, candidate := range notOverrides {
		if candidate == "~/foo" {
			continue
		}
		if IsExplicitRuntimeConfigOverride(candidate) {
			t.Fatalf("expected %q NOT to be an explicit override", candidate)
		}
	}

	overrides := []string{
		filepath.Join(t.TempDir(), "custom-runtime.yaml"),
		filepath.FromSlash(filepath.Join("my-org", "runtime.yaml")),
		filepath.FromSlash(filepath.Join("configs", otherBuildProfileRuntimeFileName())),
	}
	for _, candidate := range overrides {
		if !IsExplicitRuntimeConfigOverride(candidate) {
			t.Fatalf("expected %q to be an explicit override", candidate)
		}
	}
}

func TestResolveRuntimeConfigFilePath(t *testing.T) {
	home := t.TempDir()
	isolateHome(t, home)
	projectDir := t.TempDir()
	chdirTest(t, projectDir)

	// 1) 无指针：跟随 .aicli 层自动发现（项目层优先）。
	projectConfig := filepath.Join(projectDir, ".aicli", DefaultRuntimeConfigFileName)
	if err := os.MkdirAll(filepath.Dir(projectConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectConfig, []byte("version: \"v1\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ResolveRuntimeConfigFilePath(""); got != projectConfig {
		t.Fatalf("empty pointer must auto-discover project layer, got %q want %q", got, projectConfig)
	}

	// 2) 约定值：同样跟随 .aicli 层，不被当作真实覆盖。
	if got := ResolveRuntimeConfigFilePath(DefaultRuntimeConfigRelativePath); got != projectConfig {
		t.Fatalf("convention value must fall through to auto-discovery, got %q", got)
	}

	// 3) 显式覆盖：即使文件不存在也保留路径（调用方负责报告缺失）。
	missing := filepath.Join(t.TempDir(), "custom", "my-runtime.yaml")
	if got := ResolveRuntimeConfigFilePath(missing); got != filepath.Clean(missing) {
		t.Fatalf("explicit override must be kept even when missing, got %q want %q", got, filepath.Clean(missing))
	}
}

func TestResolveRuntimeConfigAnchorPath(t *testing.T) {
	home := t.TempDir()
	isolateHome(t, home)

	// 1) 无指针/约定值：锚点 = CWD。
	projectDir := t.TempDir()
	chdirTest(t, projectDir)
	if got := ResolveRuntimeConfigAnchorPath(""); got != filepath.Clean(projectDir) {
		t.Fatalf("empty pointer anchor must be CWD, got %q want %q", got, filepath.Clean(projectDir))
	}
	if got := ResolveRuntimeConfigAnchorPath(DefaultRuntimeConfigRelativePath); got != filepath.Clean(projectDir) {
		t.Fatalf("convention value anchor must be CWD, got %q", got)
	}

	// 2) 显式覆盖：锚点 = 覆盖文件所在目录（含不存在的文件，目录语义仍然成立）。
	override := filepath.Join(t.TempDir(), "custom", "my-runtime.yaml")
	want := filepath.Dir(override)
	if got := ResolveRuntimeConfigAnchorPath(override); got != want {
		t.Fatalf("override anchor must be its directory, got %q want %q", got, want)
	}

	// 3) CWD 不可用时退回 HOME（确定性降级，不静默扫错树）。
	// Windows 上无法真正让 Getwd 失败，这里只验证非覆盖路径返回非空。
	if ResolveRuntimeConfigAnchorPath("") == "" {
		t.Fatal("anchor must not be empty when CWD is available")
	}
}
