package knowledge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// 默认限额。阈值必须由 Phase 0 基线校准（04 §7.6），此处只提供可运行的初值。
const (
	// DefaultMaxFileBytes 是单文件参与索引的上限；超过则记为 index_state=error。
	DefaultMaxFileBytes int64 = 2 << 20 // 2 MiB
	// DefaultMaxDBSizeMB 是 knowledge.db 的软上限（Phase 5 的 GC 触发条件）。
	DefaultMaxDBSizeMB int64 = 200
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
	// Alpha 是 shadow 覆盖率阈值 α（ADR-0003 §4.2）：usable 判定与 M1 复算共用，
	// 由 Phase1-shadow 实测校准（04 §7.6）；<= 0 时取 DefaultShadowAlpha。
	Alpha float64 `yaml:"alpha,omitempty" json:"alpha,omitempty"`
	// Planner 是 Reuse Gate 的阈值组（06 §4 Phase 2 W3）。
	Planner PlannerConfig `yaml:"planner,omitempty" json:"planner,omitempty"`
	// CodeTools 控制 Phase 3 的 code.* 工具面是否注册（off|on，默认 off）。
	// 独立于 Mode：mode=on 但 code_tools 未开启时工具面不出现（灰度/回滚）。
	CodeTools string `yaml:"code_tools,omitempty" json:"code_tools,omitempty"`
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
	return nil
}

// Enabled 报告配置是否启用知识层。
func (c Config) Enabled() bool { return c.Normalize().Mode != ModeOff }

// CodeToolsEnabled 报告 Phase 3 的 code.* 工具面是否开启（默认 off）。
func (c Config) CodeToolsEnabled() bool {
	parsed, err := ParseCodeTools(c.CodeTools)
	return err == nil && parsed == CodeToolsOn
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
