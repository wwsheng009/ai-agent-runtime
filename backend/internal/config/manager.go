package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	runtimecontext "github.com/ai-gateway/ai-agent-runtime/internal/contextmgr"
	"github.com/ai-gateway/ai-agent-runtime/internal/errors"
	runtimeexecutor "github.com/ai-gateway/ai-agent-runtime/internal/executor"
	runtimehooks "github.com/ai-gateway/ai-agent-runtime/internal/hooks"
	"gopkg.in/yaml.v3"
)

// RuntimeConfig Skills Runtime 配置
type RuntimeConfig struct {
	mu sync.RWMutex

	Version string `yaml:"version,omitempty" json:"version,omitempty"`

	// Skills 引擎配置
	Agent          AgentConfig               `yaml:"agent" json:"agent"`
	Router         RouterConfig              `yaml:"router" json:"router"`
	Embedding      EmbeddingConfig           `yaml:"embedding" json:"embedding"`
	Workspace      WorkspaceConfig           `yaml:"workspace" json:"workspace"`
	Context        ContextConfig             `yaml:"context" json:"context"`
	Catalog        CatalogConfig             `yaml:"catalog" json:"catalog"`
	Team           TeamConfig                `yaml:"team" json:"team"`
	Trace          TraceConfig               `yaml:"trace" json:"trace"`
	Hooks          []runtimehooks.HookConfig `yaml:"hooks,omitempty" json:"hooks,omitempty"`
	SessionRuntime SessionRuntimeConfig      `yaml:"sessionRuntime" json:"sessionRuntime"`
	Checkpoint     CheckpointConfig          `yaml:"checkpoint" json:"checkpoint"`
	Background     BackgroundConfig          `yaml:"background" json:"background"`

	// 性能配置
	Performance PerformanceConfig `yaml:"performance" json:"performance"`

	// Sandbox 配置
	Sandbox runtimeexecutor.SandboxConfig `yaml:"sandbox" json:"sandbox"`

	// 热加载配置
	HotReload HotReloadConfig `yaml:"hotReload" json:"hotReload"`

	// Rollout 配置
	Rollout RolloutConfig `yaml:"rollout" json:"rollout"`
}

// AgentConfig Agent 配置
type AgentConfig struct {
	MaxMaxSteps         int           `yaml:"maxSteps" json:"maxSteps"`
	DefaultProvider     string        `yaml:"defaultProvider,omitempty" json:"defaultProvider,omitempty"`
	DefaultModel        string        `yaml:"defaultModel" json:"defaultModel"`
	EnableMemory        bool          `yaml:"enableMemory" json:"enableMemory"`
	EnablePlanning      bool          `yaml:"enablePlanning" json:"enablePlanning"`
	EnableRouter        bool          `yaml:"enableRouter" json:"enableRouter"`
	MaxMemorySize       int           `yaml:"maxMemorySize" json:"maxMemorySize"`
	Timeout             time.Duration `yaml:"timeout" json:"timeout"`
	DefaultPlanningMode string        `yaml:"defaultPlanningMode,omitempty" json:"defaultPlanningMode,omitempty"`
}

// RouterConfig 路由器配置
type RouterConfig struct {
	MinScore        float64 `yaml:"minScore" json:"minScore"`
	MaxResults      int     `yaml:"maxResults" json:"maxResults"`
	CaseSensitive   bool    `yaml:"caseSensitive" json:"caseSensitive"`
	EnableEmbedding bool    `yaml:"enableEmbedding" json:"enableEmbedding"`
	Threshold       float32 `yaml:"threshold" json:"threshold"`
	TopK            int     `yaml:"topK" json:"topK"`
}

// EmbeddingConfig Embedding 配置
type EmbeddingConfig struct {
	Enabled   bool    `yaml:"enabled" json:"enabled"`
	Provider  string  `yaml:"provider" json:"provider"`
	Model     string  `yaml:"model" json:"model"`
	VectorDim int     `yaml:"vectorDim" json:"vectorDim"`
	BatchSize int     `yaml:"batchSize" json:"batchSize"`
	CacheSize int     `yaml:"cacheSize" json:"cacheSize"`
	Threshold float32 `yaml:"threshold" json:"threshold"`
	TopK      int     `yaml:"topK" json:"topK"`
}

// WorkspaceConfig 工作区配置
type WorkspaceConfig struct {
	Root         string   `yaml:"root" json:"root"`
	Exclude      []string `yaml:"exclude" json:"exclude"`
	Include      []string `yaml:"include" json:"include"`
	MaxFileSize  int64    `yaml:"maxFileSize" json:"maxFileSize"`
	MaxChunkSize int      `yaml:"maxChunkSize" json:"maxChunkSize"`
	ChunkOverlap int      `yaml:"chunkOverlap" json:"chunkOverlap"`
}

type ContextConfig struct {
	Profile               string `yaml:"profile" json:"profile"`
	CompactionMode        string `yaml:"compactionMode" json:"compactionMode"`
	RecallMode            string `yaml:"recallMode" json:"recallMode"`
	ObservationMode       string `yaml:"observationMode" json:"observationMode"`
	MinCompactionMessages int    `yaml:"minCompactionMessages" json:"minCompactionMessages"`
	MinRecallQueryLength  int    `yaml:"minRecallQueryLength" json:"minRecallQueryLength"`
	LedgerLoadLimit       int    `yaml:"ledgerLoadLimit" json:"ledgerLoadLimit"`
	MaxPromptTokens       int    `yaml:"maxPromptTokens" json:"maxPromptTokens"`
	MaxMessages           int    `yaml:"maxMessages" json:"maxMessages"`
	KeepRecentMessages    int    `yaml:"keepRecentMessages" json:"keepRecentMessages"`
	MaxRecallResults      int    `yaml:"maxRecallResults" json:"maxRecallResults"`
	MaxObservationItems   int    `yaml:"maxObservationItems" json:"maxObservationItems"`
}

type CatalogConfig struct {
	Backend      string `yaml:"backend" json:"backend"`
	SnapshotPath string `yaml:"snapshotPath" json:"snapshotPath"`
}

// TraceConfig configures runtime trace exports.
type TraceConfig struct {
	TeamIDLimit int `yaml:"teamIdLimit" json:"teamIdLimit"`
}

// TeamConfig configures the team store persistence.
type TeamConfig struct {
	StorePath string `yaml:"storePath" json:"storePath"`
	StoreDSN  string `yaml:"storeDSN" json:"storeDSN"`
}

// SessionRuntimeConfig configures persistence for session runtime state/events.
type SessionRuntimeConfig struct {
	StorePath string `yaml:"storePath" json:"storePath"`
	StoreDSN  string `yaml:"storeDSN" json:"storeDSN"`
}

// CheckpointConfig controls checkpoint capture behavior.
type CheckpointConfig struct {
	Enabled      bool  `yaml:"enabled" json:"enabled"`
	MaxFileBytes int64 `yaml:"maxFileBytes" json:"maxFileBytes"`
}

// BackgroundConfig controls background task persistence.
type BackgroundConfig struct {
	StorePath         string        `yaml:"storePath" json:"storePath"`
	StoreDSN          string        `yaml:"storeDSN" json:"storeDSN"`
	LogDir            string        `yaml:"logDir" json:"logDir"`
	MaxOutputBytes    int           `yaml:"maxOutputBytes" json:"maxOutputBytes"`
	MaxConcurrentJobs int           `yaml:"maxConcurrentJobs" json:"maxConcurrentJobs"`
	DefaultTimeout    time.Duration `yaml:"defaultTimeout" json:"defaultTimeout"`
}

// PerformanceConfig 性能配置
type PerformanceConfig struct {
	MaxConcurrency int           `yaml:"maxConcurrency" json:"maxConcurrency"`
	RequestTimeout time.Duration `yaml:"requestTimeout" json:"requestTimeout"`
	ToolTimeout    time.Duration `yaml:"toolTimeout" json:"toolTimeout"`
	CacheEnabled   bool          `yaml:"cacheEnabled" json:"cacheEnabled"`
	CacheTTL       time.Duration `yaml:"cacheTTL" json:"cacheTTL"`
}

// HotReloadConfig 热加载配置
type HotReloadConfig struct {
	Enabled       bool          `yaml:"enabled" json:"enabled"`
	CheckInterval time.Duration `yaml:"checkInterval" json:"checkInterval"`
	DebounceDelay time.Duration `yaml:"debounceDelay" json:"debounceDelay"`
	WatchPatterns []string      `yaml:"watchPatterns" json:"watchPatterns"`
}

// RolloutConfig 运行时配置灰度发布配置
type RolloutConfig struct {
	Enabled          bool          `yaml:"enabled" json:"enabled"`
	Mode             string        `yaml:"mode" json:"mode"` // canary | progressive
	Percent          int           `yaml:"percent" json:"percent"`
	CandidateVersion string        `yaml:"candidateVersion,omitempty" json:"candidateVersion,omitempty"`
	CandidateFile    string        `yaml:"candidateFile,omitempty" json:"candidateFile,omitempty"`
	StartedAt        time.Time     `yaml:"startedAt,omitempty" json:"startedAt,omitempty"`
	Duration         time.Duration `yaml:"duration,omitempty" json:"duration,omitempty"`
}

// RuntimeManager Runtime 配置管理器
type RuntimeManager struct {
	mu        sync.RWMutex
	config    *RuntimeConfig
	filePath  string
	watchers  []RuntimeWatcher
	history   []RuntimeConfigSnapshot
	candidate *RuntimeConfig
}

// RuntimeWatcher Runtime 配置变更监听器
type RuntimeWatcher interface {
	OnRuntimeConfigChange(config *RuntimeConfig) error
}

// NewRuntimeManager 创建 Runtime 配置管理器
func NewRuntimeManager(filePath string) *RuntimeManager {
	config := DefaultRuntimeConfig()

	return &RuntimeManager{
		config:   config,
		filePath: filePath,
	}
}

// DefaultRuntimeConfig 默认 Runtime 配置
func DefaultRuntimeConfig() *RuntimeConfig {
	return &RuntimeConfig{
		Agent: AgentConfig{
			MaxMaxSteps:         10,
			DefaultProvider:     "",
			DefaultModel:        "claude-3-5-sonnet",
			EnableMemory:        true,
			EnablePlanning:      true,
			EnableRouter:        true,
			MaxMemorySize:       1000,
			Timeout:             5 * time.Minute,
			DefaultPlanningMode: "",
		},
		Router: RouterConfig{
			MinScore:        0.0,
			MaxResults:      5,
			CaseSensitive:   false,
			EnableEmbedding: true,
			Threshold:       0.5,
			TopK:            5,
		},
		Embedding: EmbeddingConfig{
			Enabled:   true,
			Provider:  "local",
			Model:     "local-hash-v1",
			VectorDim: 768,
			BatchSize: 100,
			CacheSize: 10000,
			Threshold: 0.5,
			TopK:      5,
		},
		Workspace: WorkspaceConfig{
			Root:         ".",
			Exclude:      []string{"node_modules", "vendor", ".git", "dist", "build", "__pycache__"},
			Include:      []string{"*.go", "*.py", "*.js", "*.ts", "*.java", "*.rs", "*.c", "*.cpp"},
			MaxFileSize:  10 * 1024 * 1024, // 10MB
			MaxChunkSize: 5000,
			ChunkOverlap: 200,
		},
		Context: ContextConfig{
			Profile: runtimecontext.BudgetProfileBalanced,
		},
		Catalog: CatalogConfig{
			Backend: "memory",
		},
		Trace: TraceConfig{
			TeamIDLimit: 0,
		},
		Team:           TeamConfig{},
		SessionRuntime: SessionRuntimeConfig{},
		Checkpoint: CheckpointConfig{
			Enabled:      true,
			MaxFileBytes: 1 * 1024 * 1024,
		},
		Background: BackgroundConfig{
			MaxOutputBytes:    1 * 1024 * 1024,
			MaxConcurrentJobs: 2,
		},
		Performance: PerformanceConfig{
			MaxConcurrency: 10,
			RequestTimeout: 30 * time.Second,
			ToolTimeout:    60 * time.Second,
			CacheEnabled:   true,
			CacheTTL:       5 * time.Minute,
		},
		Sandbox: runtimeexecutor.SandboxConfig{
			Enabled:          false,
			MaxExecutionTime: 30 * time.Second,
		},
		HotReload: HotReloadConfig{
			Enabled:       true,
			CheckInterval: 5 * time.Second,
			DebounceDelay: 1 * time.Second,
			WatchPatterns: []string{"*.yaml", "*.yml", "skill.yaml"},
		},
		Rollout: RolloutConfig{
			Enabled: false,
			Mode:    "canary",
			Percent: 0,
		},
	}
}

// Load 从文件加载 Runtime 配置
func (rm *RuntimeManager) Load() error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if rm.filePath == "" {
		return nil // 无配置文件，使用默认值
	}

	data, err := os.ReadFile(rm.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 文件不存在，使用默认配置
		}
		return errors.Wrap(errors.ErrConfigNotFound,
			fmt.Sprintf("failed to read config file: %s", rm.filePath), err)
	}

	config := DefaultRuntimeConfig()
	ext := filepath.Ext(rm.filePath)

	switch ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, config); err != nil {
			return errors.Wrap(errors.ErrConfigInvalid, "failed to parse YAML config", err)
		}
	case ".json":
		if err := json.Unmarshal(data, config); err != nil {
			return errors.Wrap(errors.ErrConfigInvalid, "failed to parse JSON config", err)
		}
	default:
		return errors.New(errors.ErrConfigInvalid, fmt.Sprintf("unsupported config format: %s", ext))
	}

	// 验证配置
	if err := ValidateRuntimeConfig(config); err != nil {
		return err
	}
	ensureRuntimeConfigVersion(config)

	rm.config = config
	if err := rm.loadCandidateLocked(); err != nil {
		return err
	}
	rm.recordHistoryLocked(config)
	return nil
}

// Save 保存配置到文件
func (rm *RuntimeManager) Save() error {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	if rm.filePath == "" {
		return errors.New(errors.ErrConfigNotFound, "config file path not set")
	}

	var data []byte
	var err error

	ext := filepath.Ext(rm.filePath)
	switch ext {
	case ".yaml", ".yml":
		data, err = yaml.Marshal(rm.config)
	case ".json":
		data, err = json.MarshalIndent(rm.config, "", "  ")
	default:
		return errors.New(errors.ErrConfigInvalid, fmt.Sprintf("unsupported config format: %s", ext))
	}

	if err != nil {
		return errors.Wrap(errors.ErrConfigInvalid, "failed to marshal config", err)
	}

	// 确保目录存在
	dir := filepath.Dir(rm.filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return errors.Wrap(errors.ErrConfigNotFound, "failed to create config directory", err)
	}

	if err := os.WriteFile(rm.filePath, data, 0644); err != nil {
		return errors.Wrap(errors.ErrConfigInvalid, fmt.Sprintf("failed to write config: %s", rm.filePath), err)
	}

	return nil
}

// Get 获取配置（返回副本）
func (rm *RuntimeManager) Get() *RuntimeConfig {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	configCopy := *rm.config
	return &configCopy
}

// GetFilePath 获取配置文件路径
func (rm *RuntimeManager) GetFilePath() string {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.filePath
}

// SetFilePath 设置配置文件路径
func (rm *RuntimeManager) SetFilePath(path string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	rm.filePath = path
}

// Update 更新配置
func (rm *RuntimeManager) Update(config *RuntimeConfig) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	// 验证配置
	if err := ValidateRuntimeConfig(config); err != nil {
		return err
	}

	ensureRuntimeConfigVersion(config)
	rm.config = config
	if err := rm.loadCandidateLocked(); err != nil {
		return err
	}
	rm.recordHistoryLocked(config)

	// 通知监听器
	for _, watcher := range rm.watchers {
		if err := watcher.OnRuntimeConfigChange(rm.config); err != nil {
			fmt.Printf("Warning: config watcher failed: %v\n", err)
		}
	}

	return nil
}

// AddWatcher 添加配置变更监听器
func (rm *RuntimeManager) AddWatcher(watcher RuntimeWatcher) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	rm.watchers = append(rm.watchers, watcher)
}

// ListHistory 返回版本历史（最新在最后）
func (rm *RuntimeManager) ListHistory() []RuntimeConfigSnapshot {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	result := make([]RuntimeConfigSnapshot, len(rm.history))
	copy(result, rm.history)
	return result
}

// SelectConfigForScope 根据 scopeKey 选择配置（支持 rollout）
func (rm *RuntimeManager) SelectConfigForScope(scopeKey string) *RuntimeConfig {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	active := rm.config
	if active == nil {
		return nil
	}
	if !active.Rollout.Enabled || rm.candidate == nil {
		return active
	}
	if !shouldUseCandidate(scopeKey, active.Rollout) {
		return active
	}
	return rm.candidate
}

// GetVersion 获取指定版本的配置快照
func (rm *RuntimeManager) GetVersion(version string) (*RuntimeConfigSnapshot, bool) {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	for _, item := range rm.history {
		if item.Version == version {
			return &item, true
		}
	}
	return nil, false
}

// Rollback 将运行时配置回滚到指定版本
func (rm *RuntimeManager) Rollback(version string) error {
	snapshot, ok := rm.GetVersion(version)
	if !ok {
		return errors.New(errors.ErrConfigNotFound, fmt.Sprintf("runtime config version not found: %s", version))
	}
	if snapshot.Config == nil {
		return errors.New(errors.ErrConfigInvalid, "runtime config snapshot is nil")
	}
	return rm.Update(snapshot.Config)
}

// GetAgentConfig 获取 Agent 配置
func (rm *RuntimeManager) GetAgentConfig() AgentConfig {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	return rm.config.Agent
}

// GetRouterConfig 获取路由器配置
func (rm *RuntimeManager) GetRouterConfig() RouterConfig {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	return rm.config.Router
}

// GetEmbeddingConfig 获取 Embedding 配置
func (rm *RuntimeManager) GetEmbeddingConfig() EmbeddingConfig {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	return rm.config.Embedding
}

// GetWorkspaceConfig 获取 Workspace 配置
func (rm *RuntimeManager) GetWorkspaceConfig() WorkspaceConfig {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	return rm.config.Workspace
}

// ValidateRuntimeConfig 验证 Runtime 配置
func ValidateRuntimeConfig(config *RuntimeConfig) error {
	if err := ValidateAgentConfig(&config.Agent); err != nil {
		return err
	}
	if err := ValidateRouterConfig(&config.Router); err != nil {
		return err
	}
	if err := ValidateEmbeddingConfig(&config.Embedding); err != nil {
		return err
	}
	if err := ValidatePerformanceConfig(&config.Performance); err != nil {
		return err
	}
	if err := ValidateContextConfig(&config.Context); err != nil {
		return err
	}
	if err := ValidateCatalogConfig(&config.Catalog); err != nil {
		return err
	}
	if err := ValidateTeamConfig(&config.Team); err != nil {
		return err
	}
	if err := ValidateSessionRuntimeConfig(&config.SessionRuntime); err != nil {
		return err
	}
	if err := ValidateTraceConfig(&config.Trace); err != nil {
		return err
	}
	if err := ValidateCheckpointConfig(&config.Checkpoint); err != nil {
		return err
	}
	if err := ValidateBackgroundConfig(&config.Background); err != nil {
		return err
	}
	if err := ValidateHookConfig(config.Hooks); err != nil {
		return err
	}
	if err := ValidateSandboxConfig(&config.Sandbox); err != nil {
		return err
	}
	if err := ValidateRolloutConfig(&config.Rollout, config.Version); err != nil {
		return err
	}
	return nil
}

func ValidateCatalogConfig(config *CatalogConfig) error {
	if config == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(config.Backend)) {
	case "", "memory", "file", "sqlite":
	default:
		return errors.New(errors.ErrValidationFailed, "catalog backend must be memory, file, or sqlite")
	}
	backend := strings.ToLower(strings.TrimSpace(config.Backend))
	if (backend == "file" || backend == "sqlite") && strings.TrimSpace(config.SnapshotPath) == "" {
		return errors.New(errors.ErrValidationFailed, "catalog snapshotPath is required for file/sqlite backend")
	}
	return nil
}

// ValidateTeamConfig validates team persistence configuration.
func ValidateTeamConfig(config *TeamConfig) error {
	if config == nil {
		return nil
	}
	if strings.TrimSpace(config.StorePath) != "" && strings.TrimSpace(config.StoreDSN) != "" {
		return errors.New(errors.ErrValidationFailed, "team storePath and storeDSN cannot both be set")
	}
	return nil
}

// ValidateSessionRuntimeConfig validates session runtime persistence configuration.
func ValidateSessionRuntimeConfig(config *SessionRuntimeConfig) error {
	if config == nil {
		return nil
	}
	if strings.TrimSpace(config.StorePath) != "" && strings.TrimSpace(config.StoreDSN) != "" {
		return errors.New(errors.ErrValidationFailed, "sessionRuntime storePath and storeDSN cannot both be set")
	}
	return nil
}

// ValidateCheckpointConfig validates checkpoint configuration.
func ValidateCheckpointConfig(config *CheckpointConfig) error {
	if config == nil {
		return nil
	}
	if config.MaxFileBytes < 0 {
		return errors.New(errors.ErrValidationFailed, "checkpoint.maxFileBytes cannot be negative")
	}
	return nil
}

// ValidateBackgroundConfig validates background configuration.
func ValidateBackgroundConfig(config *BackgroundConfig) error {
	if config == nil {
		return nil
	}
	if strings.TrimSpace(config.StorePath) != "" && strings.TrimSpace(config.StoreDSN) != "" {
		return errors.New(errors.ErrValidationFailed, "background storePath and storeDSN cannot both be set")
	}
	if config.MaxOutputBytes < 0 {
		return errors.New(errors.ErrValidationFailed, "background.maxOutputBytes cannot be negative")
	}
	if config.MaxConcurrentJobs < 0 {
		return errors.New(errors.ErrValidationFailed, "background.maxConcurrentJobs cannot be negative")
	}
	if config.DefaultTimeout < 0 {
		return errors.New(errors.ErrValidationFailed, "background.defaultTimeout cannot be negative")
	}
	return nil
}

// ValidateTraceConfig validates runtime trace configuration.
func ValidateTraceConfig(config *TraceConfig) error {
	if config == nil {
		return nil
	}
	if config.TeamIDLimit < 0 {
		return errors.New(errors.ErrValidationFailed, "trace.teamIdLimit cannot be negative")
	}
	return nil
}

func ValidateContextConfig(config *ContextConfig) error {
	if config == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(config.Profile)) {
	case "",
		runtimecontext.BudgetProfileCompact,
		runtimecontext.BudgetProfileBalanced,
		runtimecontext.BudgetProfileExtended,
		runtimecontext.BudgetProfileHot,
		runtimecontext.BudgetProfileWarm,
		runtimecontext.BudgetProfileCold:
	default:
		return errors.New(errors.ErrValidationFailed, "context profile must be compact, balanced, extended, hot, warm, or cold")
	}
	for _, item := range []struct {
		name  string
		value int
	}{
		{"minCompactionMessages", config.MinCompactionMessages},
		{"minRecallQueryLength", config.MinRecallQueryLength},
		{"ledgerLoadLimit", config.LedgerLoadLimit},
		{"maxPromptTokens", config.MaxPromptTokens},
		{"maxMessages", config.MaxMessages},
		{"keepRecentMessages", config.KeepRecentMessages},
		{"maxRecallResults", config.MaxRecallResults},
		{"maxObservationItems", config.MaxObservationItems},
	} {
		if item.value < 0 {
			return errors.New(errors.ErrValidationFailed, fmt.Sprintf("%s cannot be negative", item.name))
		}
	}
	for _, item := range []struct {
		name  string
		value string
		allow []string
	}{
		{"compactionMode", strings.TrimSpace(config.CompactionMode), []string{"", runtimecontext.CompactionModeSummary, runtimecontext.CompactionModeLedgerPreferred}},
		{"recallMode", strings.TrimSpace(config.RecallMode), []string{"", runtimecontext.RecallModeDisabled, runtimecontext.RecallModeSignals, runtimecontext.RecallModeBroad}},
		{"observationMode", strings.TrimSpace(config.ObservationMode), []string{"", runtimecontext.ObservationModeAll, runtimecontext.ObservationModeFailures}},
	} {
		if item.value == "" {
			continue
		}
		valid := false
		for _, allowed := range item.allow {
			if item.value == allowed {
				valid = true
				break
			}
		}
		if !valid {
			return errors.New(errors.ErrValidationFailed, fmt.Sprintf("%s has unsupported value", item.name))
		}
	}
	return nil
}

// ValidateAgentConfig 验证 Agent 配置
func ValidateAgentConfig(config *AgentConfig) error {
	if config.MaxMaxSteps <= 0 {
		return errors.New(errors.ErrValidationFailed, "maxSteps must be positive")
	}
	if config.DefaultModel == "" {
		return errors.New(errors.ErrValidationFailed, "defaultModel cannot be empty")
	}
	if config.MaxMemorySize < 0 {
		return errors.New(errors.ErrValidationFailed, "maxMemorySize cannot be negative")
	}
	if config.Timeout <= 0 {
		return errors.New(errors.ErrValidationFailed, "timeout must be positive")
	}
	if config.DefaultPlanningMode != "" {
		mode := strings.ToLower(strings.TrimSpace(config.DefaultPlanningMode))
		switch mode {
		case "planner_preferred", "route_preferred", "agent_only", "llm_only":
		default:
			return errors.New(errors.ErrValidationFailed, "defaultPlanningMode must be planner_preferred, route_preferred, agent_only, or llm_only")
		}
	}
	return nil
}

// ValidateRouterConfig 验证路由器配置
func ValidateRouterConfig(config *RouterConfig) error {
	if config.MinScore < 0 || config.MinScore > 1 {
		return errors.New(errors.ErrValidationFailed, "minScore must be between 0 and 1")
	}
	if config.MaxResults < 1 {
		return errors.New(errors.ErrValidationFailed, "maxResults must be at least 1")
	}
	if config.Threshold < 0 || config.Threshold > 1 {
		return errors.New(errors.ErrValidationFailed, "threshold must be between 0 and 1")
	}
	if config.TopK < 1 {
		return errors.New(errors.ErrValidationFailed, "topK must be at least 1")
	}
	return nil
}

// ValidateEmbeddingConfig 验证 Embedding 配置
func ValidateEmbeddingConfig(config *EmbeddingConfig) error {
	if config.Enabled {
		if config.Provider == "" {
			return errors.New(errors.ErrValidationFailed, "provider cannot be empty when enabled")
		}
		if config.Model == "" {
			return errors.New(errors.ErrValidationFailed, "model cannot be empty when enabled")
		}
		if config.VectorDim <= 0 {
			return errors.New(errors.ErrValidationFailed, "vectorDim must be positive")
		}
		if config.BatchSize < 1 {
			return errors.New(errors.ErrValidationFailed, "batchSize must be at least 1")
		}
	}
	return nil
}

// ValidatePerformanceConfig 验证性能配置
func ValidatePerformanceConfig(config *PerformanceConfig) error {
	if config.MaxConcurrency < 1 {
		return errors.New(errors.ErrValidationFailed, "maxConcurrency must be at least 1")
	}
	if config.RequestTimeout <= 0 {
		return errors.New(errors.ErrValidationFailed, "requestTimeout must be positive")
	}
	if config.ToolTimeout <= 0 {
		return errors.New(errors.ErrValidationFailed, "toolTimeout must be positive")
	}
	return nil
}

// ValidateSandboxConfig validates local sandbox policy configuration.
func ValidateSandboxConfig(config *runtimeexecutor.SandboxConfig) error {
	if config == nil {
		return nil
	}
	if config.MaxExecutionTime < 0 {
		return errors.New(errors.ErrValidationFailed, "sandbox.maxExecutionTime cannot be negative")
	}
	for _, path := range append(append([]string{}, config.AllowedPaths...), append(config.DeniedPaths, config.ReadOnlyPaths...)...) {
		if strings.TrimSpace(path) == "" {
			return errors.New(errors.ErrValidationFailed, "sandbox paths cannot contain empty entries")
		}
	}
	for _, command := range append(append([]string{}, config.AllowedCommands...), config.DeniedCommands...) {
		if strings.TrimSpace(command) == "" {
			return errors.New(errors.ErrValidationFailed, "sandbox commands cannot contain empty entries")
		}
	}
	for _, key := range config.EnvWhitelist {
		if strings.TrimSpace(key) == "" {
			return errors.New(errors.ErrValidationFailed, "sandbox envWhitelist cannot contain empty entries")
		}
	}
	for _, host := range append(append([]string{}, config.AllowedHosts...), config.DeniedHosts...) {
		if strings.TrimSpace(host) == "" {
			return errors.New(errors.ErrValidationFailed, "sandbox hosts cannot contain empty entries")
		}
	}
	return nil
}

// ValidateRolloutConfig 验证 Rollout 配置
func ValidateRolloutConfig(config *RolloutConfig, activeVersion string) error {
	if config == nil {
		return nil
	}
	if !config.Enabled {
		return nil
	}
	if config.Mode == "" {
		config.Mode = "canary"
	}
	switch config.Mode {
	case "canary", "progressive":
	default:
		return errors.New(errors.ErrValidationFailed, "rollout.mode must be canary or progressive")
	}
	if config.Percent <= 0 || config.Percent > 100 {
		return errors.New(errors.ErrValidationFailed, "rollout.percent must be between 1 and 100")
	}
	if config.CandidateVersion == "" {
		return errors.New(errors.ErrValidationFailed, "rollout.candidateVersion is required when rollout is enabled")
	}
	if strings.TrimSpace(config.CandidateFile) == "" {
		return errors.New(errors.ErrValidationFailed, "rollout.candidateFile is required when rollout is enabled")
	}
	if activeVersion == "" {
		return errors.New(errors.ErrValidationFailed, "runtime version is required when rollout is enabled")
	}
	if config.CandidateVersion == activeVersion {
		return errors.New(errors.ErrValidationFailed, "rollout.candidateVersion must differ from active version")
	}
	return nil
}

// ValidateHookConfig validates runtime hook configuration.
func ValidateHookConfig(hooks []runtimehooks.HookConfig) error {
	for _, hook := range hooks {
		if strings.TrimSpace(hook.ID) == "" {
			return errors.New(errors.ErrValidationFailed, "hook id cannot be empty")
		}
		if strings.TrimSpace(string(hook.Event)) == "" {
			return errors.New(errors.ErrValidationFailed, fmt.Sprintf("hook %s event is required", hook.ID))
		}
		execType := strings.ToLower(strings.TrimSpace(hook.Exec.Type))
		if execType == "" {
			return errors.New(errors.ErrValidationFailed, fmt.Sprintf("hook %s exec.type is required", hook.ID))
		}
		switch execType {
		case "shell":
			if len(hook.Exec.Cmd) == 0 {
				return errors.New(errors.ErrValidationFailed, fmt.Sprintf("hook %s exec.cmd is required for shell hooks", hook.ID))
			}
		case "http":
			if strings.TrimSpace(hook.Exec.URL) == "" {
				return errors.New(errors.ErrValidationFailed, fmt.Sprintf("hook %s exec.url is required for http hooks", hook.ID))
			}
		default:
			return errors.New(errors.ErrValidationFailed, fmt.Sprintf("hook %s exec.type must be shell or http", hook.ID))
		}
		if hook.OnError != "" {
			switch strings.ToLower(strings.TrimSpace(hook.OnError)) {
			case "fail_open", "fail_closed":
			default:
				return errors.New(errors.ErrValidationFailed, fmt.Sprintf("hook %s on_error must be fail_open or fail_closed", hook.ID))
			}
		}
	}
	return nil
}

// RuntimeConfigSnapshot 版本快照
type RuntimeConfigSnapshot struct {
	Version   string         `json:"version"`
	AppliedAt time.Time      `json:"applied_at"`
	Config    *RuntimeConfig `json:"config"`
}

func ensureRuntimeConfigVersion(config *RuntimeConfig) {
	if config == nil {
		return
	}
	if strings.TrimSpace(config.Version) == "" {
		config.Version = time.Now().UTC().Format("20060102-150405")
	}
}

func (rm *RuntimeManager) recordHistoryLocked(config *RuntimeConfig) {
	if config == nil {
		return
	}
	cloned, err := cloneRuntimeConfig(config)
	if err != nil {
		return
	}
	rm.history = append(rm.history, RuntimeConfigSnapshot{
		Version:   cloned.Version,
		AppliedAt: time.Now().UTC(),
		Config:    cloned,
	})
}

func cloneRuntimeConfig(config *RuntimeConfig) (*RuntimeConfig, error) {
	if config == nil {
		return nil, nil
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	var cloned RuntimeConfig
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return nil, err
	}
	return &cloned, nil
}

func (rm *RuntimeManager) loadCandidateLocked() error {
	rm.candidate = nil
	if rm.config == nil || !rm.config.Rollout.Enabled {
		return nil
	}
	candidateFile := strings.TrimSpace(rm.config.Rollout.CandidateFile)
	if candidateFile == "" {
		return nil
	}
	candidate, err := loadRuntimeConfigFromFile(candidateFile)
	if err != nil {
		return errors.Wrap(errors.ErrConfigInvalid, fmt.Sprintf("failed to load candidate config: %s", candidateFile), err)
	}
	if candidate == nil {
		return nil
	}
	ensureRuntimeConfigVersion(candidate)
	if rm.config.Rollout.CandidateVersion != "" && candidate.Version != rm.config.Rollout.CandidateVersion {
		return errors.New(errors.ErrValidationFailed, fmt.Sprintf("candidate version mismatch: expected %s, got %s", rm.config.Rollout.CandidateVersion, candidate.Version))
	}
	rm.candidate = candidate
	return nil
}

func loadRuntimeConfigFromFile(path string) (*RuntimeConfig, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	config := DefaultRuntimeConfig()
	ext := filepath.Ext(path)
	switch ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, config); err != nil {
			return nil, err
		}
	case ".json":
		if err := json.Unmarshal(data, config); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported config format: %s", ext)
	}
	if err := ValidateRuntimeConfig(config); err != nil {
		return nil, err
	}
	return config, nil
}

func shouldUseCandidate(scopeKey string, rollout RolloutConfig) bool {
	if scopeKey == "" {
		return false
	}
	percent := rolloutPercent(rollout)
	if percent <= 0 {
		return false
	}
	value := hashScope(scopeKey) % 100
	return value < percent
}

func rolloutPercent(rollout RolloutConfig) int {
	if !rollout.Enabled {
		return 0
	}
	if rollout.Mode != "progressive" {
		return rollout.Percent
	}
	if rollout.StartedAt.IsZero() || rollout.Duration <= 0 {
		return rollout.Percent
	}
	elapsed := time.Since(rollout.StartedAt)
	if elapsed <= 0 {
		return 0
	}
	if elapsed >= rollout.Duration {
		return rollout.Percent
	}
	ratio := float64(elapsed) / float64(rollout.Duration)
	current := int(float64(rollout.Percent) * ratio)
	if current < 0 {
		return 0
	}
	if current > rollout.Percent {
		return rollout.Percent
	}
	return current
}

func hashScope(scopeKey string) int {
	h := uint32(2166136261)
	for i := 0; i < len(scopeKey); i++ {
		h ^= uint32(scopeKey[i])
		h *= 16777619
	}
	return int(h)
}
