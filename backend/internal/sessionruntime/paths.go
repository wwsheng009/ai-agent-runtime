package sessionruntime

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

const (
	ModeServer   = "server"
	ModeCLILocal = "cli-local"

	PersistenceMemory = "memory"
	PersistenceFile   = "file"
)

const (
	runtimeDirName             = "runtime"
	sessionRuntimeFileName     = "session_runtime.sqlite"
	legacyChatRuntimeFileName  = "chat_runtime.sqlite"
	teamStoreFileName          = "team_store.sqlite"
	agentControlFileName       = "agent_control.sqlite"
	artifactStoreFileName      = "artifacts.sqlite"
	backgroundStoreFileName    = "background.sqlite"
	backgroundLogDirName       = "background_logs"
	defaultSessionRuntimeOwner = "session-runtime"
)

// ResolveOptions controls shared session runtime path resolution.
type ResolveOptions struct {
	Config     *runtimecfg.RuntimeConfig
	ConfigFile string
	SessionDir string
	Mode       string
}

// ResolvedPaths captures the effective shared session persistence paths.
type ResolvedPaths struct {
	SessionDir                    string `json:"session_dir"`
	RuntimeDir                    string `json:"runtime_dir"`
	SessionRuntimeStorePath       string `json:"session_runtime_store_path,omitempty"`
	LegacySessionRuntimeStorePath string `json:"legacy_session_runtime_store_path,omitempty"`
	TeamStorePath                 string `json:"team_store_path,omitempty"`
	AgentControlStorePath         string `json:"agent_control_store_path,omitempty"`
	AgentControlMailboxStorePath  string `json:"agent_control_mailbox_store_path,omitempty"`
	AgentControlAgentStorePath    string `json:"agent_control_agent_store_path,omitempty"`
	ArtifactStorePath             string `json:"artifact_store_path,omitempty"`
	BackgroundStorePath           string `json:"background_store_path,omitempty"`
	BackgroundLogDir              string `json:"background_log_dir,omitempty"`
	DefaultPersistence            string `json:"default_persistence,omitempty"`
	FileDefaultsEnabled           bool   `json:"file_defaults_enabled"`
}

// ResolvePaths returns the effective shared session runtime paths without
// mutating the supplied config.
func ResolvePaths(opts ResolveOptions) ResolvedPaths {
	cfg := opts.Config
	configFile := strings.TrimSpace(opts.ConfigFile)
	mode := strings.ToLower(strings.TrimSpace(opts.Mode))
	sessionDir := strings.TrimSpace(opts.SessionDir)
	if sessionDir == "" && cfg != nil {
		sessionDir = strings.TrimSpace(cfg.Sessions.Dir)
	}
	if sessionDir == "" {
		sessionDir = aiclipaths.DefaultSessionsDir()
	} else {
		sessionDir = ResolvePath(configFile, sessionDir)
	}
	if sessionDir == "" {
		sessionDir = aiclipaths.DefaultSessionsDir()
	}

	runtimeDir := filepath.Join(sessionDir, runtimeDirName)
	legacyRuntimePath := filepath.Join(runtimeDir, legacyChatRuntimeFileName)
	fileDefaults := ShouldUseFileDefaults(cfg, mode)
	persistence := ""
	if cfg != nil {
		persistence = strings.ToLower(strings.TrimSpace(cfg.SessionRuntime.DefaultPersistence))
	}

	paths := ResolvedPaths{
		SessionDir:                    sessionDir,
		RuntimeDir:                    runtimeDir,
		LegacySessionRuntimeStorePath: legacyRuntimePath,
		DefaultPersistence:            persistence,
		FileDefaultsEnabled:           fileDefaults,
	}
	if persistence == "" && fileDefaults {
		paths.DefaultPersistence = PersistenceFile
	}

	if cfg == nil {
		if fileDefaults {
			applyDefaultFilePaths(&paths)
		}
		return paths
	}

	if path := ResolvePath(configFile, cfg.SessionRuntime.StorePath); path != "" {
		paths.SessionRuntimeStorePath = path
	} else if strings.TrimSpace(cfg.SessionRuntime.StoreDSN) == "" && fileDefaults {
		paths.SessionRuntimeStorePath = defaultSessionRuntimePath(runtimeDir, legacyRuntimePath)
	}

	if path := ResolvePath(configFile, cfg.Team.StorePath); path != "" {
		paths.TeamStorePath = path
	} else if strings.TrimSpace(cfg.Team.StoreDSN) == "" && fileDefaults {
		paths.TeamStorePath = filepath.Join(runtimeDir, teamStoreFileName)
	}

	if path := ResolvePath(configFile, cfg.AgentControl.StorePath); path != "" {
		paths.AgentControlStorePath = path
	} else if agentControlEmpty(cfg) && fileDefaults {
		paths.AgentControlStorePath = filepath.Join(runtimeDir, agentControlFileName)
	}
	if path := ResolvePath(configFile, cfg.AgentControl.MailboxStorePath); path != "" {
		paths.AgentControlMailboxStorePath = path
	}
	if path := ResolvePath(configFile, cfg.AgentControl.AgentStorePath); path != "" {
		paths.AgentControlAgentStorePath = path
	}
	applyAgentControlEffectivePaths(&paths, cfg)

	if path := ResolvePath(configFile, cfg.Artifact.StorePath); path != "" {
		paths.ArtifactStorePath = path
	} else if strings.TrimSpace(cfg.Artifact.StoreDSN) == "" && fileDefaults {
		paths.ArtifactStorePath = filepath.Join(runtimeDir, artifactStoreFileName)
	}

	if path := ResolvePath(configFile, cfg.Background.StorePath); path != "" {
		paths.BackgroundStorePath = path
	} else if strings.TrimSpace(cfg.Background.StoreDSN) == "" && fileDefaults {
		paths.BackgroundStorePath = filepath.Join(runtimeDir, backgroundStoreFileName)
	}
	if path := ResolvePath(configFile, cfg.Background.LogDir); path != "" {
		paths.BackgroundLogDir = path
	} else if fileDefaults {
		paths.BackgroundLogDir = filepath.Join(runtimeDir, backgroundLogDirName)
	}

	return paths
}

// ApplyDefaults mutates config with the resolved paths that should be visible to
// existing constructors. It preserves explicit DSNs and only fills file defaults
// when ResolvePaths says file defaults are enabled.
func ApplyDefaults(config *runtimecfg.RuntimeConfig, opts ResolveOptions) ResolvedPaths {
	if config != nil && opts.Config == nil {
		opts.Config = config
	}
	paths := ResolvePaths(opts)
	if config == nil {
		return paths
	}
	config.Sessions.Dir = paths.SessionDir
	if strings.TrimSpace(config.SessionRuntime.StorePath) == "" && strings.TrimSpace(config.SessionRuntime.StoreDSN) == "" {
		config.SessionRuntime.StorePath = paths.SessionRuntimeStorePath
	} else if strings.TrimSpace(config.SessionRuntime.StorePath) != "" {
		config.SessionRuntime.StorePath = paths.SessionRuntimeStorePath
	}
	if strings.TrimSpace(config.Team.StorePath) == "" && strings.TrimSpace(config.Team.StoreDSN) == "" {
		config.Team.StorePath = paths.TeamStorePath
	} else if strings.TrimSpace(config.Team.StorePath) != "" {
		config.Team.StorePath = paths.TeamStorePath
	}
	if agentControlEmpty(config) && paths.AgentControlStorePath != "" {
		config.AgentControl.StorePath = paths.AgentControlStorePath
	} else {
		if strings.TrimSpace(config.AgentControl.StorePath) != "" {
			config.AgentControl.StorePath = paths.AgentControlStorePath
		}
		if strings.TrimSpace(config.AgentControl.MailboxStorePath) != "" {
			config.AgentControl.MailboxStorePath = paths.AgentControlMailboxStorePath
		}
		if strings.TrimSpace(config.AgentControl.AgentStorePath) != "" {
			config.AgentControl.AgentStorePath = paths.AgentControlAgentStorePath
		}
	}
	if strings.TrimSpace(config.Artifact.StorePath) == "" && strings.TrimSpace(config.Artifact.StoreDSN) == "" {
		config.Artifact.StorePath = paths.ArtifactStorePath
	} else if strings.TrimSpace(config.Artifact.StorePath) != "" {
		config.Artifact.StorePath = paths.ArtifactStorePath
	}
	if strings.TrimSpace(config.Background.StorePath) == "" && strings.TrimSpace(config.Background.StoreDSN) == "" {
		config.Background.StorePath = paths.BackgroundStorePath
	} else if strings.TrimSpace(config.Background.StorePath) != "" {
		config.Background.StorePath = paths.BackgroundStorePath
	}
	if strings.TrimSpace(config.Background.LogDir) == "" {
		config.Background.LogDir = paths.BackgroundLogDir
	} else {
		config.Background.LogDir = paths.BackgroundLogDir
	}
	return paths
}

// ShouldUseFileDefaults reports whether empty stores should be materialized as
// files. CLI local mode keeps its existing durable-local behavior; server mode
// needs explicit opt-in through sessionRuntime.defaultPersistence=file.
func ShouldUseFileDefaults(config *runtimecfg.RuntimeConfig, mode string) bool {
	if strings.EqualFold(strings.TrimSpace(mode), ModeCLILocal) {
		return true
	}
	if config == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(config.SessionRuntime.DefaultPersistence), PersistenceFile)
}

// ResolvePath expands "~" and resolves relative paths against the runtime config
// file directory when available.
func ResolvePath(configFile, target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	target = aiclipaths.ExpandUserPath(target)
	if filepath.IsAbs(target) || strings.TrimSpace(configFile) == "" {
		return filepath.Clean(target)
	}
	baseDir := filepath.Dir(strings.TrimSpace(configFile))
	if baseDir == "" || baseDir == "." {
		return filepath.Clean(target)
	}
	return filepath.Clean(filepath.Join(baseDir, target))
}

func applyDefaultFilePaths(paths *ResolvedPaths) {
	if paths == nil || strings.TrimSpace(paths.RuntimeDir) == "" {
		return
	}
	paths.SessionRuntimeStorePath = defaultSessionRuntimePath(paths.RuntimeDir, paths.LegacySessionRuntimeStorePath)
	paths.TeamStorePath = filepath.Join(paths.RuntimeDir, teamStoreFileName)
	paths.AgentControlStorePath = filepath.Join(paths.RuntimeDir, agentControlFileName)
	paths.AgentControlMailboxStorePath = paths.AgentControlStorePath
	paths.AgentControlAgentStorePath = paths.AgentControlStorePath
	paths.ArtifactStorePath = filepath.Join(paths.RuntimeDir, artifactStoreFileName)
	paths.BackgroundStorePath = filepath.Join(paths.RuntimeDir, backgroundStoreFileName)
	paths.BackgroundLogDir = filepath.Join(paths.RuntimeDir, backgroundLogDirName)
}

func applyAgentControlEffectivePaths(paths *ResolvedPaths, config *runtimecfg.RuntimeConfig) {
	if paths == nil || strings.TrimSpace(paths.AgentControlStorePath) == "" {
		return
	}
	if config == nil {
		if paths.AgentControlMailboxStorePath == "" {
			paths.AgentControlMailboxStorePath = paths.AgentControlStorePath
		}
		if paths.AgentControlAgentStorePath == "" {
			paths.AgentControlAgentStorePath = paths.AgentControlStorePath
		}
		return
	}
	if paths.AgentControlMailboxStorePath == "" && strings.TrimSpace(config.AgentControl.MailboxStoreDSN) == "" {
		paths.AgentControlMailboxStorePath = paths.AgentControlStorePath
	}
	if paths.AgentControlAgentStorePath == "" && strings.TrimSpace(config.AgentControl.AgentStoreDSN) == "" {
		paths.AgentControlAgentStorePath = paths.AgentControlStorePath
	}
}

func defaultSessionRuntimePath(runtimeDir, legacyPath string) string {
	nextPath := filepath.Join(runtimeDir, sessionRuntimeFileName)
	if fileExists(legacyPath) && !fileExists(nextPath) {
		return legacyPath
	}
	return nextPath
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func agentControlEmpty(config *runtimecfg.RuntimeConfig) bool {
	if config == nil {
		return true
	}
	cfg := config.AgentControl
	return strings.TrimSpace(cfg.StorePath) == "" &&
		strings.TrimSpace(cfg.StoreDSN) == "" &&
		strings.TrimSpace(cfg.MailboxStorePath) == "" &&
		strings.TrimSpace(cfg.MailboxStoreDSN) == "" &&
		strings.TrimSpace(cfg.AgentStorePath) == "" &&
		strings.TrimSpace(cfg.AgentStoreDSN) == ""
}

// DefaultSessionRuntimeOwner returns a stable fallback owner kind used by lease
// diagnostics when a caller does not provide one.
func DefaultSessionRuntimeOwner() string {
	return defaultSessionRuntimeOwner
}
