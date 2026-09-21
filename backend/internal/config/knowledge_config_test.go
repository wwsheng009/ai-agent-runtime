package config

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"gopkg.in/yaml.v3"
)

// Phase 0 交付 1：knowledge.mode=off 是默认值——知识层代码存在，但不配置时
// 完全不参与任何路径（04 §5 排期铁律 ③）。
func TestKnowledgeConfigDefaultsToOff(t *testing.T) {
	cfg := DefaultRuntimeConfig()

	require.Equal(t, knowledge.ModeOff, cfg.Knowledge.Mode)
	require.False(t, cfg.Knowledge.Enabled(), "默认配置不得启用知识层")
	require.NoError(t, ValidateRuntimeConfig(cfg))
}

// YAML 段与 04 §5 的 mode 取值对齐（off|shadow|on），且 workspace 不来自文件。
func TestKnowledgeConfigParsesYAML(t *testing.T) {
	cfg := DefaultRuntimeConfig()
	require.NoError(t, yaml.Unmarshal([]byte(`
knowledge:
  mode: shadow
  db_path: .aicli/knowledge/custom.db
  max_file_bytes: 1024
  max_db_size_mb: 64
`), cfg))

	require.Equal(t, knowledge.ModeShadow, cfg.Knowledge.Mode)
	require.True(t, cfg.Knowledge.Enabled())
	require.Equal(t, ".aicli/knowledge/custom.db", cfg.Knowledge.DBPath)
	require.EqualValues(t, 1024, cfg.Knowledge.MaxFileBytes)
	require.EqualValues(t, 64, cfg.Knowledge.MaxDBSizeMB)
	require.Empty(t, cfg.Knowledge.Workspace, "workspace 由运行时注入，配置文件不得设置")
	require.NoError(t, ValidateRuntimeConfig(cfg))
}

// 未知 mode 必须在加载期被拒绝，而不是推迟到 knowledge.Open 才暴露。
func TestKnowledgeConfigRejectsUnknownMode(t *testing.T) {
	cfg := DefaultRuntimeConfig()
	require.NoError(t, yaml.Unmarshal([]byte("knowledge:\n  mode: turbo\n"), cfg))

	require.Error(t, ValidateRuntimeConfig(cfg))
}

// 负数限额被拒绝；未配置的限额保持 DefaultRuntimeConfig 提供的默认值。
func TestKnowledgeConfigRejectsNegativeLimits(t *testing.T) {
	cfg := DefaultRuntimeConfig()
	cfg.Knowledge.MaxFileBytes = -1
	require.Error(t, ValidateRuntimeConfig(cfg))

	cfg = DefaultRuntimeConfig()
	cfg.Knowledge.MaxDBSizeMB = -1
	require.Error(t, ValidateRuntimeConfig(cfg))

	cfg = DefaultRuntimeConfig()
	require.EqualValues(t, knowledge.DefaultMaxFileBytes, cfg.Knowledge.MaxFileBytes)
	require.EqualValues(t, knowledge.DefaultMaxDBSizeMB, cfg.Knowledge.MaxDBSizeMB)
}

// 配置文件里显式写 mode: off（backend/configs/runtime*.yaml 的现状）必须与
// 默认值等价，确保“零行为变化”。
func TestKnowledgeConfigExplicitOffMatchesDefault(t *testing.T) {
	cfg := DefaultRuntimeConfig()
	require.NoError(t, yaml.Unmarshal([]byte("knowledge:\n  mode: off\n"), cfg))

	require.Equal(t, knowledge.ModeOff, cfg.Knowledge.Normalize().Mode)
	require.False(t, cfg.Knowledge.Enabled())
	require.NoError(t, ValidateRuntimeConfig(cfg))
}
