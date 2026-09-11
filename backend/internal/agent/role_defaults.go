package agent

import "strings"

// DefaultToolsForRole 返回子代理 role 的默认工具建议。
//
// 名字必须与运行时工具注册表（internal/tools）一致，否则父会话启用
// allowlist 时交集为空，子代理会拿到"允许 0 个工具"的执行策略而彻底失去
// 工具能力。契约测试 tool_vocabulary_contract_test.go 会校验这里的每个名字。
func DefaultToolsForRole(role string) []string {
	switch normalizeRoleName(role) {
	case "researcher", "web-researcher", "explorer", "scout":
		return []string{
			toolNameView,
			toolNameGrep,
			toolNameGlob,
			toolNameLs,
			toolNameShell,
			toolNameFetch,
			toolNameWebSearch,
		}
	case "tester", "test", "verifier", "verification":
		return []string{
			toolNameView,
			toolNameGrep,
			toolNameGlob,
			toolNameLs,
			toolNameShell,
		}
	case "writer", "implementer", "coder", "developer":
		return []string{
			toolNameView,
			toolNameGrep,
			toolNameGlob,
			toolNameLs,
			toolNameShell,
			toolNameWrite,
			toolNameEdit,
			toolNameMultiEdit,
			toolNameApplyPatch,
			toolNameAppendWrite,
		}
	default:
		return nil
	}
}

func normalizeRoleName(role string) string {
	lower := strings.ToLower(strings.TrimSpace(role))
	lower = strings.ReplaceAll(lower, "_", "-")
	return lower
}
