package knowledge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Mode 选择知识层的运行模式（06 §附录 B / 04 Phase 0）。
type Mode string

const (
	// ModeOff 完全关闭知识层（默认值）。
	ModeOff Mode = "off"
	// ModeShadow 构建索引但绝不注入提示词。
	ModeShadow Mode = "shadow"
	// ModeOn 构建索引、暴露工具面并注入上下文。
	ModeOn Mode = "on"
)

// Phase 3 的 code.* 工具面开关（06 §4 Phase 3；独立于 Mode，默认 off）。
//
// 灰度与回滚口径（04 §5 Phase 3）：mode=on 只代表知识层可用；工具面是否
// 出现由本开关决定。关闭它即回到纯 grep/view 基线（与改动前逐字节一致）。
const (
	// CodeToolsOff 不注册 code.*（默认）。
	CodeToolsOff = "off"
	// CodeToolsOn 注册 code.*：索引可用时走索引，否则按降级协议 fallback。
	CodeToolsOn = "on"
)

// Phase 4（04 §5 Phase 4）的 adapter 与 LSP 上限默认值。
//
// 全部是"保守起步值"，验收门槛由真实测量回写（06 §4 Phase 4 验收门槛）。
const (
	// DefaultAdapter 是未配置时的索引适配器（零依赖，任何环境可用）。
	DefaultAdapter = string(AdapterBuiltin)
	// DefaultLSPMaxProcesses 是知识层自管 LSP 进程数上限（ADR-0002 D7 的量化）。
	DefaultLSPMaxProcesses = 1
	// DefaultLSPMemoryLimitMB 是单个 LSP 进程的内存软上限（06 §4 Phase 4 验收）。
	DefaultLSPMemoryLimitMB = 512
	// DefaultLSPStartupTimeout 是 initialize 握手的上限。
	DefaultLSPStartupTimeout = 15 * time.Second
	// DefaultLSPRequestTimeout 是单次语义查询（definition/references）的上限。
	DefaultLSPRequestTimeout = 5 * time.Second
)

// 默认限额。阈值必须由 Phase 0 基线校准（04 §7.6），此处只提供可运行的初值。
const (
	// DefaultMaxFileBytes 是单文件参与索引的上限；超过则记为 index_state=error。
	DefaultMaxFileBytes int64 = 2 << 20 // 2 MiB
	// DefaultMaxDBSizeMB 是 knowledge.db 的软上限（Phase 5 的 GC 触发条件）。
	// 2026-09-29 校准：本仓库实测 313.9 MiB（04 §7.4），初值 200 会在一开始
	// 就压着上限，默认取 512。
	DefaultMaxDBSizeMB int64 = 512
	// DefaultDBRelativePath 是知识库相对 workspace 的默认落点（06 §附录 B）。
	DefaultDBRelativePath = ".aicli/knowledge/knowledge.db"
)

// Reuse Gate 的保守默认阈值（06 §4 Phase 2 W3 / 04 §4.4 阈值策略）。
//
// 初值用于起步，不是验收门槛：Phase 2 实测（W7 / 04 §7.6）后必须回写校准。
// 边界语义统一为"== 阈值视为通过"（≥）。
const (
	// DefaultMinReuseConfidence 是同任务内复用的直接复用下限。
	DefaultMinReuseConfidence = 0.80
	// DefaultWriteReuseConfidence 是同任务内涉及写操作的下限
	// （复用 + 强制一次验证读取）。
	DefaultWriteReuseConfidence = 0.90
	// DefaultCrossTaskConfidence 是跨任务复用的下限
	// （复用 + 强制验证读取）。
	DefaultCrossTaskConfidence = 0.90
	// DefaultVerifyReadBelow 是"必须安排一次验证读取"的置信度上限。
	DefaultVerifyReadBelow = 0.90
	// DefaultExploreBelow 是硬下限：低于它触发探索，探索失败再 fallback 到
	// grep/view（04 §4.4 的 0.50–0.80 待验证带下界）。
	DefaultExploreBelow = 0.50
)

// ErrInvalidMode 表示配置的 mode 不是 off|shadow|on 之一。
var ErrInvalidMode = errors.New("knowledge: invalid mode")

// ErrInvalidCodeTools 表示配置的 code_tools 不是 off|on 之一。
var ErrInvalidCodeTools = errors.New("knowledge: invalid code_tools")

// ErrInvalidLSPMode 表示配置的 lsp.mode 不是 off|self 之一（ADR-0002 §4.3：
// v1 不提供 external，避免 no-op 选项）。
var ErrInvalidLSPMode = errors.New("knowledge: invalid lsp mode")

// IndexConfig 是 `knowledge.index.*` 配置段（Phase 4）。
type IndexConfig struct {
	// FullRebuildOnAdapterChange：库内记录的 adapter 版本与当前不一致时是否
	// 全量重建（默认 true）。false 时保留旧索引并把差异留给后续诊断。
	FullRebuildOnAdapterChange *bool `yaml:"full_rebuild_on_adapter_change,omitempty" json:"full_rebuild_on_adapter_change,omitempty"`
	// UseGitignore 控制索引遍历是否读取工作区内的 .gitignore（默认 true）。
	// 关闭后只保留内置忽略集与隐藏目录规则，是"忽略规则误伤"的回滚逃生舱。
	UseGitignore *bool `yaml:"use_gitignore,omitempty" json:"use_gitignore,omitempty"`
}

// FullRebuildOnAdapterChangeEnabled 报告开关是否生效（缺省 true）。
func (c IndexConfig) FullRebuildOnAdapterChangeEnabled() bool {
	return c.FullRebuildOnAdapterChange == nil || *c.FullRebuildOnAdapterChange
}

// GCRetention 返回软删除行的保留期（缺省 DefaultGCRetentionDays）。
func (c Config) GCRetention() time.Duration {
	days := DefaultGCRetentionDays
	if c.GCRetentionDays != nil && *c.GCRetentionDays > 0 {
		days = *c.GCRetentionDays
	}
	return time.Duration(days) * 24 * time.Hour
}

// UseGitignoreEnabled 报告 .gitignore 过滤是否生效（缺省 true）。
func (c IndexConfig) UseGitignoreEnabled() bool {
	return c.UseGitignore == nil || *c.UseGitignore
}

// LSPConfig 是 `knowledge.lsp.*` 配置段（ADR-0002 §4.1 / ADR-0005 / ADR-0006）。
//
// 双重门控：Enabled 是硬闸（逃生舱），Mode 是行为选择（v1 仅 off|self）。
type LSPConfig struct {
	// Enabled 是全局总开关；false 时任何入口都不起 LSP。
	Enabled bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// Mode 取值 off | self；默认 off（保守一侧：不产生重复 LSP 实例）。
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`
	// MaxProcesses 是自管 LSP 进程数上限。
	MaxProcesses int `yaml:"max_processes,omitempty" json:"max_processes,omitempty"`
	// MemoryLimitMB 是单进程内存软上限（超限触发回收）。
	MemoryLimitMB int `yaml:"memory_limit_mb,omitempty" json:"memory_limit_mb,omitempty"`
	// StartupTimeout 是 initialize 握手超时。
	StartupTimeout time.Duration `yaml:"startup_timeout,omitempty" json:"startup_timeout,omitempty"`
	// RequestTimeout 是单次语义查询超时。
	RequestTimeout time.Duration `yaml:"request_timeout,omitempty" json:"request_timeout,omitempty"`
}

// LSPModeOff / LSPModeSelf 是 v1 的合法取值（ADR-0002 §4.1）。
const (
	LSPModeOff  = "off"
	LSPModeSelf = "self"
)

// ParseLSPMode 解析 lsp.mode；空值等价 off。
func ParseLSPMode(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", LSPModeOff, "false", "0", "disabled":
		return LSPModeOff, nil
	case LSPModeSelf, "true", "1", "enabled":
		return LSPModeSelf, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidLSPMode, s)
	}
}

// Normalize 补齐 LSP 段的零值。
func (c LSPConfig) Normalize() LSPConfig {
	if parsed, err := ParseLSPMode(c.Mode); err == nil {
		c.Mode = parsed
	} else {
		c.Mode = LSPModeOff
	}
	if c.MaxProcesses <= 0 {
		c.MaxProcesses = DefaultLSPMaxProcesses
	}
	if c.MemoryLimitMB <= 0 {
		c.MemoryLimitMB = DefaultLSPMemoryLimitMB
	}
	if c.StartupTimeout <= 0 {
		c.StartupTimeout = DefaultLSPStartupTimeout
	}
	if c.RequestTimeout <= 0 {
		c.RequestTimeout = DefaultLSPRequestTimeout
	}
	return c
}

// Validate 拒绝非法取值与越界上限。
func (c LSPConfig) Validate() error {
	if _, err := ParseLSPMode(c.Mode); err != nil {
		return err
	}
	if c.MaxProcesses < 0 {
		return fmt.Errorf("knowledge: lsp.max_processes must be >= 0, got %d", c.MaxProcesses)
	}
	if c.MemoryLimitMB < 0 {
		return fmt.Errorf("knowledge: lsp.memory_limit_mb must be >= 0, got %d", c.MemoryLimitMB)
	}
	return nil
}

// SemanticEnabled 报告语义通道（进程外 LSP）是否应当被装配。
func (c LSPConfig) SemanticEnabled() bool {
	normalized := c.Normalize()
	return normalized.Enabled && normalized.Mode == LSPModeSelf
}

// PlannerConfig 是 `knowledge.planner.*` 配置段：Reuse Gate 的阈值组
// （06 §4 Phase 2 W3）。零值字段由 Normalize 补齐为上述默认值；显式配置
// （含 0.85 这类非默认值）原样保留。
type PlannerConfig struct {
	// MinReuseConfidence 是同任务内复用的直接复用下限（默认 0.80）。
	MinReuseConfidence float64 `yaml:"min_reuse_confidence,omitempty" json:"min_reuse_confidence,omitempty"`
	// WriteReuseConfidence 是同任务内写操作的下限（默认 0.90）。
	WriteReuseConfidence float64 `yaml:"write_reuse_confidence,omitempty" json:"write_reuse_confidence,omitempty"`
	// CrossTaskConfidence 是跨任务复用的下限（默认 0.90）。
	CrossTaskConfidence float64 `yaml:"cross_task_confidence,omitempty" json:"cross_task_confidence,omitempty"`
	// VerifyReadBelow 是必须安排一次验证读取的上限（默认 0.90）。
	VerifyReadBelow float64 `yaml:"verify_read_below,omitempty" json:"verify_read_below,omitempty"`
	// ExploreBelow 是触发探索的硬下限（默认 0.50）。
	ExploreBelow float64 `yaml:"explore_below,omitempty" json:"explore_below,omitempty"`
}

// DefaultPlannerConfig 返回 W3 的保守默认阈值组。
func DefaultPlannerConfig() PlannerConfig {
	return PlannerConfig{
		MinReuseConfidence:   DefaultMinReuseConfidence,
		WriteReuseConfidence: DefaultWriteReuseConfidence,
		CrossTaskConfidence:  DefaultCrossTaskConfidence,
		VerifyReadBelow:      DefaultVerifyReadBelow,
		ExploreBelow:         DefaultExploreBelow,
	}
}

// Normalize 把未配置（<=0）的字段补齐为默认值并返回副本；越界值不在这里
// 夹取（由 Validate 显式拒绝），避免把坏配置静默改成另一种阈值。
func (p PlannerConfig) Normalize() PlannerConfig {
	defaults := DefaultPlannerConfig()
	if p.MinReuseConfidence <= 0 {
		p.MinReuseConfidence = defaults.MinReuseConfidence
	}
	if p.WriteReuseConfidence <= 0 {
		p.WriteReuseConfidence = defaults.WriteReuseConfidence
	}
	if p.CrossTaskConfidence <= 0 {
		p.CrossTaskConfidence = defaults.CrossTaskConfidence
	}
	if p.VerifyReadBelow <= 0 {
		p.VerifyReadBelow = defaults.VerifyReadBelow
	}
	if p.ExploreBelow <= 0 {
		p.ExploreBelow = defaults.ExploreBelow
	}
	return p
}

// Validate 校验阈值域与排序：所有阈值必须落在 (0,1]；explore_below 不得高于
// 同任务直接复用下限（否则 0.50–0.80 的"待验证"带为空，语义自相矛盾）。
func (p PlannerConfig) Validate() error {
	normalized := p.Normalize()
	fields := []struct {
		name  string
		value float64
	}{
		{"min_reuse_confidence", normalized.MinReuseConfidence},
		{"write_reuse_confidence", normalized.WriteReuseConfidence},
		{"cross_task_confidence", normalized.CrossTaskConfidence},
		{"verify_read_below", normalized.VerifyReadBelow},
		{"explore_below", normalized.ExploreBelow},
	}
	for _, field := range fields {
		if field.value <= 0 || field.value > 1 {
			return fmt.Errorf("knowledge: planner.%s must be in (0,1], got %v", field.name, field.value)
		}
	}
	if normalized.ExploreBelow > normalized.MinReuseConfidence {
		return fmt.Errorf(
			"knowledge: planner.explore_below (%v) must not exceed min_reuse_confidence (%v)",
			normalized.ExploreBelow, normalized.MinReuseConfidence,
		)
	}
	return nil
}

// ReuseFloor 返回给定作用域 / 写语义下的直接复用阈值（04 §4.4）：
// 同任务 0.80、同任务写操作 0.90、跨任务 0.90（取各适用阈值的最大值）。
func (p PlannerConfig) ReuseFloor(scope ReuseScope, write bool) float64 {
	normalized := p.Normalize()
	floor := normalized.MinReuseConfidence
	if scope.Normalize() == ReuseScopeCrossTask && normalized.CrossTaskConfidence > floor {
		floor = normalized.CrossTaskConfidence
	}
	if write && normalized.WriteReuseConfidence > floor {
		floor = normalized.WriteReuseConfidence
	}
	return floor
}

// Config 是知识层的解析后配置。
//
// 该结构同时作为 `knowledge:` YAML 段的承载类型（internal/config.RuntimeConfig
// 直接内嵌本类型），因此字段必须带 yaml/json tag；Workspace 由运行时按当前
// 工作区注入，不来自配置文件。
type Config struct {
	// Mode 是解析后的运行模式；空值等价于 off。
	Mode Mode `yaml:"mode" json:"mode"`
	// DBPath 覆盖默认的知识库路径；相对路径按 workspace 解析。
	DBPath string `yaml:"db_path,omitempty" json:"db_path,omitempty"`
	// MaxFileBytes 是单文件索引上限（字节）。
	MaxFileBytes int64 `yaml:"max_file_bytes,omitempty" json:"max_file_bytes,omitempty"`
	// MaxDBSizeMB 是 knowledge.db 软上限（MB），Phase 5 GC 使用。
	MaxDBSizeMB int64 `yaml:"max_db_size_mb,omitempty" json:"max_db_size_mb,omitempty"`
	// GCRetentionDays 是软删除行的保留期（天）；<= 0 / 未设置时取
	// DefaultGCRetentionDays（04 §5 Phase 5 交付 4）。
	GCRetentionDays *int `yaml:"gc_retention_days,omitempty" json:"gc_retention_days,omitempty"`
	// Alpha 是 shadow 覆盖率阈值 α（ADR-0003 §4.2）：usable 判定与 M1 复算共用，
	// 由 Phase1-shadow 实测校准（04 §7.6）；<= 0 时取 DefaultShadowAlpha。
	Alpha float64 `yaml:"alpha,omitempty" json:"alpha,omitempty"`
	// Planner 是 Reuse Gate 的阈值组（06 §4 Phase 2 W3）。
	Planner PlannerConfig `yaml:"planner,omitempty" json:"planner,omitempty"`
	// CodeTools 控制 Phase 3 的 code.* 工具面是否注册（off|on，默认 off）。
	// 独立于 Mode：mode=on 但 code_tools 未开启时工具面不出现（灰度/回滚）。
	CodeTools string `yaml:"code_tools,omitempty" json:"code_tools,omitempty"`
	// Watch 控制 Phase 5 交付 1 的第三类变更源（fsnotify 文件系统监听，off|on，
	// 默认 off）。开启后空闲期的外部变更（编辑器保存 / 脚本写盘 / git checkout）
	// 会被即时发现并入队；关闭时仍由判定点校正（git/stat）在下一个 turn 边界发现。
	// 默认 off 的理由：watch 是系统级资源（inotify 配额），默认开启会改变既有
	// 部署的资源画像。
	Watch string `yaml:"watch,omitempty" json:"watch,omitempty"`
	// Adapter 选择索引适配器：builtin（默认）/ treesitter / lsp（Phase 4 交付 2）。
	// 不可用的选择会降级 builtin 并记录原因，而不是失败（Degrade-Not-Fail）。
	Adapter string `yaml:"adapter,omitempty" json:"adapter,omitempty"`
	// Index 是索引管线行为开关（Phase 4 交付 4）。
	Index IndexConfig `yaml:"index,omitempty" json:"index,omitempty"`
	// LSP 是进程外语义通道的开关与资源上限（Phase 4 交付 2/3）。
	LSP LSPConfig `yaml:"lsp,omitempty" json:"lsp,omitempty"`
	// Workspace 是被索引的工作区根目录（运行时注入，不来自 YAML）。
	Workspace string `yaml:"-" json:"-"`
}

// DefaultConfig 返回默认配置：mode=off，知识层完全不参与任何路径。
func DefaultConfig() Config {
	return Config{
		Mode:         ModeOff,
		MaxFileBytes: DefaultMaxFileBytes,
		MaxDBSizeMB:  DefaultMaxDBSizeMB,
		Planner:      DefaultPlannerConfig(),
		Adapter:      DefaultAdapter,
	}
}

// Default 是 DefaultConfig 的别名，保留给早期调用方。
func Default() Config { return DefaultConfig() }

// DefaultMode 返回未配置时使用的模式。
func DefaultMode() Mode { return ModeOff }

// ParseMode 解析用户提供的模式字符串；空值等价于 off。
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(ModeOff), "false", "0", "disabled":
		return ModeOff, nil
	case string(ModeShadow):
		return ModeShadow, nil
	case string(ModeOn), "true", "1", "enabled":
		return ModeOn, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidMode, s)
	}
}

// ParseCodeTools 解析 code_tools 取值；空值等价 off（fail closed）。
func ParseCodeTools(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", CodeToolsOff, "false", "0", "disabled":
		return CodeToolsOff, nil
	case CodeToolsOn, "true", "1", "enabled":
		return CodeToolsOn, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidCodeTools, s)
	}
}

// ErrInvalidWatch 表示配置的 watch 取值不是 off|on 之一。
var ErrInvalidWatch = errors.New("knowledge: invalid watch value")

// ParseWatch 解析 watch 取值；空值等价 off（fail closed）。
func ParseWatch(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", CodeToolsOff, "false", "0", "disabled":
		return CodeToolsOff, nil
	case CodeToolsOn, "true", "1", "enabled":
		return CodeToolsOn, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidWatch, s)
	}
}

// Normalize 补齐零值（mode/限额）并返回规范化后的副本。
// 它不校验 workspace——workspace 由 Open 在启用时校验。
func (c Config) Normalize() Config {
	if strings.TrimSpace(string(c.Mode)) == "" {
		c.Mode = ModeOff
	} else if mode, err := ParseMode(string(c.Mode)); err == nil {
		c.Mode = mode
	}
	if c.MaxFileBytes <= 0 {
		c.MaxFileBytes = DefaultMaxFileBytes
	}
	if c.MaxDBSizeMB <= 0 {
		c.MaxDBSizeMB = DefaultMaxDBSizeMB
	}
	if c.Alpha <= 0 {
		c.Alpha = DefaultShadowAlpha
	}
	c.Planner = c.Planner.Normalize()
	if parsed, err := ParseCodeTools(c.CodeTools); err == nil {
		c.CodeTools = parsed
	}
	if parsed, err := ParseWatch(c.Watch); err == nil {
		c.Watch = parsed
	}
	if kind, err := ParseAdapterKind(c.Adapter); err == nil {
		c.Adapter = string(kind)
	}
	c.LSP = c.LSP.Normalize()
	c.Workspace = strings.TrimSpace(c.Workspace)
	c.DBPath = strings.TrimSpace(c.DBPath)
	return c
}

// Validate 校验模式与（启用时的）workspace。
func (c Config) Validate() error {
	if _, err := ParseMode(string(c.Mode)); err != nil {
		return err
	}
	if c.Mode != ModeOff && c.Workspace == "" {
		return errors.New("knowledge: workspace must be set when mode is shadow or on")
	}
	if c.Alpha < 0 || c.Alpha > 1 {
		return fmt.Errorf("knowledge: alpha must be in (0,1], got %v", c.Alpha)
	}
	if err := c.Planner.Validate(); err != nil {
		return err
	}
	if _, err := ParseCodeTools(c.CodeTools); err != nil {
		return err
	}
	if _, err := ParseWatch(c.Watch); err != nil {
		return err
	}
	if _, err := ParseAdapterKind(c.Adapter); err != nil {
		return err
	}
	if err := c.LSP.Validate(); err != nil {
		return err
	}
	return nil
}

// Enabled 报告配置是否启用知识层。
func (c Config) Enabled() bool { return c.Normalize().Mode != ModeOff }

// WatchEnabled 报告交付 1 的第三类变更源（fsnotify 监听）是否开启（默认 off）。
func (c Config) WatchEnabled() bool {
	parsed, err := ParseWatch(c.Watch)
	return err == nil && parsed == CodeToolsOn
}

// CodeToolsEnabled 报告 Phase 3 的 code.* 工具面是否开启（默认 off）。
func (c Config) CodeToolsEnabled() bool {
	parsed, err := ParseCodeTools(c.CodeTools)
	return err == nil && parsed == CodeToolsOn
}

// AdapterKind 返回解析后的索引适配器；非法值回落 builtin（加载期已由
// Validate 拒绝，此处只保证运行期不 panic）。
func (c Config) AdapterKind() AdapterKind {
	kind, err := ParseAdapterKind(c.Adapter)
	if err != nil {
		return AdapterBuiltin
	}
	return kind
}

// WithWorkspace 返回绑定到指定工作区的配置副本。
func (c Config) WithWorkspace(workspace string) Config {
	c.Workspace = strings.TrimSpace(workspace)
	return c
}

// storePath 返回 knowledge.db 的绝对路径。
func (c Config) storePath() string {
	c = c.Normalize()
	if c.DBPath != "" {
		if filepath.IsAbs(c.DBPath) {
			return c.DBPath
		}
		return filepath.Join(c.Workspace, filepath.FromSlash(c.DBPath))
	}
	return filepath.Join(c.Workspace, filepath.FromSlash(DefaultDBRelativePath))
}

// StorePathFor 返回配置对应的 knowledge.db 绝对路径（状态面 / 诊断用）。
//
// 与 Open 的落点逐字节一致（含 DBPath 覆盖与相对路径解析），因此 CLI 可以
// 在不打开 store 的前提下回答"这个 workspace 有没有索引过"。
func StorePathFor(cfg Config) string { return cfg.storePath() }

// dirPath 返回知识库所在目录。
func (c Config) dirPath() string { return filepath.Dir(c.storePath()) }

// ensureDir 创建知识库目录。
func (c Config) ensureDir() error {
	dir := c.dirPath()
	if dir == "" || dir == "." {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("knowledge: create store directory: %w", err)
	}
	return nil
}
