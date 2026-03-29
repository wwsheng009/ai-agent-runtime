package prompt

import "strings"

// Compose renders prompt sections into a single prompt body.
func Compose(loaded *LoadedFiles) string {
	if loaded == nil || !loaded.HasAny() {
		return ""
	}
	parts := make([]string, 0, 3)
	appendSection := func(title, body string) {
		body = strings.TrimSpace(body)
		if body == "" {
			return
		}
		parts = append(parts, "# "+title+"\n"+body)
	}
	appendSection("System", loaded.System)
	appendSection("Role", loaded.Role)
	appendSection("Tools", loaded.Tools)
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}
