package lsp

import "strings"

// ReasonCategory folds a free-form failure reason into a short, low-sensitivity
// enum. The raw reason can embed stderr text (paths, server chatter), so the
// observability plane publishes only the category; full text stays local in
// `lsp_servers` / logs.
func ReasonCategory(reason string) string {
	normalized := strings.ToLower(strings.TrimSpace(reason))
	switch {
	case normalized == "":
		return ""
	case strings.Contains(normalized, "restart budget"):
		return "restart_budget_exhausted"
	case strings.Contains(normalized, "not found"), strings.Contains(normalized, "command not found"):
		return "binary_missing"
	case strings.Contains(normalized, "still starting"):
		return "starting"
	case strings.Contains(normalized, "published nothing"):
		// The server never published anything for this path (file outside the
		// module, or in a toolchain-ignored directory). Distinct from a plain
		// wait timeout: a longer wait cannot fix it, so the tuning knobs and
		// the offline baseline must be able to tell the two apart.
		return "no_publish"
	case strings.Contains(normalized, "no fresh diagnostics"), strings.Contains(normalized, "wait budget"):
		return "wait_timeout"
	case strings.Contains(normalized, "timeout"):
		return "timeout"
	case strings.Contains(normalized, "transport closed"):
		return "transport_closed"
	case strings.Contains(normalized, "canceled"):
		return "canceled"
	case strings.Contains(normalized, "exit status"), strings.Contains(normalized, "exited unexpectedly"),
		strings.Contains(normalized, "crash"):
		return "crashed"
	case strings.Contains(normalized, "read file"):
		return "read_error"
	default:
		return "other"
	}
}
