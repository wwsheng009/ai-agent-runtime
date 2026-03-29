package agent

// DefaultToolsForRole 返回子代理 role 的默认工具建议。
func DefaultToolsForRole(role string) []string {
	switch role {
	case "researcher", "web-researcher":
		return []string{"read_logs", "read_file", "grep_repo", "search_repo"}
	case "tester":
		return []string{"run_tests", "read_logs", "read_file"}
	case "verifier":
		return []string{"run_tests", "read_logs", "read_file", "git_log"}
	case "writer":
		return []string{"read_file", "git_log", "write_file", "edit_file", "apply_patch"}
	default:
		return nil
	}
}
