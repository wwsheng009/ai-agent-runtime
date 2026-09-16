package sessionruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// 显式 background.storePath（服务端 sessionRuntime.defaultPersistence=memory）必须
// 被直接采用，且不得把 Team/AgentControl/Artifact/SessionRuntime 一并级联到共享
// 目录——后者正是历史上 /api/runtime/health 在共享库锁上长挂的触发条件。
func TestResolvePathsKeepsExplicitBackgroundStoreWithoutCascade(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skipf("user home directory unavailable: %v", err)
	}
	configFile := filepath.Join(t.TempDir(), "runtime.yaml")
	wantStore := filepath.Join(home, ".aicli", "sessions", "runtime", "background.sqlite")

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Background.StorePath = "~/.aicli/sessions/runtime/background.sqlite"

	paths := ResolvePaths(ResolveOptions{
		Config:     cfg,
		ConfigFile: configFile,
		Mode:       ModeServer,
	})

	if paths.FileDefaultsEnabled {
		t.Fatalf("server memory persistence must not enable file defaults: %#v", paths)
	}
	if paths.BackgroundStorePath != wantStore {
		t.Fatalf("explicit background storePath must be expanded and kept, want %q got %q", wantStore, paths.BackgroundStorePath)
	}
	if paths.TeamStorePath != "" || paths.AgentControlStorePath != "" || paths.ArtifactStorePath != "" || paths.SessionRuntimeStorePath != "" {
		t.Fatalf("explicit background storePath must not cascade file stores into the shared dir: %#v", paths)
	}

	applied := sessionruntimeApplyForTest(cfg, configFile)
	if applied.Background.StorePath != wantStore {
		t.Fatalf("ApplyDefaults must publish the resolved background path, want %q got %q", wantStore, applied.Background.StorePath)
	}
	if applied.Team.StorePath != "" || applied.AgentControl.StorePath != "" || applied.Artifact.StorePath != "" || applied.SessionRuntime.StorePath != "" {
		t.Fatalf("ApplyDefaults must not cascade other stores: %#v", applied)
	}
}

func sessionruntimeApplyForTest(cfg *runtimecfg.RuntimeConfig, configFile string) *runtimecfg.RuntimeConfig {
	ApplyDefaults(cfg, ResolveOptions{
		Config:     cfg,
		ConfigFile: configFile,
		Mode:       ModeServer,
	})
	return cfg
}

// 发布用的每一份 runtime 配置（主构建 runtime.yaml 与 win7 构建 runtime.win7.yaml）
// 都必须：background 显式指向共享 background.sqlite（作业跨重启保留 + 与 CLI 双向
// 可见），同时 sessionRuntime.defaultPersistence 保持 memory，避免
// Team/AgentControl/Artifact 一并级联到共享目录（历史健康检查长挂的根因）。
// 逐 profile 校验，保证「server/cli 共享」是构建通用的契约而非某个特例。
func TestShippedRuntimeConfigEnablesSharedBackgroundStore(t *testing.T) {
	configsDir := filepath.Join("..", "..", "configs")
	shipped, err := filepath.Glob(filepath.Join(configsDir, "runtime*.yaml"))
	if err != nil {
		t.Fatalf("glob shipped runtime configs: %v", err)
	}
	if len(shipped) == 0 {
		t.Skipf("shipped runtime config unavailable under %s", configsDir)
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		t.Skipf("user home directory unavailable: %v", err)
	}
	want := filepath.Join(home, ".aicli", "sessions", "runtime", "background.sqlite")

	for _, configFile := range shipped {
		configFile := configFile
		t.Run(filepath.Base(configFile), func(t *testing.T) {
			manager := runtimecfg.NewRuntimeManager(configFile)
			if err := manager.Load(); err != nil {
				t.Fatalf("load shipped runtime config %s: %v", configFile, err)
			}
			cfg := manager.Get()
			if cfg == nil {
				t.Fatalf("shipped runtime config %s produced no config", configFile)
			}
			if !strings.EqualFold(strings.TrimSpace(cfg.SessionRuntime.DefaultPersistence), PersistenceMemory) {
				t.Fatalf("%s must keep sessionRuntime.defaultPersistence=memory, got %q", configFile, cfg.SessionRuntime.DefaultPersistence)
			}
			ApplyDefaults(cfg, ResolveOptions{
				Config:     cfg,
				ConfigFile: manager.GetFilePath(),
				Mode:       ModeServer,
			})
			if cfg.Background.StorePath != want {
				t.Fatalf("%s background storePath must resolve to the shared store %q, got %q", configFile, want, cfg.Background.StorePath)
			}
			if cfg.Team.StorePath != "" || cfg.AgentControl.StorePath != "" || cfg.Artifact.StorePath != "" {
				t.Fatalf("%s must not cascade team/agent-control/artifact stores: %#v", configFile, cfg)
			}
		})
	}
}
