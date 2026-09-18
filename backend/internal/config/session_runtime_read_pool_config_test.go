package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// P1.7/Q4：sessionRuntime.readPool 的 YAML 键与时长解析（加载器同款 yaml.v3 路径）。
func TestSessionRuntimeReadPoolConfigParsesYAML(t *testing.T) {
	require.False(t, DefaultRuntimeConfig().SessionRuntime.ReadPool.Disable,
		"默认必须启用读池（零值=false）")

	config := DefaultRuntimeConfig()
	require.NoError(t, yaml.Unmarshal([]byte(`
sessionRuntime:
  storePath: /tmp/session_runtime.sqlite
  readPool:
    disable: true
    size: 6
    busyTimeout: 7s
    operationTimeout: 4s
`), config))

	readPool := config.SessionRuntime.ReadPool
	require.True(t, readPool.Disable)
	require.Equal(t, 6, readPool.Size)
	require.Equal(t, 7*time.Second, readPool.BusyTimeout)
	require.Equal(t, 4*time.Second, readPool.OperationTimeout)
}
