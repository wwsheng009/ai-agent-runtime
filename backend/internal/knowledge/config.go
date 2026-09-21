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

// 默认限额。阈值必须由 Phase 0 基线校准（04 §7.6），此处只提供可运行的初值。
const (
	// DefaultMaxFileBytes 是单文件参与索引的上限；超过则记为 index_state=error。
	DefaultMaxFileBytes int64 = 2 << 20 // 2 MiB
	// DefaultMaxDBSizeMB 是 knowledge.db 的软上限（Phase 5 的 GC 触发条件）。
	DefaultMaxDBSizeMB int64 = 200
	// DefaultDBRelativePath 是知识库相对 workspace 的默认落点（06 §附录 B）。
	DefaultDBRelativePath = ".aicli/knowledge/knowledge.db"
)

// ErrInvalidMode 表示配置的 mode 不是 off|shadow|on 之一。
var ErrInvalidMode = errors.New("knowledge: invalid mode")

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
	// Workspace 是被索引的工作区根目录（运行时注入，不来自 YAML）。
	Workspace string `yaml:"-" json:"-"`
}

// DefaultConfig 返回默认配置：mode=off，知识层完全不参与任何路径。
func DefaultConfig() Config {
	return Config{
		Mode:         ModeOff,
		MaxFileBytes: DefaultMaxFileBytes,
		MaxDBSizeMB:  DefaultMaxDBSizeMB,
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
	return nil
}

// Enabled 报告配置是否启用知识层。
func (c Config) Enabled() bool { return c.Normalize().Mode != ModeOff }

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
