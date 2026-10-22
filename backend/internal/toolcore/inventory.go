package toolcore

// CoreTool describes one tool's placement in the v1 migration matrix
// (design §13.0). It is deliberately data-only so tests can reconcile it with
// the runtime registry without creating a second registry.
type CoreTool struct {
	Name         string
	Aliases      []string
	Capabilities []Capability
	Phase        int // 1 output, 2 read, 3 mutate, 4 shell, 5 CLI
	LocalOnly    bool
	ControlPlane bool
}

// V1CoreInventory is the frozen v1 tool classification.
func V1CoreInventory() []CoreTool {
	return []CoreTool{
		{Name: "view", Aliases: []string{"read"}, Capabilities: []Capability{CapReadFS}, Phase: 2},
		{Name: "ls", Capabilities: []Capability{CapReadFS}, Phase: 2},
		{Name: "glob", Capabilities: []Capability{CapReadFS}, Phase: 2},
		{Name: "grep", Aliases: []string{"rg"}, Capabilities: []Capability{CapReadFS}, Phase: 2},
		{Name: "write", Capabilities: []Capability{CapWriteFS}, Phase: 3},
		{Name: "edit", Capabilities: []Capability{CapWriteFS}, Phase: 3},
		{Name: "append_write", Capabilities: []Capability{CapWriteFS}, Phase: 3},
		{Name: "multiedit", Capabilities: []Capability{CapWriteFS}, Phase: 3},
		{Name: "apply_patch", Capabilities: []Capability{CapWriteFS}, Phase: 3},
		{Name: "shell", Aliases: []string{"bash", "execute_shell_command"}, Capabilities: []Capability{CapExecShell}, Phase: 4},
		{Name: "artifact_read", Capabilities: []Capability{CapArtifact}, Phase: 2},

		{Name: "aicli_exec", Capabilities: []Capability{CapExecShell}, LocalOnly: true},
		{Name: "fetch", Capabilities: []Capability{CapNetwork}, LocalOnly: true},
		{Name: "download", Capabilities: []Capability{CapNetwork, CapWriteFS}, LocalOnly: true},
		{Name: "web_search", Capabilities: []Capability{CapNetwork}, LocalOnly: true},
		{Name: "sourcegraph", Capabilities: []Capability{CapNetwork}, LocalOnly: true},
		{Name: "openai_image_generate", Aliases: []string{"image_generate"}, Capabilities: []Capability{CapImage}, LocalOnly: true},
		{Name: "code_search", Capabilities: []Capability{CapReadFS}, LocalOnly: true},
		{Name: "code_inspect", Capabilities: []Capability{CapReadFS}, LocalOnly: true},
		{Name: "code_navigate", Capabilities: []Capability{CapReadFS}, LocalOnly: true},
		{Name: "code_references", Capabilities: []Capability{CapReadFS}, LocalOnly: true},
		{Name: "code_callers", Capabilities: []Capability{CapReadFS}, LocalOnly: true},
		{Name: "todos", Capabilities: []Capability{CapStateful}, LocalOnly: true},
	}
}

// CoreToolNames returns only the tools that belong to the v1 core surface.
func CoreToolNames() []string {
	inventory := V1CoreInventory()
	names := make([]string, 0, len(inventory))
	for _, tool := range inventory {
		if !tool.LocalOnly && !tool.ControlPlane {
			names = append(names, tool.Name)
		}
	}
	return names
}
