package agentconfig

import (
	"os"
	"path/filepath"
	"testing"
)

// 用户只写了 skills_runtime.config_file、没写 enabled 时，必须按文档默认启用。
// 这正是线上 /skills 报 total=0 的真实成因：Enabled 是普通 bool，缺键即 false，
// initSkillFunctionsWithManager 直接 return nil,nil，会话挂不上 skills runtime。
func TestInitGlobalConfigSkillsRuntimeEnabledDefaultsTrueWhenKeyAbsent(t *testing.T) {
	path := writeSkillsEnabledConfig(t, "skills_runtime:\n  config_file: configs/runtime.yaml\n")
	cfg, err := InitGlobalConfig(path)
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}
	if cfg.SkillsRuntime == nil {
		t.Fatal("skills_runtime section must be present")
	}
	if !cfg.SkillsRuntime.Enabled {
		t.Fatal("skills_runtime.enabled must default to true when the key is absent")
	}
	if got := cfg.SkillsRuntime.ConfigFile; got != "configs/runtime.yaml" {
		t.Fatalf("config_file = %q, want it preserved", got)
	}
}

// 反向纪律：用户显式写 enabled: false 时必须仍然是关的。
// 若实现改成无条件赋 true，这条会红——那比原 bug 更糟（显式关闭永久失效）。
func TestInitGlobalConfigSkillsRuntimeExplicitFalseIsPreserved(t *testing.T) {
	path := writeSkillsEnabledConfig(t, "skills_runtime:\n  enabled: false\n  config_file: configs/runtime.yaml\n")
	cfg, err := InitGlobalConfig(path)
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}
	if cfg.SkillsRuntime == nil {
		t.Fatal("skills_runtime section must be present")
	}
	if cfg.SkillsRuntime.Enabled {
		t.Fatal("explicit skills_runtime.enabled: false must not be overwritten by the default")
	}
}

func TestInitGlobalConfigSkillsRuntimeExplicitTrueStaysTrue(t *testing.T) {
	path := writeSkillsEnabledConfig(t, "skills_runtime:\n  enabled: true\n")
	cfg, err := InitGlobalConfig(path)
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}
	if cfg.SkillsRuntime == nil || !cfg.SkillsRuntime.Enabled {
		t.Fatal("explicit skills_runtime.enabled: true must stay true")
	}
}

func TestApplySkillsRuntimeEnabledDefaultIgnoresMissingSection(t *testing.T) {
	path := writeSkillsEnabledConfig(t, "aicli:\n  chat:\n    stream: true\n")
	cfg, err := InitGlobalConfig(path)
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}
	// 没有 skills_runtime 段时不应凭空造一个出来（目录解析仍走各自默认）。
	if cfg.SkillsRuntime != nil {
		t.Fatalf("skills_runtime must stay nil, got %+v", cfg.SkillsRuntime)
	}
}

func writeSkillsEnabledConfig(t *testing.T, body string) string {
	t.Helper()
	// 隔离 HOME：preset 目录（~/.aicli/presets.yaml）若存在会参与合并，
	// 让本用例测到的就不是"纯用户配置"这一条路径了。
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
