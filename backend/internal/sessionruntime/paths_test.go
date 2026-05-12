package sessionruntime

import (
	"os"
	"path/filepath"
	"testing"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

func TestApplyDefaultsServerKeepsMemoryByDefault(t *testing.T) {
	sessionDir := t.TempDir()
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Sessions.Dir = sessionDir

	paths := ApplyDefaults(cfg, ResolveOptions{
		Config: cfg,
		Mode:   ModeServer,
	})

	if paths.FileDefaultsEnabled {
		t.Fatal("expected server mode to keep file defaults disabled")
	}
	if cfg.Sessions.Dir != sessionDir {
		t.Fatalf("expected session dir %q, got %q", sessionDir, cfg.Sessions.Dir)
	}
	if cfg.SessionRuntime.StorePath != "" {
		t.Fatalf("expected session runtime store path to stay empty, got %q", cfg.SessionRuntime.StorePath)
	}
	if cfg.Team.StorePath != "" {
		t.Fatalf("expected team store path to stay empty, got %q", cfg.Team.StorePath)
	}
	if cfg.Artifact.StorePath != "" {
		t.Fatalf("expected artifact store path to stay empty, got %q", cfg.Artifact.StorePath)
	}
	if cfg.Background.StorePath != "" || cfg.Background.LogDir != "" {
		t.Fatalf("expected background paths to stay empty, got store=%q log=%q", cfg.Background.StorePath, cfg.Background.LogDir)
	}
}

func TestApplyDefaultsServerFilePersistenceFillsSharedPaths(t *testing.T) {
	sessionDir := t.TempDir()
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Sessions.Dir = sessionDir
	cfg.SessionRuntime.DefaultPersistence = PersistenceFile

	paths := ApplyDefaults(cfg, ResolveOptions{
		Config: cfg,
		Mode:   ModeServer,
	})

	runtimeDir := filepath.Join(sessionDir, "runtime")
	assertPath(t, paths.SessionRuntimeStorePath, filepath.Join(runtimeDir, "session_runtime.sqlite"))
	assertPath(t, cfg.SessionRuntime.StorePath, filepath.Join(runtimeDir, "session_runtime.sqlite"))
	assertPath(t, cfg.Team.StorePath, filepath.Join(runtimeDir, "team_store.sqlite"))
	assertPath(t, cfg.AgentControl.StorePath, filepath.Join(runtimeDir, "agent_control.sqlite"))
	assertPath(t, paths.AgentControlMailboxStorePath, filepath.Join(runtimeDir, "agent_control.sqlite"))
	assertPath(t, paths.AgentControlAgentStorePath, filepath.Join(runtimeDir, "agent_control.sqlite"))
	assertPath(t, cfg.Artifact.StorePath, filepath.Join(runtimeDir, "artifacts.sqlite"))
	assertPath(t, cfg.Background.StorePath, filepath.Join(runtimeDir, "background.sqlite"))
	assertPath(t, cfg.Background.LogDir, filepath.Join(runtimeDir, "background_logs"))
}

func TestApplyDefaultsCLILocalUsesSharedFilePathsAndLegacyRuntimeStore(t *testing.T) {
	sessionDir := t.TempDir()
	runtimeDir := filepath.Join(sessionDir, "runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatalf("mkdir runtime dir: %v", err)
	}
	legacyPath := filepath.Join(runtimeDir, "chat_runtime.sqlite")
	if err := os.WriteFile(legacyPath, []byte("legacy"), 0o644); err != nil {
		t.Fatalf("write legacy runtime store: %v", err)
	}

	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Sessions.Dir = sessionDir
	paths := ApplyDefaults(cfg, ResolveOptions{
		Config: cfg,
		Mode:   ModeCLILocal,
	})

	if !paths.FileDefaultsEnabled {
		t.Fatal("expected CLI local mode to enable file defaults")
	}
	assertPath(t, cfg.SessionRuntime.StorePath, legacyPath)
	assertPath(t, cfg.Team.StorePath, filepath.Join(runtimeDir, "team_store.sqlite"))
	assertPath(t, cfg.Artifact.StorePath, filepath.Join(runtimeDir, "artifacts.sqlite"))
	assertPath(t, cfg.Background.StorePath, filepath.Join(runtimeDir, "background.sqlite"))
}

func TestResolvePathsServerFileAndCLILocalShareDefaultStores(t *testing.T) {
	sessionDir := t.TempDir()

	serverCfg := runtimecfg.DefaultRuntimeConfig()
	serverCfg.Sessions.Dir = sessionDir
	serverCfg.SessionRuntime.DefaultPersistence = PersistenceFile
	serverPaths := ApplyDefaults(serverCfg, ResolveOptions{
		Config: serverCfg,
		Mode:   ModeServer,
	})

	cliCfg := runtimecfg.DefaultRuntimeConfig()
	cliCfg.Sessions.Dir = sessionDir
	cliPaths := ApplyDefaults(cliCfg, ResolveOptions{
		Config: cliCfg,
		Mode:   ModeCLILocal,
	})

	if !serverPaths.FileDefaultsEnabled || !cliPaths.FileDefaultsEnabled {
		t.Fatalf("expected both modes to use file defaults, server=%#v cli=%#v", serverPaths, cliPaths)
	}
	assertPath(t, serverPaths.SessionDir, cliPaths.SessionDir)
	assertPath(t, serverPaths.SessionRuntimeStorePath, cliPaths.SessionRuntimeStorePath)
	assertPath(t, serverPaths.TeamStorePath, cliPaths.TeamStorePath)
	assertPath(t, serverPaths.AgentControlStorePath, cliPaths.AgentControlStorePath)
	assertPath(t, serverPaths.AgentControlMailboxStorePath, cliPaths.AgentControlMailboxStorePath)
	assertPath(t, serverPaths.AgentControlAgentStorePath, cliPaths.AgentControlAgentStorePath)
	assertPath(t, serverPaths.ArtifactStorePath, cliPaths.ArtifactStorePath)
	assertPath(t, serverPaths.BackgroundStorePath, cliPaths.BackgroundStorePath)
	assertPath(t, serverPaths.BackgroundLogDir, cliPaths.BackgroundLogDir)
}

func TestApplyDefaultsPreservesExplicitDSNs(t *testing.T) {
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Sessions.Dir = t.TempDir()
	cfg.SessionRuntime.StoreDSN = "file:session-runtime?mode=memory&cache=shared"
	cfg.Team.StoreDSN = "file:team?mode=memory&cache=shared"
	cfg.AgentControl.StoreDSN = "file:agent-control?mode=memory&cache=shared"
	cfg.Artifact.StoreDSN = "file:artifact?mode=memory&cache=shared"
	cfg.Background.StoreDSN = "file:background?mode=memory&cache=shared"

	ApplyDefaults(cfg, ResolveOptions{
		Config: cfg,
		Mode:   ModeCLILocal,
	})

	if cfg.SessionRuntime.StorePath != "" {
		t.Fatalf("expected session runtime path to stay empty when DSN is explicit, got %q", cfg.SessionRuntime.StorePath)
	}
	if cfg.Team.StorePath != "" {
		t.Fatalf("expected team path to stay empty when DSN is explicit, got %q", cfg.Team.StorePath)
	}
	if cfg.AgentControl.StorePath != "" {
		t.Fatalf("expected agent control path to stay empty when DSN is explicit, got %q", cfg.AgentControl.StorePath)
	}
	if cfg.Artifact.StorePath != "" {
		t.Fatalf("expected artifact path to stay empty when DSN is explicit, got %q", cfg.Artifact.StorePath)
	}
	if cfg.Background.StorePath != "" {
		t.Fatalf("expected background path to stay empty when DSN is explicit, got %q", cfg.Background.StorePath)
	}
}

func assertPath(t *testing.T, got, want string) {
	t.Helper()
	if filepath.Clean(got) != filepath.Clean(want) {
		t.Fatalf("expected path %q, got %q", want, got)
	}
}
