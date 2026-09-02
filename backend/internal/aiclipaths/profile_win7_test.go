//go:build win7compat

package aiclipaths

import "testing"

func TestWin7BuildProfileDefaults(t *testing.T) {
	if BuildProfile != "win7" {
		t.Fatalf("BuildProfile = %q, want win7", BuildProfile)
	}
	// win7compat 构建的用户可见配置文件与主构建完全统一（config.yaml /
	// aicli.yaml）；会话数据库也统一使用主库（session_history.sqlite），
	// 使前端能按工作目录分组展示用户的所有会话。
	assertBuildProfileDefaults(t, "config.yaml", "aicli.yaml", "runtime.win7.yaml", "session_history.sqlite")
}
