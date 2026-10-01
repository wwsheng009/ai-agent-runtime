package config

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"gopkg.in/yaml.v3"
)

// ADR-0004 §4.4：knowledge.tools.* 的 YAML 段与加载期校验。
func TestKnowledgeToolsConfigParsesAndValidates(t *testing.T) {
	cfg := DefaultRuntimeConfig()
	require.NoError(t, yaml.Unmarshal([]byte(`
knowledge:
  mode: on
  tools:
    enabled: false
    stale_reader: off
`), cfg))
	require.False(t, cfg.Knowledge.Tools.ToolsEnabled())
	require.False(t, cfg.Knowledge.Tools.StaleReaderGradingEnabled())
	require.NoError(t, ValidateRuntimeConfig(cfg))

	bad := DefaultRuntimeConfig()
	bad.Knowledge.Mode = knowledge.ModeOn
	bad.Knowledge.Tools.StaleReader = "sometimes"
	require.Error(t, ValidateRuntimeConfig(bad))
}
