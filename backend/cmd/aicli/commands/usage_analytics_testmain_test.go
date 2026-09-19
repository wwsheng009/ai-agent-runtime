package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// TestMain 把默认分析库路径重定向到临时目录：本地 runtime host 未提供
// 落盘 runtime store 时（例如测试里手搓的 host），buildLocalUsageService 会回退到
// usageanalytics.DefaultDBPath()；若不重定向，测试会写入用户数据目录
// ~/.aicli/sessions/runtime/usage_analytics.sqlite。
//
// 同时把 MCP 配置的显式覆盖（-C，mcpConfigFile）固定到临时文件：MCP 解析会优先命中
// ./.aicli/mcp.yaml、~/.aicli/mcp.yaml 等真实路径，若不隔离，个别用例（或它触发的
// 热重载/规范化保存）会静默改写开发者本机的工作区配置；配合 internal/mcp/admin 的
// guardNonTempWriteInTests 形成双重保护。需要测真实路径解析的用例请显式设置并恢复该变量。
//
// 最后把 workspace 聊天偏好（$HOME/.aicli/workspace/<hash>/chat-prefs.yaml，
// decision D5）的 home 与 cwd 解析器固定到临时目录：任何未显式隔离的用例一旦走到
// 偏好持久化/读取路径，否则会读写真实用户 home。需要测真实解析的用例请用
// isolateWorkspacePrefsForTest 之外的显式恢复逻辑覆盖。
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "aicli-commands-usage-analytics-")
	if err == nil {
		_ = os.Setenv(usageanalytics.EnvDBPath, filepath.Join(dir, "usage_analytics.sqlite"))
	}
	mcpDir, mcpErr := os.MkdirTemp("", "aicli-commands-mcp-")
	if mcpErr == nil {
		mcpConfigFile = filepath.Join(mcpDir, "mcp.yaml")
	}
	// Workspace chat preferences (decision D5): disable resolution by default
	// so un-isolated tests observe an empty preference store and can never
	// read or write the real user home. Tests that exercise the persistence
	// semantics call isolateWorkspacePrefsForTest to pin home+cwd to a tempdir.
	prevCwd := agentconfig.WorkspaceCwdForTest()
	agentconfig.SetWorkspaceCwdForTest(func() (string, error) {
		return "", errWorkspacePrefsDisabledInTests
	})
	defer agentconfig.SetWorkspaceCwdForTest(prevCwd)
	code := m.Run()
	if err == nil {
		_ = os.RemoveAll(dir)
	}
	if mcpErr == nil {
		_ = os.RemoveAll(mcpDir)
	}
	os.Exit(code)
}
