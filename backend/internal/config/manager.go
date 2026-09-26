package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimecontext "github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	runtimeobserve "github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
	"gopkg.in/yaml.v3"
)

// RuntimeConfig Skills Runtime 配置
type RuntimeConfig struct {
	Version string `yaml:"version,omitempty" json:"version,omitempty"`

	// Skills 引擎配置
	Agent          AgentConfig               `yaml:"agent" json:"agent"`
	Agents         AgentsConfig              `yaml:"agents" json:"agents"`
	Router         RouterConfig              `yaml:"router" json:"router"`
	Embedding      EmbeddingConfig           `yaml:"embedding" json:"embedding"`
	Workspace      WorkspaceConfig           `yaml:"workspace" json:"workspace"`
	Context        ContextConfig             `yaml:"context" json:"context"`
	Catalog        CatalogConfig             `yaml:"catalog" json:"catalog"`
	Sessions       SessionsConfig            `yaml:"sessions" json:"sessions"`
	Team           TeamConfig                `yaml:"team" json:"team"`
	AgentControl   AgentControlConfig        `yaml:"agentControl" json:"agentControl"`
	Trace          TraceConfig               `yaml:"trace" json:"trace"`
	Hooks          []runtimehooks.HookConfig `yaml:"hooks,omitempty" json:"hooks,omitempty"`
	SessionRuntime SessionRuntimeConfig      `yaml:"sessionRuntime" json:"sessionRuntime"`
	Artifact       ArtifactConfig            `yaml:"artifact" json:"artifact"`
	Checkpoint     CheckpointConfig          `yaml:"checkpoint" json:"checkpoint"`
	Background     BackgroundConfig          `yaml:"background" json:"background"`
	Images         ImagesConfig              `yaml:"images" json:"images"`

	// 性能配置
	Performance PerformanceConfig `yaml:"performance" json:"performance"`

	// Sandbox 配置
	Sandbox runtimeexecutor.SandboxConfig `yaml:"sandbox" json:"sandbox"`

	// 热加载配置
	HotReload HotReloadConfig `yaml:"hotReload" json:"hotReload"`

	// Rollout 配置
	Rollout RolloutConfig `yaml:"rollout" json:"rollout"`

	// Observe Runtime Observation Plane 配置（默认关闭；启用后注册
	// /api/runtime/observe/v1 观测路由并订阅 runtime event bus）。
	Observe runtimeobserve.Config `yaml:"observe" json:"observe"`

	// Knowledge 知识层配置（Phase 0 交付 1）。默认 mode=off：知识层代码可以
	// 存在，但完全不参与任何路径（04 §5 排期铁律 ③）。Workspace 由运行时按
	// 当前工作区注入，不出现在配置文件里。
	Knowledge knowledge.Config `yaml:"knowledge" json:"knowledge"`
}

// AgentConfig Agent 配置
type AgentConfig struct {
	MaxMaxSteps          int `yaml:"maxSteps" json:"maxSteps"`
	MaxToolCalls         int `yaml:"maxToolCalls" json:"maxToolCalls"`
	MaxExplorationSteps  int `yaml:"maxExplorationSteps" json:"maxExplorationSteps"`
	MaxRepeatedToolCalls int `yaml:"maxRepeatedToolCalls" json:"maxRepeatedToolCalls"`
	// MaxRepeatedPollCalls is the consecutive identical polling/control call
	// count that triggers the soft polling-backoff advisory (P1-7). 0 uses the
	// built-in default (3); negative disables the guard.
	MaxRepeatedPollCalls int           `yaml:"maxRepeatedPollCalls,omitempty" json:"maxRepeatedPollCalls,omitempty"`
	DefaultProvider      string        `yaml:"defaultProvider,omitempty" json:"defaultProvider,omitempty"`
	DefaultModel         string        `yaml:"defaultModel" json:"defaultModel"`
	EnableMemory         bool          `yaml:"enableMemory" json:"enableMemory"`
	EnablePlanning       bool          `yaml:"enablePlanning" json:"enablePlanning"`
	EnableRouter         bool          `yaml:"enableRouter" json:"enableRouter"`
	EnableParallelTools  bool          `yaml:"enableParallelTools" json:"enableParallelTools"`
	MaxMemorySize        int           `yaml:"maxMemorySize" json:"maxMemorySize"`
	MaxParallelToolCalls int           `yaml:"maxParallelToolCalls" json:"maxParallelToolCalls"`
	Timeout              time.Duration `yaml:"timeout" json:"timeout"`
	DefaultPlanningMode  string        `yaml:"defaultPlanningMode,omitempty" json:"defaultPlanningMode,omitempty"`
}

// AgentsConfig controls lightweight multi-agent collaboration limits.
type AgentsConfig struct {
	MaxThreads int `yaml:"maxThreads" json:"maxThreads"`
	MaxDepth   int `yaml:"maxDepth" json:"maxDepth"`
	// MaxConcurrent caps how many child agents one subagent batch may execute
	// at the same time (the per-batch ceiling handed to
	// agent.SubagentSchedulerConfig.MaxConcurrent). 0 = unset → the built-in
	// default (4, the historical scheduler default). This is the per-batch
	// knob; the process-wide ceiling is MaxThreads, which the hosts apply as
	// one shared concurrency limiter across every scheduler they build (total
	// concurrent children ≤ maxThreads whenever maxThreads > 0; -1 stays
	// "explicitly unlimited" and leaves only the per-batch ceiling in force).
	MaxConcurrent int `yaml:"maxConcurrent,omitempty" json:"maxConcurrent,omitempty"`
	// MaxConcurrentQueueDepth / MaxConcurrentQueueTimeoutMs are the opt-in
	// backpressure knobs for a reader wave (plan P1-4 §2). Both default to 0,
	// which preserves the historical behavior: unbounded waiting for a
	// concurrency slot with no queue timeout (P2-8's fail-fast spawn gate is a
	// separate path and is unaffected). When MaxConcurrentQueueDepth > 0, a
	// wave with more than MaxConcurrent+MaxConcurrentQueueDepth reader tasks is
	// rejected before any child starts. When MaxConcurrentQueueTimeoutMs > 0, a
	// task that waits longer than that for a slot fails the batch with a
	// timeout error instead of waiting forever.
	MaxConcurrentQueueDepth     int `yaml:"maxConcurrentQueueDepth,omitempty" json:"maxConcurrentQueueDepth,omitempty"`
	MaxConcurrentQueueTimeoutMs int `yaml:"maxConcurrentQueueTimeoutMs,omitempty" json:"maxConcurrentQueueTimeoutMs,omitempty"`
	DefaultWaitTimeoutMs        int `yaml:"defaultWaitTimeoutMs,omitempty" json:"defaultWaitTimeoutMs,omitempty"`
	// MinWaitTimeoutMs / MaxWaitTimeoutMs bound every model-issued wait window
	// (wait_agent, read_agent_events, and wait_team): no wait path may block
	// longer than MaxWaitTimeoutMs. Timeouts outside the bounds are clamped or
	// rejected depending on WaitTimeoutMode.
	MinWaitTimeoutMs int `yaml:"minWaitTimeoutMs,omitempty" json:"minWaitTimeoutMs,omitempty"`
	MaxWaitTimeoutMs int `yaml:"maxWaitTimeoutMs,omitempty" json:"maxWaitTimeoutMs,omitempty"`
	// WaitTimeoutMode selects the out-of-range behavior: "clamp" (default)
	// pins the request to the nearest bound, "error" rejects it with an
	// actionable message.
	WaitTimeoutMode string `yaml:"waitTimeoutMode,omitempty" json:"waitTimeoutMode,omitempty"`
	// MaxConsecutiveWaitWithoutProgress bounds how many consecutive active
	// wait segments on one parent turn may observe zero obligation progress
	// (no terminal_delta) before the host stops granting new wait windows and
	// returns next_action=suspend (design §16.2/§16.3). 0 uses the shared
	// default (2); a negative value disables the budget. The enforcement never
	// parks a turn by itself: the parent either does independent work or ends
	// the turn, and I1 converts a premature finalize into a turn suspension.
	MaxConsecutiveWaitWithoutProgress int    `yaml:"maxConsecutiveWaitWithoutProgress,omitempty" json:"maxConsecutiveWaitWithoutProgress,omitempty"`
	DefaultForkTurns                  string `yaml:"defaultForkTurns,omitempty" json:"defaultForkTurns,omitempty"`
	// RegistryReconcileInterval is the P2-9 low-frequency consistency sweep
	// cadence for the durable agent registry. 0 uses the shared default (10m);
	// values below 1m are floored by the reconciler so a typo cannot turn the
	// audit into a hot loop.
	RegistryReconcileInterval time.Duration `yaml:"registryReconcileInterval,omitempty" json:"registryReconcileInterval,omitempty"`
	// RegistryReconcileMode selects what the sweep is allowed to do:
	// "observe" (default) only reports drift, "enforce" closes rows whose
	// session is already missing/terminal and marks stale rows stale.
	RegistryReconcileMode string `yaml:"registryReconcileMode,omitempty" json:"registryReconcileMode,omitempty"`
	// RegistryTerminalRetention bounds how long terminal (closed/stale) registry
	// rows and the wake events they produced are kept. 0 uses the shared default
	// (30d), a negative value keeps them forever (opt-out), a positive value is
	// the window. Only rows that are already terminal are pruned, so retention
	// can never touch a child that still holds quota.
	RegistryTerminalRetention time.Duration `yaml:"registryTerminalRetention,omitempty" json:"registryTerminalRetention,omitempty"`
	// ReclaimIdleMs enables idle-child eviction (plan P2-8 方案 3) when > 0:
	// a spawn that already hit maxThreads may close a child whose execution
	// container has been idle for at least this long and then retries once.
	// 0 (default) keeps eviction conservative: only children whose container is
	// provably missing or terminal are reclaimed.
	ReclaimIdleMs int `yaml:"reclaimIdleMs,omitempty" json:"reclaimIdleMs,omitempty"`
	// AutoCloseCompleted is the P1-C 方案 C convergence policy for terminal
	// subagent batches. "off" (default) keeps every child session recoverable
	// and changes nothing; "completed" converges child sessions whose task
	// succeeded when the batch finished without failures; "batch_terminal"
	// does the same for any terminal batch status. Convergence means: an
	// unresolved lifecycle row recommending close is projected for the parent
	// and the child session is closed through the durable control plane
	// (audit + resolution receipt). Non-succeeded terminal children are never
	// auto-closed — a failed/timed-out child is evidence the parent must judge.
	AutoCloseCompleted string `yaml:"autoCloseCompleted,omitempty" json:"autoCloseCompleted,omitempty"`
}

// agents.autoCloseCompleted policy values (P1-C 方案 C).
const (
	AutoClosePolicyOff           = "off"
	AutoClosePolicyCompleted     = "completed"
	AutoClosePolicyBatchTerminal = "batch_terminal"
)

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
	Enabled      bool     `yaml:"enabled" json:"enabled"`
	Mode         string   `yaml:"mode" json:"mode"`
	Root         string   `yaml:"root" json:"root"`
	Exclude      []string `yaml:"exclude" json:"exclude"`
	Include      []string `yaml:"include" json:"include"`
	MaxFileSize  int64    `yaml:"maxFileSize" json:"maxFileSize"`
	MaxChunkSize int      `yaml:"maxChunkSize" json:"maxChunkSize"`
	ChunkOverlap int      `yaml:"chunkOverlap" json:"chunkOverlap"`
}

type ContextConfig struct {
	Profile                 string `yaml:"profile" json:"profile"`
	CompactionMode          string `yaml:"compactionMode" json:"compactionMode"`
	RecallMode              string `yaml:"recallMode" json:"recallMode"`
	ObservationMode         string `yaml:"observationMode" json:"observationMode"`
	WorkspaceMode           string `yaml:"workspaceMode" json:"workspaceMode"`
	MinCompactionMessages   int    `yaml:"minCompactionMessages" json:"minCompactionMessages"`
	MinRecallQueryLength    int    `yaml:"minRecallQueryLength" json:"minRecallQueryLength"`
	LedgerLoadLimit         int    `yaml:"ledgerLoadLimit" json:"ledgerLoadLimit"`
	MaxPromptTokens         int    `yaml:"maxPromptTokens" json:"maxPromptTokens"`
	FallbackMaxPromptTokens int    `yaml:"fallbackMaxPromptTokens" json:"fallbackMaxPromptTokens"`
	MaxMessages             int    `yaml:"maxMessages" json:"maxMessages"`
	KeepRecentMessages      int    `yaml:"keepRecentMessages" json:"keepRecentMessages"`
	MaxRecallResults        int    `yaml:"maxRecallResults" json:"maxRecallResults"`
	MaxObservationItems     int    `yaml:"maxObservationItems" json:"maxObservationItems"`
}

type CatalogConfig struct {
	Backend      string `yaml:"backend" json:"backend"`
	SnapshotPath string `yaml:"snapshotPath" json:"snapshotPath"`
}

// SessionsConfig configures session history persistence.
type SessionsConfig struct {
	Dir                   string        `yaml:"dir" json:"dir"`
	Backend               string        `yaml:"backend" json:"backend"`
	StorePath             string        `yaml:"storePath" json:"storePath"`
	DefaultUserID         string        `yaml:"defaultUserId" json:"defaultUserId"`
	MaxHistory            int           `yaml:"maxHistory" json:"maxHistory"`
	HotHistoryBytes       int           `yaml:"hotHistoryBytes" json:"hotHistoryBytes"`
	MaxHotMessageBytes    int           `yaml:"maxHotMessageBytes" json:"maxHotMessageBytes"`
	HistoryPageMessages   int           `yaml:"historyPageMessages" json:"historyPageMessages"`
	HistoryPageBytes      int           `yaml:"historyPageBytes" json:"historyPageBytes"`
	MaxInlineMessageBytes int           `yaml:"maxInlineMessageBytes" json:"maxInlineMessageBytes"`
	SQLiteCacheKiB        int           `yaml:"sqliteCacheKiB" json:"sqliteCacheKiB"`
	BusyTimeout           time.Duration `yaml:"busyTimeout" json:"busyTimeout"`
	TTL                   time.Duration `yaml:"ttl" json:"ttl"`
	CleanupInterval       time.Duration `yaml:"cleanupInterval" json:"cleanupInterval"`
	IdleTimeout           time.Duration `yaml:"idleTimeout" json:"idleTimeout"`
	AutoArchive           bool          `yaml:"autoArchive" json:"autoArchive"`
}

// TraceConfig configures runtime trace exports.
type TraceConfig struct {
	TeamIDLimit int `yaml:"teamIdLimit" json:"teamIdLimit"`
}

// TeamConfig configures the team store persistence.
type TeamConfig struct {
	StorePath    string                 `yaml:"storePath" json:"storePath"`
	StoreDSN     string                 `yaml:"storeDSN" json:"storeDSN"`
	Orchestrator TeamOrchestratorConfig `yaml:"orchestrator" json:"orchestrator"`
}

// TeamOrchestratorConfig controls the host-level loop supervisor.
type TeamOrchestratorConfig struct {
	ReconcileInterval time.Duration   `yaml:"reconcileInterval" json:"reconcileInterval"`
	RestartBackoff    []time.Duration `yaml:"restartBackoff" json:"restartBackoff"`
	MaxRestartBackoff time.Duration   `yaml:"maxRestartBackoff" json:"maxRestartBackoff"`
}

// AgentControlConfig configures cross-workflow AgentControl persistence.
type AgentControlConfig struct {
	StorePath        string `yaml:"storePath" json:"storePath"`
	StoreDSN         string `yaml:"storeDSN" json:"storeDSN"`
	MailboxStorePath string `yaml:"mailboxStorePath" json:"mailboxStorePath"`
	MailboxStoreDSN  string `yaml:"mailboxStoreDSN" json:"mailboxStoreDSN"`
	AgentStorePath   string `yaml:"agentStorePath" json:"agentStorePath"`
	AgentStoreDSN    string `yaml:"agentStoreDSN" json:"agentStoreDSN"`
}

// SessionRuntimeConfig configures persistence for session runtime state/events.
type SessionRuntimeConfig struct {
	StorePath          string `yaml:"storePath" json:"storePath"`
	StoreDSN           string `yaml:"storeDSN" json:"storeDSN"`
	DefaultPersistence string `yaml:"defaultPersistence" json:"defaultPersistence"`
	// EventPersist 是 P1.5 事件持久化批量化的灰度配置；
	// BatchingEnabled 默认 false（桥接保持原逐条同步路径，行为逐字节不变）。
	EventPersist EventPersistConfig `yaml:"eventPersist" json:"eventPersist"`
	// ReadPool 是 P1.7 读写双池的灰度配置；默认启用（disable=false），
	// 回滚时置 disable=true（读回落写池，行为与拆分前逐字节一致）。
	ReadPool ReadPoolConfig `yaml:"readPool" json:"readPool"`
}

// ReadPoolConfig 控制 runtime store 的只读连接池（P1.7）。
// 设计基线见 docs/plan/runtime-store-read-write-pool-split-plan-20260918.md §3.5。
type ReadPoolConfig struct {
	// Disable 关闭拆分（回滚开关）；零值=false 表示启用。
	Disable bool `yaml:"disable" json:"disable"`
	// Size 读池连接上限；<=0 取默认 4。仅文件库（WAL）生效。
	Size int `yaml:"size" json:"size"`
	// BusyTimeout 读连接 busy_timeout；<=0 继承 store 的 BusyTimeout。
	BusyTimeout time.Duration `yaml:"busyTimeout" json:"busyTimeout"`
	// OperationTimeout 单次读操作上限；<=0 取默认 3s（小于写侧 10s）。
	OperationTimeout time.Duration `yaml:"operationTimeout" json:"operationTimeout"`
}

// EventPersistConfig 控制「总线 → session_runtime.sqlite」桥接的批量落盘。
// 设计基线见 docs/plan/runtime-store-event-persistence-batching-and-async-plan-20260918.md §3.5。
type EventPersistConfig struct {
	BatchingEnabled bool          `yaml:"batchingEnabled" json:"batchingEnabled"`
	BatchSize       int           `yaml:"batchSize" json:"batchSize"`
	FlushInterval   time.Duration `yaml:"flushInterval" json:"flushInterval"`
	QueueLimit      int           `yaml:"queueLimit" json:"queueLimit"`
	QueueBytesLimit int64         `yaml:"queueBytesLimit" json:"queueBytesLimit"`
	ShutdownTimeout time.Duration `yaml:"shutdownTimeout" json:"shutdownTimeout"`
	// FailMode: "" / "block"（默认，溢出同步落盘）| "drop"（显式丢弃并计数）。
	FailMode string `yaml:"failMode" json:"failMode"`
	// AsyncDispatch 是 P2.11 独立开关；开启前置条件为 BatchingEnabled=true。
	AsyncDispatch bool `yaml:"asyncDispatch" json:"asyncDispatch"`
}

// ArtifactConfig configures persistence for artifacts, context ledger, and checkpoints.
type ArtifactConfig struct {
	StorePath string `yaml:"storePath" json:"storePath"`
	StoreDSN  string `yaml:"storeDSN" json:"storeDSN"`
}

// CheckpointConfig controls checkpoint capture behavior.
// Enabled defaults to false so file-mutation blobs are opt-in.
//
// Storage is Codex-inspired by default when capture is enabled:
//   - storeMode=diff keeps before blobs + hashes (and optional capped diffs), not after fulltext
//   - conversationSnapshot=false avoids duplicating session history into artifacts.sqlite
//   - maxCheckpointsPerSession bounds growth via retention GC after each capture
type CheckpointConfig struct {
	Enabled                  bool   `yaml:"enabled" json:"enabled"`
	MaxFileBytes             int64  `yaml:"maxFileBytes" json:"maxFileBytes"`
	StoreMode                string `yaml:"storeMode" json:"storeMode"` // "diff" (default) | "full"
	ConversationSnapshot     bool   `yaml:"conversationSnapshot" json:"conversationSnapshot"`
	MaxDiffBytes             int64  `yaml:"maxDiffBytes" json:"maxDiffBytes"`
	MaxCheckpointsPerSession int    `yaml:"maxCheckpointsPerSession" json:"maxCheckpointsPerSession"`
}

// BackgroundConfig controls background task persistence.
type BackgroundConfig struct {
	StorePath               string          `yaml:"storePath" json:"storePath"`
	StoreDSN                string          `yaml:"storeDSN" json:"storeDSN"`
	LogDir                  string          `yaml:"logDir" json:"logDir"`
	MaxOutputBytes          int             `yaml:"maxOutputBytes" json:"maxOutputBytes"`
	MaxConcurrentJobs       int             `yaml:"maxConcurrentJobs" json:"maxConcurrentJobs"`
	DefaultTimeout          time.Duration   `yaml:"defaultTimeout" json:"defaultTimeout"`
	MonitorInterval         time.Duration   `yaml:"monitorInterval" json:"monitorInterval"`
	HeartbeatTimeout        time.Duration   `yaml:"heartbeatTimeout" json:"heartbeatTimeout"`
	LaunchMaxAttempts       int             `yaml:"launchMaxAttempts" json:"launchMaxAttempts"`
	RetryBackoff            time.Duration   `yaml:"retryBackoff" json:"retryBackoff"`
	RecoveryMaxAttempts     int             `yaml:"recoveryMaxAttempts" json:"recoveryMaxAttempts"`
	RecoveryBackoffSchedule []time.Duration `yaml:"recoveryBackoffSchedule" json:"recoveryBackoffSchedule"`
}

// ImagesConfig controls HTTP behavior for runtime-generated images.
type ImagesConfig struct {
	CacheMaxAge time.Duration           `yaml:"cacheMaxAge" json:"cacheMaxAge"`
	Generations ImagesGenerationsConfig `yaml:"generations" json:"generations"`
}

// ImagesGenerationsConfig controls defaults for the image generations tool.
type ImagesGenerationsConfig struct {
	DefaultModel        string        `yaml:"default_model" json:"default_model"`
	DefaultSize         string        `yaml:"default_size" json:"default_size"`
	DefaultQuality      string        `yaml:"default_quality" json:"default_quality"`
	DefaultOutputFormat string        `yaml:"default_output_format" json:"default_output_format"`
	RequestTimeout      time.Duration `yaml:"request_timeout" json:"request_timeout"`
	MaxN                int           `yaml:"max_n" json:"max_n"`
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
	mu                sync.RWMutex
	config            *RuntimeConfig
	filePath          string
	watchers          []RuntimeWatcher
	history           []RuntimeConfigSnapshot
	candidate         *RuntimeConfig
	candidateFilePath string
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
			MaxMaxSteps:          0,
			MaxToolCalls:         0,
			MaxExplorationSteps:  0,
			MaxRepeatedToolCalls: 0,
			DefaultProvider:      "",
			DefaultModel:         "claude-3-5-sonnet",
			EnableMemory:         true,
			EnablePlanning:       true,
			EnableRouter:         true,
			EnableParallelTools:  true,
			MaxMemorySize:        1000,
			MaxParallelToolCalls: 4,
			Timeout:              0,
			DefaultPlanningMode:  "",
		},
		Agents: AgentsConfig{
			MaxThreads: 6,
			MaxDepth:   1,
			// P1-4/H12: the per-batch subagent ceiling keeps its historical
			// scheduler default (4) so an absent config changes nothing. The
			// queue knobs stay 0 = backpressure disabled (fail-fast default).
			MaxConcurrent:        4,
			DefaultWaitTimeoutMs: int((30 * time.Second).Milliseconds()),
			MinWaitTimeoutMs:     int((10 * time.Second).Milliseconds()),
			// Wait-budget hardening (2026-09-26): the built-in active-wait
			// ceiling is 2m, not 1h — a single wait window must not be able to
			// occupy the parent turn for most of an hour. Long waits belong to
			// the suspension path (zero goroutine / zero token).
			MaxWaitTimeoutMs:                  int((2 * time.Minute).Milliseconds()),
			WaitTimeoutMode:                   "clamp",
			MaxConsecutiveWaitWithoutProgress: agentcontrol.DefaultMaxConsecutiveWaitWithoutProgress,
			DefaultForkTurns:                  "none",
			// P1-C: the built-in default stays "off" — closing a child session
			// is a user-visible convergence decision, not a silent cleanup.
			AutoCloseCompleted: AutoClosePolicyOff,
			// P2-9: report-only by default; enforce is opt-in once the audit
			// has proven clean on a real deployment.
			RegistryReconcileInterval: 10 * time.Minute,
			RegistryReconcileMode:     "observe",
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
			Profile:                 runtimecontext.BudgetProfileBalanced,
			FallbackMaxPromptTokens: runtimecontext.DefaultFallbackMaxPromptTokens,
		},
		Catalog: CatalogConfig{
			Backend: "memory",
		},
		Sessions: SessionsConfig{Backend: "sqlite"},
		Trace: TraceConfig{
			TeamIDLimit: 0,
		},
		Team: TeamConfig{
			Orchestrator: TeamOrchestratorConfig{
				ReconcileInterval: 5 * time.Second,
				RestartBackoff: []time.Duration{
					time.Second,
					2 * time.Second,
					5 * time.Second,
					10 * time.Second,
					30 * time.Second,
					time.Minute,
				},
				MaxRestartBackoff: 5 * time.Minute,
			},
		},
		AgentControl:   AgentControlConfig{},
		SessionRuntime: SessionRuntimeConfig{},
		Artifact:       ArtifactConfig{},
		Checkpoint: CheckpointConfig{
			Enabled:                  false,
			MaxFileBytes:             1 * 1024 * 1024,
			StoreMode:                "diff",
			ConversationSnapshot:     false,
			MaxDiffBytes:             64 * 1024,
			MaxCheckpointsPerSession: 50,
		},
		Background: BackgroundConfig{
			MaxOutputBytes:          1 * 1024 * 1024,
			MaxConcurrentJobs:       2,
			DefaultTimeout:          0,
			MonitorInterval:         250 * time.Millisecond,
			HeartbeatTimeout:        30 * time.Second,
			LaunchMaxAttempts:       3,
			RetryBackoff:            500 * time.Millisecond,
			RecoveryMaxAttempts:     -1,
			RecoveryBackoffSchedule: []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 3 * time.Minute, 5 * time.Minute},
		},
		Images: ImagesConfig{
			CacheMaxAge: time.Hour,
			Generations: ImagesGenerationsConfig{
				DefaultModel:        "gpt-image-2",
				DefaultSize:         "1024x1024",
				DefaultQuality:      "medium",
				DefaultOutputFormat: "png",
				RequestTimeout:      5 * time.Minute,
				MaxN:                4,
			},
		},
		Performance: PerformanceConfig{
			MaxConcurrency: 10,
			RequestTimeout: 30 * time.Second,
			ToolTimeout:    60 * time.Second,
			CacheEnabled:   true,
			CacheTTL:       5 * time.Minute,
		},
		Sandbox: runtimeexecutor.SandboxConfig{
			Enabled: false,
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
		Observe:   runtimeobserve.DefaultConfig(),
		Knowledge: knowledge.DefaultConfig(),
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

// LoadDocument 应用一份已经合并好的配置文档（分层启动用）。sourcePath 成为管理器的
// 文件路径；校验、candidate 与 history 行为与 Load 完全一致。
func (rm *RuntimeManager) LoadDocument(raw []byte, sourcePath string) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if sourcePath != "" {
		rm.filePath = sourcePath
	}

	config := DefaultRuntimeConfig()
	if len(raw) > 0 {
		// 分层加载器输出 YAML；YAML 解析器同时接受 JSON。
		if err := yaml.Unmarshal(raw, config); err != nil {
			return errors.Wrap(errors.ErrConfigInvalid, "failed to parse merged runtime config", err)
		}
	}
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

	if err := atomicWriteConfigFile(rm.filePath, data, 0644); err != nil {
		return errors.Wrap(errors.ErrConfigInvalid, fmt.Sprintf("failed to write config: %s", rm.filePath), err)
	}

	return nil
}

// atomicWriteConfigFile 原子写配置文件（临时文件 + rename）：
// 多 aicli 实例并发写同一 config.yaml 时避免 read-modify-write 竞态
// 导致的互相覆盖/半文件。
func atomicWriteConfigFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
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
	config, _ := rm.SelectConfigForScopeWithPath(scopeKey)
	return config
}

// SelectConfigForScopeWithPath 根据 scopeKey 选择配置，并返回该配置对应的文件路径。
func (rm *RuntimeManager) SelectConfigForScopeWithPath(scopeKey string) (*RuntimeConfig, string) {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	active := rm.config
	if active == nil {
		return nil, ""
	}
	if !active.Rollout.Enabled || rm.candidate == nil {
		configCopy := *active
		return &configCopy, rm.filePath
	}
	if !shouldUseCandidate(scopeKey, active.Rollout) {
		configCopy := *active
		return &configCopy, rm.filePath
	}
	configCopy := *rm.candidate
	configFile := rm.candidateFilePath
	if strings.TrimSpace(configFile) == "" {
		configFile = rm.filePath
	}
	return &configCopy, configFile
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
	if err := ValidateAgentsConfig(&config.Agents); err != nil {
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
	if err := ValidateWorkspaceConfig(&config.Workspace); err != nil {
		return err
	}
	if err := ValidateCatalogConfig(&config.Catalog); err != nil {
		return err
	}
	if err := ValidateSessionsConfig(&config.Sessions); err != nil {
		return err
	}
	if err := ValidateTeamConfig(&config.Team); err != nil {
		return err
	}
	if err := ValidateAgentControlConfig(&config.AgentControl); err != nil {
		return err
	}
	if err := ValidateSessionRuntimeConfig(&config.SessionRuntime); err != nil {
		return err
	}
	if err := ValidateArtifactConfig(&config.Artifact); err != nil {
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
	if err := ValidateKnowledgeConfig(&config.Knowledge); err != nil {
		return err
	}
	return nil
}

// ValidateKnowledgeConfig 校验知识层配置。
//
// 只校验能从配置文件读到的部分：mode 必须可解析、限额不能为负。workspace 由
// 运行时注入（knowledge.Config.Workspace 带 `yaml:"-"`），因此不在这里要求；
// mode != off 却没有 workspace 的错误由 knowledge.Open 在真正启用时报出。
func ValidateKnowledgeConfig(config *knowledge.Config) error {
	if config == nil {
		return nil
	}
	if _, err := knowledge.ParseMode(string(config.Mode)); err != nil {
		return errors.New(errors.ErrValidationFailed, "knowledge mode must be off, shadow, or on")
	}
	if config.MaxFileBytes < 0 || config.MaxDBSizeMB < 0 {
		return errors.New(errors.ErrValidationFailed, "knowledge limits cannot be negative")
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

// ValidateSessionsConfig validates session storage configuration.
func ValidateSessionsConfig(config *SessionsConfig) error {
	if config == nil {
		return nil
	}
	backend := strings.ToLower(strings.TrimSpace(config.Backend))
	if backend != "" && backend != "file" && backend != "sqlite" {
		return errors.New(errors.ErrValidationFailed, "sessions backend must be file or sqlite")
	}
	if config.HotHistoryBytes < 0 || config.MaxHotMessageBytes < 0 || config.HistoryPageMessages < 0 || config.HistoryPageBytes < 0 || config.MaxInlineMessageBytes < 0 || config.SQLiteCacheKiB < 0 {
		return errors.New(errors.ErrValidationFailed, "session storage memory and inline byte limits cannot be negative")
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
	if config.Orchestrator.ReconcileInterval < 0 {
		return errors.New(errors.ErrValidationFailed, "team.orchestrator.reconcileInterval cannot be negative")
	}
	for _, delay := range config.Orchestrator.RestartBackoff {
		if delay <= 0 {
			return errors.New(errors.ErrValidationFailed, "team.orchestrator.restartBackoff values must be positive")
		}
	}
	if config.Orchestrator.MaxRestartBackoff < 0 {
		return errors.New(errors.ErrValidationFailed, "team.orchestrator.maxRestartBackoff cannot be negative")
	}
	return nil
}

// ValidateAgentControlConfig validates global AgentControl persistence
// configuration.
func ValidateAgentControlConfig(config *AgentControlConfig) error {
	if config == nil {
		return nil
	}
	if strings.TrimSpace(config.StorePath) != "" && strings.TrimSpace(config.StoreDSN) != "" {
		return errors.New(errors.ErrValidationFailed, "agentControl storePath and storeDSN cannot both be set")
	}
	if strings.TrimSpace(config.MailboxStorePath) != "" && strings.TrimSpace(config.MailboxStoreDSN) != "" {
		return errors.New(errors.ErrValidationFailed, "agentControl mailboxStorePath and mailboxStoreDSN cannot both be set")
	}
	if strings.TrimSpace(config.AgentStorePath) != "" && strings.TrimSpace(config.AgentStoreDSN) != "" {
		return errors.New(errors.ErrValidationFailed, "agentControl agentStorePath and agentStoreDSN cannot both be set")
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
	switch strings.ToLower(strings.TrimSpace(config.DefaultPersistence)) {
	case "", "memory", "file":
	default:
		return errors.New(errors.ErrValidationFailed, "sessionRuntime.defaultPersistence must be one of memory or file")
	}
	return nil
}

// ValidateArtifactConfig validates artifact persistence configuration.
func ValidateArtifactConfig(config *ArtifactConfig) error {
	if config == nil {
		return nil
	}
	if strings.TrimSpace(config.StorePath) != "" && strings.TrimSpace(config.StoreDSN) != "" {
		return errors.New(errors.ErrValidationFailed, "artifact storePath and storeDSN cannot both be set")
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
	if config.MaxDiffBytes < 0 {
		return errors.New(errors.ErrValidationFailed, "checkpoint.maxDiffBytes cannot be negative")
	}
	if config.MaxCheckpointsPerSession < 0 {
		return errors.New(errors.ErrValidationFailed, "checkpoint.maxCheckpointsPerSession cannot be negative")
	}
	mode := strings.ToLower(strings.TrimSpace(config.StoreMode))
	if mode != "" && mode != "diff" && mode != "full" {
		return errors.New(errors.ErrValidationFailed, "checkpoint.storeMode must be \"diff\" or \"full\"")
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
	if config.MonitorInterval < 0 || config.HeartbeatTimeout < 0 || config.RetryBackoff < 0 {
		return errors.New(errors.ErrValidationFailed, "background watchdog durations cannot be negative")
	}
	if config.LaunchMaxAttempts < 0 {
		return errors.New(errors.ErrValidationFailed, "background.launchMaxAttempts cannot be negative")
	}
	if config.RecoveryMaxAttempts < -1 {
		return errors.New(errors.ErrValidationFailed, "background.recoveryMaxAttempts must be -1 or greater")
	}
	for _, delay := range config.RecoveryBackoffSchedule {
		if delay <= 0 {
			return errors.New(errors.ErrValidationFailed, "background.recoveryBackoffSchedule entries must be positive")
		}
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

func ValidateWorkspaceConfig(config *WorkspaceConfig) error {
	if config == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(config.Mode)) {
	case "", runtimecontext.WorkspaceModeDisabled, runtimecontext.WorkspaceModeSignals, runtimecontext.WorkspaceModeBroad:
	default:
		return errors.New(errors.ErrValidationFailed, "workspace.mode must be disabled, signals, or broad")
	}
	for _, item := range []struct {
		name  string
		value int
	}{
		{"maxChunkSize", config.MaxChunkSize},
		{"chunkOverlap", config.ChunkOverlap},
	} {
		if item.value < 0 {
			return errors.New(errors.ErrValidationFailed, fmt.Sprintf("workspace.%s cannot be negative", item.name))
		}
	}
	if config.MaxFileSize < 0 {
		return errors.New(errors.ErrValidationFailed, "workspace.maxFileSize cannot be negative")
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
		{"fallbackMaxPromptTokens", config.FallbackMaxPromptTokens},
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
		{"workspaceMode", strings.TrimSpace(config.WorkspaceMode), []string{"", runtimecontext.WorkspaceModeDisabled, runtimecontext.WorkspaceModeSignals, runtimecontext.WorkspaceModeBroad}},
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
	if config.DefaultModel == "" {
		return errors.New(errors.ErrValidationFailed, "defaultModel cannot be empty")
	}
	if config.MaxMemorySize < 0 {
		return errors.New(errors.ErrValidationFailed, "maxMemorySize cannot be negative")
	}
	if config.Timeout < 0 {
		return errors.New(errors.ErrValidationFailed, "timeout cannot be negative")
	}
	if config.DefaultPlanningMode != "" {
		mode := strings.ToLower(strings.TrimSpace(config.DefaultPlanningMode))
		switch mode {
		case "planner_preferred", "route_preferred", "agent_only", "llm_only":
		default:
			return errors.New(errors.ErrValidationFailed, "defaultPlanningMode must be planner_preferred, route_preferred, agent_only, or llm_only")
		}
	}
	if config.MaxParallelToolCalls < 0 {
		return errors.New(errors.ErrValidationFailed, "maxParallelToolCalls cannot be negative")
	}
	if config.MaxToolCalls < 0 || config.MaxExplorationSteps < 0 || config.MaxRepeatedToolCalls < 0 {
		return errors.New(errors.ErrValidationFailed, "agent execution limits cannot be negative")
	}
	return nil
}

// ValidateAgentsConfig validates lightweight multi-agent limits.
func ValidateAgentsConfig(config *AgentsConfig) error {
	if config == nil {
		return nil
	}
	// P2-8 语义收敛：0 = 未设置（回退默认 6），-1 = 显式不限，正数 = 配额。
	if config.MaxThreads < 0 && config.MaxThreads != -1 {
		return errors.New(errors.ErrValidationFailed, "agents.maxThreads must be -1 (unlimited), 0 (default), or a positive integer")
	}
	if config.ReclaimIdleMs < 0 {
		return errors.New(errors.ErrValidationFailed, "agents.reclaimIdleMs cannot be negative")
	}
	if config.MaxDepth < 0 {
		return errors.New(errors.ErrValidationFailed, "agents.maxDepth cannot be negative")
	}
	// P1-4/H12: 0 = 未设置（回退默认 4），正数 = 每个 batch 的并发上限。
	if config.MaxConcurrent < 0 {
		return errors.New(errors.ErrValidationFailed, "agents.maxConcurrent must be 0 (default) or a positive integer")
	}
	// 背压是可选项：0 = 关闭（保持「无限等待、无队列超时」的既有行为）。
	if config.MaxConcurrentQueueDepth < 0 {
		return errors.New(errors.ErrValidationFailed, "agents.maxConcurrentQueueDepth cannot be negative")
	}
	if config.MaxConcurrentQueueTimeoutMs < 0 {
		return errors.New(errors.ErrValidationFailed, "agents.maxConcurrentQueueTimeoutMs cannot be negative")
	}
	if config.DefaultWaitTimeoutMs < 0 {
		return errors.New(errors.ErrValidationFailed, "agents.defaultWaitTimeoutMs cannot be negative")
	}
	if config.MinWaitTimeoutMs < 0 {
		return errors.New(errors.ErrValidationFailed, "agents.minWaitTimeoutMs cannot be negative")
	}
	if config.MaxWaitTimeoutMs < 0 {
		return errors.New(errors.ErrValidationFailed, "agents.maxWaitTimeoutMs cannot be negative")
	}
	switch strings.ToLower(strings.TrimSpace(config.AutoCloseCompleted)) {
	case "", AutoClosePolicyOff, AutoClosePolicyCompleted, AutoClosePolicyBatchTerminal:
	default:
		return errors.New(errors.ErrValidationFailed, "agents.autoCloseCompleted must be off, completed or batch_terminal")
	}
	if config.MinWaitTimeoutMs > 0 && config.MaxWaitTimeoutMs > 0 && config.MinWaitTimeoutMs > config.MaxWaitTimeoutMs {
		return errors.New(errors.ErrValidationFailed, "agents.minWaitTimeoutMs cannot exceed agents.maxWaitTimeoutMs")
	}
	switch strings.ToLower(strings.TrimSpace(config.WaitTimeoutMode)) {
	case "", "clamp", "error":
	default:
		return errors.New(errors.ErrValidationFailed, "agents.waitTimeoutMode must be clamp or error")
	}
	forkTurns := strings.ToLower(strings.TrimSpace(config.DefaultForkTurns))
	if forkTurns == "" || forkTurns == "none" || forkTurns == "all" {
		return nil
	}
	if value, err := strconv.Atoi(forkTurns); err != nil || value <= 0 {
		return errors.New(errors.ErrValidationFailed, "agents.defaultForkTurns must be none, all, or a positive integer")
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
	if _, err := runtimeexecutor.NormalizeOSSandboxMode(config.OSSandbox); err != nil {
		return errors.New(errors.ErrValidationFailed, err.Error())
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
	rm.candidateFilePath = ""
	if rm.config == nil || !rm.config.Rollout.Enabled {
		return nil
	}
	candidateFile := strings.TrimSpace(rm.config.Rollout.CandidateFile)
	if candidateFile == "" {
		return nil
	}
	candidatePath := resolveRuntimeConfigReference(rm.filePath, candidateFile)
	candidate, err := loadRuntimeConfigFromFile(candidatePath)
	if err != nil {
		return errors.Wrap(errors.ErrConfigInvalid, fmt.Sprintf("failed to load candidate config: %s", candidatePath), err)
	}
	if candidate == nil {
		return nil
	}
	ensureRuntimeConfigVersion(candidate)
	if rm.config.Rollout.CandidateVersion != "" && candidate.Version != rm.config.Rollout.CandidateVersion {
		return errors.New(errors.ErrValidationFailed, fmt.Sprintf("candidate version mismatch: expected %s, got %s", rm.config.Rollout.CandidateVersion, candidate.Version))
	}
	rm.candidate = candidate
	rm.candidateFilePath = candidatePath
	return nil
}

func resolveRuntimeConfigReference(baseFile, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) || strings.TrimSpace(baseFile) == "" {
		return path
	}
	baseDir := filepath.Dir(strings.TrimSpace(baseFile))
	if baseDir == "" || baseDir == "." {
		return path
	}
	return filepath.Join(baseDir, path)
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
