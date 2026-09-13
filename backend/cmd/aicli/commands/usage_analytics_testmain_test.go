package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// TestMain 把默认分析库路径重定向到临时目录：本地 runtime host 未提供
// 落盘 runtime store 时（例如测试里手搓的 host），buildLocalUsageService 会回退到
// usageanalytics.DefaultDBPath()；若不重定向，测试会写入用户数据目录
// ~/.aicli/sessions/runtime/usage_analytics.sqlite。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "aicli-commands-usage-analytics-")
	if err == nil {
		_ = os.Setenv(usageanalytics.EnvDBPath, filepath.Join(dir, "usage_analytics.sqlite"))
	}
	code := m.Run()
	if err == nil {
		_ = os.RemoveAll(dir)
	}
	os.Exit(code)
}
