package runtimeapi

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// TestMain 把默认分析库路径重定向到临时目录，避免测试通过
// RegisterRoutes → attachUsageAnalyticsService 写入用户数据目录 ~/.aicli。
// 显式调用 SetUsageAnalyticsDBPath 的用例不受影响。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "skills-usage-analytics-")
	if err == nil {
		_ = os.Setenv(usageanalytics.EnvDBPath, filepath.Join(dir, "usage_analytics.sqlite"))
	}
	code := m.Run()
	detachUsageAnalyticsService()
	if err == nil {
		_ = os.RemoveAll(dir)
	}
	os.Exit(code)
}
