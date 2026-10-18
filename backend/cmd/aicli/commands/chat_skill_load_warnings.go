package commands

import (
	"strings"

	runtimebootstrap "github.com/wwsheng009/ai-agent-runtime/internal/bootstrap"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// reportSkillLoadWarnings 把 bootstrap manager 收集的「非标准 skill 文件跳过」
// 告警投递到交互式会话的动态状态栏。非交互 / JSON / 启动早期没有登记出口时
// 只写文件日志，绝不回退 stderr——TUI 活动时裸写终端会覆盖底部保留区
// （§8.5 stderr/stdout 契约；2026-10-18 deskctl 非标准 SKILL.md 实例）。
func reportSkillLoadWarnings(manager *runtimebootstrap.Manager) {
	if manager == nil {
		return
	}
	for _, warning := range manager.TakeSkillLoadWarnings() {
		line := strings.TrimSpace(warning)
		if line == "" {
			continue
		}
		if !NotifyChatDiagnostic(line) {
			logpkg.Warnf("%s", line)
		}
	}
}
